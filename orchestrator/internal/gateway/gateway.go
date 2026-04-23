// Package gateway implements M1: Communications Gateway.
//
// The Communications Gateway is the single inbound/outbound gateway for all
// NATS messaging. It is the ONLY module that publishes to or subscribes from
// NATS topics. All other modules communicate via internal Go method calls.
//
// Responsibilities (§4.1 M1):
//   - Validate message envelope schema on all inbound messages (§13.5)
//   - Route parsed user_task to Task Dispatcher
//   - Route agent_status_update events to Task Monitor
//   - Route terminal agent task outcomes to the Dispatcher / Plan Executor
//   - Publish all outbound messages (results, errors, status, metrics)
//   - Manage NATS consumer ACK/NAK and dead-letter monitoring
//
// NATS Topic Hierarchy (§11.8):
//   - INBOUND:  aegis.orchestrator.tasks.inbound
//   - INBOUND:  aegis.orchestrator.agent.status
//   - INBOUND:  aegis.orchestrator.capability.response  (reply to capability queries)
//   - INBOUND:  aegis.orchestrator.task.accepted
//   - INBOUND:  aegis.orchestrator.task.result
//   - INBOUND:  aegis.orchestrator.task.failed
//   - OUTBOUND: aegis.orchestrator.status.events
//   - OUTBOUND: aegis.orchestrator.errors
//   - OUTBOUND: aegis.orchestrator.audit.events
//   - OUTBOUND: aegis.orchestrator.metrics
//   - OUTBOUND: aegis.orchestrator.tasks.deadletter
//   - OUTBOUND: aegis.agents.task.inbound
//   - OUTBOUND: aegis.agents.capability.query
//   - OUTBOUND: aegis.agents.lifecycle.terminate
//   - OUTBOUND: aegis.agents.tasks.cancel
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// NATS topic constants.
const (
	TopicTasksInbound            = "aegis.orchestrator.tasks.inbound"
	TopicAgentStatusEvents       = "aegis.orchestrator.agent.status"
	TopicCapabilityQueryResponse = "aegis.orchestrator.capability.response"
	TopicTaskAccepted            = "aegis.orchestrator.task.accepted"
	TopicTaskResult              = "aegis.orchestrator.task.result"
	TopicTaskFailed              = "aegis.orchestrator.task.failed"
	TopicCredentialRequest       = "aegis.orchestrator.credential.request"
	TopicPlanDecision            = "aegis.orchestrator.plan.decision"
	TopicAgentStateWrite         = "aegis.orchestrator.state.write"
	TopicAgentStateReadRequest   = "aegis.orchestrator.state.read.request"
	TopicAgentTasksInbound       = "aegis.agents.task.inbound"
	TopicCapabilityQuery         = "aegis.agents.capability.query"
	TopicAgentTerminate          = "aegis.agents.lifecycle.terminate"
	TopicTaskCancel              = "aegis.agents.tasks.cancel"
	TopicAgentStateWriteAck      = "aegis.agents.state.write.ack"
	TopicAgentStateReadResponse  = "aegis.agents.state.read.response"
	TopicOrchestratorErrors      = "aegis.orchestrator.errors"
	TopicAuditEvents             = "aegis.orchestrator.audit.events"
	TopicMetrics                 = "aegis.orchestrator.metrics"
	TopicDeadLetter              = "aegis.orchestrator.tasks.deadletter"
	TopicStatusEvents            = "aegis.orchestrator.status.events"
)

// SchemaVersion is the current message envelope schema version (§13.5).
const SchemaVersion = "1.0"

// SourceComponent is the source_component field value for all outbound messages.
const SourceComponent = "orchestrator"

// CapabilityQueryTimeout is how long PublishCapabilityQuery waits for a response.
// Must respond within 500ms p99 (§FR-ALC-04). Set to 3s to allow for retries.
const CapabilityQueryTimeout = 3 * time.Second

// ── Handler Types ─────────────────────────────────────────────────────────────

// TaskHandler is the callback the Task Dispatcher registers to receive parsed inbound tasks.
// ctx carries the trace_id and task_id extracted/generated at the gateway entry point.
type TaskHandler func(ctx context.Context, task types.UserTask) error

