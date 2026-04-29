package registry_test

import (
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/memory"
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

// — vm_id tests ——————————————————————————————————————————————————————————

func TestRegisterPreservesVMID(t *testing.T) {
	r := registry.New()
	a := newAgent("a1", "web")
	a.VMID = "vm-initial-1"
	r.Register(a) //nolint:errcheck

	got, _ := r.Get("a1")
	if got.VMID != "vm-initial-1" {
		t.Errorf("VMID: want %q, got %q", "vm-initial-1", got.VMID)
	}
}

func TestSetVMID(t *testing.T) {
	r := registry.New()
	a := newAgent("a1", "web")
	a.VMID = "vm-old"
	r.Register(a) //nolint:errcheck

	if err := r.SetVMID("a1", "vm-new"); err != nil {
		t.Fatalf("SetVMID: %v", err)
	}

	got, _ := r.Get("a1")
	if got.VMID != "vm-new" {
		t.Errorf("VMID after SetVMID: want %q, got %q", "vm-new", got.VMID)
	}
}

func TestSetVMIDNotFound(t *testing.T) {
	r := registry.New()
	if err := r.SetVMID("ghost", "vm-x"); err == nil {
		t.Error("expected error for missing agent, got nil")
	}
}

// — failure_count tests ——————————————————————————————————————————————————

func TestFailureCountInitiallyZero(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web")) //nolint:errcheck

	got, _ := r.Get("a1")
	if got.FailureCount != 0 {
		t.Errorf("initial FailureCount: want 0, got %d", got.FailureCount)
	}
}

func TestFailureCountIncrementsOnRecovering(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web"))                   //nolint:errcheck
	r.UpdateState("a1", registry.StateSpawning, "test") //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "test")   //nolint:errcheck

	if err := r.UpdateState("a1", registry.StateRecovering, "process crashed"); err != nil {
		t.Fatalf("UpdateState →recovering: %v", err)
	}

	got, _ := r.Get("a1")
	if got.FailureCount != 1 {
		t.Errorf("FailureCount after first crash: want 1, got %d", got.FailureCount)
	}
}

func TestFailureCountAccumulatesAcrossMultipleCrashes(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web"))                   //nolint:errcheck
	r.UpdateState("a1", registry.StateSpawning, "test") //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "test")   //nolint:errcheck

	// Crash 1.
	r.UpdateState("a1", registry.StateRecovering, "crash 1") //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "recovered")   //nolint:errcheck

	// Crash 2.
	r.UpdateState("a1", registry.StateRecovering, "crash 2") //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "recovered")   //nolint:errcheck

	got, _ := r.Get("a1")
	if got.FailureCount != 2 {
		t.Errorf("FailureCount after 2 crashes: want 2, got %d", got.FailureCount)
	}
}

func TestFailureCountResetsOnIdle(t *testing.T) {
	r := registry.New()
	r.Register(newAgent("a1", "web"))                   //nolint:errcheck
	r.UpdateState("a1", registry.StateSpawning, "test") //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "test")   //nolint:errcheck

	// Accumulate two failures.
	r.UpdateState("a1", registry.StateRecovering, "crash 1") //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "recovered")   //nolint:errcheck
	r.UpdateState("a1", registry.StateRecovering, "crash 2") //nolint:errcheck
	r.UpdateState("a1", registry.StateActive, "recovered")   //nolint:errcheck

	// Successful task completion transitions to IDLE — resets FailureCount.
	if err := r.UpdateState("a1", registry.StateIdle, "task complete"); err != nil {
		t.Fatalf("UpdateState →idle: %v", err)
	}

	got, _ := r.Get("a1")
	if got.FailureCount != 0 {
		t.Errorf("FailureCount after successful completion: want 0, got %d", got.FailureCount)
	}
}

// — Persistence and startup recovery ————————————————————————————————————

// agentStateWrite builds the MemoryWrite payload that the registry itself uses
// when persisting an agent record, so tests can pre-populate the stub consistently.
func agentStateWrite(agent *types.AgentRecord) *types.MemoryWrite {
	return &types.MemoryWrite{
		AgentID:  agent.AgentID,
		DataType: registry.DataTypeAgentState,
		Payload:  agent,
		Tags:     map[string]string{"context": registry.DataTypeAgentState},
	}
}

func TestNewPersistentFirstBoot(t *testing.T) {
	mem := memory.New()
	r, err := registry.NewPersistent(mem)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	if got := r.List(); len(got) != 0 {
		t.Errorf("expected 0 agents on first boot, got %d", len(got))
	}
}

func TestNewPersistentRecoversAgents(t *testing.T) {
	mem := memory.New()
	now := time.Now().UTC()
	agent := &types.AgentRecord{
		AgentID:      "recovered-1",
		State:        registry.StateIdle,
		SkillDomains: []string{"web"},
		CreatedAt:    now,
		UpdatedAt:    now,
		StateHistory: []types.StateEvent{
			{State: registry.StatePending, Timestamp: now, Reason: "registered"},
			{State: registry.StateIdle, Timestamp: now, Reason: "task complete"},
		},
	}
	if err := mem.Write(agentStateWrite(agent)); err != nil {
		t.Fatalf("pre-populate memory: %v", err)
	}

	r, err := registry.NewPersistent(mem)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}

	got, err := r.Get("recovered-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != registry.StateIdle {
		t.Errorf("state: want %q, got %q", registry.StateIdle, got.State)
	}
	if len(got.SkillDomains) != 1 || got.SkillDomains[0] != "web" {
		t.Errorf("SkillDomains: want [web], got %v", got.SkillDomains)
	}
	if len(got.StateHistory) != 2 {
		t.Errorf("StateHistory length: want 2, got %d", len(got.StateHistory))
	}
}

