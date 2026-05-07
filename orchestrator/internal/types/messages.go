package types

import (
	"encoding/json"
	"time"
)

// ─── Message Envelope ─────────────────────────────────────────────────────────
// All messages published by the Orchestrator include this signed envelope (§13.5).
// Messages without a valid envelope are rejected by the Communications Gateway.

type MessageEnvelope struct {
	MessageID       string          `json:"message_id"`        // UUID
	MessageType     string          `json:"message_type"`      // e.g. "task_spec", "capability_query"
	SourceComponent string          `json:"source_component"`  // always "orchestrator"
	CorrelationID   string          `json:"correlation_id"`    // task UUID
	TraceID         string          `json:"trace_id"`          // W3C trace_id (32 hex) for distributed tracing
	SpanID          string          `json:"span_id,omitempty"` // optional span ID
	Timestamp       time.Time       `json:"timestamp"`
	SchemaVersion   string          `json:"schema_version"` // "1.0"
	Payload         json.RawMessage `json:"payload"`
}

// ─── Capability Query / Response ──────────────────────────────────────────────
// Internal orchestrator view of the capability query flow.
// The Gateway adapts these structs to the agents-component wire schema.

type CapabilityQuery struct {
	OrchestratorTaskRef  string   `json:"orchestrator_task_ref"`
	RequiredSkillDomains []string `json:"required_skill_domains"`
	// TraceID is W3C trace_id (32 hex); empty uses orchestrator_task_ref on the wire.
	TraceID string `json:"trace_id,omitempty"`
}

type CapabilityMatch string

const (
	CapabilityMatch_Match        CapabilityMatch = "match"
	CapabilityMatch_PartialMatch CapabilityMatch = "partial_match"
	CapabilityMatch_NoMatch      CapabilityMatch = "no_match"
)

type CapabilityResponse struct {
	OrchestratorTaskRef  string          `json:"orchestrator_task_ref"`
	Match                CapabilityMatch `json:"match"`
	AgentID              string          `json:"agent_id,omitempty"` // Set if match or partial_match
	ProvisioningEstimate int             `json:"provisioning_estimate_seconds,omitempty"`
}

// ─── Task Spec ────────────────────────────────────────────────────────────────
// Internal orchestrator view of an agent task dispatch.
// The Gateway converts this into the agents-component task.inbound wire schema:
//   - task_id
//   - required_skills
//   - instructions
//   - metadata
//   - trace_id
//   - user_context_id
//   - conversation_id
//
// PolicyScope and CallbackTopic remain internal orchestrator concerns and are
// preserved here so Dispatcher / Executor / Recovery can reuse the same struct.

type TaskSpec struct {
	OrchestratorTaskRef  string            `json:"orchestrator_task_ref"`
	TaskID               string            `json:"task_id"`
	UserID               string            `json:"user_id"`
	RequiredSkillDomains []string          `json:"required_skill_domains"`
	PolicyScope          PolicyScope       `json:"policy_scope"` // Immutable. Agent cannot exceed this scope.
	TimeoutSeconds       int               `json:"timeout_seconds"`
	Payload              json.RawMessage   `json:"payload"`
	Instructions         string            `json:"instructions,omitempty"`
	Metadata             map[string]string `json:"metadata,omitempty"`
	CallbackTopic        string            `json:"callback_topic"`
	UserContextID        string            `json:"user_context_id,omitempty"`
	ConversationID       string            `json:"conversation_id,omitempty"`  // Stable ID linking tasks in the same multi-turn conversation
	ProgressSummary      string            `json:"progress_summary,omitempty"` // Set on recovery re-dispatch
	// TraceID is W3C trace_id (32 hex); empty uses orchestrator_task_ref on the agent wire payload.
	TraceID string `json:"trace_id,omitempty"`
}

// ─── Task Accepted / Result ───────────────────────────────────────────────────
// Internal orchestrator view of inbound terminal agent events.
// The Gateway maps the agents-component payloads/envelope correlation IDs into
// these structs so downstream modules can remain stable.

type TaskAccepted struct {
	OrchestratorTaskRef string    `json:"orchestrator_task_ref"`
	AgentID             string    `json:"agent_id"`
	EstimatedCompletion time.Time `json:"estimated_completion_at"`
}

type TaskResult struct {
	OrchestratorTaskRef string          `json:"orchestrator_task_ref"`
	TaskID              string          `json:"task_id,omitempty"`
	UserID              string          `json:"user_id,omitempty"`
	ConversationID      string          `json:"conversation_id,omitempty"`
	RawInput            string          `json:"raw_input,omitempty"`
	AgentID             string          `json:"agent_id"`
	Success             bool            `json:"success"`
	Result              json.RawMessage `json:"result"`
	ErrorCode           string          `json:"error_code,omitempty"`
	CompletedAt         time.Time       `json:"completed_at"`
}