// AgentStatusHandler is the callback the Task Monitor registers to receive agent status updates.
type AgentStatusHandler func(ctx context.Context, update types.AgentStatusUpdate) error

// TaskResultHandler is the callback the Plan Executor registers to receive task results.
// ctx carries the trace_id extracted from the inbound message envelope.
type TaskResultHandler func(ctx context.Context, result types.TaskResult) error

// CredentialRequestHandler is called when an agent publishes a credential.request
// that requires user input (operation: "user_input"). Registered by main.go to
// forward the request to the IO Component.
type CredentialRequestHandler func(agentID, taskID, requestID, keyName, label string) error

// PlanDecisionHandler is called when User I/O forwards the user's approve/reject
// decision for a proposed execution plan. Registered by main.go → Dispatcher.
type PlanDecisionHandler func(ctx context.Context, decision types.PlanDecision) error

// ── Gateway ───────────────────────────────────────────────────────────────────

// Gateway is M1: Communications Gateway.
type Gateway struct {
	nats   interfaces.NATSClient
	nodeID string

	taskHandler              TaskHandler
	agentStatusHandler       AgentStatusHandler
	taskResultHandler        TaskResultHandler
	credentialRequestHandler CredentialRequestHandler
	planDecisionHandler      PlanDecisionHandler

	// pendingCapabilityQueries tracks in-flight capability query requests.
	// key: query_id, value: chan *types.CapabilityResponse
	pendingCapabilityQueries sync.Map

	// agentStore is an in-process agent memory store used to bridge
	// aegis.orchestrator.state.write and aegis.orchestrator.state.read.request
	// until the full Memory Component integration is wired up.
	// key: agentID, value: slice of raw MemoryWrite JSON objects.
	agentStore   map[string][]json.RawMessage
	agentStoreMu sync.RWMutex
}

// New creates a new Gateway. Call RegisterHandlers then Start() before use.
func New(nats interfaces.NATSClient, nodeID string) *Gateway {
	return &Gateway{
		nats:       nats,
		nodeID:     nodeID,
		agentStore: make(map[string][]json.RawMessage),
	}
}

// RegisterTaskHandler registers the callback for inbound user_task messages.
// Must be called before Start(). Registered by Task Dispatcher.
func (g *Gateway) RegisterTaskHandler(h TaskHandler) {
	g.taskHandler = h
}

// RegisterAgentStatusHandler registers the callback for agent_status_update messages.
// Must be called before Start(). Registered by Task Monitor.
func (g *Gateway) RegisterAgentStatusHandler(h AgentStatusHandler) {
	g.agentStatusHandler = h
}

// RegisterTaskResultHandler registers the callback for terminal agent task
// outcomes. Must be called before Start(). Registered by the Dispatcher.
func (g *Gateway) RegisterTaskResultHandler(h TaskResultHandler) {
	g.taskResultHandler = h
}

// RegisterCredentialRequestHandler registers the callback for agent credential
// requests that need user input. Optional — if not registered, these messages
// are logged and dropped.
func (g *Gateway) RegisterCredentialRequestHandler(h CredentialRequestHandler) {
	g.credentialRequestHandler = h
}

// RegisterPlanDecisionHandler registers the callback for user approve/reject
// decisions on a proposed execution plan. Optional — if unset, incoming
// decisions are logged and dropped, and plan execution proceeds (or doesn't)
// based on the orchestrator's approval-timeout path.
func (g *Gateway) RegisterPlanDecisionHandler(h PlanDecisionHandler) {
	g.planDecisionHandler = h
}

