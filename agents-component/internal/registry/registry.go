// Package registry is M3 — the Agent Registry. It maintains an in-memory catalog
// of all known agents and their current state. Persistence is delegated to the
// Memory Component via the Memory Interface.
package registry

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cerberOS/agents-component/internal/memory"
	"github.com/cerberOS/agents-component/pkg/types"
)

// DataTypeAgentState is the MemoryWrite.DataType used for all agent catalog writes.
// The Memory Component uses this to route and filter records.
const DataTypeAgentState = "agent_state"

// Agent state constants — the seven states of the lifecycle state machine (EDD §6.2).
const (
	StatePending    = "pending"    // Requested; VM not yet configured.
	StateSpawning   = "spawning"   // MicroVM being configured and launched.
	StateActive     = "active"     // Running and executing a task.
	StateIdle       = "idle"       // Task complete; VM running but unassigned.
	StateSuspended  = "suspended"  // State preserved; VM paused to free resources.
	StateRecovering = "recovering" // Crashed; Lifecycle Manager attempting recovery.
	StateTerminated = "terminated" // Permanently removed from service (terminal).
)

// validTransitions defines the allowed target states for each source state.
// Any transition not listed here is invalid and will be rejected.
var validTransitions = map[string][]string{
	StatePending:    {StateSpawning, StateTerminated},
	StateSpawning:   {StateActive, StateTerminated},
	StateActive:     {StateIdle, StateRecovering},
	StateIdle:       {StateActive, StateSuspended, StateTerminated},
	StateSuspended:  {StateActive, StateTerminated},
	StateRecovering: {StateActive, StateTerminated},
	StateTerminated: {}, // terminal — no further transitions
}

// Registry is the interface for agent catalog operations.
type Registry interface {
	// Register adds a new agent record in the PENDING state. Returns an error
	// if the agent already exists or AgentID is empty.
	Register(agent *types.AgentRecord) error

	// Get retrieves an agent by ID.
	Get(agentID string) (*types.AgentRecord, error)

	// FindBySkills returns agents whose SkillDomains satisfy all required domains.
	// Terminated agents are excluded.
	FindBySkills(domains []string) ([]*types.AgentRecord, error)

	// UpdateState transitions an agent to the target state, appends a StateEvent
	// to its history, and stamps UpdatedAt. Returns an error if the transition is
	// not permitted by the state machine.
	UpdateState(agentID, state, reason string) error

	// AssignTask links a task to an agent and transitions it to ACTIVE. The agent
	// must be in a state that permits a transition to ACTIVE (idle or suspended).
	AssignTask(agentID, taskID string) error

	// SetVMID updates the VMID for an agent. Used on respawn when the same
	// agent_id is paired with a freshly-created microVM (new vm_id).
	SetVMID(agentID, vmID string) error

	// Deregister removes an agent from the catalog.
	Deregister(agentID string) error

	// List returns all registered agents.
	List() []*types.AgentRecord
}

// inMemoryRegistry is the default Registry implementation.
type inMemoryRegistry struct {
	mu     sync.RWMutex
	agents map[string]*types.AgentRecord
	mem    memory.Client // nil when persistence is disabled (unit-test path)
}

// New returns a ready-to-use in-memory Registry with no persistence.
// Use NewPersistent when you need startup recovery via the Memory Interface.
func New() Registry {
	return &inMemoryRegistry{
		agents: make(map[string]*types.AgentRecord),
	}
}

// NewPersistent returns a Registry that writes every state mutation to the Memory
// Interface. On construction it publishes a state.read.request filtered to
// data_type=agent_state and re-hydrates the catalog from the response — surviving
// component restarts transparently. An empty response (first boot) is handled
// gracefully: the returned registry starts with an empty catalog.
func NewPersistent(mem memory.Client) (Registry, error) {
	r := &inMemoryRegistry{
		agents: make(map[string]*types.AgentRecord),
		mem:    mem,
	}
	if err := r.recoverFromMemory(); err != nil {
		return nil, fmt.Errorf("registry: startup recovery: %w", err)
	}
	return r, nil
}

