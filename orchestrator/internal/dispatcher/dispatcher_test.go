// Package dispatcher_test provides black-box tests for M2: Task Dispatcher.
//
// Each test demonstrates a distinct scenario from the EDD (§7, §8, §17.3).
// Run with:
//
//	cd orchestrator && go test ./internal/dispatcher/ -v
//
// Demo scenarios covered:
//
//	✅  Happy Path          — task flows from receipt → policy → capability → DISPATCHED → task_accepted
//	✅  Duplicate Task      — same task_id twice → DUPLICATE_TASK; no second agent touched
//	✅  Policy Violation    — Vault denies → POLICY_VIOLATION; capability query NEVER called
//	✅  Schema Errors       — empty skill domains, invalid priority, timeout out of range
//	✅  Memory Fail         — memory write fails before dispatch → error; task_spec NOT published (no orphan)
//	✅  Agents Unavailable  — capability query fails → AGENTS_UNAVAILABLE error
//	✅  Task Result         — HandleTaskResult writes COMPLETED state + delivers result + revokes credentials
//	✅  Task Failed         — HandleTaskResult with success=false → FAILED state + tasksFailed counter
//	✅  Metrics             — counters track received, completed, failed, violations, queue depth
//	✅  Active Tasks        — GetActiveTasks reflects in-flight tasks and clears on completion
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
// These interfaces are defined inside the dispatcher package to avoid import
// cycles — so we implement them here rather than in the shared mocks/ package.

// gatewayMock records every outbound call the Dispatcher makes to M1.
type gatewayMock struct {
	// Spy fields — inspect these in tests.
	AcceptedCalls        []types.TaskAccepted
	ErrorCalls           []types.ErrorResponse
	TaskSpecCalls        []types.TaskSpec
	CapabilityQueryCalls []types.CapabilityQuery
	TaskResultCalls      []types.TaskResult

	// Control flags.
	CapabilityQueryResponse *types.CapabilityResponse
	CapabilityQueryError    error
	PublishSpecError         error
}

func (g *gatewayMock) PublishTaskAccepted(_ string, a types.TaskAccepted) error {
	g.AcceptedCalls = append(g.AcceptedCalls, a)
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
func (g *gatewayMock) PublishCapabilityQuery(q types.CapabilityQuery) (*types.CapabilityResponse, error) {
	g.CapabilityQueryCalls = append(g.CapabilityQueryCalls, q)
	if g.CapabilityQueryError != nil {
		return nil, g.CapabilityQueryError
	}
	return g.CapabilityQueryResponse, nil
}
func (g *gatewayMock) PublishTaskResult(_ string, r types.TaskResult) error {
	g.TaskResultCalls = append(g.TaskResultCalls, r)
	return nil
}

// newCapabilityResponse returns a default CapabilityResponse with a matched agent.
func newCapabilityResponse(orchRef string) *types.CapabilityResponse {
	return &types.CapabilityResponse{
		OrchestratorTaskRef: orchRef,
		Match:               types.CapabilityMatch_Match,
		AgentID:             "agent-42",
	}
}

// policyMock is a minimal controllable PolicyEnforcer.
type policyMock struct {
	ShouldDeny      bool
	DenyReason      string
	RevokedRefs     []string
	ValidatedCalls  int
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
	}, nil
}

func (p *policyMock) RevokeCredentials(orchRef string) error {
	p.RevokedRefs = append(p.RevokedRefs, orchRef)
	return nil
}

// monitorMock records task tracking calls.
type monitorMock struct {
	TrackedTasks   []*types.TaskState
	UntrackedIDs   []string
}

func (m *monitorMock) TrackTask(ts *types.TaskState)  { m.TrackedTasks = append(m.TrackedTasks, ts) }
func (m *monitorMock) UntrackTask(taskID string)       { m.UntrackedIDs = append(m.UntrackedIDs, taskID) }

// ── Test Helpers ─────────────────────────────────────────────────────────────

