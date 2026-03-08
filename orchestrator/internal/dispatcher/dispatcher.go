// Package dispatcher implements M2: Task Dispatcher.
//
// The Task Dispatcher is the central coordinator for all incoming task routing
// decisions. It is the only module that drives the full task pipeline from
// receipt to dispatch.
//
// Responsibilities (§4.1 M2):
//   - Validate user_task schema — reject invalid specs immediately
//   - Perform deduplication check via Memory Interface using task_id
//   - Route to Policy Enforcer for permission validation before any agent interaction
//   - Dispatch validated, scoped task_spec to Agents Component via Gateway
//   - Track active tasks and correlate incoming results to originating user_task
//   - Monitor queue depth and emit queue_pressure metric at high-water mark
//
// CRITICAL ordering (§14.1):
//
//	Persist task state to Memory BEFORE dispatching to Agents Component.
//	This prevents orphaned agents if Memory fails post-dispatch.
package dispatcher

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
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
	PublishTaskSpec(spec types.TaskSpec) error
	PublishCapabilityQuery(query types.CapabilityQuery) (*types.CapabilityResponse, error)
	PublishTaskResult(callbackTopic string, result types.TaskResult) error
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

// ── Dispatcher ────────────────────────────────────────────────────────────────

// Dispatcher is M2: Task Dispatcher.
type Dispatcher struct {
	cfg    *config.OrchestratorConfig
	memory interfaces.MemoryClient
	vault  interfaces.VaultClient

	gateway Gateway
	policy  PolicyEnforcer
	monitor TaskMonitor
	logger  *log.Logger

	// activeTasks maps task_id → *TaskState for all non-terminal in-flight tasks.
	activeTasks sync.Map

	// orchRefIndex maps orchestrator_task_ref → task_id for reverse lookup on task results.
	orchRefIndex sync.Map

	// Metrics — use atomic operations to avoid a global lock.
	tasksReceived    int64
	tasksCompleted   int64
	tasksFailed      int64
	policyViolations int64
	queueDepth       int64
}

// New creates a new Dispatcher.
func New(
	cfg *config.OrchestratorConfig,
	memory interfaces.MemoryClient,
	vault interfaces.VaultClient,
	gw Gateway,
	policy PolicyEnforcer,
	monitor TaskMonitor,
) *Dispatcher {
	return &Dispatcher{
		cfg:     cfg,
		memory:  memory,
		vault:   vault,
		gateway: gw,
		policy:  policy,
		monitor: monitor,
		logger:  log.New(os.Stdout, "[dispatcher] ", log.LstdFlags|log.LUTC),
	}
}

// ── Inbound Task Pipeline ─────────────────────────────────────────────────────

