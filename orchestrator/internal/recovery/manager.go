// Package recovery implements M5: Recovery Manager.
//
// The Recovery Manager responds to all non-nominal task events:
// agent failure, timeout, policy violation, and dependency unavailability.
// It is the sole module responsible for terminal task cleanup.
//
// Responsibilities (§4.1 M5):
//   - Determine recovery strategy based on failure count and failure type
//   - Retrieve last valid task state from Memory before recovery attempt
//   - Re-validate Vault policy scope before re-dispatch (scope cannot expand)
//   - Issue agent_terminate or task_cancel via Gateway
//   - Manage retry budget: track per-task retry count, enforce max_retries
//   - Trigger credential revocation on every terminal outcome — non-optional
//   - Schedule revocation retries if Vault is unavailable (max 5, exponential backoff)
//
// CRITICAL (§13.3): Credential revocation on every terminal outcome is NON-OPTIONAL.
// If Vault is down: log REVOCATION_FAILED, schedule retry. Do NOT block termination.
// CRITICAL (§FR-SH-06): Re-dispatched tasks cannot receive broader scope than original.
package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// revocationMaxAttempts is the max number of Vault revocation retries (§13.3).
const revocationMaxAttempts = 5

// revocationInitialBackoff is the starting backoff for revocation retries.
const revocationInitialBackoff = 500 * time.Millisecond

// ── Internal Interface Definitions ───────────────────────────────────────────

// Gateway defines the outbound operations Recovery Manager needs from M1.
type Gateway interface {
	PublishAgentTerminate(ctx context.Context, terminate types.AgentTerminate) error
	PublishTaskCancel(ctx context.Context, cancel types.TaskCancel) error
	PublishError(ctx context.Context, callbackTopic string, resp types.ErrorResponse) error
	PublishTaskSpec(ctx context.Context, spec types.TaskSpec) error
}

// PolicyEnforcer defines the policy operations Recovery Manager needs from M3.
type PolicyEnforcer interface {
	VerifyScopeStillValid(ctx context.Context, scope types.PolicyScope) error
	RevokeCredentials(ctx context.Context, orchestratorTaskRef string) error
}

// TaskMonitor defines the state operations Recovery Manager needs from M4.
type TaskMonitor interface {
	StateTransition(ctx context.Context, taskID, newState, reason string) error
	UntrackTask(taskID string)
}

// ── Manager ───────────────────────────────────────────────────────────────────

// Manager is M5: Recovery Manager.
type Manager struct {
	cfg     *config.OrchestratorConfig
	memory  interfaces.MemoryClient
	gateway Gateway
	policy  PolicyEnforcer
	monitor TaskMonitor
	nodeID  string
}

// New creates a new Recovery Manager.
func New(
	cfg *config.OrchestratorConfig,
	memory interfaces.MemoryClient,
	gateway Gateway,
	policy PolicyEnforcer,
	monitor TaskMonitor,
) *Manager {
	return &Manager{
		cfg:     cfg,
		memory:  memory,
		gateway: gateway,
		policy:  policy,
		monitor: monitor,
		nodeID:  cfg.NodeID,
	}
}

// HandleRecovery is the entry point called by Task Monitor on every non-nominal task event.
// Routes to the appropriate action based on the recovery reason (§7 Flow 6, §8.4).
//
// Called by Monitor when:
//   - Agent reports RECOVERING state → RecoveryReasonAgentRecovering
//   - Agent reports TERMINATED state → RecoveryReasonAgentTerminated
//   - Task timeout fires            → RecoveryReasonTimeout
func (m *Manager) HandleRecovery(ctx context.Context, ts *types.TaskState, reason types.RecoveryReason) {
	logger := observability.LogFromContext(ctx)
	if ts == nil {
		logger.Warn("HandleRecovery called with nil TaskState — ignoring")
		return
	}

	logger.Info("recovery triggered",
		"task_id", ts.TaskID,
		"orchestrator_task_ref", ts.OrchestratorTaskRef,
		"reason", reason,
		"retry_count", ts.RetryCount,
	)

	switch reason {
	case types.RecoveryReasonTimeout:
		// Timeout is always terminal — no retry (§FR-SH-01).
		m.terminateTask(ctx, ts, types.ErrCodeTimedOut, types.StateTimedOut,
			fmt.Sprintf("task exceeded timeout_seconds after %d retries", ts.RetryCount))

	case types.RecoveryReasonAgentRecovering:
		// Agent is alive and self-healing — trust it. Do NOT re-dispatch a competing agent.
		// The existing per-task timeout goroutine in Monitor is the safety net:
		//   - If the agent recovers → it reports ACTIVE → Monitor transitions RUNNING (no M5 action needed)
		//   - If the agent gives up → it reports TERMINATED → M5 re-dispatches via AgentTerminated below
		//   - If the agent hangs forever → timeout fires → M5 terminates (TIMED_OUT, no retry)
		// Re-dispatching here would spin up a duplicate agent while the original is still alive.
		logger.Info("agent self-recovering — monitoring without intervening",
			"task_id", ts.TaskID,
			"orchestrator_task_ref", ts.OrchestratorTaskRef,
		)

	case types.RecoveryReasonAgentTerminated:
		// Agent is dead — act immediately. Check retry budget and re-dispatch.
		m.attemptRecovery(ctx, ts, reason)

	default:
		logger.Warn("unknown recovery reason — terminating",
			"reason", reason,
			"task_id", ts.TaskID,
		)
		m.terminateTask(ctx, ts, types.ErrCodeMaxRetriesExceeded, types.StateFailed,
			fmt.Sprintf("unknown recovery reason: %s", reason))
	}
}

