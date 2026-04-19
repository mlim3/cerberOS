package dispatcher_test

// Regression guard for the stale decomposition-timeout bug.
//
// Symptom: a ChatGPT-style follow-up sent on an existing conversation
// returned "Planner Agent did not respond within 120 seconds" a few
// seconds after the user hit send. The dispatcher logs showed the
// planner had already responded for the follow-up attempt; the error
// came from a STALE decomposition-timeout goroutine left over from the
// PREVIOUS attempt on the same TaskID.
//
// Root cause: watchDecompositionTimeout only checked that the task was
// still in StateDecomposing under the same TaskID. It did not verify
// that the TaskState it found was the same ATTEMPT the timer was spawned
// for. When a follow-up reuses a TaskID whose prior attempt already
// completed, the prior timer is still sleeping; if it wakes while the
// follow-up happens to be in StateDecomposing, it fires on the follow-up.
//
// Fix: compare OrchestratorTaskRef (which is fresh per attempt) before
// declaring a timeout.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/dispatcher"
	ioclient "github.com/mlim3/cerberOS/orchestrator/internal/io"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// newDispatcherWithTimeout is a variant of newDispatcher with a custom
// DecompositionTimeoutSeconds. Lives here rather than next to newDispatcher
// so this regression test stays self-contained.
func newDispatcherWithTimeout(_ *testing.T, timeoutSeconds int) (*dispatcher.Dispatcher, *gatewayMock, *policyMock, *monitorMock, *executorMock, *mocks.MemoryMock) {
	gw := &gatewayMock{}
	pol := &policyMock{}
	mon := &monitorMock{}
	exec := &executorMock{}
	mem := mocks.NewMemoryMock()

	cfg := &config.OrchestratorConfig{
		NodeID:                      "test-node-stale-timer",
		DecompositionTimeoutSeconds: timeoutSeconds,
		MaxSubtasksPerPlan:          20,
		PlanExecutorMaxParallel:     5,
		PlanApprovalMode:            "off",
	}
	d := dispatcher.New(cfg, mem, nil /* vault unused */, gw, pol, mon, exec, ioclient.New("") /* disabled */)
	return d, gw, pol, mon, exec, mem
}

