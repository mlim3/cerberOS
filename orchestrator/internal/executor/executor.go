// Package executor implements M7: Plan Executor.
//
// The Plan Executor manages execution of structured plans returned by the Planner Agent.
// It receives an execution plan from the Task Dispatcher, dispatches subtasks in
// topological (DAG) order, pipes outputs between dependent subtasks, and signals
// the Dispatcher on plan completion or failure.
//
// Responsibilities (§4.1 M7):
//   - Validate plan dependency graph (rejects cycles, empty plans, oversized plans)
//   - Dispatch subtasks in topological order via Communications Gateway
//   - Track each subtask's state independently (PENDING → DISPATCHED → COMPLETED/FAILED/BLOCKED)
//   - Pipe subtask output into dependent subtasks via prior_results[] injection (§FR-TD-06)
//   - Aggregate final results when all subtasks complete
//   - Signal Task Dispatcher on plan completion or failure
//   - Persist subtask state to Memory Interface on every transition
//   - Support up to PLAN_EXECUTOR_MAX_PARALLEL concurrent subtask dispatches
//
// Subtask States (§9.2):
//
//	PENDING → DISPATCH_PENDING → DISPATCHED → RUNNING → COMPLETED (terminal)
//	PENDING → BLOCKED (terminal — dependency failed)
//	DISPATCHED → FAILED (terminal — max retries exceeded)
//	DISPATCHED → TIMED_OUT (terminal — subtask timeout)
package executor

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// ── Internal Interface Definitions ───────────────────────────────────────────

// Gateway defines the outbound operations the Plan Executor needs from M1.
type Gateway interface {
	PublishTaskSpec(ctx context.Context, spec types.TaskSpec) error
	PublishCapabilityQuery(ctx context.Context, query types.CapabilityQuery) (*types.CapabilityResponse, error)
	PublishStatusUpdate(ctx context.Context, userContextID string, status types.StatusResponse) error
}

// PolicyEnforcer defines the credential revocation the Plan Executor needs from M3.
type PolicyEnforcer interface {
	RevokeCredentials(ctx context.Context, orchestratorTaskRef string) error
}

// ── Plan Completion Callbacks ──────────────────────────────────────────────────

// OnPlanCompleteFn is called by the Plan Executor when all subtasks complete successfully.
type OnPlanCompleteFn func(ts *types.TaskState, aggregatedResults []types.PriorResult)

// OnPlanFailedFn is called when the plan cannot complete.
// partial=true means some subtasks completed; partialResults carries their outputs.
type OnPlanFailedFn func(ts *types.TaskState, errorCode string, partial bool, partialResults []types.PriorResult)

// ── planExecution tracks the live state of one running plan ──────────────────

type planExecution struct {
	plan types.ExecutionPlan
	ts   *types.TaskState
	mu   sync.Mutex

	// subtasks maps subtask_id → *types.SubtaskState
	subtasks map[string]*types.SubtaskState

	// orchRefIndex maps orchestrator_task_ref → subtask_id
	// Used to route task_result back to the right subtask.
	orchRefIndex map[string]string

	// dispatchedCount tracks how many subtasks are currently in DISPATCHED/RUNNING.
	dispatchedCount int32
}

// ── PlanExecutor ─────────────────────────────────────────────────────────────

// PlanExecutor is M7: Plan Executor.
type PlanExecutor struct {
	cfg    *config.OrchestratorConfig
	memory interfaces.MemoryClient
	gw     Gateway
	policy PolicyEnforcer

	// activePlans maps plan_id → *planExecution
	activePlans sync.Map

	// orchRefToPlan maps orchestrator_task_ref (subtask) → plan_id
	// Used in HandleSubtaskResult to find the plan a result belongs to.
	orchRefToPlan sync.Map

	onPlanComplete OnPlanCompleteFn
	onPlanFailed   OnPlanFailedFn
}