// HandleComponentFailure is called by Memory Interface when all write retries are exhausted.
// Terminates the affected task if it can be identified (§4.1 M5).
func (m *Manager) HandleComponentFailure(ctx context.Context, payload types.OrchestratorMemoryWritePayload, writeErr error) {
	observability.LogFromContext(ctx).Error("CRITICAL: memory write failed after all retries",
		"task_id", payload.TaskID,
		"orchestrator_task_ref", payload.OrchestratorTaskRef,
		"error", writeErr,
	)
	// Best-effort: attempt credential revocation even without a full TaskState.
	if payload.OrchestratorTaskRef != "" {
		go m.scheduleRevocationRetry(ctx, payload.OrchestratorTaskRef)
	}
}

// ── Recovery Logic ────────────────────────────────────────────────────────────

// attemptRecovery checks the retry budget and either re-dispatches or terminates.
func (m *Manager) attemptRecovery(ctx context.Context, ts *types.TaskState, reason types.RecoveryReason) {
	ctx, recoverySpan := observability.StartSpan(ctx, "recovery_attempt")
	defer recoverySpan.End()
	logger := observability.LogFromContext(ctx)
	maxRetries := m.cfg.MaxTaskRetries
	if maxRetries == 0 {
		maxRetries = 3
	}

	// ── Check retry budget (§FR-SH-03) ────────────────────────────────────
	if ts.RetryCount >= maxRetries {
		logger.Warn("retry budget exhausted",
			"task_id", ts.TaskID,
			"retry_count", ts.RetryCount,
			"max_retries", maxRetries,
		)
		m.terminateTask(ctx, ts, types.ErrCodeMaxRetriesExceeded, types.StateFailed,
			fmt.Sprintf("max retries (%d) exceeded", maxRetries))
		return
	}

	// ── Read last task state from Memory (§FR-SH-02) ──────────────────────
	latestState, err := m.memory.ReadLatest(ts.TaskID, types.DataTypeTaskState)
	if err != nil {
		logger.Error("failed to read task state for recovery — terminating",
			"task_id", ts.TaskID,
			"error", err,
		)
		m.terminateTask(ctx, ts, types.ErrCodeStateRecoveryFailed, types.StateFailed,
			fmt.Sprintf("cannot read task state for recovery: %v", err))
		return
	}

	var recoveredState types.TaskState
	if err := json.Unmarshal(latestState.Payload, &recoveredState); err != nil {
		logger.Error("failed to decode task state for recovery — terminating",
			"task_id", ts.TaskID,
			"error", err,
		)
		m.terminateTask(ctx, ts, types.ErrCodeStateRecoveryFailed, types.StateFailed,
			fmt.Sprintf("cannot decode task state for recovery: %v", err))
		return
	}

	// ── Re-validate policy scope (§FR-SH-06, §13.2) ───────────────────────
	// Scope CANNOT expand. If Vault says it has changed, escalate to SCOPE_EXPIRED.
	if err := m.policy.VerifyScopeStillValid(ctx, recoveredState.PolicyScope); err != nil {
		logger.Error("policy scope expired during recovery — terminating",
			"task_id", ts.TaskID,
			"error", err,
		)
		m.terminateTask(ctx, ts, types.ErrCodeScopeExpired, types.StateFailed,
			fmt.Sprintf("policy scope expired during recovery: %v", err))
		return
	}

	// ── Increment retry count and persist recovery_event ──────────────────
	recoveredState.RetryCount++
	now := time.Now().UTC()

	recoveryEvent := types.RecoveryEvent{
		OrchestratorTaskRef: recoveredState.OrchestratorTaskRef,
		TaskID:              recoveredState.TaskID,
		AttemptNumber:       recoveredState.RetryCount,
		Reason:              string(reason),
		Timestamp:           now,
		NodeID:              m.nodeID,
	}
	eventPayload, err := json.Marshal(recoveryEvent)
	if err == nil {
		if writeErr := m.memory.Write(types.OrchestratorMemoryWritePayload{
			OrchestratorTaskRef: recoveredState.OrchestratorTaskRef,
			TaskID:              recoveredState.TaskID,
			DataType:            types.DataTypeRecoveryEvent,
			Timestamp:           now,
			Payload:             eventPayload,
		}); writeErr != nil {
			logger.Warn("failed to write recovery_event — continuing",
				"task_id", recoveredState.TaskID,
				"attempt", recoveredState.RetryCount,
				"error", writeErr,
			)
			// Non-fatal: continue with re-dispatch.
		}
	}

	// ── Transition state to RECOVERING ────────────────────────────────────
	if err := m.monitor.StateTransition(ctx, ts.TaskID, types.StateRecovering,
		fmt.Sprintf("recovery attempt %d: %s", recoveredState.RetryCount, reason)); err != nil {
		logger.Warn("failed to transition to RECOVERING — continuing",
			"task_id", ts.TaskID,
			"error", err,
		)
		// Continue — state may already be RECOVERING from monitor's earlier call.
	}

	// ── Re-dispatch task_spec with the same (immutable) policy_scope ───────
	spec := types.TaskSpec{
		OrchestratorTaskRef:  recoveredState.OrchestratorTaskRef,
		TaskID:               recoveredState.TaskID,
		UserID:               recoveredState.UserID,
		RequiredSkillDomains: recoveredState.RequiredSkillDomains,
		PolicyScope:          recoveredState.PolicyScope, // immutable — not expanded
		TimeoutSeconds:       timeoutSecondsRemaining(&recoveredState),
		Payload:              recoveredState.Payload,
		CallbackTopic:        recoveredState.CallbackTopic,
		UserContextID:        recoveredState.UserContextID,
		ProgressSummary:      fmt.Sprintf("Recovery attempt %d", recoveredState.RetryCount),
		TraceID:              recoveredState.TraceID,
	}

	if err := m.gateway.PublishTaskSpec(ctx, spec); err != nil {
		logger.Error("re-dispatch failed — terminating",
			"task_id", ts.TaskID,
			"attempt", recoveredState.RetryCount,
			"error", err,
		)
		m.terminateTask(ctx, ts, types.ErrCodeAgentsUnavailable, types.StateFailed,
			fmt.Sprintf("re-dispatch failed on attempt %d: %v", recoveredState.RetryCount, err))
		return
	}

	logger.Info("task re-dispatched",
		"task_id", recoveredState.TaskID,
		"orchestrator_task_ref", recoveredState.OrchestratorTaskRef,
		"attempt", recoveredState.RetryCount,
	)
}

