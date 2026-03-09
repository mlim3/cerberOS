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
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// revocationMaxAttempts is the max number of Vault revocation retries (§13.3).
const revocationMaxAttempts = 5

// revocationInitialBackoff is the starting backoff for revocation retries.
const revocationInitialBackoff = 500 * time.Millisecond

// ── Internal Interface Definitions ───────────────────────────────────────────

// Gateway defines the outbound operations Recovery Manager needs from M1.
type Gateway interface {
	PublishAgentTerminate(terminate types.AgentTerminate) error
	PublishTaskCancel(cancel types.TaskCancel) error
	PublishError(callbackTopic string, resp types.ErrorResponse) error
	PublishTaskSpec(spec types.TaskSpec) error
}

// PolicyEnforcer defines the policy operations Recovery Manager needs from M3.
type PolicyEnforcer interface {
	VerifyScopeStillValid(scope types.PolicyScope) error
	RevokeCredentials(orchestratorTaskRef string) error
}

// TaskMonitor defines the state operations Recovery Manager needs from M4.
type TaskMonitor interface {
	StateTransition(taskID, newState, reason string) error
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
	logger  *log.Logger
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
		logger:  log.New(os.Stdout, "[recovery] ", log.LstdFlags|log.LUTC),
	}
}

// HandleRecovery is the entry point called by Task Monitor on every non-nominal task event.
// Routes to the appropriate action based on the recovery reason (§7 Flow 6, §8.4).
//
// Called by Monitor when:
//   - Agent reports RECOVERING state → RecoveryReasonAgentRecovering
//   - Agent reports TERMINATED state → RecoveryReasonAgentTerminated
//   - Task timeout fires            → RecoveryReasonTimeout
func (m *Manager) HandleRecovery(ts *types.TaskState, reason types.RecoveryReason) {
	if ts == nil {
		m.logger.Printf("HandleRecovery called with nil TaskState — ignoring")
		return
	}

	m.logger.Printf(
		"recovery triggered: task_id=%s orchestrator_task_ref=%s reason=%s retry_count=%d",
		ts.TaskID, ts.OrchestratorTaskRef, reason, ts.RetryCount,
	)

	switch reason {
	case types.RecoveryReasonTimeout:
		// Timeout is always terminal — no retry (§FR-SH-01).
		m.terminateTask(ts, types.ErrCodeTimedOut, types.StateTimedOut,
			fmt.Sprintf("task exceeded timeout_seconds after %d retries", ts.RetryCount))

	case types.RecoveryReasonAgentRecovering, types.RecoveryReasonAgentTerminated:
		m.attemptRecovery(ts, reason)

	default:
		m.logger.Printf("unknown recovery reason %q for task_id=%s — terminating", reason, ts.TaskID)
		m.terminateTask(ts, types.ErrCodeMaxRetriesExceeded, types.StateFailed,
			fmt.Sprintf("unknown recovery reason: %s", reason))
	}
}

// HandleComponentFailure is called by Memory Interface when all write retries are exhausted.
// Terminates the affected task if it can be identified (§4.1 M5).
func (m *Manager) HandleComponentFailure(payload types.OrchestratorMemoryWritePayload, writeErr error) {
	m.logger.Printf(
		"CRITICAL: memory write failed after all retries: task_id=%s orchestrator_task_ref=%s error=%v",
		payload.TaskID, payload.OrchestratorTaskRef, writeErr,
	)
	// Best-effort: attempt credential revocation even without a full TaskState.
	if payload.OrchestratorTaskRef != "" {
		go m.scheduleRevocationRetry(payload.OrchestratorTaskRef)
	}
}

// ── Recovery Logic ────────────────────────────────────────────────────────────