// Start subscribes to all inbound NATS topics and begins message processing.
// Must be called after all handlers are registered.
func (g *Gateway) Start() error {
	if err := g.nats.Subscribe(TopicTasksInbound, g.handleRawInboundTask); err != nil {
		return fmt.Errorf("subscribe %s: %w", TopicTasksInbound, err)
	}
	if err := g.nats.Subscribe(TopicAgentStatusEvents, g.handleRawAgentStatus); err != nil {
		return fmt.Errorf("subscribe %s: %w", TopicAgentStatusEvents, err)
	}
	if err := g.nats.Subscribe(TopicCapabilityQueryResponse, g.handleCapabilityResponse); err != nil {
		return fmt.Errorf("subscribe %s: %w", TopicCapabilityQueryResponse, err)
	}
	if err := g.nats.Subscribe(TopicTaskAccepted, g.handleRawTaskAccepted); err != nil {
		return fmt.Errorf("subscribe %s: %w", TopicTaskAccepted, err)
	}
	if err := g.nats.Subscribe(TopicTaskResult, g.handleRawTaskResult); err != nil {
		return fmt.Errorf("subscribe %s: %w", TopicTaskResult, err)
	}
	if err := g.nats.Subscribe(TopicTaskFailed, g.handleRawTaskFailed); err != nil {
		return fmt.Errorf("subscribe %s: %w", TopicTaskFailed, err)
	}
	if err := g.nats.Subscribe(TopicCredentialRequest, g.handleRawCredentialRequest); err != nil {
		return fmt.Errorf("subscribe %s: %w", TopicCredentialRequest, err)
	}
	if err := g.nats.Subscribe(TopicPlanDecision, g.handleRawPlanDecision); err != nil {
		return fmt.Errorf("subscribe %s: %w", TopicPlanDecision, err)
	}
	if err := g.nats.Subscribe(TopicAgentStateWrite, g.handleRawStateWrite); err != nil {
		return fmt.Errorf("subscribe %s: %w", TopicAgentStateWrite, err)
	}
	if err := g.nats.Subscribe(TopicAgentStateReadRequest, g.handleRawStateReadRequest); err != nil {
		return fmt.Errorf("subscribe %s: %w", TopicAgentStateReadRequest, err)
	}
	observability.LogFromContext(observability.WithModule(context.Background(), "comms_gateway")).
		Info("gateway started — subscribed to inbound topics", "node_id", g.nodeID)
	return nil
}

// IsConnected returns true if the underlying NATS connection is active.
func (g *Gateway) IsConnected() bool {
	return g.nats.IsConnected()
}

// ── Inbound Handlers ─────────────────────────────────────────────────────────

// handleRawInboundTask handles aegis.orchestrator.tasks.inbound.
// Validates envelope, generates/extracts a trace_id, deserializes UserTask,
// builds the root context, logs receipt, then routes to taskHandler.
// Invalid envelopes are dead-lettered and not forwarded (§11.1).
func (g *Gateway) handleRawInboundTask(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		ctx := observability.WithModule(context.Background(), "comms_gateway")
		observability.LogFromContext(ctx).Warn("rejected malformed inbound task envelope", "error", err)
		_ = g.publishDeadLetter(data, err.Error())
		return err
	}

	var task types.UserTask
	if err := json.Unmarshal(envelope.Payload, &task); err != nil {
		ctx := observability.WithModule(context.Background(), "comms_gateway")
		observability.LogFromContext(ctx).Warn("failed to deserialize user_task payload", "error", err)
		_ = g.publishDeadLetter(data, fmt.Sprintf("payload deserialize error: %v", err))
		return fmt.Errorf("deserialize user_task: %w", err)
	}
	if task.TraceID == "" && strings.TrimSpace(envelope.TraceID) != "" {
		task.TraceID = strings.TrimSpace(envelope.TraceID)
	}

	ctx := extractOrCreateCtx(envelope, "comms_gateway")
	ctx = observability.WithTaskID(ctx, task.TaskID)

	ctx, span := observability.StartSpan(ctx, "task_received")
	defer span.End()
	observability.SpanSetTaskAttributes(span, task.TaskID, task.UserID)

	observability.LogFromContext(ctx).Info("user task received",
		"user_id", task.UserID,
		"priority", task.Priority)

	if g.taskHandler == nil {
		return fmt.Errorf("no task handler registered")
	}
	return g.taskHandler(ctx, task)
}