// terminateTask performs the full terminal cleanup sequence (§17.3 terminateTask):
//  1. Revoke credentials via Policy Enforcer (non-optional, §FR-SH-04)
//  2. Publish agent_terminate to Agents Component via Gateway
//  3. Transition task state via Monitor (persists to Memory)
//  4. Notify User I/O Component with error via Gateway
//  5. Untrack task in Task Monitor
func (m *Manager) terminateTask(ctx context.Context, ts *types.TaskState, errorCode, terminalState, reason string) {
	logger := observability.LogFromContext(ctx)
	logger.Info("terminating task",
		"task_id", ts.TaskID,
		"orchestrator_task_ref", ts.OrchestratorTaskRef,
		"state", terminalState,
		"error_code", errorCode,
	)

	// Another path may have already finished the task (e.g. decomposition timeout vs global task timeout).
	if types.IsTerminalState(ts.State) {
		logger.Info("terminateTask skipped — task already terminal",
			"task_id", ts.TaskID,
			"current_state", ts.State,
		)
		return
	}

	// ── Step 1: Revoke credentials (NON-OPTIONAL, §FR-SH-04, §13.3) ───────
	if err := m.policy.RevokeCredentials(ctx, ts.OrchestratorTaskRef); err != nil {
		logger.Error("REVOCATION_FAILED — scheduling retry",
			"orchestrator_task_ref", ts.OrchestratorTaskRef,
			"error", err,
		)
		go m.scheduleRevocationRetry(ctx, ts.OrchestratorTaskRef)
		// DO NOT block termination — continue regardless.
	}

	// ── Step 2: Publish agent_terminate ───────────────────────────────────
	if err := m.gateway.PublishAgentTerminate(ctx, types.AgentTerminate{
		AgentID:             ts.AgentID,
		OrchestratorTaskRef: ts.OrchestratorTaskRef,
		Reason:              errorCode,
	}); err != nil {
		logger.Warn("failed to publish agent_terminate",
			"task_id", ts.TaskID,
			"error", err,
		)
		// Non-fatal: agent may already be gone; continue cleanup.
	}

	// ── Step 3: Transition state via Monitor (persists to Memory) ─────────
	if err := m.monitor.StateTransition(ctx, ts.TaskID, terminalState, reason); err != nil {
		logger.Warn("state transition failed",
			"terminal_state", terminalState,
			"task_id", ts.TaskID,
			"error", err,
		)
		// Non-fatal: best-effort state write.
	}

	// ── Step 4: Notify User I/O via Gateway ───────────────────────────────
	userMessage := userMessageForErrorCode(errorCode)
	if err := m.gateway.PublishError(ctx, ts.CallbackTopic, types.ErrorResponse{
		TaskID:      ts.TaskID,
		ErrorCode:   errorCode,
		UserMessage: userMessage,
	}); err != nil {
		logger.Warn("failed to publish error to User I/O",
			"task_id", ts.TaskID,
			"callback_topic", ts.CallbackTopic,
			"error", err,
		)
	}

	// ── Step 5: Untrack from Monitor ──────────────────────────────────────
	m.monitor.UntrackTask(ts.TaskID)

	logger.Info("task terminated",
		"task_id", ts.TaskID,
		"orchestrator_task_ref", ts.OrchestratorTaskRef,
		"final_state", terminalState,
	)
}