// recoverFromMemory reads all agent_state records and re-hydrates the catalog.
// For each agentID, only the record with the latest UpdatedAt is used (subsequent
// writes to the same agent overwrite older snapshots). Terminated agents are not
// restored — they have no work to resume. An empty result set is handled as a
// normal first-boot condition and produces no error.
func (r *inMemoryRegistry) recoverFromMemory() error {
	records, err := r.mem.ReadAllByType(DataTypeAgentState)
	if err != nil {
		return fmt.Errorf("read %q records: %w", DataTypeAgentState, err)
	}
	if len(records) == 0 {
		slog.Info("registry: no persisted agent state found — starting fresh")
		return nil
	}

	// For each agentID, keep only the record with the latest UpdatedAt.
	latest := make(map[string]*types.AgentRecord, len(records))
	for _, rec := range records {
		var agent types.AgentRecord
		b, err := json.Marshal(rec.Payload)
		if err != nil {
			slog.Warn("registry: recovery: cannot marshal payload",
				"agent_id", rec.AgentID, "error", err)
			continue
		}
		if err := json.Unmarshal(b, &agent); err != nil {
			slog.Warn("registry: recovery: cannot unmarshal AgentRecord",
				"agent_id", rec.AgentID, "error", err)
			continue
		}
		if agent.AgentID == "" {
			continue
		}
		if existing, ok := latest[agent.AgentID]; !ok || agent.UpdatedAt.After(existing.UpdatedAt) {
			cp := agent
			latest[agent.AgentID] = &cp
		}
	}

	var restored int
	for _, agent := range latest {
		if agent.State == StateTerminated {
			continue // terminal — nothing to resume
		}
		r.agents[agent.AgentID] = agent
		restored++
	}

	slog.Info("registry: startup recovery complete", "agents_restored", restored)
	return nil
}

func (r *inMemoryRegistry) Register(agent *types.AgentRecord) error {
	if agent == nil || agent.AgentID == "" {
		return fmt.Errorf("registry: agent must have a non-empty AgentID")
	}

	r.mu.Lock()
	if _, exists := r.agents[agent.AgentID]; exists {
		r.mu.Unlock()
		return fmt.Errorf("registry: agent %q already registered", agent.AgentID)
	}
	now := time.Now().UTC()
	agent.State = StatePending
	agent.CreatedAt = now
	agent.UpdatedAt = now
	agent.StateHistory = []types.StateEvent{
		{State: StatePending, Timestamp: now, Reason: "registered"},
	}
	r.agents[agent.AgentID] = agent
	snapshot := copyAgent(agent)
	r.mu.Unlock()

	r.persistAgent(snapshot)
	return nil
}

func (r *inMemoryRegistry) Get(agentID string) (*types.AgentRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	a, ok := r.agents[agentID]
	if !ok {
		return nil, fmt.Errorf("registry: agent %q not found", agentID)
	}
	return copyAgent(a), nil
}

func (r *inMemoryRegistry) FindBySkills(domains []string) ([]*types.AgentRecord, error) {
	if len(domains) == 0 {
		return nil, fmt.Errorf("registry: at least one domain is required")
	}
	needed := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		needed[d] = struct{}{}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var matches []*types.AgentRecord
	for _, a := range r.agents {
		if a.State == StateTerminated {
			continue
		}
		if hasAllDomains(a.SkillDomains, needed) {
			matches = append(matches, copyAgent(a))
		}
	}
	return matches, nil
}

func (r *inMemoryRegistry) UpdateState(agentID, state, reason string) error {
	r.mu.Lock()
	a, ok := r.agents[agentID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("registry: agent %q not found", agentID)
	}
	if err := validateTransition(a.State, state); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("registry: agent %q: %w", agentID, err)
	}
	now := time.Now().UTC()
	fromState := a.State
	a.State = state
	a.UpdatedAt = now

	// Maintain FailureCount automatically:
	//   → recovering  increments the counter (each crash detection)
	//   → idle        resets to 0 (successful task completion)
	switch state {
	case StateRecovering:
		a.FailureCount++
	case StateIdle:
		a.FailureCount = 0
	}

	a.StateHistory = append(a.StateHistory, types.StateEvent{
		State:     state,
		Timestamp: now,
		Reason:    reason,
	})
	snapshot := copyAgent(a)
	r.mu.Unlock()

	slog.Info("agent.state.transition",
		"agent_id", agentID,
		"from_state", fromState,
		"to_state", state,
		"reason", reason,
	)
	r.persistAgent(snapshot)
	return nil
}