// New creates a new PlanExecutor.
// onComplete and onFailed are called by the executor when a plan reaches a terminal state.
func New(
	cfg *config.OrchestratorConfig,
	memory interfaces.MemoryClient,
	gw Gateway,
	policy PolicyEnforcer,
	onComplete OnPlanCompleteFn,
	onFailed OnPlanFailedFn,
) *PlanExecutor {
	return &PlanExecutor{
		cfg:            cfg,
		memory:         memory,
		gw:             gw,
		policy:         policy,
		onPlanComplete: onComplete,
		onPlanFailed:   onFailed,
	}
}

// ── Execute — entry point from Task Dispatcher ────────────────────────────────

// Execute initializes all subtasks as PENDING and dispatches those with no dependencies.
// Called by Task Dispatcher after plan validation succeeds (§17.3).
// ctx carries the trace_id, task_id, and plan_id from the dispatcher.
func (e *PlanExecutor) Execute(ctx context.Context, plan types.ExecutionPlan, ts *types.TaskState) error {
	ctx = observability.WithModule(ctx, "plan_executor")
	ctx = observability.WithPlanID(ctx, plan.PlanID)
	ctx, planSpan := observability.StartSpan(ctx, "plan_execution")
	observability.SpanSetTaskAttributes(planSpan, ts.TaskID, ts.UserID)
	defer planSpan.End()
	log := observability.LogFromContext(ctx)

	exec := &planExecution{
		plan:         plan,
		ts:           ts,
		subtasks:     make(map[string]*types.SubtaskState, len(plan.Subtasks)),
		orchRefIndex: make(map[string]string),
	}

	now := time.Now().UTC()

	// Initialize all subtask states as PENDING.
	for _, st := range plan.Subtasks {
		timeoutAt := now.Add(time.Duration(st.TimeoutSeconds) * time.Second)
		sub := &types.SubtaskState{
			SubtaskID:            st.SubtaskID,
			PlanID:               plan.PlanID,
			TaskID:               ts.TaskID,
			State:                types.SubtaskStatePending,
			RequiredSkillDomains: st.RequiredSkillDomains,
			DependsOn:            st.DependsOn,
			TimeoutAt:            &timeoutAt,
		}
		exec.subtasks[st.SubtaskID] = sub
		e.persistSubtaskState(sub, ts.OrchestratorTaskRef, now)
	}

	e.activePlans.Store(plan.PlanID, exec)

	log.Info("plan execution initialized", "plan_id", plan.PlanID, "subtask_count", len(plan.Subtasks))

	// Dispatch subtasks that have no dependencies.
	e.dispatchReadySubtasks(ctx, exec)

	return nil
}

// HandleSubtaskResult processes an inbound task_result for a subtask.
// Routes by OrchestratorTaskRef → planID → subtask, marks COMPLETED,
// pipes result to dependents, checks plan completion (§17.3).
// ctx is rebuilt from the envelope's trace_id in the inbound handler.
func (e *PlanExecutor) HandleSubtaskResult(ctx context.Context, result types.TaskResult) error {
	// Resolve which plan/subtask this result belongs to.
	planIDVal, ok := e.orchRefToPlan.Load(result.OrchestratorTaskRef)
	if !ok {
		observability.LogFromContext(ctx).Debug("task result for unknown orchRef — stale or already completed",
			"orchestrator_task_ref", result.OrchestratorTaskRef)
		return nil // stale result; ignore
	}
	planID := planIDVal.(string)

	execVal, ok := e.activePlans.Load(planID)
	if !ok {
		return nil // plan already completed
	}
	exec := execVal.(*planExecution)

	exec.mu.Lock()
	defer exec.mu.Unlock()

	// Find the subtask by orchRef.
	subtaskID, ok := exec.orchRefIndex[result.OrchestratorTaskRef]
	if !ok {
		return nil
	}
	sub := exec.subtasks[subtaskID]

	// Enrich context with subtask/plan IDs for logging.
	subCtx := observability.WithPlanID(ctx, planID)
	subCtx = observability.WithSubtaskID(subCtx, subtaskID)
	subCtx = observability.WithModule(subCtx, "plan_executor")
	log := observability.LogFromContext(subCtx)

	// Revoke credentials for this subtask's agent.
	if err := e.policy.RevokeCredentials(subCtx, result.OrchestratorTaskRef); err != nil {
		log.Error("subtask credential revocation failed", "error", err)
	}

	now := time.Now().UTC()

	if result.Success {
		sub.State = types.SubtaskStateCompleted
		sub.Result = result.Result
		sub.AgentID = result.AgentID
		sub.CompletedAt = &now
		log.Info("subtask completed")
	} else {
		sub.State = types.SubtaskStateFailed
		sub.ErrorCode = result.ErrorCode
		sub.CompletedAt = &now
		log.Warn("subtask failed", "error_code", result.ErrorCode)
	}
	e.persistSubtaskState(sub, exec.ts.OrchestratorTaskRef, now)
	atomic.AddInt32(&exec.dispatchedCount, -1)

	e.orchRefToPlan.Delete(result.OrchestratorTaskRef)

	if result.Success {
		// Dispatch any subtasks that are now unblocked.
		e.dispatchReadySubtasks(ctx, exec)
	} else {
		// Block all subtasks that depend on the failed subtask.
		e.blockDependents(exec, subtaskID, now)
	}

	// Check if the plan has reached a terminal state.
	e.checkPlanCompletion(exec)

	return nil
}

