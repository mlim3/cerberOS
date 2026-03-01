package skills_test

import (
	"testing"

	"github.com/aegis/aegis-agents/internal/skills"
	"github.com/aegis/aegis-agents/pkg/types"
)

func webDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": {
				Name:  "web.fetch",
				Level: "command",
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url": {Type: "string", Required: true},
					},
				},
			},
		},
	}
}

func TestRegisterAndGetDomain(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	d, err := m.GetDomain("web")
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if d.Name != "web" {
		t.Errorf("got name %q, want %q", d.Name, "web")
	}
	if len(d.Children) != 0 {
		t.Error("GetDomain must not expose children")
	}
}

func TestGetCommands(t *testing.T) {
	m := skills.New()
	m.RegisterDomain(webDomain())

	cmds, err := m.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Name != "web.fetch" {
		t.Errorf("unexpected commands: %+v", cmds)
	}
	if cmds[0].Spec != nil {
		t.Error("GetCommands must not expose specs")
	}
}

func TestGetSpec(t *testing.T) {
	m := skills.New()
	m.RegisterDomain(webDomain())

	spec, err := m.GetSpec("web", "web.fetch")
	if err != nil {
		t.Fatalf("GetSpec: %v", err)
	}
	if _, ok := spec.Parameters["url"]; !ok {
		t.Error("expected 'url' parameter in spec")
	}
}

func TestRegisterDomainWrongLevel(t *testing.T) {
	m := skills.New()
	node := &types.SkillNode{Name: "web", Level: "command"}
	if err := m.RegisterDomain(node); err == nil {
		t.Error("expected error for wrong level, got nil")
	}
}
