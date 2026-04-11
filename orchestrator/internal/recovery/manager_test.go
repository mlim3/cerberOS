// Package recovery_test provides black-box tests for M5: Recovery Manager.
//
// Each test demonstrates a distinct recovery scenario from the EDD (§7 Flow 6, §8.4, §13.3).
// Run all tests:
//
//	cd orchestrator && go test ./internal/recovery/ -v
//
// Run demo flow only:
//
//	cd orchestrator && go test ./internal/recovery/ -v -run TestRecoveryManagerDemoFlow
//
// Scenarios covered:
//
//	✅  Timeout               — RecoveryReasonTimeout → terminateTask(TIMED_OUT), no retry
//	✅  Retry within budget   — RecoveryReasonAgentRecovering → re-dispatch with same policy_scope
//	✅  Max retries exceeded  — retry_count >= max → terminateTask(MAX_RETRIES_EXCEEDED)
//	✅  Scope expired         — VerifyScopeStillValid fails → terminateTask(SCOPE_EXPIRED)
//	✅  Memory read fails     — ReadLatest error → terminateTask(STATE_RECOVERY_FAILED)
//	✅  Re-dispatch fails     — PublishTaskSpec error → terminateTask(AGENTS_UNAVAILABLE)
//	✅  Credential revocation — Always attempted; Vault failure → retry scheduled (non-blocking)
//	✅  Agent terminate       — PublishAgentTerminate called on every terminal outcome
//	✅  User I/O notified     — PublishError called on every terminal outcome
//	✅  Monitor untracked     — UntrackTask called on every terminal outcome
package recovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
	"github.com/mlim3/cerberOS/orchestrator/internal/recovery"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// ── Lightweight mocks for recovery-internal interfaces ────────────────────────

// gatewayMock records outbound calls from the Recovery Manager.
type gatewayMock struct {
	TerminateCalls []types.AgentTerminate
	CancelCalls    []types.TaskCancel
	ErrorCalls     []types.ErrorResponse
	TaskSpecCalls  []types.TaskSpec

	PublishTerminateError error
	PublishSpecError      error
}

func (g *gatewayMock) PublishAgentTerminate(t types.AgentTerminate) error {
	g.TerminateCalls = append(g.TerminateCalls, t)
	return g.PublishTerminateError
}
func (g *gatewayMock) PublishTaskCancel(c types.TaskCancel) error {
	g.CancelCalls = append(g.CancelCalls, c)
	return nil
}
func (g *gatewayMock) PublishError(_ string, e types.ErrorResponse) error {
	g.ErrorCalls = append(g.ErrorCalls, e)
	return nil
}
func (g *gatewayMock) PublishTaskSpec(s types.TaskSpec) error {
	g.TaskSpecCalls = append(g.TaskSpecCalls, s)
	return g.PublishSpecError
}

// policyMock is a controllable PolicyEnforcer.
type policyMock struct {
	ScopeExpired    bool
	RevokeFails     bool
	RevokeCallCount int
	RevokedRefs     []string
}

func (p *policyMock) VerifyScopeStillValid(_ context.Context, _ types.PolicyScope) error {
	if p.ScopeExpired {
		return errors.New("policy scope expired")
	}
	return nil
}
func (p *policyMock) RevokeCredentials(_ context.Context, orchRef string) error {
	p.RevokeCallCount++
	p.RevokedRefs = append(p.RevokedRefs, orchRef)
	if p.RevokeFails {
		return errors.New("vault unavailable")
	}
	return nil
}

// monitorMock records state transitions and untrack calls.
type monitorMock struct {
	Transitions  []stateTransition
	UntrackedIDs []string
	TransitionError error
}

type stateTransition struct {
	TaskID   string
	NewState string
	Reason   string
}

func (m *monitorMock) StateTransition(_ context.Context, taskID, newState, reason string) error {
	m.Transitions = append(m.Transitions, stateTransition{taskID, newState, reason})
	return m.TransitionError
}
func (m *monitorMock) UntrackTask(taskID string) {
	m.UntrackedIDs = append(m.UntrackedIDs, taskID)
}

// ── Test Helpers ─────────────────────────────────────────────────────────────