// ── Dispatch Helpers ──────────────────────────────────────────────────────────

// dispatchReadySubtasks finds all PENDING subtasks whose dependencies are all COMPLETED
// and dispatches them (subject to PLAN_EXECUTOR_MAX_PARALLEL limit).
func (e *PlanExecutor) dispatchReadySubtasks(ctx context.Context, exec *planExecution) {
	for _, st := range exec.plan.Subtasks {
		sub := exec.subtasks[st.SubtaskID]
		if sub.State != types.SubtaskStatePending {
			continue
		}

		// Check parallel dispatch limit.
		if int(atomic.LoadInt32(&exec.dispatchedCount)) >= e.cfg.PlanExecutorMaxParallel {
			break
		}

		// Check all dependencies are COMPLETED.
		if !e.allDependenciesMet(exec, sub.DependsOn) {
			continue
		}

		// Collect prior_results from completed dependencies.
		sub.PriorResults = e.collectPriorResults(exec, sub.DependsOn)

		e.dispatchSubtask(ctx, exec, st, sub)
	}
}

// dispatchSubtask sends a capability_query then publishes a task_spec for one subtask.
func (e *PlanExecutor) dispatchSubtask(ctx context.Context, exec *planExecution, st types.Subtask, sub *types.SubtaskState) {
	now := time.Now().UTC()

	// Build a child context for this subtask.
	subCtx := observability.WithPlanID(ctx, exec.plan.PlanID)
	subCtx = observability.WithSubtaskID(subCtx, sub.SubtaskID)
	subCtx = observability.WithModule(subCtx, "plan_executor")
	subCtx, subtaskSpan := observability.StartSpan(subCtx, "subtask_dispatch")
	defer subtaskSpan.End()
	log := observability.LogFromContext(subCtx)

	// Transition to DISPATCH_PENDING and persist before publishing.
	sub.State = types.SubtaskStateDispatchPending
	e.persistSubtaskState(sub, exec.ts.OrchestratorTaskRef, now)

	// Generate a unique orchestrator_task_ref for this subtask dispatch.
	orchRef := newUUID()
	sub.OrchestratorTaskRef = orchRef

	// Register reverse lookup before publishing (prevents lost results on race).
	exec.orchRefIndex[orchRef] = sub.SubtaskID
	e.orchRefToPlan.Store(orchRef, exec.plan.PlanID)

	log.Info("dispatching subtask", "depends_on", sub.DependsOn, "orchRef", orchRef)

	// Query capability before dispatching (§FR-ALC-01).
	capResp, err := e.gw.PublishCapabilityQuery(ctx, types.CapabilityQuery{
		OrchestratorTaskRef:  orchRef,
		RequiredSkillDomains: st.RequiredSkillDomains,
		TraceID:              exec.ts.TraceID,
	})
	if err != nil {
		log.Error("capability query failed", "error", err)
		sub.State = types.SubtaskStateFailed
		sub.ErrorCode = types.ErrCodeAgentsUnavailable
		sub.CompletedAt = &now
		e.persistSubtaskState(sub, exec.ts.OrchestratorTaskRef, now)
		e.orchRefToPlan.Delete(orchRef)
		delete(exec.orchRefIndex, orchRef)
		e.blockDependents(exec, sub.SubtaskID, now)
		e.checkPlanCompletion(exec)
		return
	}

	// Build and publish task.inbound with inherited policy_scope.
	priorResultsJSON, _ := json.Marshal(sub.PriorResults)
	instructions := buildSubtaskInstructions(st, sub.PriorResults)

	spec := types.TaskSpec{
		OrchestratorTaskRef:  orchRef,
		TaskID:               orchRef,
		UserID:               exec.ts.UserID,
		RequiredSkillDomains: st.RequiredSkillDomains,
		PolicyScope:          exec.ts.PolicyScope, // Inherited, never expanded (§13.2)
		TimeoutSeconds:       st.TimeoutSeconds,
		Payload:              priorResultsJSON,
		Instructions:         instructions,
		Metadata: map[string]string{
			"task_kind":      "subtask",
			"parent_task_id": exec.ts.TaskID,
			"plan_id":        exec.plan.PlanID,
			"subtask_id":     sub.SubtaskID,
			"action":         st.Action,
		},
		CallbackTopic: exec.ts.CallbackTopic,
		UserContextID: exec.ts.UserContextID,
		TraceID:       exec.ts.TraceID,
	}

	if err := e.gw.PublishTaskSpec(ctx, spec); err != nil {
		log.Error("task_spec publish failed", "error", err)
		sub.State = types.SubtaskStateDeliveryFailed
		sub.ErrorCode = types.ErrCodeAgentsUnavailable
		sub.CompletedAt = &now
		e.persistSubtaskState(sub, exec.ts.OrchestratorTaskRef, now)
		_ = e.policy.RevokeCredentials(ctx, orchRef)
		e.orchRefToPlan.Delete(orchRef)
		delete(exec.orchRefIndex, orchRef)
		e.blockDependents(exec, sub.SubtaskID, now)
		e.checkPlanCompletion(exec)
		return
	}

	// Transition to DISPATCHED after successful publish.
	dispatchedAt := time.Now().UTC()
	sub.State = types.SubtaskStateDispatched
	sub.AgentID = capResp.AgentID
	sub.DispatchedAt = &dispatchedAt
	e.persistSubtaskState(sub, exec.ts.OrchestratorTaskRef, dispatchedAt)
	atomic.AddInt32(&exec.dispatchedCount, 1)

	log.Info("subtask dispatched", "orchRef", orchRef, "agent_id", capResp.AgentID)
}

