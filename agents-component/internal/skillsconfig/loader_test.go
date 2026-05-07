package skillsconfig_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cerberOS/agents-component/internal/skillsconfig"
)

// TestLoad_Default verifies that loading with an empty path returns the
// embedded default, which must include all five built-in domains.
func TestLoad_Default(t *testing.T) {
	cfg, err := skillsconfig.Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load(\"\") returned nil config")
	}

	want := map[string]bool{
		"web": true, "data": true, "comms": true, "storage": true, "general": true,
	}
	for _, d := range cfg.Domains {
		delete(want, d.Name)
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for k := range want {
			missing = append(missing, k)
		}
		t.Errorf("default config missing domains: %v", missing)
	}
}

// TestLoad_DefaultHasCommands ensures key built-in commands are present.
func TestLoad_DefaultHasCommands(t *testing.T) {
	cfg, err := skillsconfig.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	type wantCmd struct {
		domain string
		name   string
	}
	cases := []wantCmd{
		{"web", "web_fetch"},
		{"web", "vault_web_fetch"},
		{"data", "data_transform"},
		{"data", "vault_data_read"},
		{"data", "vault_data_write"},
		{"comms", "comms_format"},
		{"comms", "vault_comms_send"},
		{"storage", "vault_storage_read"},
		{"storage", "vault_storage_write"},
		{"storage", "vault_storage_list"},
	}
	index := make(map[string]map[string]bool)
	for _, d := range cfg.Domains {
		index[d.Name] = make(map[string]bool)
		for _, cmd := range d.Commands {
			index[d.Name][cmd.Name] = true
		}
	}
	for _, tc := range cases {
		if !index[tc.domain][tc.name] {
			t.Errorf("default config: command %q missing from domain %q", tc.name, tc.domain)
		}
	}
}

// TestLoad_DefaultCommandsHaveRequiredFields verifies that every command in the
// default config satisfies the Tool Contract fields required by M4 validation.
func TestLoad_DefaultCommandsHaveRequiredFields(t *testing.T) {
	cfg, err := skillsconfig.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, d := range cfg.Domains {
		for _, cmd := range d.Commands {
			if cmd.Name == "" {
				t.Errorf("domain %q: command has empty name", d.Name)
			}
			if cmd.Label == "" {
				t.Errorf("domain %q command %q: missing label", d.Name, cmd.Name)
			}
			if cmd.Description == "" {
				t.Errorf("domain %q command %q: missing description", d.Name, cmd.Name)
			}
			if len(cmd.Description) > 300 {
				t.Errorf("domain %q command %q: description exceeds 300 chars (%d)",
					d.Name, cmd.Name, len(cmd.Description))
			}
			if cmd.Implementation == "" {
				t.Errorf("domain %q command %q: missing implementation", d.Name, cmd.Name)
			}
			for _, p := range cmd.Parameters {
				if p.Description == "" {
					t.Errorf("domain %q command %q param %q: missing description",
						d.Name, cmd.Name, p.Name)
				}
			}
		}
	}
}

// TestLoad_YAMLFile verifies loading from an explicit YAML file path.
func TestLoad_YAMLFile(t *testing.T) {
	yaml := `domains:
  - name: test_domain
    commands:
      - name: test_cmd
        label: Test Command
        description: A test command for unit testing.
        implementation: test_impl
        required_credential_types: []
        timeout_seconds: 10
        parameters:
          - name: input
            type: string
            required: true
            description: Input value.
`
	path := filepath.Join(t.TempDir(), "skills.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write temp YAML: %v", err)
	}

	cfg, err := skillsconfig.Load(path)
	if err != nil {
		t.Fatalf("Load(%q): %v", path, err)
	}
	if len(cfg.Domains) != 1 || cfg.Domains[0].Name != "test_domain" {
		t.Errorf("unexpected domains: %+v", cfg.Domains)
	}
	if len(cfg.Domains[0].Commands) != 1 || cfg.Domains[0].Commands[0].Name != "test_cmd" {
		t.Errorf("unexpected commands: %+v", cfg.Domains[0].Commands)
	}
}