// scheduleRevocationRetry retries credential revocation with exponential backoff.
// Runs in a goroutine — never blocks the caller (§13.3).
// Max 5 attempts with initial backoff of 500ms doubling each attempt.
func (m *Manager) scheduleRevocationRetry(ctx context.Context, orchestratorTaskRef string) {
	logger := observability.LogFromContext(ctx)
	backoff := revocationInitialBackoff
	for attempt := 1; attempt <= revocationMaxAttempts; attempt++ {
		time.Sleep(backoff)
		if err := m.policy.RevokeCredentials(ctx, orchestratorTaskRef); err == nil {
			logger.Info("revocation retry succeeded",
				"orchestrator_task_ref", orchestratorTaskRef,
				"attempt", attempt,
			)
			return
		}
		logger.Warn("REVOCATION_FAILED — retrying",
			"orchestrator_task_ref", orchestratorTaskRef,
			"attempt", attempt,
			"max_attempts", revocationMaxAttempts,
			"next_backoff", backoff*2,
		)
		backoff *= 2
	}
	logger.Error("REVOCATION_FAILED_PERMANENT — all retry attempts exhausted",
		"orchestrator_task_ref", orchestratorTaskRef,
		"max_attempts", revocationMaxAttempts,
	)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// timeoutSecondsRemaining calculates how many seconds remain before the task's
// original timeout deadline. Returns a minimum of 30 seconds to avoid
// dispatching a task that would immediately time out.
func timeoutSecondsRemaining(ts *types.TaskState) int {
	if ts.TimeoutAt == nil {
		return 60 // safe default
	}
	remaining := int(time.Until(*ts.TimeoutAt).Seconds())
	if remaining < 30 {
		return 30
	}
	return remaining
}

// userMessageForErrorCode returns a human-readable message for each error code.
func userMessageForErrorCode(errorCode string) string {
	switch errorCode {
	case types.ErrCodeTimedOut:
		return "Your task exceeded its time limit and was terminated."
	case types.ErrCodeMaxRetriesExceeded:
		return "Your task could not be completed after the maximum number of recovery attempts."
	case types.ErrCodeScopeExpired:
		return "Your task's security credentials expired during recovery and could not be renewed."
	case types.ErrCodeAgentsUnavailable:
		return "No capable agent was available to resume the task after recovery."
	case types.ErrCodeStateRecoveryFailed:
		return "Task state could not be retrieved from storage for recovery."
	default:
		return "Your task encountered an unrecoverable error and was terminated."
	}
}
