package monitor_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
	"github.com/mlim3/cerberOS/orchestrator/internal/monitor"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// recoveryMock records recovery calls from the Monitor.
type recoveryMock struct {
	Calls []recoveryCall
}

type recoveryCall struct {
	TaskID string
	Reason types.RecoveryReason
	State  string
}

func (r *recoveryMock) HandleRecovery(_ context.Context, ts *types.TaskState, reason types.RecoveryReason) {
	if ts == nil {
		return
	}
	r.Calls = append(r.Calls, recoveryCall{
		TaskID: ts.TaskID,
		Reason: reason,
		State:  ts.State,
	})
}

func newMonitor(t *testing.T) (*monitor.Monitor, *mocks.MemoryMock, *recoveryMock) {
	t.Helper()

	mem := mocks.NewMemoryMock()
	rec := &recoveryMock{}
	cfg := &config.OrchestratorConfig{
		NodeID: "test-node",
	}
	m := monitor.New(cfg, mem, rec)
	return m, mem, rec
}

func makeTaskState(taskID, state string) *types.TaskState {
	now := time.Now().UTC()
	timeoutAt := now.Add(2 * time.Second)

	return &types.TaskState{
		OrchestratorTaskRef:  "orch-" + taskID,
		TaskID:               taskID,
		UserID:               "user-1",
		State:                state,
		RequiredSkillDomains: []string{"web"},
		PolicyScope: types.PolicyScope{
			Domains:   []string{"web"},
			TokenRef:  "tok-" + taskID,
			IssuedAt:  now,
			ExpiresAt: now.Add(1 * time.Hour),
		},
		RetryCount: 0,
		TimeoutAt:  &timeoutAt,
		StateHistory: []types.StateEvent{
			{
				State:     state,
				Timestamp: now,
				NodeID:    "test-node",
			},
		},
		CallbackTopic:     "aegis.user-io.results." + taskID,
		IdempotencyWindow: 300,
	}
}

func latestTaskStateRecord(t *testing.T, mem *mocks.MemoryMock, taskID string) types.TaskState {
	t.Helper()

	var last types.TaskState
	found := false

	for _, rec := range mem.Records {
		if rec.TaskID != taskID || rec.DataType != types.DataTypeTaskState {
			continue
		}
		if err := json.Unmarshal(rec.Payload, &last); err != nil {
			t.Fatalf("json.Unmarshal(task_state payload) error = %v", err)
		}
		found = true
	}

	if !found {
		t.Fatalf("no task_state record found for task_id=%s", taskID)
	}
	return last
}

func seedTaskState(t *testing.T, mem *mocks.MemoryMock, ts *types.TaskState) {
	t.Helper()

	raw, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	if err := mem.Write(types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: ts.OrchestratorTaskRef,
		TaskID:              ts.TaskID,
		DataType:            types.DataTypeTaskState,
		Timestamp:           time.Now().UTC(),
		Payload:             raw,
	}); err != nil {
		t.Fatalf("memory.Write() error = %v", err)
	}
}

// ── Rehydrate ────────────────────────────────────────────────────────────────

func TestRehydrateFromMemory_LoadsLatestNonTerminalTasks(t *testing.T) {
	m, mem, _ := newMonitor(t)

	ts1Old := makeTaskState("task-1", types.StateDispatchPending)
	ts1New := makeTaskState("task-1", types.StateRunning)
	ts1New.StateHistory = append(ts1New.StateHistory, types.StateEvent{
		State:     types.StateRunning,
		Timestamp: time.Now().UTC().Add(1 * time.Second),
		NodeID:    "test-node",
	})

	ts2Terminal := makeTaskState("task-2", types.StateCompleted)
	now := time.Now().UTC()
	ts2Terminal.CompletedAt = &now

	seedTaskState(t, mem, ts1Old)
	seedTaskState(t, mem, ts1New)
	seedTaskState(t, mem, ts2Terminal)

	if err := m.RehydrateFromMemory(); err != nil {
		t.Fatalf("RehydrateFromMemory() error = %v", err)
	}

	if got := m.GetActiveTaskCount(); got != 1 {
		t.Fatalf("GetActiveTaskCount() = %d, want 1", got)
	}
}

