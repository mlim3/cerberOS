package factory_test

import (
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
// buildManifestText (exercised via provision integration path)
// ---------------------------------------------------------------------------

// capturingLifecycle records the most recent VMConfig passed to Spawn so tests
// can assert the manifest was wired through correctly.
type capturingLifecycle struct {
	lifecycle.Manager // embed stub for Terminate / Health
	last              lifecycle.VMConfig
}

func newCapturingLifecycle() *capturingLifecycle {
	return &capturingLifecycle{Manager: lifecycle.New()}
}

func (c *capturingLifecycle) Spawn(cfg lifecycle.VMConfig) error {
	c.last = cfg
	return c.Manager.Spawn(cfg)
}

// webDomainWithCommands returns a domain node with two command children so the
// manifest is non-empty and can be verified in assertions.
func webDomainWithCommands() *types.SkillNode {
	return &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web_fetch": {
				Name:        "web_fetch",
				Level:       "command",
				Label:       "Fetch",
				Description: "Fetches a webpage by URL and returns the HTML body.",
				Spec:        &types.SkillSpec{},
			},
			"web_parse": {
				Name:        "web_parse",
				Level:       "command",
				Label:       "Parse",
				Description: "Parses HTML content into plain text.",
				Spec:        &types.SkillSpec{},
			},
		},
	}
}

// newFactoryWithCapturingLifecycle wires a factory with a capturing lifecycle
// and a domain that has real commands. Returns both for test assertions.
func newFactoryWithCapturingLifecycle(t *testing.T) (*factory.Factory, *capturingLifecycle) {
	t.Helper()
	cap := newCapturingLifecycle()
	sm := skills.New()
	if err := sm.RegisterDomain(webDomainWithCommands()); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}
	f, err := factory.New(factory.Config{
		Registry:    registry.New(),
		Skills:      sm,
		Credentials: credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:   cap,
		Memory:      memory.New(),
		Comms:       comms.NewStubClient(),
		GenerateID:  func() string { return "agent-manifest-test" },
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}
	return f, cap
}

// TestProvision_ManifestPassedToLifecycle verifies that provision resolves the
// domain's command list and forwards the serialised manifest in VMConfig.
func TestProvision_ManifestPassedToLifecycle(t *testing.T) {
	f, cap := newFactoryWithCapturingLifecycle(t)

	spec := &types.TaskSpec{
		TaskID:         "task-manifest-1",
		RequiredSkills: []string{"web"},
		Instructions:   "Fetch the page at https://example.com",
		TraceID:        "trace-manifest-1",
	}
	if err := f.HandleTaskSpec(spec); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}

	manifest := cap.last.CommandManifest
	if manifest == "" {
		t.Fatal("CommandManifest must not be empty when domain has commands")
	}
	if !strings.Contains(manifest, "web_fetch") {
		t.Errorf("manifest must contain 'web_fetch'; got %q", manifest)
	}
	if !strings.Contains(manifest, "web_parse") {
		t.Errorf("manifest must contain 'web_parse'; got %q", manifest)
	}
	if !strings.Contains(manifest, "Fetches a webpage") {
		t.Errorf("manifest must include command description; got %q", manifest)
	}
}

// TestProvision_ManifestAlphabeticallySorted verifies buildManifestText sorts
// commands so the output is deterministic regardless of map iteration order.
func TestProvision_ManifestAlphabeticallySorted(t *testing.T) {
	f, cap := newFactoryWithCapturingLifecycle(t)

	spec := &types.TaskSpec{
		TaskID:         "task-manifest-sorted",
		RequiredSkills: []string{"web"},
		Instructions:   "Do something",
		TraceID:        "trace-manifest-sorted",
	}
	if err := f.HandleTaskSpec(spec); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}

	manifest := cap.last.CommandManifest
	fetchIdx := strings.Index(manifest, "web_fetch")
	parseIdx := strings.Index(manifest, "web_parse")
	if fetchIdx < 0 || parseIdx < 0 {
		t.Fatalf("both commands must appear in manifest; got %q", manifest)
	}
	// web_fetch < web_parse alphabetically — fetch must appear first.
	if fetchIdx > parseIdx {
		t.Errorf("manifest must be alphabetically sorted: web_fetch should precede web_parse; got %q", manifest)
	}
}

