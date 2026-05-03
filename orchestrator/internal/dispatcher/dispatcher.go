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
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	ioclient "github.com/mlim3/cerberOS/orchestrator/internal/io"
	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// PersonalizationClient is the narrow view on the personalization package we
// need inside the dispatcher. Using an interface keeps the personalization
// dependency optional and test-mockable.
type PersonalizationClient interface {
	FetchFacts(ctx context.Context, userID string, maxFacts int) ([]string, error)
}

// maxPayloadBytes is the maximum allowed serialized payload size (§5.1).
const maxPayloadBytes = 1 << 20 // 1 MB

// defaultIdempotencyWindow is the dedup window used when the task does not specify one.
const defaultIdempotencyWindow = 300 // seconds

// ── Internal Interface Definitions ───────────────────────────────────────────

// Gateway defines the outbound operations the Dispatcher needs from M1.
// Avoids a direct import cycle with the gateway package.
type Gateway interface {
	PublishTaskAccepted(ctx context.Context, callbackTopic string, accepted types.TaskAccepted) error
	PublishError(ctx context.Context, callbackTopic string, resp types.ErrorResponse) error
	PublishTaskResult(ctx context.Context, callbackTopic string, result types.TaskResult) error
	PublishTaskSpec(ctx context.Context, spec types.TaskSpec) error
	PublishStatusUpdate(ctx context.Context, userContextID string, status types.StatusResponse) error
}

// PolicyEnforcer defines the policy operations the Dispatcher needs from M3.
type PolicyEnforcer interface {
	ValidateAndScope(ctx context.Context, taskID string, orchestratorTaskRef string, userID string, requiredSkillDomains []string, timeoutSeconds int) (types.PolicyScope, error)
	RevokeCredentials(ctx context.Context, orchestratorTaskRef string) error
}

// TaskMonitor defines the monitor operations the Dispatcher needs from M4.
type TaskMonitor interface {
	TrackTask(ts *types.TaskState)
	UntrackTask(taskID string)
}

// PlanExecutor defines the plan execution interface the Dispatcher delegates to (M7).
type PlanExecutor interface {
	Execute(ctx context.Context, plan types.ExecutionPlan, ts *types.TaskState) error
	HandleSubtaskResult(ctx context.Context, result types.TaskResult) error
	UserIDForSubtask(orchRef string) (string, bool)
	TaskStateForSubtask(orchRef string) (*types.TaskState, bool)
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
	logger   *slog.Logger

	// personalization is optional. When nil, the planner prompt is built
	// without user facts — behaviour is identical to before personalization
	// was introduced. Wired via SetPersonalization from main.
	personalization PersonalizationClient

	// activeTasks maps task_id → *TaskState for all non-terminal in-flight tasks.
	activeTasks sync.Map

	// pendingDecompositions tracks top-level tasks currently waiting for a
	// planner agent result. key = top-level orchestrator_task_ref.
	pendingDecompositions sync.Map

	// pendingApprovals tracks plans waiting on an explicit user decision
	// (approve/reject). key = top-level orchestrator_task_ref → *pendingApproval.
	pendingApprovals sync.Map

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
		logger:   observability.LoggerWithModule("dispatcher"),
	}
}

// SetPersonalization wires an optional personalization client. main calls this
// after constructing the dispatcher; tests skip the call and personalization
// is a no-op.
func (d *Dispatcher) SetPersonalization(p PersonalizationClient) {
	d.personalization = p
}

// ── Inbound Task Pipeline ─────────────────────────────────────────────────────