// handleRawAgentStatus handles aegis.agents.status.events.
func (g *Gateway) handleRawAgentStatus(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		observability.LogFromContext(observability.WithModule(context.Background(), "comms_gateway")).
			Warn("rejected malformed agent status envelope", "error", err)
		return err
	}

	var payload struct {
		AgentID string `json:"agent_id"`
		TaskID  string `json:"task_id"`
		State   string `json:"state"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("deserialize agent.status: %w", err)
	}

	if g.agentStatusHandler == nil {
		return fmt.Errorf("no agent status handler registered")
	}
	ctx := extractOrCreateCtx(envelope, "task_monitor")
	if payload.TaskID != "" {
		ctx = observability.WithTaskID(ctx, payload.TaskID)
	}
	return g.agentStatusHandler(ctx, types.AgentStatusUpdate{
		AgentID:             payload.AgentID,
		OrchestratorTaskRef: envelope.CorrelationID,
		TaskID:              payload.TaskID,
		State:               types.AgentState(payload.State),
		ProgressSummary:     firstNonEmpty(payload.Message, payload.Reason),
		Reason:              firstNonEmpty(payload.Reason, payload.Message),
	})
}

// handleCapabilityResponse handles inbound capability query replies.
// Routes response to the waiting PublishCapabilityQuery call via channel.
func (g *Gateway) handleCapabilityResponse(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		return err
	}

	var payload struct {
		QueryID  string   `json:"query_id"`
		Domains  []string `json:"domains"`
		HasMatch bool     `json:"has_match"`
		TraceID  string   `json:"trace_id"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("deserialize capability.response: %w", err)
	}

	queryID := firstNonEmpty(payload.QueryID, envelope.CorrelationID)
	if ch, ok := g.pendingCapabilityQueries.Load(queryID); ok {
		match := types.CapabilityMatch_NoMatch
		if payload.HasMatch {
			match = types.CapabilityMatch_Match
		}
		ch.(chan *types.CapabilityResponse) <- &types.CapabilityResponse{
			OrchestratorTaskRef: queryID,
			Match:               match,
		}
	}
	return nil
}

// handleRawTaskAccepted accepts and validates task.accepted messages even though
// the current orchestrator pipeline does not need to act on them synchronously.
func (g *Gateway) handleRawTaskAccepted(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		observability.LogFromContext(observability.WithModule(context.Background(), "comms_gateway")).
			Warn("rejected malformed task accepted envelope", "error", err)
		return err
	}

	var accepted struct {
		TaskID                string     `json:"task_id"`
		AgentID               string     `json:"agent_id"`
		AgentType             string     `json:"agent_type"`
		EstimatedCompletionAt *time.Time `json:"estimated_completion_at"`
	}
	if err := json.Unmarshal(envelope.Payload, &accepted); err != nil {
		return fmt.Errorf("deserialize task.accepted: %w", err)
	}
	_ = accepted
	return nil
}

// handleRawTaskResult handles aegis.orchestrator.task.result.
func (g *Gateway) handleRawTaskResult(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		observability.LogFromContext(observability.WithModule(context.Background(), "comms_gateway")).
			Warn("rejected malformed task result envelope", "error", err)
		return err
	}

	ctx := extractOrCreateCtx(envelope, "comms_gateway")

	var payload struct {
		TaskID      string          `json:"task_id"`
		AgentID     string          `json:"agent_id"`
		Success     bool            `json:"success"`
		Result      json.RawMessage `json:"result"`
		Output      json.RawMessage `json:"output"`
		ErrorCode   string          `json:"error_code"`
		Error       string          `json:"error"`
		CompletedAt time.Time       `json:"completed_at"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("deserialize task.result: %w", err)
	}

	if payload.TaskID != "" {
		ctx = observability.WithTaskID(ctx, payload.TaskID)
	}
	observability.LogFromContext(ctx).Info("received task result from agents")

	if g.taskResultHandler == nil {
		return fmt.Errorf("no task result handler registered")
	}
	result := types.TaskResult{
		OrchestratorTaskRef: envelope.CorrelationID,
		TaskID:              payload.TaskID,
		AgentID:             payload.AgentID,
		Success:             payload.Success,
		Result:              firstNonEmptyJSON(payload.Result, payload.Output),
		ErrorCode:           payload.ErrorCode,
		CompletedAt:         payload.CompletedAt,
	}
	if !result.Success && result.ErrorCode == "" && payload.Error != "" {
		result.ErrorCode = payload.Error
	}
	return g.taskResultHandler(ctx, result)
}

// handleRawTaskFailed normalizes task.failed into the same internal TaskResult
// path used for task.result so Dispatcher / Executor can handle a single stream.
func (g *Gateway) handleRawTaskFailed(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		observability.LogFromContext(observability.WithModule(context.Background(), "comms_gateway")).
			Warn("rejected malformed task failed envelope", "error", err)
		return err
	}

	ctx := extractOrCreateCtx(envelope, "comms_gateway")

	var payload struct {
		TaskID       string `json:"task_id"`
		AgentID      string `json:"agent_id"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("deserialize task.failed: %w", err)
	}
	if payload.TaskID != "" {
		ctx = observability.WithTaskID(ctx, payload.TaskID)
	}
	observability.LogFromContext(ctx).Info("received task failed from agents", "error_code", payload.ErrorCode)

	if g.taskResultHandler == nil {
		return fmt.Errorf("no task result handler registered")
	}

	return g.taskResultHandler(ctx, types.TaskResult{
		OrchestratorTaskRef: envelope.CorrelationID,
		TaskID:              payload.TaskID,
		AgentID:             payload.AgentID,
		Success:             false,
		ErrorCode:           firstNonEmpty(payload.ErrorCode, payload.ErrorMessage),
		CompletedAt:         envelope.Timestamp,
	})
}

