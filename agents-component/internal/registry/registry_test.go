package registry_test

import (
	"testing"

	"github.com/aegis/aegis-agents/internal/registry"
	"github.com/aegis/aegis-agents/pkg/types"
)

func newAgent(id string, domains ...string) *types.AgentRecord {
	return &types.AgentRecord{
		AgentID:      id,
		State:        "idle",
		SkillDomains: domains,
	}
}

func TestRegisterAndGet(t *testing.T) {
	r := registry.New()

	if err := r.Register(newAgent("a1", "web")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.Get("a1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AgentID != "a1" {
		t.Errorf("got AgentID %q, want %q", got.AgentID, "a1")
	}
}

func TestRegisterDuplicate(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web"))

	if err := r.Register(newAgent("a1", "web")); err == nil {
		t.Error("expected error for duplicate AgentID, got nil")
	}
}

func TestGetNotFound(t *testing.T) {
	r := registry.New()
	if _, err := r.Get("missing"); err == nil {
		t.Error("expected error for missing agent, got nil")
	}
}

func TestFindBySkills(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web", "data"))
	r.Register(newAgent("a2", "comms"))

	matches, err := r.FindBySkills([]string{"web"})
	if err != nil {
		t.Fatalf("FindBySkills: %v", err)
	}
	if len(matches) != 1 || matches[0].AgentID != "a1" {
		t.Errorf("unexpected matches: %+v", matches)
	}
}

func TestUpdateState(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web"))

	if err := r.UpdateState("a1", "active"); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	got, _ := r.Get("a1")
	if got.State != "active" {
		t.Errorf("got state %q, want %q", got.State, "active")
	}
}

func TestDeregister(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web"))

	if err := r.Deregister("a1"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if _, err := r.Get("a1"); err == nil {
		t.Error("expected error after deregister, got nil")
	}
}
