// Package dispatcher implements M2: Task Dispatcher.
//
// The Task Dispatcher is the central coordinator for all incoming task routing
// decisions. It is the only module that drives the full task pipeline from
// receipt to decomposition and plan execution.
//
// Responsibilities (§4.1 M2):
//   - Validate user_task schema — reject invalid specs immediately
//   - Perform deduplication check via Memory Interface using task_id
//   - Route to Policy Enforcer for permission validation before any agent interaction
//   - After policy validation, dispatch a standard task.inbound request to a
//     general-purpose planner agent (§FR-TD-01)
//   - Parse the planner task result, validate plan, pass to Plan Executor
//   - Track active tasks and correlate plan results back to originating user_task
//   - Monitor queue depth and emit queue_pressure metric at high-water mark
//
// CRITICAL ordering (§14.1):
//
//	Persist task state to Memory BEFORE dispatching to Agents Component.
//	This prevents orphaned agents if Memory fails post-dispatch.
//
// v3.0 Pipeline (§7 Flows 1–3.5):
//
//  1. Schema validation — INVALID_TASK_SPEC on failure
//  2. Deduplication check — DUPLICATE_TASK if task_id seen within idempotency window
//  3. Generate orchestrator_task_ref (distinct UUID)
//  4. Policy validation — POLICY_VIOLATION on deny; Planner Agent never contacted on deny
//  5. Persist DECOMPOSING state to Memory (BEFORE planner dispatch — §14.1)
//  6. Publish a standard general-agent task via Gateway
//  7. Start decomposition timeout goroutine (DECOMPOSITION_TIMEOUT_SECONDS)
//  8. On planner task result: parse + validate plan → pass to Plan Executor → persist PLAN_ACTIVE
//  9. Send task_accepted to User I/O after plan is valid and execution starts
package dispatcher

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	ioclient "github.com/mlim3/cerberOS/orchestrator/internal/io"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// maxPayloadBytes is the maximum allowed serialized payload size (§5.1).
const maxPayloadBytes = 1 << 20 // 1 MB

// defaultIdempotencyWindow is the dedup window used when the task does not specify one.
const defaultIdempotencyWindow = 300 // seconds

// ── Internal Interface Definitions ───────────────────────────────────────────

// Gateway defines the outbound operations the Dispatcher needs from M1.
// Avoids a direct import cycle with the gateway package.
type Gateway interface {
	PublishTaskAccepted(callbackTopic string, accepted types.TaskAccepted) error
	PublishError(callbackTopic string, resp types.ErrorResponse) error
	PublishTaskResult(callbackTopic string, result types.TaskResult) error
	PublishTaskSpec(spec types.TaskSpec) error
	PublishStatusUpdate(userContextID string, status types.StatusResponse) error
}

// PolicyEnforcer defines the policy operations the Dispatcher needs from M3.
type PolicyEnforcer interface {
	ValidateAndScope(taskID string, orchestratorTaskRef string, userID string, requiredSkillDomains []string, timeoutSeconds int) (types.PolicyScope, error)
	RevokeCredentials(orchestratorTaskRef string) error
}

// TaskMonitor defines the monitor operations the Dispatcher needs from M4.
type TaskMonitor interface {
	TrackTask(ts *types.TaskState)
	UntrackTask(taskID string)
}

// PlanExecutor defines the plan execution interface the Dispatcher delegates to (M7).
type PlanExecutor interface {
	Execute(plan types.ExecutionPlan, ts *types.TaskState) error
	HandleSubtaskResult(result types.TaskResult) error
}

// ── Dispatcher ────────────────────────────────────────────────────────────────

// Dispatcher is M2: Task Dispatcher.
type Dispatcher struct {
	cfg    *config.OrchestratorConfig
	memory interfaces.MemoryClient
	vault  interfaces.VaultClient

	gateway  Gateway
	policy   PolicyEnforcer
	monitor  TaskMonitor
	executor PlanExecutor
	io       *ioclient.Client
	logger   *log.Logger

	// activeTasks maps task_id → *TaskState for all non-terminal in-flight tasks.
	activeTasks sync.Map

	// pendingDecompositions tracks top-level tasks currently waiting for a
	// planner agent result. key = top-level orchestrator_task_ref.
	pendingDecompositions sync.Map

	// Metrics — use atomic operations to avoid a global lock.
	tasksReceived       int64
	tasksCompleted      int64
	tasksFailed         int64
	policyViolations    int64
	decompositionFailed int64
	queueDepth          int64
}

