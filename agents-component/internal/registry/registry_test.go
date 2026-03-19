package registry_test

import (
	"testing"

	"github.com/cerberOS/agents-component/internal/registry"
	"github.com/cerberOS/agents-component/pkg/types"
)

func newAgent(id string, domains ...string) *types.AgentRecord {
	return &types.AgentRecord{
		AgentID:      id,
		SkillDomains: domains,
		// State is intentionally omitted — Register always forces StatePending.
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
	// Register must force the initial state to PENDING.
	if got.State != registry.StatePending {
		t.Errorf("initial state: want %q, got %q", registry.StatePending, got.State)
	}
}

func TestRegisterSetsInitialHistory(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web")) //nolint:errcheck

	got, _ := r.Get("a1")
	if len(got.StateHistory) != 1 {
		t.Fatalf("expected 1 history entry after Register, got %d", len(got.StateHistory))
	}
	if got.StateHistory[0].State != registry.StatePending {
		t.Errorf("first history state: want %q, got %q", registry.StatePending, got.StateHistory[0].State)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web")) //nolint:errcheck

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
	r.Register(newAgent("a1", "web", "data")) //nolint:errcheck
	r.Register(newAgent("a2", "comms"))       //nolint:errcheck

	matches, err := r.FindBySkills([]string{"web"})
	if err != nil {
		t.Fatalf("FindBySkills: %v", err)
	}
	if len(matches) != 1 || matches[0].AgentID != "a1" {
		t.Errorf("unexpected matches: %+v", matches)
	}
}

func TestFindBySkillsExcludesTerminated(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web"))                     //nolint:errcheck
	r.UpdateState("a1", registry.StateSpawning, "test")   //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "test")     //nolint:errcheck
	r.UpdateState("a1", registry.StateIdle, "test")       //nolint:errcheck
	r.UpdateState("a1", registry.StateTerminated, "test") //nolint:errcheck

	matches, _ := r.FindBySkills([]string{"web"})
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for terminated agent, got %d", len(matches))
	}
}

func TestUpdateStateValidTransition(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web")) //nolint:errcheck

	if err := r.UpdateState("a1", registry.StateSpawning, "transitioning"); err != nil {
		t.Fatalf("UpdateState pending→spawning: %v", err)
	}

	got, _ := r.Get("a1")
	if got.State != registry.StateSpawning {
		t.Errorf("state: want %q, got %q", registry.StateSpawning, got.State)
	}
	if len(got.StateHistory) != 2 {
		t.Errorf("history length: want 2, got %d", len(got.StateHistory))
	}
}

func TestUpdateStateInvalidTransition(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web")) //nolint:errcheck
	// PENDING → ACTIVE is not a valid transition (must go through SPAWNING).
	if err := r.UpdateState("a1", registry.StateActive, "skip spawning"); err == nil {
		t.Error("expected error for invalid transition pending→active, got nil")
	}
}

func TestUpdateStateTerminalStateRejected(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web"))                     //nolint:errcheck
	r.UpdateState("a1", registry.StateSpawning, "test")   //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "test")     //nolint:errcheck
	r.UpdateState("a1", registry.StateIdle, "test")       //nolint:errcheck
	r.UpdateState("a1", registry.StateTerminated, "test") //nolint:errcheck

	// No transition out of TERMINATED is permitted.
	if err := r.UpdateState("a1", registry.StatePending, "restart"); err == nil {
		t.Error("expected error transitioning out of TERMINATED, got nil")
	}
}

func TestFullProvisionalWalk(t *testing.T) {
	// Walk: PENDING → SPAWNING → ACTIVE → IDLE → TERMINATED
	r := registry.New()
	r.Register(newAgent("a1", "web")) //nolint:errcheck

	steps := []struct {
		to     string
		reason string
	}{
		{registry.StateSpawning, "spawning"},
		{registry.StateActive, "active"},
		{registry.StateIdle, "idle"},
		{registry.StateTerminated, "terminated"},
	}
	for _, step := range steps {
		if err := r.UpdateState("a1", step.to, step.reason); err != nil {
			t.Fatalf("UpdateState →%q: %v", step.to, err)
		}
	}

	got, _ := r.Get("a1")
	// 1 initial entry from Register + 4 transitions = 5 total.
	if len(got.StateHistory) != 5 {
		t.Errorf("history length: want 5, got %d", len(got.StateHistory))
	}
}

func TestRecoveryWalk(t *testing.T) {
	// Walk: PENDING → SPAWNING → ACTIVE → RECOVERING → ACTIVE → TERMINATED (via IDLE)
	r := registry.New()
	r.Register(newAgent("a1", "web"))                     //nolint:errcheck
	r.UpdateState("a1", registry.StateSpawning, "test")   //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "test")     //nolint:errcheck
	r.UpdateState("a1", registry.StateRecovering, "test") //nolint:errcheck

	if err := r.UpdateState("a1", registry.StateActive, "recovered"); err != nil {
		t.Fatalf("UpdateState recovering→active: %v", err)
	}
}

func TestSuspendedWalk(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web"))                    //nolint:errcheck
	r.UpdateState("a1", registry.StateSpawning, "test")  //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "test")    //nolint:errcheck
	r.UpdateState("a1", registry.StateIdle, "test")      //nolint:errcheck
	r.UpdateState("a1", registry.StateSuspended, "test") //nolint:errcheck

	// SUSPENDED → ACTIVE is valid (agent woken to handle a new task).
	if err := r.UpdateState("a1", registry.StateActive, "woken"); err != nil {
		t.Fatalf("UpdateState suspended→active: %v", err)
	}
}

func TestAssignTask(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web"))                   //nolint:errcheck
	r.UpdateState("a1", registry.StateSpawning, "test") //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "test")   //nolint:errcheck
	r.UpdateState("a1", registry.StateIdle, "test")     //nolint:errcheck

	if err := r.AssignTask("a1", "task-99"); err != nil {
		t.Fatalf("AssignTask: %v", err)
	}
	got, _ := r.Get("a1")
	if got.State != registry.StateActive {
		t.Errorf("state after AssignTask: want %q, got %q", registry.StateActive, got.State)
	}
	if got.AssignedTask != "task-99" {
		t.Errorf("AssignedTask: want %q, got %q", "task-99", got.AssignedTask)
	}
}

func TestAssignTaskFromInvalidState(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web")) //nolint:errcheck
	// Agent is in PENDING — AssignTask requires IDLE or SUSPENDED.
	if err := r.AssignTask("a1", "task-x"); err == nil {
		t.Error("expected error for AssignTask from PENDING state, got nil")
	}
}

func TestDeregister(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web")) //nolint:errcheck

	if err := r.Deregister("a1"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if _, err := r.Get("a1"); err == nil {
		t.Error("expected error after deregister, got nil")
	}
}

func TestStateHistoryImmutability(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web")) //nolint:errcheck

	// Mutating the slice returned by Get must not affect the stored record.
	got, _ := r.Get("a1")
	got.StateHistory[0].Reason = "tampered"

	stored, _ := r.Get("a1")
	if stored.StateHistory[0].Reason == "tampered" {
		t.Error("StateHistory is not copy-on-read: external mutation affected stored record")
	}
}