// newDispatcher builds a fully wired Dispatcher with fresh mocks.
// capabilityResponse controls what the gateway returns for capability queries;
// pass nil to use the default matched agent.
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

// ── §8.1 Happy Path ───────────────────────────────────────────────────────────

func TestHandleInboundTask_HappyPath_NewAgent(t *testing.T) {
	// DEMO: A valid task flows through the entire pipeline and is dispatched.
	// Shows: schema check ✓ → dedup check ✓ → policy ALLOW ✓ →
	//         capability query ✓ → memory write ✓ → task_spec published ✓ →
	//         task_accepted published ✓
	d, gw, pol, mon, mem := newDispatcher(t)

	// Gateway returns a new-agent capability response (no_match → provisioning).
	gw.CapabilityQueryResponse = &types.CapabilityResponse{
		Match:   types.CapabilityMatch_NoMatch,
		AgentID: "agent-new",
	}

	task := validTask("550e8400-e29b-41d4-a716-446655440000")
	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("HandleInboundTask() error = %v, want nil", err)
	}

	// Policy was called exactly once.
	if pol.ValidatedCalls != 1 {
		t.Fatalf("policy.ValidatedCalls = %d, want 1", pol.ValidatedCalls)
	}
	// One capability query was issued.
	if len(gw.CapabilityQueryCalls) != 1 {
		t.Fatalf("capability queries = %d, want 1", len(gw.CapabilityQueryCalls))
	}
	// task_spec was dispatched to Agents Component.
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("task_spec publishes = %d, want 1", len(gw.TaskSpecCalls))
	}
	if gw.TaskSpecCalls[0].TaskID != task.TaskID {
		t.Fatalf("task_spec.task_id = %q, want %q", gw.TaskSpecCalls[0].TaskID, task.TaskID)
	}
	// task_accepted was confirmed to User I/O.
	if len(gw.AcceptedCalls) != 1 {
		t.Fatalf("task_accepted publishes = %d, want 1", len(gw.AcceptedCalls))
	}
	// State was persisted to Memory.
	rec, err := mem.GetTaskState(task.TaskID)
	if err != nil {
		t.Fatalf("GetTaskState() error = %v", err)
	}
	if rec.State != types.StateDispatched {
		t.Fatalf("persisted state = %q, want DISPATCHED", rec.State)
	}
	// Monitor was notified.
	if len(mon.TrackedTasks) != 1 {
		t.Fatalf("monitor.TrackedTasks = %d, want 1", len(mon.TrackedTasks))
	}
	// orchestrator_task_ref is distinct from task_id.
	orchRef := orchRefFromSpec(t, gw)
	if orchRef == task.TaskID {
		t.Fatal("orchestrator_task_ref must be distinct from task_id")
	}
	// No errors sent to User I/O.
	if len(gw.ErrorCalls) != 0 {
		t.Fatalf("unexpected errors published: %+v", gw.ErrorCalls)
	}
}

func TestHandleInboundTask_HappyPath_ExistingAgent(t *testing.T) {
	// DEMO: §8.2 — A capable idle agent already exists; no provisioning needed.
	// capability response returns Match=match with agent_id set.
	d, gw, _, _, _ := newDispatcher(t)
	gw.CapabilityQueryResponse = &types.CapabilityResponse{
		Match:   types.CapabilityMatch_Match,
		AgentID: "agent-idle-42",
	}

	task := validTask("550e8400-e29b-41d4-a716-446655440001")
	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("HandleInboundTask() error = %v, want nil", err)
	}

	// The agent_id must flow through to task_spec and task_accepted.
	if gw.TaskSpecCalls[0].OrchestratorTaskRef == "" {
		t.Fatal("task_spec missing orchestrator_task_ref")
	}
	if gw.AcceptedCalls[0].AgentID != "agent-idle-42" {
		t.Fatalf("task_accepted.agent_id = %q, want agent-idle-42", gw.AcceptedCalls[0].AgentID)
	}
	// policy_scope must be propagated into the task_spec.
	if gw.TaskSpecCalls[0].PolicyScope.TokenRef == "" {
		t.Fatal("task_spec.policy_scope.token_ref is empty — policy scope not attached")
	}
}

