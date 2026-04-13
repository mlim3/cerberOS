package skills_test

import (
	"testing"

	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

// validCommand returns a SkillNode that fully satisfies the Tool Contract.
func validCommand(name string) *types.SkillNode {
	return &types.SkillNode{
		Name:        name,
		Level:       "command",
		Label:       "Test Command",
		Description: "Does something useful. Do NOT use when input is empty.",
		Spec: &types.SkillSpec{
			Parameters: map[string]types.ParameterDef{
				"input": {Type: "string", Required: true, Description: "The input value."},
			},
		},
	}
}

func registeredWebDomain(t *testing.T) skills.Manager {
	t.Helper()
	m := skills.New()
	if err := m.RegisterDomain(&types.SkillNode{
		Name:     "web",
		Level:    "domain",
		Children: map[string]*types.SkillNode{},
	}); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}
	return m
}

// ─── RegisterCommand ──────────────────────────────────────────────────────────

func TestRegisterCommand_ValidNode_Succeeds(t *testing.T) {
	m := registeredWebDomain(t)
	if err := m.RegisterCommand("web", validCommand("web_paginate")); err != nil {
		t.Errorf("valid command: unexpected error: %v", err)
	}
}

func TestRegisterCommand_NilNode_ReturnsError(t *testing.T) {
	m := registeredWebDomain(t)
	if err := m.RegisterCommand("web", nil); err == nil {
		t.Error("nil node must return an error")
	}
}

func TestRegisterCommand_WrongLevel_ReturnsError(t *testing.T) {
	m := registeredWebDomain(t)
	node := validCommand("web_paginate")
	node.Level = "domain" // wrong level
	if err := m.RegisterCommand("web", node); err == nil {
		t.Error("wrong level must return an error")
	}
}

func TestRegisterCommand_ToolContractViolation_MissingLabel(t *testing.T) {
	m := registeredWebDomain(t)
	node := validCommand("web_paginate")
	node.Label = ""
	if err := m.RegisterCommand("web", node); err == nil {
		t.Error("missing label must return a Tool Contract error")
	}
}

func TestRegisterCommand_ToolContractViolation_MissingDescription(t *testing.T) {
	m := registeredWebDomain(t)
	node := validCommand("web_paginate")
	node.Description = ""
	if err := m.RegisterCommand("web", node); err == nil {
		t.Error("missing description must return a Tool Contract error")
	}
}

func TestRegisterCommand_ToolContractViolation_MissingParamDescription(t *testing.T) {
	m := registeredWebDomain(t)
	node := validCommand("web_paginate")
	node.Spec.Parameters["input"] = types.ParameterDef{Type: "string", Required: true} // no Description
	if err := m.RegisterCommand("web", node); err == nil {
		t.Error("parameter without description must return a Tool Contract error")
	}
}

func TestRegisterCommand_UnknownDomain_ReturnsError(t *testing.T) {
	m := skills.New() // no domains registered
	if err := m.RegisterCommand("web", validCommand("web_paginate")); err == nil {
		t.Error("unknown domain must return an error")
	}
}

func TestRegisterCommand_CommandVisibleViaGetCommands(t *testing.T) {
	m := registeredWebDomain(t)
	if err := m.RegisterCommand("web", validCommand("web_paginate")); err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}

	cmds, err := m.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	found := false
	for _, c := range cmds {
		if c.Name == "web_paginate" {
			found = true
		}
	}
	if !found {
		t.Error("registered command must be visible via GetCommands")
	}
}

func TestRegisterCommand_Upsert_ReplacesExistingCommand(t *testing.T) {
	m := registeredWebDomain(t)

	v1 := validCommand("web_paginate")
	v1.Label = "Version 1"
	if err := m.RegisterCommand("web", v1); err != nil {
		t.Fatalf("first RegisterCommand: %v", err)
	}

	v2 := validCommand("web_paginate")
	v2.Label = "Version 2"
	if err := m.RegisterCommand("web", v2); err != nil {
		t.Fatalf("second RegisterCommand (upsert): %v", err)
	}

	cmds, _ := m.GetCommands("web")
	count := 0
	for _, c := range cmds {
		if c.Name == "web_paginate" {
			count++
			if c.Label != "Version 2" {
				t.Errorf("upsert: want Label 'Version 2', got %q", c.Label)
			}
		}
	}
	if count != 1 {
		t.Errorf("upsert: want exactly 1 'web_paginate' command, got %d", count)
	}
}

