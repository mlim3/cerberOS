package factory_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/internal/credentials"
	"github.com/cerberOS/agents-component/internal/factory"
	"github.com/cerberOS/agents-component/internal/lifecycle"
	"github.com/cerberOS/agents-component/internal/memory"
	"github.com/cerberOS/agents-component/internal/registry"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

func webDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:     "web",
		Level:    "domain",
		Children: map[string]*types.SkillNode{},
	}
}

func newFactory(t *testing.T, id string) *factory.Factory {
	t.Helper()
	sm := skills.New()
	sm.RegisterDomain(webDomain())

	f, err := factory.New(factory.Config{
		Registry:    registry.New(),
		Skills:      sm,
		Credentials: credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:   lifecycle.New(),
		Memory:      memory.New(),
		Comms:       comms.NewStubClient(),
		GenerateID:  func() string { return id },
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}
	return f
}

func TestHandleTaskSpecProvisions(t *testing.T) {
	f := newFactory(t, "agent-test-1")

	spec := &types.TaskSpec{
		TaskID:         "task-1",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-1",
	}
	if err := f.HandleTaskSpec(spec); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}
}

func TestHandleTaskSpecNilSpec(t *testing.T) {
	f := newFactory(t, "agent-test-2")
	if err := f.HandleTaskSpec(nil); err == nil {
		t.Error("expected error for nil spec, got nil")
	}
}

func TestHandleTaskSpecUnknownDomain(t *testing.T) {
	f := newFactory(t, "agent-test-3")
	spec := &types.TaskSpec{
		TaskID:         "task-x",
		RequiredSkills: []string{"unknown-domain"},
		TraceID:        "trace-x",
	}
	if err := f.HandleTaskSpec(spec); err == nil {
		t.Error("expected error for unknown domain, got nil")
	}
}

func TestTaskAcceptedPublishedBeforeProvisioning(t *testing.T) {
	c := comms.NewStubClient()

	sm := skills.New()
	sm.RegisterDomain(webDomain())

	var accepted []types.TaskAccepted
	// Subscribe before wiring the factory so we capture the very first publication.
	if err := c.Subscribe(comms.SubjectTaskAccepted, func(msg *comms.Message) {
		var ta types.TaskAccepted
		if err := json.Unmarshal(msg.Data, &ta); err != nil {
			t.Errorf("unmarshal TaskAccepted: %v", err)
			return
		}
		accepted = append(accepted, ta)
	}); err != nil {
		t.Fatalf("subscribe task.accepted: %v", err)
	}

	f, err := factory.New(factory.Config{
		Registry:    registry.New(),
		Skills:      sm,
		Credentials: credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:   lifecycle.New(),
		Memory:      memory.New(),
		Comms:       c,
		GenerateID:  func() string { return "agent-accepted-test" },
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	spec := &types.TaskSpec{
		TaskID:         "task-accepted-1",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-accepted-1",
		UserContextID:  "user-ctx-42",
	}
	if err := f.HandleTaskSpec(spec); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}

	if len(accepted) == 0 {
		t.Fatal("expected task.accepted to be published, got none")
	}
	ta := accepted[0]
	if ta.TaskID != spec.TaskID {
		t.Errorf("TaskID: want %q, got %q", spec.TaskID, ta.TaskID)
	}
	if ta.AgentID == "" {
		t.Error("AgentID must not be empty in task.accepted")
	}
	if ta.AgentType != "new_provision" {
		t.Errorf("AgentType: want %q, got %q", "new_provision", ta.AgentType)
	}
	if ta.UserContextID != spec.UserContextID {
		t.Errorf("UserContextID: want %q, got %q", spec.UserContextID, ta.UserContextID)
	}
	if ta.TraceID != spec.TraceID {
		t.Errorf("TraceID: want %q, got %q", spec.TraceID, ta.TraceID)
	}
}

func TestTaskAcceptedExistingAgent(t *testing.T) {
	c := comms.NewStubClient()

	sm := skills.New()
	sm.RegisterDomain(webDomain())

	reg := registry.New()
	f, err := factory.New(factory.Config{
		Registry:    reg,
		Skills:      sm,
		Credentials: credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:   lifecycle.New(),
		Memory:      memory.New(),
		Comms:       c,
		GenerateID:  func() string { return "agent-reuse-test" },
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	// Provision first agent.
	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-first",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-first",
	}); err != nil {
		t.Fatalf("HandleTaskSpec (first): %v", err)
	}

	// Mark idle so second task can reuse it.
	if err := reg.UpdateState("agent-reuse-test", registry.StateIdle, "test: marking idle for reuse"); err != nil {
		t.Fatalf("UpdateState idle: %v", err)
	}

	var accepted []types.TaskAccepted
	if err := c.Subscribe(comms.SubjectTaskAccepted, func(msg *comms.Message) {
		var ta types.TaskAccepted
		if err := json.Unmarshal(msg.Data, &ta); err != nil {
			t.Errorf("unmarshal TaskAccepted: %v", err)
			return
		}
		accepted = append(accepted, ta)
	}); err != nil {
		t.Fatalf("subscribe task.accepted: %v", err)
	}

	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-second",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-second",
	}); err != nil {
		t.Fatalf("HandleTaskSpec (second): %v", err)
	}

	if len(accepted) == 0 {
		t.Fatal("expected task.accepted for reused agent, got none")
	}
	if accepted[0].AgentType != "existing_assigned" {
		t.Errorf("AgentType: want %q, got %q", "existing_assigned", accepted[0].AgentType)
	}
	if accepted[0].AgentID != "agent-reuse-test" {
		t.Errorf("AgentID: want %q, got %q", "agent-reuse-test", accepted[0].AgentID)
	}
}

