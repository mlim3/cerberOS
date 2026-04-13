// Package lifecycle — firecracker.go implements the M3 Firecracker microVM
// lifecycle manager.
//
// Architecture
// ─────────────
// Each agent runs in a dedicated Firecracker microVM. The manager:
//  1. Starts a firecracker process per agent (via the configured binary).
//  2. Configures the VM through the Firecracker HTTP API (Unix socket).
//  3. Writes the SpawnContext + forwarded env vars to the Firecracker MMDS.
//  4. Boots the VM via the InstanceStart action.
//  5. Tracks each running VM for health checks and graceful termination.
//
// SpawnContext delivery
// ─────────────────────
// The SpawnContext JSON and selected host environment variables are written to
// the Firecracker MMDS (Microvm Metadata Service) before InstanceStart.
// The rootfs init script sets AEGIS_MMDS_ENDPOINT=http://169.254.169.254/ in
// the agent-process environment so the binary reads its context from MMDS
// rather than stdin. When AEGIS_MMDS_ENDPOINT is unset (process-manager mode
// or tests) the agent-process falls back to stdin — fully backward-compatible.
//
// Networking (MMDS prerequisite)
// ───────────────────────────────
// MMDS requires at least one TAP-backed network interface in the guest.
// Set AEGIS_FIRECRACKER_TAP to the name of a pre-created host TAP device.
// When unset the network-interface and MMDS steps are skipped (the VM still
// boots but will not receive its SpawnContext via MMDS — suitable only for
// testing against a mock Firecracker API server).
//
// Required / optional environment variables
// ──────────────────────────────────────────
//
//	AEGIS_FIRECRACKER_BINARY — path to the firecracker binary   (default: "firecracker")
//	AEGIS_FIRECRACKER_KERNEL — path to the Linux kernel image   (required for production)
//	AEGIS_FIRECRACKER_ROOTFS — path to the root filesystem image (required for production)
//	AEGIS_FIRECRACKER_TAP    — TAP network device name for MMDS  (optional; skips MMDS when absent)
//	AEGIS_FIRECRACKER_VCPUS  — vCPU count per VM                 (default: 2)
//	AEGIS_FIRECRACKER_MEM    — memory per VM in MiB              (default: 512)
//
// Snapshot-based fast respawn (< 500 ms target)
// ──────────────────────────────────────────────
// The firecrackerManager is designed to support snapshot restore via
// PUT /snapshot/load. When a base snapshot exists for a skill domain the
// manager can skip the full boot sequence and restore the pre-warmed VM state,
// then PATCH /mmds with the new SpawnContext. The snapshot lifecycle is a
// follow-on feature wired as a no-op in this release; the interface is in place.
package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	fcDefaultBinary      = "firecracker"
	fcDefaultVCPUs       = 2
	fcDefaultMemMiB      = 512
	fcRootDriveID        = "rootfs"
	fcMMDSAddress        = "169.254.169.254"
	fcMMDSVersion        = "V1"
	fcSocketReadyTimeout = 2 * time.Second
	fcShutdownTimeout    = 5 * time.Second
)

// fcMMDSPayload is written to the Firecracker MMDS before InstanceStart.
// The agent-process reads the payload from the MMDS endpoint when
// AEGIS_MMDS_ENDPOINT is set in its environment.
type fcMMDSPayload struct {
	SpawnContext agentSpawnContext `json:"spawn_context"`
	Env          map[string]string `json:"env"`
}

// fcInstanceInfo is the response body of GET / on the Firecracker API.
type fcInstanceInfo struct {
	ID    string `json:"id"`
	State string `json:"state"` // "Not started" | "Running" | "Paused"
}

// fcVM tracks a single running Firecracker microVM.
type fcVM struct {
	agentID    string
	socketPath string
	cmd        *exec.Cmd     // nil when started by test launcher
	client     *http.Client  // dials vm.socketPath via Unix socket
	done       chan struct{} // closed when cmd exits (never closed when cmd is nil)
	exitErr    error
}

// vmLauncher starts the Firecracker process for a new VM and returns the Cmd.
// The launcher must NOT wait for the process — it only starts it. Returning
// (nil, nil) is valid in tests where the socket is pre-created by a mock server.
type vmLauncher func(agentID, socketPath string) (*exec.Cmd, error)

// firecrackerConfig carries the static parameters for a firecrackerManager.
type firecrackerConfig struct {
	binaryPath string
	kernelPath string
	rootfsPath string
	tapName    string
	vcpus      int
	memMiB     int
}