// New creates a new Dispatcher.
func New(
	cfg *config.OrchestratorConfig,
	memory interfaces.MemoryClient,
	vault interfaces.VaultClient,
	gw Gateway,
	policy PolicyEnforcer,
	monitor TaskMonitor,
	executor PlanExecutor,
	io *ioclient.Client,
) *Dispatcher {
	return &Dispatcher{
		cfg:      cfg,
		memory:   memory,
		vault:    vault,
		gateway:  gw,
		policy:   policy,
		monitor:  monitor,
		executor: executor,
		io:       io,
		logger:   log.New(os.Stdout, "[dispatcher] ", log.LstdFlags|log.LUTC),
	}
}

// ── Inbound Task Pipeline ─────────────────────────────────────────────────────

// HandleInboundTask is the entry point for all user_task messages from the Gateway.
// Implements the full validation → dedup → policy → decomposition pipeline (§7, §17.3).
//
// Pipeline (§7):
//  1. Schema validation — INVALID_TASK_SPEC on failure
//  2. Deduplication check — DUPLICATE_TASK if task_id seen within idempotency window
//  3. Generate orchestrator_task_ref (distinct UUID)
//  4. Policy validation — POLICY_VIOLATION on deny; Planner Agent never contacted
//  5. Persist DECOMPOSING state to Memory BEFORE sending planner dispatch (§14.1)
//  6. Publish a standard task.inbound request to a general planner agent
//  7. Start decomposition timeout goroutine
func (d *Dispatcher) HandleInboundTask(task types.UserTask) error {
	atomic.AddInt64(&d.tasksReceived, 1)

	// ── Step 1: Schema validation ──────────────────────────────────────────
	if err := validateSchema(task); err != nil {
		d.logger.Printf("schema validation failed: task_id=%s error=%v", task.TaskID, err)
		_ = d.gateway.PublishError(task.CallbackTopic, types.ErrorResponse{
			TaskID:      task.TaskID,
			ErrorCode:   types.ErrCodeInvalidTaskSpec,
			UserMessage: err.Error(),
		})
		return err
	}

	// ── Step 2: Deduplication check ────────────────────────────────────────
	idempotencyWindow := task.IdempotencyWindow
	if idempotencyWindow == 0 {
		idempotencyWindow = defaultIdempotencyWindow
	}

	if existing := d.dedupCheck(task.TaskID, idempotencyWindow); existing != nil {
		d.logger.Printf("duplicate task rejected: task_id=%s current_state=%s", task.TaskID, existing.State)
		_ = d.gateway.PublishError(task.CallbackTopic, types.ErrorResponse{
			TaskID:      task.TaskID,
			ErrorCode:   types.ErrCodeDuplicateTask,
			UserMessage: fmt.Sprintf("task already submitted — current state: %s", existing.State),
		})
		return nil
	}

	// ── Step 3: Generate orchestrator_task_ref ─────────────────────────────
	orchRef := newUUID()

	// ── Step 4: Policy validation ──────────────────────────────────────────
	scope, err := d.policy.ValidateAndScope(
		task.TaskID, orchRef, task.UserID, task.RequiredSkillDomains, task.TimeoutSeconds,
	)
	if err != nil {
		atomic.AddInt64(&d.policyViolations, 1)
		d.logger.Printf("policy denied: task_id=%s orchestrator_task_ref=%s error=%v", task.TaskID, orchRef, err)
		_ = d.gateway.PublishError(task.CallbackTopic, types.ErrorResponse{
			TaskID:      task.TaskID,
			ErrorCode:   types.ErrCodePolicyViolation,
			UserMessage: "Task requires resources outside your configured permissions.",
		})
		return fmt.Errorf("policy validation denied: %w", err)
	}

	// ── Build initial task state as DECOMPOSING ────────────────────────────
	now := time.Now().UTC()
	timeoutAt := now.Add(time.Duration(task.TimeoutSeconds) * time.Second)

	ts := &types.TaskState{
		OrchestratorTaskRef:  orchRef,
		TaskID:               task.TaskID,
		UserID:               task.UserID,
		State:                types.StateDecomposing,
		RequiredSkillDomains: task.RequiredSkillDomains,
		PolicyScope:          scope,
		TimeoutAt:            &timeoutAt,
		CallbackTopic:        task.CallbackTopic,
		UserContextID:        task.UserContextID,
		IdempotencyWindow:    idempotencyWindow,
		Payload:              task.Payload,
		StateHistory: []types.StateEvent{
			{State: types.StateReceived, Timestamp: now, NodeID: d.cfg.NodeID},
			{State: types.StateDecomposing, Timestamp: now, NodeID: d.cfg.NodeID},
		},
	}

	// ── Step 5: Persist DECOMPOSING BEFORE sending planner dispatch ────────
	if err := d.persistTaskState(ts, now); err != nil {
		d.logger.Printf("memory write failed before decomposition: task_id=%s error=%v", task.TaskID, err)
		_ = d.gateway.PublishError(task.CallbackTopic, types.ErrorResponse{
			TaskID:      task.TaskID,
			ErrorCode:   types.ErrCodeStorageUnavailable,
			UserMessage: "Unable to persist task state. Task not dispatched.",
		})
		return fmt.Errorf("persist decomposing state: %w", err)
	}

	// Register with in-memory tracking and monitor.
	d.activeTasks.Store(task.TaskID, ts)
	d.monitor.TrackTask(ts)
	atomic.AddInt64(&d.queueDepth, 1)

	// ── Notify IO: task received, planning underway ────────────────────────
	expectedMins := 2
	_ = d.io.PushStatus(task.TaskID, ioclient.StatusWorking, "Planning your task...", &expectedMins)

	// ── Step 6: Publish planner task via standard task.inbound ─────────────
	rawInput := extractRawInput(task.Payload)
	spec := types.TaskSpec{
		OrchestratorTaskRef:  orchRef,
		TaskID:               orchRef,
		UserID:               task.UserID,
		RequiredSkillDomains: []string{"general"},
		PolicyScope:          scope,
		TimeoutSeconds:       minInt(task.TimeoutSeconds, d.cfg.DecompositionTimeoutSeconds),
		Payload:              task.Payload,
		Instructions:         buildDecompositionInstructions(task.TaskID, rawInput, scope),
		Metadata: map[string]string{
			"task_kind":      "decomposition",
			"parent_task_id": task.TaskID,
		},
		CallbackTopic:   task.CallbackTopic,
		UserContextID:   task.UserContextID,
		ProgressSummary: "Generating execution plan",
	}

	if err := d.gateway.PublishTaskSpec(spec); err != nil {
		d.logger.Printf("planner task publish failed: task_id=%s error=%v", task.TaskID, err)
		d.failTask(ts, types.ErrCodeAgentsUnavailable, "Could not reach Planner Agent. Please retry.")
		return fmt.Errorf("publish planner task: %w", err)
	}
	d.pendingDecompositions.Store(orchRef, task.TaskID)

	// ── Step 7: Start decomposition timeout goroutine ──────────────────────
	go d.watchDecompositionTimeout(ts)

	d.logger.Printf("task sent to planner agent: task_id=%s orchestrator_task_ref=%s", task.TaskID, orchRef)
	return nil
}