// stubTokenCounter is a TokenCounter that always returns a fixed count.
type stubTokenCounter struct{ count int }

func (s *stubTokenCounter) CountTokens(_ string) (int, error) { return s.count, nil }

func newFactoryWithCounter(t *testing.T, id string, counter factory.TokenCounter) *factory.Factory {
	t.Helper()
	sm := skills.New()
	sm.RegisterDomain(webDomain())

	f, err := factory.New(factory.Config{
		Registry:     registry.New(),
		Skills:       sm,
		Credentials:  credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:    lifecycle.New(),
		Memory:       memory.New(),
		Comms:        comms.NewStubClient(),
		GenerateID:   func() string { return id },
		TokenCounter: counter,
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}
	return f
}

func TestContextBudgetExceeded(t *testing.T) {
	c := comms.NewStubClient()
	sm := skills.New()
	sm.RegisterDomain(webDomain())

	var failed []types.TaskFailed
	if err := c.Subscribe(comms.SubjectTaskFailed, func(msg *comms.Message) {
		var tf types.TaskFailed
		if err := json.Unmarshal(msg.Data, &tf); err != nil {
			t.Errorf("unmarshal TaskFailed: %v", err)
			return
		}
		failed = append(failed, tf)
	}); err != nil {
		t.Fatalf("subscribe task.failed: %v", err)
	}

	f, err := factory.New(factory.Config{
		Registry:     registry.New(),
		Skills:       sm,
		Credentials:  credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:    lifecycle.New(),
		Memory:       memory.New(),
		Comms:        c,
		GenerateID:   func() string { return "agent-budget-test" },
		TokenCounter: &stubTokenCounter{count: 2049}, // one over the 2,048 limit
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	spec := &types.TaskSpec{
		TaskID:         "task-budget-1",
		RequiredSkills: []string{"web"},
		Instructions:   "some instructions",
		TraceID:        "trace-budget-1",
	}
	if err := f.HandleTaskSpec(spec); err == nil {
		t.Fatal("expected error when context budget exceeded, got nil")
	}

	if len(failed) == 0 {
		t.Fatal("expected task.failed to be published, got none")
	}
	tf := failed[0]
	if tf.ErrorCode != "CONTEXT_BUDGET_EXCEEDED" {
		t.Errorf("ErrorCode: want %q, got %q", "CONTEXT_BUDGET_EXCEEDED", tf.ErrorCode)
	}
	if tf.Phase != "skill_resolution" {
		t.Errorf("Phase: want %q, got %q", "skill_resolution", tf.Phase)
	}
	if tf.TaskID != spec.TaskID {
		t.Errorf("TaskID: want %q, got %q", spec.TaskID, tf.TaskID)
	}
	if tf.AgentID == "" {
		t.Error("AgentID must not be empty in task.failed for CONTEXT_BUDGET_EXCEEDED")
	}
}

func TestContextBudgetWithinLimit(t *testing.T) {
	// TokenCounter returning exactly 2,048 must not abort provisioning.
	f := newFactoryWithCounter(t, "agent-budget-ok", &stubTokenCounter{count: 2048})
	spec := &types.TaskSpec{
		TaskID:         "task-budget-ok",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-budget-ok",
	}
	if err := f.HandleTaskSpec(spec); err != nil {
		t.Fatalf("HandleTaskSpec: unexpected error at budget boundary: %v", err)
	}
}

func TestContextBudgetNoCounter(t *testing.T) {
	// When no TokenCounter is configured, provisioning proceeds without a budget check.
	f := newFactory(t, "agent-no-counter")
	spec := &types.TaskSpec{
		TaskID:         "task-no-counter",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-no-counter",
	}
	if err := f.HandleTaskSpec(spec); err != nil {
		t.Fatalf("HandleTaskSpec: unexpected error without token counter: %v", err)
	}
}

func TestCompleteTask(t *testing.T) {
	f := newFactory(t, "agent-complete-1")
	spec := &types.TaskSpec{
		TaskID:         "task-2",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-2",
	}
	if err := f.HandleTaskSpec(spec); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}
	if err := f.CompleteTask("agent-complete-1", "session-1", "trace-2", "result output", nil); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
}

// ── OQ-03 / OQ-06 tests ──────────────────────────────────────────────────────

func newSuspendFactory(t *testing.T, id string) (*factory.Factory, registry.Registry) {
	t.Helper()
	sm := skills.New()
	sm.RegisterDomain(webDomain())
	reg := registry.New()

	f, err := factory.New(factory.Config{
		Registry:           reg,
		Skills:             sm,
		Credentials:        credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:          lifecycle.New(),
		Memory:             memory.New(),
		Comms:              comms.NewStubClient(),
		GenerateID:         func() string { return id },
		IdleSuspendTimeout: 30 * time.Minute, // OQ-03 enabled
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}
	return f, reg
}

// TestCompleteTaskLeavesAgentIdleWhenSuspensionEnabled verifies that, when
// IdleSuspendTimeout > 0, CompleteTask transitions the agent to IDLE instead of
// immediately terminating it to TERMINATED.
func TestCompleteTaskLeavesAgentIdleWhenSuspensionEnabled(t *testing.T) {
	f, reg := newSuspendFactory(t, "agent-idle-suspend")

	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-idle-1",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-idle-1",
	}); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}

	if err := f.CompleteTask("agent-idle-suspend", "session-idle-1", "trace-idle-1", "output", nil); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	agent, err := reg.Get("agent-idle-suspend")
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	if agent.State != registry.StateIdle {
		t.Errorf("state: want %q, got %q", registry.StateIdle, agent.State)
	}
}