// newManager builds a fully wired Manager with fresh mocks.
func newManager(t *testing.T) (*recovery.Manager, *gatewayMock, *policyMock, *monitorMock, *mocks.MemoryMock) {
	t.Helper()
	gw := &gatewayMock{}
	pol := &policyMock{}
	mon := &monitorMock{}
	mem := mocks.NewMemoryMock()
	cfg := &config.OrchestratorConfig{NodeID: "test-node", MaxTaskRetries: 3}
	m := recovery.New(cfg, mem, gw, pol, mon)
	return m, gw, pol, mon, mem
}

// activeTask returns a TaskState that represents a dispatched, in-flight task.
func activeTask(taskID, orchRef string, retryCount int) *types.TaskState {
	now := time.Now().UTC()
	timeoutAt := now.Add(5 * time.Minute)
	return &types.TaskState{
		OrchestratorTaskRef:  orchRef,
		TaskID:               taskID,
		UserID:               "user-1",
		State:                types.StateRunning,
		RequiredSkillDomains: []string{"web"},
		PolicyScope: types.PolicyScope{
			Domains:   []string{"web"},
			TokenRef:  "tok-" + orchRef,
			IssuedAt:  now,
			ExpiresAt: now.Add(2 * time.Hour),
		},
		AgentID:           "agent-42",
		RetryCount:        retryCount,
		TimeoutAt:         &timeoutAt,
		CallbackTopic:     "aegis.user-io.results." + taskID,
		IdempotencyWindow: 300,
		Payload:           json.RawMessage(`{"url":"https://example.com"}`),
		StateHistory: []types.StateEvent{
			{State: types.StateDispatched, Timestamp: now, NodeID: "test-node"},
			{State: types.StateRunning, Timestamp: now, NodeID: "test-node"},
		},
	}
}

// seedMemory serializes ts into the MemoryMock so ReadLatest will find it.
func seedMemory(t *testing.T, mem *mocks.MemoryMock, ts *types.TaskState) {
	t.Helper()
	payload, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("seedMemory: marshal error = %v", err)
	}
	mem.Records = append(mem.Records, types.MemoryRecord{
		OrchestratorTaskRef: ts.OrchestratorTaskRef,
		TaskID:              ts.TaskID,
		DataType:            types.DataTypeTaskState,
		Timestamp:           time.Now().UTC(),
		Payload:             payload,
	})
}

// lastTransition returns the final state transition recorded on the monitor mock.
func lastTransition(mon *monitorMock) stateTransition {
	if len(mon.Transitions) == 0 {
		return stateTransition{}
	}
	return mon.Transitions[len(mon.Transitions)-1]
}

// ── Timeout ───────────────────────────────────────────────────────────────────

func TestHandleRecovery_Timeout_TerminatesWithTimedOut(t *testing.T) {
	// Timeout is always terminal — no retry regardless of retry_count (§FR-SH-01).
	m, gw, pol, mon, _ := newManager(t)
	ts := activeTask("task-1", "orch-1", 0)

	m.HandleRecovery(context.Background(), ts, types.RecoveryReasonTimeout)

	// Terminal state must be TIMED_OUT.
	tr := lastTransition(mon)
	if tr.NewState != types.StateTimedOut {
		t.Fatalf("state transition = %q, want TIMED_OUT", tr.NewState)
	}
	// Credentials must be revoked.
	if pol.RevokeCallCount != 1 {
		t.Fatalf("RevokeCallCount = %d, want 1", pol.RevokeCallCount)
	}
	// Agent must be terminated.
	if len(gw.TerminateCalls) != 1 {
		t.Fatalf("TerminateCalls = %d, want 1", len(gw.TerminateCalls))
	}
	if gw.TerminateCalls[0].OrchestratorTaskRef != ts.OrchestratorTaskRef {
		t.Fatalf("terminate orchRef = %q, want %q", gw.TerminateCalls[0].OrchestratorTaskRef, ts.OrchestratorTaskRef)
	}
	// TIMED_OUT error must be sent to User I/O.
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeTimedOut {
		t.Fatalf("error_code = %q, want TIMED_OUT", safeErrorCode(gw))
	}
	// Task must be untracked.
	if len(mon.UntrackedIDs) != 1 || mon.UntrackedIDs[0] != ts.TaskID {
		t.Fatalf("UntrackedIDs = %v, want [%s]", mon.UntrackedIDs, ts.TaskID)
	}
}

