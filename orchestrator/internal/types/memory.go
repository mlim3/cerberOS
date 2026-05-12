package types

import (
	"encoding/json"
	"time"
)

// ─── Data Type Constants ──────────────────────────────────────────────────────
// All valid data_type tag values for Memory Interface writes (§11.4).
// Untagged writes are rejected with an error.

const (
	DataTypeTaskState     = "task_state"
	DataTypePlanState     = "plan_state"     // NEW v3.0 — execution plan records
	DataTypeSubtaskState  = "subtask_state"  // NEW v3.0 — per-subtask state records
	DataTypeAuditLog      = "audit_log"
	DataTypeRecoveryEvent = "recovery_event"
	DataTypePolicyEvent   = "policy_event"
)

// ─── OrchestratorMemoryWritePayload ──────────────────────────────────────────
// The only accepted write payload shape for the Memory Interface (§11.4).
// All fields are required. Untagged payloads are rejected.

type OrchestratorMemoryWritePayload struct {
	UserID              string          `json:"user_id"` // Required — owning tenant, persisted on every orchestrator record (MT-4 #185).
	OrchestratorTaskRef string          `json:"orchestrator_task_ref"`
	TaskID              string          `json:"task_id"`
	PlanID              string          `json:"plan_id,omitempty"`    // NEW v3.0 — set for plan_state and subtask_state writes
	SubtaskID           string          `json:"subtask_id,omitempty"` // NEW v3.0 — set for subtask_state writes
	DataType            string          `json:"data_type"`            // Must be one of DataType* constants
	Timestamp           time.Time       `json:"timestamp"`
	Payload             json.RawMessage `json:"payload"`
	TTLSeconds          int             `json:"ttl_seconds,omitempty"` // 0 = no expiry (audit records)
}

// ─── MemoryQuery ──────────────────────────────────────────────────────────────
// Read request sent to Memory Interface (§11.4).

type MemoryQuery struct {
	UserID              string            `json:"user_id,omitempty"` // Required UNLESS AllTenants=true (MT-4 #185 / MT-14 #189).
	OrchestratorTaskRef string            `json:"orchestrator_task_ref,omitempty"`
	TaskID              string            `json:"task_id,omitempty"`
	DataType            string            `json:"data_type"`
	FromTimestamp       *time.Time        `json:"from_timestamp,omitempty"`
	ToTimestamp         *time.Time        `json:"to_timestamp,omitempty"`
	Filter              map[string]string `json:"filter,omitempty"` // e.g. {"state": "not_terminal"}
	// AllTenants is the explicit opt-in for a cross-tenant read (MT-14, #189).
	// Single-host demo orchestrators are global-by-design and the startup
	// RehydrateFromMemory path is the only legitimate caller. Every other read
	// MUST scope by UserID; AllTenants is validated independently at the
	// orchestrator Memory Interface and at the memory BFF before any unscoped
	// SQL runs.
	AllTenants bool `json:"all_tenants,omitempty"`
}

// ─── MemoryRecord ─────────────────────────────────────────────────────────────
// A single record returned by a Memory Interface read operation.

type MemoryRecord struct {
	UserID              string          `json:"user_id"`
	OrchestratorTaskRef string          `json:"orchestrator_task_ref"`
	TaskID              string          `json:"task_id"`
	DataType            string          `json:"data_type"`
	Timestamp           time.Time       `json:"timestamp"`
	Payload             json.RawMessage `json:"payload"`
}

// ─── RecoveryEvent ───────────────────────────────────────────────────────────
// Written to Memory Interface on each recovery attempt (§FR-SH-02).

type RecoveryEvent struct {
	OrchestratorTaskRef string    `json:"orchestrator_task_ref"`
	TaskID              string    `json:"task_id"`
	AttemptNumber       int       `json:"attempt_number"`
	Reason              string    `json:"reason"`
	Timestamp           time.Time `json:"timestamp"`
	NodeID              string    `json:"node_id"`
}