// TestCompleteTaskKeepsLiveProcessWhenReuseEnabled asserts the cold-start-tax
// elimination contract: when reuse is enabled, CompleteTask must not call
// lifecycle.Terminate — the agent-process has to stay alive so the next
// matching task can hit the warm Priority 1 Deliver path.
func TestCompleteTaskKeepsLiveProcessWhenReuseEnabled(t *testing.T) {
	sm := skills.New()
	sm.RegisterDomain(webDomain())
	reg := registry.New()

	lc := &recordingLifecycle{Manager: lifecycle.New()}
	f, err := factory.New(factory.Config{
		Registry:           reg,
		Skills:             sm,
		Credentials:        credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:          lc,
		Memory:             memory.New(),
		Comms:              comms.NewStubClient(),
		GenerateID:         func() string { return "agent-reuse-keep" },
		IdleSuspendTimeout: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-keep-1",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-keep-1",
	}); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}
	if err := f.CompleteTask("agent-reuse-keep", "session-keep-1", "trace-keep-1", "done", nil); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	if lc.terminateCount != 0 {
		t.Errorf("Terminate must not be called on CompleteTask when reuse is enabled; got %d calls", lc.terminateCount)
	}
}

// TestHandleTaskSpecDeliversOnReuse verifies the warm-path: after CompleteTask
// leaves the agent IDLE with a live process, a follow-up HandleTaskSpec must
// go through lifecycle.Deliver rather than a second Spawn — that is the
// cold-start-tax elimination in action.
func TestHandleTaskSpecDeliversOnReuse(t *testing.T) {
	sm := skills.New()
	sm.RegisterDomain(webDomain())
	reg := registry.New()

	lc := &recordingLifecycle{Manager: lifecycle.New()}
	f, err := factory.New(factory.Config{
		Registry:           reg,
		Skills:             sm,
		Credentials:        credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:          lc,
		Memory:             memory.New(),
		Comms:              comms.NewStubClient(),
		GenerateID:         func() string { return "agent-reuse-deliver" },
		IdleSuspendTimeout: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	// First task: full provision (cold).
	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-deliver-1",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-deliver-1",
	}); err != nil {
		t.Fatalf("HandleTaskSpec (first): %v", err)
	}
	if err := f.CompleteTask("agent-reuse-deliver", "session-deliver-1", "trace-deliver-1", "done", nil); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	spawnsBeforeReuse := lc.spawnCount
	deliversBeforeReuse := lc.deliverCount

	// Second task: warm reuse — must Deliver, must NOT Spawn.
	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-deliver-2",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-deliver-2",
	}); err != nil {
		t.Fatalf("HandleTaskSpec (reuse): %v", err)
	}

	if got := lc.spawnCount - spawnsBeforeReuse; got != 0 {
		t.Errorf("reuse path must not Spawn; got %d extra spawn calls", got)
	}
	if got := lc.deliverCount - deliversBeforeReuse; got != 1 {
		t.Errorf("reuse path must Deliver exactly once; got %d delivers", got)
	}

	agent, err := reg.Get("agent-reuse-deliver")
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	if agent.State != registry.StateActive {
		t.Errorf("state after reuse: want %q, got %q", registry.StateActive, agent.State)
	}
	if agent.AssignedTask != "task-deliver-2" {
		t.Errorf("AssignedTask: want %q, got %q", "task-deliver-2", agent.AssignedTask)
	}
}

