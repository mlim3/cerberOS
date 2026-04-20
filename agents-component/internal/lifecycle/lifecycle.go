// Package lifecycle is M6 — the Lifecycle Manager. It owns agent process
// spawn and teardown, health monitoring, and crash recovery.
//
// For M2 the manager launches cmd/agent-process as a local OS process.
// M3 will replace this with Firecracker microVM calls; the Manager interface
// is identical in both cases.
package lifecycle

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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
	AgentID          string
	VMID             string // allocated VM identity; changes on respawn (same AgentID, new VMID)
	TaskID           string // task the agent is being spawned to execute
	SkillDomain      string // entry-point domain injected into the agent at spawn
	CredentialPtr    string // vault permission token pointer (not the token value)
	Instructions     string // natural-language task description for the agent
	CommandManifest  string // pre-built "- name: description" list for the entry domain; injected into system prompt
	RecoveredContext string // non-empty on respawn: serialised CrashSnapshot for checkpoint resume
	AgentMemory      string // distilled facts from past tasks in this domain; injected into system prompt
	UserProfile      string // user preference observations; injected into system prompt
	TraceID          string
	UserContextID    string // propagated from TaskSpec; echoed in all outbound events (issue #67)

	// OnComplete is called by the process manager when the agent process exits.
	// output holds the raw TaskOutput JSON written to stdout; exitErr is non-nil
	// when the process exited with a non-zero status. The factory uses this to
	// call CompleteTask on clean exit rather than waiting for the crash detector
	// to misinterpret heartbeat silence as a crash.
	OnComplete func(agentID string, output []byte, exitErr error)
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

	// Deliver hands a fresh SpawnContext to an already-running agent process,
	// enabling live-process reuse so subsequent tasks on the same agent skip
	// the Vault pre-authorize + fork + agent-process boot cold-start tax.
	//
	// Implementations that do not support reuse (Firecracker) must return
	// ErrReuseUnsupported. Callers are expected to fall back to Spawn in that
	// case.
	//
	// On success the caller's previous VMConfig.OnComplete is replaced by
	// config.OnComplete; the next TaskOutput produced by the agent process
	// will fire config.OnComplete with the decoded JSON line.
	Deliver(agentID string, config VMConfig) error

	// SupportsReuse reports whether Deliver is expected to succeed on this
	// manager. It is a cheap capability probe callers can use to decide
	// between the reuse-enabled flow (Priority 1 IDLE reuse) and the classic
	// provision-per-task flow. Implementations must return a stable value for
	// the lifetime of the manager.
	SupportsReuse() bool
}

// ErrReuseUnsupported is returned by Manager.Deliver implementations that do
// not support live-process reuse (firecracker manager, in-process stub).
var ErrReuseUnsupported = fmt.Errorf("lifecycle: live-process reuse not supported by this manager")

// ─── Process manager (M2 implementation) ────────────────────────────────────

// agentSpawnContext is the JSON payload written to the agent-process binary's
// stdin at launch. It mirrors cmd/agent-process.SpawnContext; the struct is
// defined here to avoid importing a main package.
type agentSpawnContext struct {
	TaskID           string `json:"task_id"`
	SkillDomain      string `json:"skill_domain"`
	PermissionToken  string `json:"permission_token"` // opaque vault reference — never a raw credential
	Instructions     string `json:"instructions"`
	CommandManifest  string `json:"command_manifest,omitempty"`  // "- name: description" list; injected into system prompt
	RecoveredContext string `json:"recovered_context,omitempty"` // non-empty on respawn; contains crash snapshot for checkpoint resume
	AgentMemory      string `json:"agent_memory,omitempty"`      // distilled facts from past tasks in this domain
	UserProfile      string `json:"user_profile,omitempty"`      // user preference observations
	TraceID          string `json:"trace_id"`
	UserContextID    string `json:"user_context_id,omitempty"` // propagated from TaskSpec; echoed in all child agent events (issue #67)
}

// processEntry tracks a single running agent-process subprocess. The process
// is long-lived: each task is delivered as a JSON SpawnContext on stdin and
// each TaskOutput is consumed as a newline-delimited JSON object on stdout.
// One processEntry therefore corresponds to many task completions, not one.
type processEntry struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser // write side of the persistent stdin pipe
	done    chan struct{}  // closed when the process has exited
	exitErr error          // non-nil if the process exited with an error

	mu              sync.Mutex
	pendingComplete func(agentID string, output []byte, exitErr error)
}

// setPending swaps the callback invoked on the next TaskOutput line. Returns
// the previous callback so callers can detect a lost completion signal.
func (e *processEntry) setPending(cb func(string, []byte, error)) func(string, []byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	prev := e.pendingComplete
	e.pendingComplete = cb
	return prev
}

