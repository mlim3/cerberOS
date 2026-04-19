package factory_test

// Recovery sequence tests for HandleCrash (EDD §6.3).
//
// Each test provisions a fresh agent via HandleTaskSpec, then calls HandleCrash
// directly to simulate the CrashDetector callback firing. The test verifies the
// expected state transitions, registry mutations, and memory writes.

import (
	"encoding/json"
	"testing"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/internal/credentials"
	"github.com/cerberOS/agents-component/internal/factory"
	"github.com/cerberOS/agents-component/internal/lifecycle"
	"github.com/cerberOS/agents-component/internal/memory"
	"github.com/cerberOS/agents-component/internal/registry"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

// idSeq returns successive IDs from ids on each call. Panics if exhausted.
func idSeq(ids ...string) factory.IDGenerator {
	i := 0
	return func() string {
		if i >= len(ids) {
			panic("idSeq exhausted")
		}
		id := ids[i]
		i++
		return id
	}
}

// newRecoveryFactory wires a factory with a configurable maxRetries and ID sequence.
func newRecoveryFactory(t *testing.T, maxRetries int, ids ...string) (*factory.Factory, registry.Registry, memory.Client) {
	t.Helper()
	sm := skills.New()
	sm.RegisterDomain(webDomain())

	reg := registry.New()
	mem := memory.New()

	f, err := factory.New(factory.Config{
		Registry:    reg,
		Skills:      sm,
		Credentials: credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:   lifecycle.New(),
		Memory:      mem,
		Comms:       comms.NewStubClient(),
		GenerateID:  idSeq(ids...),
		MaxRetries:  maxRetries,
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}
	return f, reg, mem
}

// provisionAgent calls HandleTaskSpec and returns the agent_id used.
func provisionAgent(t *testing.T, f *factory.Factory, agentID, taskID string) {
	t.Helper()
	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         taskID,
		RequiredSkills: []string{"web"},
		TraceID:        "trace-recovery",
	}); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}
}

// TestHandleCrashTransitionsToRecoveringThenActive verifies the state walk:
// ACTIVE → RECOVERING → ACTIVE on a successful single crash + respawn.
func TestHandleCrashTransitionsToRecoveringThenActive(t *testing.T) {
	// IDs: first call → agent_id (provision), second call → vm_id (provision),
	// third call → new_vm_id (respawn).
	f, reg, _ := newRecoveryFactory(t, 3, "agent-r1", "vm-r1", "vm-r1-new")
	provisionAgent(t, f, "agent-r1", "task-r1")

	// Agent is ACTIVE after provisioning.
	agentBefore, _ := reg.Get("agent-r1")
	if agentBefore.State != registry.StateActive {
		t.Fatalf("expected ACTIVE before crash, got %q", agentBefore.State)
	}

	if err := f.HandleCrash("agent-r1"); err != nil {
		t.Fatalf("HandleCrash: %v", err)
	}

	agentAfter, err := reg.Get("agent-r1")
	if err != nil {
		t.Fatalf("registry.Get after recovery: %v", err)
	}

	// Final state must be ACTIVE (respawn succeeded).
	if agentAfter.State != registry.StateActive {
		t.Errorf("state after recovery: want %q, got %q", registry.StateActive, agentAfter.State)
	}

	// failure_count must be 1 (incremented by → RECOVERING).
	if agentAfter.FailureCount != 1 {
		t.Errorf("failure_count: want 1, got %d", agentAfter.FailureCount)
	}

	// vm_id must have changed to the new one.
	if agentAfter.VMID != "vm-r1-new" {
		t.Errorf("vm_id after respawn: want %q, got %q", "vm-r1-new", agentAfter.VMID)
	}
}

