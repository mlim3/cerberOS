package memory_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	memoryiface "github.com/mlim3/cerberOS/orchestrator/internal/memory"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// TestWriteValidDataTypeSucceeds verifies that Write succeeds
// when the payload contains a valid DataType.
// It also checks that the underlying memory layer is called once
// and that exactly one record is written.
func TestWriteValidDataTypeSucceeds(t *testing.T) {
	mem := mocks.NewMemoryMock()
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	payload := newWritePayload(t, types.DataTypeTaskState)
	if err := iface.Write(payload); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if mem.WriteCallCount != 1 {
		t.Fatalf("WriteCallCount = %d, want 1", mem.WriteCallCount)
	}
	if len(mem.Records) != 1 {
		t.Fatalf("records written = %d, want 1", len(mem.Records))
	}
}

// TestWriteInvalidDataTypeReturnsError verifies that Write rejects
// payloads whose DataType is not in the allowed constants list.
// It should return an error and must not call the underlying memory write.
func TestWriteInvalidDataTypeReturnsError(t *testing.T) {
	mem := mocks.NewMemoryMock()
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	err := iface.Write(newWritePayload(t, "bad_type"))
	if err == nil {
		t.Fatal("Write() error = nil, want invalid data_type error")
	}
	if !strings.Contains(err.Error(), "invalid data_type") {
		t.Fatalf("Write() error = %v, want invalid data_type", err)
	}
	if mem.WriteCallCount != 0 {
		t.Fatalf("WriteCallCount = %d, want 0", mem.WriteCallCount)
	}
}

// TestWriteMemoryDownRetriesThreeTimes verifies that Write retries
// up to three times when the memory backend is unavailable.
// The mock is configured to fail all writes, and the test confirms
// that the retry limit is reached before returning an error.
func TestWriteMemoryDownRetriesThreeTimes(t *testing.T) {
	mem := mocks.NewMemoryMock()
	mem.ShouldFailWrites = true
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	err := iface.Write(newWritePayload(t, types.DataTypeAuditLog))
	if err == nil {
		t.Fatal("Write() error = nil, want retry exhaustion error")
	}
	if mem.WriteCallCount != 3 {
		t.Fatalf("WriteCallCount = %d, want 3", mem.WriteCallCount)
	}
}

// TestWriteAllRetriesExhaustedCallsWriteFailureHandler verifies that
// the configured write failure handler is invoked after all retry attempts
// are exhausted. It also checks that the handler receives the original
// payload and a non-nil error.
func TestWriteAllRetriesExhaustedCallsWriteFailureHandler(t *testing.T) {
	mem := mocks.NewMemoryMock()
	mem.ShouldFailWrites = true
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	var called bool
	iface.SetWriteFailureHandler(func(payload types.OrchestratorMemoryWritePayload, err error) {
		called = true
		if payload.TaskID != "task-1" {
			t.Fatalf("payload.TaskID = %q, want task-1", payload.TaskID)
		}
		if err == nil {
			t.Fatal("handler err = nil, want non-nil")
		}
	})

	err := iface.Write(newWritePayload(t, types.DataTypeRecoveryEvent))
	if err == nil {
		t.Fatal("Write() error = nil, want retry exhaustion error")
	}
	if !called {
		t.Fatal("write failure handler was not called")
	}
}

