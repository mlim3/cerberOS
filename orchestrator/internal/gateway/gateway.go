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
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
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
	TopicAgentTasksInbound       = "aegis.agents.task.inbound"
	TopicCapabilityQuery         = "aegis.agents.capability.query"
	TopicAgentTerminate          = "aegis.agents.lifecycle.terminate"
	TopicTaskCancel              = "aegis.agents.tasks.cancel"
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
type TaskHandler func(task types.UserTask) error

// AgentStatusHandler is the callback the Task Monitor registers to receive agent status updates.
type AgentStatusHandler func(update types.AgentStatusUpdate) error

// TaskResultHandler is the callback the Plan Executor registers to receive task results.
type TaskResultHandler func(result types.TaskResult) error

// CredentialRequestHandler is called when an agent publishes a credential.request
// that requires user input (operation: "user_input"). Registered by main.go to
// forward the request to the IO Component.
type CredentialRequestHandler func(agentID, taskID, requestID, keyName, label string) error

// ── Gateway ───────────────────────────────────────────────────────────────────

// Gateway is M1: Communications Gateway.
type Gateway struct {
	nats   interfaces.NATSClient
	nodeID string
	logger *slog.Logger

	taskHandler               TaskHandler
	agentStatusHandler        AgentStatusHandler
	taskResultHandler         TaskResultHandler
	credentialRequestHandler  CredentialRequestHandler

	// pendingCapabilityQueries tracks in-flight capability query requests.
	// key: query_id, value: chan *types.CapabilityResponse
	pendingCapabilityQueries sync.Map
}

// New creates a new Gateway. Call RegisterHandlers then Start() before use.
func New(nats interfaces.NATSClient, nodeID string) *Gateway {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	return &Gateway{
		nats:   nats,
		nodeID: nodeID,
		logger: slog.New(h),
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
	g.logger.Info("gateway started",
		"service", "orchestrator",
		"component", "gateway",
		"node_id", g.nodeID,
	)
	return nil
}

// IsConnected returns true if the underlying NATS connection is active.
func (g *Gateway) IsConnected() bool {
	return g.nats.IsConnected()
}

// ── Inbound Handlers ─────────────────────────────────────────────────────────

// handleRawInboundTask handles aegis.orchestrator.tasks.inbound.
// Validates envelope, deserializes UserTask, routes to taskHandler.
// Invalid envelopes are dead-lettered and not forwarded (§11.1).
func (g *Gateway) handleRawInboundTask(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		g.logger.Error("rejected malformed inbound task envelope",
			"service", "orchestrator",
			"component", "gateway",
			"node_id", g.nodeID,
			"err", err,
		)
		_ = g.publishDeadLetter(data, err.Error())
		return err
	}

	var task types.UserTask
	if err := json.Unmarshal(envelope.Payload, &task); err != nil {
		attrs := []any{
			"service", "orchestrator",
			"component", "gateway",
			"node_id", g.nodeID,
			"err", err,
		}
		if envelope.TraceID != "" {
			attrs = append(attrs, "trace_id", envelope.TraceID)
		}
		g.logger.Error("failed to deserialize user_task payload", attrs...)
		_ = g.publishDeadLetter(data, fmt.Sprintf("payload deserialize error: %v", err))
		return fmt.Errorf("deserialize user_task: %w", err)
	}

	if g.taskHandler == nil {
		return fmt.Errorf("no task handler registered")
	}
	recvAttrs := []any{
		"service", "orchestrator",
		"component", "gateway",
		"node_id", g.nodeID,
		"task_id", task.TaskID,
	}
	if envelope.TraceID != "" {
		recvAttrs = append(recvAttrs, "trace_id", envelope.TraceID)
	}
	g.logger.Info("inbound user_task received", recvAttrs...)
	return g.taskHandler(task)
}

