package lifecycle_test

import (
	"bytes"
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
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)

	var sc struct {
		TaskID       string `json:"task_id"`
		TraceID      string `json:"trace_id"`
		Instructions string `json:"instructions"`
	}
	if err := dec.Decode(&sc); err != nil {
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
	_ = enc.Encode(out)

	if sc.Instructions != "loop" {
		// One-shot contract — the classic behaviour expected by the stdin-
		// stream tests that drove a single SpawnContext to a bytes.Reader.
		return
	}

	// Loop mode: mimic the real agent-process lifetime. Keep reading fresh
	// SpawnContexts from stdin and emitting one newline-delimited TaskOutput
	// per request. Exit cleanly on EOF so Terminate() can drive the process
	// down by simply closing its stdin.
	for {
		if err := dec.Decode(&sc); err != nil {
			return
		}
		out.TaskID = sc.TaskID
		out.TraceID = sc.TraceID
		out.Result = "helper ok: " + sc.Instructions
		_ = enc.Encode(out)
	}
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

// TestProcessManagerDeliverReusesProcess verifies the core cold-start-tax
// elimination contract: Spawn launches one long-lived agent-process, the
// initial SpawnContext's OnComplete fires with the first TaskOutput, and
// Deliver then hands a second SpawnContext to the SAME process and receives a
// second TaskOutput via the swapped OnComplete callback — without ever forking
// a new subprocess. This is the warm-path that Priority 1 IDLE reuse depends
// on.
func TestProcessManagerDeliverReusesProcess(t *testing.T) {
	t.Setenv("TEST_LIFECYCLE_AGENT_HELPER", "1")

	m := lifecycle.NewProcess(os.Args[0])

	first := make(chan []byte, 1)
	cfg := lifecycle.VMConfig{
		AgentID:      "pm-reuse",
		TaskID:       "task-a",
		SkillDomain:  "web",
		Instructions: "loop",
		TraceID:      "tr-a",
		OnComplete: func(_ string, output []byte, exitErr error) {
			if exitErr != nil {
				t.Errorf("unexpected exitErr on first task: %v", exitErr)
			}
			first <- output
		},
	}
	if err := m.Spawn(cfg); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = m.Terminate("pm-reuse") })

	select {
	case raw := <-first:
		if !bytes.Contains(raw, []byte("task-a")) {
			t.Errorf("first TaskOutput missing task_id=task-a: %s", raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first TaskOutput")
	}

	second := make(chan []byte, 1)
	deliverCfg := lifecycle.VMConfig{
		AgentID:      "pm-reuse",
		TaskID:       "task-b",
		SkillDomain:  "web",
		Instructions: "second",
		TraceID:      "tr-b",
		OnComplete: func(_ string, output []byte, exitErr error) {
			if exitErr != nil {
				t.Errorf("unexpected exitErr on second task: %v", exitErr)
			}
			second <- output
		},
	}
	if err := m.Deliver("pm-reuse", deliverCfg); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	select {
	case raw := <-second:
		if !bytes.Contains(raw, []byte("task-b")) {
			t.Errorf("second TaskOutput missing task_id=task-b: %s", raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second TaskOutput")
	}

	// Confirm the process is still alive between tasks.
	if h, _ := m.Health("pm-reuse"); h.State != lifecycle.StateRunning {
		t.Errorf("want StateRunning between tasks, got %q", h.State)
	}
}

// TestProcessManagerDeliverUnknownAgent confirms Deliver fails cleanly when
// asked about an agent the manager has no record of.
func TestProcessManagerDeliverUnknownAgent(t *testing.T) {
	m := lifecycle.NewProcess(os.Args[0])
	err := m.Deliver("ghost", lifecycle.VMConfig{AgentID: "ghost", TaskID: "t", TraceID: "tr"})
	if err == nil {
		t.Error("expected error from Deliver on unknown agent, got nil")
	}
}

// TestFirecrackerReuseUnsupported guards the fallback contract: lifecycle
// callers that ask Firecracker to reuse a VM must get ErrReuseUnsupported so
// they can fall back to Spawn.
func TestStubSupportsReuse(t *testing.T) {
	if !lifecycle.New().SupportsReuse() {
		t.Error("stub manager should report SupportsReuse=true for factory tests")
	}
}

func TestProcessSupportsReuse(t *testing.T) {
	if !lifecycle.NewProcess(os.Args[0]).SupportsReuse() {
		t.Error("process manager must report SupportsReuse=true")
	}
}