func TestRegisterCommand_MultipleCommandsInDomain(t *testing.T) {
	m := registeredWebDomain(t)
	for _, name := range []string{"web_paginate", "web_retry", "web_extract"} {
		if err := m.RegisterCommand("web", validCommand(name)); err != nil {
			t.Fatalf("RegisterCommand(%s): %v", name, err)
		}
	}

	cmds, err := m.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	if len(cmds) != 3 {
		t.Errorf("want 3 commands, got %d", len(cmds))
	}
}

func TestRegisterCommand_SpecNotExposedViaGetCommands(t *testing.T) {
	m := registeredWebDomain(t)
	if err := m.RegisterCommand("web", validCommand("web_paginate")); err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}

	cmds, _ := m.GetCommands("web")
	for _, c := range cmds {
		if c.Name == "web_paginate" && c.Spec != nil {
			t.Error("GetCommands must not expose Spec (progressive disclosure)")
		}
	}
}

func TestRegisterCommand_SpecAccessibleViaGetSpec(t *testing.T) {
	m := registeredWebDomain(t)
	node := validCommand("web_paginate")
	if err := m.RegisterCommand("web", node); err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}

	spec, err := m.GetSpec("web", "web_paginate")
	if err != nil {
		t.Fatalf("GetSpec: %v", err)
	}
	if _, ok := spec.Parameters["input"]; !ok {
		t.Error("GetSpec must return the full parameter spec")
	}
}

func TestRegisterCommand_ParallelRegistrations_NoPanic(t *testing.T) {
	m := registeredWebDomain(t)
	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			name := "web_cmd_" + string(rune('a'+i))
			_ = m.RegisterCommand("web", validCommand(name))
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

// ─── ListDomains ──────────────────────────────────────────────────────────────

func TestListDomains_EmptyManager_ReturnsEmpty(t *testing.T) {
	m := skills.New()
	domains := m.ListDomains()
	if len(domains) != 0 {
		t.Errorf("empty manager: want 0 domains, got %d: %v", len(domains), domains)
	}
}

func TestListDomains_AfterRegistration_ContainsDomain(t *testing.T) {
	m := skills.New()
	_ = m.RegisterDomain(&types.SkillNode{Name: "web", Level: "domain", Children: map[string]*types.SkillNode{}})

	domains := m.ListDomains()
	if len(domains) != 1 {
		t.Fatalf("want 1 domain, got %d", len(domains))
	}
	if domains[0] != "web" {
		t.Errorf("want domain 'web', got %q", domains[0])
	}
}

func TestListDomains_MultipleDomains_AllListed(t *testing.T) {
	m := skills.New()
	for _, name := range []string{"web", "data", "comms", "storage"} {
		_ = m.RegisterDomain(&types.SkillNode{Name: name, Level: "domain", Children: map[string]*types.SkillNode{}})
	}

	domains := m.ListDomains()
	if len(domains) != 4 {
		t.Fatalf("want 4 domains, got %d: %v", len(domains), domains)
	}
	seen := make(map[string]bool)
	for _, d := range domains {
		seen[d] = true
	}
	for _, want := range []string{"web", "data", "comms", "storage"} {
		if !seen[want] {
			t.Errorf("domain %q missing from ListDomains", want)
		}
	}
}

func TestListDomains_RegisterCommandDoesNotCreateDomain(t *testing.T) {
	// Domains can only be added via RegisterDomain; RegisterCommand on a
	// non-existent domain returns an error and must not create a domain.
	m := skills.New()
	_ = m.RegisterCommand("web", validCommand("web_paginate")) // errors silently in test

	if domains := m.ListDomains(); len(domains) != 0 {
		t.Errorf("failed RegisterCommand must not create domain; got %v", domains)
	}
}
