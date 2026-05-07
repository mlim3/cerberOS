package mocks

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// MemoryMock is a controllable in-memory implementation of interfaces.MemoryClient.
// All records are stored in-process — no database required.
//
// Usage:
//
//	mem := mocks.NewMemoryMock()
//	mem.ShouldFailWrites = true    // simulate Memory Component being down
//	mem.Records                    // inspect what was written
type MemoryMock struct {
	mu sync.RWMutex

	// Control flags
	ShouldFailWrites bool // Write() returns error (simulate Memory Component down)
	ShouldFailReads  bool // Read() and ReadLatest() return error
	PingFails        bool // Ping() returns error

	// Storage
	Records []types.MemoryRecord

	// Inspection
	WriteCallCount int
	ReadCallCount  int
	PingCallCount  int
}

func NewMemoryMock() *MemoryMock {
	return &MemoryMock{}
}

func (m *MemoryMock) Write(payload types.OrchestratorMemoryWritePayload) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.WriteCallCount++

	if m.ShouldFailWrites {
		return errors.New("memory component unavailable")
	}

	// Validate data_type — reject untagged writes
	switch payload.DataType {
	case types.DataTypeTaskState, types.DataTypePlanState, types.DataTypeSubtaskState,
		types.DataTypeAuditLog, types.DataTypeRecoveryEvent, types.DataTypePolicyEvent:
		// valid
	default:
		return fmt.Errorf("invalid data_type: %q — must be one of: task_state, plan_state, subtask_state, audit_log, recovery_event, policy_event", payload.DataType)
	}

	m.Records = append(m.Records, types.MemoryRecord{
		OrchestratorTaskRef: payload.OrchestratorTaskRef,
		TaskID:              payload.TaskID,
		DataType:            payload.DataType,
		Timestamp:           payload.Timestamp,
		Payload:             payload.Payload,
	})

	return nil
}

func (m *MemoryMock) Read(query types.MemoryQuery) ([]types.MemoryRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	m.ReadCallCount++

	if m.ShouldFailReads {
		return nil, errors.New("memory component unavailable")
	}

	var results []types.MemoryRecord
	for _, r := range m.Records {
		if !matchesQuery(r, query) {
			continue
		}
		results = append(results, r)
	}

	// Sort ascending by timestamp
	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.Before(results[j].Timestamp)
	})

	return results, nil
}

func (m *MemoryMock) ReadLatest(taskID string, dataType string) (*types.MemoryRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	m.ReadCallCount++

	if m.ShouldFailReads {
		return nil, errors.New("memory component unavailable")
	}

	var latest *types.MemoryRecord
	for i := range m.Records {
		r := &m.Records[i]
		if r.TaskID != taskID || r.DataType != dataType {
			continue
		}
		if latest == nil || r.Timestamp.After(latest.Timestamp) {
			latest = r
		}
	}

	if latest == nil {
		return nil, fmt.Errorf("no records found for task_id=%s data_type=%s", taskID, dataType)
	}

	return latest, nil
}

func (m *MemoryMock) Ping() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.PingCallCount++

	if m.PingFails {
		return errors.New("memory component unreachable")
	}
	return nil
}

// GetTaskState is a test helper that deserializes the latest task_state record for a given task.
func (m *MemoryMock) GetTaskState(taskID string) (*types.TaskState, error) {
	record, err := m.ReadLatest(taskID, types.DataTypeTaskState)
	if err != nil {
		return nil, err
	}
	var ts types.TaskState
	if err := json.Unmarshal(record.Payload, &ts); err != nil {
		return nil, fmt.Errorf("failed to deserialize task state: %w", err)
	}
	return &ts, nil
}

// Reset clears all records and resets control flags and counters.
func (m *MemoryMock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Records = nil
	m.ShouldFailWrites = false
	m.ShouldFailReads = false
	m.PingFails = false
	m.WriteCallCount = 0
	m.ReadCallCount = 0
	m.PingCallCount = 0
}

// matchesQuery checks if a MemoryRecord satisfies the given MemoryQuery filters.
func matchesQuery(r types.MemoryRecord, q types.MemoryQuery) bool {
	if q.DataType != "" && r.DataType != q.DataType {
		return false
	}
	if q.TaskID != "" && r.TaskID != q.TaskID {
		return false
	}
	if q.OrchestratorTaskRef != "" && r.OrchestratorTaskRef != q.OrchestratorTaskRef {
		return false
	}
	if q.FromTimestamp != nil && r.Timestamp.Before(*q.FromTimestamp) {
		return false
	}
	if q.ToTimestamp != nil && r.Timestamp.After(*q.ToTimestamp) {
		return false
	}

	// Handle "not_terminal" filter
	if state, ok := q.Filter["state"]; ok && state == "not_terminal" {
		var ts types.TaskState
		if err := json.Unmarshal(r.Payload, &ts); err == nil {
			if types.IsTerminalState(ts.State) {
				return false
			}
		}
	}

	return true
}

// Ensure MemoryMock satisfies the interface at compile time.
var _ interface {
	Write(types.OrchestratorMemoryWritePayload) error
	Read(types.MemoryQuery) ([]types.MemoryRecord, error)
	ReadLatest(string, string) (*types.MemoryRecord, error)
	Ping() error
} = &MemoryMock{}

// ensure time is used
var _ = time.Now