// consumePending returns and clears the current pending callback atomically.
func (e *processEntry) consumePending() func(string, []byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cb := e.pendingComplete
	e.pendingComplete = nil
	return cb
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
//
// The stdin pipe is kept open after launch so the Lifecycle Manager can deliver
// subsequent SpawnContexts to the same process on IDLE-agent reuse. The process
// terminates cleanly when Terminate closes stdin or when the caller signals it.
func NewProcess(binaryPath string) Manager {
	return &processManager{
		binaryPath: binaryPath,
		procs:      make(map[string]*processEntry),
	}
}

// encodeSpawnContext serialises a VMConfig into the agent-process wire format.
// The encoded form is suffixed with a newline so the receiving process, which
// decodes a stream of JSON objects, can flush cleanly between tasks.
func encodeSpawnContext(config VMConfig) ([]byte, error) {
	payload, err := json.Marshal(agentSpawnContext{
		TaskID:           config.TaskID,
		SkillDomain:      config.SkillDomain,
		PermissionToken:  config.CredentialPtr,
		Instructions:     config.Instructions,
		CommandManifest:  config.CommandManifest,
		RecoveredContext: config.RecoveredContext,
		AgentMemory:      config.AgentMemory,
		UserProfile:      config.UserProfile,
		TraceID:          config.TraceID,
		UserContextID:    config.UserContextID,
	})
	if err != nil {
		return nil, fmt.Errorf("lifecycle: marshal spawn context: %w", err)
	}
	return append(payload, '\n'), nil
}

func (m *processManager) Spawn(config VMConfig) error {
	if config.AgentID == "" {
		return fmt.Errorf("lifecycle: AgentID must not be empty")
	}

	payload, err := encodeSpawnContext(config)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.procs[config.AgentID]; exists {
		return fmt.Errorf("lifecycle: process for agent %q is already running", config.AgentID)
	}

	cmd := exec.Command(m.binaryPath) //nolint:gosec // path comes from operator config

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("lifecycle: open stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return fmt.Errorf("lifecycle: open stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr // agent logs flow to parent stderr

	// Inherit parent env (for ANTHROPIC_API_KEY etc.) then overlay agent-specific
	// variables so the agent process can identify itself and publish heartbeats.
	cmd.Env = append(os.Environ(),
		"AEGIS_AGENT_ID="+config.AgentID,
		"AEGIS_TASK_ID="+config.TaskID,
		"AEGIS_TRACE_ID="+config.TraceID,
	)

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		return fmt.Errorf("lifecycle: start agent process: %w", err)
	}

	entry := &processEntry{
		cmd:             cmd,
		stdin:           stdinPipe,
		done:            make(chan struct{}),
		pendingComplete: config.OnComplete,
	}
	m.procs[config.AgentID] = entry

	// Stdout reader: one newline-delimited JSON object per task completion.
	// We fire the current pending callback for each line; callers swap the
	// callback via Deliver before pushing a new SpawnContext.
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		// Allow large JSON outputs — the default bufio.MaxScanTokenSize (64 KiB)
		// is too tight for multi-step planner results with full argument lists.
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			if cb := entry.consumePending(); cb != nil {
				cb(config.AgentID, line, nil)
			}
			// If no callback was pending we drop the line. This should not
			// happen in normal operation because Deliver always sets a fresh
			// pending callback before writing a new SpawnContext.
		}
	}()

	// Wait watcher: fires the still-pending callback (if any) with the exit
	// error so an unexpected process death does not leave the caller waiting
	// indefinitely.
	go func() {
		entry.exitErr = cmd.Wait()
		close(entry.done)
		if cb := entry.consumePending(); cb != nil {
			cb(config.AgentID, nil, entry.exitErr)
		}
	}()

	if _, err := entry.stdin.Write(payload); err != nil {
		_ = m.Terminate(config.AgentID)
		return fmt.Errorf("lifecycle: write initial spawn context: %w", err)
	}

	return nil
}

func (m *processManager) Deliver(agentID string, config VMConfig) error {
	m.mu.RLock()
	entry, ok := m.procs[agentID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("lifecycle: no process found for agent %q", agentID)
	}
	select {
	case <-entry.done:
		return fmt.Errorf("lifecycle: agent %q process has exited", agentID)
	default:
	}

	payload, err := encodeSpawnContext(config)
	if err != nil {
		return err
	}

	// Install the new completion callback *before* writing the payload so the
	// stdout reader sees it in place when the agent emits its TaskOutput.
	if prev := entry.setPending(config.OnComplete); prev != nil {
		// A previous task's completion callback was never invoked — this
		// indicates a logic bug (Deliver called before the prior task finished).
		// Returning an error keeps the runtime honest; the previous callback is
		// left un-invoked because we cannot fabricate an output for it.
		entry.setPending(prev)
		return fmt.Errorf("lifecycle: agent %q has a pending task completion; refusing to deliver", agentID)
	}
	if _, err := entry.stdin.Write(payload); err != nil {
		// On write failure, clear the callback so a Deliver retry is possible.
		entry.consumePending()
		return fmt.Errorf("lifecycle: write spawn context: %w", err)
	}
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

	// Closing stdin signals the agent-process loop to exit cleanly after the
	// current task (if any) completes. Fall back to SIGTERM/SIGKILL if it does
	// not exit within the deadline.
	_ = entry.stdin.Close()
	select {
	case <-entry.done:
		return nil
	case <-time.After(5 * time.Second):
	}
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

func (m *processManager) SupportsReuse() bool { return true }

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

// Deliver on the stub manager emulates a successful task completion by
// invoking the supplied OnComplete with a synthesised TaskOutput envelope.
// This keeps factory-level reuse tests realistic without spinning up a real
// agent binary.
func (m *stubManager) Deliver(agentID string, config VMConfig) error {
	m.mu.RLock()
	state, ok := m.vms[agentID]
	m.mu.RUnlock()
	if !ok || state != StateRunning {
		return fmt.Errorf("lifecycle: no running process for agent %q", agentID)
	}
	if config.OnComplete != nil {
		out, _ := json.Marshal(map[string]any{
			"task_id":  config.TaskID,
			"trace_id": config.TraceID,
			"success":  true,
			"result":   "stub delivery",
		})
		go config.OnComplete(agentID, out, nil)
	}
	return nil
}

// SupportsReuse reports true for the in-process stub so factory-level tests
// exercise the reuse code path. The real gating in production happens via the
// factory's idleSuspendTimeout configuration.
func (m *stubManager) SupportsReuse() bool { return true }