// HandleTaskResult routes terminal agent outcomes. Planner-task results are
// parsed into execution plans; all other outcomes are delegated to the executor
// as subtask results.
func (d *Dispatcher) HandleTaskResult(result types.TaskResult) error {
	if _, ok := d.pendingDecompositions.Load(result.OrchestratorTaskRef); ok {
		d.pendingDecompositions.Delete(result.OrchestratorTaskRef)
		if !result.Success {
			if ts := d.activeTaskByOrchRef(result.OrchestratorTaskRef); ts != nil {
				msg := "Planner agent failed to produce an execution plan."
				if result.ErrorCode != "" {
					msg = fmt.Sprintf("Planner agent failed: %s", result.ErrorCode)
				}
				d.failTask(ts, types.ErrCodeAgentsUnavailable, msg)
			}
			return nil
		}

		plan, err := parseExecutionPlan(result.Result)
		if err != nil {
			if ts := d.activeTaskByOrchRef(result.OrchestratorTaskRef); ts != nil {
				d.failTask(ts, types.ErrCodeInvalidPlan,
					fmt.Sprintf("Planner agent returned an invalid plan: %v", err))
			}
			return fmt.Errorf("parse planner result: %w", err)
		}
		return d.HandleDecompositionResponse(types.DecompositionResponse{
			TaskID: plan.ParentTaskID,
			Plan:   plan,
		})
	}

	return d.executor.HandleSubtaskResult(result)
}

