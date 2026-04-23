// Package dispatcher_test provides black-box tests for M2: Task Dispatcher (v3.0).
//
// Each test demonstrates a distinct scenario from the EDD v3.0 (§7, §8, §17.3).
// Run with:
//
//	cd orchestrator && go test ./internal/dispatcher/ -v
//
// Demo scenarios covered:
//
//	✅  Happy Path — Decomposition     — task flows DECOMPOSING → DecompositionResponse → PLAN_ACTIVE
//	✅  Duplicate Task                 — same task_id twice → DUPLICATE_TASK; Planner Agent never contacted
//	✅  Policy Violation               — Vault denies → POLICY_VIOLATION; planner task NEVER sent
//	✅  Schema Errors                  — invalid UUID, priority, timeout; required_skill_domains now optional
//	✅  Memory Fail Before Decompose   — memory write fails → error; planner task NOT published
//	✅  Decomposition Publish Fail     — gateway fails → AGENTS_UNAVAILABLE; cleanup performed
//	✅  Invalid Plan                   — circular deps, empty plan, scope violation → DECOMPOSITION_FAILED
//	✅  Plan Complete                  — HandlePlanComplete writes COMPLETED + delivers result + revokes creds
//	✅  Plan Failed                    — HandlePlanFailed writes FAILED + publishes error
//	✅  Partial Complete               — HandlePlanFailed with partial=true writes PARTIAL_COMPLETE + result
//	✅  Metrics                        — counters track received, completed, failed, violations, queue depth
//	✅  Active Tasks                   — GetActiveTasks reflects in-flight tasks and clears on completion
package dispatcher_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/dispatcher"
	ioclient "github.com/mlim3/cerberOS/orchestrator/internal/io"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// ── Lightweight mocks for dispatcher-internal interfaces ──────────────────────

// gatewayMock records every outbound call the Dispatcher makes to M1 (v3.0 interface).
type gatewayMock struct {
	AcceptedCalls     []types.TaskAccepted
	ErrorCalls        []types.ErrorResponse
	TaskSpecCalls     []types.TaskSpec
	TaskResultCalls   []types.TaskResult
	StatusUpdateCalls []types.StatusResponse

	PublishTaskSpecError error
	PublishAcceptedError error
	PublishResultError   error
}

func (g *gatewayMock) PublishTaskAccepted(_ context.Context, _ string, a types.TaskAccepted) error {
	g.AcceptedCalls = append(g.AcceptedCalls, a)
	return g.PublishAcceptedError
}

func (g *gatewayMock) PublishError(_ context.Context, _ string, e types.ErrorResponse) error {
	g.ErrorCalls = append(g.ErrorCalls, e)
	return nil
}

func (g *gatewayMock) PublishTaskResult(_ context.Context, _ string, r types.TaskResult) error {
	g.TaskResultCalls = append(g.TaskResultCalls, r)
	return g.PublishResultError
}

func (g *gatewayMock) PublishTaskSpec(_ context.Context, spec types.TaskSpec) error {
	g.TaskSpecCalls = append(g.TaskSpecCalls, spec)
	return g.PublishTaskSpecError
}

func (g *gatewayMock) PublishStatusUpdate(_ context.Context, _ string, s types.StatusResponse) error {
	g.StatusUpdateCalls = append(g.StatusUpdateCalls, s)
	return nil
}

// policyMock is a minimal controllable PolicyEnforcer.
type policyMock struct {
	ShouldDeny     bool
	DenyReason     string
	RevokeError    error
	RevokedRefs    []string
	ValidatedCalls int
}

func (p *policyMock) ValidateAndScope(_ context.Context, taskID, orchRef, userID string, domains []string, timeout int) (types.PolicyScope, error) {
	p.ValidatedCalls++
	if p.ShouldDeny {
		reason := p.DenyReason
		if reason == "" {
			reason = "policy denied"
		}
		return types.PolicyScope{}, errors.New(reason)
	}
	return types.PolicyScope{
		Domains:   []string{"web", "research", "calendar"},
		TokenRef:  "tok-" + taskID,
		IssuedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
		Metadata: map[string]string{
			"user_id": userID,
			"orchRef": orchRef,
			"timeout": fmt.Sprintf("%d", timeout),
		},
	}, nil
}

func (p *policyMock) RevokeCredentials(_ context.Context, orchRef string) error {
	p.RevokedRefs = append(p.RevokedRefs, orchRef)
	return p.RevokeError
}

// monitorMock records task tracking calls.
type monitorMock struct {
	TrackedTasks []*types.TaskState
	UntrackedIDs []string
}

func (m *monitorMock) TrackTask(ts *types.TaskState) { m.TrackedTasks = append(m.TrackedTasks, ts) }
func (m *monitorMock) UntrackTask(taskID string)     { m.UntrackedIDs = append(m.UntrackedIDs, taskID) }

// executorMock records Execute calls and can inject failures.
type executorMock struct {
	ExecuteCalls []struct {
		Plan types.ExecutionPlan
		TS   *types.TaskState
	}
	ResultCalls  []types.TaskResult
	ExecuteError error
}

func (e *executorMock) Execute(_ context.Context, plan types.ExecutionPlan, ts *types.TaskState) error {
	e.ExecuteCalls = append(e.ExecuteCalls, struct {
		Plan types.ExecutionPlan
		TS   *types.TaskState
	}{plan, ts})
	return e.ExecuteError
}

func (e *executorMock) HandleSubtaskResult(_ context.Context, result types.TaskResult) error {
	e.ResultCalls = append(e.ResultCalls, result)
	return nil
}

// ── Test Helpers ─────────────────────────────────────────────────────────────

// newDispatcher builds a fully wired Dispatcher with fresh mocks.
func newDispatcher(t *testing.T) (*dispatcher.Dispatcher, *gatewayMock, *policyMock, *monitorMock, *executorMock, *mocks.MemoryMock) {
	t.Helper()
	gw := &gatewayMock{}
	pol := &policyMock{}
	mon := &monitorMock{}
	exec := &executorMock{}
	mem := mocks.NewMemoryMock()

	cfg := &config.OrchestratorConfig{
		NodeID:                      "test-node",
		DecompositionTimeoutSeconds: 30,
		MaxSubtasksPerPlan:          20,
		PlanExecutorMaxParallel:     5,
		// All existing tests were written before the approval gate existed;
		// opt them out by default. Approval-gate behaviour is exercised in
		// its own test below.
		PlanApprovalMode: "off",
	}
	d := dispatcher.New(cfg, mem, nil /* vault unused */, gw, pol, mon, exec, ioclient.New("") /* disabled */)
	return d, gw, pol, mon, exec, mem
}

// validTask returns a minimal, schema-valid UserTask for v3.0.
// required_skill_domains is optional in v3.0 (FR-TRK-04).
func validTask(taskID string) types.UserTask {
	return types.UserTask{
		TaskID:         taskID,
		UserID:         "user-1",
		Priority:       5,
		TimeoutSeconds: 60,
		Payload:        json.RawMessage(`{"raw_input":"book a flight from NYC to LA"}`),
		CallbackTopic:  "aegis.user-io.results.task-1",
	}
}

