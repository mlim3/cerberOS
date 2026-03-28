package factory_test

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