// HandleDecompositionResponse processes a task_decomposition_response from the Planner Agent.
// Validates the plan, transitions task to PLAN_ACTIVE, passes plan to Plan Executor,
// and sends task_accepted to User I/O (§7 Flow 3.5, §8.1 steps 13–22).
func (d *Dispatcher) HandleDecompositionResponse(resp types.DecompositionResponse) error {
	tsVal, ok := d.activeTasks.Load(resp.TaskID)
	if !ok {
		// Task may have timed out already — ignore stale response.
		d.logger.Printf("decomposition response for unknown/timed-out task: task_id=%s", resp.TaskID)
		return nil
	}
	ts := tsVal.(*types.TaskState)

	// Guard: only act if task is still DECOMPOSING.
	if ts.State != types.StateDecomposing {
		d.logger.Printf("decomposition response ignored — task not in DECOMPOSING state: task_id=%s state=%s",
			resp.TaskID, ts.State)
		return nil
	}

	// ── Validate plan ──────────────────────────────────────────────────────
	if err := d.validatePlan(resp.Plan, ts); err != nil {
		d.logger.Printf("plan validation failed: task_id=%s error=%v", resp.TaskID, err)
		errorCode := classifyPlanError(err)
		d.failTask(ts, errorCode, fmt.Sprintf("Execution plan is invalid: %s", err.Error()))
		return fmt.Errorf("plan validation: %w", err)
	}

	// ── Transition to PLAN_ACTIVE ──────────────────────────────────────────
	now := time.Now().UTC()
	ts.State = types.StatePlanActive
	ts.PlanID = resp.Plan.PlanID
	d.pendingDecompositions.Delete(ts.OrchestratorTaskRef)
	ts.StateHistory = append(ts.StateHistory, types.StateEvent{
		State:     types.StatePlanActive,
		Timestamp: now,
		NodeID:    d.cfg.NodeID,
	})

	if err := d.persistTaskState(ts, now); err != nil {
		d.logger.Printf("memory write failed on PLAN_ACTIVE: task_id=%s error=%v", resp.TaskID, err)
		// Non-fatal: plan will still proceed. Reconciliation handles it later.
	}

	// Persist the plan itself.
	if err := d.persistPlanState(resp.Plan, ts.TaskID, ts.OrchestratorTaskRef, now); err != nil {
		d.logger.Printf("plan state persist failed: task_id=%s plan_id=%s error=%v",
			resp.TaskID, resp.Plan.PlanID, err)
		// Non-fatal: executor holds plan in memory.
	}

	// ── Notify IO: plan validated, subtasks dispatching ───────────────────
	subtaskCount := len(resp.Plan.Subtasks)
	expectedMins := subtaskCount / 2
	if expectedMins < 1 {
		expectedMins = 1
	}
	_ = d.io.PushStatus(ts.TaskID, ioclient.StatusWorking,
		fmt.Sprintf("Executing %d subtasks...", subtaskCount), &expectedMins)

	// ── Hand off to Plan Executor ──────────────────────────────────────────
	if err := d.executor.Execute(resp.Plan, ts); err != nil {
		d.logger.Printf("plan executor failed to start: task_id=%s error=%v", resp.TaskID, err)
		d.failTask(ts, types.ErrCodeAgentsUnavailable, "Failed to start plan execution.")
		return fmt.Errorf("plan executor execute: %w", err)
	}

	// ── Send task_accepted to User I/O (§8.1 step 22) ────────────────────
	// Estimated completion = now + remaining timeout.
	var estimatedCompletion time.Time
	if ts.TimeoutAt != nil {
		estimatedCompletion = *ts.TimeoutAt
	} else {
		estimatedCompletion = now.Add(5 * time.Minute) // fallback
	}

	accepted := types.TaskAccepted{
		OrchestratorTaskRef: ts.OrchestratorTaskRef,
		EstimatedCompletion: estimatedCompletion,
	}
	if err := d.gateway.PublishTaskAccepted(ts.CallbackTopic, accepted); err != nil {
		d.logger.Printf("publish task_accepted failed: task_id=%s error=%v", resp.TaskID, err)
		// Non-fatal: plan is running; user may poll for status.
	}

	d.logger.Printf("plan execution started: task_id=%s plan_id=%s subtasks=%d",
		resp.TaskID, resp.Plan.PlanID, len(resp.Plan.Subtasks))
	return nil
}

// HandlePlanComplete is called by the Plan Executor when all subtasks complete successfully.
// Writes COMPLETED state, delivers aggregated result to User I/O, revokes credentials.
func (d *Dispatcher) HandlePlanComplete(ts *types.TaskState, aggregatedResults []types.PriorResult) {
	now := time.Now().UTC()

	ts.State = types.StateCompleted
	ts.CompletedAt = &now
	ts.StateHistory = append(ts.StateHistory, types.StateEvent{
		State:     types.StateCompleted,
		Timestamp: now,
		NodeID:    d.cfg.NodeID,
	})

	if err := d.persistTaskState(ts, now); err != nil {
		d.logger.Printf("memory write failed on task completion: task_id=%s error=%v", ts.TaskID, err)
	}

	// Build and deliver final task result.
	resultsJSON, _ := json.Marshal(aggregatedResults)
	result := types.TaskResult{
		OrchestratorTaskRef: ts.OrchestratorTaskRef,
		Success:             true,
		Result:              resultsJSON,
		CompletedAt:         now,
	}
	if err := d.gateway.PublishTaskResult(ts.CallbackTopic, result); err != nil {
		d.logger.Printf("publish task_result failed: task_id=%s error=%v", ts.TaskID, err)
	}

	// Notify IO: task complete.
	zero := 0
	_ = d.io.PushStatus(ts.TaskID, ioclient.StatusCompleted, "Task complete", &zero)

	if err := d.policy.RevokeCredentials(ts.OrchestratorTaskRef); err != nil {
		d.logger.Printf("credential revocation failed: orchestrator_task_ref=%s error=%v",
			ts.OrchestratorTaskRef, err)
	}

	d.activeTasks.Delete(ts.TaskID)
	d.monitor.UntrackTask(ts.TaskID)
	atomic.AddInt64(&d.queueDepth, -1)
	atomic.AddInt64(&d.tasksCompleted, 1)

	d.logger.Printf("task completed: task_id=%s plan_id=%s", ts.TaskID, ts.PlanID)
}