// HandleInboundTask is the entry point for all user_task messages from the Gateway.
// Implements the full validation → dedup → policy → dispatch pipeline (§7, §17.3).
//
// Pipeline (§7):
//  1. Schema validation — INVALID_TASK_SPEC on failure
//  2. Deduplication check — DUPLICATE_TASK if task_id seen within idempotency window
//  3. Generate orchestrator_task_ref (distinct UUID)
//  4. Policy validation — POLICY_VIOLATION on deny; no agent is ever touched on deny
//  5. Capability query to Agents Component
//  6. Persist DISPATCH_PENDING state to Memory (BEFORE dispatch — §14.1)
//  7. Dispatch task_spec to Agents Component
//  8. On success, persist DISPATCHED state; on failure, persist DELIVERY_FAILED
//  9. Confirm task_accepted to User I/O
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

	// ── Step 5: Capability query ───────────────────────────────────────────
	capResp, err := d.gateway.PublishCapabilityQuery(types.CapabilityQuery{
		OrchestratorTaskRef:  orchRef,
		RequiredSkillDomains: task.RequiredSkillDomains,
	})
	if err != nil {
		d.logger.Printf("capability query failed: task_id=%s error=%v", task.TaskID, err)
		_ = d.gateway.PublishError(task.CallbackTopic, types.ErrorResponse{
			TaskID:      task.TaskID,
			ErrorCode:   types.ErrCodeAgentsUnavailable,
			UserMessage: "No capable agent is available for this task.",
		})
		return fmt.Errorf("capability query: %w", err)
	}

	// ── Build initial task state as DISPATCH_PENDING ───────────────────────
	now := time.Now().UTC()
	timeoutAt := now.Add(time.Duration(task.TimeoutSeconds) * time.Second)

	ts := &types.TaskState{
		OrchestratorTaskRef:  orchRef,
		TaskID:               task.TaskID,
		UserID:               task.UserID,
		State:                types.StateDispatchPending,
		RequiredSkillDomains: task.RequiredSkillDomains,
		PolicyScope:          scope,
		AgentID:              capResp.AgentID,
		TimeoutAt:            &timeoutAt,
		CallbackTopic:        task.CallbackTopic,
		UserContextID:        task.UserContextID,
		IdempotencyWindow:    idempotencyWindow,
		Payload:              task.Payload,
		StateHistory: []types.StateEvent{
			{State: types.StateReceived, Timestamp: now, NodeID: d.cfg.NodeID},
			{State: types.StateDispatchPending, Timestamp: now, NodeID: d.cfg.NodeID},
		},
	}

	// ── Step 6: Persist DISPATCH_PENDING BEFORE dispatch ───────────────────
	if err := d.persistTaskState(ts, now); err != nil {
		d.logger.Printf("memory write failed before dispatch: task_id=%s error=%v", task.TaskID, err)
		_ = d.gateway.PublishError(task.CallbackTopic, types.ErrorResponse{
			TaskID:      task.TaskID,
			ErrorCode:   types.ErrCodeStorageUnavailable,
			UserMessage: "Unable to persist task state. Task not dispatched.",
		})
		return fmt.Errorf("persist dispatch pending state: %w", err)
	}

	// Register with in-memory tracking and monitor.
	d.activeTasks.Store(task.TaskID, ts)
	d.orchRefIndex.Store(orchRef, task.TaskID)
	d.monitor.TrackTask(ts)
	atomic.AddInt64(&d.queueDepth, 1)

	// ── Step 7: Dispatch task_spec ─────────────────────────────────────────
	spec := types.TaskSpec{
		OrchestratorTaskRef:  orchRef,
		TaskID:               task.TaskID,
		UserID:               task.UserID,
		RequiredSkillDomains: task.RequiredSkillDomains,
		PolicyScope:          scope,
		TimeoutSeconds:       task.TimeoutSeconds,
		Payload:              task.Payload,
		CallbackTopic:        task.CallbackTopic,
		UserContextID:        task.UserContextID,
	}

	if err := d.gateway.PublishTaskSpec(spec); err != nil {
		failTime := time.Now().UTC()

		ts.State = types.StateDeliveryFailed
		ts.ErrorCode = types.ErrCodeAgentsUnavailable
		ts.StateHistory = append(ts.StateHistory, types.StateEvent{
			State:     types.StateDeliveryFailed,
			Timestamp: failTime,
			Reason:    err.Error(),
			NodeID:    d.cfg.NodeID,
		})

		if persistErr := d.persistTaskState(ts, failTime); persistErr != nil {
			d.logger.Printf(
				"failed to persist DELIVERY_FAILED state: task_id=%s orchestrator_task_ref=%s error=%v",
				task.TaskID, orchRef, persistErr,
			)
		}

		if revokeErr := d.policy.RevokeCredentials(orchRef); revokeErr != nil {
			d.logger.Printf(
				"credential revocation failed after dispatch failure: orchestrator_task_ref=%s error=%v",
				orchRef, revokeErr,
			)
		}

		d.activeTasks.Delete(task.TaskID)
		d.orchRefIndex.Delete(orchRef)
		d.monitor.UntrackTask(task.TaskID)
		atomic.AddInt64(&d.queueDepth, -1)

		d.logger.Printf(
			"task dispatch failed: task_id=%s orchestrator_task_ref=%s agent_id=%s error=%v",
			task.TaskID, orchRef, capResp.AgentID, err,
		)

		_ = d.gateway.PublishError(task.CallbackTopic, types.ErrorResponse{
			TaskID:      task.TaskID,
			ErrorCode:   types.ErrCodeAgentsUnavailable,
			UserMessage: "Task could not be delivered to an available agent.",
		})

		return fmt.Errorf("dispatch task_spec: %w", err)
	}

	// ── Step 8: Persist DISPATCHED after successful publish ────────────────
	dispatchTime := time.Now().UTC()
	ts.State = types.StateDispatched
	ts.DispatchedAt = &dispatchTime
	ts.StateHistory = append(ts.StateHistory, types.StateEvent{
		State:     types.StateDispatched,
		Timestamp: dispatchTime,
		NodeID:    d.cfg.NodeID,
	})

	if err := d.persistTaskState(ts, dispatchTime); err != nil {
		d.logger.Printf(
			"failed to persist DISPATCHED state after publish: task_id=%s orchestrator_task_ref=%s error=%v",
			task.TaskID, orchRef, err,
		)
		// Do not fail the request here — the agent has already received the task.
		// This should be handled by recovery / reconciliation later.
	}

	// ── Step 9: Confirm task_accepted to User I/O ──────────────────────────
	accepted := types.TaskAccepted{
		OrchestratorTaskRef: orchRef,
		AgentID:             capResp.AgentID,
		EstimatedCompletion: dispatchTime.Add(time.Duration(task.TimeoutSeconds) * time.Second),
	}
	if err := d.gateway.PublishTaskAccepted(task.CallbackTopic, accepted); err != nil {
		d.logger.Printf("publish task_accepted failed: task_id=%s error=%v", task.TaskID, err)
		// Non-fatal: task is already dispatched and persisted.
	}

	d.logger.Printf(
		"task dispatched: task_id=%s orchestrator_task_ref=%s agent_id=%s match=%s",
		task.TaskID, orchRef, capResp.AgentID, capResp.Match,
	)
	return nil
}