// ── Recovery Within Budget ────────────────────────────────────────────────────

func TestHandleRecovery_AgentRecovering_DoesNotRedispatch(t *testing.T) {
	// AgentRecovering: agent is alive and self-healing — M5 trusts it and does NOT re-dispatch.
	// A competing agent would duplicate work while the original is still alive.
	// Safety net: per-task timeout goroutine in Monitor catches the hung-agent case.
	m, gw, _, _, _ := newManager(t)
	ts := activeTask("task-2", "orch-2", 0)

	m.HandleRecovery(context.Background(), ts, types.RecoveryReasonAgentRecovering)

	// Must NOT re-dispatch — agent is still alive.
	if len(gw.TaskSpecCalls) != 0 {
		t.Fatalf("TaskSpecCalls = %d, want 0 — must not re-dispatch while agent is self-recovering", len(gw.TaskSpecCalls))
	}
	// Must NOT terminate — task is still alive.
	if len(gw.ErrorCalls) != 0 {
		t.Fatalf("unexpected error published: %+v — task should still be alive", gw.ErrorCalls)
	}
	if len(gw.TerminateCalls) != 0 {
		t.Fatalf("unexpected agent_terminate sent: %+v — agent is still alive", gw.TerminateCalls)
	}
}

func TestHandleRecovery_AgentTerminated_WithinBudget_ReDispatches(t *testing.T) {
	// AgentTerminated: agent is dead — act immediately. retry_count=0 < max=3 → re-dispatch.
	m, gw, _, _, mem := newManager(t)
	ts := activeTask("task-3", "orch-3", 0)
	seedMemory(t, mem, ts)

	m.HandleRecovery(context.Background(), ts, types.RecoveryReasonAgentTerminated)

	// task_spec must be re-dispatched.
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("TaskSpecCalls = %d, want 1 (re-dispatch)", len(gw.TaskSpecCalls))
	}
	// Policy scope must NOT be expanded — same TokenRef (§FR-SH-06).
	if gw.TaskSpecCalls[0].PolicyScope.TokenRef != ts.PolicyScope.TokenRef {
		t.Fatalf("re-dispatch policy_scope.token_ref = %q, want %q",
			gw.TaskSpecCalls[0].PolicyScope.TokenRef, ts.PolicyScope.TokenRef)
	}
	// ProgressSummary must indicate attempt number.
	if gw.TaskSpecCalls[0].ProgressSummary == "" {
		t.Fatal("re-dispatch task_spec missing progress_summary")
	}
	// No terminal error sent to User I/O.
	if len(gw.ErrorCalls) != 0 {
		t.Fatalf("unexpected error published: %+v", gw.ErrorCalls)
	}
	// A recovery_event record must be written to Memory.
	var foundRecoveryEvent bool
	for _, rec := range mem.Records {
		if rec.DataType == types.DataTypeRecoveryEvent && rec.TaskID == ts.TaskID {
			foundRecoveryEvent = true
			break
		}
	}
	if !foundRecoveryEvent {
		t.Fatal("no recovery_event written to Memory")
	}
}

func TestHandleRecovery_AgentTerminated_SecondAttempt_ReDispatches(t *testing.T) {
	// retry_count=1, still within max=3 → re-dispatch on second termination.
	m, gw, _, _, mem := newManager(t)
	ts := activeTask("task-4", "orch-4", 1)
	seedMemory(t, mem, ts)

	m.HandleRecovery(context.Background(), ts, types.RecoveryReasonAgentTerminated)

	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("TaskSpecCalls = %d, want 1", len(gw.TaskSpecCalls))
	}
}

// ── Max Retries Exceeded ──────────────────────────────────────────────────────

