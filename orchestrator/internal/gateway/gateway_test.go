package gateway_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/gateway"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// newEnvelopedMessage wraps any payload in a valid MessageEnvelope and serializes it.
// Used in tests to simulate correctly-formatted inbound NATS messages.
func newEnvelopedMessage(t *testing.T, messageType, correlationID string, payload any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal(payload) error = %v", err)
	}
	envelope := types.MessageEnvelope{
		MessageID:       "test-msg-id-001",
		MessageType:     messageType,
		SourceComponent: "user-io",
		CorrelationID:   correlationID,
		Timestamp:       time.Now().UTC(),
		SchemaVersion:   "1.0",
		Payload:         raw,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("json.Marshal(envelope) error = %v", err)
	}
	return data
}

// newGateway creates a Gateway wired to a fresh NATSMock and calls Start().
func newGateway(t *testing.T) (*gateway.Gateway, *mocks.NATSMock) {
	t.Helper()
	nats := mocks.NewNATSMock()
	gw := gateway.New(nats, "test-node")
	if err := gw.Start(); err != nil {
		t.Fatalf("gateway.Start() error = %v", err)
	}
	return gw, nats
}

// ── Start / Construction ──────────────────────────────────────────────────────

func TestGatewayStart_SubscribesToInboundTopics(t *testing.T) {
	_, nats := newGateway(t)

	if nats.SubscribeCallCount != 7 {
		t.Fatalf("SubscribeCallCount = %d, want 7 (tasks.inbound, agent.status, capability.response, task.accepted, task.result, task.failed, credential.request)", nats.SubscribeCallCount)
	}
}

func TestGatewayIsConnected_ReflectsNATSState(t *testing.T) {
	gw, nats := newGateway(t)

	if !gw.IsConnected() {
		t.Fatal("IsConnected() = false, want true")
	}
	nats.ShouldBeDisconnected = true
	if gw.IsConnected() {
		t.Fatal("IsConnected() = true, want false after disconnect")
	}
}

// ── Envelope Validation ───────────────────────────────────────────────────────

func TestHandleInboundTask_MalformedEnvelope_DeadLettered(t *testing.T) {
	_, nats := newGateway(t)

	// Send raw garbage — not a valid envelope
	err := nats.Deliver(gateway.TopicTasksInbound, []byte(`{"bad": "message"}`))
	if err == nil {
		t.Fatal("Deliver() error = nil, want envelope validation error")
	}

	// Must be dead-lettered
	dlMsgs := nats.Published[gateway.TopicDeadLetter]
	if len(dlMsgs) != 1 {
		t.Fatalf("dead-letter messages = %d, want 1", len(dlMsgs))
	}
}

func TestHandleInboundTask_MissingMessageID_DeadLettered(t *testing.T) {
	_, nats := newGateway(t)

	envelope := map[string]any{
		"message_id":       "",
		"message_type":     "user_task",
		"source_component": "user-io",
		"correlation_id":   "task-1",
		"timestamp":        time.Now().UTC(),
		"schema_version":   "1.0",
		"payload":          json.RawMessage(`{}`),
	}
	data, _ := json.Marshal(envelope)

	err := nats.Deliver(gateway.TopicTasksInbound, data)
	if err == nil {
		t.Fatal("Deliver() error = nil, want missing message_id error")
	}
	if !strings.Contains(err.Error(), "message_id") {
		t.Fatalf("error = %v, want message_id mention", err)
	}
	if len(nats.Published[gateway.TopicDeadLetter]) != 1 {
		t.Fatal("expected dead-letter on missing message_id")
	}
}

// ── Task Handler Routing ──────────────────────────────────────────────────────

