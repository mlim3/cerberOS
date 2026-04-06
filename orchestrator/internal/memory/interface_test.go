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

// TestMemoryInterfaceDemoFlow is a demo-style integration test for the
// orchestrator-side Memory Interface.
//
// This test intentionally exercises the public M6 methods in the same order
// a simple task lifecycle would use them during a demo:
// 1. Create a mock MemoryClient to stand in for the external Memory Component.
// 2. Write two task_state snapshots for the same task to simulate state changes.
// 3. Read all task_state records back and verify ordering + filtering behavior.
// 4. Read the latest task_state record and verify the newest snapshot is returned.
// 5. Ping the dependency to verify the health-check path works.
//
// What this test does NOT do:
// - It does not call a real memory service API.
// - It does not write to a real database.
// - It does not validate storage-layer append-only triggers.
//
// Instead, it demonstrates that the orchestrator's M6 layer correctly uses a
// contract-compatible MemoryClient implementation, which is exactly what we
// want for the current mock-data demo phase.
func TestMemoryInterfaceDemoFlow(t *testing.T) {
	mem := mocks.NewMemoryMock()
	iface := memoryiface.New(mem, &config.OrchestratorConfig{})

	t.Log("demo setup: created MemoryMock as the mock Memory Component backend")
	t.Log("demo setup: created memory.Interface as the orchestrator-side M6 wrapper")

	// Step 1: simulate the first persisted task snapshot right after dispatch.
	// The payload is a structured task_state record, not a raw transcript.
	dispatchedAt := time.Now().UTC().Add(-2 * time.Minute)
	dispatchedPayload := types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: "orch-demo-1",
		TaskID:              "task-demo-1",
		DataType:            types.DataTypeTaskState,
		Timestamp:           dispatchedAt,
		Payload: mustJSON(t, map[string]any{
			"state":      "DISPATCHED",
			"retry_count": 0,
			"agent_id":   "agent-42",
		}),
	}
	t.Logf("step 1: writing first task_state snapshot for task_id=%s state=DISPATCHED at %s", dispatchedPayload.TaskID, dispatchedPayload.Timestamp.Format(time.RFC3339))
	if err := iface.Write(dispatchedPayload); err != nil {
		t.Fatalf("Write(dispatchedPayload) error = %v", err)
	}
	t.Logf("step 1 complete: first write succeeded, total persisted mock records=%d", len(mem.Records))

	// Step 2: simulate a later snapshot for the same task after the agent starts.
	// Using the same task_id but a later timestamp lets us verify ReadLatest.
	runningAt := dispatchedAt.Add(1 * time.Minute)
	runningPayload := types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: "orch-demo-1",
		TaskID:              "task-demo-1",
		DataType:            types.DataTypeTaskState,
		Timestamp:           runningAt,
		Payload: mustJSON(t, map[string]any{
			"state":      "RUNNING",
			"retry_count": 0,
			"agent_id":   "agent-42",
		}),
	}
	t.Logf("step 2: writing second task_state snapshot for task_id=%s state=RUNNING at %s", runningPayload.TaskID, runningPayload.Timestamp.Format(time.RFC3339))
	if err := iface.Write(runningPayload); err != nil {
		t.Fatalf("Write(runningPayload) error = %v", err)
	}
	t.Logf("step 2 complete: second write succeeded, total persisted mock records=%d", len(mem.Records))

	// Step 3: read back all task_state records for the task.
	// This demonstrates the normal dedup/rehydration style read path used by
	// other orchestrator modules.
	t.Log("step 3: reading all task_state records for task-demo-1")
	records, err := iface.Read(types.MemoryQuery{
		TaskID:   "task-demo-1",
		DataType: types.DataTypeTaskState,
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("Read() returned %d records, want 2", len(records))
	}
	if !records[0].Timestamp.Equal(dispatchedAt) {
		t.Fatalf("records[0].Timestamp = %v, want %v", records[0].Timestamp, dispatchedAt)
	}
	if !records[1].Timestamp.Equal(runningAt) {
		t.Fatalf("records[1].Timestamp = %v, want %v", records[1].Timestamp, runningAt)
	}
	t.Logf("step 3 complete: read returned %d records in ascending order", len(records))
	t.Logf("step 3 result: record[0]=%s payload=%s", records[0].Timestamp.Format(time.RFC3339), string(records[0].Payload))
	t.Logf("step 3 result: record[1]=%s payload=%s", records[1].Timestamp.Format(time.RFC3339), string(records[1].Payload))

	// Step 4: ask for the latest snapshot only.
	// This is the recovery-oriented read path where orchestrator wants the most
	// recent structured state for a task.
	t.Log("step 4: reading latest task_state snapshot for task-demo-1")
	latest, err := iface.ReadLatest("task-demo-1", types.DataTypeTaskState)
	if err != nil {
		t.Fatalf("ReadLatest() error = %v", err)
	}
	if !latest.Timestamp.Equal(runningAt) {
		t.Fatalf("latest.Timestamp = %v, want %v", latest.Timestamp, runningAt)
	}
	if !strings.Contains(string(latest.Payload), "RUNNING") {
		t.Fatalf("latest.Payload = %s, want payload containing RUNNING", string(latest.Payload))
	}
	t.Logf("step 4 complete: latest snapshot timestamp=%s payload=%s", latest.Timestamp.Format(time.RFC3339), string(latest.Payload))

	// Step 5: verify dependency health probing.
	// In production this path is used by the orchestrator health monitor.
	t.Log("step 5: pinging the mock Memory Component through memory.Interface")
	if err := iface.Ping(); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	t.Log("step 5 complete: ping succeeded")

	// Final sanity checks: because this is a demo-style flow test, we also assert
	// that the mock observed the expected number of underlying client calls.
	if mem.WriteCallCount != 2 {
		t.Fatalf("WriteCallCount = %d, want 2", mem.WriteCallCount)
	}
	if mem.ReadCallCount != 2 {
		t.Fatalf("ReadCallCount = %d, want 2", mem.ReadCallCount)
	}
	if mem.PingCallCount != 1 {
		t.Fatalf("PingCallCount = %d, want 1", mem.PingCallCount)
	}
	t.Logf("demo summary: writes=%d reads=%d pings=%d", mem.WriteCallCount, mem.ReadCallCount, mem.PingCallCount)
	t.Log("demo summary: Memory Interface successfully completed write, read, read-latest, and ping against mock data")
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