func TestRehydrateFromMemory_ReturnsErrorOnBadPayload(t *testing.T) {
	m, mem, _ := newMonitor(t)

	if err := mem.Write(types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: "orch-bad",
		TaskID:              "task-bad",
		DataType:            types.DataTypeTaskState,
		Timestamp:           time.Now().UTC(),
		Payload:             json.RawMessage(`{"state":`), // malformed json
	}); err != nil {
		t.Fatalf("memory.Write() error = %v", err)
	}

	err := m.RehydrateFromMemory()
	if err == nil {
		t.Fatal("RehydrateFromMemory() error = nil, want non-nil")
	}
}

// ── Track / Untrack ──────────────────────────────────────────────────────────

func TestTrackTaskAndUntrackTask(t *testing.T) {
	m, _, _ := newMonitor(t)

	ts := makeTaskState("task-10", types.StateDispatched)
	m.TrackTask(ts)

	if got := m.GetActiveTaskCount(); got != 1 {
		t.Fatalf("GetActiveTaskCount() after TrackTask = %d, want 1", got)
	}

	m.UntrackTask("task-10")

	if got := m.GetActiveTaskCount(); got != 0 {
		t.Fatalf("GetActiveTaskCount() after UntrackTask = %d, want 0", got)
	}
}

// ── StateTransition ──────────────────────────────────────────────────────────

func TestStateTransition_ValidTransitionPersistsState(t *testing.T) {
	m, mem, _ := newMonitor(t)

	ts := makeTaskState("task-20", types.StateDispatched)
	m.TrackTask(ts)

	if err := m.StateTransition(context.Background(), "task-20", types.StateRunning, "agent became active"); err != nil {
		t.Fatalf("StateTransition() error = %v", err)
	}

	rec := latestTaskStateRecord(t, mem, "task-20")
	if rec.State != types.StateRunning {
		t.Fatalf("persisted state = %q, want RUNNING", rec.State)
	}
	if len(rec.StateHistory) == 0 {
		t.Fatal("state history is empty")
	}
	last := rec.StateHistory[len(rec.StateHistory)-1]
	if last.State != types.StateRunning {
		t.Fatalf("last state history entry = %q, want RUNNING", last.State)
	}
	if last.Reason != "agent became active" {
		t.Fatalf("last reason = %q, want %q", last.Reason, "agent became active")
	}
}

func TestStateTransition_InvalidTransitionReturnsError(t *testing.T) {
	m, _, _ := newMonitor(t)

	ts := makeTaskState("task-21", types.StateDispatched)
	m.TrackTask(ts)

	err := m.StateTransition(context.Background(), "task-21", types.StateDispatchPending, "go backwards")
	if err == nil {
		t.Fatal("StateTransition() error = nil, want invalid transition error")
	}
}

func TestStateTransition_TerminalStateSetsCompletedAtAndErrorCode(t *testing.T) {
	m, mem, _ := newMonitor(t)

	ts := makeTaskState("task-22", types.StateRunning)
	m.TrackTask(ts)

	if err := m.StateTransition(context.Background(), "task-22", types.StateTimedOut, "deadline exceeded"); err != nil {
		t.Fatalf("StateTransition() error = %v", err)
	}

	rec := latestTaskStateRecord(t, mem, "task-22")
	if rec.State != types.StateTimedOut {
		t.Fatalf("persisted state = %q, want TIMED_OUT", rec.State)
	}
	if rec.ErrorCode != types.ErrCodeTimedOut {
		t.Fatalf("persisted error_code = %q, want TIMED_OUT", rec.ErrorCode)
	}
	if rec.CompletedAt == nil {
		t.Fatal("CompletedAt = nil, want non-nil")
	}

	if got := m.GetActiveTaskCount(); got != 0 {
		t.Fatalf("GetActiveTaskCount() after terminal transition = %d, want 0", got)
	}
}

// ── Agent Status Update Handling ─────────────────────────────────────────────