// HandlePlanFailed is called by the Plan Executor when the plan cannot complete.
// Writes FAILED or PARTIAL_COMPLETE state, delivers error to User I/O.
func (d *Dispatcher) HandlePlanFailed(ts *types.TaskState, errorCode string, partial bool, partialResults []types.PriorResult) {
	now := time.Now().UTC()

	finalState := types.StateFailed
	if partial {
		finalState = types.StatePartialComplete
	}

	ts.State = finalState
	ts.ErrorCode = errorCode
	ts.CompletedAt = &now
	ts.StateHistory = append(ts.StateHistory, types.StateEvent{
		State:     finalState,
		Timestamp: now,
		Reason:    errorCode,
		NodeID:    d.cfg.NodeID,
	})

	if err := d.persistTaskState(ts, now); err != nil {
		d.logger.Printf("memory write failed on task failure: task_id=%s error=%v", ts.TaskID, err)
	}

	if partial && len(partialResults) > 0 {
		resultsJSON, _ := json.Marshal(partialResults)
		result := types.TaskResult{
			OrchestratorTaskRef: ts.OrchestratorTaskRef,
			Success:             false,
			Result:              resultsJSON,
			ErrorCode:           errorCode,
			CompletedAt:         now,
		}
		_ = d.gateway.PublishTaskResult(ts.CallbackTopic, result)
	} else {
		_ = d.gateway.PublishError(ts.CallbackTopic, types.ErrorResponse{
			TaskID:      ts.TaskID,
			ErrorCode:   errorCode,
			UserMessage: humanReadableError(errorCode),
		})
	}

	// Notify IO: task failed (partial or full failure).
	zero := 0
	lastUpdate := humanReadableError(errorCode)
	if partial {
		lastUpdate = "Partially completed — some subtasks failed"
	}
	_ = d.io.PushStatus(ts.TaskID, ioclient.StatusCompleted, lastUpdate, &zero)

	if err := d.policy.RevokeCredentials(ts.OrchestratorTaskRef); err != nil {
		d.logger.Printf("credential revocation failed: orchestrator_task_ref=%s error=%v",
			ts.OrchestratorTaskRef, err)
	}

	d.activeTasks.Delete(ts.TaskID)
	d.monitor.UntrackTask(ts.TaskID)
	atomic.AddInt64(&d.queueDepth, -1)
	atomic.AddInt64(&d.tasksFailed, 1)

	d.logger.Printf("task %s: task_id=%s plan_id=%s error_code=%s", finalState, ts.TaskID, ts.PlanID, errorCode)
}

// ── Read-only Accessors ───────────────────────────────────────────────────────

// GetActiveTasks returns a snapshot of all currently tracked non-terminal tasks.
// Used by the health endpoint to report active_tasks count (§12.2).
func (d *Dispatcher) GetActiveTasks() []*types.TaskState {
	var tasks []*types.TaskState
	d.activeTasks.Range(func(_, v any) bool {
		if ts, ok := v.(*types.TaskState); ok {
			tasks = append(tasks, ts)
		}
		return true
	})
	return tasks
}

// GetMetrics returns current task counters for metrics emission (§15.2).
func (d *Dispatcher) GetMetrics() (received, completed, failed, violations, decompositionFailed, queueDepth int64) {
	return atomic.LoadInt64(&d.tasksReceived),
		atomic.LoadInt64(&d.tasksCompleted),
		atomic.LoadInt64(&d.tasksFailed),
		atomic.LoadInt64(&d.policyViolations),
		atomic.LoadInt64(&d.decompositionFailed),
		atomic.LoadInt64(&d.queueDepth)
}

// ── Internal Helpers ──────────────────────────────────────────────────────────

