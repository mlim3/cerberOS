package types

import (
	"encoding/json"
	"time"
)

// ─── Task State Constants ─────────────────────────────────────────────────────
// All valid states in the task lifecycle state machine (§9).

const (
	StateReceived        = "RECEIVED"
	StatePolicyCheck     = "POLICY_CHECK"
	StateDispatched      = "DISPATCHED"
	StateRunning         = "RUNNING"
	StateRecovering      = "RECOVERING"
	StateCompleted       = "COMPLETED"
	StateFailed          = "FAILED"
	StateTimedOut        = "TIMED_OUT"
	StatePolicyViolation = "POLICY_VIOLATION"
)

// ─── Error Code Constants ─────────────────────────────────────────────────────
// Returned in error responses to User I/O Component (§14).

const (
	ErrCodePolicyViolation    = "POLICY_VIOLATION"
	ErrCodeMaxRetriesExceeded = "MAX_RETRIES_EXCEEDED"
	ErrCodeVaultUnavailable   = "VAULT_UNAVAILABLE"
	ErrCodeInvalidTaskSpec    = "INVALID_TASK_SPEC"
	ErrCodeStorageUnavailable = "STORAGE_UNAVAILABLE"
	ErrCodeDuplicateTask      = "DUPLICATE_TASK"
	ErrCodeAgentsUnavailable  = "AGENTS_UNAVAILABLE"
	ErrCodeScopeViolation      = "SCOPE_VIOLATION"
	ErrCodeScopeExpired        = "SCOPE_EXPIRED"
	ErrCodeStateRecoveryFailed = "STATE_RECOVERY_FAILED"
	ErrCodeProvisioningFailed  = "PROVISIONING_FAILED"
	ErrCodeTimedOut            = "TIMED_OUT"
)

// ─── PolicyScope ──────────────────────────────────────────────────────────────
// Derived by Policy Enforcer from Vault. Immutable once attached to a task_spec.
// Defines the ceiling for all credential requests during task execution (§13.2).

type PolicyScope struct {
	Domains      []string          `json:"domains"`
	TokenRef     string            `json:"token_ref"`      // Vault token accessor — NOT the token itself
	IssuedAt     time.Time         `json:"issued_at"`
	ExpiresAt    time.Time         `json:"expires_at"`
	Metadata     map[string]string `json:"metadata"`
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
	OrchestratorTaskRef  string          `json:"orchestrator_task_ref"`   // UUID — Orchestrator-internal, distinct from task_id
	TaskID               string          `json:"task_id"`                 // User-provided deduplication key
	UserID               string          `json:"user_id"`
	State                string          `json:"state"`
	RequiredSkillDomains []string        `json:"required_skill_domains"`
	PolicyScope          PolicyScope     `json:"policy_scope"`
	AgentID              string          `json:"agent_id,omitempty"`      // Null until DISPATCHED
	RetryCount           int             `json:"retry_count"`
	DispatchedAt         *time.Time      `json:"dispatched_at,omitempty"`
	TimeoutAt            *time.Time      `json:"timeout_at,omitempty"`   // dispatched_at + timeout_seconds
	CompletedAt          *time.Time      `json:"completed_at,omitempty"` // Null while in progress
	ErrorCode            string          `json:"error_code,omitempty"`
	StateHistory         []StateEvent    `json:"state_history"`          // Append-only
	Payload              json.RawMessage `json:"payload,omitempty"`      // Opaque — passed verbatim to Agents Component
	CallbackTopic        string          `json:"callback_topic"`
	UserContextID        string          `json:"user_context_id,omitempty"`
	IdempotencyWindow    int             `json:"idempotency_window_seconds"`
}

// ─── UserTask ─────────────────────────────────────────────────────────────────
// Inbound message schema from User I/O Component (§10.2).

type UserTask struct {
	TaskID               string          `json:"task_id"`                // UUID, required — deduplication key
	UserID               string          `json:"user_id"`                // Required
	RequiredSkillDomains []string        `json:"required_skill_domains"` // Required, min 1 item
	Priority             int             `json:"priority"`               // 1 (lowest) to 10 (highest)
	TimeoutSeconds       int             `json:"timeout_seconds"`        // Min 30, max 86400
	Payload              json.RawMessage `json:"payload"`                // Max 1MB serialized
	CallbackTopic        string          `json:"callback_topic"`         // Valid NATS topic
	UserContextID        string          `json:"user_context_id,omitempty"`
	IdempotencyWindow    int             `json:"idempotency_window_seconds,omitempty"` // Default 300
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// IsTerminalState returns true if the state has no further transitions.
func IsTerminalState(state string) bool {
	switch state {
	case StateCompleted, StateFailed, StateTimedOut, StatePolicyViolation:
		return true
	}
	return false
}