// ── §8.1 Deduplication (§FR-TRK-03) ─────────────────────────────────────────

func TestHandleInboundTask_DuplicateTaskID_ReturnsCurrentStatus(t *testing.T) {
	// DEMO: The same task_id submitted twice within the idempotency window
	// returns DUPLICATE_TASK on the second attempt — no second agent is spawned.
	d, gw, _, _, _ := newDispatcher(t)
	gw.CapabilityQueryResponse = newCapabilityResponse("")

	task := validTask("550e8400-e29b-41d4-a716-446655440002")

	// First submission — should succeed.
	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("first HandleInboundTask() error = %v, want nil", err)
	}

	// Second submission with identical task_id.
	if err := d.HandleInboundTask(task); err != nil {
		t.Fatalf("second HandleInboundTask() error = %v, want nil (idempotent)", err)
	}

	// Only ONE capability query and ONE task_spec — second was rejected at dedup.
	if len(gw.CapabilityQueryCalls) != 1 {
		t.Fatalf("capability queries = %d, want 1 (second must be deduped)", len(gw.CapabilityQueryCalls))
	}
	if len(gw.TaskSpecCalls) != 1 {
		t.Fatalf("task_spec publishes = %d, want 1", len(gw.TaskSpecCalls))
	}

	// A DUPLICATE_TASK error was sent to User I/O for the second attempt.
	if len(gw.ErrorCalls) != 1 {
		t.Fatalf("error calls = %d, want 1 (DUPLICATE_TASK)", len(gw.ErrorCalls))
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeDuplicateTask {
		t.Fatalf("error_code = %q, want DUPLICATE_TASK", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── §8.3 Policy Violation ─────────────────────────────────────────────────────

func TestHandleInboundTask_PolicyViolation_NoAgentTouched(t *testing.T) {
	// DEMO: When Vault denies the task, the capability query is NEVER called
	// and no task_spec is published — no agent is ever spawned (§7, §8.3).
	d, gw, pol, _, _ := newDispatcher(t)
	pol.ShouldDeny = true
	pol.DenyReason = "required domain 'financial' not in user policy"

	task := validTask("550e8400-e29b-41d4-a716-446655440003")
	err := d.HandleInboundTask(task)
	if err == nil {
		t.Fatal("HandleInboundTask() error = nil, want policy denied error")
	}

	// Capability query was NEVER issued.
	if len(gw.CapabilityQueryCalls) != 0 {
		t.Fatalf("capability queries = %d, want 0 (no agent should be touched on policy deny)", len(gw.CapabilityQueryCalls))
	}
	// task_spec was NEVER published.
	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("task_spec was published — agent must not be touched on POLICY_VIOLATION")
	}
	// POLICY_VIOLATION error was sent to User I/O.
	if len(gw.ErrorCalls) != 1 {
		t.Fatalf("error calls = %d, want 1 (POLICY_VIOLATION)", len(gw.ErrorCalls))
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodePolicyViolation {
		t.Fatalf("error_code = %q, want POLICY_VIOLATION", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── Schema Validation (§FR-TRK-04, §5.1) ─────────────────────────────────────

func TestHandleInboundTask_EmptySkillDomains_InvalidTaskSpec(t *testing.T) {
	// DEMO: required_skill_domains must have at least one entry (§FR-TRK-04).
	d, gw, pol, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440004")
	task.RequiredSkillDomains = []string{} // violates FR-TRK-04

	err := d.HandleInboundTask(task)
	if err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC")
	}

	// Policy and capability query must not be called for schema failures.
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
	task.TaskID = "" // required field missing

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
	// Priority must be 1–10. Zero is out of range.
	d, gw, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440006")
	task.Priority = 0 // invalid

	if err := d.HandleInboundTask(task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC for priority=0")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", gw.ErrorCalls[0].ErrorCode)
	}
}

func TestHandleInboundTask_TimeoutTooShort_InvalidTaskSpec(t *testing.T) {
	// timeout_seconds must be at least 30.
	d, gw, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440007")
	task.TimeoutSeconds = 10 // below minimum (30)

	if err := d.HandleInboundTask(task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC for timeout=10")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", gw.ErrorCalls[0].ErrorCode)
	}
}

func TestHandleInboundTask_PayloadTooLarge_InvalidTaskSpec(t *testing.T) {
	// payload must not exceed 1MB.
	d, gw, _, _, _ := newDispatcher(t)

	task := validTask("550e8400-e29b-41d4-a716-446655440008")
	bigPayload := make([]byte, 1<<20+1) // 1MB + 1 byte
	for i := range bigPayload {
		bigPayload[i] = 'x'
	}
	// Wrap it so it's valid JSON (a string).
	task.Payload = json.RawMessage(fmt.Sprintf("%q", string(bigPayload)))

	if err := d.HandleInboundTask(task); err == nil {
		t.Fatal("HandleInboundTask() error = nil, want INVALID_TASK_SPEC for oversized payload")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeInvalidTaskSpec {
		t.Fatalf("error_code = %q, want INVALID_TASK_SPEC", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── §14.1 Memory-Before-Dispatch Safety ──────────────────────────────────────

func TestHandleInboundTask_MemoryFailBeforeDispatch_NoOrphanedAgent(t *testing.T) {
	// DEMO: If Memory write fails before dispatch, the task_spec must NOT be
	// published — otherwise the agent would be orphaned with no tracked state (§14.1).
	d, gw, _, _, mem := newDispatcher(t)
	gw.CapabilityQueryResponse = newCapabilityResponse("")
	mem.ShouldFailWrites = true // Memory is down

	task := validTask("550e8400-e29b-41d4-a716-446655440009")
	err := d.HandleInboundTask(task)
	if err == nil {
		t.Fatal("HandleInboundTask() error = nil, want memory error")
	}

	// task_spec MUST NOT have been published — agent would be orphaned.
	if len(gw.TaskSpecCalls) != 0 {
		t.Fatal("task_spec was published despite memory write failure — this would create an orphaned agent")
	}
	// An error was sent to User I/O.
	if len(gw.ErrorCalls) == 0 {
		t.Fatal("no error sent to User I/O — caller must be notified of failure")
	}
	if gw.ErrorCalls[0].ErrorCode != types.ErrCodeStorageUnavailable {
		t.Fatalf("error_code = %q, want STORAGE_UNAVAILABLE", gw.ErrorCalls[0].ErrorCode)
	}
}

// ── Agents Unavailable ────────────────────────────────────────────────────────

func TestHandleInboundTask_AgentsUnavailable_ReturnsError(t *testing.T) {
	// DEMO: If the capability query fails (Agents Component down / timeout),
	// the dispatcher returns AGENTS_UNAVAILABLE and does not dispatch.
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

// ── Task Result Processing ────────────────────────────────────────────────────

func TestHandleTaskResult_WritesCompletedState_DeliversResult(t *testing.T) {
	// DEMO: When the Agents Component returns a successful task_result:
	//   1. COMPLETED state is written to Memory
	//   2. result is delivered to the callback_topic
	//   3. credential revocation is triggered
	//   4. task is removed from active tracking
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

	// COMPLETED state persisted to Memory.
	rec, err := mem.GetTaskState(task.TaskID)
	if err != nil {
		t.Fatalf("GetTaskState() error = %v", err)
	}
	if rec.State != types.StateCompleted {
		t.Fatalf("persisted state = %q, want COMPLETED", rec.State)
	}

	// Result was delivered to callback_topic.
	if len(gw.TaskResultCalls) != 1 {
		t.Fatalf("task_result publishes = %d, want 1", len(gw.TaskResultCalls))
	}

	// Credential revocation was triggered.
	if len(pol.RevokedRefs) != 1 {
		t.Fatalf("revoked refs = %d, want 1", len(pol.RevokedRefs))
	}
	if pol.RevokedRefs[0] != orchRef {
		t.Fatalf("revoked ref = %q, want %q", pol.RevokedRefs[0], orchRef)
	}

	// Task removed from active tracking.
	if len(d.GetActiveTasks()) != 0 {
		t.Fatal("task still in GetActiveTasks() after completion — expected empty")
	}
	if len(mon.UntrackedIDs) != 1 {
		t.Fatalf("monitor.UntrackTask called %d times, want 1", len(mon.UntrackedIDs))
	}
}

func TestHandleTaskResult_FailedTask_WritesFailedState(t *testing.T) {
	// DEMO: When success=false, state transitions to FAILED.
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

	rec, err := mem.GetTaskState(task.TaskID)
	if err != nil {
		t.Fatalf("GetTaskState() error = %v", err)
	}
	if rec.State != types.StateFailed {
		t.Fatalf("persisted state = %q, want FAILED", rec.State)
	}
}

// ── Metrics (§FR-TRK-06, §15.2) ──────────────────────────────────────────────

func TestGetMetrics_CountersTrackPipeline(t *testing.T) {
	// DEMO: Metrics counters accumulate correctly across the full pipeline.
	// Shows: received, policy_violations, queue_depth, completed increment as expected.
	d, gw, pol, _, _ := newDispatcher(t)
	gw.CapabilityQueryResponse = newCapabilityResponse("")

	// Submit task 1 — happy path.
	task1 := validTask("550e8400-e29b-41d4-a716-44665544000d")
	_ = d.HandleInboundTask(task1)

	// Submit task 2 — policy deny.
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

	// Complete task 1.
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
	_ = received // already checked
}

// ── Active Tasks Snapshot ─────────────────────────────────────────────────────

func TestGetActiveTasks_ReflectsInFlightTasks(t *testing.T) {
	// DEMO: GetActiveTasks shows what is currently in flight.
	// Active count grows on dispatch and shrinks on completion.
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

	// Complete task1.
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

// ── Dispatcher Demo Flow ──────────────────────────────────────────────────────

// TestDispatcherDemoFlow is a demo-style walkthrough of M2: Task Dispatcher.
//
// Run with:
//
//	cd orchestrator && go test ./internal/dispatcher/ -v -run TestDispatcherDemoFlow
//
// This test exercises the full lifecycle in the same order it would execute
// during a live demo, narrating each step via t.Log so the output reads as
// a human-friendly trace of what the dispatcher did and why.
//
// Scenarios covered (in order):
//
//	Step 1 — Happy Path:        valid task → DISPATCHED → task_accepted to User I/O
//	Step 2 — Duplicate Task:    same task_id resubmitted → DUPLICATE_TASK; no second agent
//	Step 3 — Policy Violation:  Vault denies → POLICY_VIOLATION; capability query never called
//	Step 4 — Schema Rejection:  empty skill domains → INVALID_TASK_SPEC; rejected before policy
//	Step 5 — Task Result:       agent returns success → COMPLETED + credential revocation
//	Step 6 — Failed Task:       agent returns failure → FAILED state persisted
//	Step 7 — Metrics Summary:   all counters reflect the above activity
//
// What this test does NOT do:
//   - Does not call a real NATS broker, Vault, or database
//   - Does not test concurrent load or timing guarantees
//   - Does not validate credential token contents
//
// Instead, it demonstrates that the orchestrator's M2 layer correctly
// orchestrates its dependencies through well-defined Go interfaces — which
// is exactly what we want for the current mock-based demo phase.
func TestDispatcherDemoFlow(t *testing.T) {
	// ── Setup ──────────────────────────────────────────────────────────────
	gw := &gatewayMock{}
	pol := &policyMock{}
	mon := &monitorMock{}
	mem := mocks.NewMemoryMock()
	cfg := &config.OrchestratorConfig{NodeID: "demo-node"}
	d := dispatcher.New(cfg, mem, nil, gw, pol, mon)

	// Default capability response: a matching idle agent is available.
	gw.CapabilityQueryResponse = &types.CapabilityResponse{
		Match:   types.CapabilityMatch_Match,
		AgentID: "agent-demo-42",
	}

	t.Log("demo setup: Task Dispatcher wired to mocks for Gateway, PolicyEnforcer, Monitor, and MemoryClient")
	t.Log("demo setup: all external components (NATS, Vault, DB) replaced by in-process test doubles")

	// ── Step 1: Happy Path ─────────────────────────────────────────────────
	// A well-formed task flows through the full pipeline:
	//   schema check → dedup (new) → policy ALLOW → capability query →
	//   memory write → task_spec dispatched → task_accepted confirmed
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

	// Verify the orchestrator_task_ref was generated and is distinct from task_id.
	orchRef1 := gw.TaskSpecCalls[0].OrchestratorTaskRef
	if orchRef1 == task1.TaskID {
		t.Fatal("step 1: orchestrator_task_ref must be distinct from task_id")
	}
	t.Logf("step 1: generated orchestrator_task_ref=%s (distinct from task_id=%s)", orchRef1, task1.TaskID)

	// Verify policy_scope was attached to the dispatched task_spec.
	if gw.TaskSpecCalls[0].PolicyScope.TokenRef == "" {
		t.Fatal("step 1: task_spec.policy_scope.token_ref is empty — policy scope not attached")
	}
	t.Logf("step 1: policy_scope attached — token_ref=%s domains=%v",
		gw.TaskSpecCalls[0].PolicyScope.TokenRef,
		gw.TaskSpecCalls[0].PolicyScope.Domains,
	)

	// Verify DISPATCHED state was persisted to Memory before dispatch.
	rec1, err := mem.GetTaskState(task1.TaskID)
	if err != nil {
		t.Fatalf("step 1: GetTaskState() error = %v", err)
	}
	if rec1.State != types.StateDispatched {
		t.Fatalf("step 1: persisted state = %q, want DISPATCHED", rec1.State)
	}
	t.Logf("step 1: task_state persisted to Memory — state=%s agent_id=%s", rec1.State, rec1.AgentID)

	// Verify task_accepted was sent to User I/O.
	if len(gw.AcceptedCalls) != 1 {
		t.Fatalf("step 1: task_accepted publishes = %d, want 1", len(gw.AcceptedCalls))
	}
	t.Logf("step 1: task_accepted sent to User I/O — agent_id=%s orch_ref=%s",
		gw.AcceptedCalls[0].AgentID, gw.AcceptedCalls[0].OrchestratorTaskRef)
	t.Log("step 1 complete: task dispatched successfully ✓")

	// ── Step 2: Duplicate Task ─────────────────────────────────────────────
	// Same task_id resubmitted within the idempotency window.
	// The dispatcher must detect the duplicate via the Memory read and
	// return DUPLICATE_TASK without spawning a second agent.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 2: duplicate task — submitting the same task_id a second time")

	if err := d.HandleInboundTask(task1); err != nil {
		t.Fatalf("step 2: HandleInboundTask() error = %v, want nil (idempotent no-op)", err)
	}

	// The capability query count must still be 1 — not 2.
	if len(gw.CapabilityQueryCalls) != 1 {
		t.Fatalf("step 2: capability queries = %d, want 1 (second must be rejected at dedup)", len(gw.CapabilityQueryCalls))
	}
	// A DUPLICATE_TASK error must have been returned to User I/O.
	if len(gw.ErrorCalls) != 1 || gw.ErrorCalls[0].ErrorCode != types.ErrCodeDuplicateTask {
		t.Fatalf("step 2: error_code = %q, want DUPLICATE_TASK", safeErrorCode(gw))
	}
	t.Logf("step 2: DUPLICATE_TASK returned for task_id=%s — current_state mentioned in message: %q",
		task1.TaskID, gw.ErrorCalls[0].UserMessage)
	t.Log("step 2 complete: duplicate rejected; no second agent spawned ✓")

	// Reset error spy for upcoming steps.
	gw.ErrorCalls = nil

	// ── Step 3: Policy Violation ───────────────────────────────────────────
	// Vault denies the task. The capability query must NEVER be called —
	// no agent interaction is permitted on a policy deny (§7, §8.3).
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

	// ── Step 4: Schema Rejection ───────────────────────────────────────────
	// A task with empty required_skill_domains violates §FR-TRK-04.
	// The dispatcher rejects it immediately — before reaching the policy enforcer.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 4: schema rejection — task submitted with empty required_skill_domains")

	task3 := types.UserTask{
		TaskID:               "550e8400-e29b-41d4-a716-446655440002",
		UserID:               "user-carol",
		RequiredSkillDomains: []string{}, // violates FR-TRK-04
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

	// ── Step 5: Task Result — Success ──────────────────────────────────────
	// The agent for task1 completes successfully.
	// The dispatcher must: write COMPLETED state → deliver result → revoke credentials.
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

	// COMPLETED state must be persisted.
	rec1Final, err := mem.GetTaskState(task1.TaskID)
	if err != nil {
		t.Fatalf("step 5: GetTaskState() error = %v", err)
	}
	if rec1Final.State != types.StateCompleted {
		t.Fatalf("step 5: persisted state = %q, want COMPLETED", rec1Final.State)
	}
	t.Logf("step 5: task_state updated in Memory — state=%s completed_at=%s",
		rec1Final.State, rec1Final.CompletedAt.Format(time.RFC3339))

	// Result must be delivered to callback_topic.
	if len(gw.TaskResultCalls) != 1 {
		t.Fatalf("step 5: task_result publishes = %d, want 1", len(gw.TaskResultCalls))
	}
	t.Logf("step 5: task_result delivered to callback_topic=%s", task1.CallbackTopic)

	// Credentials must be revoked.
	if len(pol.RevokedRefs) != 1 || pol.RevokedRefs[0] != orchRef1 {
		t.Fatalf("step 5: revoked refs = %v, want [%s]", pol.RevokedRefs, orchRef1)
	}
	t.Logf("step 5: credential revocation triggered for orchestrator_task_ref=%s", pol.RevokedRefs[0])

	// Task must be removed from active tracking.
	if len(d.GetActiveTasks()) != 0 {
		t.Fatalf("step 5: active tasks = %d, want 0 after completion", len(d.GetActiveTasks()))
	}
	t.Log("step 5 complete: result processed, credentials revoked, task untracked ✓")

	// ── Step 6: Task Result — Failure ──────────────────────────────────────
	// Submit a second task and let it fail. State should be FAILED.
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

	rec4, err := mem.GetTaskState(task4.TaskID)
	if err != nil {
		t.Fatalf("step 6: GetTaskState() error = %v", err)
	}
	if rec4.State != types.StateFailed {
		t.Fatalf("step 6: persisted state = %q, want FAILED", rec4.State)
	}
	t.Logf("step 6: task_state updated in Memory — state=%s error_code=%s",
		rec4.State, result4.ErrorCode)
	t.Log("step 6 complete: failure path handled correctly ✓")

	// ── Step 7: Metrics Summary ────────────────────────────────────────────
	// Verify all counters accumulated correctly across the demo run.
	t.Log("─────────────────────────────────────────────────────")
	t.Log("step 7: metrics — reading final counters")

	received, completed, failed, violations, queueDepth := d.GetMetrics()
	t.Logf("step 7: tasks_received=%d tasks_completed=%d tasks_failed=%d policy_violations=%d queue_depth=%d",
		received, completed, failed, violations, queueDepth)

	// received = task1 + task1(dup) + task2(policy) + task3(schema) + task4 = 5
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

// ── helpers ───────────────────────────────────────────────────────────────────

func safeErrorCode(gw *gatewayMock) string {
	if len(gw.ErrorCalls) == 0 {
		return "<no error published>"
	}
	return gw.ErrorCalls[0].ErrorCode
}