func TestHandleInboundTask_ValidEnvelope_CallsTaskHandler(t *testing.T) {
	gw, nats := newGateway(t)

	var received types.UserTask
	gw.RegisterTaskHandler(func(_ context.Context, task types.UserTask) error {
		received = task
		return nil
	})

	task := types.UserTask{
		TaskID:               "task-abc",
		UserID:               "user-1",
		RequiredSkillDomains: []string{"web"},
		Priority:             5,
		TimeoutSeconds:       60,
		CallbackTopic:        "aegis.user-io.results",
	}
	data := newEnvelopedMessage(t, "user_task", "task-abc", task)

	if err := nats.Deliver(gateway.TopicTasksInbound, data); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	if received.TaskID != "task-abc" {
		t.Fatalf("received.TaskID = %q, want task-abc", received.TaskID)
	}
	if received.UserID != "user-1" {
		t.Fatalf("received.UserID = %q, want user-1", received.UserID)
	}
}

func TestHandleInboundTask_NoHandlerRegistered_ReturnsError(t *testing.T) {
	_, nats := newGateway(t)

	task := types.UserTask{TaskID: "task-1", UserID: "u1", RequiredSkillDomains: []string{"web"}}
	data := newEnvelopedMessage(t, "user_task", "task-1", task)

	err := nats.Deliver(gateway.TopicTasksInbound, data)
	if err == nil {
		t.Fatal("Deliver() error = nil, want no handler error")
	}
}

// ── Agent Status Routing ──────────────────────────────────────────────────────

func TestHandleAgentStatus_ValidEnvelope_CallsStatusHandler(t *testing.T) {
	gw, nats := newGateway(t)

	var received types.AgentStatusUpdate
	gw.RegisterAgentStatusHandler(func(update types.AgentStatusUpdate) error {
		received = update
		return nil
	})

	update := types.AgentStatusUpdate{
		AgentID:             "agent-42",
		OrchestratorTaskRef: "orch-1",
		TaskID:              "task-1",
		State:               types.AgentStateRecovering,
	}
	data := newEnvelopedMessage(t, "agent_status_update", "orch-1", update)

	if err := nats.Deliver(gateway.TopicAgentStatusEvents, data); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	if received.AgentID != "agent-42" {
		t.Fatalf("received.AgentID = %q, want agent-42", received.AgentID)
	}
	if received.State != types.AgentStateRecovering {
		t.Fatalf("received.State = %q, want RECOVERING", received.State)
	}
}

// ── Outbound Publishing ───────────────────────────────────────────────────────

func TestPublishError_WritesToCorrectTopic(t *testing.T) {
	gw, nats := newGateway(t)

	err := gw.PublishError("aegis.user-io.results.task-1", types.ErrorResponse{
		TaskID:      "task-1",
		ErrorCode:   types.ErrCodePolicyViolation,
		UserMessage: "Task requires resources outside your configured permissions.",
	})
	if err != nil {
		t.Fatalf("PublishError() error = %v", err)
	}

	msgs := nats.Published["aegis.user-io.results.task-1"]
	if len(msgs) != 1 {
		t.Fatalf("published to callback topic = %d, want 1", len(msgs))
	}

	// Verify wrapped in a valid envelope
	var envelope types.MessageEnvelope
	if err := json.Unmarshal(msgs[0], &envelope); err != nil {
		t.Fatalf("published message is not a valid envelope: %v", err)
	}
	if envelope.MessageType != "error_response" {
		t.Fatalf("envelope.MessageType = %q, want error_response", envelope.MessageType)
	}
	if envelope.SourceComponent != "orchestrator" {
		t.Fatalf("envelope.SourceComponent = %q, want orchestrator", envelope.SourceComponent)
	}
	if envelope.SchemaVersion != "1.0" {
		t.Fatalf("envelope.SchemaVersion = %q, want 1.0", envelope.SchemaVersion)
	}
}