// AgentSpawnRequest is published by a running agent when it wants the
// Orchestrator to route a self-contained child task to another skill-scoped
// agent. The parent agent supplies task intent and required skill domains; the
// Orchestrator resolves trusted user/policy context from the parent task state.
type AgentSpawnRequest struct {
	RequestID      string   `json:"request_id"`
	ParentAgentID  string   `json:"parent_agent_id"`
	ParentTaskID   string   `json:"parent_task_id"`
	RequiredSkills []string `json:"required_skills"`
	Instructions   string   `json:"instructions"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	TraceID        string   `json:"trace_id"`
	UserContextID  string   `json:"user_context_id,omitempty"`
}

// AgentSpawnResponse is returned to the parent agent once the child task reaches
// a terminal task.result/task.failed event.
type AgentSpawnResponse struct {
	RequestID     string `json:"request_id"`
	ParentAgentID string `json:"parent_agent_id"`
	ChildAgentID  string `json:"child_agent_id,omitempty"`
	Status        string `json:"status"` // success | failed
	Result        string `json:"result,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
	TraceID       string `json:"trace_id"`
}

// ─── Agent Status Update ──────────────────────────────────────────────────────
// Internal orchestrator view of agent lifecycle status updates. The Gateway
// maps the agents-component status payload into this shape.

type AgentState string

const (
	AgentStateActive     AgentState = "ACTIVE"
	AgentStateRecovering AgentState = "RECOVERING"
	AgentStateTerminated AgentState = "TERMINATED"
)

type AgentStatusUpdate struct {
	AgentID             string     `json:"agent_id"`
	OrchestratorTaskRef string     `json:"orchestrator_task_ref"`
	TaskID              string     `json:"task_id"`
	State               AgentState `json:"state"`
	ProgressSummary     string     `json:"progress_summary,omitempty"`
	Reason              string     `json:"reason,omitempty"`
}

// ─── Agent Terminate / Task Cancel ───────────────────────────────────────────
// Outbound to Agents Component (§11.2).

type AgentTerminate struct {
	AgentID             string `json:"agent_id"`
	OrchestratorTaskRef string `json:"orchestrator_task_ref"`
	Reason              string `json:"reason"`
}

type TaskCancel struct {
	OrchestratorTaskRef string `json:"orchestrator_task_ref"`
	TaskID              string `json:"task_id"`
	Reason              string `json:"reason"`
}

// ─── Error / Status Responses ─────────────────────────────────────────────────
// Outbound to User I/O Component.

type ErrorResponse struct {
	TaskID         string `json:"task_id"`
	UserID         string `json:"user_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	ErrorCode      string `json:"error_code"`
	UserMessage    string `json:"user_message"`
}

type StatusResponse struct {
	TaskID    string `json:"task_id"`
	State     string `json:"state"`
	AgentID   string `json:"agent_id,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

// ─── Task Decomposition Request / Response (NEW in v3.0) ──────────────────────
// Internal planning data structures.
// Decomposition is now performed by dispatching a standard task.inbound request
// to a general-purpose agent; these structs are kept for internal validation and
// Dispatcher/Executor handoff after the planner agent's JSON result is parsed.

// DecompositionRequest is sent to the Planner Agent after policy validation (§FR-TD-01).
// The Planner Agent must only assign skill domains within the provided policy_scope.
type DecompositionRequest struct {
	TaskID              string      `json:"task_id"`
	OrchestratorTaskRef string      `json:"orchestrator_task_ref"`
	UserID              string      `json:"user_id"`
	RawInput            string      `json:"raw_input"`    // Verbatim from user_task.payload.raw_input
	PolicyScope         PolicyScope `json:"policy_scope"` // Ceiling for all subtask domains
	UserContextID       string      `json:"user_context_id,omitempty"`
}

// DecompositionResponse is returned by the Planner Agent containing the execution plan (§11.5).
type DecompositionResponse struct {
	TaskID             string         `json:"task_id"`
	Plan               ExecutionPlan  `json:"plan"`
	GenerationMetadata map[string]any `json:"generation_metadata,omitempty"`
}

// ─── Metrics Payload ──────────────────────────────────────────────────────────
// Emitted to aegis.orchestrator.metrics on a configurable interval (§15.2).

type MetricsPayload struct {
	NodeID                string    `json:"node_id"`
	Timestamp             time.Time `json:"timestamp"`
	TasksReceivedTotal    int64     `json:"orchestrator_tasks_received_total"`
	TasksCompletedTotal   int64     `json:"orchestrator_tasks_completed_total"`
	TasksFailedTotal      int64     `json:"orchestrator_tasks_failed_total"`
	PolicyViolationsTotal int64     `json:"orchestrator_policy_violations_total"`
	RecoveryAttemptsTotal int64     `json:"orchestrator_recovery_attempts_total"`
	ActiveTasks           int64     `json:"orchestrator_active_tasks"`
	VaultAvailable        int       `json:"orchestrator_vault_available"` // 1 = reachable, 0 = unreachable
	QueueDepth            int64     `json:"queue_depth"`
}