// handleRawAgentStatus handles aegis.agents.status.events.
func (g *Gateway) handleRawAgentStatus(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		g.logger.Error("rejected malformed agent status envelope",
			"service", "orchestrator", "component", "gateway", "node_id", g.nodeID, "err", err)
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
	return g.agentStatusHandler(types.AgentStatusUpdate{
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
		g.logger.Error("rejected malformed task accepted envelope",
			"service", "orchestrator", "component", "gateway", "node_id", g.nodeID, "err", err)
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
		g.logger.Error("rejected malformed task result envelope",
			"service", "orchestrator", "component", "gateway", "node_id", g.nodeID, "err", err)
		return err
	}

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
	return g.taskResultHandler(result)
}

// handleRawTaskFailed normalizes task.failed into the same internal TaskResult
// path used for task.result so Dispatcher / Executor can handle a single stream.
func (g *Gateway) handleRawTaskFailed(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		g.logger.Error("rejected malformed task failed envelope",
			"service", "orchestrator", "component", "gateway", "node_id", g.nodeID, "err", err)
		return err
	}

	var payload struct {
		TaskID       string `json:"task_id"`
		AgentID      string `json:"agent_id"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("deserialize task.failed: %w", err)
	}
	if g.taskResultHandler == nil {
		return fmt.Errorf("no task result handler registered")
	}

	return g.taskResultHandler(types.TaskResult{
		OrchestratorTaskRef: envelope.CorrelationID,
		TaskID:              payload.TaskID,
		AgentID:             payload.AgentID,
		Success:             false,
		ErrorCode:           firstNonEmpty(payload.ErrorCode, payload.ErrorMessage),
		CompletedAt:         envelope.Timestamp,
	})
}

/// handleRawCredentialRequest handles aegis.orchestrator.credential.request.
// Vault pre-authorization requests (operation: "authorize"/"revoke") are routed
// to the Vault via the orchestrator's policy flow — those are NOT forwarded to IO.
// Requests with operation "user_input" ask the user to supply a secret via IO.
func (g *Gateway) handleRawCredentialRequest(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		g.logger.Error("rejected malformed credential.request envelope",
			"service", "orchestrator", "component", "gateway", "node_id", g.nodeID, "err", err)
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
		g.logger.Warn("credential.request (user_input) received but no handler registered",
			"service", "orchestrator", "component", "gateway", "node_id", g.nodeID, "task_id", payload.TaskID)
		return nil
	}
	return g.credentialRequestHandler(
		payload.AgentID, payload.TaskID, payload.RequestID, payload.KeyName, payload.Label,
	)
}

// ── Outbound: to User I/O Component ──────────────────────────────────────────

// PublishTaskAccepted notifies User I/O that a task was accepted (§FR-ALC-03).
func (g *Gateway) PublishTaskAccepted(callbackTopic string, accepted types.TaskAccepted) error {
	return g.publishEnvelope(callbackTopic, "task_accepted", accepted.OrchestratorTaskRef, accepted)
}

// PublishError sends a structured error response to the User I/O Component (§11.1).
func (g *Gateway) PublishError(callbackTopic string, resp types.ErrorResponse) error {
	return g.publishEnvelope(callbackTopic, "error_response", resp.TaskID, resp)
}

// PublishStatusUpdate forwards an intermediate task progress update to User I/O (§FR-ALC-05).
func (g *Gateway) PublishStatusUpdate(userContextID string, status types.StatusResponse) error {
	return g.publishEnvelope(TopicStatusEvents, "task_status_update", status.TaskID, status)
}

// PublishTaskResult delivers the final task result to the task's callback_topic (§11.5).
func (g *Gateway) PublishTaskResult(callbackTopic string, result types.TaskResult) error {
	return g.publishEnvelope(callbackTopic, "task_result", result.OrchestratorTaskRef, result)
}

// ── Outbound: to Agents Component ────────────────────────────────────────────

// PublishTaskSpec dispatches a validated task.inbound request to the Agents
// Component. The internal TaskSpec is adapted to the agents-component schema.
func (g *Gateway) PublishTaskSpec(spec types.TaskSpec) error {
	wire := struct {
		TaskID         string            `json:"task_id"`
		RequiredSkills []string          `json:"required_skills"`
		Instructions   string            `json:"instructions"`
		Metadata       map[string]string `json:"metadata,omitempty"`
		TraceID        string            `json:"trace_id"`
		UserContextID  string            `json:"user_context_id,omitempty"`
	}{
		TaskID:         spec.TaskID,
		RequiredSkills: spec.RequiredSkillDomains,
		Instructions:   buildAgentInstructions(spec),
		Metadata:       buildAgentMetadata(spec),
		TraceID:        spec.OrchestratorTaskRef,
		UserContextID:  spec.UserContextID,
	}
	return g.publishEnvelope(TopicAgentTasksInbound, "task.inbound", spec.OrchestratorTaskRef, wire)
}

// PublishCapabilityQuery sends a capability query and waits for the response.
// Blocks up to CapabilityQueryTimeout. Returns error on timeout (§FR-ALC-01).
func (g *Gateway) PublishCapabilityQuery(query types.CapabilityQuery) (*types.CapabilityResponse, error) {
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
		TraceID: query.OrchestratorTaskRef,
	}

	if err := g.publishEnvelope(TopicCapabilityQuery, "capability.query", queryID, wire); err != nil {
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
func (g *Gateway) PublishAgentTerminate(terminate types.AgentTerminate) error {
	return g.publishEnvelope(TopicAgentTerminate, "agent_terminate", terminate.OrchestratorTaskRef, terminate)
}

// PublishTaskCancel notifies the Agents Component to cancel a task (§11.2).
func (g *Gateway) PublishTaskCancel(cancel types.TaskCancel) error {
	return g.publishEnvelope(TopicTaskCancel, "task_cancel", cancel.OrchestratorTaskRef, cancel)
}

// ── Outbound: Observability ───────────────────────────────────────────────────

// PublishMetrics emits structured metrics to aegis.orchestrator.metrics (§15.2).
func (g *Gateway) PublishMetrics(metrics types.MetricsPayload) error {
	return g.publishEnvelope(TopicMetrics, "metrics", g.nodeID, metrics)
}

// PublishAuditEvent emits an audit event to aegis.orchestrator.audit.events (§11.5).
func (g *Gateway) PublishAuditEvent(event types.AuditEvent) error {
	return g.publishEnvelope(TopicAuditEvents, "audit_event", event.OrchestratorTaskRef, event)
}

// ── Envelope Helpers ──────────────────────────────────────────────────────────

// publishEnvelope wraps any payload in a signed MessageEnvelope and publishes it (§13.5).
func (g *Gateway) publishEnvelope(topic, messageType, correlationID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload for %s: %w", messageType, err)
	}

	envelope := types.MessageEnvelope{
		MessageID:       newUUID(),
		MessageType:     messageType,
		SourceComponent: SourceComponent,
		CorrelationID:   correlationID,
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

// newUUID generates a random UUID v4 using crypto/rand.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