// firecrackerManager implements Manager using Firecracker microVMs.
type firecrackerManager struct {
	socketDir string
	cfg       firecrackerConfig
	launcher  vmLauncher

	mu  sync.RWMutex
	vms map[string]*fcVM
}

// NewFirecracker returns a Lifecycle Manager that isolates each agent in a
// dedicated Firecracker microVM. socketDir is the directory in which per-VM
// API Unix sockets are created; it is created if absent.
//
// Configuration is read from the environment variables documented at the top
// of this file.
func NewFirecracker(socketDir string) Manager {
	binary := os.Getenv("AEGIS_FIRECRACKER_BINARY")
	if binary == "" {
		binary = fcDefaultBinary
	}
	vcpus := fcParseEnvInt("AEGIS_FIRECRACKER_VCPUS", fcDefaultVCPUs)
	memMiB := fcParseEnvInt("AEGIS_FIRECRACKER_MEM", fcDefaultMemMiB)

	cfg := firecrackerConfig{
		binaryPath: binary,
		kernelPath: os.Getenv("AEGIS_FIRECRACKER_KERNEL"),
		rootfsPath: os.Getenv("AEGIS_FIRECRACKER_ROOTFS"),
		tapName:    os.Getenv("AEGIS_FIRECRACKER_TAP"),
		vcpus:      vcpus,
		memMiB:     memMiB,
	}

	return newFirecrackerManager(socketDir, cfg, func(agentID, socketPath string) (*exec.Cmd, error) {
		cmd := exec.Command(cfg.binaryPath, //nolint:gosec // path comes from operator config
			"--api-sock", socketPath,
			"--id", agentID,
		)
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start firecracker process for agent %q: %w", agentID, err)
		}
		return cmd, nil
	})
}

// newFirecrackerManager constructs a firecrackerManager with an injectable
// launcher. Used by production (NewFirecracker) and by tests.
func newFirecrackerManager(socketDir string, cfg firecrackerConfig, launcher vmLauncher) *firecrackerManager {
	return &firecrackerManager{
		socketDir: socketDir,
		cfg:       cfg,
		launcher:  launcher,
		vms:       make(map[string]*fcVM),
	}
}

// ─── Manager interface ────────────────────────────────────────────────────────

// Spawn creates and boots a Firecracker microVM for the given agent.
//
// Sequence:
//  1. Invoke launcher to start the Firecracker process (creates API socket).
//  2. Poll for socket readiness (up to fcSocketReadyTimeout).
//  3. Configure the VM: machine-config, boot-source, rootfs drive,
//     optional network interface (if AEGIS_FIRECRACKER_TAP is set),
//     optional MMDS payload with SpawnContext + env vars.
//  4. Send InstanceStart action to boot the VM.
func (m *firecrackerManager) Spawn(config VMConfig) error {
	if config.AgentID == "" {
		return fmt.Errorf("lifecycle: AgentID must not be empty")
	}

	m.mu.Lock()
	if _, exists := m.vms[config.AgentID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("lifecycle: VM for agent %q is already running", config.AgentID)
	}
	m.mu.Unlock()

	if err := os.MkdirAll(m.socketDir, 0o755); err != nil {
		return fmt.Errorf("lifecycle: create socket dir %s: %w", m.socketDir, err)
	}

	socketPath := filepath.Join(m.socketDir, config.AgentID+".sock")

	cmd, err := m.launcher(config.AgentID, socketPath)
	if err != nil {
		return fmt.Errorf("lifecycle: launcher: %w", err)
	}

	vm := &fcVM{
		agentID:    config.AgentID,
		socketPath: socketPath,
		cmd:        cmd,
		client:     fcNewUnixClient(socketPath),
		done:       make(chan struct{}),
	}

	if cmd != nil {
		go func() {
			vm.exitErr = cmd.Wait()
			close(vm.done)
		}()
	}

	if err := fcWaitForSocket(socketPath, fcSocketReadyTimeout); err != nil {
		m.killVM(vm)
		return fmt.Errorf("lifecycle: %w", err)
	}

	if err := m.configureAndBoot(vm, config); err != nil {
		m.killVM(vm)
		_ = os.Remove(socketPath)
		return fmt.Errorf("lifecycle: configure VM for agent %q: %w", config.AgentID, err)
	}

	m.mu.Lock()
	m.vms[config.AgentID] = vm
	m.mu.Unlock()
	return nil
}

