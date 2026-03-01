package factory_test

import (
	"testing"

	"github.com/aegis/aegis-agents/internal/comms"
	"github.com/aegis/aegis-agents/internal/credentials"
	"github.com/aegis/aegis-agents/internal/factory"
	"github.com/aegis/aegis-agents/internal/lifecycle"
	"github.com/aegis/aegis-agents/internal/memory"
	"github.com/aegis/aegis-agents/internal/registry"
	"github.com/aegis/aegis-agents/internal/skills"
	"github.com/aegis/aegis-agents/pkg/types"
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