// / handleRawCredentialRequest handles aegis.orchestrator.credential.request.
// Vault pre-authorization requests (operation: "authorize"/"revoke") are routed
// to the Vault via the orchestrator's policy flow — those are NOT forwarded to IO.
// Requests with operation "user_input" ask the user to supply a secret via IO.
func (g *Gateway) handleRawCredentialRequest(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		observability.LogFromContext(observability.WithModule(context.Background(), "comms_gateway")).
			Warn("rejected malformed credential.request envelope", "error", err)
		return err
	}

	var payload struct {
		RequestID   string `json:"request_id"`
		AgentID     string `json:"agent_id"`
		TaskID      string `json:"task_id"`
		Operation   string `json:"operation"`
		KeyName     string `json:"key_name"`
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("deserialize credential.request: %w", err)
	}

	// Only forward user_input requests to IO; vault authorize/revoke are
	// handled internally by the Policy Enforcer.
	if payload.Operation != "user_input" {
		return nil
	}

	if g.credentialRequestHandler == nil {
		ctx := extractOrCreateCtx(envelope, "comms_gateway")
		observability.LogFromContext(ctx).Warn("credential.request (user_input) received but no handler registered",
			"task_id", payload.TaskID)
		return nil
	}
	return g.credentialRequestHandler(
		payload.AgentID, payload.TaskID, payload.RequestID, payload.KeyName, payload.Label,
	)
}

// handleRawPlanDecision handles aegis.orchestrator.plan.decision. User I/O
// publishes these when the user clicks Approve or Reject on a plan preview.
func (g *Gateway) handleRawPlanDecision(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		observability.LogFromContext(observability.WithModule(context.Background(), "comms_gateway")).
			Warn("rejected malformed plan_decision envelope", "error", err)
		return err
	}

	var payload types.PlanDecision
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("deserialize plan.decision: %w", err)
	}
	// Fallback: correlation_id carries the orchestrator_task_ref on most
	// outbound envelopes we emit; accept it as a default if the payload
	// omits the field so IO clients have some leeway.
	if payload.OrchestratorTaskRef == "" {
		payload.OrchestratorTaskRef = envelope.CorrelationID
	}

	ctx := extractOrCreateCtx(envelope, "task_dispatcher")
	if payload.TaskID != "" {
		ctx = observability.WithTaskID(ctx, payload.TaskID)
	}
	observability.LogFromContext(ctx).Info("plan_decision received",
		"task_id", payload.TaskID,
		"approved", payload.Approved,
	)

	if g.planDecisionHandler == nil {
		observability.LogFromContext(ctx).Warn("plan_decision received but no handler registered")
		return nil
	}
	return g.planDecisionHandler(ctx, payload)
}