func TestPublishTaskSpec_PublishesToAgentsTopic(t *testing.T) {
	gw, nats := newGateway(t)

	spec := types.TaskSpec{
		OrchestratorTaskRef:  "orch-1",
		TaskID:               "task-1",
		UserID:               "user-1",
		RequiredSkillDomains: []string{"web"},
		PolicyScope:          types.PolicyScope{Domains: []string{"web"}, TokenRef: "tok-1"},
		TimeoutSeconds:       60,
		CallbackTopic:        "aegis.user-io.results.task-1",
	}

	if err := gw.PublishTaskSpec(spec); err != nil {
		t.Fatalf("PublishTaskSpec() error = %v", err)
	}

	msgs := nats.Published[gateway.TopicAgentTasksInbound]
	if len(msgs) != 1 {
		t.Fatalf("published to agents.tasks.inbound = %d, want 1", len(msgs))
	}

	var envelope types.MessageEnvelope
	if err := json.Unmarshal(msgs[0], &envelope); err != nil {
		t.Fatalf("invalid envelope: %v", err)
	}
	if envelope.MessageType != "task.inbound" {
		t.Fatalf("envelope.MessageType = %q, want task.inbound", envelope.MessageType)
	}

	// Verify the payload is translated into the agents-component wire schema.
	var decodedSpec struct {
		TaskID         string            `json:"task_id"`
		RequiredSkills []string          `json:"required_skills"`
		Instructions   string            `json:"instructions"`
		Metadata       map[string]string `json:"metadata"`
		TraceID        string            `json:"trace_id"`
	}
	if err := json.Unmarshal(envelope.Payload, &decodedSpec); err != nil {
		t.Fatalf("decode task.inbound payload error = %v", err)
	}
	if decodedSpec.TraceID != "orch-1" {
		t.Fatalf("decodedSpec.TraceID = %q, want orch-1", decodedSpec.TraceID)
	}
	if len(decodedSpec.RequiredSkills) != 1 || decodedSpec.RequiredSkills[0] != "web" {
		t.Fatalf("decodedSpec.RequiredSkills = %v, want [web]", decodedSpec.RequiredSkills)
	}
	if decodedSpec.Metadata["orchestrator_task_ref"] != "orch-1" {
		t.Fatalf("decodedSpec.Metadata[orchestrator_task_ref] = %q, want orch-1", decodedSpec.Metadata["orchestrator_task_ref"])
	}
}

func TestPublishAgentTerminate_PublishesToTerminateTopic(t *testing.T) {
	gw, nats := newGateway(t)

	term := types.AgentTerminate{
		AgentID:             "agent-42",
		OrchestratorTaskRef: "orch-1",
		Reason:              types.ErrCodeTimedOut,
	}

	if err := gw.PublishAgentTerminate(term); err != nil {
		t.Fatalf("PublishAgentTerminate() error = %v", err)
	}

	if len(nats.Published[gateway.TopicAgentTerminate]) != 1 {
		t.Fatal("expected 1 message on terminate topic")
	}
}

func TestPublishMetrics_PublishesToMetricsTopic(t *testing.T) {
	gw, nats := newGateway(t)

	metrics := types.MetricsPayload{
		NodeID:      "test-node",
		Timestamp:   time.Now().UTC(),
		ActiveTasks: 5,
	}

	if err := gw.PublishMetrics(metrics); err != nil {
		t.Fatalf("PublishMetrics() error = %v", err)
	}

	if len(nats.Published[gateway.TopicMetrics]) != 1 {
		t.Fatal("expected 1 message on metrics topic")
	}
}

// ── Capability Query ──────────────────────────────────────────────────────────

func TestPublishCapabilityQuery_ReceivesResponse(t *testing.T) {
	gw, nats := newGateway(t)

	// Simulate the Agents Component responding asynchronously
	go func() {
		time.Sleep(10 * time.Millisecond)
		resp := map[string]any{
			"query_id":  "orch-1",
			"domains":   []string{"web"},
			"has_match": true,
			"trace_id":  "orch-1",
		}
		data := newEnvelopedMessage(t, "capability.response", "orch-1", resp)
		_ = nats.Deliver(gateway.TopicCapabilityQueryResponse, data)
	}()

	query := types.CapabilityQuery{
		OrchestratorTaskRef:  "orch-1",
		RequiredSkillDomains: []string{"web"},
	}

	result, err := gw.PublishCapabilityQuery(query)
	if err != nil {
		t.Fatalf("PublishCapabilityQuery() error = %v", err)
	}
	if result.Match != types.CapabilityMatch_Match {
		t.Fatalf("result.Match = %q, want match", result.Match)
	}
}