func TestNewPersistentSkipsTerminatedAgents(t *testing.T) {
	mem := memory.New()
	now := time.Now().UTC()
	agent := &types.AgentRecord{
		AgentID:   "terminated-1",
		State:     registry.StateTerminated,
		CreatedAt: now,
		UpdatedAt: now,
	}
	mem.Write(agentStateWrite(agent)) //nolint:errcheck

	r, err := registry.NewPersistent(mem)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	if _, err := r.Get("terminated-1"); err == nil {
		t.Error("expected terminated agent to be excluded from recovery, but Get succeeded")
	}
}

func TestNewPersistentUsesLatestRecord(t *testing.T) {
	mem := memory.New()
	old := time.Now().UTC().Add(-time.Hour)
	recent := time.Now().UTC()

	// Older record: state = idle.
	mem.Write(agentStateWrite(&types.AgentRecord{ //nolint:errcheck
		AgentID:   "multi-1",
		State:     registry.StateIdle,
		CreatedAt: old,
		UpdatedAt: old,
	}))

	// Newer record: state = active.
	mem.Write(agentStateWrite(&types.AgentRecord{ //nolint:errcheck
		AgentID:   "multi-1",
		State:     registry.StateActive,
		CreatedAt: old,
		UpdatedAt: recent,
	}))

	r, err := registry.NewPersistent(mem)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	got, err := r.Get("multi-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != registry.StateActive {
		t.Errorf("expected latest state %q, got %q", registry.StateActive, got.State)
	}
}

func TestRegisterPersistsToMemory(t *testing.T) {
	mem := memory.New()
	r, err := registry.NewPersistent(mem)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}

	if err := r.Register(newAgent("persist-1", "web")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	records, err := mem.ReadAllByType(registry.DataTypeAgentState, "")
	if err != nil {
		t.Fatalf("ReadAllByType: %v", err)
	}
	var found bool
	for _, rec := range records {
		if rec.AgentID == "persist-1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected agent_state record for persist-1, not found in memory")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	mem := memory.New()

	// First "boot": register an agent and walk it to active.
	r1, err := registry.NewPersistent(mem)
	if err != nil {
		t.Fatalf("NewPersistent (boot 1): %v", err)
	}
	r1.Register(newAgent("rt-1", "web"))               //nolint:errcheck
	r1.UpdateState("rt-1", registry.StateSpawning, "") //nolint:errcheck
	r1.UpdateState("rt-1", registry.StateActive, "")   //nolint:errcheck

	// Second "boot": create a new registry from the same memory stub.
	r2, err := registry.NewPersistent(mem)
	if err != nil {
		t.Fatalf("NewPersistent (boot 2): %v", err)
	}
	got, err := r2.Get("rt-1")
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if got.State != registry.StateActive {
		t.Errorf("recovered state: want %q, got %q", registry.StateActive, got.State)
	}
	if len(got.SkillDomains) != 1 || got.SkillDomains[0] != "web" {
		t.Errorf("recovered SkillDomains: want [web], got %v", got.SkillDomains)
	}
}

// — StateHistory immutability ————————————————————————————————————————————

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