func TestHandleAgentStatusUpdate_ActiveMovesTaskToRunning(t *testing.T) {
	m, mem, _ := newMonitor(t)

	ts := makeTaskState("task-30", types.StateDispatched)
	m.TrackTask(ts)

	update := types.AgentStatusUpdate{
		TaskID: "task-30",
		State:  types.AgentStateActive,
	}

	if err := m.HandleAgentStatusUpdate(context.Background(), update); err != nil {
		t.Fatalf("HandleAgentStatusUpdate() error = %v", err)
	}

	rec := latestTaskStateRecord(t, mem, "task-30")
	if rec.State != types.StateRunning {
		t.Fatalf("persisted state = %q, want RUNNING", rec.State)
	}
}

func TestHandleAgentStatusUpdate_RecoveringTransitionsAndCallsRecoveryManager(t *testing.T) {
	m, mem, recMock := newMonitor(t)

	ts := makeTaskState("task-31", types.StateRunning)
	m.TrackTask(ts)

	update := types.AgentStatusUpdate{
		TaskID: "task-31",
		State:  types.AgentStateRecovering,
		Reason: "agent restarting",
	}

	if err := m.HandleAgentStatusUpdate(context.Background(), update); err != nil {
		t.Fatalf("HandleAgentStatusUpdate() error = %v", err)
	}

	rec := latestTaskStateRecord(t, mem, "task-31")
	if rec.State != types.StateRecovering {
		t.Fatalf("persisted state = %q, want RECOVERING", rec.State)
	}

	if len(recMock.Calls) != 1 {
		t.Fatalf("recovery calls = %d, want 1", len(recMock.Calls))
	}
	if recMock.Calls[0].TaskID != "task-31" {
		t.Fatalf("recovery taskID = %q, want task-31", recMock.Calls[0].TaskID)
	}
	if recMock.Calls[0].Reason != types.RecoveryReasonAgentRecovering {
		t.Fatalf("recovery reason = %q, want AGENT_RECOVERING", recMock.Calls[0].Reason)
	}
}

func TestHandleAgentStatusUpdate_TerminatedCallsRecoveryManager(t *testing.T) {
	m, _, recMock := newMonitor(t)

	ts := makeTaskState("task-32", types.StateRunning)
	m.TrackTask(ts)

	update := types.AgentStatusUpdate{
		TaskID: "task-32",
		State:  types.AgentStateTerminated,
		Reason: "agent crashed",
	}

	if err := m.HandleAgentStatusUpdate(context.Background(), update); err != nil {
		t.Fatalf("HandleAgentStatusUpdate() error = %v", err)
	}

	if len(recMock.Calls) != 1 {
		t.Fatalf("recovery calls = %d, want 1", len(recMock.Calls))
	}
	if recMock.Calls[0].Reason != types.RecoveryReasonAgentTerminated {
		t.Fatalf("recovery reason = %q, want AGENT_TERMINATED", recMock.Calls[0].Reason)
	}
}

func TestHandleAgentStatusUpdate_TerminatedDoesNothingForTerminalTask(t *testing.T) {
	m, _, recMock := newMonitor(t)

	ts := makeTaskState("task-33", types.StateCompleted)
	now := time.Now().UTC()
	ts.CompletedAt = &now
	m.TrackTask(ts)

	update := types.AgentStatusUpdate{
		TaskID: "task-33",
		State:  types.AgentStateTerminated,
	}

	if err := m.HandleAgentStatusUpdate(context.Background(), update); err != nil {
		t.Fatalf("HandleAgentStatusUpdate() error = %v", err)
	}

	if len(recMock.Calls) != 0 {
		t.Fatalf("recovery calls = %d, want 0", len(recMock.Calls))
	}
}

func TestHandleAgentStatusUpdate_UnknownStateReturnsError(t *testing.T) {
	m, _, _ := newMonitor(t)

	ts := makeTaskState("task-34", types.StateDispatched)
	m.TrackTask(ts)

	update := types.AgentStatusUpdate{
		TaskID: "task-34",
		State:  "SOMETHING_UNKNOWN",
	}

	err := m.HandleAgentStatusUpdate(context.Background(), update)
	if err == nil {
		t.Fatal("HandleAgentStatusUpdate() error = nil, want non-nil")
	}
}

// ── Timeout Monitoring ───────────────────────────────────────────────────────

