// Package dispatcher_test provides black-box tests for M2: Task Dispatcher.
//
// Each test demonstrates a distinct scenario from the EDD (§7, §8, §17.3).
// Run with:
//
//	cd orchestrator && go test ./internal/dispatcher/ -v
//
// Demo scenarios covered:
//
//	✅  Happy Path                 — task flows through DISPATCH_PENDING → DISPATCHED → task_accepted
//	✅  Duplicate Task             — same task_id twice → DUPLICATE_TASK; no second agent touched
//	✅  Policy Violation           — Vault denies → POLICY_VIOLATION; capability query NEVER called
//	✅  Schema Errors              — empty skill domains, invalid priority, timeout out of range
//	✅  Memory Fail                — memory write fails before dispatch → error; task_spec NOT published
//	✅  Agents Unavailable         — capability query fails → AGENTS_UNAVAILABLE error
//	✅  Dispatch Failure Recovery  — PublishTaskSpec fails → DELIVERY_FAILED + cleanup + revoke
//	✅  Task Result                — HandleTaskResult writes COMPLETED state + delivers result + revokes credentials
//	✅  Task Failed                — HandleTaskResult with success=false → FAILED state + tasksFailed counter
//	✅  Metrics                    — counters track received, completed, failed, violations, queue depth
//	✅  Active Tasks               — GetActiveTasks reflects in-flight tasks and clears on completion
package dispatcher_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/dispatcher"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// ── Lightweight mocks for dispatcher-internal interfaces ──────────────────────

// gatewayMock records every outbound call the Dispatcher makes to M1.
type gatewayMock struct {
	AcceptedCalls        []types.TaskAccepted
	ErrorCalls           []types.ErrorResponse
	TaskSpecCalls        []types.TaskSpec
	CapabilityQueryCalls []types.CapabilityQuery
	TaskResultCalls      []types.TaskResult

	CapabilityQueryResponse *types.CapabilityResponse
	CapabilityQueryError    error
	PublishSpecError        error
	PublishAcceptedError    error
	PublishResultError      error
}

func (g *gatewayMock) PublishTaskAccepted(_ string, a types.TaskAccepted) error {
	g.AcceptedCalls = append(g.AcceptedCalls, a)
	return g.PublishAcceptedError
}

func (g *gatewayMock) PublishError(_ string, e types.ErrorResponse) error {
	g.ErrorCalls = append(g.ErrorCalls, e)
	return nil
}

func (g *gatewayMock) PublishTaskSpec(s types.TaskSpec) error {
	g.TaskSpecCalls = append(g.TaskSpecCalls, s)
	return g.PublishSpecError
}

func (g *gatewayMock) PublishCapabilityQuery(q types.CapabilityQuery) (*types.CapabilityResponse, error) {
	g.CapabilityQueryCalls = append(g.CapabilityQueryCalls, q)
	if g.CapabilityQueryError != nil {
		return nil, g.CapabilityQueryError
	}
	return g.CapabilityQueryResponse, nil
}

func (g *gatewayMock) PublishTaskResult(_ string, r types.TaskResult) error {
	g.TaskResultCalls = append(g.TaskResultCalls, r)
	return g.PublishResultError
}

// newCapabilityResponse returns a default CapabilityResponse with a matched agent.
func newCapabilityResponse(_ string) *types.CapabilityResponse {
	return &types.CapabilityResponse{
		Match:   types.CapabilityMatch_Match,
		AgentID: "agent-42",
	}
}

// policyMock is a minimal controllable PolicyEnforcer.
type policyMock struct {
	ShouldDeny     bool
	DenyReason     string
	RevokeError    error
	RevokedRefs    []string
	ValidatedCalls int
}