// recordingLifecycle wraps a Manager and counts Spawn, Deliver, and Terminate
// calls so warm-path / cold-path behaviour can be asserted in factory tests.
// When deliverErr is non-nil, Deliver returns that error without calling the
// underlying Manager — used to simulate live-process reuse failure (stale
// stdin pipe, dead process, pending callback race) so the factory's fallback
// to fresh provision can be asserted.
type recordingLifecycle struct {
	lifecycle.Manager
	spawnCount     int
	deliverCount   int
	terminateCount int
	deliverErr     error
}

func (r *recordingLifecycle) Spawn(cfg lifecycle.VMConfig) error {
	r.spawnCount++
	return r.Manager.Spawn(cfg)
}

func (r *recordingLifecycle) Deliver(agentID string, cfg lifecycle.VMConfig) error {
	r.deliverCount++
	if r.deliverErr != nil {
		return r.deliverErr
	}
	return r.Manager.Deliver(agentID, cfg)
}

func (r *recordingLifecycle) Terminate(agentID string) error {
	r.terminateCount++
	return r.Manager.Terminate(agentID)
}

// TestHandleTaskSpecFallsBackToSpawnWhenDeliverFails verifies that a failing
// Deliver on the reuse path does not surface AGENT_REUSE_FAILED to the user:
// the factory must tear down the zombie entry, provision a fresh agent, and
// the task completes successfully. This is the "reuse is an optimization"
// contract — any failure degrades gracefully to a cold-start, never a hard
// user-visible error.
func TestHandleTaskSpecFallsBackToSpawnWhenDeliverFails(t *testing.T) {
	sm := skills.New()
	sm.RegisterDomain(webDomain())
	reg := registry.New()

	lc := &recordingLifecycle{Manager: lifecycle.New()}
	idSeq := 0
	f, err := factory.New(factory.Config{
		Registry:    reg,
		Skills:      sm,
		Credentials: credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:   lc,
		Memory:      memory.New(),
		Comms:       comms.NewStubClient(),
		GenerateID: func() string {
			idSeq++
			return fmt.Sprintf("agent-fallback-%d", idSeq)
		},
		IdleSuspendTimeout: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	// First task: full provision, then CompleteTask → IDLE, live process kept.
	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-fallback-1",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-fallback-1",
	}); err != nil {
		t.Fatalf("HandleTaskSpec (first): %v", err)
	}
	if err := f.CompleteTask("agent-fallback-1", "sess-1", "trace-fallback-1", "done", nil); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	spawnsBeforeReuse := lc.spawnCount
	terminatesBeforeReuse := lc.terminateCount

	// Second task: simulate a stale live process by making Deliver fail.
	// The factory must not surface AGENT_REUSE_FAILED; it must Terminate the
	// zombie, publish TERMINATED, and Spawn a fresh agent for this task.
	lc.deliverErr = fmt.Errorf("simulated stale stdin pipe")
	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-fallback-2",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-fallback-2",
	}); err != nil {
		t.Fatalf("HandleTaskSpec (fallback): %v", err)
	}

	if got := lc.deliverCount; got == 0 {
		t.Errorf("expected Deliver to be attempted on reuse path; got %d", got)
	}
	if got := lc.spawnCount - spawnsBeforeReuse; got != 1 {
		t.Errorf("fallback must Spawn exactly one fresh agent; got %d extra spawns", got)
	}
	if got := lc.terminateCount - terminatesBeforeReuse; got < 1 {
		t.Errorf("fallback must Terminate the zombie agent; got %d extra terminates", got)
	}

	// The zombie entry must be TERMINATED, not IDLE (otherwise a later task
	// could match it again and loop on the same failure).
	zombie, err := reg.Get("agent-fallback-1")
	if err != nil {
		t.Fatalf("registry.Get zombie: %v", err)
	}
	if zombie.State != registry.StateTerminated {
		t.Errorf("zombie state: want %q, got %q", registry.StateTerminated, zombie.State)
	}

	// A fresh agent must exist in ACTIVE state for this task. We find it by
	// scanning the registry rather than hard-coding the ID because the ID
	// generator is also used for VM IDs internally, so the exact next value
	// depends on factory implementation details.
	var fresh *types.AgentRecord
	for _, a := range reg.List() {
		if a.AssignedTask == "task-fallback-2" && a.State == registry.StateActive {
			fresh = a
			break
		}
	}
	if fresh == nil {
		t.Fatalf("expected an ACTIVE agent assigned to task-fallback-2; registry: %+v", reg.List())
	}
	if fresh.AgentID == "agent-fallback-1" {
		t.Errorf("fresh agent must have a new ID; got zombie ID %q", fresh.AgentID)
	}
}

