// Package lifecycle is M6 — the Lifecycle Manager. It owns agent process
// spawn and teardown, health monitoring, and crash recovery.
//
// For M2 the manager launches cmd/agent-process as a local OS process.
// M3 will replace this with Firecracker microVM calls; the Manager interface
// is identical in both cases.
package lifecycle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// State represents the runtime state of a managed agent process.
type State string

const (
	StateRunning State = "running"
	StateStopped State = "stopped"
	StateUnknown State = "unknown"
)

// VMConfig carries the parameters needed to launch an agent process.
type VMConfig struct {
	AgentID       string
	VMID          string // allocated VM identity; changes on respawn (same AgentID, new VMID)
	TaskID        string // task the agent is being spawned to execute
	SkillDomain   string // entry-point domain injected into the agent at spawn
	CredentialPtr string // vault permission token pointer (not the token value)
	Instructions  string // natural-language task description for the agent
	TraceID       string
}

// HealthStatus is the result of a health probe for a running agent.
type HealthStatus struct {
	AgentID string
	State   State
	Message string
}

// Manager is the interface for agent lifecycle operations.
type Manager interface {
	// Spawn starts an agent process for the given agent and config.
	Spawn(config VMConfig) error

	// Terminate stops and cleans up the agent process.
	Terminate(agentID string) error

	// Health returns the current health status of the agent process.
	Health(agentID string) (HealthStatus, error)
}

// ─── Process manager (M2 implementation) ────────────────────────────────────

// agentSpawnContext is the JSON payload written to the agent-process binary's
// stdin at launch. It mirrors cmd/agent-process.SpawnContext; the struct is
// defined here to avoid importing a main package.
type agentSpawnContext struct {
	TaskID          string `json:"task_id"`
	SkillDomain     string `json:"skill_domain"`
	PermissionToken string `json:"permission_token"` // opaque vault reference — never a raw credential
	Instructions    string `json:"instructions"`
	TraceID         string `json:"trace_id"`
}

// processEntry tracks a single running agent-process subprocess.
type processEntry struct {
	cmd     *exec.Cmd
	stdout  bytes.Buffer  // captures TaskOutput JSON from stdout; read by factory on exit
	done    chan struct{} // closed when the process has exited
	exitErr error         // non-nil if the process exited with an error
}

// processManager launches a real agent-process binary for each agent.
type processManager struct {
	mu         sync.RWMutex
	binaryPath string
	procs      map[string]*processEntry
}

// NewProcess returns a Lifecycle Manager that launches the agent-process binary
// at binaryPath as a local OS process. The binary receives its SpawnContext via
// stdin and writes its TaskOutput to stdout.
//
// The spawned process inherits the parent's environment so that
// ANTHROPIC_API_KEY and any other required variables are available.
func NewProcess(binaryPath string) Manager {
	return &processManager{
		binaryPath: binaryPath,
		procs:      make(map[string]*processEntry),
	}
}

func (m *processManager) Spawn(config VMConfig) error {
	if config.AgentID == "" {
		return fmt.Errorf("lifecycle: AgentID must not be empty")
	}

	payload, err := json.Marshal(agentSpawnContext{
		TaskID:          config.TaskID,
		SkillDomain:     config.SkillDomain,
		PermissionToken: config.CredentialPtr,
		Instructions:    config.Instructions,
		TraceID:         config.TraceID,
	})
	if err != nil {
		return fmt.Errorf("lifecycle: marshal spawn context: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.procs[config.AgentID]; exists {
		return fmt.Errorf("lifecycle: process for agent %q is already running", config.AgentID)
	}

	entry := &processEntry{done: make(chan struct{})}

	cmd := exec.Command(m.binaryPath) //nolint:gosec // path comes from operator config
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout = &entry.stdout
	cmd.Stderr = os.Stderr // agent logs flow to parent stderr
	cmd.Env = os.Environ() // inherit env so ANTHROPIC_API_KEY reaches the agent

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("lifecycle: start agent process: %w", err)
	}

	entry.cmd = cmd
	m.procs[config.AgentID] = entry

	go func() {
		entry.exitErr = cmd.Wait()
		close(entry.done)
	}()

	return nil
}

func (m *processManager) Terminate(agentID string) error {
	m.mu.Lock()
	entry, ok := m.procs[agentID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("lifecycle: no process found for agent %q", agentID)
	}
	delete(m.procs, agentID)
	m.mu.Unlock()

	if entry.cmd.Process == nil {
		return nil
	}

	// Attempt graceful shutdown; escalate to SIGKILL after 5 s.
	if err := entry.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = entry.cmd.Process.Kill()
		return nil
	}
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		_ = entry.cmd.Process.Kill()
	}
	return nil
}

func (m *processManager) Health(agentID string) (HealthStatus, error) {
	m.mu.RLock()
	entry, ok := m.procs[agentID]
	m.mu.RUnlock()

	if !ok {
		return HealthStatus{AgentID: agentID, State: StateUnknown}, nil
	}
	select {
	case <-entry.done:
		return HealthStatus{AgentID: agentID, State: StateStopped}, nil
	default:
		return HealthStatus{AgentID: agentID, State: StateRunning}, nil
	}
}

// ─── Stub manager (used in unit tests that do not need a real binary) ────────

// stubManager is an in-process fake that simulates process management without
// invoking a real binary. Use New() to obtain one.
type stubManager struct {
	mu  sync.RWMutex
	vms map[string]State
}

// New returns a Lifecycle Manager backed by an in-process stub.
// Use this in unit tests; use NewProcess for integration and production.
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
