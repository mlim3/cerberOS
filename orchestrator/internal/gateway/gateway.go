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
//   - Publish all outbound messages (results, errors, status, metrics)
//   - Manage NATS consumer ACK/NAK and dead-letter monitoring
//
// NATS Topic Hierarchy (§11.5):
//   - INBOUND:  aegis.orchestrator.tasks.inbound
//   - INBOUND:  aegis.agents.status.events
//   - INBOUND:  aegis.agents.capability.response  (reply to capability queries)
//   - OUTBOUND: aegis.orchestrator.tasks.results.>
//   - OUTBOUND: aegis.orchestrator.status.events
//   - OUTBOUND: aegis.orchestrator.errors
//   - OUTBOUND: aegis.orchestrator.audit.events
//   - OUTBOUND: aegis.orchestrator.metrics
//   - OUTBOUND: aegis.orchestrator.tasks.deadletter
//   - OUTBOUND: aegis.agents.tasks.inbound
//   - OUTBOUND: aegis.agents.capability.query
//   - OUTBOUND: aegis.agents.lifecycle.terminate
//   - OUTBOUND: aegis.agents.tasks.cancel
package gateway

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// NATS topic constants.
const (
	TopicTasksInbound            = "aegis.orchestrator.tasks.inbound"
	TopicAgentStatusEvents       = "aegis.agents.status.events"
	TopicCapabilityQueryResponse = "aegis.agents.capability.response"
	TopicAgentTasksInbound       = "aegis.agents.tasks.inbound"
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

// TaskResultHandler is the callback the Task Dispatcher registers to receive task results.
type TaskResultHandler func(result types.TaskResult) error

// ── Gateway ───────────────────────────────────────────────────────────────────

// Gateway is M1: Communications Gateway.
type Gateway struct {
	nats   interfaces.NATSClient
	nodeID string
	logger *log.Logger

	taskHandler        TaskHandler
	agentStatusHandler AgentStatusHandler
	taskResultHandler  TaskResultHandler

	// pendingCapabilityQueries tracks in-flight capability query requests.
	// key: correlationID (orchestrator_task_ref), value: chan *types.CapabilityResponse
	pendingCapabilityQueries sync.Map
}

// New creates a new Gateway. Call RegisterHandlers then Start() before use.
func New(nats interfaces.NATSClient, nodeID string) *Gateway {
	return &Gateway{
		nats:   nats,
		nodeID: nodeID,
		logger: log.New(os.Stdout, "[gateway] ", log.LstdFlags|log.LUTC),
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

// RegisterTaskResultHandler registers the callback for task_result messages.
// Must be called before Start(). Registered by Task Dispatcher.
func (g *Gateway) RegisterTaskResultHandler(h TaskResultHandler) {
	g.taskResultHandler = h
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
	g.logger.Printf("gateway started on node %s — subscribed to inbound topics", g.nodeID)
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
		g.logger.Printf("rejected malformed inbound task envelope: %v", err)
		_ = g.publishDeadLetter(data, err.Error())
		return err
	}

	var task types.UserTask
	if err := json.Unmarshal(envelope.Payload, &task); err != nil {
		g.logger.Printf("failed to deserialize user_task payload: %v", err)
		_ = g.publishDeadLetter(data, fmt.Sprintf("payload deserialize error: %v", err))
		return fmt.Errorf("deserialize user_task: %w", err)
	}

	if g.taskHandler == nil {
		return fmt.Errorf("no task handler registered")
	}
	return g.taskHandler(task)
}

// handleRawAgentStatus handles aegis.agents.status.events.
func (g *Gateway) handleRawAgentStatus(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		g.logger.Printf("rejected malformed agent status envelope: %v", err)
		return err
	}

	var update types.AgentStatusUpdate
	if err := json.Unmarshal(envelope.Payload, &update); err != nil {
		return fmt.Errorf("deserialize agent_status_update: %w", err)
	}

	if g.agentStatusHandler == nil {
		return fmt.Errorf("no agent status handler registered")
	}
	return g.agentStatusHandler(update)
}

// handleCapabilityResponse handles inbound capability query replies.
// Routes response to the waiting PublishCapabilityQuery call via channel.
func (g *Gateway) handleCapabilityResponse(subject string, data []byte) error {
	envelope, err := validateEnvelope(data)
	if err != nil {
		return err
	}

	var resp types.CapabilityResponse
	if err := json.Unmarshal(envelope.Payload, &resp); err != nil {
		return fmt.Errorf("deserialize capability_response: %w", err)
	}

	// Route to the waiting channel keyed by correlationID (= orchestrator_task_ref)
	if ch, ok := g.pendingCapabilityQueries.Load(envelope.CorrelationID); ok {
		ch.(chan *types.CapabilityResponse) <- &resp
	}
	return nil
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

// PublishTaskSpec dispatches a validated task_spec to the Agents Component (§11.2).
func (g *Gateway) PublishTaskSpec(spec types.TaskSpec) error {
	return g.publishEnvelope(TopicAgentTasksInbound, "task_spec", spec.OrchestratorTaskRef, spec)
}

// PublishCapabilityQuery sends a capability query and waits for the response.
// Blocks up to CapabilityQueryTimeout. Returns error on timeout (§FR-ALC-01).
func (g *Gateway) PublishCapabilityQuery(query types.CapabilityQuery) (*types.CapabilityResponse, error) {
	responseCh := make(chan *types.CapabilityResponse, 1)
	g.pendingCapabilityQueries.Store(query.OrchestratorTaskRef, responseCh)
	defer g.pendingCapabilityQueries.Delete(query.OrchestratorTaskRef)

	if err := g.publishEnvelope(TopicCapabilityQuery, "capability_query", query.OrchestratorTaskRef, query); err != nil {
		return nil, fmt.Errorf("publish capability_query: %w", err)
	}

	select {
	case resp := <-responseCh:
		return resp, nil
	case <-time.After(CapabilityQueryTimeout):
		return nil, fmt.Errorf("capability_query timed out after %s", CapabilityQueryTimeout)
	}
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