// TestSuspendAgentTransitionsSuspended verifies that SuspendAgent moves an IDLE
// agent to SUSPENDED and that the registry reflects the new state.
func TestSuspendAgentTransitionsSuspended(t *testing.T) {
	f, reg := newSuspendFactory(t, "agent-suspend-1")

	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-susp-1",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-susp-1",
	}); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}

	// Move agent to IDLE via CompleteTask.
	if err := f.CompleteTask("agent-suspend-1", "session-susp-1", "trace-susp-1", "result", nil); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	// SuspendAgent requires IDLE state.
	if err := f.SuspendAgent("agent-suspend-1", "trace-susp-1"); err != nil {
		t.Fatalf("SuspendAgent: %v", err)
	}

	agent, err := reg.Get("agent-suspend-1")
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	if agent.State != registry.StateSuspended {
		t.Errorf("state: want %q, got %q", registry.StateSuspended, agent.State)
	}
}

// TestSuspendAgentRejectsNonIdleAgent verifies that SuspendAgent returns an
// error when called on an agent that is not in the IDLE state.
func TestSuspendAgentRejectsNonIdleAgent(t *testing.T) {
	f, _ := newSuspendFactory(t, "agent-suspend-bad")

	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-susp-bad",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-susp-bad",
	}); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}

	// Agent is ACTIVE — SuspendAgent should fail.
	if err := f.SuspendAgent("agent-suspend-bad", "trace-susp-bad"); err == nil {
		t.Error("expected error suspending an ACTIVE agent, got nil")
	}
}

