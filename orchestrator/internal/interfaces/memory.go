package interfaces

import "github.com/mlim3/cerberOS/orchestrator/internal/types"

// MemoryClient defines how the Memory Interface (M6) communicates with
// the Memory Component (§11.4).
// The real implementation talks to the Memory Component REST API or libSQL directly.
// The mock implementation is in internal/mocks/memory_mock.go.
//
// CRITICAL: Only M6 (MemoryInterface module) may call these methods.
// No other module gets a direct database connection (§4.1, §11.4).
type MemoryClient interface {
	// Write persists a tagged payload to the Memory Component.
	// The payload's DataType MUST be one of the types.DataType* constants.
	// Untagged writes are rejected with an error.
	// Write retries up to 3 times with exponential backoff before returning error (§14.1).
	// IMPORTANT: audit_log and policy_event records are append-only.
	// UPDATE and DELETE on those data types are rejected at the storage layer (§13.4).
	Write(payload types.OrchestratorMemoryWritePayload) error

	// Read retrieves all matching records ordered by timestamp ascending.
	// Supports filtering by orchestrator_task_ref OR task_id, data_type, and time range (§11.4).
	Read(query types.MemoryQuery) ([]types.MemoryRecord, error)

	// ReadLatest retrieves the single most recent record for a given task_id and data_type.
	// Used by Recovery Manager to restore the last valid task state before re-dispatch (§FR-SH-02).
	ReadLatest(taskID string, dataType string) (*types.MemoryRecord, error)

	// Ping checks Memory Component reachability.
	// Used by the health monitoring loop every 10 seconds (§12.1).
	Ping() error
}