// attemptRecovery checks the retry budget and either re-dispatches or terminates.
func (m *Manager) attemptRecovery(ts *types.TaskState, reason types.RecoveryReason) {
	maxRetries := m.cfg.MaxTaskRetries
	if maxRetries == 0 {
		maxRetries = 3
	}

	// ── Check retry budget (§FR-SH-03) ────────────────────────────────────
	if ts.RetryCount >= maxRetries {
		m.logger.Printf(
			"retry budget exhausted: task_id=%s retry_count=%d max_retries=%d",
			ts.TaskID, ts.RetryCount, maxRetries,
		)
		m.terminateTask(ts, types.ErrCodeMaxRetriesExceeded, types.StateFailed,
			fmt.Sprintf("max retries (%d) exceeded", maxRetries))
		return
	}

	// ── Read last task state from Memory (§FR-SH-02) ──────────────────────
	latestState, err := m.memory.ReadLatest(ts.TaskID, types.DataTypeTaskState)
	if err != nil {
		m.logger.Printf(
			"failed to read task state for recovery: task_id=%s error=%v — terminating",
			ts.TaskID, err,
		)
		m.terminateTask(ts, types.ErrCodeStateRecoveryFailed, types.StateFailed,
			fmt.Sprintf("cannot read task state for recovery: %v", err))
		return
	}

	var recoveredState types.TaskState
	if err := json.Unmarshal(latestState.Payload, &recoveredState); err != nil {
		m.logger.Printf(
			"failed to decode task state for recovery: task_id=%s error=%v — terminating",
			ts.TaskID, err,
		)
		m.terminateTask(ts, types.ErrCodeStateRecoveryFailed, types.StateFailed,
			fmt.Sprintf("cannot decode task state for recovery: %v", err))
		return
	}

	// ── Re-validate policy scope (§FR-SH-06, §13.2) ───────────────────────
	// Scope CANNOT expand. If Vault says it has changed, escalate to SCOPE_EXPIRED.
	if err := m.policy.VerifyScopeStillValid(recoveredState.PolicyScope); err != nil {
		m.logger.Printf(
			"policy scope expired during recovery: task_id=%s error=%v — terminating",
			ts.TaskID, err,
		)
		m.terminateTask(ts, types.ErrCodeScopeExpired, types.StateFailed,
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
			m.logger.Printf(
				"failed to write recovery_event: task_id=%s attempt=%d error=%v",
				recoveredState.TaskID, recoveredState.RetryCount, writeErr,
			)
			// Non-fatal: continue with re-dispatch.
		}
	}

	// ── Transition state to RECOVERING ────────────────────────────────────
	if err := m.monitor.StateTransition(ts.TaskID, types.StateRecovering,
		fmt.Sprintf("recovery attempt %d: %s", recoveredState.RetryCount, reason)); err != nil {
		m.logger.Printf(
			"failed to transition to RECOVERING: task_id=%s error=%v",
			ts.TaskID, err,
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
	}

	if err := m.gateway.PublishTaskSpec(spec); err != nil {
		m.logger.Printf(
			"re-dispatch failed: task_id=%s attempt=%d error=%v — terminating",
			ts.TaskID, recoveredState.RetryCount, err,
		)
		m.terminateTask(ts, types.ErrCodeAgentsUnavailable, types.StateFailed,
			fmt.Sprintf("re-dispatch failed on attempt %d: %v", recoveredState.RetryCount, err))
		return
	}

	m.logger.Printf(
		"task re-dispatched: task_id=%s orchestrator_task_ref=%s attempt=%d",
		recoveredState.TaskID, recoveredState.OrchestratorTaskRef, recoveredState.RetryCount,
	)
}

// terminateTask performs the full terminal cleanup sequence (§17.3 terminateTask):
//  1. Revoke credentials via Policy Enforcer (non-optional, §FR-SH-04)
//  2. Publish agent_terminate to Agents Component via Gateway
//  3. Transition task state via Monitor (persists to Memory)
//  4. Notify User I/O Component with error via Gateway
//  5. Untrack task in Task Monitor
func (m *Manager) terminateTask(ts *types.TaskState, errorCode, terminalState, reason string) {
	m.logger.Printf(
		"terminating task: task_id=%s orchestrator_task_ref=%s state=%s error_code=%s",
		ts.TaskID, ts.OrchestratorTaskRef, terminalState, errorCode,
	)

	// ── Step 1: Revoke credentials (NON-OPTIONAL, §FR-SH-04, §13.3) ───────
	if err := m.policy.RevokeCredentials(ts.OrchestratorTaskRef); err != nil {
		m.logger.Printf(
			"REVOCATION_FAILED: orchestrator_task_ref=%s error=%v — scheduling retry",
			ts.OrchestratorTaskRef, err,
		)
		go m.scheduleRevocationRetry(ts.OrchestratorTaskRef)
		// DO NOT block termination — continue regardless.
	}

	// ── Step 2: Publish agent_terminate ───────────────────────────────────
	if err := m.gateway.PublishAgentTerminate(types.AgentTerminate{
		AgentID:             ts.AgentID,
		OrchestratorTaskRef: ts.OrchestratorTaskRef,
		Reason:              errorCode,
	}); err != nil {
		m.logger.Printf(
			"failed to publish agent_terminate: task_id=%s error=%v",
			ts.TaskID, err,
		)
		// Non-fatal: agent may already be gone; continue cleanup.
	}

	// ── Step 3: Transition state via Monitor (persists to Memory) ─────────
	if err := m.monitor.StateTransition(ts.TaskID, terminalState, reason); err != nil {
		m.logger.Printf(
			"state transition to %s failed: task_id=%s error=%v",
			terminalState, ts.TaskID, err,
		)
		// Non-fatal: best-effort state write.
	}

	// ── Step 4: Notify User I/O via Gateway ───────────────────────────────
	userMessage := userMessageForErrorCode(errorCode)
	if err := m.gateway.PublishError(ts.CallbackTopic, types.ErrorResponse{
		TaskID:      ts.TaskID,
		ErrorCode:   errorCode,
		UserMessage: userMessage,
	}); err != nil {
		m.logger.Printf(
			"failed to publish error to User I/O: task_id=%s callback_topic=%s error=%v",
			ts.TaskID, ts.CallbackTopic, err,
		)
	}

	// ── Step 5: Untrack from Monitor ──────────────────────────────────────
	m.monitor.UntrackTask(ts.TaskID)

	m.logger.Printf(
		"task terminated: task_id=%s orchestrator_task_ref=%s final_state=%s",
		ts.TaskID, ts.OrchestratorTaskRef, terminalState,
	)
}

// scheduleRevocationRetry retries credential revocation with exponential backoff.
// Runs in a goroutine — never blocks the caller (§13.3).
// Max 5 attempts with initial backoff of 500ms doubling each attempt.
func (m *Manager) scheduleRevocationRetry(orchestratorTaskRef string) {
	backoff := revocationInitialBackoff
	for attempt := 1; attempt <= revocationMaxAttempts; attempt++ {
		time.Sleep(backoff)
		if err := m.policy.RevokeCredentials(orchestratorTaskRef); err == nil {
			m.logger.Printf(
				"revocation retry succeeded: orchestrator_task_ref=%s attempt=%d",
				orchestratorTaskRef, attempt,
			)
			return
		}
		m.logger.Printf(
			"REVOCATION_FAILED: orchestrator_task_ref=%s attempt=%d/%d — retrying in %s",
			orchestratorTaskRef, attempt, revocationMaxAttempts, backoff*2,
		)
		backoff *= 2
	}
	m.logger.Printf(
		"REVOCATION_FAILED_PERMANENT: orchestrator_task_ref=%s all %d retry attempts exhausted",
		orchestratorTaskRef, revocationMaxAttempts,
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
