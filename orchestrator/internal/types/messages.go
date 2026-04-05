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
	Timestamp       time.Time       `json:"timestamp"`
	SchemaVersion   string          `json:"schema_version"`    // "1.0"
	Payload         json.RawMessage `json:"payload"`
}

// ─── Capability Query / Response ──────────────────────────────────────────────
// Outbound to Agents Component on aegis.agents.capability.query (§11.2).

type CapabilityQuery struct {
	OrchestratorTaskRef  string   `json:"orchestrator_task_ref"`
	RequiredSkillDomains []string `json:"required_skill_domains"`
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
	AgentID              string          `json:"agent_id,omitempty"`          // Set if match or partial_match
	ProvisioningEstimate int             `json:"provisioning_estimate_seconds,omitempty"`
}

// ─── Task Spec ────────────────────────────────────────────────────────────────
// Outbound to Agents Component on aegis.agents.tasks.inbound (§11.2).
// Contains policy_scope — the immutable ceiling for all agent credential requests.

type TaskSpec struct {
	OrchestratorTaskRef  string          `json:"orchestrator_task_ref"`
	TaskID               string          `json:"task_id"`
	UserID               string          `json:"user_id"`
	RequiredSkillDomains []string        `json:"required_skill_domains"`
	PolicyScope          PolicyScope     `json:"policy_scope"` // Immutable. Agent cannot exceed this scope.
	TimeoutSeconds       int             `json:"timeout_seconds"`
	Payload              json.RawMessage `json:"payload"`
	CallbackTopic        string          `json:"callback_topic"`
	UserContextID        string          `json:"user_context_id,omitempty"`
	ProgressSummary      string          `json:"progress_summary,omitempty"` // Set on recovery re-dispatch
}

// ─── Task Accepted / Result ───────────────────────────────────────────────────
// Inbound from Agents Component after task_spec dispatch.

type TaskAccepted struct {
	OrchestratorTaskRef string    `json:"orchestrator_task_ref"`
	AgentID             string    `json:"agent_id"`
	EstimatedCompletion time.Time `json:"estimated_completion_at"`
}

type TaskResult struct {
	OrchestratorTaskRef string          `json:"orchestrator_task_ref"`
	AgentID             string          `json:"agent_id"`
	Success             bool            `json:"success"`
	Result              json.RawMessage `json:"result"`
	ErrorCode           string          `json:"error_code,omitempty"`
	CompletedAt         time.Time       `json:"completed_at"`
}

// ─── Agent Status Update ──────────────────────────────────────────────────────
// Inbound from Agents Component on aegis.agents.status.events.

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
	TaskID      string `json:"task_id"`
	ErrorCode   string `json:"error_code"`
	UserMessage string `json:"user_message"`
}

type StatusResponse struct {
	TaskID      string `json:"task_id"`
	State       string `json:"state"`
	AgentID     string `json:"agent_id,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
}

// ─── Task Decomposition Request / Response (NEW in v3.0) ──────────────────────
// Outbound to Agents Component on aegis.agents.decomposition.request (§11.2).
// Inbound from Agents Component on aegis.orchestrator.decomposition.response (§11.3).

// DecompositionRequest is sent to the Planner Agent after policy validation (§FR-TD-01).
// The Planner Agent must only assign skill domains within the provided policy_scope.
type DecompositionRequest struct {
	TaskID              string      `json:"task_id"`
	OrchestratorTaskRef string      `json:"orchestrator_task_ref"`
	UserID              string      `json:"user_id"`
	RawInput            string      `json:"raw_input"`           // Verbatim from user_task.payload.raw_input
	PolicyScope         PolicyScope `json:"policy_scope"`        // Ceiling for all subtask domains
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
	NodeID                         string    `json:"node_id"`
	Timestamp                      time.Time `json:"timestamp"`
	TasksReceivedTotal             int64     `json:"orchestrator_tasks_received_total"`
	TasksCompletedTotal            int64     `json:"orchestrator_tasks_completed_total"`
	TasksFailedTotal               int64     `json:"orchestrator_tasks_failed_total"`
	PolicyViolationsTotal          int64     `json:"orchestrator_policy_violations_total"`
	RecoveryAttemptsTotal          int64     `json:"orchestrator_recovery_attempts_total"`
	ActiveTasks                    int64     `json:"orchestrator_active_tasks"`
	VaultAvailable                 int       `json:"orchestrator_vault_available"` // 1 = reachable, 0 = unreachable
	QueueDepth                     int64     `json:"queue_depth"`
}
