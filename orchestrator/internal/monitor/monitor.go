package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// RecoveryManager defines the escalation operations M4 needs from M5.
type RecoveryManager interface {
	HandleRecovery(ctx context.Context, ts *types.TaskState, reason types.RecoveryReason)
}

// Monitor is M4: Task Monitor.
type Monitor struct {
	mu          sync.RWMutex
	activeTasks sync.Map // map[taskID]*types.TaskState — goroutine-safe

	cfg      *config.OrchestratorConfig
	memory   interfaces.MemoryClient
	recovery RecoveryManager
	nodeID   string
}

// New creates a new Task Monitor.
func New(cfg *config.OrchestratorConfig, memory interfaces.MemoryClient, recovery RecoveryManager) *Monitor {
	return &Monitor{
		cfg:      cfg,
		memory:   memory,
		recovery: recovery,
		nodeID:   cfg.NodeID,
	}
}

// RehydrateFromMemory loads all non-terminal tasks from the Memory Component
// on startup and resumes timeout tracking for each (§FR-SH-05).
//
// CRITICAL: Must complete before the Orchestrator accepts new tasks.
// Returns error if rehydration takes longer than 10 seconds (§NFR-06).
func (m *Monitor) RehydrateFromMemory() error {
	start := time.Now()
	log := observability.LogFromContext(observability.WithModule(context.Background(), "task_monitor"))
	log.Info("task monitor rehydration started")

	records, err := m.memory.Read(types.MemoryQuery{
		DataType: types.DataTypeTaskState,
		Filter:   map[string]string{"state": "not_terminal"},
	})
	if err != nil {
		return fmt.Errorf("rehydrate task states: %w", err)
	}

	latestByTask := make(map[string]*types.TaskState)
	for _, record := range records {
		var ts types.TaskState
		if err := json.Unmarshal(record.Payload, &ts); err != nil {
			return fmt.Errorf("decode rehydrated task state for task_id=%s: %w", record.TaskID, err)
		}

		existing, ok := latestByTask[ts.TaskID]
		if !ok || isStateNewer(&ts, existing) {
			tsCopy := ts
			latestByTask[ts.TaskID] = &tsCopy
		}
	}

	for _, ts := range latestByTask {
		m.activeTasks.Store(ts.TaskID, ts)
		go m.monitorTaskTimeout(ts)
	}

	if time.Since(start) > 10*time.Second {
		return fmt.Errorf("rehydration exceeded 10 second startup budget")
	}
	log.Info("task monitor rehydration complete", "tasks_restored", len(latestByTask), "elapsed_ms", time.Since(start).Milliseconds())
	return nil
}

// TrackTask begins monitoring a newly dispatched task.
// Called by Task Dispatcher after successful dispatch (§4.1 M4).
func (m *Monitor) TrackTask(ts *types.TaskState) {
	if ts == nil {
		return
	}
	m.activeTasks.Store(ts.TaskID, ts)
	observability.LogFromContext(taskCtx(ts, "task_monitor")).Info("task tracking started",
		"state", ts.State,
	)
	go m.monitorTaskTimeout(ts)
}

// UntrackTask removes a task from the active monitoring map.
// Called by Recovery Manager after terminal outcome.
//
// TODO Phase 5: implement
func (m *Monitor) UntrackTask(taskID string) {
	m.activeTasks.Delete(taskID)
}

// StateTransition is the SOLE authority for task state changes.
// Validates the transition is legal per the state machine (§9),
// updates the task record, appends to state_history, and
// persists the new state to Memory Component.
func (m *Monitor) StateTransition(_ context.Context, taskID, newState, reason string) error {
	tsRaw, ok := m.activeTasks.Load(taskID)
	if !ok {
		return fmt.Errorf("task %s is not actively tracked", taskID)
	}

	ts, ok := tsRaw.(*types.TaskState)
	if !ok {
		return fmt.Errorf("tracked task %s has invalid type", taskID)
	}

	if !isValidTransition(ts.State, newState) {
		return fmt.Errorf("invalid state transition: %s -> %s", ts.State, newState)
	}

	fromState := ts.State
	now := time.Now().UTC()
	ts.State = newState
	if types.IsTerminalState(newState) {
		ts.CompletedAt = &now
		switch newState {
		case types.StateTimedOut:
			ts.ErrorCode = types.ErrCodeTimedOut
		case types.StateFailed:
			if ts.ErrorCode == "" {
				ts.ErrorCode = types.ErrCodeProvisioningFailed
			}
		case types.StatePolicyViolation:
			ts.ErrorCode = types.ErrCodePolicyViolation
		case types.StateDeliveryFailed:
			ts.ErrorCode = types.ErrCodeAgentsUnavailable
		}
	}

	ts.StateHistory = append(ts.StateHistory, types.StateEvent{
		State:     newState,
		Timestamp: now,
		Reason:    reason,
		NodeID:    m.nodeID,
	})

	payload, err := json.Marshal(ts)
	if err != nil {
		return fmt.Errorf("marshal task state transition for task %s: %w", taskID, err)
	}
	if err := m.memory.Write(types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: ts.OrchestratorTaskRef,
		TaskID:              ts.TaskID,
		DataType:            types.DataTypeTaskState,
		Timestamp:           now,
		Payload:             payload,
	}); err != nil {
		return fmt.Errorf("persist task state transition for task %s: %w", taskID, err)
	}

	if types.IsTerminalState(newState) {
		m.activeTasks.Delete(taskID)
	}
	observability.LogFromContext(taskCtx(ts, "task_monitor")).Info("task state transitioned",
		"from_state", fromState,
		"to_state", newState,
		"reason", reason,
	)
	return nil
}

