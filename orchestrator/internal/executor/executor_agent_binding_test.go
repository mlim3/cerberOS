package executor

import (
	"context"
	"sync"
	"testing"

	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// stubPolicy implements PolicyEnforcer with a counter so tests can assert
// whether RevokeCredentials was called.
type stubPolicy struct {
	mu      sync.Mutex
	revoked []string
}

func (s *stubPolicy) RevokeCredentials(_ context.Context, orchRef string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = append(s.revoked, orchRef)
	return nil
}

// newTestExecutor builds a minimum PlanExecutor wired with the in-memory state
// needed for HandleSubtaskResult — no NATS, no DB, no real gateway. Only the
// policy stub is wired because the mismatch guard sits above the gateway call.
func newTestExecutor(policy PolicyEnforcer) *PlanExecutor {
	return &PlanExecutor{policy: policy}
}

// seedSubtask plumbs orchRef → planID → exec with a single subtask whose
// expected agent_id is `expectedAgentID`. Returns the subtask state so tests
// can inspect post-call mutations.
func seedSubtask(e *PlanExecutor, orchRef, planID, subtaskID, expectedAgentID string) *types.SubtaskState {
	sub := &types.SubtaskState{
		SubtaskID: subtaskID,
		AgentID:   expectedAgentID,
		State:     types.SubtaskStatePending,
	}
	exec := &planExecution{
		ts:           &types.TaskState{TaskID: "task-1", UserID: "user-A"},
		subtasks:     map[string]*types.SubtaskState{subtaskID: sub},
		orchRefIndex: map[string]string{orchRef: subtaskID},
	}
	e.activePlans.Store(planID, exec)
	e.orchRefToPlan.Store(orchRef, planID)
	return sub
}

func TestHandleSubtaskResult_DropsMismatchedAgentID(t *testing.T) {
	policy := &stubPolicy{}
	e := newTestExecutor(policy)

	sub := seedSubtask(e, "orch-ref-1", "plan-1", "sub-1", "agent-expected")

	err := e.HandleSubtaskResult(context.Background(), types.TaskResult{
		OrchestratorTaskRef: "orch-ref-1",
		AgentID:             "agent-IMPOSTER",
		Success:             true,
		Result:              []byte(`"forged"`),
	})
	if err != nil {
		t.Fatalf("expected nil (drop), got error: %v", err)
	}

	if sub.State != types.SubtaskStatePending {
		t.Errorf("expected subtask to remain PENDING after dropped result, got %q", sub.State)
	}
	if sub.Result != nil {
		t.Errorf("expected subtask Result to remain unset after dropped result, got %s", string(sub.Result))
	}
	if sub.CompletedAt != nil {
		t.Errorf("expected CompletedAt to remain nil after dropped result")
	}
	if len(policy.revoked) != 0 {
		t.Errorf("expected no credential revocation on dropped result, got %v", policy.revoked)
	}
}

func TestHandleSubtaskResult_DropsForgedFailure(t *testing.T) {
	// A failure result from the wrong agent must also be dropped — the symmetric
	// case prevents a malicious agent from forcing another tenant's task into
	// a failure state.
	policy := &stubPolicy{}
	e := newTestExecutor(policy)
	sub := seedSubtask(e, "orch-ref-2", "plan-2", "sub-2", "agent-A")

	err := e.HandleSubtaskResult(context.Background(), types.TaskResult{
		OrchestratorTaskRef: "orch-ref-2",
		AgentID:             "agent-B",
		Success:             false,
		ErrorCode:           "forged-failure",
	})
	if err != nil {
		t.Fatalf("expected nil (drop), got error: %v", err)
	}
	if sub.State == types.SubtaskStateFailed {
		t.Errorf("forged failure was not rejected: subtask transitioned to FAILED")
	}
}

func TestHandleSubtaskResult_AllowsEmptyExpectedAgentID(t *testing.T) {
	// Legacy / fast-path subtasks may not have an expected agent_id set
	// (the orchestrator only assigns it after a capability response). When
	// the stored expected agent_id is empty we must NOT drop the result —
	// otherwise we'd block normal flows. The guard only fires when both
	// values are non-empty and disagree.
	//
	// Past the guard the code hits persistSubtaskState, which needs the
	// memory client we haven't wired. We tolerate that downstream panic
	// here — the proof we wanted (RevokeCredentials was reached) lands
	// before the panic.
	policy := &stubPolicy{}
	e := newTestExecutor(policy)
	seedSubtask(e, "orch-ref-3", "plan-3", "sub-3", "")

	defer func() {
		_ = recover() // expected: nil-memory panic past the guard
		if len(policy.revoked) == 0 {
			t.Errorf("expected guard to allow result with empty expected agent_id (RevokeCredentials should have been reached); got no revocation")
		}
	}()

	_ = e.HandleSubtaskResult(context.Background(), types.TaskResult{
		OrchestratorTaskRef: "orch-ref-3",
		AgentID:             "agent-anyone",
		Success:             true,
		Result:              []byte(`"ok"`),
	})
}