func (p *policyMock) ValidateAndScope(taskID, orchRef, userID string, domains []string, timeout int) (types.PolicyScope, error) {
	p.ValidatedCalls++
	if p.ShouldDeny {
		reason := p.DenyReason
		if reason == "" {
			reason = "policy denied"
		}
		return types.PolicyScope{}, errors.New(reason)
	}
	return types.PolicyScope{
		Domains:   domains,
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

func (p *policyMock) RevokeCredentials(orchRef string) error {
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

// ── Test Helpers ─────────────────────────────────────────────────────────────

// newDispatcher builds a fully wired Dispatcher with fresh mocks.
func newDispatcher(t *testing.T) (*dispatcher.Dispatcher, *gatewayMock, *policyMock, *monitorMock, *mocks.MemoryMock) {
	t.Helper()
	gw := &gatewayMock{}
	pol := &policyMock{}
	mon := &monitorMock{}
	mem := mocks.NewMemoryMock()

	cfg := &config.OrchestratorConfig{NodeID: "test-node"}
	d := dispatcher.New(cfg, mem, nil /* vault unused in dispatcher */, gw, pol, mon)
	return d, gw, pol, mon, mem
}

// validTask returns a minimal, schema-valid UserTask ready for submission.
func validTask(taskID string) types.UserTask {
	return types.UserTask{
		TaskID:               taskID,
		UserID:               "user-1",
		RequiredSkillDomains: []string{"web"},
		Priority:             5,
		TimeoutSeconds:       60,
		Payload:              json.RawMessage(`{"url":"https://example.com"}`),
		CallbackTopic:        "aegis.user-io.results.task-1",
	}
}

// orchRefFromSpec extracts the orchestrator_task_ref from the first PublishTaskSpec call.
func orchRefFromSpec(t *testing.T, gw *gatewayMock) string {
	t.Helper()
	if len(gw.TaskSpecCalls) == 0 {
		t.Fatal("no task_spec was published")
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

// ── Happy Path ───────────────────────────────────────────────────────────────

func TestHandleInboundTask_HappyPath_NewAgent(t *testing.T) {
	d, gw, pol, mon, mem := newDispatcher(t)

	gw.CapabilityQueryResponse = &types.CapabilityResponse{
		Match:   types.CapabilityMatch_NoMatch,
		AgentID: "agent-new",
	}

	task := validTask("550e8400-e29b-41d4-a716-446655440000")
	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("HandleInboundTask() error = %v, want nil", err)
	}

	if pol.ValidatedCalls != 1 {
		t.Fatalf("policy.ValidatedCalls = %d, want 1", pol.ValidatedCalls)
	}
	if len(gw.CapabilityQueryCalls) != 1 {
		t.Fatalf("capability queries = %d, want 1", len(gw.CapabilityQueryCalls))
	}
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("task_spec publishes = %d, want 1", len(gw.TaskSpecCalls))
	}
	if gw.TaskSpecCalls[0].TaskID != task.TaskID {
		t.Fatalf("task_spec.task_id = %q, want %q", gw.TaskSpecCalls[0].TaskID, task.TaskID)
	}
	if len(gw.AcceptedCalls) != 1 {
		t.Fatalf("task_accepted publishes = %d, want 1", len(gw.AcceptedCalls))
	}

	rec := latestTaskStateRecord(t, mem, task.TaskID)
	if rec.State != types.StateDispatched {
		t.Fatalf("persisted state = %q, want DISPATCHED", rec.State)
	}
	if rec.AgentID != "agent-new" {
		t.Fatalf("persisted agent_id = %q, want agent-new", rec.AgentID)
	}

	if len(mon.TrackedTasks) != 1 {
		t.Fatalf("monitor.TrackedTasks = %d, want 1", len(mon.TrackedTasks))
	}

	orchRef := orchRefFromSpec(t, gw)
	if orchRef == task.TaskID {
		t.Fatal("orchestrator_task_ref must be distinct from task_id")
	}
	if len(gw.ErrorCalls) != 0 {
		t.Fatalf("unexpected errors published: %+v", gw.ErrorCalls)
	}
}

func TestHandleInboundTask_HappyPath_ExistingAgent(t *testing.T) {
	d, gw, _, _, _ := newDispatcher(t)
	gw.CapabilityQueryResponse = &types.CapabilityResponse{
		Match:   types.CapabilityMatch_Match,
		AgentID: "agent-idle-42",
	}

	task := validTask("550e8400-e29b-41d4-a716-446655440001")
	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("HandleInboundTask() error = %v, want nil", err)
	}

	if gw.TaskSpecCalls[0].OrchestratorTaskRef == "" {
		t.Fatal("task_spec missing orchestrator_task_ref")
	}
	if gw.AcceptedCalls[0].AgentID != "agent-idle-42" {
		t.Fatalf("task_accepted.agent_id = %q, want agent-idle-42", gw.AcceptedCalls[0].AgentID)
	}
	if gw.TaskSpecCalls[0].PolicyScope.TokenRef == "" {
		t.Fatal("task_spec.policy_scope.token_ref is empty — policy scope not attached")
	}
}

func TestHandleInboundTask_PersistsDispatchPendingThenDispatched(t *testing.T) {
	d, gw, _, _, mem := newDispatcher(t)
	gw.CapabilityQueryResponse = newCapabilityResponse("")

	task := validTask("550e8400-e29b-41d4-a716-446655440098")
	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("HandleInboundTask() error = %v", err)
	}

	states := taskStateHistory(t, mem, task.TaskID)
	if len(states) < 2 {
		t.Fatalf("persisted states = %d, want at least 2", len(states))
	}
	if states[0].State != types.StateDispatchPending {
		t.Fatalf("first persisted state = %q, want DISPATCH_PENDING", states[0].State)
	}
	if states[len(states)-1].State != types.StateDispatched {
		t.Fatalf("last persisted state = %q, want DISPATCHED", states[len(states)-1].State)
	}
}

// ── Deduplication ────────────────────────────────────────────────────────────

func TestHandleInboundTask_DuplicateTaskID_ReturnsCurrentStatus(t *testing.T) {
	d, gw, _, _, _ := newDispatcher(t)
	gw.CapabilityQueryResponse = newCapabilityResponse("")

	task := validTask("550e8400-e29b-41d4-a716-446655440002")

	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("first HandleInboundTask() error = %v, want nil", err)
	}
	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("second HandleInboundTask() error = %v, want nil (idempotent)", err)
	}

	if len(gw.CapabilityQueryCalls) != 1 {
		t.Fatalf("capability queries = %d, want 1 (second must be deduped)", len(gw.CapabilityQueryCalls))
	}
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("task_spec publishes = %d, want 1", len(gw.TaskSpecCalls))
	}
	if len(gw.ErrorCalls) != 1 {
		t.Fatalf("error calls = %d, want 1 (DUPLICATE_TASK)", len(gw.ErrorCalls))
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeDuplicateTask {
		t.Fatalf("error_code = %q, want DUPLICATE_TASK", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── Policy Violation ─────────────────────────────────────────────────────────

func TestHandleInboundTask_PolicyViolation_NoAgentTouched(t *testing.T) {
	d, gw, pol, _, _ := newDispatcher(t)
	pol.ShouldDeny = true
	pol.DenyReason = "required domain 'financial' not in user policy"

	task := validTask("550e8400-e29b-41d4-a716-446655440003")
	err := d.HandleInboundTask(task)
	if err == nil {
		t.Fatal("HandleInboundTask() error = nil, want policy denied error")
	}

	if len(gw.CapabilityQueryCalls) != 0 {
		t.Fatalf("capability queries = %d, want 0 (no agent should be touched on policy deny)", len(gw.CapabilityQueryCalls))
	}
	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("task_spec was published — agent must not be touched on POLICY_VIOLATION")
	}
	if len(gw.ErrorCalls) != 1 {
		t.Fatalf("error calls = %d, want 1 (POLICY_VIOLATION)", len(gw.ErrorCalls))
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodePolicyViolation {
		t.Fatalf("error_code = %q, want POLICY_VIOLATION", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── Schema Validation ────────────────────────────────────────────────────────

func TestHandleInboundTask_EmptySkillDomains_InvalidTaskSpec(t *testing.T) {
	d, gw, pol, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440004")
	task.RequiredSkillDomains = []string{}

	err := d.HandleInboundTask(task)
	if err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC")
	}

	if pol.ValidatedCalls != 0 {
		t.Fatal("policy was called — must short-circuit on schema failure")
	}
	if len(gw.CapabilityQueryCalls) != 0 {
		t.Fatal("capability query was called — must short-circuit on schema failure")
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", safeErrorCode(gw))
	}
}

func TestHandleInboundTask_MissingTaskID_InvalidTaskSpec(t *testing.T) {
	d, gw, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440005")
	task.TaskID = ""

	if err := d.HandleInboundTask(task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", gw.ErrorCalls[0].ErrorCode)
	}
	if !strings.Contains(gw.ErrorCalls[0].UserMessage, "task_id") {
		t.Fatalf("user_message = %q, want mention of task_id", gw.ErrorCalls[0].UserMessage)
	}
}

func TestHandleInboundTask_PriorityOutOfRange_InvalidTaskSpec(t *testing.T) {
	d, gw, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440006")
	task.Priority = 0

	if err := d.HandleInboundTask(task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC for priority=0")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", gw.ErrorCalls[0].ErrorCode)
	}
}

func TestHandleInboundTask_TimeoutTooShort_InvalidTaskSpec(t *testing.T) {
	d, gw, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440007")
	task.TimeoutSeconds = 10

	if err := d.HandleInboundTask(task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC for timeout=10")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", gw.ErrorCalls[0].ErrorCode)
	}
}

func TestHandleInboundTask_PayloadTooLarge_InvalidTaskSpec(t *testing.T) {
	d, gw, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440008")
	bigPayload := make([]byte, 1<<20+1)
	for i := range bigPayload {
		bigPayload[i] = 'x'
	}
	task.Payload = json.RawMessage(fmt.Sprintf("%q", string(bigPayload)))

	if err := d.HandleInboundTask(task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC for oversized payload")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── Memory-Before-Dispatch Safety ────────────────────────────────────────────

func TestHandleInboundTask_MemoryFailBeforeDispatch_NoOrphanedAgent(t *testing.T) {
	d, gw, _, _, mem := newDispatcher(t)
	gw.CapabilityQueryResponse = newCapabilityResponse("")
	mem.ShouldFailWrites = true

	task := validTask("550e8400-e29b-41d4-a716-446655440009")
	err := d.HandleInboundTask(task)
	if err == nil {
		t.Fatal("HandleInboundTask() error = nil, want memory error")
	}

	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("task_spec was published despite memory write failure — this would create an orphaned agent")
	}
	if len(gw.ErrorCalls) == 0 {
		t.Fatal("no error sent to User I/O — caller must be notified of failure")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeStorageUnavailable {
		t.Fatalf("error_code = %q, want STORAGE_UNAVAILABLE", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── Agents Unavailable ───────────────────────────────────────────────────────

func TestHandleInboundTask_AgentsUnavailable_ReturnsError(t *testing.T) {
	d, gw, _, _, _ := newDispatcher(t)
	gw.CapabilityQueryError = errors.New("capability_query timed out after 3s")

	task := validTask("550e8400-e29b-41d4-a716-44665544000a")
	if err := d.HandleInboundTask(task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want agents unavailable error")
	}

	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("task_spec published despite agents being unavailable")
	}
	if len(gw.ErrorCalls) == 0 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeAgentsUnavailable {
		t.Fatalf("error_code = %q, want AGENTS_UNAVAILABLE", safeErrorCode(gw))
	}
}

func TestHandleInboundTask_DispatchFailure_PersistsDeliveryFailedAndCleansUp(t *testing.T) {
	d, gw, pol, mon, mem := newDispatcher(t)

	gw.CapabilityQueryResponse = newCapabilityResponse("")
	gw.PublishSpecError = errors.New("nats publish failed")

	task := validTask("550e8400-e29b-41d4-a716-446655440099")

	err := d.HandleInboundTask(task)
	if err == nil {
		t.Fatal("HandleInboundTask() error = nil, want dispatch error")
	}

	rec := latestTaskStateRecord(t, mem, task.TaskID)
	if rec.State != types.StateDeliveryFailed {
		t.Fatalf("persisted state = %q, want DELIVERY_FAILED", rec.State)
	}
	if rec.ErrorCode != types.ErrCodeAgentsUnavailable {
		t.Fatalf("persisted error_code = %q, want AGENTS_UNAVAILABLE", rec.ErrorCode)
	}
	if len(rec.StateHistory) == 0 || rec.StateHistory[len(rec.StateHistory)-1].State != types.StateDeliveryFailed {
		t.Fatalf("last state history entry = %+v, want DELIVERY_FAILED", rec.StateHistory[len(rec.StateHistory)-1])
	}

	if len(pol.RevokedRefs) != 1 {
		t.Fatalf("revoked refs = %d, want 1", len(pol.RevokedRefs))
	}

	if len(d.GetActiveTasks()) != 0 {
		t.Fatal("expected no active tasks after dispatch failure cleanup")
	}
	if len(mon.UntrackedIDs) != 1 {
		t.Fatalf("monitor.UntrackTask called %d times, want 1", len(mon.UntrackedIDs))
	}

	if len(gw.ErrorCalls) != 1 {
		t.Fatalf("error calls = %d, want 1", len(gw.ErrorCalls))
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeAgentsUnavailable {
		t.Fatalf("error_code = %q, want AGENTS_UNAVAILABLE", gw.ErrorCalls[0].ErrorCode)
	}

	_, _, _, _, queueDepth := d.GetMetrics()
	if queueDepth != 0 {
		t.Fatalf("queueDepth = %d, want 0 after cleanup", queueDepth)
	}
}

// ── Task Result Processing ───────────────────────────────────────────────────

func TestHandleTaskResult_WritesCompletedState_DeliversResult(t *testing.T) {
	d, gw, pol, mon, mem := newDispatcher(t)
	gw.CapabilityQueryResponse = newCapabilityResponse("")

	task := validTask("550e8400-e29b-41d4-a716-44665544000b")
	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("setup HandleInboundTask() error = %v", err)
	}
	orchRef := orchRefFromSpec(t, gw)

	result := types.TaskResult{
		OrchestratorTaskRef: orchRef,
		AgentID:             "agent-42",
		Success:             true,
		Result:              json.RawMessage(`{"summary":"done"}`),
		CompletedAt:         time.Now().UTC(),
	}
	if err := d.HandleTaskResult(result); err != nil {
		t.Fatalf("HandleTaskResult() error = %v", err)
	}

	rec := latestTaskStateRecord(t, mem, task.TaskID)
	if rec.State != types.StateCompleted {
		t.Fatalf("persisted state = %q, want COMPLETED", rec.State)
	}

	if len(gw.TaskResultCalls) != 1 {
		t.Fatalf("task_result publishes = %d, want 1", len(gw.TaskResultCalls))
	}

	if len(pol.RevokedRefs) != 1 {
		t.Fatalf("revoked refs = %d, want 1", len(pol.RevokedRefs))
	}
	if pol.RevokedRefs[0] != orchRef {
		t.Fatalf("revoked ref = %q, want %q", pol.RevokedRefs[0], orchRef)
	}

	if len(d.GetActiveTasks()) != 0 {
		t.Fatal("task still in GetActiveTasks() after completion — expected empty")
	}
	if len(mon.UntrackedIDs) != 1 {
		t.Fatalf("monitor.UntrackTask called %d times, want 1", len(mon.UntrackedIDs))
	}
}

func TestHandleTaskResult_FailedTask_WritesFailedState(t *testing.T) {
	d, gw, _, _, mem := newDispatcher(t)
	gw.CapabilityQueryResponse = newCapabilityResponse("")

	task := validTask("550e8400-e29b-41d4-a716-44665544000c")
	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("setup error: %v", err)
	}
	orchRef := orchRefFromSpec(t, gw)

	result := types.TaskResult{
		OrchestratorTaskRef: orchRef,
		AgentID:             "agent-42",
		Success:             false,
		ErrorCode:           types.ErrCodeTimedOut,
		CompletedAt:         time.Now().UTC(),
	}
	if err := d.HandleTaskResult(result); err != nil {
		t.Fatalf("HandleTaskResult() error = %v", err)
	}

	rec := latestTaskStateRecord(t, mem, task.TaskID)
	if rec.State != types.StateFailed {
		t.Fatalf("persisted state = %q, want FAILED", rec.State)
	}
}

// ── Metrics ──────────────────────────────────────────────────────────────────

func TestGetMetrics_CountersTrackPipeline(t *testing.T) {
	d, gw, pol, _, _ := newDispatcher(t)
	gw.CapabilityQueryResponse = newCapabilityResponse("")

	task1 := validTask("550e8400-e29b-41d4-a716-44665544000d")
	_ = d.HandleInboundTask(task1)

	pol.ShouldDeny = true
	task2 := validTask("550e8400-e29b-41d4-a716-44665544000e")
	_ = d.HandleInboundTask(task2)
	pol.ShouldDeny = false

	received, completed, failed, violations, queueDepth := d.GetMetrics()

	if received != 2 {
		t.Fatalf("tasksReceived = %d, want 2", received)
	}
	if violations != 1 {
		t.Fatalf("policyViolations = %d, want 1", violations)
	}
	if queueDepth != 1 {
		t.Fatalf("queueDepth = %d, want 1 (task1 still in flight)", queueDepth)
	}

	orchRef := orchRefFromSpec(t, gw)
	_ = d.HandleTaskResult(types.TaskResult{
		OrchestratorTaskRef: orchRef,
		Success:             true,
		CompletedAt:         time.Now().UTC(),
	})

	received, completed, failed, violations, queueDepth = d.GetMetrics()
	if completed != 1 {
		t.Fatalf("tasksCompleted = %d, want 1", completed)
	}
	if failed != 0 {
		t.Fatalf("tasksFailed = %d, want 0", failed)
	}
	if queueDepth != 0 {
		t.Fatalf("queueDepth = %d, want 0 (task completed)", queueDepth)
	}
	_ = received
	_ = violations
}

// ── Active Tasks Snapshot ────────────────────────────────────────────────────

func TestGetActiveTasks_ReflectsInFlightTasks(t *testing.T) {
	d, gw, _, _, _ := newDispatcher(t)
	gw.CapabilityQueryResponse = newCapabilityResponse("")

	if len(d.GetActiveTasks()) != 0 {
		t.Fatal("expected 0 active tasks before any submission")
	}

	task1 := validTask("550e8400-e29b-41d4-a716-44665544000f")
	task2 := validTask("550e8400-e29b-41d4-a716-446655440010")
	_ = d.HandleInboundTask(task1)
	_ = d.HandleInboundTask(task2)

	if len(d.GetActiveTasks()) != 2 {
		t.Fatalf("active tasks = %d, want 2", len(d.GetActiveTasks()))
	}

	orchRef1 := gw.TaskSpecCalls[0].OrchestratorTaskRef
	_ = d.HandleTaskResult(types.TaskResult{
		OrchestratorTaskRef: orchRef1,
		Success:             true,
		CompletedAt:         time.Now().UTC(),
	})

	if len(d.GetActiveTasks()) != 1 {
		t.Fatalf("active tasks = %d, want 1 after first completion", len(d.GetActiveTasks()))
	}
}

// ── Dispatcher Demo Flow ─────────────────────────────────────────────────────

func TestDispatcherDemoFlow(t *testing.T) {
	gw := &gatewayMock{}
	pol := &policyMock{}
	mon := &monitorMock{}
	mem := mocks.NewMemoryMock()
	cfg := &config.OrchestratorConfig{NodeID: "demo-node"}
	d := dispatcher.New(cfg, mem, nil, gw, pol, mon)

	gw.CapabilityQueryResponse = &types.CapabilityResponse{
		Match:   types.CapabilityMatch_Match,
		AgentID: "agent-demo-42",
	}

	t.Log("demo setup: Task Dispatcher wired to mocks for Gateway, PolicyEnforcer, Monitor, and MemoryClient")
	t.Log("demo setup: all external components (NATS, Vault, DB) replaced by in-process test doubles")

	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 1: happy path — submitting a valid task")

	task1 := types.UserTask{
		TaskID:               "550e8400-e29b-41d4-a716-446655440000",
		UserID:               "user-alice",
		RequiredSkillDomains: []string{"web", "research"},
		Priority:             7,
		TimeoutSeconds:       120,
		Payload:              json.RawMessage(`{"url":"https://example.com","action":"summarise"}`),
		CallbackTopic:        "aegis.user-io.results.task-1",
	}

	if err := d.HandleInboundTask(task1); err != nil {
		t.Fatalf("step 1: HandleInboundTask() error = %v, want nil", err)
	}

	orchRef1 := gw.TaskSpecCalls[0].OrchestratorTaskRef
	if orchRef1 == task1.TaskID {
		t.Fatal("step 1: orchestrator_task_ref must be distinct from task_id")
	}
	t.Logf("step 1: generated orchestrator_task_ref=%s (distinct from task_id=%s)", orchRef1, task1.TaskID)

	if gw.TaskSpecCalls[0].PolicyScope.TokenRef == "" {
		t.Fatal("step 1: task_spec.policy_scope.token_ref is empty — policy scope not attached")
	}
	t.Logf("step 1: policy_scope attached — token_ref=%s domains=%v",
		gw.TaskSpecCalls[0].PolicyScope.TokenRef,
		gw.TaskSpecCalls[0].PolicyScope.Domains,
	)

	stateHistory := taskStateHistory(t, mem, task1.TaskID)
	if len(stateHistory) < 2 {
		t.Fatalf("step 1: expected at least 2 persisted task states, got %d", len(stateHistory))
	}
	if stateHistory[0].State != types.StateDispatchPending {
		t.Fatalf("step 1: first persisted state = %q, want DISPATCH_PENDING", stateHistory[0].State)
	}
	if stateHistory[len(stateHistory)-1].State != types.StateDispatched {
		t.Fatalf("step 1: final persisted state = %q, want DISPATCHED", stateHistory[len(stateHistory)-1].State)
	}
	t.Logf("step 1: task_state persisted in order — %s → %s",
		stateHistory[0].State, stateHistory[len(stateHistory)-1].State)

	if len(gw.AcceptedCalls) != 1 {
		t.Fatalf("step 1: task_accepted publishes = %d, want 1", len(gw.AcceptedCalls))
	}
	t.Logf("step 1: task_accepted sent to User I/O — agent_id=%s orch_ref=%s",
		gw.AcceptedCalls[0].AgentID, gw.AcceptedCalls[0].OrchestratorTaskRef)
	t.Log("step 1 complete: task dispatched successfully ✓")

	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 2: duplicate task — submitting the same task_id a second time")

	if err := d.HandleInboundTask(task1); err != nil {
		t.Fatalf("step 2: HandleInboundTask() error = %v, want nil (idempotent no-op)", err)
	}

	if len(gw.CapabilityQueryCalls) != 1 {
		t.Fatalf("step 2: capability queries = %d, want 1 (second must be rejected at dedup)", len(gw.CapabilityQueryCalls))
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeDuplicateTask {
		t.Fatalf("step 2: error_code = %q, want DUPLICATE_TASK", safeErrorCode(gw))
	}
	t.Logf("step 2: DUPLICATE_TASK returned for task_id=%s — current_state mentioned in message: %q",
		task1.TaskID, gw.ErrorCalls[0].UserMessage)
	t.Log("step 2 complete: duplicate rejected; no second agent spawned ✓")

	gw.ErrorCalls = nil

	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 3: policy violation — Vault denies task for user-bob")

	pol.ShouldDeny = true
	pol.DenyReason = "required domain 'financial' not in user-bob policy"

	task2 := types.UserTask{
		TaskID:               "550e8400-e29b-41d4-a716-446655440001",
		UserID:               "user-bob",
		RequiredSkillDomains: []string{"financial"},
		Priority:             5,
		TimeoutSeconds:       60,
		Payload:              json.RawMessage(`{}`),
		CallbackTopic:        "aegis.user-io.results.task-2",
	}

	capQueriesBefore := len(gw.CapabilityQueryCalls)
	if err := d.HandleInboundTask(task2); err == nil {
		t.Fatal("step 3: HandleInboundTask() error = nil, want policy denied error")
	}

	if len(gw.CapabilityQueryCalls) != capQueriesBefore {
		t.Fatal("step 3: capability query was called — agent must never be touched on POLICY_VIOLATION")
	}
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatal("step 3: task_spec count changed — no new dispatch should occur on POLICY_VIOLATION")
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodePolicyViolation {
		t.Fatalf("step 3: error_code = %q, want POLICY_VIOLATION", safeErrorCode(gw))
	}
	t.Logf("step 3: POLICY_VIOLATION sent to User I/O — message: %q", gw.ErrorCalls[0].UserMessage)
	t.Log("step 3 complete: policy enforced; no agent was provisioned ✓")

	pol.ShouldDeny = false
	gw.ErrorCalls = nil

	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 4: schema rejection — task submitted with empty required_skill_domains")

	task3 := types.UserTask{
		TaskID:               "550e8400-e29b-41d4-a716-446655440002",
		UserID:               "user-carol",
		RequiredSkillDomains: []string{},
		Priority:             3,
		TimeoutSeconds:       30,
		Payload:              json.RawMessage(`{}`),
		CallbackTopic:        "aegis.user-io.results.task-3",
	}

	policyCallsBefore := pol.ValidatedCalls
	if err := d.HandleInboundTask(task3); err == nil {
		t.Fatal("step 4: HandleInboundTask() error = nil, want INVALID_TASK_SPEC")
	}
	if pol.ValidatedCalls != policyCallsBefore {
		t.Fatal("step 4: policy enforcer was called — must short-circuit on schema failure")
	}
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("step 4: error_code = %q, want INVALID_TASK_SPEC", safeErrorCode(gw))
	}
	t.Logf("step 4: INVALID_TASK_SPEC returned — reason: %q", gw.ErrorCalls[0].UserMessage)
	t.Log("step 4 complete: schema validated before any downstream call ✓")

	gw.ErrorCalls = nil

	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 5: task result — agent reports success for task-1")

	result1 := types.TaskResult{
		OrchestratorTaskRef: orchRef1,
		AgentID:             "agent-demo-42",
		Success:             true,
		Result:              json.RawMessage(`{"summary":"Example.com is a placeholder domain."}`),
		CompletedAt:         time.Now().UTC(),
	}

	if err := d.HandleTaskResult(result1); err != nil {
		t.Fatalf("step 5: HandleTaskResult() error = %v", err)
	}

	rec1Final := latestTaskStateRecord(t, mem, task1.TaskID)
	if rec1Final.State != types.StateCompleted {
		t.Fatalf("step 5: persisted state = %q, want COMPLETED", rec1Final.State)
	}
	t.Logf("step 5: task_state updated in Memory — state=%s completed_at=%s",
		rec1Final.State, rec1Final.CompletedAt.Format(time.RFC3339))

	if len(gw.TaskResultCalls) != 1 {
		t.Fatalf("step 5: task_result publishes = %d, want 1", len(gw.TaskResultCalls))
	}
	t.Logf("step 5: task_result delivered to callback_topic=%s", task1.CallbackTopic)

	if len(pol.RevokedRefs) != 1 || pol.RevokedRefs[0] != orchRef1 {
		t.Fatalf("step 5: revoked refs = %v, want [%s]", pol.RevokedRefs, orchRef1)
	}
	t.Logf("step 5: credential revocation triggered for orchestrator_task_ref=%s", pol.RevokedRefs[0])

	if len(d.GetActiveTasks()) != 0 {
		t.Fatalf("step 5: active tasks = %d, want 0 after completion", len(d.GetActiveTasks()))
	}
	t.Log("step 5 complete: result processed, credentials revoked, task untracked ✓")

	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 6: failed task — agent reports failure, state transitions to FAILED")

	task4 := types.UserTask{
		TaskID:               "550e8400-e29b-41d4-a716-446655440003",
		UserID:               "user-alice",
		RequiredSkillDomains: []string{"code"},
		Priority:             5,
		TimeoutSeconds:       30,
		Payload:              json.RawMessage(`{"repo":"github.com/example/broken"}`),
		CallbackTopic:        "aegis.user-io.results.task-4",
	}
	if err := d.HandleInboundTask(task4); err != nil {
		t.Fatalf("step 6: HandleInboundTask() error = %v", err)
	}
	orchRef4 := gw.TaskSpecCalls[len(gw.TaskSpecCalls)-1].OrchestratorTaskRef

	result4 := types.TaskResult{
		OrchestratorTaskRef: orchRef4,
		AgentID:             "agent-demo-42",
		Success:             false,
		ErrorCode:           types.ErrCodeTimedOut,
		CompletedAt:         time.Now().UTC(),
	}
	if err := d.HandleTaskResult(result4); err != nil {
		t.Fatalf("step 6: HandleTaskResult() error = %v", err)
	}

	rec4 := latestTaskStateRecord(t, mem, task4.TaskID)
	if rec4.State != types.StateFailed {
		t.Fatalf("step 6: persisted state = %q, want FAILED", rec4.State)
	}
	t.Logf("step 6: task_state updated in Memory — state=%s error_code=%s",
		rec4.State, result4.ErrorCode)
	t.Log("step 6 complete: failure path handled correctly ✓")

	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 7: metrics — reading final counters")

	received, completed, failed, violations, queueDepth := d.GetMetrics()
	t.Logf("step 7: tasks_received=%d tasks_completed=%d tasks_failed=%d policy_violations=%d queue_depth=%d",
		received, completed, failed, violations, queueDepth)

	if received != 5 {
		t.Fatalf("step 7: tasksReceived = %d, want 5", received)
	}
	if completed != 1 {
		t.Fatalf("step 7: tasksCompleted = %d, want 1 (task1)", completed)
	}
	if failed != 1 {
		t.Fatalf("step 7: tasksFailed = %d, want 1 (task4)", failed)
	}
	if violations != 1 {
		t.Fatalf("step 7: policyViolations = %d, want 1 (task2)", violations)
	}
	if queueDepth != 0 {
		t.Fatalf("step 7: queueDepth = %d, want 0 (all dispatched tasks have completed)", queueDepth)
	}
	t.Log("step 7 complete: all counters match expected values ✓")

	t.Log("─────────────────────────────────────────────────────")
	t.Log("demo summary: Task Dispatcher completed all 7 steps against mock dependencies")
	t.Log("demo summary: no real NATS broker, Vault, or database was used")
	t.Logf("demo summary: memory records written=%d | policy calls=%d | revocations=%d",
		mem.WriteCallCount, pol.ValidatedCalls, len(pol.RevokedRefs))
}

// ── helpers ──────────────────────────────────────────────────────────────────

func safeErrorCode(gw *gatewayMock) string {
	if len(gw.ErrorCalls) == 0 {
		return "<no error published>"
	}
	return gw.ErrorCalls[0].ErrorCode
}