// Terminate sends a graceful ACPI shutdown signal to the VM and waits for the
// Firecracker process to exit (up to fcShutdownTimeout, then SIGKILL).
func (m *firecrackerManager) Terminate(agentID string) error {
	m.mu.Lock()
	vm, ok := m.vms[agentID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("lifecycle: no VM found for agent %q", agentID)
	}
	delete(m.vms, agentID)
	m.mu.Unlock()

	// Best-effort graceful shutdown via ACPI power button.
	_ = fcRequest(vm.client, http.MethodPut, "/actions",
		map[string]string{"action_type": "SendCtrlAltDel"})

	if vm.cmd != nil && vm.cmd.Process != nil {
		select {
		case <-vm.done:
		case <-time.After(fcShutdownTimeout):
			_ = vm.cmd.Process.Kill()
		}
	}

	_ = os.Remove(vm.socketPath)
	return nil
}

// Health queries the Firecracker API for the current VM state.
func (m *firecrackerManager) Health(agentID string) (HealthStatus, error) {
	m.mu.RLock()
	vm, ok := m.vms[agentID]
	m.mu.RUnlock()

	if !ok {
		return HealthStatus{AgentID: agentID, State: StateUnknown}, nil
	}

	// Fast path: process has already exited.
	select {
	case <-vm.done:
		return HealthStatus{AgentID: agentID, State: StateStopped, Message: "VM process exited"}, nil
	default:
	}

	// Query Firecracker for live instance state.
	resp, err := vm.client.Get("http://localhost/") //nolint:noctx
	if err != nil {
		return HealthStatus{AgentID: agentID, State: StateUnknown, Message: err.Error()}, nil
	}
	defer resp.Body.Close()

	var info fcInstanceInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return HealthStatus{AgentID: agentID, State: StateUnknown,
			Message: "failed to decode instance info"}, nil
	}

	switch info.State {
	case "Running", "Paused":
		return HealthStatus{AgentID: agentID, State: StateRunning}, nil
	default:
		return HealthStatus{AgentID: agentID, State: StateStopped, Message: info.State}, nil
	}
}

// ─── VM configuration ─────────────────────────────────────────────────────────

// configureAndBoot drives the Firecracker API call sequence that sets up and
// starts the VM. All steps are ordered as required by the Firecracker API:
// machine resources first, then block devices, optional networking, MMDS, boot.
func (m *firecrackerManager) configureAndBoot(vm *fcVM, config VMConfig) error {
	// 1. Machine resources.
	if err := fcRequest(vm.client, http.MethodPut, "/machine-config", map[string]interface{}{
		"vcpu_count":   m.cfg.vcpus,
		"mem_size_mib": m.cfg.memMiB,
	}); err != nil {
		return fmt.Errorf("PUT /machine-config: %w", err)
	}

	// 2. Boot source (kernel image + boot args).
	if err := fcRequest(vm.client, http.MethodPut, "/boot-source", map[string]interface{}{
		"kernel_image_path": m.cfg.kernelPath,
		"boot_args":         fcBootArgs(),
	}); err != nil {
		return fmt.Errorf("PUT /boot-source: %w", err)
	}

	// 3. Root filesystem drive.
	if err := fcRequest(vm.client, http.MethodPut, "/drives/"+fcRootDriveID, map[string]interface{}{
		"drive_id":       fcRootDriveID,
		"path_on_host":   m.cfg.rootfsPath,
		"is_root_device": true,
		"is_read_only":   false,
	}); err != nil {
		return fmt.Errorf("PUT /drives/rootfs: %w", err)
	}

	// 4–6. Network interface + MMDS — requires a TAP device.
	// When AEGIS_FIRECRACKER_TAP is unset these steps are skipped; the VM boots
	// without MMDS connectivity (suitable for tests against a mock API server).
	if m.cfg.tapName != "" {
		if err := m.configureMMDS(vm, config); err != nil {
			return err
		}
	}

	// 7. Boot the VM.
	if err := fcRequest(vm.client, http.MethodPut, "/actions",
		map[string]string{"action_type": "InstanceStart"}); err != nil {
		return fmt.Errorf("PUT /actions InstanceStart: %w", err)
	}

	return nil
}