// watchDecompositionTimeout fires DECOMPOSITION_TIMEOUT if task is still DECOMPOSING
// after DecompositionTimeoutSeconds (§FR-TD-02, §8.5).
func (d *Dispatcher) watchDecompositionTimeout(ts *types.TaskState) {
	timeout := time.Duration(d.cfg.DecompositionTimeoutSeconds) * time.Second
	<-time.After(timeout)

	tsVal, ok := d.activeTasks.Load(ts.TaskID)
	if !ok {
		return // Task already completed or was cleaned up.
	}
	current := tsVal.(*types.TaskState)
	if current.State != types.StateDecomposing {
		return // Task has moved on (plan received).
	}

	atomic.AddInt64(&d.decompositionFailed, 1)
	d.logger.Printf("decomposition timeout: task_id=%s orchestrator_task_ref=%s timeout=%s",
		ts.TaskID, ts.OrchestratorTaskRef, timeout)
	d.failTask(current, types.ErrCodeDecompositionTimeout,
		fmt.Sprintf("Planner Agent did not respond within %d seconds.", d.cfg.DecompositionTimeoutSeconds))
}

// failTask transitions a task to a terminal failure state, publishes error, and cleans up.
func (d *Dispatcher) failTask(ts *types.TaskState, errorCode, userMessage string) {
	now := time.Now().UTC()
	d.pendingDecompositions.Delete(ts.OrchestratorTaskRef)

	ts.State = types.StateDecompositionFailed
	ts.ErrorCode = errorCode
	ts.CompletedAt = &now
	ts.StateHistory = append(ts.StateHistory, types.StateEvent{
		State:     types.StateDecompositionFailed,
		Timestamp: now,
		Reason:    errorCode,
		NodeID:    d.cfg.NodeID,
	})

	if err := d.persistTaskState(ts, now); err != nil {
		d.logger.Printf("memory write failed on task failure: task_id=%s error=%v", ts.TaskID, err)
	}

	_ = d.gateway.PublishError(ts.CallbackTopic, types.ErrorResponse{
		TaskID:      ts.TaskID,
		ErrorCode:   errorCode,
		UserMessage: userMessage,
	})

	// Notify IO: task failed during decomposition.
	zero := 0
	_ = d.io.PushStatus(ts.TaskID, ioclient.StatusCompleted, userMessage, &zero)

	d.activeTasks.Delete(ts.TaskID)
	d.monitor.UntrackTask(ts.TaskID)
	atomic.AddInt64(&d.queueDepth, -1)
}

// validatePlan checks an execution plan for structural validity (§FR-TD-04, §FR-TD-07).
func (d *Dispatcher) validatePlan(plan types.ExecutionPlan, ts *types.TaskState) error {
	if len(plan.Subtasks) == 0 {
		return fmt.Errorf("%s: plan has no subtasks", types.ErrCodeEmptyPlan)
	}
	if len(plan.Subtasks) > d.cfg.MaxSubtasksPerPlan {
		return fmt.Errorf("%s: plan has %d subtasks, max is %d",
			types.ErrCodePlanTooLarge, len(plan.Subtasks), d.cfg.MaxSubtasksPerPlan)
	}

	// Build a set of subtask IDs for dependency validation.
	subtaskIDs := make(map[string]bool, len(plan.Subtasks))
	for _, st := range plan.Subtasks {
		if subtaskIDs[st.SubtaskID] {
			return fmt.Errorf("%s: duplicate subtask_id %q", types.ErrCodeInvalidPlan, st.SubtaskID)
		}
		subtaskIDs[st.SubtaskID] = true
	}

	// Validate depends_on references exist and check for scope violations.
	scopeDomains := make(map[string]bool, len(ts.PolicyScope.Domains))
	for _, d := range ts.PolicyScope.Domains {
		scopeDomains[d] = true
	}

	for _, st := range plan.Subtasks {
		for _, dep := range st.DependsOn {
			if !subtaskIDs[dep] {
				return fmt.Errorf("%s: subtask %q depends on unknown subtask_id %q",
					types.ErrCodeInvalidPlan, st.SubtaskID, dep)
			}
		}
		// Scope violation check: each subtask's required domains must be within policy_scope.
		for _, domain := range st.RequiredSkillDomains {
			if len(scopeDomains) > 0 && !scopeDomains[domain] {
				return fmt.Errorf("%s: subtask %q requires domain %q outside policy scope",
					types.ErrCodeScopeViolation, st.SubtaskID, domain)
			}
		}
	}

	// Detect circular dependencies via topological sort (Kahn's algorithm).
	if hasCycle(plan.Subtasks) {
		return fmt.Errorf("%s: circular dependency detected in plan", types.ErrCodeInvalidPlan)
	}

	return nil
}