// HandleAgentStatusUpdate processes an inbound agent_status_update from the Gateway.
// Routes to the correct action based on agent state (§4.1 M4).
func (m *Monitor) HandleAgentStatusUpdate(ctx context.Context, update types.AgentStatusUpdate) error {
	switch update.State {
	case types.AgentStateActive:
		return m.StateTransition(ctx, update.TaskID, types.StateRunning, "")

	case types.AgentStateRecovering:
		if err := m.StateTransition(ctx, update.TaskID, types.StateRecovering, update.Reason); err != nil {
			return err
		}
		if ts := m.getTask(update.TaskID); ts != nil {
			m.recovery.HandleRecovery(ctx, ts, types.RecoveryReasonAgentRecovering)
		}
		return nil

	case types.AgentStateTerminated:
		if ts := m.getTask(update.TaskID); ts != nil && !types.IsTerminalState(ts.State) {
			m.recovery.HandleRecovery(ctx, ts, types.RecoveryReasonAgentTerminated)
		}
		return nil

	default:
		return fmt.Errorf("unknown agent state: %s", update.State)
	}
}

// GetActiveTaskCount returns the number of tasks currently in non-terminal states.
// Used by the health endpoint (§12.2).
func (m *Monitor) GetActiveTaskCount() int {
	count := 0
	m.activeTasks.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// monitorTaskTimeout enforces the per-task timeout deadline (§FR-SH-01, §17.3).
// Runs as a goroutine per active task. Signals Recovery Manager on timeout.
//
// The task-level TimeoutAt is a runaway backstop, not a phase timer. Each
// pipeline phase (planner LLM, user approval, per-subtask execution) has
// its own dedicated timeout. The monitor therefore:
//
//   - Re-reads the task on fire so the latest TimeoutAt / State wins
//     (TimeoutAt may be extended while the task is alive).
//   - Skips termination when the task is in AWAITING_APPROVAL — that phase
//     is governed by PlanApprovalTimeoutSeconds in the dispatcher; the
//     wallclock must not double-count user review time or the user sees
//     "Your task exceeded its time limit" while staring at the Approve
//     button. Reschedules a short re-check so a post-approval overshoot
//     is still caught.
//   - Terminates only when TimeoutAt is in the past AND the task is in
//     an active (non-terminal, non-approval) state.
func (m *Monitor) monitorTaskTimeout(ts *types.TaskState) {
	if ts == nil || ts.TimeoutAt == nil {
		return
	}

	ctx := taskCtx(ts, "task_monitor")
	const approvalRecheckInterval = 30 * time.Second

	for {
		current := m.getTask(ts.TaskID)
		if current == nil || types.IsTerminalState(current.State) {
			return
		}
		if current.TimeoutAt == nil {
			return
		}

		remaining := time.Until(*current.TimeoutAt)
		if remaining > 0 {
			time.Sleep(remaining)
			continue
		}

		// Deadline passed. Give AWAITING_APPROVAL a reprieve — the approval
		// timeout goroutine in the dispatcher is the authoritative clock
		// for that phase. Re-check periodically so we still enforce the
		// backstop once execution resumes.
		if current.State == types.StateAwaitingApproval {
			time.Sleep(approvalRecheckInterval)
			continue
		}

		observability.LogFromContext(ctx).Warn("task timeout elapsed",
			"state", current.State,
			"timeout_at", current.TimeoutAt.Format(time.RFC3339Nano),
		)
		m.recovery.HandleRecovery(ctx, current, types.RecoveryReasonTimeout)
		return
	}
}

// taskCtx builds a context with observability IDs reconstructed from TaskState.
// Used in goroutines that fire without an incoming request context.
func taskCtx(ts *types.TaskState, module string) context.Context {
	ctx := context.Background()
	if ts.TraceID != "" {
		ctx = observability.WithTraceID(ctx, ts.TraceID)
	}
	ctx = observability.WithTaskID(ctx, ts.TaskID)
	ctx = observability.WithConversationID(ctx, ts.ConversationID)
	ctx = observability.WithModule(ctx, module)
	return ctx
}

func (m *Monitor) getTask(taskID string) *types.TaskState {
	tsRaw, ok := m.activeTasks.Load(taskID)
	if !ok {
		return nil
	}
	ts, ok := tsRaw.(*types.TaskState)
	if !ok {
		return nil
	}
	return ts
}

func isValidTransition(currentState, newState string) bool {
	if currentState == newState {
		return true
	}

	switch currentState {
	case types.StateDispatchPending:
		return newState == types.StateDispatched || newState == types.StateDeliveryFailed
	case types.StateDispatched:
		return newState == types.StateRunning || newState == types.StateRecovering || types.IsTerminalState(newState)
	case types.StateRunning:
		return newState == types.StateRecovering || types.IsTerminalState(newState)
	case types.StateRecovering:
		return newState == types.StateRunning || types.IsTerminalState(newState)
	case types.StateReceived, types.StatePolicyCheck:
		return newState == types.StateDispatchPending || types.IsTerminalState(newState)
	default:
		return false
	}
}

func isStateNewer(a, b *types.TaskState) bool {
	aTime := latestStateTimestamp(a)
	bTime := latestStateTimestamp(b)
	return aTime.After(bTime)
}

func latestStateTimestamp(ts *types.TaskState) time.Time {
	if ts == nil {
		return time.Time{}
	}
	if len(ts.StateHistory) > 0 {
		return ts.StateHistory[len(ts.StateHistory)-1].Timestamp
	}
	if ts.CompletedAt != nil {
		return *ts.CompletedAt
	}
	if ts.DispatchedAt != nil {
		return *ts.DispatchedAt
	}
	return time.Time{}
}