// ── Agent Memory Bridge ───────────────────────────────────────────────────────
//
// handleRawStateWrite receives aegis.orchestrator.state.write messages published
// by the Agents Component and stores them in the in-process agentStore. For writes
// with require_ack=true it publishes a state.write.ack back on
// aegis.agents.state.write.ack. Fire-and-forget writes (require_ack=false) are
// stored silently.
func (g *Gateway) handleRawStateWrite(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		return nil // tolerate malformed writes; don't crash the handler
	}

	// Keep a copy of the raw payload to return verbatim on reads.
	raw := json.RawMessage(envelope.Payload)

	// Peek at agentID and require_ack without full deserialization.
	var peek struct {
		AgentID    string `json:"agent_id"`
		RequireAck bool   `json:"require_ack"`
		RequestID  string `json:"request_id"`
	}
	if err := json.Unmarshal(envelope.Payload, &peek); err != nil || peek.AgentID == "" {
		return nil
	}

	g.agentStoreMu.Lock()
	g.agentStore[peek.AgentID] = append(g.agentStore[peek.AgentID], raw)
	g.agentStoreMu.Unlock()

	if peek.RequireAck {
		ack := struct {
			RequestID string `json:"request_id"`
			AgentID   string `json:"agent_id"`
			Status    string `json:"status"`
		}{
			RequestID: firstNonEmpty(peek.RequestID, envelope.CorrelationID),
			AgentID:   peek.AgentID,
			Status:    "accepted",
		}
		ctx := extractOrCreateCtx(envelope, "comms_gateway")
		correlationID := firstNonEmpty(peek.RequestID, envelope.CorrelationID)
		_ = g.publishEnvelope(ctx, TopicAgentStateWriteAck, "state.write.ack", correlationID, ack)
	}
	return nil
}

// handleRawStateReadRequest receives aegis.orchestrator.state.read.request messages
// published by the Agents Component, queries the in-process agentStore, and
// publishes matching records back on aegis.agents.state.read.response.
func (g *Gateway) handleRawStateReadRequest(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		return nil
	}

	var req struct {
		AgentID    string `json:"agent_id"`
		DataType   string `json:"data_type"`
		ContextTag string `json:"context_tag"`
		TraceID    string `json:"trace_id"`
	}
	if err := json.Unmarshal(envelope.Payload, &req); err != nil {
		return nil
	}

	traceID := firstNonEmpty(req.TraceID, envelope.CorrelationID)

	g.agentStoreMu.RLock()
	all := g.agentStore[req.AgentID]
	g.agentStoreMu.RUnlock()

	// Filter by contextTag (tags["context"]) and DataType if specified.
	var matched []json.RawMessage
	for _, rec := range all {
		var r struct {
			DataType string            `json:"data_type"`
			Tags     map[string]string `json:"tags"`
		}
		if err := json.Unmarshal(rec, &r); err != nil {
			continue
		}
		if req.DataType != "" && r.DataType != req.DataType {
			continue
		}
		if req.ContextTag != "" {
			if v, ok := r.Tags["context"]; !ok || v != req.ContextTag {
				continue
			}
		}
		matched = append(matched, rec)
	}

	resp := struct {
		AgentID string            `json:"agent_id"`
		Records []json.RawMessage `json:"records"`
		TraceID string            `json:"trace_id"`
	}{
		AgentID: req.AgentID,
		Records: matched,
		TraceID: traceID,
	}
	if resp.Records == nil {
		resp.Records = []json.RawMessage{} // never send null
	}

	ctx := extractOrCreateCtx(envelope, "comms_gateway")
	_ = g.publishEnvelope(ctx, TopicAgentStateReadResponse, "state.read.response", traceID, resp)
	return nil
}

// ── Outbound: to User I/O Component ──────────────────────────────────────────

// PublishTaskAccepted notifies User I/O that a task was accepted (§FR-ALC-03).
func (g *Gateway) PublishTaskAccepted(ctx context.Context, callbackTopic string, accepted types.TaskAccepted) error {
	return g.publishEnvelope(ctx, callbackTopic, "task_accepted", accepted.OrchestratorTaskRef, accepted)
}

// PublishError sends a structured error response to the User I/O Component (§11.1).
func (g *Gateway) PublishError(ctx context.Context, callbackTopic string, resp types.ErrorResponse) error {
	return g.publishEnvelope(ctx, callbackTopic, "error_response", resp.TaskID, resp)
}