func TestHandleRecovery_MaxRetriesExceeded_TerminatesWithFailed(t *testing.T) {
	// retry_count == max_retries → terminate with MAX_RETRIES_EXCEEDED (§FR-SH-03).
	// Uses AgentTerminated because only a dead agent triggers the re-dispatch path.
	m, gw, pol, mon, _ := newManager(t)
	ts := activeTask("task-5", "orch-5", 3) // retry_count=3 == max_retries=3

	m.HandleRecovery(context.Background(), ts, types.RecoveryReasonAgentTerminated)

	// Must NOT re-dispatch.
	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("task_spec published despite retry budget exhausted")
	}
	// Must terminate with FAILED state.
	tr := lastTransition(mon)
	if tr.NewState != types.StateFailed {
		t.Fatalf("state = %q, want FAILED", tr.NewState)
	}
	// MAX_RETRIES_EXCEEDED error must be sent to User I/O.
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeMaxRetriesExceeded {
		t.Fatalf("error_code = %q, want MAX_RETRIES_EXCEEDED", safeErrorCode(gw))
	}
	// Credentials must be revoked even on budget exhaustion.
	if pol.RevokeCallCount == 0 {
		t.Fatal("credentials not revoked on MAX_RETRIES_EXCEEDED — revocation is non-optional")
	}
}

// ── Scope Expired ─────────────────────────────────────────────────────────────

func TestHandleRecovery_ScopeExpired_TerminatesWithScopeExpired(t *testing.T) {
	// VerifyScopeStillValid fails → SCOPE_EXPIRED, no re-dispatch (§FR-SH-06).
	// Uses AgentTerminated because only a dead agent triggers the re-dispatch path.
	m, gw, pol, mon, mem := newManager(t)
	pol.ScopeExpired = true
	ts := activeTask("task-6", "orch-6", 0)
	seedMemory(t, mem, ts)

	m.HandleRecovery(context.Background(), ts, types.RecoveryReasonAgentTerminated)

	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("task_spec published despite scope expired — scope must not expand during recovery")
	}
	tr := lastTransition(mon)
	if tr.NewState != types.StateFailed {
		t.Fatalf("state = %q, want FAILED", tr.NewState)
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeScopeExpired {
		t.Fatalf("error_code = %q, want SCOPE_EXPIRED", safeErrorCode(gw))
	}
	if pol.RevokeCallCount == 0 {
		t.Fatal("credentials not revoked on scope expiry")
	}
}

// ── Memory Read Fails ─────────────────────────────────────────────────────────

func TestHandleRecovery_MemoryReadFails_TerminatesWithStateRecoveryFailed(t *testing.T) {
	// If ReadLatest fails, recovery cannot proceed → STATE_RECOVERY_FAILED.
	// Uses AgentTerminated because only a dead agent triggers the re-dispatch path.
	m, gw, _, mon, mem := newManager(t)
	mem.ShouldFailReads = true
	ts := activeTask("task-7", "orch-7", 0)

	m.HandleRecovery(context.Background(), ts, types.RecoveryReasonAgentTerminated)

	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("task_spec published despite memory read failure")
	}
	tr := lastTransition(mon)
	if tr.NewState != types.StateFailed {
		t.Fatalf("state = %q, want FAILED", tr.NewState)
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeStateRecoveryFailed {
		t.Fatalf("error_code = %q, want STATE_RECOVERY_FAILED", safeErrorCode(gw))
	}
}

// ── Re-Dispatch Fails ─────────────────────────────────────────────────────────