// HandleTaskResult processes an inbound task_result from the Agents Component.
// Writes COMPLETED or FAILED state to Memory, delivers result to callback_topic,
// and triggers credential revocation (§7 Flow 5).
func (d *Dispatcher) HandleTaskResult(result types.TaskResult) error {
	// Resolve task_id from orchestrator_task_ref.
	taskIDVal, ok := d.orchRefIndex.Load(result.OrchestratorTaskRef)
	if !ok {
		return fmt.Errorf("unknown orchestrator_task_ref: %s", result.OrchestratorTaskRef)
	}
	taskID := taskIDVal.(string)

	tsVal, ok := d.activeTasks.Load(taskID)
	if !ok {
		return fmt.Errorf("no active task for task_id: %s", taskID)
	}
	ts := tsVal.(*types.TaskState)

	now := time.Now().UTC()

	// Determine terminal state.
	newState := types.StateCompleted
	if !result.Success {
		newState = types.StateFailed
	}

	ts.State = newState
	ts.CompletedAt = &now
	ts.AgentID = result.AgentID
	ts.StateHistory = append(ts.StateHistory, types.StateEvent{
		State:     newState,
		Timestamp: now,
		NodeID:    d.cfg.NodeID,
	})

	// Write terminal state to Memory.
	if err := d.persistTaskState(ts, now); err != nil {
		d.logger.Printf("memory write failed on task result: task_id=%s error=%v", taskID, err)
	}

	// Deliver result to callback_topic.
	if err := d.gateway.PublishTaskResult(ts.CallbackTopic, result); err != nil {
		d.logger.Printf("publish task_result failed: task_id=%s error=%v", taskID, err)
	}

	// Trigger credential revocation (non-blocking on error).
	if err := d.policy.RevokeCredentials(ts.OrchestratorTaskRef); err != nil {
		d.logger.Printf("credential revocation failed: orchestrator_task_ref=%s error=%v", ts.OrchestratorTaskRef, err)
	}

	// Untrack the task.
	d.activeTasks.Delete(taskID)
	d.orchRefIndex.Delete(result.OrchestratorTaskRef)
	d.monitor.UntrackTask(taskID)
	atomic.AddInt64(&d.queueDepth, -1)

	if result.Success {
		atomic.AddInt64(&d.tasksCompleted, 1)
	} else {
		atomic.AddInt64(&d.tasksFailed, 1)
	}

	d.logger.Printf("task result processed: task_id=%s state=%s", taskID, newState)
	return nil
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
func (d *Dispatcher) GetMetrics() (received, completed, failed, violations, queueDepth int64) {
	return atomic.LoadInt64(&d.tasksReceived),
		atomic.LoadInt64(&d.tasksCompleted),
		atomic.LoadInt64(&d.tasksFailed),
		atomic.LoadInt64(&d.policyViolations),
		atomic.LoadInt64(&d.queueDepth)
}

// ── Internal Helpers ──────────────────────────────────────────────────────────

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

// validateSchema checks all required fields on a UserTask (§5.1, §FR-TRK-02, §FR-TRK-04).
//
// Rules:
//   - task_id: required, UUID v4 format
//   - user_id: required, non-empty
//   - required_skill_domains: required, min 1 item
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
	if len(task.RequiredSkillDomains) == 0 {
		return fmt.Errorf("required_skill_domains must have at least one entry")
	}
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

// isValidUUID returns true if s matches the general UUID string format
// (8-4-4-4-12 hex characters with dashes).
// check valid UUID v4 in subsequent phase
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
