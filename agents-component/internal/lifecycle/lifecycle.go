// Package lifecycle is M6 — the Lifecycle Manager. It owns Firecracker microVM
// spawn and teardown, health monitoring, and crash recovery. The microVM
// integration is stubbed until the Firecracker binary is available.
package lifecycle

import (
	"fmt"
	"sync"
)

// State represents the runtime state of a managed VM.
type State string

const (
	StateRunning State = "running"
	StateStopped State = "stopped"
	StateUnknown State = "unknown"
)

// VMConfig carries the parameters needed to launch a microVM for an agent.
type VMConfig struct {
	AgentID       string
	SkillDomain   string // entry-point domain injected into the VM
	CredentialPtr string // vault token pointer (not the token value)
	TraceID       string
}

// HealthStatus is the result of a health probe for a running VM.
type HealthStatus struct {
	AgentID string
	State   State
	Message string
}

// Manager is the interface for microVM lifecycle operations.
type Manager interface {
	// Spawn starts a Firecracker microVM for the given agent and config.
	Spawn(config VMConfig) error

	// Terminate stops and cleans up the microVM for the given agent.
	Terminate(agentID string) error

	// Health returns the current health status of the agent's VM.
	Health(agentID string) (HealthStatus, error)
}

// stubManager is the default implementation that simulates VM management
// without invoking Firecracker. Replace with real Firecracker API calls
// when the microVM integration is ready.
type stubManager struct {
	mu  sync.RWMutex
	vms map[string]State
}

// New returns a Lifecycle Manager backed by an in-process stub.
func New() Manager {
	return &stubManager{
		vms: make(map[string]State),
	}
}

func (m *stubManager) Spawn(config VMConfig) error {
	if config.AgentID == "" {
		return fmt.Errorf("lifecycle: AgentID must not be empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, exists := m.vms[config.AgentID]; exists && state == StateRunning {
		return fmt.Errorf("lifecycle: VM for agent %q is already running", config.AgentID)
	}
	m.vms[config.AgentID] = StateRunning
	return nil
}

func (m *stubManager) Terminate(agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.vms[agentID]; !ok {
		return fmt.Errorf("lifecycle: no VM found for agent %q", agentID)
	}
	m.vms[agentID] = StateStopped
	delete(m.vms, agentID)
	return nil
}

func (m *stubManager) Health(agentID string) (HealthStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, ok := m.vms[agentID]
	if !ok {
		return HealthStatus{AgentID: agentID, State: StateUnknown}, nil
	}
	return HealthStatus{AgentID: agentID, State: state}, nil
}