// hasCycle returns true if the subtask dependency graph contains a cycle.
func hasCycle(subtasks []types.Subtask) bool {
	inDegree := make(map[string]int, len(subtasks))
	for _, st := range subtasks {
		if _, ok := inDegree[st.SubtaskID]; !ok {
			inDegree[st.SubtaskID] = 0
		}
		for _, dep := range st.DependsOn {
			inDegree[dep]++ // we're doing reverse; simpler: count how many depend on each
			_ = dep
		}
	}

	// Proper Kahn's: build in-degree count for each node.
	indeg := make(map[string]int, len(subtasks))
	for _, st := range subtasks {
		if _, ok := indeg[st.SubtaskID]; !ok {
			indeg[st.SubtaskID] = 0
		}
		for range st.DependsOn {
			indeg[st.SubtaskID]++
		}
	}

	queue := []string{}
	for id, deg := range indeg {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	visited := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		visited++
		// For each subtask whose depends_on includes cur, decrement its in-degree.
		for _, st := range subtasks {
			for _, dep := range st.DependsOn {
				if dep == cur {
					indeg[st.SubtaskID]--
					if indeg[st.SubtaskID] == 0 {
						queue = append(queue, st.SubtaskID)
					}
				}
			}
		}
	}

	return visited != len(subtasks)
}

// classifyPlanError maps a validation error message to the appropriate error code.
func classifyPlanError(err error) string {
	msg := err.Error()
	for _, code := range []string{
		types.ErrCodeEmptyPlan, types.ErrCodePlanTooLarge,
		types.ErrCodeScopeViolation, types.ErrCodeInvalidPlan,
	} {
		if len(msg) >= len(code) && msg[:len(code)] == code {
			return code
		}
	}
	return types.ErrCodeInvalidPlan
}

// dedupCheck queries Memory for an existing task_state within the idempotency window.
// Returns the existing TaskState if a duplicate is found, or nil if the task is new.
func (d *Dispatcher) dedupCheck(taskID string, windowSeconds int) *types.TaskState {
	windowStart := time.Now().UTC().Add(-time.Duration(windowSeconds) * time.Second)
	records, err := d.memory.Read(types.MemoryQuery{
		TaskID:        taskID,
		DataType:      types.DataTypeTaskState,
		FromTimestamp: &windowStart,
	})
	if err != nil || len(records) == 0 {
		return nil
	}
	// Use the most recent record (last in ascending-timestamp slice).
	var ts types.TaskState
	if err := json.Unmarshal(records[len(records)-1].Payload, &ts); err != nil {
		return nil
	}
	return &ts
}

func (d *Dispatcher) persistTaskState(ts *types.TaskState, timestamp time.Time) error {
	statePayload, err := json.Marshal(ts)
	if err != nil {
		return fmt.Errorf("marshal task state: %w", err)
	}

	return d.memory.Write(types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: ts.OrchestratorTaskRef,
		TaskID:              ts.TaskID,
		DataType:            types.DataTypeTaskState,
		Timestamp:           timestamp,
		Payload:             statePayload,
	})
}

func (d *Dispatcher) persistPlanState(plan types.ExecutionPlan, taskID, orchRef string, timestamp time.Time) error {
	planPayload, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}

	return d.memory.Write(types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: orchRef,
		TaskID:              taskID,
		PlanID:              plan.PlanID,
		DataType:            types.DataTypePlanState,
		Timestamp:           timestamp,
		Payload:             planPayload,
	})
}

// extractRawInput attempts to pull raw_input from the user task payload.
// If the payload is not a JSON object or raw_input is missing, returns the full payload as string.
func extractRawInput(payload []byte) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return string(payload)
	}
	if raw, ok := m["raw_input"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	return string(payload)
}

func buildDecompositionInstructions(taskID, rawInput string, scope types.PolicyScope) string {
	allowedDomains := scope.Domains
	if len(allowedDomains) == 0 {
		allowedDomains = []string{"general"}
	}

	domains := "[]"
	if len(allowedDomains) > 0 {
		raw, _ := json.Marshal(allowedDomains)
		domains = string(raw)
	}

	return fmt.Sprintf(
		"Decompose the user's task into a JSON execution plan for downstream agents.\n"+
			"Return JSON only. Do not wrap the result in markdown fences. Do not include commentary.\n"+
			"The JSON schema is:\n"+
			"{\"plan_id\":\"string\",\"parent_task_id\":\"%s\",\"created_at\":\"RFC3339 timestamp\",\"subtasks\":[{\"subtask_id\":\"string\",\"required_skill_domains\":[\"domain\"],\"action\":\"string\",\"instructions\":\"string\",\"params\":{},\"depends_on\":[\"subtask_id\"],\"timeout_seconds\":30}]}\n"+
			"Rules:\n"+
			"- parent_task_id must equal %q\n"+
			"- required_skill_domains for every subtask must be a subset of %s\n"+
			"- Do not invent new skill domain names outside the allowed list\n"+
			"- Use an empty array for depends_on when a subtask has no dependencies\n"+
			"- Keep the plan concise and executable\n"+
			"User task:\n%s",
		taskID,
		taskID,
		domains,
		rawInput,
	)
}

