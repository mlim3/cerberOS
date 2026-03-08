// Package monitor implements M4: Task Monitor.
//
// The Task Monitor is the SOLE AUTHORITY for task state transitions.
// It tracks every active task from dispatch to completion or failure,
// enforces timeouts, and rehydrates state on startup.
//
// Responsibilities (§4.1 M4):
//   - Maintain in-memory task state map (rehydrated from Memory Component on startup)
//   - Enforce per-task timeout: signal Recovery Manager when timeout_at is exceeded
//   - Subscribe to agent_status_update events from Agents Component via Gateway
//   - Detect RECOVERING/TERMINATED agent events and escalate to Recovery Manager
//   - Emit task_progress events when agents publish intermediate progress
//   - StateTransition() is the sole entry point for all state changes
//
// Startup sequence (§FR-SH-05):
//   RehydrateFromMemory() MUST complete before new tasks are accepted.
//   Target: < 10 seconds (§NFR-06).
package monitor

import (
	"sync"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// RecoveryManager defines the escalation operations M4 needs from M5.
type RecoveryManager interface {
	HandleRecovery(taskID string, retryCount int, policyScope types.PolicyScope)
	HandleTimeout(ts *types.TaskState)
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
//
// TODO Phase 5: implement
func (m *Monitor) RehydrateFromMemory() error {
	// TODO Phase 5:
	// 1. Read all task_state records where state != terminal
	// 2. Populate activeTasks map
	// 3. Spawn monitorTaskTimeout() goroutine for each
	// 4. Return error if duration exceeds 10s
	return nil
}

// TrackTask begins monitoring a newly dispatched task.
// Called by Task Dispatcher after successful dispatch (§4.1 M4).
//
// TODO Phase 5: implement — add to activeTasks, spawn monitorTaskTimeout()
func (m *Monitor) TrackTask(ts *types.TaskState) {
	// TODO Phase 5
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
//
// TODO Phase 5: implement with state machine validation
func (m *Monitor) StateTransition(taskID, newState, reason string) error {
	// TODO Phase 5
	return nil
}

// HandleAgentStatusUpdate processes an inbound agent_status_update from the Gateway.
// Routes to the correct action based on agent state (§4.1 M4).
//
// TODO Phase 5: implement routing:
//   - ACTIVE     → StateTransition(taskID, StateRunning, "")
//   - RECOVERING → signal Recovery Manager
//   - TERMINATED → signal Recovery Manager if task not already terminal
func (m *Monitor) HandleAgentStatusUpdate(update types.AgentStatusUpdate) error {
	// TODO Phase 5
	return nil
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
// TODO Phase 5: implement using time.After(remaining) pattern from §17.3
func (m *Monitor) monitorTaskTimeout(ts *types.TaskState) {
	// TODO Phase 5
}
