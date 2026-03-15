// Package registry is M3 — the Agent Registry. It maintains an in-memory catalog
// of all known agents and their current state. Persistence is delegated to the
// Memory Component via the Memory Interface.
package registry

import (
	"fmt"
	"sync"
	"time"

	"github.com/cerberOS/agents-component/pkg/types"
)

// Registry is the interface for agent catalog operations.
type Registry interface {
	// Register adds a new agent record. Returns an error if the agent already exists.
	Register(agent *types.AgentRecord) error

	// Get retrieves an agent by ID.
	Get(agentID string) (*types.AgentRecord, error)

	// FindBySkills returns agents whose SkillDomains satisfy all required domains.
	FindBySkills(domains []string) ([]*types.AgentRecord, error)

	// UpdateState sets the agent's State field and stamps UpdatedAt.
	UpdateState(agentID, state string) error

	// AssignTask links a task to an agent and marks it active.
	AssignTask(agentID, taskID string) error

	// Deregister removes an agent from the catalog.
	Deregister(agentID string) error

	// List returns all registered agents.
	List() []*types.AgentRecord
}

// inMemoryRegistry is the default Registry implementation backed by a sync.Map.
type inMemoryRegistry struct {
	mu     sync.RWMutex
	agents map[string]*types.AgentRecord
}

// New returns a ready-to-use in-memory Registry.
func New() Registry {
	return &inMemoryRegistry{
		agents: make(map[string]*types.AgentRecord),
	}
}

func (r *inMemoryRegistry) Register(agent *types.AgentRecord) error {
	if agent == nil || agent.AgentID == "" {
		return fmt.Errorf("registry: agent must have a non-empty AgentID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.agents[agent.AgentID]; exists {
		return fmt.Errorf("registry: agent %q already registered", agent.AgentID)
	}
	now := time.Now().UTC()
	agent.CreatedAt = now
	agent.UpdatedAt = now
	r.agents[agent.AgentID] = agent
	return nil
}

func (r *inMemoryRegistry) Get(agentID string) (*types.AgentRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	a, ok := r.agents[agentID]
	if !ok {
		return nil, fmt.Errorf("registry: agent %q not found", agentID)
	}
	// Return a copy to prevent external mutation.
	cp := *a
	return &cp, nil
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
		if a.State == "terminated" {
			continue
		}
		if hasAllDomains(a.SkillDomains, needed) {
			cp := *a
			matches = append(matches, &cp)
		}
	}
	return matches, nil
}

func (r *inMemoryRegistry) UpdateState(agentID, state string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.agents[agentID]
	if !ok {
		return fmt.Errorf("registry: agent %q not found", agentID)
	}
	a.State = state
	a.UpdatedAt = time.Now().UTC()
	return nil
}

func (r *inMemoryRegistry) AssignTask(agentID, taskID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.agents[agentID]
	if !ok {
		return fmt.Errorf("registry: agent %q not found", agentID)
	}
	a.AssignedTask = taskID
	a.State = "active"
	a.UpdatedAt = time.Now().UTC()
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
		cp := *a
		out = append(out, &cp)
	}
	return out
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
