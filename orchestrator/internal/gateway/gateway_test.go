package gateway_test

import (
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

	if nats.SubscribeCallCount != 3 {
		t.Fatalf("SubscribeCallCount = %d, want 3 (tasks.inbound, status.events, capability.response)", nats.SubscribeCallCount)
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
	gw.RegisterTaskHandler(func(task types.UserTask) error {
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
	if envelope.MessageType != "task_spec" {
		t.Fatalf("envelope.MessageType = %q, want task_spec", envelope.MessageType)
	}

	// Verify policy_scope is preserved in the payload
	var decodedSpec types.TaskSpec
	if err := json.Unmarshal(envelope.Payload, &decodedSpec); err != nil {
		t.Fatalf("decode task_spec payload error = %v", err)
	}
	if decodedSpec.PolicyScope.TokenRef != "tok-1" {
		t.Fatalf("decodedSpec.PolicyScope.TokenRef = %q, want tok-1", decodedSpec.PolicyScope.TokenRef)
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
		resp := types.CapabilityResponse{
			OrchestratorTaskRef: "orch-1",
			Match:               types.CapabilityMatch_Match,
			AgentID:             "agent-42",
		}
		data := newEnvelopedMessage(t, "capability_response", "orch-1", resp)
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
	if result.AgentID != "agent-42" {
		t.Fatalf("result.AgentID = %q, want agent-42", result.AgentID)
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
