package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// validSHA is a syntactically correct 40-char hex SHA used across tests.
const validSHA = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

// minimalManifest returns the smallest valid externalSkillManifest.
func minimalManifest() *externalSkillManifest {
	return &externalSkillManifest{
		Name:        "test_skill",
		Label:       "Test Skill",
		Description: "Does a test thing. Do NOT use for anything else.",
		Execution: externalExecution{
			Type:        "http_call",
			URLTemplate: "https://example.com/{{city}}",
			Method:      "GET",
		},
		Parameters: []externalParamDef{
			{Name: "city", Type: "string", Required: true, Description: "The city name."},
		},
	}
}

// ---- parseRepoRef ----

func TestParseRepoRef_Valid(t *testing.T) {
	owner, repo, sha, err := parseRepoRef("myorg/myrepo@" + validSHA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "myorg" || repo != "myrepo" || sha != validSHA {
		t.Errorf("got owner=%q repo=%q sha=%q", owner, repo, sha)
	}
}

func TestParseRepoRef_MissingAt(t *testing.T) {
	_, _, _, err := parseRepoRef("myorg/myrepo")
	if err == nil {
		t.Error("expected error for missing @, got nil")
	}
}

func TestParseRepoRef_BranchInsteadOfSHA(t *testing.T) {
	_, _, _, err := parseRepoRef("myorg/myrepo@main")
	if err == nil {
		t.Error("expected error for branch name, got nil")
	}
}

func TestParseRepoRef_ShortSHA(t *testing.T) {
	_, _, _, err := parseRepoRef("myorg/myrepo@abc123")
	if err == nil {
		t.Error("expected error for short SHA, got nil")
	}
}

func TestParseRepoRef_UppercaseSHARejected(t *testing.T) {
	uppercase := strings.ToUpper(validSHA)
	_, _, _, err := parseRepoRef("myorg/myrepo@" + uppercase)
	if err == nil {
		t.Error("expected error for uppercase SHA, got nil")
	}
}

func TestParseRepoRef_MissingOwner(t *testing.T) {
	_, _, _, err := parseRepoRef("/myrepo@" + validSHA)
	if err == nil {
		t.Error("expected error for missing owner, got nil")
	}
}

func TestParseRepoRef_MissingRepo(t *testing.T) {
	_, _, _, err := parseRepoRef("myorg/@" + validSHA)
	if err == nil {
		t.Error("expected error for missing repo, got nil")
	}
}

func TestParseRepoRef_RepoWithSlashesPreservesFirst(t *testing.T) {
	// Only the first slash separates owner from repo; the rest belong to repo.
	owner, repo, _, err := parseRepoRef("myorg/subrepo@" + validSHA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "myorg" || repo != "subrepo" {
		t.Errorf("got owner=%q repo=%q", owner, repo)
	}
}

// ---- validateManifest ----

func TestValidateManifest_Valid(t *testing.T) {
	if err := validateManifest(minimalManifest()); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestValidateManifest_MissingName(t *testing.T) {
	m := minimalManifest()
	m.Name = ""
	if err := validateManifest(m); err == nil {
		t.Error("expected error for missing name")
	}
}

func TestValidateManifest_NameTooLong(t *testing.T) {
	m := minimalManifest()
	m.Name = strings.Repeat("a", externalMaxNameLen+1)
	if err := validateManifest(m); err == nil {
		t.Errorf("expected error for name longer than %d chars", externalMaxNameLen)
	}
}

func TestValidateManifest_MissingLabel(t *testing.T) {
	m := minimalManifest()
	m.Label = ""
	if err := validateManifest(m); err == nil {
		t.Error("expected error for missing label")
	}
}

func TestValidateManifest_MissingDescription(t *testing.T) {
	m := minimalManifest()
	m.Description = ""
	if err := validateManifest(m); err == nil {
		t.Error("expected error for missing description")
	}
}

func TestValidateManifest_DescriptionTooLong(t *testing.T) {
	m := minimalManifest()
	m.Description = strings.Repeat("x", externalMaxDescLen+1)
	if err := validateManifest(m); err == nil {
		t.Errorf("expected error for description longer than %d chars", externalMaxDescLen)
	}
}

func TestValidateManifest_TimeoutNegative(t *testing.T) {
	m := minimalManifest()
	m.TimeoutSeconds = -1
	if err := validateManifest(m); err == nil {
		t.Error("expected error for negative timeout")
	}
}

func TestValidateManifest_TimeoutExceedsMax(t *testing.T) {
	m := minimalManifest()
	m.TimeoutSeconds = externalMaxTimeout + 1
	if err := validateManifest(m); err == nil {
		t.Errorf("expected error for timeout > %d", externalMaxTimeout)
	}
}

func TestValidateManifest_TimeoutZeroAllowed(t *testing.T) {
	m := minimalManifest()
	m.TimeoutSeconds = 0
	if err := validateManifest(m); err != nil {
		t.Errorf("timeout=0 should be allowed: %v", err)
	}
}

func TestValidateManifest_UnsupportedExecutionType(t *testing.T) {
	m := minimalManifest()
	m.Execution.Type = "shell_exec"
	if err := validateManifest(m); err == nil {
		t.Error("expected error for unsupported execution type")
	}
}

func TestValidateManifest_MissingURLTemplate(t *testing.T) {
	m := minimalManifest()
	m.Execution.URLTemplate = ""
	if err := validateManifest(m); err == nil {
		t.Error("expected error for missing url_template")
	}
}

func TestValidateManifest_BadMethod(t *testing.T) {
	m := minimalManifest()
	m.Execution.Method = "DELETE"
	if err := validateManifest(m); err == nil {
		t.Error("expected error for unsupported HTTP method")
	}
}

func TestValidateManifest_MethodCaseInsensitive(t *testing.T) {
	m := minimalManifest()
	m.Execution.Method = "post" // lowercase — should be normalised and accepted
	if err := validateManifest(m); err != nil {
		t.Errorf("lowercase method should be accepted: %v", err)
	}
}

func TestValidateManifest_EmptyMethodAllowed(t *testing.T) {
	m := minimalManifest()
	m.Execution.Method = ""
	if err := validateManifest(m); err != nil {
		t.Errorf("empty method (defaults to GET) should be accepted: %v", err)
	}
}

func TestValidateManifest_ParameterMissingDescription(t *testing.T) {
	m := minimalManifest()
	m.Parameters = []externalParamDef{{Name: "foo", Type: "string", Required: true, Description: ""}}
	if err := validateManifest(m); err == nil {
		t.Error("expected error for parameter with no description")
	}
}

func TestValidateManifest_ParameterMissingName(t *testing.T) {
	m := minimalManifest()
	m.Parameters = []externalParamDef{{Name: "", Type: "string", Description: "some desc"}}
	if err := validateManifest(m); err == nil {
		t.Error("expected error for parameter with no name")
	}
}

// ---- substituteTemplate ----

func TestSubstituteTemplate_SinglePlaceholder(t *testing.T) {
	got := substituteTemplate("https://example.com/{{city}}", map[string]interface{}{"city": "london"})
	if got != "https://example.com/london" {
		t.Errorf("got %q", got)
	}
}

func TestSubstituteTemplate_MultiplePlaceholders(t *testing.T) {
	got := substituteTemplate("{{a}}-{{b}}", map[string]interface{}{"a": "foo", "b": "bar"})
	if got != "foo-bar" {
		t.Errorf("got %q", got)
	}
}

func TestSubstituteTemplate_NumericValue(t *testing.T) {
	got := substituteTemplate("count={{n}}", map[string]interface{}{"n": float64(42)})
	if got != "count=42" {
		t.Errorf("got %q", got)
	}
}

func TestSubstituteTemplate_UnknownPlaceholderUnchanged(t *testing.T) {
	got := substituteTemplate("hello={{missing}}", map[string]interface{}{})
	if got != "hello={{missing}}" {
		t.Errorf("unresolved placeholder should remain: got %q", got)
	}
}

func TestSubstituteTemplate_NoPlaceholders(t *testing.T) {
	got := substituteTemplate("static", map[string]interface{}{"unused": "val"})
	if got != "static" {
		t.Errorf("got %q", got)
	}
}

// ---- DynamicRegistry ----

func TestDynamicRegistry_RegisterAndRetrieve(t *testing.T) {
	r := newDynamicRegistry(nil)
	tool := minimalSkillTool("alpha")
	if err := r.Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tools := r.Tools()
	if len(tools) != 1 || tools[0].Definition.Name != "alpha" {
		t.Errorf("expected [alpha], got %v", toolNames(tools))
	}
}

func TestDynamicRegistry_InitialToolsPreserved(t *testing.T) {
	initial := []SkillTool{minimalSkillTool("one"), minimalSkillTool("two")}
	r := newDynamicRegistry(initial)
	tools := r.Tools()
	if len(tools) != 2 {
		t.Errorf("expected 2 initial tools, got %d", len(tools))
	}
}

func TestDynamicRegistry_DuplicateRejected(t *testing.T) {
	r := newDynamicRegistry([]SkillTool{minimalSkillTool("dupe")})
	if err := r.Register(minimalSkillTool("dupe")); err == nil {
		t.Error("expected error for duplicate tool name, got nil")
	}
}

func TestDynamicRegistry_SnapshotIsolated(t *testing.T) {
	r := newDynamicRegistry(nil)
	_ = r.Register(minimalSkillTool("a"))
	snap1 := r.Tools()
	_ = r.Register(minimalSkillTool("b"))
	snap2 := r.Tools()

	if len(snap1) != 1 {
		t.Errorf("snap1 should have 1 tool, got %d", len(snap1))
	}
	if len(snap2) != 2 {
		t.Errorf("snap2 should have 2 tools, got %d", len(snap2))
	}
}

// ---- buildHTTPSkillTool ----

func TestBuildHTTPSkillTool_FieldsMapped(t *testing.T) {
	m := minimalManifest()
	m.TimeoutSeconds = 60
	tool := buildHTTPSkillTool(m)

	if tool.Definition.Name != m.Name {
		t.Errorf("name: want %q, got %q", m.Name, tool.Definition.Name)
	}
	if tool.Label != m.Label {
		t.Errorf("label: want %q, got %q", m.Label, tool.Label)
	}
	if tool.TimeoutSeconds != 60 {
		t.Errorf("timeout: want 60, got %d", tool.TimeoutSeconds)
	}
	if len(tool.RequiredCredentialTypes) != 0 {
		t.Errorf("v1 skills must have no credential types, got %v", tool.RequiredCredentialTypes)
	}
}

func TestBuildHTTPSkillTool_DefaultTimeout(t *testing.T) {
	m := minimalManifest()
	m.TimeoutSeconds = 0
	tool := buildHTTPSkillTool(m)
	if tool.TimeoutSeconds != 30 {
		t.Errorf("zero timeout should default to 30, got %d", tool.TimeoutSeconds)
	}
}

func TestBuildHTTPSkillTool_RequiredParamsInSchema(t *testing.T) {
	m := minimalManifest()
	tool := buildHTTPSkillTool(m)

	schema := tool.Definition.InputSchema

	// Required is []string on ToolInputSchemaParam.
	found := false
	for _, r := range schema.Required {
		if r == "city" {
			found = true
		}
	}
	if !found {
		t.Errorf("required param 'city' not in schema.Required: %v", schema.Required)
	}

	// Properties is any (map[string]interface{} at runtime).
	props, ok := schema.Properties.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{} Properties, got %T", schema.Properties)
	}
	if _, ok := props["city"]; !ok {
		t.Error("param 'city' not in schema.Properties")
	}
}

// ---- executeHTTPSkill ----

func TestExecuteHTTPSkill_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "sunny")
	}))
	defer srv.Close()

	m := minimalManifest()
	m.Execution.URLTemplate = srv.URL + "/weather/{{city}}"
	raw, _ := json.Marshal(map[string]string{"city": "paris"})

	result := executeHTTPSkill(context.Background(), m, raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "sunny") {
		t.Errorf("expected response body in content, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "HTTP 200") {
		t.Errorf("expected status line in content, got %q", result.Content)
	}
}

