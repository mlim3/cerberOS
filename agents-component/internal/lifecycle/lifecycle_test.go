package lifecycle_test

import (
	"testing"

	"github.com/cerberOS/agents-component/internal/lifecycle"
)

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
	m.Spawn(cfg)

	if err := m.Spawn(cfg); err == nil {
		t.Error("expected error for duplicate spawn, got nil")
	}
}

func TestTerminate(t *testing.T) {
	m := lifecycle.New()
	m.Spawn(lifecycle.VMConfig{AgentID: "a1"})

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
