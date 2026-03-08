// Package gateway implements M1: Communications Gateway.
//
// The Communications Gateway is the single inbound/outbound gateway for all
// NATS messaging. It is the ONLY module that publishes to or subscribes from
// NATS topics. All other modules communicate via internal Go method calls.
//
// Responsibilities (§4.1 M1):
//   - Validate message envelope schema on all inbound messages
//   - Route parsed user_task to Task Dispatcher
//   - Route agent_status_update events to Task Monitor
//   - Publish all outbound messages (results, errors, status, metrics)
//   - Manage NATS consumer ACK/NAK and dead-letter queue monitoring
//
// NATS Topic Hierarchy (§11.5):
//   - INBOUND:  aegis.orchestrator.tasks.inbound
//   - INBOUND:  aegis.agents.status.events
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
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// NATS topic constants. All orchestrator topics are under aegis.orchestrator.>
// All agent topics are under aegis.agents.> (published TO the Agents Component).
const (
	TopicTasksInbound       = "aegis.orchestrator.tasks.inbound"
	TopicAgentStatusEvents  = "aegis.agents.status.events"
	TopicAgentTasksInbound  = "aegis.agents.tasks.inbound"
	TopicCapabilityQuery    = "aegis.agents.capability.query"
	TopicAgentTerminate     = "aegis.agents.lifecycle.terminate"
	TopicTaskCancel         = "aegis.agents.tasks.cancel"
	TopicOrchestratorErrors = "aegis.orchestrator.errors"
	TopicAuditEvents        = "aegis.orchestrator.audit.events"
	TopicMetrics            = "aegis.orchestrator.metrics"
	TopicDeadLetter         = "aegis.orchestrator.tasks.deadletter"
	TopicStatusEvents       = "aegis.orchestrator.status.events"
)

// TaskHandler is the callback the Task Dispatcher registers to receive parsed inbound tasks.
type TaskHandler func(task types.UserTask) error

// AgentStatusHandler is the callback the Task Monitor registers to receive agent status updates.
type AgentStatusHandler func(update types.AgentStatusUpdate) error

// TaskResultHandler is the callback the Task Dispatcher registers to receive task results.
type TaskResultHandler func(result types.TaskResult) error

// Gateway is M1: Communications Gateway.
type Gateway struct {
	nats              interfaces.NATSClient
	taskHandler       TaskHandler
	agentStatusHandler AgentStatusHandler
	taskResultHandler TaskResultHandler
	nodeID            string
}

// New creates a new Gateway. Call Start() to begin receiving messages.
func New(nats interfaces.NATSClient, nodeID string) *Gateway {
	return &Gateway{
		nats:   nats,
		nodeID: nodeID,
	}
}

// RegisterTaskHandler registers the callback for inbound user_task messages.
// Must be called before Start(). Typically registered by Task Dispatcher.
func (g *Gateway) RegisterTaskHandler(h TaskHandler) {
	g.taskHandler = h
}

// RegisterAgentStatusHandler registers the callback for agent_status_update messages.
// Must be called before Start(). Typically registered by Task Monitor.
func (g *Gateway) RegisterAgentStatusHandler(h AgentStatusHandler) {
	g.agentStatusHandler = h
}

// RegisterTaskResultHandler registers the callback for task_result messages.
// Must be called before Start(). Typically registered by Task Dispatcher.
func (g *Gateway) RegisterTaskResultHandler(h TaskResultHandler) {
	g.taskResultHandler = h
}

// Start subscribes to all inbound NATS topics and begins message processing.
// Must be called after all handlers are registered.
// Blocks until an error occurs or Close() is called.
//
// TODO Phase 3: implement full subscription logic with:
//   - Message envelope validation (§13.5)
//   - ACK on success, NAK on handler error
//   - Dead-letter after max_redelivery
func (g *Gateway) Start() error {
	// TODO Phase 3: subscribe to TopicTasksInbound
	// TODO Phase 3: subscribe to TopicAgentStatusEvents
	// TODO Phase 3: subscribe to task result callback topics
	return nil
}

// ── Outbound: to User I/O Component ──────────────────────────────────────────

// PublishTaskAccepted notifies the User I/O Component that a task was accepted (§FR-ALC-03).
// Must be sent within 5s (existing agent) or 30s (new agent provisioning).
//
// TODO Phase 3: implement
func (g *Gateway) PublishTaskAccepted(callbackTopic string, accepted types.TaskAccepted) error {
	// TODO Phase 3
	return nil
}

// PublishError sends a structured error response to the User I/O Component (§11.1).
//
// TODO Phase 3: implement
func (g *Gateway) PublishError(callbackTopic string, resp types.ErrorResponse) error {
	// TODO Phase 3
	return nil
}

// PublishStatusUpdate forwards an intermediate task progress update to User I/O
// if user_context_id is set on the task (§FR-ALC-05).
//
// TODO Phase 3: implement
func (g *Gateway) PublishStatusUpdate(userContextID string, status types.StatusResponse) error {
	// TODO Phase 3
	return nil
}

// PublishTaskResult delivers the final task result to the task's callback_topic (§11.5).
//
// TODO Phase 3: implement
func (g *Gateway) PublishTaskResult(callbackTopic string, result types.TaskResult) error {
	// TODO Phase 3
	return nil
}

// ── Outbound: to Agents Component ────────────────────────────────────────────

// PublishTaskSpec dispatches a validated task_spec to the Agents Component (§11.2).
//
// TODO Phase 3: implement
func (g *Gateway) PublishTaskSpec(spec types.TaskSpec) error {
	// TODO Phase 3
	return nil
}

// PublishCapabilityQuery queries the Agents Component for a capable agent
// before dispatch (§FR-ALC-01). Must respond within 500ms p99.
//
// TODO Phase 3: implement with request-reply pattern
func (g *Gateway) PublishCapabilityQuery(query types.CapabilityQuery) (*types.CapabilityResponse, error) {
	// TODO Phase 3
	return nil, nil
}

// PublishAgentTerminate instructs the Agents Component to terminate an agent (§11.2).
//
// TODO Phase 3: implement
func (g *Gateway) PublishAgentTerminate(terminate types.AgentTerminate) error {
	// TODO Phase 3
	return nil
}

// PublishTaskCancel notifies the Agents Component to cancel a task (§11.2).
//
// TODO Phase 3: implement
func (g *Gateway) PublishTaskCancel(cancel types.TaskCancel) error {
	// TODO Phase 3
	return nil
}

// ── Outbound: Observability ───────────────────────────────────────────────────

// PublishMetrics emits structured metrics to aegis.orchestrator.metrics (§15.2).
//
// TODO Phase 7: implement
func (g *Gateway) PublishMetrics(metrics types.MetricsPayload) error {
	// TODO Phase 7
	return nil
}

// PublishAuditEvent emits an audit event to aegis.orchestrator.audit.events (§11.5).
//
// TODO Phase 7: implement
func (g *Gateway) PublishAuditEvent(event types.AuditEvent) error {
	// TODO Phase 7
	return nil
}