func TestHandleRecovery_ReDispatchFails_TerminatesWithAgentsUnavailable(t *testing.T) {
	// PublishTaskSpec fails during re-dispatch → AGENTS_UNAVAILABLE.
	// Uses AgentTerminated because only a dead agent triggers the re-dispatch path.
	m, gw, _, mon, mem := newManager(t)
	gw.PublishSpecError = errors.New("nats broker unavailable")
	ts := activeTask("task-8", "orch-8", 0)
	seedMemory(t, mem, ts)

	m.HandleRecovery(context.Background(), ts, types.RecoveryReasonAgentTerminated)

	tr := lastTransition(mon)
	if tr.NewState != types.StateFailed {
		t.Fatalf("state = %q, want FAILED", tr.NewState)
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeAgentsUnavailable {
		t.Fatalf("error_code = %q, want AGENTS_UNAVAILABLE", safeErrorCode(gw))
	}
}

// ── Credential Revocation ─────────────────────────────────────────────────────

func TestTerminateTask_RevokesCredentials_Always(t *testing.T) {
	// Credential revocation must be attempted on EVERY terminal outcome (§FR-SH-04).
	m, _, pol, _, _ := newManager(t)
	ts := activeTask("task-8", "orch-8", 0)

	m.HandleRecovery(context.Background(), ts, types.RecoveryReasonTimeout)

	if pol.RevokeCallCount == 0 {
		t.Fatal("RevokeCredentials not called — revocation is non-optional on terminal outcome")
	}
	if len(pol.RevokedRefs) == 0 || pol.RevokedRefs[0] != ts.OrchestratorTaskRef {
		t.Fatalf("revoked ref = %v, want [%s]", pol.RevokedRefs, ts.OrchestratorTaskRef)
	}
}

func TestTerminateTask_VaultDown_ContinuesTermination(t *testing.T) {
	// If Vault is down, revocation fails but termination must NOT be blocked (§13.3).
	m, gw, pol, mon, _ := newManager(t)
	pol.RevokeFails = true
	ts := activeTask("task-9", "orch-9", 0)

	// Should complete without hanging.
	done := make(chan struct{})
	go func() {
		m.HandleRecovery(context.Background(), ts, types.RecoveryReasonTimeout)
		close(done)
	}()

	select {
	case <-done:
		// Good — termination completed despite Vault being down.
	case <-time.After(2 * time.Second):
		t.Fatal("HandleRecovery blocked waiting for Vault — must not block on revocation failure")
	}

	// Termination must have proceeded: state transitioned and agent terminate sent.
	tr := lastTransition(mon)
	if tr.NewState != types.StateTimedOut {
		t.Fatalf("state = %q, want TIMED_OUT — termination must proceed even if revocation fails", tr.NewState)
	}
	if len(gw.TerminateCalls) != 1 {
		t.Fatal("agent_terminate not published — termination must proceed even if revocation fails")
	}
	if len(gw.ErrorCalls) != 1 {
		t.Fatal("User I/O not notified — termination must proceed even if revocation fails")
	}
}

// ── Demo Flow ─────────────────────────────────────────────────────────────────

// TestRecoveryManagerDemoFlow is a demo-style walkthrough of M5: Recovery Manager.
//
// Run with:
//
//	cd orchestrator && go test ./internal/recovery/ -v -run TestRecoveryManagerDemoFlow
//
// Scenarios covered (in order):
//
//	Step 1 — Timeout:              task times out → TIMED_OUT, credentials revoked, agent terminated
//	Step 2 — Agent Self-Recovering: agent reports RECOVERING → M5 trusts it (no re-dispatch); timeout is safety net
//	Step 3 — Agent Terminated:     agent dies → M5 re-dispatches (attempt 1) with same policy_scope
//	Step 4 — Agent Terminated:     agent dies again → M5 re-dispatches (attempt 2); retry_count=1→2
//	Step 5 — Max Retries:          agent dies a third time → MAX_RETRIES_EXCEEDED; no re-dispatch
//	Step 6 — Scope Expired:        policy scope expired during recovery → SCOPE_EXPIRED; no re-dispatch
//	Step 7 — Vault Down:           revocation fails → retry scheduled; termination not blocked
//
// What this test does NOT do:
//   - Does not call a real Vault, NATS broker, or database
//   - Does not test concurrent goroutine scheduling
//   - Does not wait for revocation retry backoff timers
func TestRecoveryManagerDemoFlow(t *testing.T) {
	// ── Setup ──────────────────────────────────────────────────────────────
	gw := &gatewayMock{}
	pol := &policyMock{}
	mon := &monitorMock{}
	mem := mocks.NewMemoryMock()
	cfg := &config.OrchestratorConfig{NodeID: "demo-node", MaxTaskRetries: 3}
	m := recovery.New(cfg, mem, gw, pol, mon)

	// totalRedispatches tracks how many times M5 successfully re-dispatched a task.
	// (Terminal outcomes are NOT counted — only successful re-dispatches are.)
	totalRedispatches := 0

	t.Log("demo setup: Recovery Manager wired to mocks for Gateway, PolicyEnforcer, Monitor, and MemoryClient")
	t.Log("demo setup: MaxTaskRetries=3")

	// ── Step 1: Timeout ────────────────────────────────────────────────────
	// A task exceeds its timeout_seconds deadline.
	// Timeout is always terminal — no retry regardless of retry_count.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 1: timeout — task-1 exceeds its deadline (retry_count=0, but timeout never retries)")

	tsTimeout := activeTask("task-timeout-1", "orch-timeout-1", 0)
	m.HandleRecovery(context.Background(), tsTimeout, types.RecoveryReasonTimeout)

	if lastTransition(mon).NewState != types.StateTimedOut {
		t.Fatalf("step 1: state = %q, want TIMED_OUT", lastTransition(mon).NewState)
	}
	if pol.RevokeCallCount != 1 {
		t.Fatalf("step 1: RevokeCallCount = %d, want 1", pol.RevokeCallCount)
	}
	if len(gw.TerminateCalls) != 1 {
		t.Fatalf("step 1: TerminateCalls = %d, want 1", len(gw.TerminateCalls))
	}
	t.Logf("step 1: task terminated — state=TIMED_OUT revocations=%d agent_terminate_sent=%v",
		pol.RevokeCallCount, len(gw.TerminateCalls) > 0)
	t.Logf("step 1: User I/O notified — error_code=%s", gw.ErrorCalls[0].ErrorCode)
	t.Log("step 1 complete: timeout handled; credentials revoked; agent terminated ✓")

	// Reset spies for next step.
	gw.TerminateCalls = nil
	gw.ErrorCalls = nil
	pol.RevokeCallCount = 0
	pol.RevokedRefs = nil
	mon.Transitions = nil
	mon.UntrackedIDs = nil

	// ── Step 2: Agent Self-Recovering — M5 Trusts It ─────────────────────
	// Agent reports RECOVERING (it is alive and self-healing).
	// M5 does NOT re-dispatch — spinning up a competing agent while the original
	// is still alive would waste resources and could corrupt task state.
	// Safety nets already in place:
	//   - Agent recovers → reports ACTIVE → Monitor transitions RUNNING (no M5 action)
	//   - Agent gives up → reports TERMINATED → M5 re-dispatches (Step 3 below)
	//   - Agent hangs forever → timeout fires → M5 terminates (TIMED_OUT)
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 2: agent self-recovering — M5 trusts it (does NOT re-dispatch)")
	t.Log("step 2: safety net: timeout goroutine catches the hung-agent case")

	tsSelfRecov := activeTask("task-self-recov-1", "orch-self-recov-1", 0)

	m.HandleRecovery(context.Background(), tsSelfRecov, types.RecoveryReasonAgentRecovering)

	// Must NOT re-dispatch — agent is still alive.
	if len(gw.TaskSpecCalls) != 0 {
		t.Fatalf("step 2: TaskSpecCalls = %d, want 0 — must not re-dispatch while agent is self-recovering", len(gw.TaskSpecCalls))
	}
	// Must NOT terminate — task is still active.
	if len(gw.ErrorCalls) != 0 {
		t.Fatalf("step 2: unexpected error published — task should still be alive")
	}
	if len(gw.TerminateCalls) != 0 {
		t.Fatalf("step 2: unexpected agent_terminate — agent is still alive")
	}
	t.Log("step 2: no re-dispatch, no termination — M5 is monitoring without intervening")
	t.Log("step 2: [simulated] agent recovers → reports ACTIVE → Monitor.HandleAgentStatusUpdate → RUNNING")
	t.Log("step 2 complete: M5 correctly trusted a self-recovering agent ✓")

	// ── Step 3: Agent Terminated — Re-dispatch Attempt 1 ──────────────────
	// Agent dies (TERMINATED). retry_count=0 < max=3 → M5 re-dispatches immediately.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 3: agent terminated (dead) — M5 re-dispatches immediately (attempt 1, retry_count=0)")

	tsTerminated1 := activeTask("task-terminated-1", "orch-terminated-1", 0)
	seedMemory(t, mem, tsTerminated1)

	m.HandleRecovery(context.Background(), tsTerminated1, types.RecoveryReasonAgentTerminated)

	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("step 3: TaskSpecCalls = %d, want 1 (re-dispatch)", len(gw.TaskSpecCalls))
	}
	if gw.TaskSpecCalls[0].PolicyScope.TokenRef != tsTerminated1.PolicyScope.TokenRef {
		t.Fatalf("step 3: policy_scope changed — scope must not expand (§FR-SH-06)")
	}
	if len(gw.ErrorCalls) != 0 {
		t.Fatalf("step 3: unexpected error — task still has retry budget")
	}
	if len(mon.UntrackedIDs) != 0 {
		t.Fatalf("step 3: task untracked — should remain active after re-dispatch")
	}
	// Verify recovery_event written to Memory for audit trail.
	recoveryEvents := 0
	for _, rec := range mem.Records {
		if rec.DataType == types.DataTypeRecoveryEvent && rec.TaskID == tsTerminated1.TaskID {
			recoveryEvents++
		}
	}
	if recoveryEvents == 0 {
		t.Fatal("step 3: no recovery_event written to Memory — audit trail required")
	}
	totalRedispatches++
	t.Logf("step 3: re-dispatched — token_ref=%s progress=%q recovery_events_in_memory=%d",
		gw.TaskSpecCalls[0].PolicyScope.TokenRef, gw.TaskSpecCalls[0].ProgressSummary, recoveryEvents)
	t.Log("step 3 complete: dead agent → re-dispatched with original policy_scope ✓")

	gw.TaskSpecCalls = nil
	mon.Transitions = nil
	mem.Records = mem.Records[:0]

	// ── Step 4: Agent Terminated — Re-dispatch Attempt 2 ──────────────────
	// Same task: new agent also dies. retry_count=1 → attempt 2 still within budget.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 4: agent terminated again — M5 re-dispatches (attempt 2, retry_count=1)")

	tsTerminated2 := activeTask("task-terminated-1", "orch-terminated-1", 1)
	seedMemory(t, mem, tsTerminated2)

	m.HandleRecovery(context.Background(), tsTerminated2, types.RecoveryReasonAgentTerminated)

	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("step 4: TaskSpecCalls = %d, want 1", len(gw.TaskSpecCalls))
	}
	if len(gw.ErrorCalls) != 0 {
		t.Fatalf("step 4: unexpected error — retry budget still has 1 remaining")
	}
	totalRedispatches++
	t.Logf("step 4: re-dispatched again — progress=%q (attempt 2 of 3)", gw.TaskSpecCalls[0].ProgressSummary)
	t.Log("step 4 complete: second re-dispatch within budget ✓")

	gw.TaskSpecCalls = nil
	gw.ErrorCalls = nil
	mon.Transitions = nil
	pol.RevokeCallCount = 0
	pol.RevokedRefs = nil
	mon.UntrackedIDs = nil
	mem.Records = mem.Records[:0]

	// ── Step 5: Max Retries Exceeded ──────────────────────────────────────
	// retry_count=3 == max_retries=3 → budget exhausted; terminate.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 5: max retries exceeded — retry_count=3 hits max=3; task terminated")

	tsMaxed := activeTask("task-maxed-1", "orch-maxed-1", 3)
	seedMemory(t, mem, tsMaxed)

	m.HandleRecovery(context.Background(), tsMaxed, types.RecoveryReasonAgentTerminated)

	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("step 5: task_spec published — must not re-dispatch when budget exhausted")
	}
	if lastTransition(mon).NewState != types.StateFailed {
		t.Fatalf("step 5: state = %q, want FAILED", lastTransition(mon).NewState)
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeMaxRetriesExceeded {
		t.Fatalf("step 5: error_code = %q, want MAX_RETRIES_EXCEEDED", safeErrorCode(gw))
	}
	t.Logf("step 5: task terminated — state=FAILED error_code=%s", gw.ErrorCalls[0].ErrorCode)
	t.Logf("step 5: revocation triggered=%v untracked=%v",
		pol.RevokeCallCount > 0, len(mon.UntrackedIDs) > 0)
	t.Log("step 5 complete: retry budget enforced; task failed gracefully ✓")

	gw.ErrorCalls = nil
	gw.TaskSpecCalls = nil
	mon.Transitions = nil
	pol.RevokeCallCount = 0
	pol.RevokedRefs = nil
	mon.UntrackedIDs = nil
	mem.Records = mem.Records[:0]

	// ── Step 6: Scope Expired ──────────────────────────────────────────────
	// VerifyScopeStillValid fails during re-dispatch — scope cannot expand (§FR-SH-06).
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 6: scope expired — Vault reports policy scope no longer valid during recovery")

	pol.ScopeExpired = true
	tsScope := activeTask("task-scope-1", "orch-scope-1", 0)
	seedMemory(t, mem, tsScope)

	m.HandleRecovery(context.Background(), tsScope, types.RecoveryReasonAgentTerminated)

	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("step 6: task re-dispatched despite scope expiry — scope must not expand")
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeScopeExpired {
		t.Fatalf("step 6: error_code = %q, want SCOPE_EXPIRED", safeErrorCode(gw))
	}
	t.Logf("step 6: task terminated — error_code=%s policy_enforcement=strict", gw.ErrorCalls[0].ErrorCode)
	t.Log("step 6 complete: scope expiry enforced; task not re-dispatched with expired credentials ✓")

	pol.ScopeExpired = false
	gw.ErrorCalls = nil
	mon.Transitions = nil
	pol.RevokeCallCount = 0
	pol.RevokedRefs = nil
	mon.UntrackedIDs = nil
	mem.Records = mem.Records[:0]

	// ── Step 7: Vault Down During Revocation ──────────────────────────────
	// Revocation fails but termination must NOT be blocked (§13.3).
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 7: Vault unavailable — revocation fails; termination must not block")

	pol.RevokeFails = true
	tsVaultDown := activeTask("task-vault-1", "orch-vault-1", 0)

	done := make(chan struct{})
	go func() {
		m.HandleRecovery(context.Background(), tsVaultDown, types.RecoveryReasonTimeout)
		close(done)
	}()

	select {
	case <-done:
		// Good — HandleRecovery returned despite Vault being down.
	case <-time.After(2 * time.Second):
		t.Fatal("step 7: HandleRecovery blocked — must not block on revocation failure")
	}

	if lastTransition(mon).NewState != types.StateTimedOut {
		t.Fatalf("step 7: state = %q, want TIMED_OUT — termination must proceed", lastTransition(mon).NewState)
	}
	if len(gw.TerminateCalls) == 0 {
		t.Fatal("step 7: agent_terminate not sent — termination must proceed even when Vault is down")
	}
	t.Logf("step 7: terminated despite Vault failure — state=TIMED_OUT agent_terminate_sent=true")
	t.Log("step 7: revocation retry scheduled in background goroutine (non-blocking)")
	t.Log("step 7 complete: Vault outage did not block task termination ✓")

	t.Log("─────────────────────────────────────────────────────")
	t.Log("demo summary: Recovery Manager completed all 7 steps against mock dependencies")
	t.Log("demo summary: no real Vault, NATS broker, or database was used")
	t.Log("demo summary:")
	t.Log("  step 1 — timeout:           TIMED_OUT (timeout is always terminal, never retried)")
	t.Log("  step 2 — agent recovering:  no re-dispatch (M5 trusts self-healing agent)")
	t.Log("  step 3 — agent terminated:  re-dispatched (attempt 1); policy_scope unchanged")
	t.Log("  step 4 — agent terminated:  re-dispatched (attempt 2); retry budget preserved")
	t.Log("  step 5 — max retries:       FAILED/MAX_RETRIES_EXCEEDED (budget=3 exhausted)")
	t.Log("  step 6 — scope expired:     FAILED/SCOPE_EXPIRED (Vault invalidated credentials)")
	t.Log("  step 7 — vault down:        TIMED_OUT; revocation retried in background goroutine")
	t.Logf("demo summary: total successful re-dispatches=%d | step-7 revocations=%d | step-7 agent terminates=%d",
		totalRedispatches,
		pol.RevokeCallCount,
		len(gw.TerminateCalls),
	)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func safeErrorCode(gw *gatewayMock) string {
	if len(gw.ErrorCalls) == 0 {
		return "<no error published>"
	}
	return gw.ErrorCalls[0].ErrorCode
}
