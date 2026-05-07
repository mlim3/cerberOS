package domains_test

import (
	"testing"

	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/internal/skills/domains"
)

func TestLogsDomain_PassesToolContract(t *testing.T) {
	domain := domains.LogsDomain()

	if domain.Name != "logs" {
		t.Fatalf("expected domain name 'logs', got %q", domain.Name)
	}
	if domain.Level != "domain" {
		t.Fatalf("expected level 'domain', got %q", domain.Level)
	}

	expectedCommands := []string{"logs.query", "logs.search", "logs.tail", "logs.agent"}
	for _, name := range expectedCommands {
		cmd, ok := domain.Children[name]
		if !ok {
			t.Errorf("missing expected command %q", name)
			continue
		}
		if err := skills.ValidateCommandContract(cmd); err != nil {
			t.Errorf("command %q failed Tool Contract: %v", name, err)
		}
	}
}

func TestLogsDomain_RegistersWithManager(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(domains.LogsDomain()); err != nil {
		t.Fatalf("RegisterDomain failed: %v", err)
	}

	cmds, err := mgr.GetCommands("logs")
	if err != nil {
		t.Fatalf("GetCommands('logs') failed: %v", err)
	}
	if len(cmds) != 4 {
		t.Fatalf("expected 4 commands, got %d", len(cmds))
	}

	// Parameters must be withheld at command level (progressive disclosure)
	for _, cmd := range cmds {
		if cmd.Spec != nil {
			t.Errorf("command %q: Spec should be withheld by GetCommands", cmd.Name)
		}
	}
}

func TestLogsDomain_SearchFindsRelevantCommands(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(domains.LogsDomain()); err != nil {
		t.Fatalf("RegisterDomain failed: %v", err)
	}

	results, err := mgr.Search("search log messages by keyword", 3)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one search result")
	}
	// Top result should be logs.search for a keyword-search query
	if results[0].Name != "logs.search" {
		t.Errorf("expected top result 'logs.search', got %q (score %.3f)", results[0].Name, results[0].Score)
	}
}

func TestLogsDomain_GetSpecReturnsParameters(t *testing.T) {
	mgr := skills.New()
	if err := mgr.RegisterDomain(domains.LogsDomain()); err != nil {
		t.Fatalf("RegisterDomain failed: %v", err)
	}

	spec, err := mgr.GetSpec("logs", "logs.query")
	if err != nil {
		t.Fatalf("GetSpec failed: %v", err)
	}
	if _, ok := spec.Parameters["severity"]; !ok {
		t.Error("expected 'severity' parameter in logs.query spec")
	}
	if _, ok := spec.Parameters["limit"]; !ok {
		t.Error("expected 'limit' parameter in logs.query spec")
	}
}
