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
//   Persist task state to Memory BEFORE dispatching to Agents Component.
//   This prevents orphaned agents if Memory fails post-dispatch.
package dispatcher

import (
	"sync"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// Dispatcher is M2: Task Dispatcher.
type Dispatcher struct {
	mu          sync.RWMutex
	activeTasks sync.Map // map[taskID]*types.TaskState — goroutine-safe

	cfg     *config.OrchestratorConfig
	memory  interfaces.MemoryClient
	vault   interfaces.VaultClient
	gateway Gateway // internal interface to M1
	policy  PolicyEnforcer // internal interface to M3
	monitor TaskMonitor    // internal interface to M4

	// Metrics counters
	tasksReceived   int64
	tasksCompleted  int64
	tasksFailed     int64
	policyViolations int64
	queueDepth      int64
}

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
	}
}

// HandleInboundTask is the entry point for all user_task messages from the Gateway.
// Implements the full validation → dedup → policy → dispatch pipeline (§7, §17.3).
//
// TODO Phase 4: implement full pipeline:
//   1. Schema validation
//   2. Deduplication check
//   3. Generate orchestrator_task_ref
//   4. Policy validation via Policy Enforcer
//   5. Capability query to Agents Component
//   6. Persist state to Memory (BEFORE dispatch)
//   7. Dispatch task_spec to Agents Component
//   8. Confirm task_accepted to User I/O
func (d *Dispatcher) HandleInboundTask(task types.UserTask) error {
	// TODO Phase 4
	return nil
}

// HandleTaskResult processes an inbound task_result from the Agents Component.
// Writes COMPLETED state to Memory, delivers result to callback_topic,
// and triggers credential revocation (§7 Flow 5).
//
// TODO Phase 4: implement
func (d *Dispatcher) HandleTaskResult(result types.TaskResult) error {
	// TODO Phase 4
	return nil
}

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
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.tasksReceived, d.tasksCompleted, d.tasksFailed, d.policyViolations, d.queueDepth
}

// validateSchema checks all required fields on a UserTask (§5.1, §FR-TRK-02, §FR-TRK-04).
// Returns a descriptive error if validation fails. Returns nil if valid.
//
// TODO Phase 4: implement full validation rules:
//   - task_id: required, valid UUID
//   - user_id: required, non-empty
//   - required_skill_domains: required, min 1 item, known domain names only
//   - priority: 1–10
//   - timeout_seconds: 30–86400
//   - payload: required, max 1MB serialized
//   - callback_topic: required, valid NATS topic format
func validateSchema(task types.UserTask) error {
	// TODO Phase 4
	return nil
}
