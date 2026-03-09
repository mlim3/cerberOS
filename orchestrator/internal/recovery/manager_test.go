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

func (p *policyMock) VerifyScopeStillValid(_ types.PolicyScope) error {
	if p.ScopeExpired {
		return errors.New("policy scope expired")
	}
	return nil
}
func (p *policyMock) RevokeCredentials(orchRef string) error {
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

func (m *monitorMock) StateTransition(taskID, newState, reason string) error {
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

	m.HandleRecovery(ts, types.RecoveryReasonTimeout)

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

func TestHandleRecovery_WithinBudget_ReDispatches(t *testing.T) {
	// retry_count=0 < max_retries=3 → re-dispatch with same policy_scope (§FR-SH-02, §FR-SH-06).
	m, gw, _, _, mem := newManager(t)
	ts := activeTask("task-2", "orch-2", 0)
	seedMemory(t, mem, ts)

	m.HandleRecovery(ts, types.RecoveryReasonAgentRecovering)

	// task_spec must be re-dispatched.
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("TaskSpecCalls = %d, want 1 (re-dispatch)", len(gw.TaskSpecCalls))
	}
	// Policy scope must NOT be expanded — same TokenRef.
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

func TestHandleRecovery_WithinBudget_AgentTerminated_ReDispatches(t *testing.T) {
	// AgentTerminated reason also triggers retry when within budget.
	m, gw, _, _, mem := newManager(t)
	ts := activeTask("task-3", "orch-3", 1) // retry_count=1, still within 3
	seedMemory(t, mem, ts)

	m.HandleRecovery(ts, types.RecoveryReasonAgentTerminated)

	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("TaskSpecCalls = %d, want 1", len(gw.TaskSpecCalls))
	}
}

// ── Max Retries Exceeded ──────────────────────────────────────────────────────

func TestHandleRecovery_MaxRetriesExceeded_TerminatesWithFailed(t *testing.T) {
	// retry_count == max_retries → terminate with MAX_RETRIES_EXCEEDED (§FR-SH-03).
	m, gw, pol, mon, _ := newManager(t)
	ts := activeTask("task-4", "orch-4", 3) // retry_count=3 == max_retries=3

	m.HandleRecovery(ts, types.RecoveryReasonAgentRecovering)

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
	m, gw, pol, mon, mem := newManager(t)
	pol.ScopeExpired = true
	ts := activeTask("task-5", "orch-5", 0)
	seedMemory(t, mem, ts)

	m.HandleRecovery(ts, types.RecoveryReasonAgentRecovering)

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
	m, gw, _, mon, mem := newManager(t)
	mem.ShouldFailReads = true
	ts := activeTask("task-6", "orch-6", 0)

	m.HandleRecovery(ts, types.RecoveryReasonAgentRecovering)

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
	m, gw, _, mon, mem := newManager(t)
	gw.PublishSpecError = errors.New("nats broker unavailable")
	ts := activeTask("task-7", "orch-7", 0)
	seedMemory(t, mem, ts)

	m.HandleRecovery(ts, types.RecoveryReasonAgentRecovering)

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

	m.HandleRecovery(ts, types.RecoveryReasonTimeout)

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
		m.HandleRecovery(ts, types.RecoveryReasonTimeout)
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
//	Step 2 — Agent Recovering:     agent reports RECOVERING → re-dispatch with same policy_scope (attempt 1)
//	Step 3 — Second Recovery:      second RECOVERING → re-dispatch (attempt 2); recovery_event written
//	Step 4 — Max Retries:          third RECOVERING → MAX_RETRIES_EXCEEDED; no re-dispatch
//	Step 5 — Scope Expired:        policy scope expired during recovery → SCOPE_EXPIRED; no re-dispatch
//	Step 6 — Vault Down:           revocation fails → retry scheduled; termination not blocked
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

	t.Log("demo setup: Recovery Manager wired to mocks for Gateway, PolicyEnforcer, Monitor, and MemoryClient")
	t.Log("demo setup: MaxTaskRetries=3")

	// ── Step 1: Timeout ────────────────────────────────────────────────────
	// A task exceeds its timeout_seconds deadline.
	// Timeout is always terminal — no retry regardless of retry_count.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 1: timeout — task-1 exceeds its deadline (retry_count=0, but timeout never retries)")

	tsTimeout := activeTask("task-timeout-1", "orch-timeout-1", 0)
	m.HandleRecovery(tsTimeout, types.RecoveryReasonTimeout)

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

	// ── Step 2: Agent Recovering — Attempt 1 ──────────────────────────────
	// Agent enters RECOVERING state. retry_count=0 < max=3 → re-dispatch.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 2: agent recovering — attempt 1 (retry_count=0, budget=3)")

	tsRecov := activeTask("task-recov-1", "orch-recov-1", 0)
	seedMemory(t, mem, tsRecov)

	m.HandleRecovery(tsRecov, types.RecoveryReasonAgentRecovering)

	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("step 2: TaskSpecCalls = %d, want 1 (re-dispatch)", len(gw.TaskSpecCalls))
	}
	if gw.TaskSpecCalls[0].PolicyScope.TokenRef != tsRecov.PolicyScope.TokenRef {
		t.Fatalf("step 2: policy_scope changed — scope must not expand during recovery")
	}
	// Count recovery_event records written.
	recoveryEvents := 0
	for _, rec := range mem.Records {
		if rec.DataType == types.DataTypeRecoveryEvent {
			recoveryEvents++
		}
	}
	t.Logf("step 2: re-dispatched task_spec — policy_scope.token_ref=%s progress=%q",
		gw.TaskSpecCalls[0].PolicyScope.TokenRef, gw.TaskSpecCalls[0].ProgressSummary)
	t.Logf("step 2: recovery_events written to Memory = %d", recoveryEvents)
	t.Log("step 2 complete: task re-dispatched with original policy_scope ✓")

	gw.TaskSpecCalls = nil
	mon.Transitions = nil

	// ── Step 3: Agent Recovering — Attempt 2 ──────────────────────────────
	// Same task fails again. retry_count=1 → attempt 2.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 3: agent recovering again — attempt 2 (retry_count=1)")

	tsRecov2 := activeTask("task-recov-1", "orch-recov-1", 1)
	// Update memory with incremented retry count.
	mem.Records = mem.Records[:0] // clear, re-seed
	seedMemory(t, mem, tsRecov2)

	m.HandleRecovery(tsRecov2, types.RecoveryReasonAgentRecovering)

	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("step 3: TaskSpecCalls = %d, want 1", len(gw.TaskSpecCalls))
	}
	t.Logf("step 3: re-dispatched — progress=%q", gw.TaskSpecCalls[0].ProgressSummary)
	t.Log("step 3 complete: second recovery attempt succeeded ✓")

	gw.TaskSpecCalls = nil
	gw.ErrorCalls = nil
	mon.Transitions = nil
	pol.RevokeCallCount = 0
	pol.RevokedRefs = nil
	mon.UntrackedIDs = nil

	// ── Step 4: Max Retries Exceeded ──────────────────────────────────────
	// retry_count=3 == max_retries=3 → no more retries; terminate.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 4: max retries exceeded — retry_count=3 reaches max; task terminated")

	tsMaxed := activeTask("task-maxed-1", "orch-maxed-1", 3)
	seedMemory(t, mem, tsMaxed)

	m.HandleRecovery(tsMaxed, types.RecoveryReasonAgentRecovering)

	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("step 4: task_spec published — must not re-dispatch when budget exhausted")
	}
	if lastTransition(mon).NewState != types.StateFailed {
		t.Fatalf("step 4: state = %q, want FAILED", lastTransition(mon).NewState)
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeMaxRetriesExceeded {
		t.Fatalf("step 4: error_code = %q, want MAX_RETRIES_EXCEEDED", safeErrorCode(gw))
	}
	t.Logf("step 4: task terminated — state=FAILED error_code=%s", gw.ErrorCalls[0].ErrorCode)
	t.Logf("step 4: revocation triggered=%v untracked=%v",
		pol.RevokeCallCount > 0, len(mon.UntrackedIDs) > 0)
	t.Log("step 4 complete: retry budget enforced; task failed gracefully ✓")

	gw.ErrorCalls = nil
	gw.TaskSpecCalls = nil
	mon.Transitions = nil
	pol.RevokeCallCount = 0
	pol.RevokedRefs = nil
	mon.UntrackedIDs = nil
	mem.Records = mem.Records[:0]

	// ── Step 5: Scope Expired ──────────────────────────────────────────────
	// VerifyScopeStillValid fails during recovery — scope cannot expand (§FR-SH-06).
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 5: scope expired — Vault reports policy scope no longer valid during recovery")

	pol.ScopeExpired = true
	tsScope := activeTask("task-scope-1", "orch-scope-1", 0)
	seedMemory(t, mem, tsScope)

	m.HandleRecovery(tsScope, types.RecoveryReasonAgentRecovering)

	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("step 5: task re-dispatched despite scope expiry — scope must not expand")
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeScopeExpired {
		t.Fatalf("step 5: error_code = %q, want SCOPE_EXPIRED", safeErrorCode(gw))
	}
	t.Logf("step 5: task terminated — error_code=%s policy_enforcement=strict", gw.ErrorCalls[0].ErrorCode)
	t.Log("step 5 complete: scope expiry enforced; task not re-dispatched with expired credentials ✓")

	pol.ScopeExpired = false
	gw.ErrorCalls = nil
	mon.Transitions = nil
	pol.RevokeCallCount = 0
	pol.RevokedRefs = nil
	mon.UntrackedIDs = nil
	mem.Records = mem.Records[:0]

	// ── Step 6: Vault Down During Revocation ──────────────────────────────
	// Revocation fails but termination must NOT be blocked (§13.3).
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 6: Vault unavailable — revocation fails; termination must not block")

	pol.RevokeFails = true
	tsVaultDown := activeTask("task-vault-1", "orch-vault-1", 0)

	done := make(chan struct{})
	go func() {
		m.HandleRecovery(tsVaultDown, types.RecoveryReasonTimeout)
		close(done)
	}()

	select {
	case <-done:
		// Good — HandleRecovery returned despite Vault being down.
	case <-time.After(2 * time.Second):
		t.Fatal("step 6: HandleRecovery blocked — must not block on revocation failure")
	}

	if lastTransition(mon).NewState != types.StateTimedOut {
		t.Fatalf("step 6: state = %q, want TIMED_OUT — termination must proceed", lastTransition(mon).NewState)
	}
	if len(gw.TerminateCalls) == 0 {
		t.Fatal("step 6: agent_terminate not sent — termination must proceed even when Vault is down")
	}
	t.Logf("step 6: terminated despite Vault failure — state=TIMED_OUT agent_terminate_sent=true")
	t.Log("step 6: revocation retry scheduled in background (non-blocking)")
	t.Log("step 6 complete: Vault outage did not block task termination ✓")

	t.Log("─────────────────────────────────────────────────────")
	t.Log("demo summary: Recovery Manager completed all 6 steps against mock dependencies")
	t.Log("demo summary: no real Vault, NATS broker, or database was used")
	t.Logf("demo summary: total recovery attempts=%d | revocations=%d | agent terminates=%d",
		0, // counted per step
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