// configureMMDS sets up the network interface and writes the SpawnContext +
// host env vars to the MMDS. Called only when AEGIS_FIRECRACKER_TAP is set.
func (m *firecrackerManager) configureMMDS(vm *fcVM, config VMConfig) error {
	// Network interface (required for guest → MMDS connectivity).
	if err := fcRequest(vm.client, http.MethodPut, "/network-interfaces/eth0", map[string]interface{}{
		"iface_id":      "eth0",
		"host_dev_name": m.cfg.tapName,
	}); err != nil {
		return fmt.Errorf("PUT /network-interfaces/eth0: %w", err)
	}

	// MMDS network configuration.
	if err := fcRequest(vm.client, http.MethodPut, "/mmds/config", map[string]interface{}{
		"version":            fcMMDSVersion,
		"ipv4_address":       fcMMDSAddress,
		"network_interfaces": []string{"eth0"},
	}); err != nil {
		return fmt.Errorf("PUT /mmds/config: %w", err)
	}

	// Write SpawnContext + env vars to MMDS.
	payload := fcMMDSPayload{
		SpawnContext: agentSpawnContext{
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
		},
		Env: fcGuestEnv(config),
	}
	if err := fcRequest(vm.client, http.MethodPut, "/mmds", payload); err != nil {
		return fmt.Errorf("PUT /mmds: %w", err)
	}

	return nil
}

// fcGuestEnv builds the map of environment variables to forward to the guest
// agent-process via MMDS. Agent-specific values are set directly; critical host
// env vars (API keys, transport URL) are forwarded when present.
func fcGuestEnv(config VMConfig) map[string]string {
	env := map[string]string{
		"AEGIS_AGENT_ID":      config.AgentID,
		"AEGIS_TASK_ID":       config.TaskID,
		"AEGIS_TRACE_ID":      config.TraceID,
		"AEGIS_MMDS_ENDPOINT": "http://" + fcMMDSAddress + "/",
	}
	// Forward host env vars that the agent-process needs inside the guest.
	for _, key := range []string{
		"ANTHROPIC_API_KEY",
		"AEGIS_NATS_URL",
		"AEGIS_AGENT_PROCESS_PATH", // in case the guest needs to respawn
		"AEGIS_SKILL_SPEC_BUDGET",
		"VOYAGE_API_KEY",
		"AEGIS_EMBEDDING_MODEL",
	} {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}
	return env
}

// fcBootArgs returns the Linux kernel command-line arguments used for all VMs.
// These configure the serial console and set a minimal panic behaviour.
// Agent-specific env vars are delivered via MMDS, not boot args.
func fcBootArgs() string {
	return "console=ttyS0 reboot=k panic=1 pci=off"
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// fcRequest makes an HTTP request to the Firecracker API over the VM's Unix socket.
// method must be a standard HTTP method (PUT, GET, PATCH). body is JSON-encoded
// when non-nil. Returns an error for any non-2xx response.
func fcRequest(client *http.Client, method, apiPath string, body interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, "http://localhost"+apiPath, bodyReader) //nolint:noctx
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, apiPath, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: unexpected status %d", method, apiPath, resp.StatusCode)
	}
	return nil
}

// fcNewUnixClient returns an *http.Client whose Transport dials socketPath as
// a Unix domain socket. All HTTP requests use http://localhost/... as the URL;
// the hostname is ignored since the socket provides the connection.
func fcNewUnixClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.DialTimeout("unix", socketPath, time.Second)
			},
		},
	}
}

// fcWaitForSocket polls socketPath until a TCP-like Unix socket accept succeeds
// or timeout elapses. Uses short exponential back-off to avoid busy-spinning.
func fcWaitForSocket(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	sleep := 10 * time.Millisecond
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(sleep)
		if sleep < 200*time.Millisecond {
			sleep *= 2
		}
	}
	return fmt.Errorf("timed out waiting for Firecracker API socket %s (waited %s)", socketPath, timeout)
}

// killVM sends SIGKILL to the Firecracker process and waits briefly for exit.
// Used for cleanup when Spawn fails partway through configuration.
func (m *firecrackerManager) killVM(vm *fcVM) {
	if vm.cmd != nil && vm.cmd.Process != nil {
		_ = vm.cmd.Process.Kill()
		select {
		case <-vm.done:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// fcParseEnvInt reads a positive integer from envVar, returning defaultVal when
// the variable is unset, empty, or invalid.
func fcParseEnvInt(envVar string, defaultVal int) int {
	s := os.Getenv(envVar)
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return defaultVal
	}
	return n
}