// TestLoad_JSONFile verifies loading from an explicit JSON file path.
func TestLoad_JSONFile(t *testing.T) {
	data := skillsconfig.Config{
		Domains: []skillsconfig.DomainDef{
			{
				Name: "json_domain",
				Commands: []skillsconfig.CommandDef{
					{
						Name:           "json_cmd",
						Label:          "JSON Command",
						Description:    "A test command loaded from JSON.",
						Implementation: "json_impl",
						TimeoutSeconds: 20,
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(data)

	path := filepath.Join(t.TempDir(), "skills.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write temp JSON: %v", err)
	}

	cfg, err := skillsconfig.Load(path)
	if err != nil {
		t.Fatalf("Load(%q): %v", path, err)
	}
	if len(cfg.Domains) != 1 || cfg.Domains[0].Name != "json_domain" {
		t.Errorf("unexpected domains: %+v", cfg.Domains)
	}
}

// TestLoad_NonexistentFile verifies that a missing path returns an error.
func TestLoad_NonexistentFile(t *testing.T) {
	_, err := skillsconfig.Load("/nonexistent/path/skills.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file, got nil")
	}
}

// TestLoad_InvalidYAML verifies that malformed YAML returns an error.
func TestLoad_InvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("domains: [unclosed"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := skillsconfig.Load(path)
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

// TestLoad_InvalidJSON verifies that malformed JSON returns an error.
func TestLoad_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"domains": [`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := skillsconfig.Load(path)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// TestToSkillNodes_DomainLevel verifies that ToSkillNodes produces domain-level nodes.
func TestToSkillNodes_DomainLevel(t *testing.T) {
	cfg, err := skillsconfig.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	nodes := cfg.ToSkillNodes()
	if len(nodes) != len(cfg.Domains) {
		t.Errorf("ToSkillNodes: want %d nodes, got %d", len(cfg.Domains), len(nodes))
	}
	for _, n := range nodes {
		if n.Level != "domain" {
			t.Errorf("node %q: want level=domain, got %q", n.Name, n.Level)
		}
	}
}

// TestToSkillNodes_CommandLevel verifies that command children have required Tool Contract fields.
func TestToSkillNodes_CommandLevel(t *testing.T) {
	cfg, err := skillsconfig.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	nodes := cfg.ToSkillNodes()

	// Find the "web" domain node and check its children.
	var webNode interface{ GetChildren() map[string]interface{} }
	_ = webNode
	for _, n := range nodes {
		if n.Name != "web" {
			continue
		}
		if n.Children == nil {
			t.Fatal("web domain: children map is nil")
		}
		fetchCmd, ok := n.Children["web_fetch"]
		if !ok {
			t.Fatal("web domain: web_fetch command missing")
		}
		if fetchCmd.Level != "command" {
			t.Errorf("web_fetch: want level=command, got %q", fetchCmd.Level)
		}
		if fetchCmd.Label == "" {
			t.Error("web_fetch: label is empty")
		}
		if fetchCmd.Description == "" {
			t.Error("web_fetch: description is empty")
		}
		if fetchCmd.Spec == nil {
			t.Error("web_fetch: spec is nil")
		} else if fetchCmd.Spec.Parameters["url"].Description == "" {
			t.Error("web_fetch: url parameter has no description")
		}
		return
	}
	t.Error("web domain not found in ToSkillNodes output")
}

// TestToSkillNodes_VaultCommandHasCredTypes verifies that vault commands carry
// their required_credential_types through to the SkillNode.
func TestToSkillNodes_VaultCommandHasCredTypes(t *testing.T) {
	cfg, err := skillsconfig.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	nodes := cfg.ToSkillNodes()
	for _, n := range nodes {
		if n.Name != "web" {
			continue
		}
		vwf, ok := n.Children["vault_web_fetch"]
		if !ok {
			t.Fatal("vault_web_fetch missing from web domain")
		}
		if len(vwf.RequiredCredentialTypes) == 0 {
			t.Error("vault_web_fetch: RequiredCredentialTypes should be non-empty")
		}
		return
	}
	t.Error("web domain not found")
}

// TestToSkillNodes_GeneralDomainEmpty verifies that the general domain node
// has no command children (it is a reasoning-only domain with no tools).
func TestToSkillNodes_GeneralDomainEmpty(t *testing.T) {
	cfg, err := skillsconfig.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	nodes := cfg.ToSkillNodes()
	for _, n := range nodes {
		if n.Name == "general" {
			if len(n.Children) != 0 {
				t.Errorf("general domain: expected 0 children, got %d", len(n.Children))
			}
			return
		}
	}
	t.Error("general domain not found in ToSkillNodes output")
}
