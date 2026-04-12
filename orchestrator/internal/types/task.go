package types

import (
	"encoding/json"
	"time"
)

// ─── Task State Constants ─────────────────────────────────────────────────────
// All valid states in the top-level task lifecycle state machine (§9.1).

const (
	StateReceived            = "RECEIVED"
	StatePolicyCheck         = "POLICY_CHECK"
	StateDispatchPending     = "DISPATCH_PENDING"
	StateDispatched          = "DISPATCHED"
	StateDecomposing         = "DECOMPOSING"          // NEW v3.0 — awaiting Planner Agent response
	StatePlanActive          = "PLAN_ACTIVE"          // NEW v3.0 — Plan Executor dispatching subtasks
	StateRunning             = "RUNNING"
	StateRecovering          = "RECOVERING"
	StateCompleted           = "COMPLETED"
	StateFailed              = "FAILED"
	StateDeliveryFailed      = "DELIVERY_FAILED"
	StateTimedOut            = "TIMED_OUT"
	StatePolicyViolation     = "POLICY_VIOLATION"
	StateDecompositionFailed = "DECOMPOSITION_FAILED" // NEW v3.0 — Planner timed out or invalid plan
	StatePartialComplete     = "PARTIAL_COMPLETE"     // NEW v3.0 — some subtasks completed, some failed
)

// ─── Subtask State Constants ──────────────────────────────────────────────────
// All valid states for subtasks managed by Plan Executor (M7) (§9.2).

const (
	SubtaskStatePending         = "PENDING"
	SubtaskStateDispatchPending = "DISPATCH_PENDING"
	SubtaskStateDispatched      = "DISPATCHED"
	SubtaskStateRunning         = "RUNNING"
	SubtaskStateRecovering      = "RECOVERING"
	SubtaskStateCompleted       = "COMPLETED"
	SubtaskStateFailed          = "FAILED"
	SubtaskStateBlocked         = "BLOCKED"
	SubtaskStateTimedOut        = "TIMED_OUT"
	SubtaskStateDeliveryFailed  = "DELIVERY_FAILED"
)

// ─── Error Code Constants ─────────────────────────────────────────────────────
// Returned in error responses to User I/O Component (§14).

const (
	ErrCodePolicyViolation      = "POLICY_VIOLATION"
	ErrCodeMaxRetriesExceeded   = "MAX_RETRIES_EXCEEDED"
	ErrCodeVaultUnavailable     = "VAULT_UNAVAILABLE"
	ErrCodeInvalidTaskSpec      = "INVALID_TASK_SPEC"
	ErrCodeStorageUnavailable   = "STORAGE_UNAVAILABLE"
	ErrCodeDuplicateTask        = "DUPLICATE_TASK"
	ErrCodeAgentsUnavailable    = "AGENTS_UNAVAILABLE"
	ErrCodeScopeViolation       = "SCOPE_VIOLATION"
	ErrCodeScopeExpired         = "SCOPE_EXPIRED"
	ErrCodeStateRecoveryFailed  = "STATE_RECOVERY_FAILED"
	ErrCodeProvisioningFailed   = "PROVISIONING_FAILED"
	ErrCodeTimedOut             = "TIMED_OUT"
	ErrCodeDecompositionTimeout = "DECOMPOSITION_TIMEOUT" // NEW v3.0
	ErrCodeInvalidPlan          = "INVALID_PLAN"          // NEW v3.0
	ErrCodeEmptyPlan            = "EMPTY_PLAN"            // NEW v3.0
	ErrCodePlanTooLarge         = "PLAN_TOO_LARGE"        // NEW v3.0
)

// ─── Recovery Reason Constants ────────────────────────────────────────────────
// Passed from Task Monitor to Recovery Manager so recovery logic can distinguish
// why a task needs recovery.

type RecoveryReason string

const (
	RecoveryReasonAgentRecovering RecoveryReason = "AGENT_RECOVERING"
	RecoveryReasonAgentTerminated RecoveryReason = "AGENT_TERMINATED"
	RecoveryReasonTimeout         RecoveryReason = "TIMEOUT"
)

// ─── PolicyScope ──────────────────────────────────────────────────────────────
// Derived by Policy Enforcer from Vault. Immutable once attached to a task_spec.
// Defines the ceiling for all credential requests during task execution (§13.2).

type PolicyScope struct {
	Domains   []string          `json:"domains"`
	TokenRef  string            `json:"token_ref"` // Vault token accessor — NOT the token itself
	IssuedAt  time.Time         `json:"issued_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	Metadata  map[string]string `json:"metadata"`
}

// ─── StateEvent ───────────────────────────────────────────────────────────────
// One entry in TaskState.StateHistory. Append-only (§10.1).

type StateEvent struct {
	State     string    `json:"state"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason,omitempty"`
	NodeID    string    `json:"node_id"`
}

// ─── TaskState ────────────────────────────────────────────────────────────────
// The canonical task record stored in the Memory Component (§10.1).
// One record per task, updated on every state transition.