// validPlan returns a minimal valid 1-subtask execution plan.
func validPlan(taskID string) types.ExecutionPlan {
	return types.ExecutionPlan{
		PlanID:       "plan-" + taskID,
		ParentTaskID: taskID,
		CreatedAt:    time.Now().UTC(),
		Subtasks: []types.Subtask{
			{
				SubtaskID:            "s1",
				RequiredSkillDomains: []string{"web"},
				Action:               "search",
				Instructions:         "Search for flights from NYC to LA",
				DependsOn:            []string{},
				TimeoutSeconds:       30,
			},
		},
	}
}

// decompositionResponse wraps a plan as a DecompositionResponse for a given task.
func decompositionResponse(taskID string, plan types.ExecutionPlan) types.DecompositionResponse {
	return types.DecompositionResponse{
		TaskID: taskID,
		Plan:   plan,
	}
}

// orchRefFromPlannerTask extracts orchestrator_task_ref from the first planner task.
func orchRefFromPlannerTask(t *testing.T, gw *gatewayMock) string {
	t.Helper()
	if len(gw.TaskSpecCalls) == 0 {
		t.Fatal("no planner task was published")
	}
	return gw.TaskSpecCalls[0].OrchestratorTaskRef
}

// latestTaskStateRecord returns the most recent task_state record for a task_id.
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

// taskStateHistory returns all persisted task states for a task_id in write order.
func taskStateHistory(t *testing.T, mem *mocks.MemoryMock, taskID string) []types.TaskState {
	t.Helper()
	var states []types.TaskState
	for _, rec := range mem.Records {
		if rec.TaskID != taskID || rec.DataType != types.DataTypeTaskState {
			continue
		}
		var ts types.TaskState
		if err := json.Unmarshal(rec.Payload, &ts); err != nil {
			t.Fatalf("json.Unmarshal(task_state payload) error = %v", err)
		}
		states = append(states, ts)
	}
	return states
}

// ── Happy Path: Inbound → DECOMPOSING ────────────────────────────────────────

func TestHandleInboundTask_HappyPath_Decomposing(t *testing.T) {
	d, gw, pol, mon, exec, mem := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440000")
	if err := d.HandleInboundTask(context.Background(), task); err != nil {
		t.Fatalf("HandleInboundTask() error = %v, want nil", err)
	}

	// Policy must be called once.
	if pol.ValidatedCalls != 1 {
		t.Fatalf("policy.ValidatedCalls = %d, want 1", pol.ValidatedCalls)
	}

	// A planner task must be published to the general agent.
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("planner task publishes = %d, want 1", len(gw.TaskSpecCalls))
	}
	req := gw.TaskSpecCalls[0]
	if req.TaskID != req.OrchestratorTaskRef {
		t.Fatalf("planner task_id = %q, want orchestrator_task_ref", req.TaskID)
	}
	if req.OrchestratorTaskRef == "" || req.OrchestratorTaskRef == task.TaskID {
		t.Fatal("planner orchestrator_task_ref must be a distinct non-empty UUID")
	}
	if got := req.RequiredSkillDomains; len(got) != 1 || got[0] != "general" {
		t.Fatalf("planner required_skill_domains = %v, want [general]", got)
	}
	if !strings.Contains(req.Instructions, "book a flight from NYC to LA") {
		t.Fatalf("planner instructions = %q, want raw_input included", req.Instructions)
	}
	if req.PolicyScope.TokenRef == "" {
		t.Fatal("planner policy_scope.token_ref is empty — policy scope not attached")
	}

	// task_accepted must fire early — right after policy + memory persist,
	// so the user sees acknowledgement without waiting for the planner round-trip.
	if len(gw.AcceptedCalls) != 1 {
		t.Fatalf("task_accepted publishes = %d, want 1 (early ack after policy + memory persist)", len(gw.AcceptedCalls))
	}
	if gw.AcceptedCalls[0].OrchestratorTaskRef == "" {
		t.Fatal("early task_accepted.orchestrator_task_ref is empty")
	}

	// Executor must NOT be called yet.
	if len(exec.ExecuteCalls) != 0 {
		t.Fatalf("executor.Execute calls = %d, want 0 before decomposition response", len(exec.ExecuteCalls))
	}

	// Task must be persisted as DECOMPOSING.
	rec := latestTaskStateRecord(t, mem, task.TaskID)
	if rec.State != types.StateDecomposing {
		t.Fatalf("persisted state = %q, want DECOMPOSING", rec.State)
	}

	// Task must be tracked.
	if len(mon.TrackedTasks) != 1 {
		t.Fatalf("monitor.TrackedTasks = %d, want 1", len(mon.TrackedTasks))
	}

	if len(gw.ErrorCalls) != 0 {
		t.Fatalf("unexpected errors published: %+v", gw.ErrorCalls)
	}
	_ = exec
}

func TestHandleInboundTask_MaintenancePayload_SystemPromptAndMetadata(t *testing.T) {
	d, gw, _, _, _, _ := newDispatcher(t)
	payload := map[string]any{
		"raw_input":     "kick off decay",
		"system_prompt": "SYS: extract facts",
		"maintenance":   true,
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	task := types.UserTask{
		TaskID:         "550e8400-e29b-41d4-a716-4466554400aa",
		UserID:         "system",
		Priority:       5,
		TimeoutSeconds: 60,
		Payload:        pb,
		CallbackTopic:  "aegis.orchestrator.cron.wake.results",
	}
	if err := d.HandleInboundTask(context.Background(), task); err != nil {
		t.Fatalf("HandleInboundTask() error = %v", err)
	}
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("planner task publishes = %d, want 1", len(gw.TaskSpecCalls))
	}
	req := gw.TaskSpecCalls[0]
	if req.Metadata["task_kind"] != "maintenance" {
		t.Fatalf("task_kind = %q, want maintenance", req.Metadata["task_kind"])
	}
	if !strings.Contains(req.Instructions, "SYS: extract facts") {
		t.Fatalf("planner instructions missing system prompt: %q", req.Instructions)
	}
	if !strings.Contains(req.Instructions, "kick off decay") {
		t.Fatalf("planner instructions missing raw_input: %q", req.Instructions)
	}
}

// ── Happy Path: DecompositionResponse → PLAN_ACTIVE ──────────────────────────