// PublishStatusUpdate forwards an intermediate task progress update to User I/O (§FR-ALC-05).
func (g *Gateway) PublishStatusUpdate(ctx context.Context, userContextID string, status types.StatusResponse) error {
	return g.publishEnvelope(ctx, TopicStatusEvents, "task_status_update", status.TaskID, status)
}

// PublishTaskResult delivers the final task result to the task's callback_topic (§11.5).
func (g *Gateway) PublishTaskResult(ctx context.Context, callbackTopic string, result types.TaskResult) error {
	return g.publishEnvelope(ctx, callbackTopic, "task_result", result.OrchestratorTaskRef, result)
}

// ── Outbound: to Agents Component ────────────────────────────────────────────

// PublishTaskSpec dispatches a validated task.inbound request to the Agents
// Component. The internal TaskSpec is adapted to the agents-component schema.
func (g *Gateway) PublishTaskSpec(ctx context.Context, spec types.TaskSpec) error {
	wire := struct {
		TaskID         string            `json:"task_id"`
		RequiredSkills []string          `json:"required_skills"`
		Instructions   string            `json:"instructions"`
		Metadata       map[string]string `json:"metadata,omitempty"`
		TraceID        string            `json:"trace_id"`
		UserContextID  string            `json:"user_context_id,omitempty"`
		ConversationID string            `json:"conversation_id,omitempty"`
	}{
		TaskID:         spec.TaskID,
		RequiredSkills: spec.RequiredSkillDomains,
		Instructions:   buildAgentInstructions(spec),
		Metadata:       buildAgentMetadata(spec),
		TraceID:        observability.TraceIDFrom(ctx),
		UserContextID:  spec.UserContextID,
		ConversationID: spec.ConversationID,
	}
	return g.publishEnvelope(ctx, TopicAgentTasksInbound, "task.inbound", spec.OrchestratorTaskRef, wire)
}

// PublishCapabilityQuery sends a capability query and waits for the response.
// Blocks up to CapabilityQueryTimeout. Returns error on timeout (§FR-ALC-01).
func (g *Gateway) PublishCapabilityQuery(ctx context.Context, query types.CapabilityQuery) (*types.CapabilityResponse, error) {
	responseCh := make(chan *types.CapabilityResponse, 1)
	queryID := query.OrchestratorTaskRef
	g.pendingCapabilityQueries.Store(queryID, responseCh)
	defer g.pendingCapabilityQueries.Delete(queryID)

	wire := struct {
		QueryID string   `json:"query_id"`
		Domains []string `json:"domains"`
		TraceID string   `json:"trace_id"`
	}{
		QueryID: queryID,
		Domains: query.RequiredSkillDomains,
		TraceID: observability.TraceIDFrom(ctx),
	}

	if err := g.publishEnvelope(ctx, TopicCapabilityQuery, "capability.query", queryID, wire); err != nil {
		return nil, fmt.Errorf("publish capability_query: %w", err)
	}

	select {
	case resp := <-responseCh:
		return resp, nil
	case <-time.After(CapabilityQueryTimeout):
		return nil, fmt.Errorf("capability_query timed out after %s", CapabilityQueryTimeout)
	}
}