// TestHandleTaskSpecWakesSuspendedAgent verifies the Priority 2 path: when a
// SUSPENDED agent with matching skills exists, HandleTaskSpec wakes it (issues
// fresh credentials + spawns new VM) instead of provisioning a new agent.
func TestHandleTaskSpecWakesSuspendedAgent(t *testing.T) {
	sm := skills.New()
	sm.RegisterDomain(webDomain())
	reg := registry.New()
	c := comms.NewStubClient()

	var acceptedIDs []string
	_ = c.Subscribe(comms.SubjectTaskAccepted, func(msg *comms.Message) {
		var ta types.TaskAccepted
		if err := json.Unmarshal(msg.Data, &ta); err != nil {
			return
		}
		acceptedIDs = append(acceptedIDs, ta.AgentID)
	})

	idSeq := []string{"agent-wake-1", "vmid-wake-2"}
	idx := 0
	genID := func() string {
		id := idSeq[idx%len(idSeq)]
		idx++
		return id
	}

	f, err := factory.New(factory.Config{
		Registry:           reg,
		Skills:             sm,
		Credentials:        credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:          lifecycle.New(),
		Memory:             memory.New(),
		Comms:              c,
		GenerateID:         genID,
		IdleSuspendTimeout: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	// Provision → complete → suspend.
	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-wake-1",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-wake-1",
	}); err != nil {
		t.Fatalf("HandleTaskSpec (provision): %v", err)
	}
	if err := f.CompleteTask("agent-wake-1", "session-wake-1", "trace-wake-1", "done", nil); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := f.SuspendAgent("agent-wake-1", "trace-wake-1"); err != nil {
		t.Fatalf("SuspendAgent: %v", err)
	}

	// A second task should reuse the SUSPENDED agent.
	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-wake-2",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-wake-2",
	}); err != nil {
		t.Fatalf("HandleTaskSpec (wake): %v", err)
	}

	agent, err := reg.Get("agent-wake-1")
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	if agent.State != registry.StateActive {
		t.Errorf("state after wake: want %q, got %q", registry.StateActive, agent.State)
	}
	if agent.AssignedTask != "task-wake-2" {
		t.Errorf("AssignedTask: want %q, got %q", "task-wake-2", agent.AssignedTask)
	}
}

// TestOnSuspendAndOnWakeHooks verifies that the OnSuspend and OnWake hooks are
// called when an agent is suspended and subsequently woken.
func TestOnSuspendAndOnWakeHooks(t *testing.T) {
	sm := skills.New()
	sm.RegisterDomain(webDomain())
	reg := registry.New()

	idSeq := []string{"agent-hooks-1", "vmid-hooks-2"}
	idx := 0
	genID := func() string {
		id := idSeq[idx%len(idSeq)]
		idx++
		return id
	}

	var suspendCalled, wakeCalled bool
	f, err := factory.New(factory.Config{
		Registry:           reg,
		Skills:             sm,
		Credentials:        credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:          lifecycle.New(),
		Memory:             memory.New(),
		Comms:              comms.NewStubClient(),
		GenerateID:         genID,
		IdleSuspendTimeout: 30 * time.Minute,
		OnSuspend:          func(_ string) { suspendCalled = true },
		OnWake:             func(_ string) { wakeCalled = true },
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-hooks-1",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-hooks-1",
	}); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}
	if err := f.CompleteTask("agent-hooks-1", "session-hooks-1", "trace-hooks-1", "ok", nil); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := f.SuspendAgent("agent-hooks-1", "trace-hooks-1"); err != nil {
		t.Fatalf("SuspendAgent: %v", err)
	}
	if !suspendCalled {
		t.Error("OnSuspend hook was not called")
	}

	if err := f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-hooks-2",
		RequiredSkills: []string{"web"},
		TraceID:        "trace-hooks-2",
	}); err != nil {
		t.Fatalf("HandleTaskSpec (wake): %v", err)
	}
	if !wakeCalled {
		t.Error("OnWake hook was not called")
	}
}