// blockDependents marks all subtasks that (transitively) depend on a failed subtask as BLOCKED.
func (e *PlanExecutor) blockDependents(exec *planExecution, failedID string, now time.Time) {
	for _, st := range exec.plan.Subtasks {
		sub := exec.subtasks[st.SubtaskID]
		if sub.State != types.SubtaskStatePending {
			continue
		}
		for _, dep := range sub.DependsOn {
			if dep == failedID {
				sub.State = types.SubtaskStateBlocked
				sub.ErrorCode = fmt.Sprintf("dependency %q failed", failedID)
				sub.CompletedAt = &now
				e.persistSubtaskState(sub, exec.ts.OrchestratorTaskRef, now)
				// Recursively block subtasks that depend on this one.
				e.blockDependents(exec, sub.SubtaskID, now)
				break
			}
		}
	}
}

// checkPlanCompletion checks if all subtasks have reached terminal states and signals
// the Dispatcher via the registered callback.
func (e *PlanExecutor) checkPlanCompletion(exec *planExecution) {
	allTerminal := true
	anyFailed := false
	anyBlocked := false
	var completedResults []types.PriorResult

	for _, sub := range exec.subtasks {
		if !types.IsTerminalSubtaskState(sub.State) {
			allTerminal = false
			break
		}
		if sub.State == types.SubtaskStateCompleted {
			completedResults = append(completedResults, types.PriorResult{
				SubtaskID: sub.SubtaskID,
				Result:    sub.Result,
			})
		}
		if sub.State == types.SubtaskStateFailed || sub.State == types.SubtaskStateDeliveryFailed ||
			sub.State == types.SubtaskStateTimedOut {
			anyFailed = true
		}
		if sub.State == types.SubtaskStateBlocked {
			anyBlocked = true
		}
	}

	if !allTerminal {
		return
	}

	// Remove from active maps before calling callbacks (prevents double-fire).
	e.activePlans.Delete(exec.plan.PlanID)

	// Rebuild a context from the task state for logging.
	planCtx := context.Background()
	if exec.ts.TraceID != "" {
		planCtx = observability.WithTraceID(planCtx, exec.ts.TraceID)
	}
	planCtx = observability.WithTaskID(planCtx, exec.ts.TaskID)
	planCtx = observability.WithPlanID(planCtx, exec.plan.PlanID)
	planCtx = observability.WithModule(planCtx, "plan_executor")
	log := observability.LogFromContext(planCtx)

	if !anyFailed && !anyBlocked {
		// All subtasks completed successfully.
		log.Info("plan completed", "subtask_count", len(exec.subtasks))
		if e.onPlanComplete != nil {
			e.onPlanComplete(exec.ts, completedResults)
		}
		return
	}

	// Some subtasks failed or were blocked.
	partial := len(completedResults) > 0
	errorCode := types.ErrCodeMaxRetriesExceeded
	if anyBlocked && !anyFailed {
		errorCode = types.ErrCodeInvalidPlan // all failures are blocked deps
	}

	log.Warn("plan failed", "partial", partial, "error_code", errorCode)
	if e.onPlanFailed != nil {
		e.onPlanFailed(exec.ts, errorCode, partial, completedResults)
	}
}