// HandleInboundTask is the entry point for all user_task messages from the Gateway.
// ctx carries the trace_id and task_id established at the gateway entry point.
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
func (d *Dispatcher) HandleInboundTask(ctx context.Context, task types.UserTask) error {
	ctx = observability.WithModule(ctx, "task_dispatcher")
	log := observability.LogFromContext(ctx)

	atomic.AddInt64(&d.tasksReceived, 1)
	log.Info("dispatcher accepted user task; starting validation/dedup/policy pipeline",
		"task_id", task.TaskID,
		"user_id", task.UserID,
		"priority", task.Priority,
		"content_preview", observability.PreviewHeadTail(extractRawInput(task.Payload), 15, 10))

	// ── Step 1: Schema validation ──────────────────────────────────────────
	if err := validateSchema(task); err != nil {
		log.Warn("rejected user task: schema validation failed; returning error_response to io", "error", err)
		_ = d.gateway.PublishError(ctx, task.CallbackTopic, types.ErrorResponse{
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

	{
		dedupCtx, dedupSpan := observability.StartSpan(ctx, "dedup_check")
		existing := d.dedupCheck(task.TaskID, idempotencyWindow)
		dedupSpan.End()
		_ = dedupCtx
		// A task_id is only a "duplicate" while a prior attempt is still in
		// flight. Once the prior attempt reaches a terminal state (COMPLETED,
		// FAILED, TIMED_OUT, CANCELLED, DELIVERY_FAILED), the same task_id
		// must be allowed to re-enter as a follow-up — ChatGPT-style, tasks
		// never really "end". A new orchestrator_task_ref is generated below
		// so downstream state is isolated from the prior attempt.
		if existing != nil && !types.IsTerminalState(existing.State) {
			log.Info("rejected duplicate user task: prior attempt is still in flight", "current_state", existing.State)
			_ = d.gateway.PublishError(ctx, task.CallbackTopic, types.ErrorResponse{
				TaskID:      task.TaskID,
				ErrorCode:   types.ErrCodeDuplicateTask,
				UserMessage: fmt.Sprintf("task already submitted — current state: %s", existing.State),
			})
			return nil
		}
		if existing != nil {
			log.Info("accepted re-entry of completed task as follow-up turn in same conversation",
				"prior_state", existing.State,
				"prior_orchestrator_task_ref", existing.OrchestratorTaskRef,
			)
		}
	}

	// ── Step 3: Generate orchestrator_task_ref ─────────────────────────────
	orchRef := newUUID()

	// ── Step 4: Policy validation ──────────────────────────────────────────
	var scope types.PolicyScope
	{
		policyCtx, policySpan := observability.StartSpan(ctx, "policy_validation")
		observability.SpanSetTaskAttributes(policySpan, task.TaskID, task.UserID)
		var err error
		scope, err = d.policy.ValidateAndScope(
			policyCtx, task.TaskID, orchRef, task.UserID, task.RequiredSkillDomains, task.TimeoutSeconds,
		)
		observability.SpanRecordError(policySpan, err)
		policySpan.End()
		if err != nil {
			atomic.AddInt64(&d.policyViolations, 1)
			log.Warn("policy enforcer denied user task: requested skills exceed user permissions",
				"orchestrator_task_ref", orchRef,
				"requested_skill_domains", task.RequiredSkillDomains,
				"reason", err.Error())
			_ = d.gateway.PublishError(ctx, task.CallbackTopic, types.ErrorResponse{
				TaskID:      task.TaskID,
				ErrorCode:   types.ErrCodePolicyViolation,
				UserMessage: "Task requires resources outside your configured permissions.",
			})
			return fmt.Errorf("policy validation denied: %w", err)
		}
	}

	// ── Build initial task state as DECOMPOSING ────────────────────────────
	now := time.Now().UTC()
	timeoutAt := now.Add(time.Duration(task.TimeoutSeconds) * time.Second)

	ts := &types.TaskState{
		OrchestratorTaskRef:  orchRef,
		TaskID:               task.TaskID,
		UserID:               task.UserID,
		TraceID:              observability.TraceIDFrom(ctx),
		State:                types.StateDecomposing,
		RequiredSkillDomains: task.RequiredSkillDomains,
		PolicyScope:          scope,
		TimeoutAt:            &timeoutAt,
		CallbackTopic:        task.CallbackTopic,
		UserContextID:        task.UserContextID,
		ConversationID:       task.ConversationID,
		IdempotencyWindow:    idempotencyWindow,
		Payload:              task.Payload,
		StateHistory: []types.StateEvent{
			{State: types.StateReceived, Timestamp: now, NodeID: d.cfg.NodeID},
			{State: types.StateDecomposing, Timestamp: now, NodeID: d.cfg.NodeID},
		},
	}

	// ── Step 5: Persist DECOMPOSING BEFORE sending planner dispatch ────────
	if err := d.persistTaskState(ts, now); err != nil {
		log.Error("memory rejected task-state write before decomposition; aborting and returning error to io", "error", err)
		_ = d.gateway.PublishError(ctx, task.CallbackTopic, types.ErrorResponse{
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

	rawInput := extractRawInput(task.Payload)
	systemPrompt := extractSystemPrompt(task.Payload)
	maintenance := isMaintenancePayload(task.Payload)

	// ── Early task_accepted to User I/O ────────────────────────────────────
	// Fires right after policy + memory persist so the user sees acknowledgement
	// in ~tens of ms instead of waiting a full planner-LLM round-trip. The second
	// publish in HandleDecompositionResponse is removed to avoid duplicate events.
	acceptedAt := types.TaskAccepted{
		OrchestratorTaskRef: orchRef,
		EstimatedCompletion: timeoutAt,
	}
	if err := d.gateway.PublishTaskAccepted(ctx, task.CallbackTopic, acceptedAt); err != nil {
		log.Warn("could not publish early task_accepted to io; chat stream will rely on status updates", "error", err)
		// Non-fatal: the task still proceeds; IO will fall through to status updates.
	} else {
		log.Info("sent early task_accepted ack to io so the user sees feedback before planner round-trip",
			"orchestrator_task_ref", orchRef)
	}

	// ── Notify IO: task received, planning underway ────────────────────────
	if !maintenance {
		expectedMins := 2
		_ = d.io.PushStatus(task.TaskID, ioclient.StatusWorking, "Planning your task...", &expectedMins, observability.TraceIDFrom(ctx))
	}

	// ── Step 6: Publish planner task via standard task.inbound ─────────────
	// Personalization: fetch user facts from Memory (best-effort). A failure
	// or empty result is non-fatal — the prompt is emitted without facts.
	// Cron / maintenance tasks skip personalization to avoid noisy Memory reads.
	var userFacts []string
	if !maintenance && d.personalization != nil && task.UserID != "" {
		facts, ferr := d.personalization.FetchFacts(ctx, task.UserID, 8)
		if ferr != nil {
			log.Warn("could not fetch user personalization facts from memory; continuing without them", "error", ferr)
		} else if len(facts) > 0 {
			userFacts = facts
			log.Info("attached user personalization facts to planner prompt", "fact_count", len(facts))
		}
	}
	spec := types.TaskSpec{
		OrchestratorTaskRef:  orchRef,
		TaskID:               orchRef,
		UserID:               task.UserID,
		RequiredSkillDomains: []string{"general"},
		PolicyScope:          scope,
		TimeoutSeconds:       minInt(task.TimeoutSeconds, d.cfg.DecompositionTimeoutSeconds),
		Payload:              task.Payload,
		Instructions:         buildDecompositionInstructionsWithFacts(task.TaskID, rawInput, scope, userFacts, systemPrompt),
		Metadata: map[string]string{
			"task_kind":      taskKindForPlannerSpec(maintenance),
			"parent_task_id": task.TaskID,
		},
		CallbackTopic:   task.CallbackTopic,
		UserContextID:   task.UserContextID,
		ConversationID:  task.ConversationID,
		ProgressSummary: "Generating execution plan",
		TraceID:         ts.TraceID,
	}

	{
		decompCtx, decompSpan := observability.StartSpan(ctx, "decomposition")
		observability.SpanSetTaskAttributes(decompSpan, task.TaskID, task.UserID)
		err := d.gateway.PublishTaskSpec(decompCtx, spec)
		observability.SpanRecordError(decompSpan, err)
		decompSpan.End()
		if err != nil {
			log.Error("could not publish planner task on nats; failing user task with agents-unavailable", "error", err)
			d.failTask(ctx, ts, types.ErrCodeAgentsUnavailable, "Could not reach Planner Agent. Please retry.")
			return fmt.Errorf("publish planner task: %w", err)
		}
	}
	d.pendingDecompositions.Store(orchRef, task.TaskID)

	// ── Step 7: Start decomposition timeout goroutine ──────────────────────
	go d.watchDecompositionTimeout(ctx, ts)

	log.Info("published planner task to agents on nats; awaiting decomposition response",
		"orchestrator_task_ref", orchRef,
		"timeout_seconds", spec.TimeoutSeconds,
		"content_preview", observability.PreviewHeadTail(rawInput, 15, 10))
	return nil
}

// HandleTaskResult routes terminal agent outcomes. Planner-task results are
// parsed into execution plans; all other outcomes are delegated to the executor
// as subtask results.
// ctx is built by the inbound handler from the message envelope's trace_id.
func (d *Dispatcher) HandleTaskResult(ctx context.Context, result types.TaskResult) error {
	if _, ok := d.pendingDecompositions.Load(result.OrchestratorTaskRef); ok {
		d.pendingDecompositions.Delete(result.OrchestratorTaskRef)
		if !result.Success {
			if ts := d.activeTaskByOrchRef(result.OrchestratorTaskRef); ts != nil {
				msg := "Planner agent failed to produce an execution plan."
				if result.ErrorCode != "" {
					msg = fmt.Sprintf("Planner agent failed: %s", result.ErrorCode)
				}
				d.failTask(ctx, ts, types.ErrCodeAgentsUnavailable, msg)
			}
			return nil
		}

		plan, err := parseExecutionPlan(result.Result)
		if err != nil {
			// Log a bounded sample of the raw planner payload so operators can
			// see what the LLM actually returned (common cause: a free-form
			// conversational reply on an ambiguous follow-up prompt instead of
			// the required execution-plan JSON). Capped to avoid flooding Loki.
			rawSample := string(result.Result)
			const maxSample = 1024
			if len(rawSample) > maxSample {
				rawSample = rawSample[:maxSample] + "…[truncated]"
			}
			log := observability.LogFromContext(observability.WithModule(ctx, "task_dispatcher"))
			log.Warn("planner returned a non-JSON or malformed plan; failing task as invalid_plan (raw_result_sample is bounded for debug)",
				"orchestrator_task_ref", result.OrchestratorTaskRef,
				"parse_error", err.Error(),
				"raw_result_len", len(result.Result),
				"raw_result_sample", rawSample,
			)
			if ts := d.activeTaskByOrchRef(result.OrchestratorTaskRef); ts != nil {
				d.failTask(ctx, ts, types.ErrCodeInvalidPlan,
					fmt.Sprintf("Planner agent returned an invalid plan: %v", err))
			}
			return fmt.Errorf("parse planner result: %w", err)
		}
		return d.HandleDecompositionResponse(ctx, types.DecompositionResponse{
			TaskID: plan.ParentTaskID,
			Plan:   plan,
		})
	}

	return d.executor.HandleSubtaskResult(ctx, result)
}

// HandleDecompositionResponse processes a task_decomposition_response from the Planner Agent.
// Validates the plan, transitions task to PLAN_ACTIVE, passes plan to Plan Executor,
// and sends task_accepted to User I/O (§7 Flow 3.5, §8.1 steps 13–22).
func (d *Dispatcher) HandleDecompositionResponse(ctx context.Context, resp types.DecompositionResponse) error {
	ctx = observability.WithModule(ctx, "task_dispatcher")
	log := observability.LogFromContext(ctx)

	tsVal, ok := d.activeTasks.Load(resp.TaskID)
	if !ok {
		// Task may have timed out already — ignore stale response.
		log.Info("dropped decomposition response: parent task is no longer active (timed out or completed)")
		return nil
	}
	ts := tsVal.(*types.TaskState)

	// Guard: only act if task is still DECOMPOSING.
	if ts.State != types.StateDecomposing {
		log.Info("dropped decomposition response: parent task already moved past DECOMPOSING", "state", ts.State)
		return nil
	}

	// ── Validate plan ──────────────────────────────────────────────────────
	if err := d.validatePlan(resp.Plan, ts); err != nil {
		log.Warn("rejecting invalid execution plan from planner; failing task", "error", err)
		errorCode := classifyPlanError(err)
		d.failTask(ctx, ts, errorCode, fmt.Sprintf("Execution plan is invalid: %s", err.Error()))
		return fmt.Errorf("plan validation: %w", err)
	}

	// ── Plan approval gate (multi-step prompting & confirmation) ──────────
	now := time.Now().UTC()
	ts.PlanID = resp.Plan.PlanID
	d.pendingDecompositions.Delete(ts.OrchestratorTaskRef)

	// Cron / maintenance runs must not block on human plan approval.
	if d.planApprovalRequired(resp.Plan) && !isMaintenancePayload(ts.Payload) {
		return d.enterAwaitingApproval(ctx, ts, resp.Plan, now)
	}

	return d.startPlanExecution(ctx, ts, resp.Plan, now)
}

// planApprovalRequired applies PLAN_APPROVAL_MODE to the current plan.
//   - off    → never require approval
//   - always → always require approval
//   - multi  (default) → require only when the plan has more than one subtask;
//     a single-subtask plan behaves like a classic single-agent task and is
//     dispatched immediately (no user friction).
func (d *Dispatcher) planApprovalRequired(plan types.ExecutionPlan) bool {
	switch d.cfg.PlanApprovalMode {
	case "off":
		return false
	case "always":
		return true
	default:
		return len(plan.Subtasks) > 1
	}
}

// enterAwaitingApproval persists AWAITING_APPROVAL, pushes a plan_preview event
// to IO, and arms a timeout that fails the task if the user never responds.
// The plan is NOT handed to the executor yet — that only happens once
// HandlePlanDecision records an approval.
func (d *Dispatcher) enterAwaitingApproval(ctx context.Context, ts *types.TaskState, plan types.ExecutionPlan, now time.Time) error {
	log := observability.LogFromContext(ctx)

	ts.State = types.StateAwaitingApproval
	ts.StateHistory = append(ts.StateHistory, types.StateEvent{
		State:     types.StateAwaitingApproval,
		Timestamp: now,
		NodeID:    d.cfg.NodeID,
	})
	if err := d.persistTaskState(ts, now); err != nil {
		log.Warn("could not persist task transition to AWAITING_APPROVAL in memory; continuing in-process", "error", err)
	}
	if err := d.persistPlanState(plan, ts.TaskID, ts.OrchestratorTaskRef, now); err != nil {
		log.Warn("could not persist execution plan to memory; continuing in-process", "plan_id", plan.PlanID, "error", err)
	}

	timeoutSec := d.cfg.PlanApprovalTimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 300
	}
	// Build preview payload
	previewSubtasks := make([]ioclient.PlanPreviewSubtask, 0, len(plan.Subtasks))
	for _, s := range plan.Subtasks {
		previewSubtasks = append(previewSubtasks, ioclient.PlanPreviewSubtask{
			SubtaskID:    s.SubtaskID,
			Action:       s.Action,
			Instructions: s.Instructions,
			DependsOn:    s.DependsOn,
			Domains:      s.RequiredSkillDomains,
		})
	}
	if !isMaintenancePayload(ts.Payload) {
		if err := d.io.PushPlanPreview(ioclient.PlanPreviewPayload{
			TaskID:              ts.TaskID,
			OrchestratorTaskRef: ts.OrchestratorTaskRef,
			PlanID:              plan.PlanID,
			Subtasks:            previewSubtasks,
			ExpiresInSeconds:    timeoutSec,
		}); err != nil {
			log.Warn("could not push plan_preview to io; user will still see status updates and the dispatcher still awaits a decision", "error", err)
		}

		// Also surface a status-line so users without the preview UI see something.
		minsLeft := int(time.Duration(timeoutSec) * time.Second / time.Minute)
		if minsLeft < 1 {
			minsLeft = 1
		}
		_ = d.io.PushStatus(ts.TaskID, ioclient.StatusAwaitingFeedback,
			fmt.Sprintf("Awaiting your approval for a %d-step plan...", len(plan.Subtasks)), &minsLeft, ts.TraceID)
	}

	// Arm the timeout.
	timerCtx, cancel := context.WithCancel(context.Background())
	pending := &pendingApproval{
		ts:     ts,
		plan:   plan,
		cancel: cancel,
	}
	d.pendingApprovals.Store(ts.OrchestratorTaskRef, pending)

	go func() {
		select {
		case <-timerCtx.Done():
			return
		case <-time.After(time.Duration(timeoutSec) * time.Second):
			// If still pending, fail the task.
			if _, ok := d.pendingApprovals.LoadAndDelete(ts.OrchestratorTaskRef); ok {
				tctx := ctxFromTaskState(ts, "task_dispatcher")
				observability.LogFromContext(tctx).Warn("user did not approve or reject the plan within the timeout; failing task as approval_timeout",
					"orchestrator_task_ref", ts.OrchestratorTaskRef,
					"timeout_seconds", timeoutSec,
				)
				d.failTaskWithState(tctx, ts, types.StateFailed, types.ErrCodeApprovalTimeout,
					"Plan approval timed out. Please resubmit if you still want to run this task.")
			}
		}
	}()

	log.Info("plan ready; awaiting user approve/reject decision in io",
		"plan_id", plan.PlanID,
		"subtask_count", len(plan.Subtasks),
		"timeout_seconds", timeoutSec,
	)
	return nil
}

// startPlanExecution performs the historical PLAN_ACTIVE transition + executor
// hand-off. Shared between the unmodified no-approval path and the post-approval
// path reached via HandlePlanDecision.
func (d *Dispatcher) startPlanExecution(ctx context.Context, ts *types.TaskState, plan types.ExecutionPlan, now time.Time) error {
	log := observability.LogFromContext(ctx)

	ts.State = types.StatePlanActive
	ts.StateHistory = append(ts.StateHistory, types.StateEvent{
		State:     types.StatePlanActive,
		Timestamp: now,
		NodeID:    d.cfg.NodeID,
	})

	if err := d.persistTaskState(ts, now); err != nil {
		log.Warn("could not persist task transition to PLAN_ACTIVE in memory; continuing in-process", "error", err)
	}
	if err := d.persistPlanState(plan, ts.TaskID, ts.OrchestratorTaskRef, now); err != nil {
		log.Warn("could not persist execution plan to memory on PLAN_ACTIVE; continuing in-process", "plan_id", plan.PlanID, "error", err)
	}

	subtaskCount := len(plan.Subtasks)
	if !isMaintenancePayload(ts.Payload) {
		expectedMins := subtaskCount / 2
		if expectedMins < 1 {
			expectedMins = 1
		}
		_ = d.io.PushStatus(ts.TaskID, ioclient.StatusWorking,
			fmt.Sprintf("Executing %d subtasks...", subtaskCount), &expectedMins, ts.TraceID)
	}

	planCtx := observability.WithPlanID(ctx, plan.PlanID)
	if err := d.executor.Execute(planCtx, plan, ts); err != nil {
		log.Error("plan executor refused to start; failing task as agents-unavailable", "error", err)
		d.failTask(ctx, ts, types.ErrCodeAgentsUnavailable, "Failed to start plan execution.")
		return fmt.Errorf("plan executor execute: %w", err)
	}

	log.Info("handed plan to executor; subtask dispatch underway",
		"plan_id", plan.PlanID,
		"subtask_count", subtaskCount)
	return nil
}

// pendingApproval carries the state that startPlanExecution needs once the
// user's decision lands.
type pendingApproval struct {
	ts     *types.TaskState
	plan   types.ExecutionPlan
	cancel context.CancelFunc
}

// HandlePlanDecision processes an approve/reject decision from User I/O.
// Registered with the Gateway via RegisterPlanDecisionHandler in main.
func (d *Dispatcher) HandlePlanDecision(ctx context.Context, decision types.PlanDecision) error {
	ctx = observability.WithModule(ctx, "task_dispatcher")
	log := observability.LogFromContext(ctx)

	key := decision.OrchestratorTaskRef
	val, ok := d.pendingApprovals.LoadAndDelete(key)
	if !ok {
		log.Info("dropped plan decision: no pending approval matches (already approved, rejected, or timed out)",
			"orchestrator_task_ref", key,
			"approved", decision.Approved,
		)
		return nil
	}
	pending := val.(*pendingApproval)
	pending.cancel()

	ts := pending.ts
	if ts.State != types.StateAwaitingApproval {
		log.Info("dropped plan decision: task already moved past AWAITING_APPROVAL", "state", ts.State)
		return nil
	}

	if !decision.Approved {
		reason := decision.Reason
		if strings.TrimSpace(reason) == "" {
			reason = "User rejected the proposed plan."
		}
		log.Info("user rejected the proposed plan; failing task with plan_rejected",
			"orchestrator_task_ref", key,
			"reason_preview", observability.PreviewWords(reason, 20, 140))
		tctx := ctxFromTaskState(ts, "task_dispatcher")
		d.failTaskWithState(tctx, ts, types.StateFailed, types.ErrCodePlanRejected, reason)
		return nil
	}

	log.Info("user approved the proposed plan; transitioning to PLAN_ACTIVE and starting executor",
		"orchestrator_task_ref", key,
		"plan_id", pending.plan.PlanID,
		"subtask_count", len(pending.plan.Subtasks))
	now := time.Now().UTC()
	return d.startPlanExecution(ctx, ts, pending.plan, now)
}

// HandleVaultExecuteRequest is registered with the Gateway to handle vault.execute.request messages.
// It resolves user_id from in-flight task state (the orchestrator adds it — agents never provide it)
// and forwards the operation to the Vault engine for execution.
func (d *Dispatcher) HandleVaultExecuteRequest(ctx context.Context, req types.VaultExecuteRequest) (types.VaultExecuteResult, error) {
	// Resolve user_id from trusted task state — never from the agent's request.
	// First try the parent task (req.TaskID == parent task_id), then fall back
	// to the executor's subtask-orchRef → plan → user_id path (req.TaskID == subtask orchRef).
	var userID string
	if tsVal, ok := d.activeTasks.Load(req.TaskID); ok {
		userID = tsVal.(*types.TaskState).UserID
	} else if uid, ok := d.executor.UserIDForSubtask(req.TaskID); ok {
		userID = uid
	} else {
		return types.VaultExecuteResult{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       types.VaultExecStatusScopeViolation,
			ErrorCode:    "UNKNOWN_TASK",
			ErrorMessage: "task_id not found in active tasks",
		}, nil
	}

	log := observability.LogFromContext(ctx)
	log.Info("forwarding agent vault.execute request to vault engine",
		"request_id", req.RequestID,
		"operation_type", req.OperationType,
		"user_id", userID,
		"agent_id", req.AgentID)
	result, err := d.vault.Execute(ctx, userID, req)
	if err != nil {
		log.Warn("vault engine returned error for vault.execute; relaying status to agent",
			"request_id", req.RequestID,
			"status", result.Status,
			"elapsed_ms", result.ElapsedMS,
			"error", err)
	} else {
		log.Info("vault engine completed vault.execute; relaying result to agent",
			"request_id", req.RequestID,
			"status", result.Status,
			"elapsed_ms", result.ElapsedMS)
	}
	return result, err
}

// HandleCredentialRequest is registered with the Gateway to handle credential.request
// (operation: "user_input") messages from agents. It resolves the top-level task_id
// and user_id from the subtask's orchRef so the IO push reaches the correct SSE stream.
func (d *Dispatcher) HandleCredentialRequest(agentID, subtaskRef, requestID, keyName, label, traceID string) error {
	var topTaskID, userID string

	// Try direct active-task lookup first (subtaskRef == top-level task_id in some flows).
	if tsVal, ok := d.activeTasks.Load(subtaskRef); ok {
		ts := tsVal.(*types.TaskState)
		topTaskID = ts.TaskID
		userID = ts.UserID
	} else if ts, ok := d.executor.TaskStateForSubtask(subtaskRef); ok {
		topTaskID = ts.TaskID
		userID = ts.UserID
	} else {
		observability.LogFromContext(observability.WithModule(context.Background(), "task_dispatcher")).
			Warn("credential.request: could not resolve top-level task_id",
				"agent_id", agentID, "subtask_ref", subtaskRef, "request_id", requestID)
		return nil
	}

	return d.io.PushCredentialRequest(ioclient.CredentialRequestPayload{
		TaskID:      topTaskID,
		RequestID:   requestID,
		UserID:      userID,
		KeyName:     keyName,
		Label:       label,
	}, traceID)
}

// HandlePlanComplete is called by the Plan Executor when all subtasks complete successfully.
// Writes COMPLETED state, delivers aggregated result to User I/O, revokes credentials.
func (d *Dispatcher) HandlePlanComplete(ts *types.TaskState, aggregatedResults []types.PriorResult) {
	ctx := ctxFromTaskState(ts, "task_dispatcher")
	log := observability.LogFromContext(ctx)

	now := time.Now().UTC()

	ts.State = types.StateCompleted
	ts.CompletedAt = &now
	ts.StateHistory = append(ts.StateHistory, types.StateEvent{
		State:     types.StateCompleted,
		Timestamp: now,
		NodeID:    d.cfg.NodeID,
	})

	if err := d.persistTaskState(ts, now); err != nil {
		log.Error("could not persist COMPLETED task state to memory; in-process completion still proceeding", "error", err)
	}

	// Build and deliver final task result.
	resultsJSON, _ := json.Marshal(aggregatedResults)
	result := types.TaskResult{
		OrchestratorTaskRef: ts.OrchestratorTaskRef,
		Success:             true,
		Result:              resultsJSON,
		CompletedAt:         now,
	}
	{
		deliveryCtx, deliverySpan := observability.StartSpan(ctx, "result_delivery")
		observability.SpanSetTaskAttributes(deliverySpan, ts.TaskID, ts.UserID)
		err := d.gateway.PublishTaskResult(deliveryCtx, ts.CallbackTopic, result)
		observability.SpanRecordError(deliverySpan, err)
		deliverySpan.End()
		if err != nil {
			log.Error("could not publish final task_result envelope to io callback topic", "error", err)
		}
	}

	// Notify IO: task complete.
	if !isMaintenancePayload(ts.Payload) {
		zero := 0
		_ = d.io.PushStatus(ts.TaskID, ioclient.StatusCompleted, "Task complete", &zero, ts.TraceID)
	}

	if err := d.policy.RevokeCredentials(ctx, ts.OrchestratorTaskRef); err != nil {
		log.Error("could not revoke vault credentials after task completion; tokens may linger until ttl", "error", err)
	}

	d.activeTasks.Delete(ts.TaskID)
	d.monitor.UntrackTask(ts.TaskID)
	atomic.AddInt64(&d.queueDepth, -1)
	atomic.AddInt64(&d.tasksCompleted, 1)

	log.Info("task completed successfully; aggregated result delivered to io",
		"plan_id", ts.PlanID,
		"subtask_count", len(aggregatedResults),
		"result_preview", observability.PreviewHeadTail(string(resultsJSON), 15, 10))
}

// HandlePlanFailed is called by the Plan Executor when the plan cannot complete.
// Writes FAILED or PARTIAL_COMPLETE state, delivers error to User I/O.
func (d *Dispatcher) HandlePlanFailed(ts *types.TaskState, errorCode string, partial bool, partialResults []types.PriorResult) {
	ctx := ctxFromTaskState(ts, "task_dispatcher")
	log := observability.LogFromContext(ctx)

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
		log.Error("could not persist failed task state to memory; in-process failure handling still proceeding", "error", err)
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
		_ = d.gateway.PublishTaskResult(ctx, ts.CallbackTopic, result)
	} else {
		_ = d.gateway.PublishError(ctx, ts.CallbackTopic, types.ErrorResponse{
			TaskID:      ts.TaskID,
			ErrorCode:   errorCode,
			UserMessage: humanReadableError(errorCode),
		})
	}

	// Notify IO: task failed (partial or full failure).
	if !isMaintenancePayload(ts.Payload) {
		zero := 0
		lastUpdate := humanReadableError(errorCode)
		if partial {
			lastUpdate = "Partially completed — some subtasks failed"
		}
		_ = d.io.PushStatus(ts.TaskID, ioclient.StatusCompleted, lastUpdate, &zero, ts.TraceID)
	}

	if err := d.policy.RevokeCredentials(ctx, ts.OrchestratorTaskRef); err != nil {
		log.Error("could not revoke vault credentials after task failure; tokens may linger until ttl", "error", err)
	}

	d.activeTasks.Delete(ts.TaskID)
	d.monitor.UntrackTask(ts.TaskID)
	atomic.AddInt64(&d.queueDepth, -1)
	atomic.AddInt64(&d.tasksFailed, 1)

	terminalAttrs := []any{
		"final_state", finalState,
		"plan_id", ts.PlanID,
		"error_code", errorCode,
		"partial", partial,
	}
	if partial && len(partialResults) > 0 {
		blob, _ := json.Marshal(partialResults)
		terminalAttrs = append(terminalAttrs,
			"result_preview", observability.PreviewHeadTail(string(blob), 15, 10),
			"completed_subtask_count", len(partialResults))
	}
	log.Info("task reached terminal state; orchestrator pipeline finished", terminalAttrs...)
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
//
// ChatGPT-style follow-ups reuse the same TaskID across distinct attempts
// (each attempt gets a fresh OrchestratorTaskRef — see handleUserTask step 2).
// A completed prior attempt leaves its watcher goroutine still sleeping for
// the remainder of the 120 s window. When the user sends a follow-up that
// happens to be in StateDecomposing at the moment the stale timer wakes,
// a TaskID-only lookup would find the NEW attempt and incorrectly mark it
// as timed out. The guard below compares the per-attempt
// OrchestratorTaskRef so each timer can only ever fail its own attempt.
func (d *Dispatcher) watchDecompositionTimeout(ctx context.Context, ts *types.TaskState) {
	timeout := time.Duration(d.cfg.DecompositionTimeoutSeconds) * time.Second
	<-time.After(timeout)

	tsVal, ok := d.activeTasks.Load(ts.TaskID)
	if !ok {
		return // Task already completed or was cleaned up.
	}
	current := tsVal.(*types.TaskState)
	if current.OrchestratorTaskRef != ts.OrchestratorTaskRef {
		// A follow-up on the same TaskID replaced the attempt this timer
		// was watching. The new attempt has its own watchDecompositionTimeout
		// goroutine; this stale timer must not fail it.
		return
	}
	if current.State != types.StateDecomposing {
		return // Task has moved on (plan received).
	}

	atomic.AddInt64(&d.decompositionFailed, 1)
	log := observability.LogFromContext(ctx)
	log.Warn("planner agent did not return a plan within the decomposition timeout; failing user task",
		"orchestrator_task_ref", ts.OrchestratorTaskRef,
		"timeout_seconds", timeout.Seconds())
	d.failTask(ctx, current, types.ErrCodeDecompositionTimeout,
		fmt.Sprintf("Planner Agent did not respond within %d seconds.", d.cfg.DecompositionTimeoutSeconds))
}

// failTask transitions a task to a terminal failure state, publishes error, and cleans up.
func (d *Dispatcher) failTask(ctx context.Context, ts *types.TaskState, errorCode, userMessage string) {
	d.failTaskWithState(ctx, ts, types.StateDecompositionFailed, errorCode, userMessage)
}

// failTaskWithState is failTask with an explicit terminal state, used by paths
// where DECOMPOSITION_FAILED would be semantically wrong — e.g. a valid plan
// rejected by the user (FAILED + PLAN_REJECTED) or an approval timeout.
func (d *Dispatcher) failTaskWithState(ctx context.Context, ts *types.TaskState, finalState, errorCode, userMessage string) {
	log := observability.LogFromContext(observability.WithModule(ctx, "task_dispatcher"))
	now := time.Now().UTC()
	d.pendingDecompositions.Delete(ts.OrchestratorTaskRef)
	d.pendingApprovals.Delete(ts.OrchestratorTaskRef)

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
		log.Error("memory write failed on task failure", "error", err)
	}

	_ = d.gateway.PublishError(ctx, ts.CallbackTopic, types.ErrorResponse{
		TaskID:      ts.TaskID,
		ErrorCode:   errorCode,
		UserMessage: userMessage,
	})

	// Notify IO: task failed during decomposition.
	if !isMaintenancePayload(ts.Payload) {
		zero := 0
		_ = d.io.PushStatus(ts.TaskID, ioclient.StatusCompleted, userMessage, &zero, ts.TraceID)
	}

	d.activeTasks.Delete(ts.TaskID)
	d.monitor.UntrackTask(ts.TaskID)
	atomic.AddInt64(&d.queueDepth, -1)
}

// ctxFromTaskState reconstructs an observability context from a persisted TaskState.
// Used by callbacks that don't have an explicit context (e.g. HandlePlanComplete).
func ctxFromTaskState(ts *types.TaskState, module string) context.Context {
	ctx := context.Background()
	if ts.TraceID != "" {
		ctx = observability.WithTraceID(ctx, ts.TraceID)
	}
	ctx = observability.WithTaskID(ctx, ts.TaskID)
	ctx = observability.WithConversationID(ctx, ts.ConversationID)
	if ts.PlanID != "" {
		ctx = observability.WithPlanID(ctx, ts.PlanID)
	}
	return observability.WithModule(ctx, module)
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
	return buildDecompositionInstructionsWithFacts(taskID, rawInput, scope, nil, "")
}

func taskKindForPlannerSpec(maintenance bool) string {
	if maintenance {
		return "maintenance"
	}
	return "decomposition"
}

// allSkillDomains is the full set of registered skill domains in the agents component.
// Used as the fallback when the task's policy scope carries no domain restrictions
// (empty Domains = "any domain permitted"). Must stay in sync with default_skills.yaml.
var allSkillDomains = []string{"web", "data", "comms", "storage", "logs", "google_search", "github", "general"}

// buildDecompositionInstructionsWithFacts renders the planner prompt with an
// optional list of user facts (from personal_info via the Memory service).
// When facts is empty the prompt is byte-identical to the historic output so
// existing tests keep passing.
// systemPrompt is optional; when non-empty it is prepended as scheduled maintenance
// directives for the planner (cron wake / batch jobs).
func buildDecompositionInstructionsWithFacts(taskID, rawInput string, scope types.PolicyScope, facts []string, systemPrompt string) string {
	allowedDomains := scope.Domains
	if len(allowedDomains) == 0 {
		// Empty scope.Domains means "no restriction" — grant the planner access to
		// all registered skill domains so it can assign the right domain per subtask.
		allowedDomains = allSkillDomains
	}

	domains := "[]"
	if len(allowedDomains) > 0 {
		raw, _ := json.Marshal(allowedDomains)
		domains = string(raw)
	}

	systemSection := ""
	if strings.TrimSpace(systemPrompt) != "" {
		systemSection = "Scheduled maintenance directives (follow in addition to the rules below):\n" +
			strings.TrimSpace(systemPrompt) + "\n\n"
	}

	credSection := ""
	if len(scope.AvailableCredTypes) > 0 {
		raw, _ := json.Marshal(scope.AvailableCredTypes)
		credSection = fmt.Sprintf(
			"Available credential types (this user has registered these API keys in the Vault — only use credentialed skills for these types):\n%s\n",
			string(raw),
		)
	}

	factsSection := ""
	if len(facts) > 0 {
		var b strings.Builder
		b.WriteString("User facts (from personal_info — use to personalize the plan):\n")
		for _, f := range facts {
			b.WriteString("- ")
			b.WriteString(f)
			b.WriteString("\n")
		}
		factsSection = b.String()
	}

	return systemSection + factsSection + credSection + fmt.Sprintf(
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
			"Skill domain guide: use \"web\" for fetch/parse/extract of known URLs, \"data\" for transforms/reads/writes, \"comms\" for messaging, \"storage\" for file operations, \"logs\" for log queries, \"google_search\" for any Google search or web search query (PREFERRED over web for search tasks), \"github\" for GitHub API calls, \"general\" for reasoning/summarization with no external tools.\n"+
			"- Prefer \"google_search\" over \"web\" whenever the task is a search query — the system will prompt the user for an API key if it is not yet configured.\n"+
			"- Only avoid \"google_search\" if the user has explicitly said they do not want to use Google search.\n"+
			"- The \"web\" domain (web.fetch, web.parse, web.extract) does NOT require credentials — use it for fetching specific known URLs, not for general search.\n"+
			"Ambiguity handling (CRITICAL):\n"+
			"- You MUST return a valid execution plan JSON object. NEVER reply with a clarifying question, free-form text, an apology, or anything that is not JSON matching the schema above.\n"+
			"- If the user's message is ambiguous, conversational, a greeting, or a follow-up that depends on prior context, produce a SINGLE-subtask plan where one general-domain agent composes a direct natural-language answer using the conversation context provided.\n"+
			"- Treat \"Conversation so far:\" content in the user task as authoritative context for resolving pronouns and references in \"Current message:\".\n"+
			"Parallelism guidance:\n"+
			"- Independent subtasks MUST have empty depends_on so the executor can dispatch them in parallel.\n"+
			"- Only add a dependency when a later subtask genuinely needs an earlier subtask's output.\n"+
			"- Prefer the smallest dependency graph that still produces a correct result.\n"+
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
	case types.ErrCodeSubtaskFailed:
		return "A subtask failed. Please retry or rephrase your request."
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