func firstNonEmptyJSON(values ...json.RawMessage) json.RawMessage {
	for _, v := range values {
		if len(v) != 0 && string(v) != "null" {
			return v
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func buildAgentMetadata(spec types.TaskSpec) map[string]string {
	meta := map[string]string{
		"orchestrator_task_ref": spec.OrchestratorTaskRef,
		"user_id":               spec.UserID,
		"callback_topic":        spec.CallbackTopic,
	}
	if spec.ProgressSummary != "" {
		meta["progress_summary"] = spec.ProgressSummary
	}
	for k, v := range spec.Metadata {
		meta[k] = v
	}
	return meta
}

func buildAgentInstructions(spec types.TaskSpec) string {
	if strings.TrimSpace(spec.Instructions) != "" {
		return spec.Instructions
	}
	if len(spec.Payload) == 0 {
		return "Complete the assigned task."
	}
	return "Complete the assigned task using this JSON payload as context:\n" + string(spec.Payload)
}

// PublishAgentTerminate instructs the Agents Component to terminate an agent (§11.2).
func (g *Gateway) PublishAgentTerminate(ctx context.Context, terminate types.AgentTerminate) error {
	return g.publishEnvelope(ctx, TopicAgentTerminate, "agent_terminate", terminate.OrchestratorTaskRef, terminate)
}

// PublishTaskCancel notifies the Agents Component to cancel a task (§11.2).
func (g *Gateway) PublishTaskCancel(ctx context.Context, cancel types.TaskCancel) error {
	return g.publishEnvelope(ctx, TopicTaskCancel, "task_cancel", cancel.OrchestratorTaskRef, cancel)
}

// ── Outbound: Observability ───────────────────────────────────────────────────

// PublishMetrics emits structured metrics to aegis.orchestrator.metrics (§15.2).
func (g *Gateway) PublishMetrics(metrics types.MetricsPayload) error {
	return g.publishEnvelope(context.Background(), TopicMetrics, "metrics", g.nodeID, metrics)
}

// PublishAuditEvent emits an audit event to aegis.orchestrator.audit.events (§11.5).
func (g *Gateway) PublishAuditEvent(ctx context.Context, event types.AuditEvent) error {
	return g.publishEnvelope(ctx, TopicAuditEvents, "audit_event", event.OrchestratorTaskRef, event)
}

// ── Envelope Helpers ──────────────────────────────────────────────────────────

// publishEnvelope wraps any payload in a signed MessageEnvelope and publishes it (§13.5).
// The trace_id from ctx is stamped into every outbound envelope (Step 5).
func (g *Gateway) publishEnvelope(ctx context.Context, topic, messageType, correlationID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload for %s: %w", messageType, err)
	}

	envelope := types.MessageEnvelope{
		MessageID:       newUUID(),
		MessageType:     messageType,
		SourceComponent: SourceComponent,
		CorrelationID:   correlationID,
		TraceID:         observability.TraceIDFrom(ctx),
		Timestamp:       time.Now().UTC(),
		SchemaVersion:   SchemaVersion,
		Payload:         raw,
	}

	envelopeBytes, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope for %s: %w", messageType, err)
	}

	return g.nats.Publish(topic, envelopeBytes)
}

// publishDeadLetter sends an unprocessable raw message to the dead-letter topic (§11.1).
func (g *Gateway) publishDeadLetter(raw []byte, reason string) error {
	dl := map[string]any{
		"raw_message": string(raw),
		"reason":      reason,
		"timestamp":   time.Now().UTC(),
		"node_id":     g.nodeID,
	}
	data, _ := json.Marshal(dl)
	return g.nats.Publish(TopicDeadLetter, data)
}

// validateEnvelope parses and validates an inbound message envelope (§13.5).
// All required fields must be present. Returns error for malformed messages.
func validateEnvelope(data []byte) (*types.MessageEnvelope, error) {
	var envelope types.MessageEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	if envelope.MessageID == "" {
		return nil, fmt.Errorf("envelope missing message_id")
	}
	if envelope.MessageType == "" {
		return nil, fmt.Errorf("envelope missing message_type")
	}
	if envelope.SourceComponent == "" {
		return nil, fmt.Errorf("envelope missing source_component")
	}
	if envelope.CorrelationID == "" {
		return nil, fmt.Errorf("envelope missing correlation_id")
	}
	if envelope.Timestamp.IsZero() {
		return nil, fmt.Errorf("envelope missing timestamp")
	}
	if envelope.SchemaVersion == "" {
		return nil, fmt.Errorf("envelope missing schema_version")
	}
	if len(envelope.Payload) == 0 {
		return nil, fmt.Errorf("envelope missing payload")
	}
	return &envelope, nil
}

// extractOrCreateCtx builds a context from the envelope's trace_id (or creates a new one)
// and attaches the given module name. Used by inbound NATS message handlers.
func extractOrCreateCtx(envelope *types.MessageEnvelope, module string) context.Context {
	traceID := envelope.TraceID
	if traceID == "" {
		traceID = newUUID()
	}
	ctx := context.Background()
	ctx = observability.WithTraceID(ctx, traceID)
	ctx = observability.WithModule(ctx, module)
	return ctx
}

// newUUID generates a random UUID v4 using crypto/rand.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