func parseExecutionPlan(raw json.RawMessage) (types.ExecutionPlan, error) {
	var plan types.ExecutionPlan
	if len(raw) == 0 {
		return plan, fmt.Errorf("empty planner result")
	}
	if err := json.Unmarshal(raw, &plan); err == nil && plan.PlanID != "" {
		return plan, nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
		if err := json.Unmarshal([]byte(s), &plan); err == nil && plan.PlanID != "" {
			return plan, nil
		}
	}

	return plan, fmt.Errorf("planner result is not a valid execution plan JSON object")
}

func (d *Dispatcher) activeTaskByOrchRef(orchRef string) *types.TaskState {
	var found *types.TaskState
	d.activeTasks.Range(func(_, v any) bool {
		ts, ok := v.(*types.TaskState)
		if ok && ts.OrchestratorTaskRef == orchRef {
			found = ts
			return false
		}
		return true
	})
	return found
}

func minInt(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

// validateSchema checks all required fields on a UserTask (§5.1, §FR-TRK-02, §FR-TRK-04).
//
// Rules:
//   - task_id: required, UUID v4 format
//   - user_id: required, non-empty
//   - required_skill_domains: OPTIONAL in v3.0 (FR-TRK-04)
//   - priority: 1–10
//   - timeout_seconds: 30–86400
//   - payload: max 1MB serialized
//   - callback_topic: required, valid NATS topic characters
func validateSchema(task types.UserTask) error {
	if task.TaskID == "" {
		return fmt.Errorf("task_id is required")
	}
	if !isValidUUID(task.TaskID) {
		return fmt.Errorf("task_id must be a valid UUID-formatted string: %q", task.TaskID)
	}
	if task.UserID == "" {
		return fmt.Errorf("user_id is required")
	}
	// required_skill_domains is OPTIONAL in v3.0 (FR-TRK-04).
	if task.Priority < 1 || task.Priority > 10 {
		return fmt.Errorf("priority must be 1–10, got %d", task.Priority)
	}
	if task.TimeoutSeconds < 30 || task.TimeoutSeconds > 86400 {
		return fmt.Errorf("timeout_seconds must be 30–86400, got %d", task.TimeoutSeconds)
	}
	if len(task.Payload) > maxPayloadBytes {
		return fmt.Errorf("payload exceeds maximum size of 1MB (%d bytes)", len(task.Payload))
	}
	if task.CallbackTopic == "" {
		return fmt.Errorf("callback_topic is required")
	}
	if !isValidNATSTopic(task.CallbackTopic) {
		return fmt.Errorf("callback_topic contains invalid characters: %q", task.CallbackTopic)
	}
	return nil
}

// humanReadableError returns a user-facing message for a given error code.
func humanReadableError(code string) string {
	switch code {
	case types.ErrCodeDecompositionTimeout:
		return "The Planner Agent did not respond in time. Please retry."
	case types.ErrCodeInvalidPlan:
		return "The execution plan returned by the Planner Agent was invalid."
	case types.ErrCodeEmptyPlan:
		return "The Planner Agent returned an empty plan with no subtasks."
	case types.ErrCodePlanTooLarge:
		return "The Planner Agent returned a plan that exceeds the maximum allowed size."
	case types.ErrCodeScopeViolation:
		return "The Planner Agent requested capabilities outside your authorized scope."
	case types.ErrCodeMaxRetriesExceeded:
		return "Maximum recovery attempts exceeded. Task could not complete."
	case types.ErrCodeTimedOut:
		return "Task exceeded its maximum allowed time."
	default:
		return "Task failed. Please retry or contact support."
	}
}

// isValidUUID returns true if s matches the general UUID string format.
func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	dashAt := map[int]bool{8: true, 13: true, 18: true, 23: true}
	for i, c := range s {
		if dashAt[i] {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// isValidNATSTopic returns true if every character in s is legal in a NATS subject.
func isValidNATSTopic(s string) bool {
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == '_' || c == '>' || c == '*') {
			return false
		}
	}
	return true
}

// newUUID generates a random UUID v4 using crypto/rand.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