func TestHandleDecompositionResponse_ValidPlan_ActivatesPlanAndSendsAccepted(t *testing.T) {
	d, gw, _, _, exec, mem := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440001")
	if err := d.HandleInboundTask(context.Background(), task); err != nil {
		t.Fatalf("setup HandleInboundTask() error = %v", err)
	}

	plan := validPlan(task.TaskID)
	resp := decompositionResponse(task.TaskID, plan)

	if err := d.HandleDecompositionResponse(context.Background(), resp); err != nil {
		t.Fatalf("HandleDecompositionResponse() error = %v, want nil", err)
	}

	// Task must be transitioned to PLAN_ACTIVE.
	rec := latestTaskStateRecord(t, mem, task.TaskID)
	if rec.State != types.StatePlanActive {
		t.Fatalf("persisted state = %q, want PLAN_ACTIVE", rec.State)
	}
	if rec.PlanID != plan.PlanID {
		t.Fatalf("persisted plan_id = %q, want %q", rec.PlanID, plan.PlanID)
	}

	// Executor must be called once with the plan.
	if len(exec.ExecuteCalls) != 1 {
		t.Fatalf("executor.Execute calls = %d, want 1", len(exec.ExecuteCalls))
	}
	if exec.ExecuteCalls[0].Plan.PlanID != plan.PlanID {
		t.Fatalf("executor received plan_id = %q, want %q", exec.ExecuteCalls[0].Plan.PlanID, plan.PlanID)
	}

	// task_accepted must be sent after plan validation.
	if len(gw.AcceptedCalls) != 1 {
		t.Fatalf("task_accepted publishes = %d, want 1", len(gw.AcceptedCalls))
	}
	if gw.AcceptedCalls[0].OrchestratorTaskRef == "" {
		t.Fatal("task_accepted.orchestrator_task_ref is empty")
	}

	// Plan state must also be persisted.
	planFound := false
	for _, rec := range mem.Records {
		if rec.TaskID == task.TaskID && rec.DataType == types.DataTypePlanState {
			planFound = true
			break
		}
	}
	if !planFound {
		t.Fatal("plan_state record not found in Memory — plan was not persisted")
	}
}

// ── DecompositionResponse: State Guard ───────────────────────────────────────

func TestHandleDecompositionResponse_IgnoredIfNotDecomposing(t *testing.T) {
	d, gw, _, _, exec, _ := newDispatcher(t)

	// Do not submit a task — respond for unknown task_id.
	resp := decompositionResponse("550e8400-e29b-41d4-a716-446655440099", validPlan("550e8400-e29b-41d4-a716-446655440099"))
	if err := d.HandleDecompositionResponse(context.Background(), resp); err != nil {
		t.Fatalf("HandleDecompositionResponse() error = %v, want nil (unknown task silently ignored)", err)
	}
	if len(exec.ExecuteCalls) != 0 {
		t.Fatal("executor.Execute called for unknown task — should be silently ignored")
	}
	if len(gw.AcceptedCalls) != 0 {
		t.Fatal("task_accepted sent for unknown task")
	}
}

// ── Plan Validation ───────────────────────────────────────────────────────────

