package lifecycle_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/lifecycle"
)

// TestMain runs the in-process agent helper when the test binary is re-invoked
// as a fake agent-process subprocess. The processManager tests spawn this test
// binary itself (os.Args[0]) with TEST_LIFECYCLE_AGENT_HELPER=1 in the env.
//
// The helper reads a SpawnContext-shaped JSON from stdin:
//   - instructions == "sleep" → block until killed (used by TestProcessManagerTerminate)
//   - otherwise            → write a success TaskOutput and exit
func TestMain(m *testing.M) {
	if os.Getenv("TEST_LIFECYCLE_AGENT_HELPER") == "1" {
		agentHelper()
		return
	}
	os.Exit(m.Run())
}

func agentHelper() {
	var sc struct {
		TaskID       string `json:"task_id"`
		TraceID      string `json:"trace_id"`
		Instructions string `json:"instructions"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&sc); err != nil {
		os.Exit(1)
	}

	if sc.Instructions == "sleep" {
		// Block until the process is killed by Terminate().
		select {}
	}

	out := struct {
		TaskID  string `json:"task_id"`
		TraceID string `json:"trace_id"`
		Success bool   `json:"success"`
		Result  string `json:"result"`
	}{TaskID: sc.TaskID, TraceID: sc.TraceID, Success: true, Result: "helper ok"}

	_ = json.NewEncoder(os.Stdout).Encode(out)
}

// ─── Stub manager tests ──────────────────────────────────────────────────────

func TestSpawnAndHealth(t *testing.T) {
	m := lifecycle.New()
	cfg := lifecycle.VMConfig{AgentID: "a1", SkillDomain: "web", TraceID: "t1"}

	if err := m.Spawn(cfg); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	h, err := m.Health("a1")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State != lifecycle.StateRunning {
		t.Errorf("got state %q, want %q", h.State, lifecycle.StateRunning)
	}
}

func TestSpawnDuplicate(t *testing.T) {
	m := lifecycle.New()
	cfg := lifecycle.VMConfig{AgentID: "a1"}
	_ = m.Spawn(cfg)

	if err := m.Spawn(cfg); err == nil {
		t.Error("expected error for duplicate spawn, got nil")
	}
}

func TestTerminate(t *testing.T) {
	m := lifecycle.New()
	_ = m.Spawn(lifecycle.VMConfig{AgentID: "a1"})

	if err := m.Terminate("a1"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	h, _ := m.Health("a1")
	if h.State != lifecycle.StateUnknown {
		t.Errorf("got state %q after termination, want %q", h.State, lifecycle.StateUnknown)
	}
}

func TestHealthUnknownAgent(t *testing.T) {
	m := lifecycle.New()
	h, err := m.Health("ghost")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State != lifecycle.StateUnknown {
		t.Errorf("got state %q, want %q", h.State, lifecycle.StateUnknown)
	}
}

// ─── Process manager tests ───────────────────────────────────────────────────
//
// These tests re-invoke the test binary itself (os.Args[0]) as a fake
// agent-process so no pre-built binary is required.

func TestProcessManagerSpawnAndHealth(t *testing.T) {
	t.Setenv("TEST_LIFECYCLE_AGENT_HELPER", "1")

	m := lifecycle.NewProcess(os.Args[0])
	cfg := lifecycle.VMConfig{
		AgentID:      "pm1",
		TaskID:       "task-pm1",
		SkillDomain:  "web",
		Instructions: "do thing",
		TraceID:      "tr1",
	}

	if err := m.Spawn(cfg); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	h, err := m.Health("pm1")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State != lifecycle.StateRunning {
		t.Errorf("got state %q immediately after Spawn, want %q", h.State, lifecycle.StateRunning)
	}
}

func TestProcessManagerSpawnDuplicate(t *testing.T) {
	t.Setenv("TEST_LIFECYCLE_AGENT_HELPER", "1")

	m := lifecycle.NewProcess(os.Args[0])
	cfg := lifecycle.VMConfig{
		AgentID:      "pm-dup",
		TaskID:       "task-dup",
		SkillDomain:  "web",
		Instructions: "do thing",
		TraceID:      "tr1",
	}

	if err := m.Spawn(cfg); err != nil {
		t.Fatalf("first Spawn: %v", err)
	}
	if err := m.Spawn(cfg); err == nil {
		t.Error("expected error for duplicate spawn, got nil")
	}
	// Clean up.
	_ = m.Terminate("pm-dup")
}

func TestProcessManagerTerminate(t *testing.T) {
	t.Setenv("TEST_LIFECYCLE_AGENT_HELPER", "1")

	m := lifecycle.NewProcess(os.Args[0])
	cfg := lifecycle.VMConfig{
		AgentID:      "pm-term",
		TaskID:       "task-term",
		SkillDomain:  "web",
		Instructions: "sleep", // causes helper to block until killed
		TraceID:      "tr1",
	}

	if err := m.Spawn(cfg); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Confirm running.
	h, _ := m.Health("pm-term")
	if h.State != lifecycle.StateRunning {
		t.Fatalf("expected running after Spawn, got %q", h.State)
	}

	if err := m.Terminate("pm-term"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	// After Terminate the entry is removed from the registry.
	h, _ = m.Health("pm-term")
	if h.State != lifecycle.StateUnknown {
		t.Errorf("got state %q after Terminate, want %q", h.State, lifecycle.StateUnknown)
	}
}

func TestProcessManagerHealthAfterExit(t *testing.T) {
	t.Setenv("TEST_LIFECYCLE_AGENT_HELPER", "1")

	m := lifecycle.NewProcess(os.Args[0])
	cfg := lifecycle.VMConfig{
		AgentID:      "pm-exit",
		TaskID:       "task-exit",
		SkillDomain:  "web",
		Instructions: "finish", // helper exits immediately
		TraceID:      "tr1",
	}

	if err := m.Spawn(cfg); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Wait for the helper process to exit naturally.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h, _ := m.Health("pm-exit")
		if h.State == lifecycle.StateStopped {
			return // test passed
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("process did not reach StateStopped within 5 s")
}

func TestProcessManagerUnknownAgent(t *testing.T) {
	m := lifecycle.NewProcess(os.Args[0])
	h, err := m.Health("ghost")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State != lifecycle.StateUnknown {
		t.Errorf("got state %q, want %q", h.State, lifecycle.StateUnknown)
	}
}