func (r *inMemoryRegistry) SetVMID(agentID, vmID string) error {
	r.mu.Lock()
	a, ok := r.agents[agentID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("registry: agent %q not found", agentID)
	}
	a.VMID = vmID
	a.UpdatedAt = time.Now().UTC()
	snapshot := copyAgent(a)
	r.mu.Unlock()

	r.persistAgent(snapshot)
	return nil
}

func (r *inMemoryRegistry) AssignTask(agentID, taskID string) error {
	r.mu.Lock()
	a, ok := r.agents[agentID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("registry: agent %q not found", agentID)
	}
	if err := validateTransition(a.State, StateActive); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("registry: AssignTask for agent %q: %w", agentID, err)
	}
	now := time.Now().UTC()
	a.AssignedTask = taskID
	a.State = StateActive
	a.UpdatedAt = now
	a.StateHistory = append(a.StateHistory, types.StateEvent{
		State:     StateActive,
		Timestamp: now,
		Reason:    "task assigned: " + taskID,
	})
	snapshot := copyAgent(a)
	r.mu.Unlock()

	r.persistAgent(snapshot)
	return nil
}

func (r *inMemoryRegistry) Deregister(agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.agents[agentID]; !ok {
		return fmt.Errorf("registry: agent %q not found", agentID)
	}
	delete(r.agents, agentID)
	return nil
}

func (r *inMemoryRegistry) List() []*types.AgentRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*types.AgentRecord, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, copyAgent(a))
	}
	return out
}

// persistAgent writes a snapshot of the agent record to the Memory Interface.
// Writes are fire-and-forget (RequireAck: false) to keep the mutation hot path
// fast. A failed persist is logged as a warning but does not fail the mutation —
// the in-memory catalog remains authoritative during the component's lifetime.
func (r *inMemoryRegistry) persistAgent(agent *types.AgentRecord) {
	if r.mem == nil {
		return
	}
	if err := r.mem.Write(&types.MemoryWrite{
		AgentID:  agent.AgentID,
		DataType: DataTypeAgentState,
		Payload:  agent,
		Tags:     map[string]string{"context": DataTypeAgentState},
	}); err != nil {
		slog.Warn("registry: failed to persist agent state",
			"agent_id", agent.AgentID,
			"state", agent.State,
			"error", err,
		)
	}
}

// — Helpers ————————————————————————————————————————————————————————————————

// copyAgent returns a shallow copy of a with its StateHistory slice deep-copied
// to prevent external mutation of the catalog entry.
func copyAgent(a *types.AgentRecord) *types.AgentRecord {
	cp := *a
	cp.StateHistory = make([]types.StateEvent, len(a.StateHistory))
	copy(cp.StateHistory, a.StateHistory)
	return &cp
}

// validateTransition returns an error if transitioning from → to is not
// permitted by the state machine.
func validateTransition(from, to string) error {
	allowed, known := validTransitions[from]
	if !known {
		return fmt.Errorf("unknown source state %q", from)
	}
	for _, s := range allowed {
		if s == to {
			return nil
		}
	}
	return fmt.Errorf("invalid transition %q → %q", from, to)
}

// hasAllDomains returns true if agentDomains contains every key in needed.
func hasAllDomains(agentDomains []string, needed map[string]struct{}) bool {
	have := make(map[string]struct{}, len(agentDomains))
	for _, d := range agentDomains {
		have[d] = struct{}{}
	}
	for n := range needed {
		if _, ok := have[n]; !ok {
			return false
		}
	}
	return true
}