type TaskState struct {
	OrchestratorTaskRef  string          `json:"orchestrator_task_ref"` // UUID — Orchestrator-internal, distinct from task_id
	TaskID               string          `json:"task_id"`               // User-provided deduplication key
	UserID               string          `json:"user_id"`
	TraceID              string          `json:"trace_id,omitempty"`    // Distributed trace ID from inbound envelope
	State                string          `json:"state"`
	RequiredSkillDomains []string        `json:"required_skill_domains,omitempty"`
	PolicyScope          PolicyScope     `json:"policy_scope"`
	PlanID               string          `json:"plan_id,omitempty"`              // NEW v3.0 — set when PLAN_ACTIVE
	AgentID              string          `json:"agent_id,omitempty"`             // Null until DISPATCHED (single-agent legacy path)
	RetryCount           int             `json:"retry_count"`
	DispatchedAt         *time.Time      `json:"dispatched_at,omitempty"`
	TimeoutAt            *time.Time      `json:"timeout_at,omitempty"`   // received_at + timeout_seconds
	CompletedAt          *time.Time      `json:"completed_at,omitempty"` // Null while in progress
	ErrorCode            string          `json:"error_code,omitempty"`
	StateHistory         []StateEvent    `json:"state_history"`       // Append-only
	Payload              json.RawMessage `json:"payload,omitempty"`   // Opaque — passed verbatim to Planner Agent
	CallbackTopic        string          `json:"callback_topic"`
	UserContextID        string          `json:"user_context_id,omitempty"`
	IdempotencyWindow    int             `json:"idempotency_window_seconds"`
}

// ─── UserTask ─────────────────────────────────────────────────────────────────
// Inbound message schema from User I/O Component (§10.2).

type UserTask struct {
	TaskID               string          `json:"task_id"`                              // UUID, required — deduplication key
	UserID               string          `json:"user_id"`                              // Required
	RequiredSkillDomains []string        `json:"required_skill_domains,omitempty"`     // OPTIONAL in v3.0 (FR-TRK-04)
	Priority             int             `json:"priority"`                             // 1 (lowest) to 10 (highest)
	TimeoutSeconds       int             `json:"timeout_seconds"`                      // Min 30, max 86400
	Payload              json.RawMessage `json:"payload"`                              // Max 1MB; must contain raw_input for NL tasks
	CallbackTopic        string          `json:"callback_topic"`                       // Valid NATS topic
	UserContextID        string          `json:"user_context_id,omitempty"`
	IdempotencyWindow    int             `json:"idempotency_window_seconds,omitempty"` // Default 300
	// TraceID is W3C trace_id (32 hex) from I/O; also accepted on the inbound MessageEnvelope.trace_id.
	TraceID string `json:"trace_id,omitempty"`
}

// ─── Execution Plan & Subtask Types (NEW in v3.0) ─────────────────────────────

// PriorResult carries the output of a completed subtask for injection into dependents (§FR-TD-06).
type PriorResult struct {
	SubtaskID string          `json:"subtask_id"`
	Result    json.RawMessage `json:"result"`
}

// Subtask is one unit of work within an ExecutionPlan returned by the Planner Agent (§10.3).
type Subtask struct {
	SubtaskID            string         `json:"subtask_id"`
	RequiredSkillDomains []string       `json:"required_skill_domains"`
	Action               string         `json:"action"`
	Instructions         string         `json:"instructions"`
	Params               map[string]any `json:"params,omitempty"`
	DependsOn            []string       `json:"depends_on"`  // Empty = no dependencies; dispatch immediately
	TimeoutSeconds       int            `json:"timeout_seconds"`
}

// ExecutionPlan is the structured plan returned by the Planner Agent (§10.3).
type ExecutionPlan struct {
	PlanID       string    `json:"plan_id"`
	ParentTaskID string    `json:"parent_task_id"`
	Subtasks     []Subtask `json:"subtasks"`
	CreatedAt    time.Time `json:"created_at"`
}

// SubtaskState is the per-subtask tracking record managed by Plan Executor (§10.4).
type SubtaskState struct {
	SubtaskID            string          `json:"subtask_id"`
	PlanID               string          `json:"plan_id"`
	TaskID               string          `json:"task_id"`
	OrchestratorTaskRef  string          `json:"orchestrator_task_ref,omitempty"` // Set at dispatch; used to route task_result back
	State                string          `json:"state"`
	AgentID              string          `json:"agent_id,omitempty"`
	RequiredSkillDomains []string        `json:"required_skill_domains"`
	DependsOn            []string        `json:"depends_on"`
	PriorResults         []PriorResult   `json:"prior_results,omitempty"`
	RetryCount           int             `json:"retry_count"`
	DispatchedAt         *time.Time      `json:"dispatched_at,omitempty"`
	TimeoutAt            *time.Time      `json:"timeout_at,omitempty"`
	CompletedAt          *time.Time      `json:"completed_at,omitempty"`
	Result               json.RawMessage `json:"result,omitempty"`
	ErrorCode            string          `json:"error_code,omitempty"`
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// IsTerminalState returns true if the top-level task state has no further transitions.
func IsTerminalState(state string) bool {
	switch state {
	case StateCompleted, StateFailed, StateDeliveryFailed, StateTimedOut,
		StatePolicyViolation, StateDecompositionFailed, StatePartialComplete:
		return true
	}
	return false
}

// IsTerminalSubtaskState returns true if the subtask state has no further transitions.
func IsTerminalSubtaskState(state string) bool {
	switch state {
	case SubtaskStateCompleted, SubtaskStateFailed, SubtaskStateBlocked,
		SubtaskStateTimedOut, SubtaskStateDeliveryFailed:
		return true
	}
	return false
}