func TestExecuteHTTPSkill_SubstitutesURLParam(t *testing.T) {
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	m := minimalManifest()
	m.Execution.URLTemplate = srv.URL + "/v1/{{resource}}"
	raw, _ := json.Marshal(map[string]string{"resource": "widgets"})

	result := executeHTTPSkill(context.Background(), m, raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if receivedPath != "/v1/widgets" {
		t.Errorf("URL substitution: want path /v1/widgets, got %q", receivedPath)
	}
}

func TestExecuteHTTPSkill_4xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	m := minimalManifest()
	m.Execution.URLTemplate = srv.URL
	raw, _ := json.Marshal(map[string]string{})

	result := executeHTTPSkill(context.Background(), m, raw)
	if !result.IsError {
		t.Error("expected IsError=true for 404 response")
	}
}

func TestExecuteHTTPSkill_CustomHeader(t *testing.T) {
	var receivedHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-Custom")
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	m := minimalManifest()
	m.Execution.URLTemplate = srv.URL
	m.Execution.Headers = map[string]string{"X-Custom": "value-{{token}}"}
	raw, _ := json.Marshal(map[string]string{"token": "abc"})

	result := executeHTTPSkill(context.Background(), m, raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if receivedHeader != "value-abc" {
		t.Errorf("header substitution: want %q, got %q", "value-abc", receivedHeader)
	}
}

func TestExecuteHTTPSkill_InvalidParams(t *testing.T) {
	m := minimalManifest()
	result := executeHTTPSkill(context.Background(), m, json.RawMessage(`not-json`))
	if !result.IsError {
		t.Error("expected IsError=true for invalid JSON params")
	}
}

func TestExecuteHTTPSkill_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block forever — the caller's context should cancel first.
		<-r.Context().Done()
	}))
	defer srv.Close()

	m := minimalManifest()
	m.Execution.URLTemplate = srv.URL
	raw, _ := json.Marshal(map[string]string{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result := executeHTTPSkill(ctx, m, raw)
	if !result.IsError {
		t.Error("expected IsError=true when context is cancelled")
	}
}

// ---- executeSkillLoad end-to-end (httptest as GitHub raw) ----

func TestExecuteSkillLoad_HappyPath(t *testing.T) {
	manifest := `
name: weather_fetch
label: Weather Fetch
description: "Fetch weather for a city. Do NOT use for other data."
timeout_seconds: 10
execution:
  type: http_call
  url_template: "https://wttr.in/{{city}}?format=3"
  method: GET
parameters:
  - name: city
    type: string
    required: true
    description: "The city to look up."
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, manifest)
	}))
	defer srv.Close()

	// Point fetchManifest at the test server.
	original := rawGitHubBase
	rawGitHubBase = srv.URL
	defer func() { rawGitHubBase = original }()

	registry := newDynamicRegistry(nil)
	raw, _ := json.Marshal(map[string]string{"repo": "myorg/myrepo@" + validSHA})
	result := executeSkillLoad(context.Background(), registry, raw)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "weather_fetch") {
		t.Errorf("expected skill name in confirmation, got %q", result.Content)
	}

	// The skill should now be in the registry.
	tools := registry.Tools()
	found := false
	for _, tool := range tools {
		if tool.Definition.Name == "weather_fetch" {
			found = true
		}
	}
	if !found {
		t.Errorf("weather_fetch not registered; tools: %v", toolNames(tools))
	}
}

func TestExecuteSkillLoad_CustomPath(t *testing.T) {
	var requestedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		fmt.Fprint(w, `
name: custom_skill
label: Custom
description: "A custom skill. Do NOT use otherwise."
execution:
  type: http_call
  url_template: "https://example.com"
  method: GET
`)
	}))
	defer srv.Close()

	original := rawGitHubBase
	rawGitHubBase = srv.URL
	defer func() { rawGitHubBase = original }()

	registry := newDynamicRegistry(nil)
	raw, _ := json.Marshal(map[string]string{
		"repo": "myorg/myrepo@" + validSHA,
		"path": "skills/my-skill.yaml",
	})
	result := executeSkillLoad(context.Background(), registry, raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(requestedPath, "skills/my-skill.yaml") {
		t.Errorf("expected custom path in request, got %q", requestedPath)
	}
}

func TestExecuteSkillLoad_ManifestNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	original := rawGitHubBase
	rawGitHubBase = srv.URL
	defer func() { rawGitHubBase = original }()

	registry := newDynamicRegistry(nil)
	raw, _ := json.Marshal(map[string]string{"repo": "myorg/myrepo@" + validSHA})
	result := executeSkillLoad(context.Background(), registry, raw)
	if !result.IsError {
		t.Error("expected IsError=true for 404 manifest")
	}
}

func TestExecuteSkillLoad_InvalidYAML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, ":::not valid yaml:::")
	}))
	defer srv.Close()

	original := rawGitHubBase
	rawGitHubBase = srv.URL
	defer func() { rawGitHubBase = original }()

	registry := newDynamicRegistry(nil)
	raw, _ := json.Marshal(map[string]string{"repo": "myorg/myrepo@" + validSHA})
	result := executeSkillLoad(context.Background(), registry, raw)
	if !result.IsError {
		t.Error("expected IsError=true for invalid YAML")
	}
}

func TestExecuteSkillLoad_InvalidManifest(t *testing.T) {
	// Valid YAML but fails Tool Contract (missing label).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `
name: broken
description: "Missing label."
execution:
  type: http_call
  url_template: "https://example.com"
`)
	}))
	defer srv.Close()

	original := rawGitHubBase
	rawGitHubBase = srv.URL
	defer func() { rawGitHubBase = original }()

	registry := newDynamicRegistry(nil)
	raw, _ := json.Marshal(map[string]string{"repo": "myorg/myrepo@" + validSHA})
	result := executeSkillLoad(context.Background(), registry, raw)
	if !result.IsError {
		t.Error("expected IsError=true for manifest failing Tool Contract")
	}
}

func TestExecuteSkillLoad_DuplicateSkillRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `
name: dupe_skill
label: Dupe
description: "A duplicated skill. Do NOT use otherwise."
execution:
  type: http_call
  url_template: "https://example.com"
  method: GET
`)
	}))
	defer srv.Close()

	original := rawGitHubBase
	rawGitHubBase = srv.URL
	defer func() { rawGitHubBase = original }()

	// Pre-seed the registry with the same name.
	registry := newDynamicRegistry([]SkillTool{minimalSkillTool("dupe_skill")})
	raw, _ := json.Marshal(map[string]string{"repo": "myorg/myrepo@" + validSHA})
	result := executeSkillLoad(context.Background(), registry, raw)
	if !result.IsError {
		t.Error("expected IsError=true when skill name already registered")
	}
}

func TestExecuteSkillLoad_BadRepoRef(t *testing.T) {
	registry := newDynamicRegistry(nil)
	raw, _ := json.Marshal(map[string]string{"repo": "myorg/myrepo@main"})
	result := executeSkillLoad(context.Background(), registry, raw)
	if !result.IsError {
		t.Error("expected IsError=true for branch-name repo ref")
	}
}

func TestExecuteSkillLoad_InvalidParams(t *testing.T) {
	registry := newDynamicRegistry(nil)
	result := executeSkillLoad(context.Background(), registry, json.RawMessage(`not-json`))
	if !result.IsError {
		t.Error("expected IsError=true for invalid JSON params")
	}
}

// ---- helpers ----

// minimalSkillTool builds a no-op SkillTool with the given name for use in
// registry and dispatch tests.
func minimalSkillTool(name string) SkillTool {
	return SkillTool{
		Label: name,
		Definition: anthropic.ToolParam{
			Name:        name,
			Description: anthropic.String("test tool"),
			InputSchema: anthropic.ToolInputSchemaParam{},
		},
		Execute: func(_ context.Context, _ json.RawMessage) ToolResult {
			return ToolResult{Content: "ok"}
		},
	}
}