func TestTrackTask_TimeoutTriggersRecoveryManager(t *testing.T) {
	m, _, recMock := newMonitor(t)

	now := time.Now().UTC()
	timeoutAt := now.Add(50 * time.Millisecond)

	ts := &types.TaskState{
		OrchestratorTaskRef:  "orch-timeout",
		TaskID:               "task-timeout",
		UserID:               "user-1",
		State:                types.StateDispatched,
		RequiredSkillDomains: []string{"web"},
		PolicyScope: types.PolicyScope{
			Domains:   []string{"web"},
			TokenRef:  "tok-timeout",
			IssuedAt:  now,
			ExpiresAt: now.Add(1 * time.Hour),
		},
		TimeoutAt: &timeoutAt,
		StateHistory: []types.StateEvent{
			{
				State:     types.StateDispatched,
				Timestamp: now,
				NodeID:    "test-node",
			},
		},
		CallbackTopic:     "aegis.user-io.results.timeout",
		IdempotencyWindow: 300,
	}

	m.TrackTask(ts)

	time.Sleep(120 * time.Millisecond)

	if len(recMock.Calls) != 1 {
		t.Fatalf("recovery calls = %d, want 1", len(recMock.Calls))
	}
	if recMock.Calls[0].TaskID != "task-timeout" {
		t.Fatalf("recovery taskID = %q, want task-timeout", recMock.Calls[0].TaskID)
	}
	if recMock.Calls[0].Reason != types.RecoveryReasonTimeout {
		t.Fatalf("recovery reason = %q, want TIMEOUT", recMock.Calls[0].Reason)
	}
}

