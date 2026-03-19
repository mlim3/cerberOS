package skills_test

import (
	"testing"

	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

func webDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": {
				Name:           "web.fetch",
				Level:          "command",
				Label:          "Web Fetch",
				Description:    "Fetch the content of a URL via HTTP. Use for web pages and APIs without authentication. Do NOT use for authenticated operations.",
				TimeoutSeconds: 30,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url":    {Type: "string", Required: true, Description: "The fully-qualified URL to fetch."},
						"method": {Type: "string", Required: false, Description: "HTTP method: GET or POST. Defaults to GET."},
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

// --- Tool Contract enforcement tests (EDD §13.2) ---

func commandWithout(field string) *types.SkillNode {
	cmd := &types.SkillNode{
		Name:           "web.fetch",
		Level:          "command",
		Label:          "Web Fetch",
		Description:    "Fetch a URL. Use for unauthenticated HTTP. Do NOT use for authenticated operations.",
		TimeoutSeconds: 30,
		Spec: &types.SkillSpec{
			Parameters: map[string]types.ParameterDef{
				"url": {Type: "string", Required: true, Description: "URL to fetch."},
			},
		},
	}
	switch field {
	case "label":
		cmd.Label = ""
	case "description":
		cmd.Description = ""
	case "param_description":
		cmd.Spec.Parameters["url"] = types.ParameterDef{Type: "string", Required: true} // no Description
	case "timeout_invalid":
		cmd.TimeoutSeconds = 999
	}
	return cmd
}

func domainWith(cmd *types.SkillNode) *types.SkillNode {
	return &types.SkillNode{
		Name:     "web",
		Level:    "domain",
		Children: map[string]*types.SkillNode{cmd.Name: cmd},
	}
}

func TestContractMissingLabel(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(domainWith(commandWithout("label"))); err == nil {
		t.Error("expected error for missing label, got nil")
	}
}

func TestContractMissingDescription(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(domainWith(commandWithout("description"))); err == nil {
		t.Error("expected error for missing description, got nil")
	}
}

func TestContractMissingParameterDescription(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(domainWith(commandWithout("param_description"))); err == nil {
		t.Error("expected error for parameter with no description, got nil")
	}
}

func TestContractTimeoutOutOfRange(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(domainWith(commandWithout("timeout_invalid"))); err == nil {
		t.Error("expected error for timeout > 300, got nil")
	}
}

func TestGetCommandsExposesContractFields(t *testing.T) {
	m := skills.New()
	if err := m.RegisterDomain(webDomain()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}
	cmds, err := m.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	cmd := cmds[0]
	if cmd.Label == "" {
		t.Error("GetCommands must expose Label")
	}
	if cmd.Description == "" {
		t.Error("GetCommands must expose Description")
	}
	if cmd.Spec != nil {
		t.Error("GetCommands must not expose Spec")
	}
}
