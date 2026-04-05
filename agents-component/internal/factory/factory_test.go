package factory_test

import (
	"encoding/json"
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