// TestMonitorDemoFlow is a demo-style workflow test for M4: Task Monitor.
//
// This test intentionally walks through the main monitor responsibilities in a
// human-readable order suitable for presentation:
// 1. Rehydrate a non-terminal task from Memory on startup.
// 2. Process an ACTIVE agent update and transition the task to RUNNING.
// 3. Process a RECOVERING agent update and escalate recovery with reason.
// 4. Track a second task whose timeout expires and verify timeout recovery fires.
//
// What this test does NOT do:
// - It does not call a real database.
// - It does not call a real Recovery Manager implementation.
// - It does not validate concurrent scaling behavior.
//
// Instead, it demonstrates that the orchestrator's M4 layer correctly:
// - rehydrates active task state,
// - owns task state transitions,
// - reacts to agent status updates,
// - and escalates recovery with explicit reasons.
func TestMonitorDemoFlow(t *testing.T) {
	m, mem, rec := newMonitor(t)

	t.Log("demo setup: created MemoryMock as the mock Memory Component backend")
	t.Log("demo setup: created recoveryMock as the mock Recovery Manager")
	t.Log("demo setup: created monitor.Monitor as the orchestrator-side M4 monitor")

	// Step 1: simulate startup rehydration from Memory.
	// We seed one non-terminal DISPATCHED task so the monitor can load it.
	t.Log("step 1: seeding Memory with a non-terminal DISPATCHED task for startup rehydration")

	dispatchedTask := makeTaskState("task-demo-1", types.StateDispatched)
	dispatchedTask.TimeoutAt = ptrTime(time.Now().UTC().Add(2 * time.Minute))
	seedTaskState(t, mem, dispatchedTask)

	if err := m.RehydrateFromMemory(); err != nil {
		t.Fatalf("RehydrateFromMemory() error = %v", err)
	}
	if got := m.GetActiveTaskCount(); got != 1 {
		t.Fatalf("GetActiveTaskCount() after rehydrate = %d, want 1", got)
	}
	t.Logf("step 1 complete: monitor rehydrated %d active task", m.GetActiveTaskCount())

	// Step 2: agent reports ACTIVE.
	// This should move the task from DISPATCHED -> RUNNING and persist the new state.
	t.Log("step 2: sending agent_status_update ACTIVE for task-demo-1")

	activeUpdate := types.AgentStatusUpdate{
		TaskID: "task-demo-1",
		State:  types.AgentStateActive,
	}

	if err := m.HandleAgentStatusUpdate(context.Background(), activeUpdate); err != nil {
		t.Fatalf("HandleAgentStatusUpdate(ACTIVE) error = %v", err)
	}

	runningRec := latestTaskStateRecord(t, mem, "task-demo-1")
	if runningRec.State != types.StateRunning {
		t.Fatalf("persisted state = %q, want RUNNING", runningRec.State)
	}
	t.Logf("step 2 complete: task-demo-1 transitioned to %s and persisted to Memory", runningRec.State)

	// Step 3: agent reports RECOVERING.
	// This should move the task to RECOVERING and call the Recovery Manager
	// with reason AGENT_RECOVERING.
	t.Log("step 3: sending agent_status_update RECOVERING for task-demo-1")

	recoveringUpdate := types.AgentStatusUpdate{
		TaskID: "task-demo-1",
		State:  types.AgentStateRecovering,
		Reason: "agent restarting after transient failure",
	}

	if err := m.HandleAgentStatusUpdate(context.Background(), recoveringUpdate); err != nil {
		t.Fatalf("HandleAgentStatusUpdate(RECOVERING) error = %v", err)
	}

	recoveringRec := latestTaskStateRecord(t, mem, "task-demo-1")
	if recoveringRec.State != types.StateRecovering {
		t.Fatalf("persisted state = %q, want RECOVERING", recoveringRec.State)
	}
	if len(rec.Calls) != 1 {
		t.Fatalf("recovery calls = %d, want 1", len(rec.Calls))
	}
	if rec.Calls[0].Reason != types.RecoveryReasonAgentRecovering {
		t.Fatalf("recovery reason = %q, want AGENT_RECOVERING", rec.Calls[0].Reason)
	}
	t.Logf("step 3 complete: task-demo-1 transitioned to %s and recovery was escalated with reason=%s",
		recoveringRec.State, rec.Calls[0].Reason)

	// Step 4: track a second task with a very short timeout.
	// This demonstrates the per-task timeout enforcement path of the monitor.
	t.Log("step 4: tracking a second task with a short timeout to demonstrate timeout enforcement")

	now := time.Now().UTC()
	shortTimeout := now.Add(50 * time.Millisecond)
	timeoutTask := &types.TaskState{
		OrchestratorTaskRef:  "orch-demo-2",
		TaskID:               "task-demo-2",
		UserID:               "user-2",
		State:                types.StateDispatched,
		RequiredSkillDomains: []string{"web"},
		PolicyScope: types.PolicyScope{
			Domains:   []string{"web"},
			TokenRef:  "tok-demo-2",
			IssuedAt:  now,
			ExpiresAt: now.Add(1 * time.Hour),
		},
		TimeoutAt: &shortTimeout,
		StateHistory: []types.StateEvent{
			{
				State:     types.StateDispatched,
				Timestamp: now,
				NodeID:    "test-node",
			},
		},
		CallbackTopic:     "aegis.user-io.results.task-demo-2",
		IdempotencyWindow: 300,
	}

	m.TrackTask(timeoutTask)
	t.Log("step 4: waiting for timeout goroutine to fire")
	time.Sleep(120 * time.Millisecond)

	if len(rec.Calls) != 2 {
		t.Fatalf("recovery calls = %d, want 2 after timeout", len(rec.Calls))
	}
	if rec.Calls[1].TaskID != "task-demo-2" {
		t.Fatalf("timeout recovery taskID = %q, want task-demo-2", rec.Calls[1].TaskID)
	}
	if rec.Calls[1].Reason != types.RecoveryReasonTimeout {
		t.Fatalf("timeout recovery reason = %q, want TIMEOUT", rec.Calls[1].Reason)
	}
	t.Logf("step 4 complete: timeout recovery triggered for task-demo-2 with reason=%s", rec.Calls[1].Reason)

	// Final summary for presentation.
	t.Logf("demo summary: active_tasks=%d memory_writes=%d recovery_calls=%d",
		m.GetActiveTaskCount(), mem.WriteCallCount, len(rec.Calls))
	t.Logf("demo summary: recovery[0]=task_id=%s reason=%s", rec.Calls[0].TaskID, rec.Calls[0].Reason)
	t.Logf("demo summary: recovery[1]=task_id=%s reason=%s", rec.Calls[1].TaskID, rec.Calls[1].Reason)
	t.Log("demo summary: Monitor successfully demonstrated rehydration, state transition, agent recovery escalation, and timeout handling")
}

func ptrTime(t time.Time) *time.Time {
	return &t
}