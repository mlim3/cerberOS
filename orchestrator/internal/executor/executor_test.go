package executor_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/executor"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

type gatewayMock struct {
	taskSpecs            []types.TaskSpec
	capabilityQueries    []types.CapabilityQuery
	confirmationRequests []types.ConfirmationRequest
	confirmationErr      error
}

func (g *gatewayMock) PublishTaskSpec(_ context.Context, spec types.TaskSpec) error {
	g.taskSpecs = append(g.taskSpecs, spec)
	return nil
}

func (g *gatewayMock) PublishCapabilityQuery(_ context.Context, query types.CapabilityQuery) (*types.CapabilityResponse, error) {
	g.capabilityQueries = append(g.capabilityQueries, query)
	return &types.CapabilityResponse{
		OrchestratorTaskRef: query.OrchestratorTaskRef,
		Match:               types.CapabilityMatch_Match,
		AgentID:             "agent-" + query.OrchestratorTaskRef,
	}, nil
}

func (g *gatewayMock) PublishStatusUpdate(context.Context, string, types.StatusResponse) error {
	return nil
}

func (g *gatewayMock) PublishConfirmationRequest(_ context.Context, req types.ConfirmationRequest) error {
	g.confirmationRequests = append(g.confirmationRequests, req)
	return g.confirmationErr
}

type policyMock struct {
	revoked []string
}

func (p *policyMock) RevokeCredentials(_ context.Context, orchestratorTaskRef string) error {
	p.revoked = append(p.revoked, orchestratorTaskRef)
	return nil
}

type harness struct {
	exec      *executor.PlanExecutor
	gw        *gatewayMock
	mem       *mocks.MemoryMock
	completed []types.PriorResult
	failed    struct {
		called       bool
		errorCode    string
		partial      bool
		partialCount int
	}
}

func newHarness(maxParallel int) *harness {
	h := &harness{
		gw:  &gatewayMock{},
		mem: mocks.NewMemoryMock(),
	}
	cfg := &config.OrchestratorConfig{
		NodeID:                  "test-node",
		PlanExecutorMaxParallel: maxParallel,
	}
	pol := &policyMock{}
	h.exec = executor.New(
		cfg,
		h.mem,
		h.gw,
		pol,
		func(_ *types.TaskState, results []types.PriorResult) {
			h.completed = results
		},
		func(_ *types.TaskState, errorCode string, partial bool, partialResults []types.PriorResult) {
			h.failed.called = true
			h.failed.errorCode = errorCode
			h.failed.partial = partial
			h.failed.partialCount = len(partialResults)
		},
	)
	return h
}

func baseTaskState() *types.TaskState {
	return &types.TaskState{
		OrchestratorTaskRef: "orch-parent",
		TaskID:              "550e8400-e29b-41d4-a716-446655440000",
		UserID:              "user-1",
		TraceID:             "0123456789abcdef0123456789abcdef",
		PolicyScope: types.PolicyScope{
			Domains:  []string{"web"},
			TokenRef: "tok-1",
		},
		CallbackTopic: "aegis.user-io.results.task-1",
	}
}

func basePlan(taskID string, subtasks ...types.Subtask) types.ExecutionPlan {
	return types.ExecutionPlan{
		PlanID:       "plan-1",
		ParentTaskID: taskID,
		CreatedAt:    time.Now().UTC(),
		Subtasks:     subtasks,
	}
}

func subtask(id string, deps ...string) types.Subtask {
	return types.Subtask{
		SubtaskID:            id,
		RequiredSkillDomains: []string{"web"},
		Action:               "action-" + id,
		Instructions:         "Do " + id,
		DependsOn:            deps,
		TimeoutSeconds:       30,
	}
}

