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
	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

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

// HandleRecovery is triggered when Task Monitor receives an RECOVERING agent_status_update.
// Implements the full recovery flow from §7 Flow 6 and §8.3.
//
// TODO Phase 6: implement:
//  1. Check retry_count < max_retries; if exhausted → TerminateTask(MAX_RETRIES_EXCEEDED)
//  2. Read last task_state snapshot from Memory Interface
//  3. Re-validate policy scope via Policy Enforcer (scope CANNOT expand)
//  4. Re-dispatch task_spec (same policy_scope) via Gateway
//  5. Write recovery_event to Memory Interface
func (m *Manager) HandleRecovery(taskID string, retryCount int, policyScope types.PolicyScope) {
	// TODO Phase 6
}

// HandleTimeout is triggered by Task Monitor when a task exceeds timeout_at (§FR-SH-01).
//
// TODO Phase 6: implement → TerminateTask(ts, TIMED_OUT)
func (m *Manager) HandleTimeout(ts *types.TaskState) {
	// TODO Phase 6
}

// TerminateTask performs the full terminal cleanup sequence (§17.3 terminateTask):
//  1. Revoke credentials via Policy Enforcer (non-optional)
//  2. Publish agent_terminate to Agents Component via Gateway
//  3. Persist final state to Memory Interface
//  4. Notify User I/O Component with error via Gateway
//  5. Untrack task in Task Monitor
//
// CRITICAL: Credential revocation must be attempted even if Vault is degraded.
// On revocation failure: log REVOCATION_FAILED, schedule retry, continue termination.
//
// TODO Phase 6: implement
func (m *Manager) TerminateTask(ts *types.TaskState, reason string) {
	// TODO Phase 6
}

// scheduleRevocationRetry queues a Vault credential revocation for retry
// with exponential backoff (max 5 attempts). Does not block caller (§13.3).
//
// TODO Phase 6: implement with goroutine + exponential backoff
func (m *Manager) scheduleRevocationRetry(orchestratorTaskRef string) {
	// TODO Phase 6
}