// TestHandleCrashStateHistoryRecordsRecovery verifies that RECOVERING appears
// in the state_history append-only log.
func TestHandleCrashStateHistoryRecordsRecovery(t *testing.T) {
	f, reg, _ := newRecoveryFactory(t, 3, "agent-hist", "vm-hist", "vm-hist-new")
	provisionAgent(t, f, "agent-hist", "task-hist")

	if err := f.HandleCrash("agent-hist"); err != nil {
		t.Fatalf("HandleCrash: %v", err)
	}

	agent, _ := reg.Get("agent-hist")

	// Collect all states from history.
	var states []string
	for _, e := range agent.StateHistory {
		states = append(states, e.State)
	}

	// RECOVERING must appear in the history.
	found := false
	for _, s := range states {
		if s == registry.StateRecovering {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("state_history missing %q; got: %v", registry.StateRecovering, states)
	}
}

// TestHandleCrashSavesSnapshotToMemory verifies that a "snapshot" MemoryWrite
// with context tag "crash_snapshot" is persisted before any state mutation.
func TestHandleCrashSavesSnapshotToMemory(t *testing.T) {
	f, _, mem := newRecoveryFactory(t, 3, "agent-snap", "vm-snap", "vm-snap-new")
	provisionAgent(t, f, "agent-snap", "task-snap")

	if err := f.HandleCrash("agent-snap"); err != nil {
		t.Fatalf("HandleCrash: %v", err)
	}

	records, err := mem.Read("agent-snap", "crash_snapshot", "")
	if err != nil {
		t.Fatalf("memory.Read: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected crash_snapshot in memory, got none")
	}

	r := records[0]
	if r.DataType != "snapshot" {
		t.Errorf("DataType: want %q, got %q", "snapshot", r.DataType)
	}
	if r.Tags["context"] != "crash_snapshot" {
		t.Errorf("tag context: want %q, got %q", "crash_snapshot", r.Tags["context"])
	}
}

// TestHandleCrashSnapshotContainsUnknownVaultRequestIDs verifies that in-flight
// Vault request_ids are captured in the crash snapshot with UNKNOWN status
// (EDD §6.3, Step 2).
func TestHandleCrashSnapshotContainsUnknownVaultRequestIDs(t *testing.T) {
	f, _, mem := newRecoveryFactory(t, 3, "agent-vault", "vm-vault", "vm-vault-new")
	provisionAgent(t, f, "agent-vault", "task-vault")

	// Simulate two in-flight Vault requests that never received a result.
	f.TrackVaultRequest("agent-vault", "req-001")
	f.TrackVaultRequest("agent-vault", "req-002")

	if err := f.HandleCrash("agent-vault"); err != nil {
		t.Fatalf("HandleCrash: %v", err)
	}

	records, err := mem.Read("agent-vault", "crash_snapshot", "")
	if err != nil || len(records) == 0 {
		t.Fatal("expected crash_snapshot in memory")
	}

	// Unmarshal the Payload as a CrashSnapshot.
	raw, err := json.Marshal(records[0].Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var snap types.CrashSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal CrashSnapshot: %v", err)
	}

	if len(snap.UnknownVaultRequestIDs) != 2 {
		t.Errorf("UnknownVaultRequestIDs: want 2, got %d: %v", len(snap.UnknownVaultRequestIDs), snap.UnknownVaultRequestIDs)
	}
}

// TestHandleCrashCompleteVaultRequestClearsInflight verifies that a completed
// Vault request is NOT flagged UNKNOWN in the crash snapshot.
func TestHandleCrashCompleteVaultRequestClearsInflight(t *testing.T) {
	f, _, mem := newRecoveryFactory(t, 3, "agent-clr", "vm-clr", "vm-clr-new")
	provisionAgent(t, f, "agent-clr", "task-clr")

	// Track two requests; complete one before the crash.
	f.TrackVaultRequest("agent-clr", "req-A")
	f.TrackVaultRequest("agent-clr", "req-B")
	f.CompleteVaultRequest("agent-clr", "req-A") // req-A resolved

	if err := f.HandleCrash("agent-clr"); err != nil {
		t.Fatalf("HandleCrash: %v", err)
	}

	records, err := mem.Read("agent-clr", "crash_snapshot", "")
	if err != nil || len(records) == 0 {
		t.Fatal("expected crash_snapshot in memory")
	}

	raw, _ := json.Marshal(records[0].Payload)
	var snap types.CrashSnapshot
	json.Unmarshal(raw, &snap) //nolint:errcheck

	// Only req-B should be flagged UNKNOWN.
	if len(snap.UnknownVaultRequestIDs) != 1 || snap.UnknownVaultRequestIDs[0] != "req-B" {
		t.Errorf("UnknownVaultRequestIDs: want [req-B], got %v", snap.UnknownVaultRequestIDs)
	}
}

// TestHandleCrashExceedsMaxRetriesTerminates verifies that an agent is permanently
// terminated once failure_count reaches maxRetries (EDD §6.3, Step 3).
func TestHandleCrashExceedsMaxRetriesTerminates(t *testing.T) {
	// maxRetries = 2: two crashes → respawn twice (FC=1, FC=2); third crash → terminate.
	// IDs: agent, vm0 (provision), vm1 (respawn 1), vm2 (respawn 2).
	// HandleCrash #3 does not spawn a new VM, so only 3 vm IDs are needed.
	f, reg, _ := newRecoveryFactory(t, 2, "agent-term", "vm0", "vm1", "vm2")
	provisionAgent(t, f, "agent-term", "task-term")

	// Crash 1: failure_count → 1. 1 < 2 → respawn.
	if err := f.HandleCrash("agent-term"); err != nil {
		t.Fatalf("HandleCrash #1: %v", err)
	}
	a, _ := reg.Get("agent-term")
	if a.State != registry.StateActive {
		t.Fatalf("after crash 1: want ACTIVE, got %q", a.State)
	}

	// Crash 2: failure_count → 2. 2 >= 2 → TERMINATED.
	if err := f.HandleCrash("agent-term"); err != nil {
		t.Fatalf("HandleCrash #2: %v", err)
	}
	a, err := reg.Get("agent-term")
	if err != nil {
		t.Fatalf("registry.Get after termination: %v", err)
	}
	if a.State != registry.StateTerminated {
		t.Errorf("after crash 2 (max retries): want %q, got %q", registry.StateTerminated, a.State)
	}
}

// TestHandleCrashUnknownAgent returns an error for an agent not in the registry.
func TestHandleCrashUnknownAgent(t *testing.T) {
	f, _, _ := newRecoveryFactory(t, 3, "agent-unk", "vm-unk")
	if err := f.HandleCrash("does-not-exist"); err == nil {
		t.Error("expected error for unknown agent, got nil")
	}
}
