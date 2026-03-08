// Package memory implements M6: Memory Interface.
//
// The Memory Interface is the ONLY module that reads from or writes to the
// Memory Component (libSQL/SQLite). No other module gets a direct database
// connection. All persistence goes through this package (§4.1 M6, §11.4).
//
// Responsibilities:
//   - Enforce structured, tagged write payloads (untagged writes → error)
//   - Serve task state reads for deduplication, recovery, and startup rehydration
//   - Never accept raw session transcripts — only structured, extracted state
//   - Write retries: up to 3 attempts with exponential backoff
//   - Escalate to Recovery Manager on persistent write failure
//   - Enforce append-only on audit_log and policy_event records at DB layer
//
// CRITICAL (§13.4): audit_log and policy_event records are APPEND-ONLY.
// UPDATE and DELETE on those data types are rejected at the storage layer
// via a database trigger — NOT just at the application layer.
//
// Schema (§10.1, §10.3):
//   - task_state table
//   - audit_log table (append-only, write-once integrity hash)
//   - recovery_event table
//   - policy_event table
package memory

import (
	"fmt"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// WriteFailureHandler is called when all write retries are exhausted.
// Typically wired to Recovery Manager.HandleComponentFailure().
type WriteFailureHandler func(payload types.OrchestratorMemoryWritePayload, err error)

// Interface is M6: Memory Interface.
// It wraps the raw MemoryClient (real libSQL or mock) and adds:
//   - Data type validation
//   - Retry logic with exponential backoff
//   - Append-only enforcement for audit records
type Interface struct {
	client         interfaces.MemoryClient
	cfg            *config.OrchestratorConfig
	onWriteFailure WriteFailureHandler
}

const (
	writeMaxAttempts     = 3
	initialRetryBackoff  = 50 * time.Millisecond
)

// New creates a new Memory Interface wrapping the given MemoryClient.
func New(client interfaces.MemoryClient, cfg *config.OrchestratorConfig) *Interface {
	return &Interface{
		client: client,
		cfg:    cfg,
	}
}

// SetWriteFailureHandler registers the callback invoked when all write retries fail.
// Must be set before Write() is called. Typically wired to Recovery Manager.
func (i *Interface) SetWriteFailureHandler(h WriteFailureHandler) {
	i.onWriteFailure = h
}

// Write persists a tagged payload to the Memory Component.
// Validates data_type before writing. Retries up to 3 times with exponential backoff.
// On persistent failure: calls onWriteFailure handler and returns error.
func (i *Interface) Write(payload types.OrchestratorMemoryWritePayload) error {
	if err := validateWritePayload(payload); err != nil {
		return err
	}

	var lastErr error
	backoff := initialRetryBackoff

	for attempt := 1; attempt <= writeMaxAttempts; attempt++ {
		if err := i.client.Write(payload); err != nil {
			lastErr = err
			if attempt < writeMaxAttempts {
				time.Sleep(backoff)
				backoff *= 2
			}
			continue
		}
		return nil
	}

	err := fmt.Errorf("memory write failed after %d attempts: %w", writeMaxAttempts, lastErr)
	if i.onWriteFailure != nil {
		i.onWriteFailure(payload, err)
	}
	return err
}

// Read retrieves all matching records ordered by timestamp ascending (§11.4).
func (i *Interface) Read(query types.MemoryQuery) ([]types.MemoryRecord, error) {
	if !isValidDataType(query.DataType) {
		return nil, fmt.Errorf("invalid data_type: %q", query.DataType)
	}
	if query.TaskID == "" && query.OrchestratorTaskRef == "" && len(query.Filter) == 0 {
		return nil, fmt.Errorf("read query requires task_id, orchestrator_task_ref, or filter")
	}
	if query.FromTimestamp != nil && query.ToTimestamp != nil && query.FromTimestamp.After(*query.ToTimestamp) {
		return nil, fmt.Errorf("from_timestamp must be before to_timestamp")
	}

	return i.client.Read(query)
}

// ReadLatest retrieves the most recent record for a given task_id and data_type.
// Used by Recovery Manager to restore the last valid task state (§FR-SH-02).
func (i *Interface) ReadLatest(taskID string, dataType string) (*types.MemoryRecord, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	if !isValidDataType(dataType) {
		return nil, fmt.Errorf("invalid data_type: %q", dataType)
	}

	return i.client.ReadLatest(taskID, dataType)
}

// Ping checks Memory Component reachability (§12.1).
func (i *Interface) Ping() error {
	return i.client.Ping()
}

// MigrateSchema ensures all required tables exist with the correct schema.
// Called once at startup before RehydrateFromMemory (§FR-SH-05).
// Enforces append-only constraint on audit_log and policy_event via DB trigger.
//
// TODO Phase 1: implement with CREATE TABLE IF NOT EXISTS + trigger DDL
func (i *Interface) MigrateSchema() error {
	// TODO Phase 1
	return nil
}

func validateWritePayload(payload types.OrchestratorMemoryWritePayload) error {
	if payload.OrchestratorTaskRef == "" {
		return fmt.Errorf("orchestrator_task_ref is required")
	}
	if payload.TaskID == "" {
		return fmt.Errorf("task_id is required")
	}
	if !isValidDataType(payload.DataType) {
		return fmt.Errorf("invalid data_type: %q", payload.DataType)
	}
	if payload.Timestamp.IsZero() {
		return fmt.Errorf("timestamp is required")
	}
	if len(payload.Payload) == 0 {
		return fmt.Errorf("payload is required")
	}
	return nil
}

func isValidDataType(dataType string) bool {
	switch dataType {
	case types.DataTypeTaskState, types.DataTypeAuditLog, types.DataTypeRecoveryEvent, types.DataTypePolicyEvent:
		return true
	default:
		return false
	}
}