func TestExecute_DispatchesReadySubtasksInParallel(t *testing.T) {
	h := newHarness(2)
	ts := baseTaskState()
	plan := basePlan(ts.TaskID, subtask("s1"), subtask("s2"))

	if err := h.exec.Execute(context.Background(), plan, ts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(h.gw.taskSpecs) != 2 {
		t.Fatalf("task specs published = %d, want 2 parallel-ready subtasks", len(h.gw.taskSpecs))
	}
	if len(h.gw.capabilityQueries) != 2 {
		t.Fatalf("capability queries = %d, want 2", len(h.gw.capabilityQueries))
	}
	got := map[string]bool{}
	for _, spec := range h.gw.taskSpecs {
		got[spec.Metadata["subtask_id"]] = true
	}
	if !got["s1"] || !got["s2"] {
		t.Fatalf("published subtasks = %v, want s1 and s2", got)
	}
}

func TestHandleSubtaskResult_DispatchesDependentSubtaskSeriallyWithPriorResults(t *testing.T) {
	h := newHarness(2)
	ts := baseTaskState()
	plan := basePlan(ts.TaskID, subtask("s1"), subtask("s2", "s1"))

	if err := h.exec.Execute(context.Background(), plan, ts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(h.gw.taskSpecs) != 1 {
		t.Fatalf("initial task specs = %d, want only dependency-free s1", len(h.gw.taskSpecs))
	}

	firstRef := h.gw.taskSpecs[0].OrchestratorTaskRef
	err := h.exec.HandleSubtaskResult(context.Background(), types.TaskResult{
		OrchestratorTaskRef: firstRef,
		AgentID:             "agent-1",
		Success:             true,
		Result:              json.RawMessage(`{"value":"s1-result"}`),
		CompletedAt:         time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("HandleSubtaskResult(s1) error = %v", err)
	}

	if len(h.gw.taskSpecs) != 2 {
		t.Fatalf("task specs after s1 completion = %d, want dependent s2 dispatched", len(h.gw.taskSpecs))
	}
	second := h.gw.taskSpecs[1]
	if second.Metadata["subtask_id"] != "s2" {
		t.Fatalf("second dispatched subtask = %q, want s2", second.Metadata["subtask_id"])
	}
	var prior []types.PriorResult
	if err := json.Unmarshal(second.Payload, &prior); err != nil {
		t.Fatalf("unmarshal prior results: %v", err)
	}
	if len(prior) != 1 || prior[0].SubtaskID != "s1" {
		t.Fatalf("prior results = %+v, want s1 result injected", prior)
	}
}

func TestConfirmation_ApprovalResumesDispatch(t *testing.T) {
	h := newHarness(2)
	ts := baseTaskState()
	st := subtask("s1")
	st.RequiresConfirmation = true
	plan := basePlan(ts.TaskID, st)

	if err := h.exec.Execute(context.Background(), plan, ts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(h.gw.taskSpecs) != 0 {
		t.Fatalf("task specs before confirmation = %d, want 0", len(h.gw.taskSpecs))
	}
	if len(h.gw.confirmationRequests) != 1 {
		t.Fatalf("confirmation requests = %d, want 1", len(h.gw.confirmationRequests))
	}

	if err := h.exec.HandleConfirmationResponse(context.Background(), types.ConfirmationResponse{
		PlanID:    plan.PlanID,
		SubtaskID: "s1",
		TaskID:    ts.TaskID,
		Confirmed: true,
	}); err != nil {
		t.Fatalf("HandleConfirmationResponse(confirm) error = %v", err)
	}

	if len(h.gw.taskSpecs) != 1 {
		t.Fatalf("task specs after confirmation = %d, want 1", len(h.gw.taskSpecs))
	}
	if h.gw.taskSpecs[0].Metadata["subtask_id"] != "s1" {
		t.Fatalf("dispatched subtask = %q, want s1", h.gw.taskSpecs[0].Metadata["subtask_id"])
	}
}

func TestConfirmation_RejectionFailsSubtaskAndBlocksDependents(t *testing.T) {
	h := newHarness(2)
	ts := baseTaskState()
	st := subtask("s1")
	st.RequiresConfirmation = true
	plan := basePlan(ts.TaskID, st, subtask("s2", "s1"))

	if err := h.exec.Execute(context.Background(), plan, ts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if err := h.exec.HandleConfirmationResponse(context.Background(), types.ConfirmationResponse{
		PlanID:    plan.PlanID,
		SubtaskID: "s1",
		TaskID:    ts.TaskID,
		Confirmed: false,
		Reason:    "not now",
	}); err != nil {
		t.Fatalf("HandleConfirmationResponse(reject) error = %v", err)
	}

	if len(h.gw.taskSpecs) != 0 {
		t.Fatalf("task specs after rejection = %d, want 0", len(h.gw.taskSpecs))
	}
	if !h.failed.called {
		t.Fatal("plan failure callback not called")
	}
	if h.failed.errorCode != types.ErrCodeUserRejected {
		t.Fatalf("failure error code = %q, want USER_REJECTED", h.failed.errorCode)
	}
	if latestSubtaskState(t, h.mem, "s2").State != types.SubtaskStateBlocked {
		t.Fatalf("dependent subtask state = %q, want BLOCKED", latestSubtaskState(t, h.mem, "s2").State)
	}
}

func TestConfirmation_DeliveryFailureFailsSubtask(t *testing.T) {
	h := newHarness(2)
	h.gw.confirmationErr = errors.New("io unavailable")
	ts := baseTaskState()
	st := subtask("s1")
	st.RequiresConfirmation = true
	plan := basePlan(ts.TaskID, st)

	if err := h.exec.Execute(context.Background(), plan, ts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(h.gw.taskSpecs) != 0 {
		t.Fatalf("task specs = %d, want 0 when confirmation delivery fails", len(h.gw.taskSpecs))
	}
	if !h.failed.called {
		t.Fatal("plan failure callback not called")
	}
	if h.failed.errorCode != types.ErrCodeConfirmationUnavailable {
		t.Fatalf("failure error code = %q, want CONFIRMATION_UNAVAILABLE", h.failed.errorCode)
	}
	if latestSubtaskState(t, h.mem, "s1").ErrorCode != types.ErrCodeConfirmationUnavailable {
		t.Fatalf("subtask error code = %q, want CONFIRMATION_UNAVAILABLE", latestSubtaskState(t, h.mem, "s1").ErrorCode)
	}
}

func TestConfirmation_ResponseTaskIDMismatchRejected(t *testing.T) {
	h := newHarness(2)
	ts := baseTaskState()
	st := subtask("s1")
	st.RequiresConfirmation = true
	plan := basePlan(ts.TaskID, st)

	if err := h.exec.Execute(context.Background(), plan, ts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	err := h.exec.HandleConfirmationResponse(context.Background(), types.ConfirmationResponse{
		PlanID:    plan.PlanID,
		SubtaskID: "s1",
		TaskID:    "550e8400-e29b-41d4-a716-446655440999",
		Confirmed: true,
	})
	if err == nil {
		t.Fatal("HandleConfirmationResponse() error = nil, want task_id mismatch error")
	}
	if len(h.gw.taskSpecs) != 0 {
		t.Fatalf("task specs after mismatched confirmation = %d, want 0", len(h.gw.taskSpecs))
	}
}

func latestSubtaskState(t *testing.T, mem *mocks.MemoryMock, subtaskID string) types.SubtaskState {
	t.Helper()
	var latest types.SubtaskState
	found := false
	for _, rec := range mem.Records {
		if rec.DataType != types.DataTypeSubtaskState {
			continue
		}
		var sub types.SubtaskState
		if err := json.Unmarshal(rec.Payload, &sub); err != nil {
			t.Fatalf("unmarshal subtask_state: %v", err)
		}
		if sub.SubtaskID == subtaskID {
			latest = sub
			found = true
		}
	}
	if !found {
		t.Fatalf("no subtask_state found for %s", subtaskID)
	}
	return latest
}