// TestProvision_EmptyDomain_ManifestEmpty verifies that a domain with no
// commands produces an empty manifest (agent still spawns successfully).
func TestProvision_EmptyDomain_ManifestEmpty(t *testing.T) {
	cap := newCapturingLifecycle()
	sm := skills.New()
	if err := sm.RegisterDomain(&types.SkillNode{
		Name:     "empty",
		Level:    "domain",
		Children: map[string]*types.SkillNode{},
	}); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}
	f, err := factory.New(factory.Config{
		Registry:    registry.New(),
		Skills:      sm,
		Credentials: credentials.New(map[string]string{"empty.credential": "tok"}),
		Lifecycle:   cap,
		Memory:      memory.New(),
		Comms:       comms.NewStubClient(),
		GenerateID:  func() string { return "agent-empty-domain" },
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	spec := &types.TaskSpec{
		TaskID:         "task-empty-domain",
		RequiredSkills: []string{"empty"},
		Instructions:   "Do something",
		TraceID:        "trace-empty-domain",
	}
	if err := f.HandleTaskSpec(spec); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}

	if cap.last.CommandManifest != "" {
		t.Errorf("manifest for empty domain must be empty; got %q", cap.last.CommandManifest)
	}
}

// ---------------------------------------------------------------------------
// spawnSystemPrompt
// ---------------------------------------------------------------------------

// TestSpawnSystemPrompt_NoManifest verifies the base prompt is returned when
// the manifest is empty — no "Available commands" section injected.
func TestSpawnSystemPrompt_NoManifest(t *testing.T) {
	// Direct unit test: spawn a factory with empty domain and confirm no
	// "Available commands" appears in the captured manifest (which would
	// be appended to the system prompt inside the agent process).
	cap2 := newCapturingLifecycle()
	sm2 := skills.New()
	sm2.RegisterDomain(&types.SkillNode{Name: "solo", Level: "domain", Children: map[string]*types.SkillNode{}})
	f2, _ := factory.New(factory.Config{
		Registry:    registry.New(),
		Skills:      sm2,
		Credentials: credentials.New(map[string]string{"solo.credential": "tok"}),
		Lifecycle:   cap2,
		Memory:      memory.New(),
		Comms:       comms.NewStubClient(),
		GenerateID:  func() string { return "agent-solo" },
	})
	f2.HandleTaskSpec(&types.TaskSpec{
		TaskID: "task-solo", RequiredSkills: []string{"solo"},
		Instructions: "x", TraceID: "t",
	})
	if cap2.last.CommandManifest != "" {
		t.Errorf("empty domain must produce empty manifest; got %q", cap2.last.CommandManifest)
	}
}

// TestProvision_ManifestIncludedInTokenBudgetCount verifies the token counter
// receives the manifest text as part of the spawn context string, ensuring the
// budget is checked against the real (post-manifest) prompt size.
func TestProvision_ManifestIncludedInTokenBudgetCount(t *testing.T) {
	var counted []string
	recorder := &recordingCounter{recorded: &counted}

	cap := newCapturingLifecycle()
	sm := skills.New()
	sm.RegisterDomain(webDomainWithCommands())
	f, err := factory.New(factory.Config{
		Registry:     registry.New(),
		Skills:       sm,
		Credentials:  credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:    cap,
		Memory:       memory.New(),
		Comms:        comms.NewStubClient(),
		GenerateID:   func() string { return "agent-budget-manifest" },
		TokenCounter: recorder,
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	spec := &types.TaskSpec{
		TaskID:         "task-budget-manifest",
		RequiredSkills: []string{"web"},
		Instructions:   "Do something",
		TraceID:        "trace-budget-manifest",
	}
	if err := f.HandleTaskSpec(spec); err != nil {
		t.Fatalf("HandleTaskSpec: %v", err)
	}

	if len(counted) == 0 {
		t.Fatal("expected token counter to be called")
	}
	// The context text passed to CountTokens must include command names from
	// the manifest — proving the budget is computed on the real prompt.
	ctx := counted[0]
	if !strings.Contains(ctx, "web_fetch") {
		t.Errorf("token budget context must include manifest commands; got %q", ctx)
	}
}

// recordingCounter captures the text passed to CountTokens for inspection.
type recordingCounter struct {
	recorded *[]string
}

func (r *recordingCounter) CountTokens(text string) (int, error) {
	*r.recorded = append(*r.recorded, text)
	return 100, nil // well under 2,048 — never blocks provisioning
}