// TestReadReturnsRecordsOrderedByTimestampAscending verifies that Read
// delegates to the underlying memory client and returns records sorted
// in ascending timestamp order.
func TestReadReturnsRecordsOrderedByTimestampAscending(t *testing.T) {
	mem := mocks.NewMemoryMock()
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	later := time.Now().UTC()
	earlier := later.Add(-1 * time.Minute)

	mem.Records = []types.MemoryRecord{
		{
			OrchestratorTaskRef: "orch-1",
			TaskID:              "task-1",
			DataType:            types.DataTypeTaskState,
			Timestamp:           later,
			Payload:             mustJSON(t, map[string]any{"state": "RUNNING"}),
		},
		{
			OrchestratorTaskRef: "orch-1",
			TaskID:              "task-1",
			DataType:            types.DataTypeTaskState,
			Timestamp:           earlier,
			Payload:             mustJSON(t, map[string]any{"state": "DISPATCHED"}),
		},
	}

	records, err := iface.Read(types.MemoryQuery{
		TaskID:   "task-1",
		DataType: types.DataTypeTaskState,
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records len = %d, want 2", len(records))
	}
	if !records[0].Timestamp.Equal(earlier) {
		t.Fatalf("records[0].Timestamp = %v, want %v", records[0].Timestamp, earlier)
	}
	if !records[1].Timestamp.Equal(later) {
		t.Fatalf("records[1].Timestamp = %v, want %v", records[1].Timestamp, later)
	}
}

// TestReadInvalidDataTypeReturnsError verifies that Read rejects unsupported
// data types before reaching the underlying memory client.
func TestReadInvalidDataTypeReturnsError(t *testing.T) {
	mem := mocks.NewMemoryMock()
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	_, err := iface.Read(types.MemoryQuery{
		TaskID:   "task-1",
		DataType: "bad_type",
	})
	if err == nil {
		t.Fatal("Read() error = nil, want invalid data_type error")
	}
	if !strings.Contains(err.Error(), "invalid data_type") {
		t.Fatalf("Read() error = %v, want invalid data_type", err)
	}
	if mem.ReadCallCount != 0 {
		t.Fatalf("ReadCallCount = %d, want 0", mem.ReadCallCount)
	}
}

// TestReadLatestReturnsNewestRecord verifies that ReadLatest returns the most
// recent record for the given task and data type.
func TestReadLatestReturnsNewestRecord(t *testing.T) {
	mem := mocks.NewMemoryMock()
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	older := time.Now().UTC().Add(-2 * time.Minute)
	newer := older.Add(1 * time.Minute)

	mem.Records = []types.MemoryRecord{
		{
			OrchestratorTaskRef: "orch-1",
			TaskID:              "task-1",
			DataType:            types.DataTypeTaskState,
			Timestamp:           older,
			Payload:             mustJSON(t, map[string]any{"state": "DISPATCHED"}),
		},
		{
			OrchestratorTaskRef: "orch-1",
			TaskID:              "task-1",
			DataType:            types.DataTypeTaskState,
			Timestamp:           newer,
			Payload:             mustJSON(t, map[string]any{"state": "RUNNING"}),
		},
	}

	record, err := iface.ReadLatest("task-1", types.DataTypeTaskState)
	if err != nil {
		t.Fatalf("ReadLatest() error = %v", err)
	}
	if !record.Timestamp.Equal(newer) {
		t.Fatalf("record.Timestamp = %v, want %v", record.Timestamp, newer)
	}
}

// TestReadLatestEmptyTaskIDReturnsError verifies that ReadLatest rejects
// empty task IDs before calling the underlying memory client.
func TestReadLatestEmptyTaskIDReturnsError(t *testing.T) {
	mem := mocks.NewMemoryMock()
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	_, err := iface.ReadLatest("", types.DataTypeTaskState)
	if err == nil {
		t.Fatal("ReadLatest() error = nil, want task_id required error")
	}
	if !strings.Contains(err.Error(), "task_id is required") {
		t.Fatalf("ReadLatest() error = %v, want task_id is required", err)
	}
}

// TestPingDelegatesToClient verifies that Ping forwards the reachability check
// to the underlying memory client.
func TestPingDelegatesToClient(t *testing.T) {
	mem := mocks.NewMemoryMock()
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	if err := iface.Ping(); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if mem.PingCallCount != 1 {
		t.Fatalf("PingCallCount = %d, want 1", mem.PingCallCount)
	}
}

// TestPingReturnsUnderlyingClientError verifies that Ping returns the
// underlying memory client error unchanged when the dependency is unreachable.
func TestPingReturnsUnderlyingClientError(t *testing.T) {
	mem := mocks.NewMemoryMock()
	mem.PingFails = true
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	err := iface.Ping()
	if err == nil {
		t.Fatal("Ping() error = nil, want underlying client error")
	}
	if !strings.Contains(err.Error(), "memory component unreachable") {
		t.Fatalf("Ping() error = %v, want memory component unreachable", err)
	}
}

// newWritePayload creates a minimal valid OrchestratorMemoryWritePayload
// for tests. The caller provides the DataType so tests can simulate either
// valid or invalid payloads while keeping all other fields well-formed.
func newWritePayload(t *testing.T, dataType string) types.OrchestratorMemoryWritePayload {
	t.Helper()

	return types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: "orch-1",
		TaskID:              "task-1",
		DataType:            dataType,
		Timestamp:           time.Now().UTC(),
		Payload:             mustJSON(t, map[string]any{"state": "DISPATCHED"}),
	}
}

// mustJSON marshals test data into json.RawMessage and fails the test
// immediately if the payload cannot be encoded.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return raw
}
