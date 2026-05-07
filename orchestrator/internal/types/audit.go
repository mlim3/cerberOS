package types

import (
	"encoding/json"
	"time"
)

// ─── Event Type Constants ─────────────────────────────────────────────────────
// All valid event_type values for the audit log (§10.3).

const (
	EventTaskReceived      = "task_received"
	EventPolicyAllow       = "policy_allow"
	EventPolicyDeny        = "policy_deny"
	EventTaskDispatched    = "task_dispatched"
	EventTaskCompleted     = "task_completed"
	EventTaskFailed        = "task_failed"
	EventRecoveryAttempt   = "recovery_attempt"
	EventCredentialRevoked = "credential_revoked"
	EventVaultUnavailable  = "vault_unavailable"
	EventComponentFailure  = "component_failure"
	EventRevocationFailed  = "revocation_failed"
)

// ─── Outcome Constants ────────────────────────────────────────────────────────

const (
	OutcomeSuccess   = "success"
	OutcomeDenied    = "denied"
	OutcomeFailed    = "failed"
	OutcomePartial   = "partial"
	OutcomeRecovered = "recovered"
)

// ─── Initiating Module Constants ─────────────────────────────────────────────
// Maps to the six internal modules M1–M6 (§4.1).

const (
	ModuleCommunicationsGateway = "CommunicationsGateway"
	ModuleTaskDispatcher        = "TaskDispatcher"
	ModulePolicyEnforcer        = "PolicyEnforcer"
	ModuleTaskMonitor           = "TaskMonitor"
	ModuleRecoveryManager       = "RecoveryManager"
	ModuleMemoryInterface       = "MemoryInterface"
)

// ─── AuditEvent ───────────────────────────────────────────────────────────────
// Stored in Memory Component, append-only. Every state transition and
// policy decision writes one record (§10.3, §FR-OBS-01).
// CRITICAL: Records with data_type=audit_log must never be updated or deleted.

type AuditEvent struct {
	LogID               string          `json:"log_id"`                // UUID — unique log entry
	OrchestratorTaskRef string          `json:"orchestrator_task_ref"`
	EventType           string          `json:"event_type"`            // See EventType constants
	InitiatingModule    string          `json:"initiating_module"`     // See Module constants
	Outcome             string          `json:"outcome"`               // See Outcome constants
	EventDetail         json.RawMessage `json:"event_detail"`          // Structured detail. No PII, no credentials.
	Timestamp           time.Time       `json:"timestamp"`             // Immutable once written
	NodeID              string          `json:"node_id"`
	TaskID              string          `json:"task_id,omitempty"`
	UserID              string          `json:"user_id,omitempty"`
}
