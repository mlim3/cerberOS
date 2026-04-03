package lifecycle

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// shortTempDir creates a temporary directory under /tmp with a short name so
// that Unix socket paths derived from it stay under the 104-character macOS
// kernel limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fc_")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// mockFCServer starts a minimal HTTP server on a Unix socket that records the
// requests it receives and responds with 204 No Content (Firecracker style).
// It also handles GET / for Health checks, returning a JSON instance info with
// State "Running".
type mockFCServer struct {
	socketPath string
	listener   net.Listener
	requests   []string // recorded API paths
}

func newMockFCServer(t *testing.T, socketPath string) *mockFCServer {
	t.Helper()
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", socketPath, err)
	}
	srv := &mockFCServer{socketPath: socketPath, listener: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		srv.requests = append(srv.requests, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(fcInstanceInfo{ID: "test", State: "Running"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	go func() { _ = http.Serve(ln, mux) }() //nolint:gosec // test-only
	return srv
}

func (s *mockFCServer) Close() { _ = s.listener.Close() }

// ---------------------------------------------------------------------------
// fcParseEnvInt
// ---------------------------------------------------------------------------

func TestFCParseEnvInt_Default(t *testing.T) {
	t.Setenv("TEST_FC_INT_MISSING", "")
	if got := fcParseEnvInt("TEST_FC_INT_MISSING", 42); got != 42 {
		t.Errorf("want 42, got %d", got)
	}
}

func TestFCParseEnvInt_Valid(t *testing.T) {
	t.Setenv("TEST_FC_INT", "8")
	if got := fcParseEnvInt("TEST_FC_INT", 2); got != 8 {
		t.Errorf("want 8, got %d", got)
	}
}

func TestFCParseEnvInt_Invalid(t *testing.T) {
	t.Setenv("TEST_FC_INT_BAD", "abc")
	if got := fcParseEnvInt("TEST_FC_INT_BAD", 3); got != 3 {
		t.Errorf("want default 3 on invalid input, got %d", got)
	}
}

func TestFCParseEnvInt_Zero(t *testing.T) {
	t.Setenv("TEST_FC_INT_ZERO", "0")
	// 0 is not >= 1 so the default should be returned.
	if got := fcParseEnvInt("TEST_FC_INT_ZERO", 5); got != 5 {
		t.Errorf("want default 5 for zero value, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// fcWaitForSocket
// ---------------------------------------------------------------------------

func TestFCWaitForSocket_AlreadyReady(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "ready.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if err := fcWaitForSocket(sockPath, 500*time.Millisecond); err != nil {
		t.Errorf("expected no error for ready socket, got %v", err)
	}
}

func TestFCWaitForSocket_Timeout(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "absent.sock")
	// Socket never created — should time out quickly.
	start := time.Now()
	err := fcWaitForSocket(sockPath, 50*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected timeout error, got nil")
	}
	// Should not wait much longer than the timeout.
	if elapsed > 500*time.Millisecond {
		t.Errorf("waited too long: %v (timeout was 50ms)", elapsed)
	}
}

// ---------------------------------------------------------------------------
// fcGuestEnv
// ---------------------------------------------------------------------------

func TestFCGuestEnv_AgentFields(t *testing.T) {
	cfg := VMConfig{
		AgentID: "agent-1",
		TaskID:  "task-1",
		TraceID: "trace-1",
	}
	env := fcGuestEnv(cfg)

	if env["AEGIS_AGENT_ID"] != "agent-1" {
		t.Errorf("AEGIS_AGENT_ID: want agent-1, got %q", env["AEGIS_AGENT_ID"])
	}
	if env["AEGIS_TASK_ID"] != "task-1" {
		t.Errorf("AEGIS_TASK_ID: want task-1, got %q", env["AEGIS_TASK_ID"])
	}
	if env["AEGIS_TRACE_ID"] != "trace-1" {
		t.Errorf("AEGIS_TRACE_ID: want trace-1, got %q", env["AEGIS_TRACE_ID"])
	}
	if env["AEGIS_MMDS_ENDPOINT"] == "" {
		t.Error("AEGIS_MMDS_ENDPOINT must be set in guest env")
	}
}

func TestFCGuestEnv_ForwardsAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	cfg := VMConfig{AgentID: "a", TaskID: "t", TraceID: "tr"}
	env := fcGuestEnv(cfg)
	if env["ANTHROPIC_API_KEY"] != "test-key" {
		t.Errorf("ANTHROPIC_API_KEY not forwarded: got %q", env["ANTHROPIC_API_KEY"])
	}
}

func TestFCGuestEnv_OmitsUnsetKeys(t *testing.T) {
	// Ensure VOYAGE_API_KEY is unset so the test is deterministic.
	os.Unsetenv("VOYAGE_API_KEY")
	cfg := VMConfig{AgentID: "a", TaskID: "t", TraceID: "tr"}
	env := fcGuestEnv(cfg)
	if _, ok := env["VOYAGE_API_KEY"]; ok {
		t.Error("unset VOYAGE_API_KEY must not appear in guest env")
	}
}

// ---------------------------------------------------------------------------
// Spawn / Terminate / Health — using mock Firecracker API server
// ---------------------------------------------------------------------------

// newTestManager creates a firecrackerManager wired with the given mock server.
// The noop launcher skips actually starting firecracker; the mock server
// handles all API requests.
func newTestManager(t *testing.T, srv *mockFCServer) *firecrackerManager {
	t.Helper()
	dir := filepath.Dir(srv.socketPath)
	cfg := firecrackerConfig{vcpus: 1, memMiB: 128}
	return newFirecrackerManager(dir, cfg, func(agentID, socketPath string) (*exec.Cmd, error) {
		return nil, nil // mock server already listening on socketPath
	})
}

func TestSpawn_EmptyAgentID(t *testing.T) {
	m := newFirecrackerManager(shortTempDir(t), firecrackerConfig{vcpus: 1, memMiB: 128},
		func(_, _ string) (*exec.Cmd, error) { return nil, nil })
	err := m.Spawn(VMConfig{})
	if err == nil {
		t.Error("expected error for empty AgentID, got nil")
	}
}

func TestSpawn_DuplicateAgent(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "dup-agent.sock")
	srv := newMockFCServer(t, sockPath)
	defer srv.Close()

	m := newFirecrackerManager(dir, firecrackerConfig{vcpus: 1, memMiB: 128},
		func(_, _ string) (*exec.Cmd, error) { return nil, nil })

	cfg := VMConfig{
		AgentID:      "dup-agent",
		TaskID:       "t1",
		SkillDomain:  "web",
		TraceID:      "tr1",
		Instructions: "test",
	}
	if err := m.Spawn(cfg); err != nil {
		t.Fatalf("first Spawn: %v", err)
	}
	if err := m.Spawn(cfg); err == nil {
		t.Error("second Spawn with same AgentID must return an error")
	}
}

func TestSpawn_CallsConfigureAndBoot(t *testing.T) {
	dir := shortTempDir(t)
	agentID := "boot-agent"
	sockPath := filepath.Join(dir, agentID+".sock")
	srv := newMockFCServer(t, sockPath)
	defer srv.Close()

	m := newFirecrackerManager(dir, firecrackerConfig{vcpus: 2, memMiB: 256},
		func(_, _ string) (*exec.Cmd, error) { return nil, nil })

	cfg := VMConfig{
		AgentID:      agentID,
		TaskID:       "task-boot",
		SkillDomain:  "web",
		TraceID:      "trace-boot",
		Instructions: "do something",
	}
	if err := m.Spawn(cfg); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Verify the expected API calls were made (no TAP → no MMDS calls).
	want := map[string]bool{
		"PUT /machine-config": false,
		"PUT /boot-source":    false,
		"PUT /drives/rootfs":  false,
		"PUT /actions":        false,
	}
	for _, req := range srv.requests {
		for k := range want {
			if req == k {
				want[k] = true
			}
		}
	}
	for path, seen := range want {
		if !seen {
			t.Errorf("expected API call %q was not made; got %v", path, srv.requests)
		}
	}
}

func TestHealth_UnknownAgent(t *testing.T) {
	m := newFirecrackerManager(shortTempDir(t), firecrackerConfig{vcpus: 1, memMiB: 128},
		func(_, _ string) (*exec.Cmd, error) { return nil, nil })
	status, err := m.Health("nobody")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if status.State != StateUnknown {
		t.Errorf("want StateUnknown, got %v", status.State)
	}
}

func TestHealth_RunningAgent(t *testing.T) {
	dir := shortTempDir(t)
	agentID := "health-agent"
	sockPath := filepath.Join(dir, agentID+".sock")
	srv := newMockFCServer(t, sockPath)
	defer srv.Close()

	m := newFirecrackerManager(dir, firecrackerConfig{vcpus: 1, memMiB: 128},
		func(_, _ string) (*exec.Cmd, error) { return nil, nil })

	if err := m.Spawn(VMConfig{
		AgentID: agentID, TaskID: "t", SkillDomain: "web",
		TraceID: "tr", Instructions: "x",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	status, err := m.Health(agentID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if status.State != StateRunning {
		t.Errorf("want StateRunning, got %v", status.State)
	}
}

func TestTerminate_UnknownAgent(t *testing.T) {
	m := newFirecrackerManager(shortTempDir(t), firecrackerConfig{vcpus: 1, memMiB: 128},
		func(_, _ string) (*exec.Cmd, error) { return nil, nil })
	if err := m.Terminate("nobody"); err == nil {
		t.Error("expected error when terminating unknown agent, got nil")
	}
}

func TestTerminate_RemovesFromMap(t *testing.T) {
	dir := shortTempDir(t)
	agentID := "term-agent"
	sockPath := filepath.Join(dir, agentID+".sock")
	srv := newMockFCServer(t, sockPath)
	defer srv.Close()

	m := newFirecrackerManager(dir, firecrackerConfig{vcpus: 1, memMiB: 128},
		func(_, _ string) (*exec.Cmd, error) { return nil, nil })

	if err := m.Spawn(VMConfig{
		AgentID: agentID, TaskID: "t", SkillDomain: "web",
		TraceID: "tr", Instructions: "x",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if err := m.Terminate(agentID); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	// After termination the agent must be unknown.
	status, _ := m.Health(agentID)
	if status.State != StateUnknown {
		t.Errorf("after Terminate: want StateUnknown, got %v", status.State)
	}
}

func TestMMDSSkippedWithoutTAP(t *testing.T) {
	dir := shortTempDir(t)
	agentID := "no-tap-agent"
	sockPath := filepath.Join(dir, agentID+".sock")
	srv := newMockFCServer(t, sockPath)
	defer srv.Close()

	// No TAP configured — MMDS steps must be skipped.
	m := newFirecrackerManager(dir, firecrackerConfig{vcpus: 1, memMiB: 128, tapName: ""},
		func(_, _ string) (*exec.Cmd, error) { return nil, nil })

	if err := m.Spawn(VMConfig{
		AgentID: agentID, TaskID: "t", SkillDomain: "web",
		TraceID: "tr", Instructions: "x",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	for _, req := range srv.requests {
		if req == "PUT /mmds/config" || req == "PUT /mmds" {
			t.Errorf("MMDS call %q must not be made without a TAP device", req)
		}
	}
}