// ── Dependency Helpers ────────────────────────────────────────────────────────

// allDependenciesMet returns true if every subtask_id in deps has state COMPLETED.
func (e *PlanExecutor) allDependenciesMet(exec *planExecution, deps []string) bool {
	for _, depID := range deps {
		dep, ok := exec.subtasks[depID]
		if !ok || dep.State != types.SubtaskStateCompleted {
			return false
		}
	}
	return true
}

// collectPriorResults gathers the results of all completed dependency subtasks.
func (e *PlanExecutor) collectPriorResults(exec *planExecution, deps []string) []types.PriorResult {
	var results []types.PriorResult
	for _, depID := range deps {
		if dep, ok := exec.subtasks[depID]; ok && dep.State == types.SubtaskStateCompleted {
			results = append(results, types.PriorResult{
				SubtaskID: depID,
				Result:    dep.Result,
			})
		}
	}
	return results
}

// ── Memory Persistence ────────────────────────────────────────────────────────

func (e *PlanExecutor) persistSubtaskState(sub *types.SubtaskState, orchRef string, timestamp time.Time) {
	log := observability.LogFromContext(context.Background())
	payload, err := json.Marshal(sub)
	if err != nil {
		log.Error("marshal subtask state failed", "subtask_id", sub.SubtaskID, "error", err)
		return
	}

	if err := e.memory.Write(types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: orchRef,
		TaskID:              sub.TaskID,
		PlanID:              sub.PlanID,
		SubtaskID:           sub.SubtaskID,
		DataType:            types.DataTypeSubtaskState,
		Timestamp:           timestamp,
		Payload:             payload,
	}); err != nil {
		log.Error("subtask state persist failed", "subtask_id", sub.SubtaskID, "error", err)
	}
}

// ── Utilities ─────────────────────────────────────────────────────────────────

// mustMarshal marshals v to JSON; returns null bytes on error.
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

func buildSubtaskInstructions(st types.Subtask, priorResults []types.PriorResult) string {
	priorResultsJSON, _ := json.Marshal(priorResults)
	paramsJSON, _ := json.Marshal(st.Params)

	return fmt.Sprintf(
		"Execute this subtask.\n"+
			"Action: %s\n"+
			"Instructions: %s\n"+
			"Parameters JSON: %s\n"+
			"Prior results JSON: %s\n"+
			"Return the task outcome directly and concisely.",
		st.Action,
		st.Instructions,
		string(paramsJSON),
		string(priorResultsJSON),
	)
}

// newUUID generates a random UUID v4 using crypto/rand.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