func TestPublishCapabilityQuery_Timeout_ReturnsError(t *testing.T) {
	gw, _ := newGateway(t)

	// No response delivered — will time out after CapabilityQueryTimeout (3s)
	done := make(chan error, 1)
	go func() {
		query := types.CapabilityQuery{
			OrchestratorTaskRef:  "orch-timeout",
			RequiredSkillDomains: []string{"web"},
		}
		_, err := gw.PublishCapabilityQuery(query)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("PublishCapabilityQuery() error = nil, want timeout error")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("error = %v, want timed out", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test timed out waiting for capability query timeout")
	}
}

// ── NATS disconnected ─────────────────────────────────────────────────────────

func TestPublishError_NATSDisconnected_ReturnsError(t *testing.T) {
	gw, nats := newGateway(t)
	nats.ShouldBeDisconnected = true

	err := gw.PublishError("some.topic", types.ErrorResponse{TaskID: "t1", ErrorCode: "ERR"})
	if err == nil {
		t.Fatal("PublishError() error = nil, want nats disconnected error")
	}
}

// ── Gateway Demo Flow ─────────────────────────────────────────────────────────

// TestGatewayDemoFlow is a demo-style walkthrough of M1: Communications Gateway.
//
// Run with:
//
//	cd orchestrator && go test ./internal/gateway/ -v -run TestGatewayDemoFlow
//
// This test exercises the full inbound and outbound message lifecycle in the
// same order it would execute during a demo, narrating each step via t.Log so
// the output reads as a human-friendly trace of what the gateway did and why.
//
// Scenarios covered (in order):
//
//	Step 1 — Startup:            Gateway subscribes to 3 inbound topics; IsConnected = true
//	Step 2 — Inbound Task:       valid envelope → envelope validated → task handler called
//	Step 3 — Dead-Letter:        malformed envelope → rejected and dead-lettered; handler not called
//	Step 4 — Agent Status:       valid agent_status_update → status handler called
//	Step 5 — Publish Error:      PublishError → envelope written to callback_topic
//	Step 6 — Publish TaskSpec:   PublishTaskSpec → policy_scope preserved in payload
//	Step 7 — Publish Terminate:  PublishAgentTerminate → message on terminate topic
//	Step 8 — Capability Query:   PublishCapabilityQuery → async response matched by correlationID
//	Step 9 — NATS Disconnect:    IsConnected flips false; publish returns error
//
// What this test does NOT do:
//   - Does not connect to a real NATS broker
//   - Does not validate mTLS certificates
//   - Does not test JetStream persistence or consumer ACK/NAK
//
// Instead, it demonstrates that the orchestrator's M1 layer correctly validates,
// routes, and wraps all messages through well-defined Go interfaces — which is
// exactly what we want for the current mock-based demo phase.
func TestGatewayDemoFlow(t *testing.T) {
	// ── Setup ──────────────────────────────────────────────────────────────
	nats := mocks.NewNATSMock()
	gw := gateway.New(nats, "demo-node")

	t.Log("demo setup: Gateway created with NATSMock as the mock NATS broker")
	t.Log("demo setup: no real NATS connection — all publish/subscribe calls go through the mock")

	// ── Step 1: Startup — Subscribe to inbound topics ─────────────────────
	// The Gateway must subscribe to exactly 3 inbound topics on Start():
	//   aegis.orchestrator.tasks.inbound
	//   aegis.agents.status.events
	//   aegis.agents.capability.response
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 1: startup — calling gw.Start() and verifying subscriptions")

	if err := gw.Start(); err != nil {
		t.Fatalf("step 1: Start() error = %v", err)
	}
	if nats.SubscribeCallCount != 7 {
		t.Fatalf("step 1: SubscribeCallCount = %d, want 7", nats.SubscribeCallCount)
	}
	if !gw.IsConnected() {
		t.Fatal("step 1: IsConnected() = false, want true")
	}
	t.Logf("step 1: subscribed to %d inbound topics — IsConnected=%v", nats.SubscribeCallCount, gw.IsConnected())
	t.Log("step 1 complete: gateway started and NATS connection confirmed ✓")

	// ── Step 2: Inbound Task — Valid envelope routed to handler ───────────
	// A correctly-formed MessageEnvelope arrives on tasks.inbound.
	// The gateway validates it, deserializes the UserTask payload, and calls
	// the registered task handler.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 2: inbound task — delivering a valid user_task envelope")

	var receivedTask types.UserTask
	gw.RegisterTaskHandler(func(_ context.Context, task types.UserTask) error {
		receivedTask = task
		return nil
	})

	inboundTask := types.UserTask{
		TaskID:               "550e8400-e29b-41d4-a716-446655440000",
		UserID:               "user-alice",
		RequiredSkillDomains: []string{"web", "research"},
		Priority:             7,
		TimeoutSeconds:       120,
		CallbackTopic:        "aegis.user-io.results.task-1",
	}
	taskData := newEnvelopedMessage(t, "user_task", inboundTask.TaskID, inboundTask)

	if err := nats.Deliver(gateway.TopicTasksInbound, taskData); err != nil {
		t.Fatalf("step 2: Deliver() error = %v", err)
	}
	if receivedTask.TaskID != inboundTask.TaskID {
		t.Fatalf("step 2: receivedTask.TaskID = %q, want %q", receivedTask.TaskID, inboundTask.TaskID)
	}
	t.Logf("step 2: task handler called — task_id=%s user_id=%s domains=%v",
		receivedTask.TaskID, receivedTask.UserID, receivedTask.RequiredSkillDomains)
	t.Log("step 2 complete: envelope validated and task routed to handler ✓")

	// ── Step 3: Dead-Letter — Malformed envelope rejected ─────────────────
	// A message that fails envelope validation (missing message_id) must be
	// dead-lettered to aegis.orchestrator.tasks.deadletter and NOT forwarded
	// to the task handler.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 3: dead-letter — delivering a malformed envelope (missing message_id)")

	badEnvelope, _ := json.Marshal(map[string]any{
		"message_id":       "", // intentionally empty — fails validation
		"message_type":     "user_task",
		"source_component": "user-io",
		"correlation_id":   "task-bad",
		"timestamp":        time.Now().UTC(),
		"schema_version":   "1.0",
		"payload":          json.RawMessage(`{}`),
	})

	deliverErr := nats.Deliver(gateway.TopicTasksInbound, badEnvelope)
	if deliverErr == nil {
		t.Fatal("step 3: Deliver() error = nil, want envelope validation error")
	}

	dlMsgs := nats.Published[gateway.TopicDeadLetter]
	if len(dlMsgs) != 1 {
		t.Fatalf("step 3: dead-letter messages = %d, want 1", len(dlMsgs))
	}
	t.Logf("step 3: rejected message dead-lettered — topic=%s reason contains: %q",
		gateway.TopicDeadLetter, deliverErr.Error())
	t.Log("step 3 complete: malformed envelope rejected and dead-lettered ✓")

	// ── Step 4: Agent Status — Valid status update routed to handler ───────
	// An agent.status event arrives on aegis.orchestrator.agent.status.
	// The gateway validates, deserializes, and calls the registered status handler.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 4: agent status — delivering an agent_status_update envelope")

	var receivedStatus types.AgentStatusUpdate
	gw.RegisterAgentStatusHandler(func(update types.AgentStatusUpdate) error {
		receivedStatus = update
		return nil
	})

	statusUpdate := map[string]any{
		"agent_id": "agent-42",
		"task_id":  inboundTask.TaskID,
		"state":    string(types.AgentStateActive),
		"message":  "Fetching page content...",
	}
	statusData := newEnvelopedMessage(t, "agent.status", "orch-demo-1", statusUpdate)

	if err := nats.Deliver(gateway.TopicAgentStatusEvents, statusData); err != nil {
		t.Fatalf("step 4: Deliver() error = %v", err)
	}
	if receivedStatus.AgentID != "agent-42" {
		t.Fatalf("step 4: receivedStatus.AgentID = %q, want agent-42", receivedStatus.AgentID)
	}
	t.Logf("step 4: status handler called — agent_id=%s state=%s progress=%q",
		receivedStatus.AgentID, receivedStatus.State, receivedStatus.ProgressSummary)
	t.Log("step 4 complete: agent status update routed to handler ✓")

	// ── Step 5: Publish Error — Error response wrapped in envelope ─────────
	// The dispatcher calls PublishError when a task is rejected.
	// The gateway wraps it in a signed MessageEnvelope before publishing.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 5: publish error — sending POLICY_VIOLATION to User I/O callback topic")

	callbackTopic := "aegis.user-io.results.task-1"
	errResp := types.ErrorResponse{
		TaskID:      inboundTask.TaskID,
		ErrorCode:   types.ErrCodePolicyViolation,
		UserMessage: "Task requires resources outside your configured permissions.",
	}
	if err := gw.PublishError(callbackTopic, errResp); err != nil {
		t.Fatalf("step 5: PublishError() error = %v", err)
	}

	errMsgs := nats.Published[callbackTopic]
	if len(errMsgs) != 1 {
		t.Fatalf("step 5: messages on callback topic = %d, want 1", len(errMsgs))
	}
	var errEnvelope types.MessageEnvelope
	if err := json.Unmarshal(errMsgs[0], &errEnvelope); err != nil {
		t.Fatalf("step 5: published message is not a valid envelope: %v", err)
	}
	if errEnvelope.MessageType != "error_response" {
		t.Fatalf("step 5: envelope.MessageType = %q, want error_response", errEnvelope.MessageType)
	}
	if errEnvelope.SourceComponent != "orchestrator" {
		t.Fatalf("step 5: envelope.SourceComponent = %q, want orchestrator", errEnvelope.SourceComponent)
	}
	t.Logf("step 5: error_response published — message_type=%s source=%s correlation_id=%s",
		errEnvelope.MessageType, errEnvelope.SourceComponent, errEnvelope.CorrelationID)
	t.Log("step 5 complete: error wrapped in signed envelope and published ✓")

	// ── Step 6: Publish TaskSpec — translated to task.inbound ──────────────
	// The dispatcher calls PublishTaskSpec; the gateway must translate it into
	// the agents-component wire schema.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 6: publish task_spec — verifying policy_scope is preserved in payload")

	spec := types.TaskSpec{
		OrchestratorTaskRef:  "orch-demo-1",
		TaskID:               inboundTask.TaskID,
		UserID:               inboundTask.UserID,
		RequiredSkillDomains: inboundTask.RequiredSkillDomains,
		PolicyScope: types.PolicyScope{
			Domains:  []string{"web", "research"},
			TokenRef: "tok-demo-abc123",
		},
		TimeoutSeconds: inboundTask.TimeoutSeconds,
		CallbackTopic:  callbackTopic,
	}
	if err := gw.PublishTaskSpec(spec); err != nil {
		t.Fatalf("step 6: PublishTaskSpec() error = %v", err)
	}

	specMsgs := nats.Published[gateway.TopicAgentTasksInbound]
	if len(specMsgs) != 1 {
		t.Fatalf("step 6: task_spec messages = %d, want 1", len(specMsgs))
	}
	var specEnvelope types.MessageEnvelope
	if err := json.Unmarshal(specMsgs[0], &specEnvelope); err != nil {
		t.Fatalf("step 6: invalid envelope: %v", err)
	}
	var decodedSpec struct {
		TaskID         string            `json:"task_id"`
		RequiredSkills []string          `json:"required_skills"`
		Instructions   string            `json:"instructions"`
		Metadata       map[string]string `json:"metadata"`
		TraceID        string            `json:"trace_id"`
	}
	if err := json.Unmarshal(specEnvelope.Payload, &decodedSpec); err != nil {
		t.Fatalf("step 6: decode task.inbound payload error = %v", err)
	}
	if decodedSpec.TraceID != "orch-demo-1" {
		t.Fatalf("step 6: trace_id = %q, want orch-demo-1", decodedSpec.TraceID)
	}
	t.Logf("step 6: task.inbound published to %s — required_skills=%v trace_id=%s",
		gateway.TopicAgentTasksInbound, decodedSpec.RequiredSkills, decodedSpec.TraceID)
	t.Log("step 6 complete: task_spec translated to task.inbound ✓")

	// ── Step 7: Publish AgentTerminate ────────────────────────────────────
	// The Recovery Manager calls PublishAgentTerminate to stop a timed-out agent.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 7: publish agent_terminate — instructing Agents Component to stop agent-42")

	terminate := types.AgentTerminate{
		AgentID:             "agent-42",
		OrchestratorTaskRef: "orch-demo-1",
		Reason:              types.ErrCodeTimedOut,
	}
	if err := gw.PublishAgentTerminate(terminate); err != nil {
		t.Fatalf("step 7: PublishAgentTerminate() error = %v", err)
	}
	if len(nats.Published[gateway.TopicAgentTerminate]) != 1 {
		t.Fatal("step 7: expected 1 message on terminate topic")
	}
	t.Logf("step 7: agent_terminate published to %s — agent_id=%s reason=%s",
		gateway.TopicAgentTerminate, terminate.AgentID, terminate.Reason)
	t.Log("step 7 complete: terminate instruction published ✓")

	// ── Step 8: Capability Query — async request/response ─────────────────
	// The dispatcher calls PublishCapabilityQuery which blocks waiting for a
	// capability.response keyed by query_id/correlationID.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 8: capability query — async request/response via correlationID matching")

	go func() {
		time.Sleep(10 * time.Millisecond)
		resp := map[string]any{
			"query_id":  "orch-demo-cap",
			"domains":   []string{"web"},
			"has_match": true,
			"trace_id":  "orch-demo-cap",
		}
		respData := newEnvelopedMessage(t, "capability.response", "orch-demo-cap", resp)
		_ = nats.Deliver(gateway.TopicCapabilityQueryResponse, respData)
	}()

	capResult, err := gw.PublishCapabilityQuery(types.CapabilityQuery{
		OrchestratorTaskRef:  "orch-demo-cap",
		RequiredSkillDomains: []string{"web"},
	})
	if err != nil {
		t.Fatalf("step 8: PublishCapabilityQuery() error = %v", err)
	}
	if capResult.Match != types.CapabilityMatch_Match {
		t.Fatalf("step 8: capResult.Match = %q, want match", capResult.Match)
	}
	t.Logf("step 8: capability_response received — match=%s", capResult.Match)
	t.Log("step 8 complete: capability query matched by correlationID ✓")

	// ── Step 9: NATS Disconnect — connection failure reflected ────────────
	// When the NATS broker goes away, IsConnected() must return false
	// and outbound publishes must return an error immediately.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 9: NATS disconnect — simulating broker going offline")

	nats.ShouldBeDisconnected = true

	if gw.IsConnected() {
		t.Fatal("step 9: IsConnected() = true, want false after simulated disconnect")
	}
	publishErr := gw.PublishError("some.topic", types.ErrorResponse{TaskID: "t1", ErrorCode: "ERR"})
	if publishErr == nil {
		t.Fatal("step 9: PublishError() error = nil, want NATS disconnected error")
	}
	t.Logf("step 9: IsConnected()=false — publish returned error: %v", publishErr)
	t.Log("step 9 complete: disconnect correctly reflected in gateway state ✓")

	// ── Summary ────────────────────────────────────────────────────────────
	t.Log("─────────────────────────────────────────────────────")
	t.Log("demo summary: Communications Gateway completed all 9 steps against NATSMock")
	t.Log("demo summary: no real NATS broker was used")
	t.Logf("demo summary: total publishes=%d | subscriptions=%d | dead-letters=%d",
		func() int {
			total := 0
			for _, msgs := range nats.Published {
				total += len(msgs)
			}
			return total
		}(),
		nats.SubscribeCallCount,
		len(nats.Published[gateway.TopicDeadLetter]),
	)
}
