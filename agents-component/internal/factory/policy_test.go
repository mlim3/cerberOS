package factory_test

import (
	"os"
	"path/filepath"
	"strings"
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

// ---------------------------------------------------------------------------
// NewPermissionPolicy
// ---------------------------------------------------------------------------

func TestNewPermissionPolicy_EmptyEntries(t *testing.T) {
	_, err := factory.NewPermissionPolicy(map[string][]string{})
	if err == nil {
		t.Error("expected error for empty entries map, got nil")
	}
}

func TestNewPermissionPolicy_DomainWithNoOps(t *testing.T) {
	_, err := factory.NewPermissionPolicy(map[string][]string{
		"web": {},
	})
	if err == nil {
		t.Error("expected error for domain with empty operation list, got nil")
	}
}

func TestNewPermissionPolicy_Valid(t *testing.T) {
	p, err := factory.NewPermissionPolicy(map[string][]string{
		"web":  {"web_fetch", "web_post"},
		"data": {"data_read"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil policy")
	}
}

// ---------------------------------------------------------------------------
// PermissionsFor — known domain
// ---------------------------------------------------------------------------

func TestPermissionsFor_KnownDomain_ReturnsOps(t *testing.T) {
	p, _ := factory.NewPermissionPolicy(map[string][]string{
		"web": {"web_post", "web_fetch"},
	})
	ops, err := p.PermissionsFor([]string{"web"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("want 2 ops, got %d: %v", len(ops), ops)
	}
	// Result must be sorted.
	if ops[0] != "web_fetch" || ops[1] != "web_post" {
		t.Errorf("want [web_fetch web_post] (sorted), got %v", ops)
	}
}

// ---------------------------------------------------------------------------
// PermissionsFor — unknown domain
// ---------------------------------------------------------------------------

func TestPermissionsFor_UnknownDomain_ReturnsError(t *testing.T) {
	p, _ := factory.NewPermissionPolicy(map[string][]string{
		"web": {"web_fetch"},
	})
	_, err := p.PermissionsFor([]string{"storage"})
	if err == nil {
		t.Error("expected error for unknown domain, got nil")
	}
	if !strings.Contains(err.Error(), "storage") {
		t.Errorf("error must mention the unknown domain name; got %q", err.Error())
	}
}

func TestPermissionsFor_PartialUnknown_ReturnsError(t *testing.T) {
	// Even if some domains are known, a single unknown domain must fail.
	p, _ := factory.NewPermissionPolicy(map[string][]string{
		"web": {"web_fetch"},
	})
	_, err := p.PermissionsFor([]string{"web", "unknown_domain"})
	if err == nil {
		t.Error("expected error when one domain is unknown, got nil")
	}
}

// ---------------------------------------------------------------------------
// PermissionsFor — multi-domain union
// ---------------------------------------------------------------------------

func TestPermissionsFor_MultiDomain_Union(t *testing.T) {
	p, _ := factory.NewPermissionPolicy(map[string][]string{
		"web":  {"web_fetch", "web_post"},
		"data": {"data_read", "web_fetch"}, // overlap with web
	})
	ops, err := p.PermissionsFor([]string{"web", "data"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Union: web_fetch (deduplicated), web_post, data_read — sorted.
	want := []string{"data_read", "web_fetch", "web_post"}
	if len(ops) != len(want) {
		t.Fatalf("want %v, got %v", want, ops)
	}
	for i, w := range want {
		if ops[i] != w {
			t.Errorf("ops[%d]: want %q, got %q", i, w, ops[i])
		}
	}
}

func TestPermissionsFor_MultiDomain_Deduplicated(t *testing.T) {
	// Both domains grant the same operation — result must deduplicate.
	p, _ := factory.NewPermissionPolicy(map[string][]string{
		"web":   {"web_fetch"},
		"comms": {"web_fetch"},
	})
	ops, err := p.PermissionsFor([]string{"web", "comms"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops) != 1 || ops[0] != "web_fetch" {
		t.Errorf("expected exactly [web_fetch], got %v", ops)
	}
}

// ---------------------------------------------------------------------------
// LoadPermissionPolicy
// ---------------------------------------------------------------------------

func TestLoadPermissionPolicy_ValidYAML(t *testing.T) {
	yaml := "web:\n  - web_fetch\n  - web_post\ndata:\n  - data_read\n"
	path := writeTempFile(t, yaml)

	p, err := factory.LoadPermissionPolicy(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ops, err := p.PermissionsFor([]string{"web"})
	if err != nil {
		t.Fatalf("PermissionsFor: %v", err)
	}
	if len(ops) != 2 {
		t.Errorf("want 2 ops for web, got %d: %v", len(ops), ops)
	}
}

func TestLoadPermissionPolicy_EmptyPath(t *testing.T) {
	_, err := factory.LoadPermissionPolicy("")
	if err == nil {
		t.Error("expected error for empty path, got nil")
	}
}

func TestLoadPermissionPolicy_MissingFile(t *testing.T) {
	_, err := factory.LoadPermissionPolicy("/nonexistent/policy.yaml")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestLoadPermissionPolicy_MalformedYAML(t *testing.T) {
	path := writeTempFile(t, "not: valid: yaml: !!!")
	_, err := factory.LoadPermissionPolicy(path)
	// Either YAML parse error or empty-entries error; must not succeed.
	if err == nil {
		t.Error("expected error for malformed YAML, got nil")
	}
}

// ---------------------------------------------------------------------------
// Factory integration — provision rejects unknown domain when policy is set
// ---------------------------------------------------------------------------

func TestProvision_PolicyViolation_UnknownDomain(t *testing.T) {
	// Policy covers only "web" — requesting "storage" must fail at provision.
	p, _ := factory.NewPermissionPolicy(map[string][]string{
		"web": {"web_fetch"},
	})

	sm := skills.New()
	// Register "storage" so skill resolution doesn't fail before the policy check.
	sm.RegisterDomain(&types.SkillNode{
		Name:     "storage",
		Level:    "domain",
		Children: map[string]*types.SkillNode{},
	})

	f, err := factory.New(factory.Config{
		Registry:    registry.New(),
		Skills:      sm,
		Credentials: credentials.New(map[string]string{}),
		Lifecycle:   lifecycle.New(),
		Memory:      memory.New(),
		Comms:       comms.NewStubClient(),
		Policy:      p,
		GenerateID:  func() string { return "agent-policy-test" },
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	err = f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-policy-violation",
		RequiredSkills: []string{"storage"},
		Instructions:   "store something",
		TraceID:        "trace-policy-violation",
	})
	if err == nil {
		t.Error("expected provision to fail for domain not in policy, got nil")
	}
}

func TestProvision_PolicyMatch_Succeeds(t *testing.T) {
	// Policy covers "web" — provisioning a web agent must succeed.
	p, _ := factory.NewPermissionPolicy(map[string][]string{
		"web": {"web_fetch"},
	})

	sm := skills.New()
	sm.RegisterDomain(&types.SkillNode{
		Name:     "web",
		Level:    "domain",
		Children: map[string]*types.SkillNode{},
	})

	f, err := factory.New(factory.Config{
		Registry:    registry.New(),
		Skills:      sm,
		Credentials: credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:   lifecycle.New(),
		Memory:      memory.New(),
		Comms:       comms.NewStubClient(),
		Policy:      p,
		GenerateID:  func() string { return "agent-policy-ok" },
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	err = f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-policy-ok",
		RequiredSkills: []string{"web"},
		Instructions:   "fetch something",
		TraceID:        "trace-policy-ok",
	})
	if err != nil {
		t.Errorf("expected provision to succeed for known policy domain, got: %v", err)
	}
}

func TestProvision_NilPolicy_UsesLegacyStub(t *testing.T) {
	// No policy set — factory must still provision without error (legacy path).
	sm := skills.New()
	sm.RegisterDomain(&types.SkillNode{
		Name:     "web",
		Level:    "domain",
		Children: map[string]*types.SkillNode{},
	})

	f, err := factory.New(factory.Config{
		Registry:    registry.New(),
		Skills:      sm,
		Credentials: credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:   lifecycle.New(),
		Memory:      memory.New(),
		Comms:       comms.NewStubClient(),
		// Policy intentionally omitted — nil → legacy stub.
		GenerateID: func() string { return "agent-legacy-stub" },
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	err = f.HandleTaskSpec(&types.TaskSpec{
		TaskID:         "task-legacy-stub",
		RequiredSkills: []string{"web"},
		Instructions:   "fetch something",
		TraceID:        "trace-legacy-stub",
	})
	if err != nil {
		t.Errorf("legacy stub path must succeed without a policy; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}