func TestHandleDecompositionResponse_EmptyPlan_DecompositionFailed(t *testing.T) {
	d, gw, _, _, _, mem := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440010")
	_ = d.HandleInboundTask(context.Background(), task)

	resp := types.DecompositionResponse{
		TaskID: task.TaskID,
		Plan:   types.ExecutionPlan{PlanID: "plan-empty", ParentTaskID: task.TaskID, Subtasks: []types.Subtask{}},
	}
	_ = d.HandleDecompositionResponse(context.Background(), resp)

	if len(gw.ErrorCalls) != 1 {
		t.Fatalf("error calls = %d, want 1", len(gw.ErrorCalls))
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeEmptyPlan {
		t.Fatalf("error_code = %q, want EMPTY_PLAN", gw.ErrorCalls[0].ErrorCode)
	}

	rec := latestTaskStateRecord(t, mem, task.TaskID)
	if rec.State != types.StateDecompositionFailed {
		t.Fatalf("persisted state = %q, want DECOMPOSITION_FAILED", rec.State)
	}
}

func TestHandleDecompositionResponse_CircularDependency_DecompositionFailed(t *testing.T) {
	d, gw, _, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440011")
	_ = d.HandleInboundTask(context.Background(), task)

	// s1 depends on s2, s2 depends on s1 → cycle.
	cyclicPlan := types.ExecutionPlan{
		PlanID:       "plan-cyclic",
		ParentTaskID: task.TaskID,
		Subtasks: []types.Subtask{
			{SubtaskID: "s1", Action: "a", RequiredSkillDomains: []string{"web"}, DependsOn: []string{"s2"}, TimeoutSeconds: 30},
			{SubtaskID: "s2", Action: "b", RequiredSkillDomains: []string{"web"}, DependsOn: []string{"s1"}, TimeoutSeconds: 30},
		},
	}
	resp := decompositionResponse(task.TaskID, cyclicPlan)
	_ = d.HandleDecompositionResponse(context.Background(), resp)

	if len(gw.ErrorCalls) == 0 {
		t.Fatal("expected error for cyclic plan, got none")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidPlan {
		t.Fatalf("error_code = %q, want INVALID_PLAN", gw.ErrorCalls[0].ErrorCode)
	}
}

func TestHandleDecompositionResponse_PlanTooLarge_DecompositionFailed(t *testing.T) {
	d, gw, _, _, _, _ := newDispatcher(t)
	task := validTask("550e8400-e29b-41d4-a716-446655440012")
	_ = d.HandleInboundTask(context.Background(), task)

	// Build a plan with 21 subtasks (MaxSubtasksPerPlan=20).
	subtasks := make([]types.Subtask, 21)
	for i := range subtasks {
		subtasks[i] = types.Subtask{
			SubtaskID:            fmt.Sprintf("s%d", i),
			RequiredSkillDomains: []string{"web"},
			Action:               "act",
			DependsOn:            []string{},
			TimeoutSeconds:       30,
		}
	}
	resp := types.DecompositionResponse{
		TaskID: task.TaskID,
		Plan:   types.ExecutionPlan{PlanID: "plan-huge", ParentTaskID: task.TaskID, Subtasks: subtasks},
	}
	_ = d.HandleDecompositionResponse(context.Background(), resp)

	if len(gw.ErrorCalls) == 0 {
		t.Fatal("expected error for oversized plan")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodePlanTooLarge {
		t.Fatalf("error_code = %q, want PLAN_TOO_LARGE", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── Plan Completion Callbacks ──────────────────────────────────────────────────

func TestHandlePlanComplete_WritesCompletedAndDeliversResult(t *testing.T) {
	d, gw, pol, mon, _, mem := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440020")
	_ = d.HandleInboundTask(context.Background(), task)
	plan := validPlan(task.TaskID)
	_ = d.HandleDecompositionResponse(context.Background(), decompositionResponse(task.TaskID, plan))

	// Retrieve the TaskState reference from the dispatcher.
	activeTasks := d.GetActiveTasks()
	if len(activeTasks) != 1 {
		t.Fatalf("expected 1 active task, got %d", len(activeTasks))
	}
	ts := activeTasks[0]

	// Simulate plan executor signalling completion.
	results := []types.PriorResult{{SubtaskID: "s1", Result: json.RawMessage(`{"status":"ok"}`)}}
	d.HandlePlanComplete(ts, results)

	// Task must be persisted as COMPLETED.
	rec := latestTaskStateRecord(t, mem, task.TaskID)
	if rec.State != types.StateCompleted {
		t.Fatalf("persisted state = %q, want COMPLETED", rec.State)
	}
	if rec.CompletedAt == nil {
		t.Fatal("completed_at not set on COMPLETED task")
	}

	// task_result must be delivered.
	if len(gw.TaskResultCalls) != 1 {
		t.Fatalf("task_result publishes = %d, want 1", len(gw.TaskResultCalls))
	}
	if !gw.TaskResultCalls[0].Success {
		t.Fatal("task_result.success = false, want true")
	}

	// Credentials must be revoked.
	if len(pol.RevokedRefs) != 1 {
		t.Fatalf("revoked refs = %d, want 1", len(pol.RevokedRefs))
	}
	if pol.RevokedRefs[0] != ts.OrchestratorTaskRef {
		t.Fatalf("revoked ref = %q, want %q", pol.RevokedRefs[0], ts.OrchestratorTaskRef)
	}

	// Task must be removed from active tracking.
	if len(d.GetActiveTasks()) != 0 {
		t.Fatal("task still active after completion")
	}
	if len(mon.UntrackedIDs) != 1 {
		t.Fatalf("monitor.UntrackTask called %d times, want 1", len(mon.UntrackedIDs))
	}
}

func TestHandlePlanFailed_WritesFailedAndPublishesError(t *testing.T) {
	d, gw, pol, _, _, mem := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440021")
	_ = d.HandleInboundTask(context.Background(), task)
	plan := validPlan(task.TaskID)
	_ = d.HandleDecompositionResponse(context.Background(), decompositionResponse(task.TaskID, plan))

	activeTasks := d.GetActiveTasks()
	ts := activeTasks[0]

	d.HandlePlanFailed(ts, types.ErrCodeMaxRetriesExceeded, false, nil)

	rec := latestTaskStateRecord(t, mem, task.TaskID)
	if rec.State != types.StateFailed {
		t.Fatalf("persisted state = %q, want FAILED", rec.State)
	}
	if rec.ErrorCode != types.ErrCodeMaxRetriesExceeded {
		t.Fatalf("error_code = %q, want MAX_RETRIES_EXCEEDED", rec.ErrorCode)
	}

	if len(gw.ErrorCalls) != 1 {
		t.Fatalf("error publishes = %d, want 1", len(gw.ErrorCalls))
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeMaxRetriesExceeded {
		t.Fatalf("error_code = %q, want MAX_RETRIES_EXCEEDED", gw.ErrorCalls[0].ErrorCode)
	}

	if len(pol.RevokedRefs) != 1 {
		t.Fatalf("revoked refs = %d, want 1", len(pol.RevokedRefs))
	}

	if len(d.GetActiveTasks()) != 0 {
		t.Fatal("task still active after failure")
	}
}

func TestHandlePlanFailed_Partial_WritesPartialCompleteAndDeliversResult(t *testing.T) {
	d, gw, _, _, _, mem := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440022")
	_ = d.HandleInboundTask(context.Background(), task)
	plan := validPlan(task.TaskID)
	_ = d.HandleDecompositionResponse(context.Background(), decompositionResponse(task.TaskID, plan))

	activeTasks := d.GetActiveTasks()
	ts := activeTasks[0]

	partialResults := []types.PriorResult{{SubtaskID: "s1", Result: json.RawMessage(`{"done":true}`)}}
	d.HandlePlanFailed(ts, types.ErrCodeMaxRetriesExceeded, true, partialResults)

	rec := latestTaskStateRecord(t, mem, task.TaskID)
	if rec.State != types.StatePartialComplete {
		t.Fatalf("persisted state = %q, want PARTIAL_COMPLETE", rec.State)
	}

	// With partial results, a task_result is delivered (not a bare error).
	if len(gw.TaskResultCalls) != 1 {
		t.Fatalf("task_result publishes = %d, want 1 for partial completion", len(gw.TaskResultCalls))
	}
	if gw.TaskResultCalls[0].Success {
		t.Fatal("task_result.success = true on partial completion — expected false")
	}
}

// ── Policy Violation ─────────────────────────────────────────────────────────

func TestHandleInboundTask_PolicyViolation_NoDecompositionRequest(t *testing.T) {
	d, gw, pol, _, _, _ := newDispatcher(t)
	pol.ShouldDeny = true
	pol.DenyReason = "required domain 'financial' not in user policy"

	task := validTask("550e8400-e29b-41d4-a716-446655440030")
	err := d.HandleInboundTask(context.Background(), task)
	if err == nil {
		t.Fatal("HandleInboundTask() error = nil, want policy denied error")
	}

	if len(gw.TaskSpecCalls) != 0 {
		t.Fatalf("planner task publishes = %d, want 0 — Planner Agent must not be contacted on policy deny",
			len(gw.TaskSpecCalls))
	}
	if len(gw.ErrorCalls) != 1 {
		t.Fatalf("error calls = %d, want 1 (POLICY_VIOLATION)", len(gw.ErrorCalls))
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodePolicyViolation {
		t.Fatalf("error_code = %q, want POLICY_VIOLATION", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── Schema Validation ─────────────────────────────────────────────────────────

func TestHandleInboundTask_OptionalSkillDomains_ValidInV3(t *testing.T) {
	d, gw, pol, _, _, _ := newDispatcher(t)

	// In v3.0 required_skill_domains is OPTIONAL (FR-TRK-04).
	task := validTask("550e8400-e29b-41d4-a716-446655440040")
	task.RequiredSkillDomains = nil // explicitly empty

	err := d.HandleInboundTask(context.Background(), task)
	if err != nil {
		t.Fatalf("HandleInboundTask() error = %v, want nil — empty skill domains must be valid in v3.0", err)
	}

	// Policy must still be called; planner task must still be published.
	if pol.ValidatedCalls != 1 {
		t.Fatal("policy was not called — optional skill domains should not block pipeline")
	}
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatal("planner task was not published for task with empty skill domains")
	}
}

func TestHandleInboundTask_MissingTaskID_InvalidTaskSpec(t *testing.T) {
	d, gw, pol, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440041")
	task.TaskID = ""

	if err := d.HandleInboundTask(context.Background(), task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC")
	}
	if pol.ValidatedCalls != 0 {
		t.Fatal("policy was called — must short-circuit on schema failure")
	}
	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("planner task published on schema failure")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", gw.ErrorCalls[0].ErrorCode)
	}
	if !strings.Contains(gw.ErrorCalls[0].UserMessage, "task_id") {
		t.Fatalf("user_message = %q, want mention of task_id", gw.ErrorCalls[0].UserMessage)
	}
}

func TestHandleInboundTask_PriorityOutOfRange_InvalidTaskSpec(t *testing.T) {
	d, gw, _, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440042")
	task.Priority = 0

	if err := d.HandleInboundTask(context.Background(), task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC for priority=0")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", gw.ErrorCalls[0].ErrorCode)
	}
}

func TestHandleInboundTask_TimeoutTooShort_InvalidTaskSpec(t *testing.T) {
	d, gw, _, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440043")
	task.TimeoutSeconds = 10

	if err := d.HandleInboundTask(context.Background(), task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC for timeout=10")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── Memory-Before-Dispatch Safety ────────────────────────────────────────────

func TestHandleInboundTask_MemoryFailBeforeDecomposition_NoOrphanedRequest(t *testing.T) {
	d, gw, _, _, _, mem := newDispatcher(t)
	mem.ShouldFailWrites = true

	task := validTask("550e8400-e29b-41d4-a716-446655440050")
	err := d.HandleInboundTask(context.Background(), task)
	if err == nil {
		t.Fatal("HandleInboundTask() error = nil, want memory error")
	}

	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("planner task published despite memory write failure — prevents safe recovery")
	}
	if len(gw.ErrorCalls) == 0 {
		t.Fatal("no error sent to User I/O — caller must be notified of failure")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeStorageUnavailable {
		t.Fatalf("error_code = %q, want STORAGE_UNAVAILABLE", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── Decomposition Publish Failure ────────────────────────────────────────────

func TestHandleInboundTask_DecompositionPublishFails_TaskFails(t *testing.T) {
	d, gw, _, mon, _, _ := newDispatcher(t)
	gw.PublishTaskSpecError = errors.New("NATS publish failed")

	task := validTask("550e8400-e29b-41d4-a716-446655440051")
	err := d.HandleInboundTask(context.Background(), task)
	if err == nil {
		t.Fatal("HandleInboundTask() error = nil, want publish error")
	}

	if len(gw.ErrorCalls) == 0 {
		t.Fatal("no error sent to User I/O after decomposition publish failure")
	}

	// Task should be cleaned up.
	if len(d.GetActiveTasks()) != 0 {
		t.Fatal("active tasks not empty after decomposition failure")
	}
	if len(mon.UntrackedIDs) != 1 {
		t.Fatalf("monitor.UntrackTask called %d times, want 1", len(mon.UntrackedIDs))
	}
}

// ── Deduplication ─────────────────────────────────────────────────────────────

func TestHandleInboundTask_DuplicateTaskID_ReturnsCurrentStatus(t *testing.T) {
	d, gw, _, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440060")

	if err := d.HandleInboundTask(context.Background(), task); err != nil {
		t.Fatalf("first HandleInboundTask() error = %v, want nil", err)
	}
	if err := d.HandleInboundTask(context.Background(), task); err != nil {
		t.Fatalf("second HandleInboundTask() error = %v, want nil (idempotent)", err)
	}

	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("planner task publishes = %d, want 1 (second must be deduped)", len(gw.TaskSpecCalls))
	}
	if len(gw.ErrorCalls) != 1 {
		t.Fatalf("error calls = %d, want 1 (DUPLICATE_TASK)", len(gw.ErrorCalls))
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeDuplicateTask {
		t.Fatalf("error_code = %q, want DUPLICATE_TASK", gw.ErrorCalls[0].ErrorCode)
	}
}

// TestHandleInboundTask_ReEntryAfterTerminalState verifies that once a prior
// attempt for a given task_id has reached a terminal state (e.g. COMPLETED),
// the same task_id is accepted again as a follow-up. The dedup check must
// only reject while a prior attempt is still in flight. This is the
// "ChatGPT-style follow-up" behaviour: a completed task never really blocks
// further messages; it just gets a fresh orchestrator_task_ref.
func TestHandleInboundTask_ReEntryAfterTerminalState(t *testing.T) {
	d, gw, _, _, _, mem := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440099")

	if err := d.HandleInboundTask(context.Background(), task); err != nil {
		t.Fatalf("first HandleInboundTask() error = %v, want nil", err)
	}
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("after first inbound: planner task publishes = %d, want 1", len(gw.TaskSpecCalls))
	}

	// Simulate the prior attempt reaching COMPLETED by writing a terminal
	// task_state record directly to memory. The next inbound with the same
	// task_id should now be accepted (not rejected as DUPLICATE_TASK) because
	// the prior attempt is no longer in flight.
	terminalState := types.TaskState{
		OrchestratorTaskRef: "orch-ref-prior",
		TaskID:              task.TaskID,
		UserID:              task.UserID,
		State:               types.StateCompleted,
	}
	payload, err := json.Marshal(terminalState)
	if err != nil {
		t.Fatalf("marshal terminal state: %v", err)
	}
	if err := mem.Write(types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: terminalState.OrchestratorTaskRef,
		TaskID:              task.TaskID,
		DataType:            types.DataTypeTaskState,
		Timestamp:           time.Now().UTC(),
		Payload:             payload,
	}); err != nil {
		t.Fatalf("memory.Write terminal state: %v", err)
	}

	if err := d.HandleInboundTask(context.Background(), task); err != nil {
		t.Fatalf("re-entry HandleInboundTask() error = %v, want nil", err)
	}

	if len(gw.TaskSpecCalls) != 2 {
		t.Fatalf("after re-entry: planner task publishes = %d, want 2 (re-entry must reach planner)", len(gw.TaskSpecCalls))
	}
	for _, e := range gw.ErrorCalls {
		if e.ErrorCode == types.ErrCodeDuplicateTask {
			t.Fatalf("unexpected DUPLICATE_TASK error on re-entry after terminal state")
		}
	}
}

// ── Metrics ──────────────────────────────────────────────────────────────────

func TestGetMetrics_CountersTrackPipeline(t *testing.T) {
	d, gw, pol, _, _, _ := newDispatcher(t)

	// Task 1: happy path.
	task1 := validTask("550e8400-e29b-41d4-a716-446655440070")
	_ = d.HandleInboundTask(context.Background(), task1)

	// Task 2: policy violation.
	pol.ShouldDeny = true
	task2 := validTask("550e8400-e29b-41d4-a716-446655440071")
	_ = d.HandleInboundTask(context.Background(), task2)
	pol.ShouldDeny = false

	received, completed, failed, violations, decompositionFailed, queueDepth := d.GetMetrics()

	if received != 2 {
		t.Fatalf("tasksReceived = %d, want 2", received)
	}
	if violations != 1 {
		t.Fatalf("policyViolations = %d, want 1", violations)
	}
	if queueDepth != 1 {
		t.Fatalf("queueDepth = %d, want 1 (task1 still in flight)", queueDepth)
	}

	// Now complete task1 via plan callbacks.
	activeTasks := d.GetActiveTasks()
	ts1 := activeTasks[0]

	plan := validPlan(task1.TaskID)
	_ = d.HandleDecompositionResponse(context.Background(), decompositionResponse(task1.TaskID, plan))
	d.HandlePlanComplete(ts1, []types.PriorResult{{SubtaskID: "s1", Result: json.RawMessage(`{}`)}})

	received, completed, failed, violations, decompositionFailed, queueDepth = d.GetMetrics()
	if completed != 1 {
		t.Fatalf("tasksCompleted = %d, want 1", completed)
	}
	if failed != 0 {
		t.Fatalf("tasksFailed = %d, want 0", failed)
	}
	if queueDepth != 0 {
		t.Fatalf("queueDepth = %d, want 0", queueDepth)
	}

	_ = gw
	_ = decompositionFailed
}

// ── Active Tasks Snapshot ─────────────────────────────────────────────────────

func TestGetActiveTasks_ReflectsInFlightTasks(t *testing.T) {
	d, _, _, _, _, _ := newDispatcher(t)

	if len(d.GetActiveTasks()) != 0 {
		t.Fatal("expected 0 active tasks before any submission")
	}

	task1 := validTask("550e8400-e29b-41d4-a716-446655440080")
	task2 := validTask("550e8400-e29b-41d4-a716-446655440081")
	_ = d.HandleInboundTask(context.Background(), task1)
	_ = d.HandleInboundTask(context.Background(), task2)

	if len(d.GetActiveTasks()) != 2 {
		t.Fatalf("active tasks = %d, want 2", len(d.GetActiveTasks()))
	}

	// Complete task1.
	activeTasks := d.GetActiveTasks()
	var ts1 *types.TaskState
	for _, ts := range activeTasks {
		if ts.TaskID == task1.TaskID {
			ts1 = ts
			break
		}
	}
	if ts1 == nil {
		t.Fatal("could not find task1 in active tasks")
	}

	plan1 := validPlan(task1.TaskID)
	_ = d.HandleDecompositionResponse(context.Background(), decompositionResponse(task1.TaskID, plan1))
	d.HandlePlanComplete(ts1, nil)

	if len(d.GetActiveTasks()) != 1 {
		t.Fatalf("active tasks = %d, want 1 after first completion", len(d.GetActiveTasks()))
	}
}

// ── Dispatcher Demo Flow (v3.0) ───────────────────────────────────────────────

func TestDispatcherDemoFlow_V3(t *testing.T) {
	gw := &gatewayMock{}
	pol := &policyMock{}
	mon := &monitorMock{}
	exec := &executorMock{}
	mem := mocks.NewMemoryMock()
	cfg := &config.OrchestratorConfig{
		NodeID:                      "demo-node",
		DecompositionTimeoutSeconds: 30,
		MaxSubtasksPerPlan:          20,
		PlanExecutorMaxParallel:     5,
		// This flow asserts unattended plan execution; the approval gate
		// (which would park the 3-subtask plan in AWAITING_APPROVAL) is
		// exercised separately in TestPlanApprovalGate_ApproveAndReject.
		PlanApprovalMode: "off",
	}
	d := dispatcher.New(cfg, mem, nil, gw, pol, mon, exec, ioclient.New("") /* disabled */)

	t.Log("demo setup: Task Dispatcher v3.0 wired to mocks for Gateway, PolicyEnforcer, Monitor, PlanExecutor, MemoryClient")
	t.Log("demo setup: all external components (NATS, Vault, DB, Planner Agent) replaced by in-process test doubles")

	t.Log("─────────────────────────────────────────────────────────────────────")
	t.Log("step 1: happy path — task submitted, flows to DECOMPOSING")

	task1 := types.UserTask{
		TaskID:         "550e8400-e29b-41d4-a716-446655440000",
		UserID:         "user-alice",
		Priority:       7,
		TimeoutSeconds: 120,
		Payload:        json.RawMessage(`{"raw_input":"book a flight from NYC to LA for next Friday"}`),
		CallbackTopic:  "aegis.user-io.results.task-1",
	}

	if err := d.HandleInboundTask(context.Background(), task1); err != nil {
		t.Fatalf("step 1: HandleInboundTask() error = %v, want nil", err)
	}

	orchRef1 := orchRefFromPlannerTask(t, gw)
	if orchRef1 == task1.TaskID {
		t.Fatal("step 1: orchestrator_task_ref must be distinct from task_id")
	}
	t.Logf("step 1: generated orchestrator_task_ref=%s (distinct from task_id=%s)", orchRef1, task1.TaskID)

	req1 := gw.TaskSpecCalls[0]
	if req1.PolicyScope.TokenRef == "" {
		t.Fatal("step 1: planner task policy_scope.token_ref is empty")
	}
	t.Logf("step 1: policy_scope attached to planner task — token_ref=%s domains=%v",
		req1.PolicyScope.TokenRef, req1.PolicyScope.Domains)
	t.Logf("step 1: raw_input embedded in planner instructions: %q", req1.Instructions)

	s1 := taskStateHistory(t, mem, task1.TaskID)
	if len(s1) < 1 || s1[len(s1)-1].State != types.StateDecomposing {
		t.Fatalf("step 1: persisted state = %q, want DECOMPOSING", s1[len(s1)-1].State)
	}
	t.Log("step 1 complete: task persisted as DECOMPOSING, planner task sent to general agent ✓")

	if len(gw.AcceptedCalls) != 1 {
		t.Fatalf("step 1: task_accepted must be sent early (after policy + memory persist); got %d", len(gw.AcceptedCalls))
	}
	t.Log("step 1: task_accepted published early (before Planner Agent round-trip) ✓")

	t.Log("─────────────────────────────────────────────────────────────────────")
	t.Log("step 2: Planner Agent responds with a 3-subtask plan")

	plan1 := types.ExecutionPlan{
		PlanID:       "plan-flight-booking",
		ParentTaskID: task1.TaskID,
		CreatedAt:    time.Now().UTC(),
		Subtasks: []types.Subtask{
			{SubtaskID: "s1", Action: "search_flights", RequiredSkillDomains: []string{"web"},
				Instructions: "Search for flights from NYC to LA for next Friday",
				DependsOn:    []string{}, TimeoutSeconds: 30},
			{SubtaskID: "s2", Action: "find_hotels", RequiredSkillDomains: []string{"web"},
				Instructions: "Find hotels near LAX with a pool",
				DependsOn:    []string{"s1"}, TimeoutSeconds: 30},
			{SubtaskID: "s3", Action: "present_options", RequiredSkillDomains: []string{"calendar"},
				Instructions: "Format and present flight + hotel options to user",
				DependsOn:    []string{"s1", "s2"}, TimeoutSeconds: 30},
		},
	}

	resp1 := decompositionResponse(task1.TaskID, plan1)
	if err := d.HandleDecompositionResponse(context.Background(), resp1); err != nil {
		t.Fatalf("step 2: HandleDecompositionResponse() error = %v", err)
	}

	if len(exec.ExecuteCalls) != 1 {
		t.Fatalf("step 2: executor.Execute calls = %d, want 1", len(exec.ExecuteCalls))
	}
	if exec.ExecuteCalls[0].Plan.PlanID != plan1.PlanID {
		t.Fatalf("step 2: executor received plan_id = %q, want %q", exec.ExecuteCalls[0].Plan.PlanID, plan1.PlanID)
	}
	t.Logf("step 2: plan validated and handed to Plan Executor — plan_id=%s subtasks=%d",
		plan1.PlanID, len(plan1.Subtasks))

	if len(gw.AcceptedCalls) != 1 {
		t.Fatalf("step 2: task_accepted publishes = %d, want 1", len(gw.AcceptedCalls))
	}
	t.Logf("step 2: task_accepted sent to User I/O — orchestrator_task_ref=%s",
		gw.AcceptedCalls[0].OrchestratorTaskRef)

	s2 := taskStateHistory(t, mem, task1.TaskID)
	if s2[len(s2)-1].State != types.StatePlanActive {
		t.Fatalf("step 2: persisted state = %q, want PLAN_ACTIVE", s2[len(s2)-1].State)
	}
	t.Log("step 2 complete: plan active, subtask dispatch delegated to Plan Executor ✓")

	t.Log("─────────────────────────────────────────────────────────────────────")
	t.Log("step 3: duplicate task — same task_id submitted again")

	gw.ErrorCalls = nil
	if err := d.HandleInboundTask(context.Background(), task1); err != nil {
		t.Fatalf("step 3: HandleInboundTask() error = %v, want nil (idempotent no-op)", err)
	}

	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("step 3: planner task publishes = %d, want 1 (second must be rejected at dedup)",
			len(gw.TaskSpecCalls))
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeDuplicateTask {
		t.Fatalf("step 3: error_code = %q, want DUPLICATE_TASK", safeErrorCode(gw))
	}
	t.Logf("step 3: DUPLICATE_TASK returned — current_state mentioned: %q", gw.ErrorCalls[0].UserMessage)
	t.Log("step 3 complete: duplicate rejected at dedup; no second Planner Agent call ✓")

	gw.ErrorCalls = nil

	t.Log("─────────────────────────────────────────────────────────────────────")
	t.Log("step 4: policy violation — Vault denies task for out-of-scope domain")

	pol.ShouldDeny = true
	pol.DenyReason = "required domain 'financial' not in user policy"

	task2 := types.UserTask{
		TaskID:         "550e8400-e29b-41d4-a716-446655440001",
		UserID:         "user-bob",
		Priority:       5,
		TimeoutSeconds: 60,
		Payload:        json.RawMessage(`{"raw_input":"transfer money to account 12345"}`),
		CallbackTopic:  "aegis.user-io.results.task-2",
	}

	decompBefore := len(gw.TaskSpecCalls)
	if err := d.HandleInboundTask(context.Background(), task2); err == nil {
		t.Fatal("step 4: HandleInboundTask() error = nil, want policy denied error")
	}

	if len(gw.TaskSpecCalls) != decompBefore {
		t.Fatal("step 4: planner task sent — Planner Agent must not be contacted on POLICY_VIOLATION")
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodePolicyViolation {
		t.Fatalf("step 4: error_code = %q, want POLICY_VIOLATION", safeErrorCode(gw))
	}
	t.Log("step 4 complete: POLICY_VIOLATION enforced before any decomposition ✓")

	pol.ShouldDeny = false
	gw.ErrorCalls = nil

	t.Log("─────────────────────────────────────────────────────────────────────")
	t.Log("step 5: plan completion — all subtasks complete, task reaches COMPLETED")

	activeTasks := d.GetActiveTasks()
	if len(activeTasks) != 1 {
		t.Fatalf("step 5: expected 1 active task (task1), got %d", len(activeTasks))
	}
	ts1 := activeTasks[0]

	aggregatedResults := []types.PriorResult{
		{SubtaskID: "s1", Result: json.RawMessage(`{"flights":["UA123","AA456"]}`)},
		{SubtaskID: "s2", Result: json.RawMessage(`{"hotels":["Marriott LAX","Hilton Garden"]}`)},
		{SubtaskID: "s3", Result: json.RawMessage(`{"presentation":"Here are your options..."}`)},
	}
	d.HandlePlanComplete(ts1, aggregatedResults)

	finalRec := latestTaskStateRecord(t, mem, task1.TaskID)
	if finalRec.State != types.StateCompleted {
		t.Fatalf("step 5: persisted state = %q, want COMPLETED", finalRec.State)
	}
	if len(gw.TaskResultCalls) != 1 {
		t.Fatalf("step 5: task_result publishes = %d, want 1", len(gw.TaskResultCalls))
	}
	if len(pol.RevokedRefs) == 0 {
		t.Fatal("step 5: no credentials revoked after task completion")
	}
	t.Logf("step 5: task COMPLETED — result delivered to callback, credentials revoked for orchRef=%s",
		pol.RevokedRefs[0])
	t.Log("step 5 complete: plan completion handled correctly ✓")

	t.Log("─────────────────────────────────────────────────────────────────────")
	t.Log("step 6: invalid plan — Planner Agent returns a plan with circular deps")

	task3 := types.UserTask{
		TaskID:         "550e8400-e29b-41d4-a716-446655440002",
		UserID:         "user-carol",
		Priority:       3,
		TimeoutSeconds: 60,
		Payload:        json.RawMessage(`{"raw_input":"do impossible circular task"}`),
		CallbackTopic:  "aegis.user-io.results.task-3",
	}
	_ = d.HandleInboundTask(context.Background(), task3)

	cyclicPlan := types.ExecutionPlan{
		PlanID:       "plan-cyclic",
		ParentTaskID: task3.TaskID,
		Subtasks: []types.Subtask{
			{SubtaskID: "s1", Action: "a", RequiredSkillDomains: []string{"web"}, DependsOn: []string{"s2"}, TimeoutSeconds: 30},
			{SubtaskID: "s2", Action: "b", RequiredSkillDomains: []string{"web"}, DependsOn: []string{"s1"}, TimeoutSeconds: 30},
		},
	}
	_ = d.HandleDecompositionResponse(context.Background(), decompositionResponse(task3.TaskID, cyclicPlan))

	rec3 := latestTaskStateRecord(t, mem, task3.TaskID)
	if rec3.State != types.StateDecompositionFailed {
		t.Fatalf("step 6: persisted state = %q, want DECOMPOSITION_FAILED", rec3.State)
	}
	t.Logf("step 6: DECOMPOSITION_FAILED for circular plan — error_code=%s", rec3.ErrorCode)
	t.Log("step 6 complete: invalid plan rejected before any agent dispatch ✓")

	t.Log("─────────────────────────────────────────────────────────────────────")
	t.Log("step 7: final metrics")

	received, completed, failed, violations, decompositionFailed, queueDepth := d.GetMetrics()
	t.Logf("step 7: tasks_received=%d tasks_completed=%d tasks_failed=%d policy_violations=%d decomposition_failed=%d queue_depth=%d",
		received, completed, failed, violations, decompositionFailed, queueDepth)

	if received != 4 { // task1, task1-dup, task2 (policy), task3
		t.Fatalf("step 7: tasksReceived = %d, want 4", received)
	}
	if completed != 1 {
		t.Fatalf("step 7: tasksCompleted = %d, want 1 (task1)", completed)
	}
	if violations != 1 {
		t.Fatalf("step 7: policyViolations = %d, want 1 (task2)", violations)
	}
	if queueDepth != 0 {
		t.Fatalf("step 7: queueDepth = %d, want 0", queueDepth)
	}
	t.Log("step 7 complete: all counters match expected values ✓")

	t.Log("─────────────────────────────────────────────────────────────────────")
	t.Log("demo summary: Task Dispatcher v3.0 completed all 7 steps")
	t.Log("demo summary: no real NATS, Vault, database, or Planner Agent used")
	t.Logf("demo summary: memory records written=%d | policy calls=%d | revocations=%d",
		mem.WriteCallCount, pol.ValidatedCalls, len(pol.RevokedRefs))
}

// ── helpers ───────────────────────────────────────────────────────────────────

func safeErrorCode(gw *gatewayMock) string {
	if len(gw.ErrorCalls) == 0 {
		return "<no error published>"
	}
	return gw.ErrorCalls[0].ErrorCode
}

// ── Multi-step prompting & confirmation (plan approval gate) ─────────────────

// newDispatcherWithApproval is a variant of newDispatcher that enables the
// approval gate in "always" mode for deterministic testing of the approve/
// reject paths regardless of subtask count.
func newDispatcherWithApproval(t *testing.T, mode string) (*dispatcher.Dispatcher, *gatewayMock, *executorMock, *mocks.MemoryMock) {
	t.Helper()
	gw := &gatewayMock{}
	pol := &policyMock{}
	mon := &monitorMock{}
	exec := &executorMock{}
	mem := mocks.NewMemoryMock()
	cfg := &config.OrchestratorConfig{
		NodeID:                      "test-node",
		DecompositionTimeoutSeconds: 30,
		MaxSubtasksPerPlan:          20,
		PlanExecutorMaxParallel:     5,
		PlanApprovalMode:            mode,
		PlanApprovalTimeoutSeconds:  300,
	}
	d := dispatcher.New(cfg, mem, nil, gw, pol, mon, exec, ioclient.New(""))
	return d, gw, exec, mem
}

func TestPlanApprovalGate_ApproveAndReject(t *testing.T) {
	t.Run("approve transitions AWAITING_APPROVAL → PLAN_ACTIVE and starts executor", func(t *testing.T) {
		d, _, exec, mem := newDispatcherWithApproval(t, "always")

		task := validTask("550e8400-e29b-41d4-a716-446655440050")
		if err := d.HandleInboundTask(context.Background(), task); err != nil {
			t.Fatalf("HandleInboundTask() error = %v", err)
		}

		plan := validPlan(task.TaskID)
		if err := d.HandleDecompositionResponse(context.Background(), decompositionResponse(task.TaskID, plan)); err != nil {
			t.Fatalf("HandleDecompositionResponse() error = %v", err)
		}

		rec := latestTaskStateRecord(t, mem, task.TaskID)
		if rec.State != types.StateAwaitingApproval {
			t.Fatalf("state after plan = %q, want AWAITING_APPROVAL", rec.State)
		}
		if len(exec.ExecuteCalls) != 0 {
			t.Fatalf("executor must not run before approval; got %d calls", len(exec.ExecuteCalls))
		}

		if err := d.HandlePlanDecision(context.Background(), types.PlanDecision{
			OrchestratorTaskRef: rec.OrchestratorTaskRef,
			TaskID:              task.TaskID,
			Approved:            true,
		}); err != nil {
			t.Fatalf("HandlePlanDecision(approve) error = %v", err)
		}

		if len(exec.ExecuteCalls) != 1 {
			t.Fatalf("executor.Execute calls after approval = %d, want 1", len(exec.ExecuteCalls))
		}
		after := latestTaskStateRecord(t, mem, task.TaskID)
		if after.State != types.StatePlanActive {
			t.Fatalf("state after approval = %q, want PLAN_ACTIVE", after.State)
		}
	})

	t.Run("reject fails the task with PLAN_REJECTED", func(t *testing.T) {
		d, gw, exec, mem := newDispatcherWithApproval(t, "always")

		task := validTask("550e8400-e29b-41d4-a716-446655440051")
		if err := d.HandleInboundTask(context.Background(), task); err != nil {
			t.Fatalf("HandleInboundTask() error = %v", err)
		}
		plan := validPlan(task.TaskID)
		if err := d.HandleDecompositionResponse(context.Background(), decompositionResponse(task.TaskID, plan)); err != nil {
			t.Fatalf("HandleDecompositionResponse() error = %v", err)
		}

		rec := latestTaskStateRecord(t, mem, task.TaskID)
		if rec.State != types.StateAwaitingApproval {
			t.Fatalf("state = %q, want AWAITING_APPROVAL", rec.State)
		}

		if err := d.HandlePlanDecision(context.Background(), types.PlanDecision{
			OrchestratorTaskRef: rec.OrchestratorTaskRef,
			TaskID:              task.TaskID,
			Approved:            false,
			Reason:              "looks wrong",
		}); err != nil {
			t.Fatalf("HandlePlanDecision(reject) error = %v", err)
		}
		if len(exec.ExecuteCalls) != 0 {
			t.Fatalf("executor must not run on reject; got %d calls", len(exec.ExecuteCalls))
		}
		after := latestTaskStateRecord(t, mem, task.TaskID)
		if after.State != types.StateFailed {
			t.Fatalf("state after reject = %q, want FAILED", after.State)
		}
		if after.ErrorCode != types.ErrCodePlanRejected {
			t.Fatalf("error_code after reject = %q, want %q", after.ErrorCode, types.ErrCodePlanRejected)
		}

		found := false
		for _, e := range gw.ErrorCalls {
			if e.ErrorCode == types.ErrCodePlanRejected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected PLAN_REJECTED error_response published to IO; got %v", gw.ErrorCalls)
		}
	})

	t.Run("multi mode only gates plans with >1 subtask", func(t *testing.T) {
		d, _, exec, mem := newDispatcherWithApproval(t, "multi")

		// Single-subtask plan → proceeds immediately.
		single := validTask("550e8400-e29b-41d4-a716-446655440052")
		if err := d.HandleInboundTask(context.Background(), single); err != nil {
			t.Fatalf("inbound: %v", err)
		}
		if err := d.HandleDecompositionResponse(context.Background(), decompositionResponse(single.TaskID, validPlan(single.TaskID))); err != nil {
			t.Fatalf("decomp: %v", err)
		}
		if got := latestTaskStateRecord(t, mem, single.TaskID).State; got != types.StatePlanActive {
			t.Fatalf("single-subtask plan state = %q, want PLAN_ACTIVE", got)
		}
		if len(exec.ExecuteCalls) != 1 {
			t.Fatalf("executor must run for single-subtask plan; got %d calls", len(exec.ExecuteCalls))
		}

		// Multi-subtask plan → AWAITING_APPROVAL.
		multi := validTask("550e8400-e29b-41d4-a716-446655440053")
		if err := d.HandleInboundTask(context.Background(), multi); err != nil {
			t.Fatalf("inbound (multi): %v", err)
		}
		multiPlan := types.ExecutionPlan{
			PlanID:       "plan-multi",
			ParentTaskID: multi.TaskID,
			CreatedAt:    time.Now().UTC(),
			Subtasks: []types.Subtask{
				{SubtaskID: "s1", RequiredSkillDomains: []string{"web"}, Action: "a", Instructions: "a", DependsOn: []string{}, TimeoutSeconds: 30},
				{SubtaskID: "s2", RequiredSkillDomains: []string{"web"}, Action: "b", Instructions: "b", DependsOn: []string{}, TimeoutSeconds: 30},
			},
		}
		if err := d.HandleDecompositionResponse(context.Background(), decompositionResponse(multi.TaskID, multiPlan)); err != nil {
			t.Fatalf("decomp (multi): %v", err)
		}
		if got := latestTaskStateRecord(t, mem, multi.TaskID).State; got != types.StateAwaitingApproval {
			t.Fatalf("multi-subtask plan state = %q, want AWAITING_APPROVAL", got)
		}
		if len(exec.ExecuteCalls) != 1 {
			t.Fatalf("executor must NOT run for multi-subtask plan yet; got %d calls (expected only the single-plan call)", len(exec.ExecuteCalls))
		}
	})
}