// TestStaleDecompositionTimer_DoesNotFailFollowUp replays the exact
// sequence that produced the spurious "did not respond within 120 seconds"
// error on a9-stefan-chemero:
//
//  1. Submit attempt A on TaskID X. Goroutine GA starts sleeping for the
//     configured decomposition timeout.
//  2. Attempt A completes before its timer expires. activeTasks[X] is
//     deleted. GA is still sleeping — nothing cancels it.
//  3. Submit attempt B on the SAME TaskID X (follow-up). A new
//     orchestrator_task_ref is generated; activeTasks[X] now points at
//     attempt B in StateDecomposing. Goroutine GB starts its own timer.
//  4. Wait past the original timeout window so GA wakes up.
//  5. Resolve attempt B (HandleDecompositionResponse) before GB fires, so
//     the test observes exactly GA's behaviour.
//
// Assertions:
//   - No DECOMPOSITION_TIMEOUT error was published for the follow-up.
//   - The decompositionFailed metric did not increment.
//   - Attempt B reached StatePlanActive as expected.
func TestStaleDecompositionTimer_DoesNotFailFollowUp(t *testing.T) {
	// timeoutSec = 1 s keeps the test fast. attemptBDelay puts a 500 ms gap
	// between A's GA deadline and B's GB deadline, giving the test a wide
	// window to (a) observe that GA did NOT fire on B, and (b) resolve B
	// before GB fires on its own legitimately.
	const (
		timeoutSec     = 1
		attemptBDelay  = 500 * time.Millisecond
		settleAfterGA  = 150 * time.Millisecond
		settleAfterGB  = 600 * time.Millisecond
	)
	d, gw, _, _, exec, _ := newDispatcherWithTimeout(t, timeoutSec)
	ctx := context.Background()

	taskID := "550e8400-e29b-41d4-a716-446655440aaa"

	// ── Attempt A ─────────────────────────────────────────────────────────
	taskA := validTask(taskID)
	if err := d.HandleInboundTask(ctx, taskA); err != nil {
		t.Fatalf("attempt A HandleInboundTask error = %v", err)
	}
	planA := validPlan(taskID)
	if err := d.HandleDecompositionResponse(ctx, decompositionResponse(taskID, planA)); err != nil {
		t.Fatalf("attempt A HandleDecompositionResponse error = %v", err)
	}
	// Drive attempt A to COMPLETED so the dedup check treats the next
	// inbound task as a follow-up rather than a duplicate. HandlePlanComplete
	// deletes activeTasks[X] — matching the production path.
	activeA := d.GetActiveTasks()
	if len(activeA) != 1 {
		t.Fatalf("expected 1 active task after attempt A setup, got %d", len(activeA))
	}
	d.HandlePlanComplete(activeA[0], nil)

	orchRefA := activeA[0].OrchestratorTaskRef
	if orchRefA == "" {
		t.Fatal("attempt A has empty orchestrator_task_ref")
	}

	// Baseline counters so we can attribute any future changes to the
	// stale timer firing on attempt B (rather than attempt A's own path).
	_, _, _, _, decompFailedBefore, _ := d.GetMetrics()
	errorCallsBefore := len(gw.ErrorCalls)

	// Delay the follow-up so GA (fires at T≈1.0 s) and GB (fires at
	// T≈attemptBDelay + 1.0 s) have separable deadlines. Without this gap,
	// both timers fire within a few ms of each other and the test cannot
	// distinguish the stale-timer bug from B's own legitimate timeout.
	time.Sleep(attemptBDelay)

	// ── Attempt B (follow-up on the same TaskID) ──────────────────────────
	taskB := validTask(taskID)
	if err := d.HandleInboundTask(ctx, taskB); err != nil {
		t.Fatalf("attempt B HandleInboundTask error = %v (follow-up rejected?)", err)
	}
	activeB := d.GetActiveTasks()
	if len(activeB) != 1 {
		t.Fatalf("expected 1 active task after attempt B setup, got %d", len(activeB))
	}
	orchRefB := activeB[0].OrchestratorTaskRef
	if orchRefB == orchRefA {
		t.Fatalf("attempts A and B share orchestrator_task_ref %q — dispatcher must mint a fresh ref per attempt", orchRefA)
	}

	// ── Wait past GA's deadline, still before GB's ────────────────────────
	//
	//   T = 0 ────────────────── attempt A start ── GA fires at T = 1.0
	//   T = attemptBDelay ────── attempt B start ── GB fires at T = 1.5
	//
	// Sleep just far enough past GA's deadline for any failTask call it
	// would have made to complete. Under the FIX, GA sees orchRef
	// mismatch and returns without touching attempt B.
	time.Sleep(time.Duration(timeoutSec)*time.Second - attemptBDelay + settleAfterGA)

	// First assertion window: GA has fired but GB has not. If the bug is
	// present the metrics already show decompositionFailed=1 here.
	_, _, _, _, decompFailedAfterGA, _ := d.GetMetrics()
	if delta := decompFailedAfterGA - decompFailedBefore; delta != 0 {
		t.Fatalf("stale GA timer fired on follow-up (decompositionFailed += %d after GA window)", delta)
	}

	// Resolve B BEFORE GB fires so GB's legitimate timer finds a
	// non-DECOMPOSING state and no-ops. This isolates the test from B's
	// own timeout path, which is not what we're testing here.
	planB := validPlan(taskID)
	if err := d.HandleDecompositionResponse(ctx, decompositionResponse(taskID, planB)); err != nil {
		t.Fatalf("attempt B HandleDecompositionResponse error = %v", err)
	}

	// Sleep past GB's deadline so any delayed goroutine work settles.
	time.Sleep(settleAfterGB)

	// ── Assertions ────────────────────────────────────────────────────────
	_, _, _, _, decompFailedAfter, _ := d.GetMetrics()
	if delta := decompFailedAfter - decompFailedBefore; delta != 0 {
		t.Fatalf("decompositionFailed incremented by %d across the follow-up — stale timer fired on attempt B", delta)
	}

	// No DECOMPOSITION_TIMEOUT error payload should have been published
	// for taskID after the follow-up was submitted.
	for _, e := range gw.ErrorCalls[errorCallsBefore:] {
		if e.ErrorCode == types.ErrCodeDecompositionTimeout && e.TaskID == taskID {
			t.Fatalf("stale timer fired DECOMPOSITION_TIMEOUT on follow-up: %+v", e)
		}
	}

	// Attempt B must still be alive (either ACTIVE or cleaned up by a
	// subsequent step) and must not be in DECOMPOSITION_FAILED.
	for _, ts := range d.GetActiveTasks() {
		if ts.TaskID == taskID && ts.State == types.StateDecompositionFailed {
			t.Fatalf("attempt B transitioned to DECOMPOSITION_FAILED — stale timer fired")
		}
	}

	// Both attempt A and attempt B decompositions dispatched a plan to
	// the executor — A via its HandleDecompositionResponse in setup, B via
	// the follow-up path after GA's window. The last Execute call must
	// carry attempt B's plan, which proves the follow-up actually
	// completed its decomposition cleanly (rather than being killed by
	// the stale timer before its DecompositionResponse arrived).
	if len(exec.ExecuteCalls) != 2 {
		t.Fatalf("executor.Execute calls = %d, want 2 (A + B)", len(exec.ExecuteCalls))
	}
	if got := exec.ExecuteCalls[len(exec.ExecuteCalls)-1].Plan.PlanID; got != planB.PlanID {
		t.Fatalf("last executor plan_id %q, want attempt B's %q", got, planB.PlanID)
	}

	// Suppress the "atomic unused" warning when test body paths are edited.
	_ = atomic.LoadInt64
}
