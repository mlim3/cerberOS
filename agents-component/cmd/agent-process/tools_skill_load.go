// Package main — tools_skill_load.go implements the skill_load built-in tool.
//
// skill_load fetches an aegis-skill.yaml manifest from a public GitHub
// repository and registers the described skill into the running agent's
// DynamicRegistry so it is available immediately on the next Reason phase.
//
// v1 constraints:
//   - Only http_call execution type is supported (no credentialed operations).
//   - The repo reference must include a full 40-character commit SHA — branch
//     names are rejected to prevent supply-chain drift.
//   - The manifest is fetched from raw.githubusercontent.com; no GitHub token
//     is used, so only public repositories are accessible.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"go.yaml.in/yaml/v2"
)

// Tool Contract limits (mirrors skills.ValidateCommandContract — EDD §13.2).
const (
	externalMaxNameLen = 64
	externalMaxDescLen = 300
	externalMaxTimeout = 300
)

// shaPattern matches a full 40-character lowercase hex SHA.
// Branch names, tags, and short SHAs are all rejected.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// externalSkillManifest is the schema for an aegis-skill.yaml placed in a
// GitHub repository. It defines everything needed to build and execute the skill.
type externalSkillManifest struct {
	Name           string              `yaml:"name"`
	Label          string              `yaml:"label"`
	Description    string              `yaml:"description"`
	TimeoutSeconds int                 `yaml:"timeout_seconds"`
	Execution      externalExecution   `yaml:"execution"`
	Parameters     []externalParamDef  `yaml:"parameters"`
}

// externalExecution describes how the skill is executed.
// Only type "http_call" is supported in v1.
type externalExecution struct {
	Type         string            `yaml:"type"`          // must be "http_call"
	URLTemplate  string            `yaml:"url_template"`  // {{param}} placeholders substituted at call time
	Method       string            `yaml:"method"`        // GET | POST; defaults to GET
	Headers      map[string]string `yaml:"headers"`       // static or {{param}} header values
	BodyTemplate string            `yaml:"body_template"` // POST body; {{param}} placeholders substituted at call time
}

// externalParamDef describes one parameter in the external skill's input schema.
type externalParamDef struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
}

// skillLoadTool returns the skill_load SkillTool. The registry reference
// allows the tool to register newly loaded skills without restarting the agent.
func skillLoadTool(registry *DynamicRegistry) SkillTool {
	return SkillTool{
		Label:                   "Skill Load",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          30,
		Definition: anthropic.ToolParam{
			Name: "skill_load",
			Description: anthropic.String(
				"Load a skill from a public GitHub repository and make it available immediately. " +
					"Use when the user asks to load, install, or use a skill from GitHub. " +
					"Do NOT use for skills already in your current domain. " +
					"Do NOT pass credentials or secrets in any parameter. " +
					"Only local-execution skills (no external credentials required) can be loaded in v1."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"repo": map[string]interface{}{
						"type":        "string",
						"description": "GitHub repository and full commit SHA in the format \"owner/repo@<40-char-sha>\". A full commit SHA is required — branch names are not accepted.",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Path to the aegis-skill.yaml manifest within the repository. Defaults to \"aegis-skill.yaml\" at the root when omitted.",
					},
				},
				Required: []string{"repo"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeSkillLoad(ctx, registry, raw)
		},
	}
}

func executeSkillLoad(ctx context.Context, registry *DynamicRegistry, raw json.RawMessage) ToolResult {
	var params struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
	}
	if params.Path == "" {
		params.Path = "aegis-skill.yaml"
	}

	owner, repo, sha, err := parseRepoRef(params.Repo)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}

	manifest, err := fetchManifest(ctx, owner, repo, sha, params.Path)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to fetch skill manifest: %v", err),
			IsError: true,
			Details: map[string]interface{}{"repo": fmt.Sprintf("%s/%s", owner, repo), "sha": sha},
		}
	}

	if err := validateManifest(manifest); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid skill manifest: %v", err),
			IsError: true,
		}
	}

	tool := buildHTTPSkillTool(manifest)
	if err := registry.Register(tool); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("skill registration failed: %v", err),
			IsError: true,
		}
	}

	return ToolResult{
		Content: fmt.Sprintf(
			"Skill %q loaded from %s/%s@%.8s. It is now available as a tool — call it by name.",
			manifest.Name, owner, repo, sha,
		),
		Details: map[string]interface{}{
			"skill_name": manifest.Name,
			"repo":       fmt.Sprintf("%s/%s", owner, repo),
			"sha":        sha,
		},
	}
}

// parseRepoRef parses "owner/repo@<sha>" into its components.
// Returns an error if the format is invalid or the SHA is not 40 hex chars.
func parseRepoRef(ref string) (owner, repo, sha string, err error) {
	atIdx := strings.LastIndex(ref, "@")
	if atIdx < 0 {
		return "", "", "", fmt.Errorf("repo must include a commit SHA in the format \"owner/repo@<40-char-sha>\"")
	}
	repoPath := ref[:atIdx]
	sha = ref[atIdx+1:]

	if !shaPattern.MatchString(sha) {
		return "", "", "", fmt.Errorf(
			"commit SHA must be a 40-character lowercase hex string; got %q — use a full commit SHA, not a branch name",
			sha,
		)
	}

	parts := strings.SplitN(repoPath, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("repo must be in the format \"owner/repo@sha\", got %q", ref)
	}
	return parts[0], parts[1], sha, nil
}

// fetchManifest downloads and parses the aegis-skill.yaml from GitHub raw content.
// The URL is always constructed from the validated owner/repo/sha components so
// it is guaranteed to point to raw.githubusercontent.com.
func fetchManifest(ctx context.Context, owner, repo, sha, path string) (*externalSkillManifest, error) {
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, sha, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "aegis-agent/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("manifest not found at %s/%s@%.8s (path: %s)", owner, repo, sha, path)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP %d fetching manifest", resp.StatusCode)
	}

	// Cap manifest size at 64KB — manifests should be tiny; large ones indicate
	// something unexpected and we should not parse unbounded remote content.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var m externalSkillManifest
	if err := yaml.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	return &m, nil
}

// validateManifest enforces the Tool Contract (EDD §13.2) on an external skill.
// v1 also rejects any skill that declares credential requirements.
func validateManifest(m *externalSkillManifest) error {
	if m.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(m.Name) > externalMaxNameLen {
		return fmt.Errorf("name %q exceeds %d characters", m.Name, externalMaxNameLen)
	}
	if m.Label == "" {
		return fmt.Errorf("label is required")
	}
	if m.Description == "" {
		return fmt.Errorf("description is required")
	}
	if len(m.Description) > externalMaxDescLen {
		return fmt.Errorf("description for %q exceeds %d characters", m.Name, externalMaxDescLen)
	}
	if m.TimeoutSeconds < 0 || m.TimeoutSeconds > externalMaxTimeout {
		return fmt.Errorf("timeout_seconds must be 0–%d, got %d", externalMaxTimeout, m.TimeoutSeconds)
	}
	if m.Execution.Type != "http_call" {
		return fmt.Errorf("execution.type %q is not supported — only \"http_call\" is supported in v1", m.Execution.Type)
	}
	if m.Execution.URLTemplate == "" {
		return fmt.Errorf("execution.url_template is required for http_call")
	}
	method := strings.ToUpper(m.Execution.Method)
	if method != "" && method != "GET" && method != "POST" {
		return fmt.Errorf("execution.method must be GET or POST, got %q", m.Execution.Method)
	}
	for _, p := range m.Parameters {
		if p.Name == "" {
			return fmt.Errorf("every parameter must have a name")
		}
		if p.Description == "" {
			return fmt.Errorf("parameter %q has no description", p.Name)
		}
	}
	return nil
}

// buildHTTPSkillTool converts a validated external manifest into a SkillTool
// whose Execute performs the declared http_call with parameter substitution.
func buildHTTPSkillTool(m *externalSkillManifest) SkillTool {
	timeout := m.TimeoutSeconds
	if timeout == 0 {
		timeout = 30
	}

	props := make(map[string]interface{}, len(m.Parameters))
	var required []string
	for _, p := range m.Parameters {
		props[p.Name] = map[string]interface{}{
			"type":        p.Type,
			"description": p.Description,
		}
		if p.Required {
			required = append(required, p.Name)
		}
	}

	manifest := *m // copy so the closure does not hold the pointer from the caller
	return SkillTool{
		Label:                   manifest.Label,
		RequiredCredentialTypes: nil, // v1: credentialed external skills are not supported
		TimeoutSeconds:          timeout,
		Definition: anthropic.ToolParam{
			Name:        manifest.Name,
			Description: anthropic.String(manifest.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: props,
				Required:   required,
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeHTTPSkill(ctx, &manifest, raw)
		},
	}
}

// executeHTTPSkill performs the http_call described by the manifest.
// Parameter values are substituted into the URL, headers, and body templates.
func executeHTTPSkill(ctx context.Context, m *externalSkillManifest, raw json.RawMessage) ToolResult {
	var params map[string]interface{}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
	}

	url := substituteTemplate(m.Execution.URLTemplate, params)
	method := strings.ToUpper(m.Execution.Method)
	if method == "" {
		method = "GET"
	}

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("failed to build request: %v", err), IsError: true}
	}
	req.Header.Set("User-Agent", "aegis-agent/1.0")
	for k, v := range m.Execution.Headers {
		req.Header.Set(k, substituteTemplate(v, params))
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		content := fmt.Sprintf("HTTP request failed: %v", err)
		if ctx.Err() == context.DeadlineExceeded {
			content = fmt.Sprintf("TOOL_TIMEOUT: %s did not complete within the allowed time", m.Name)
		}
		return ToolResult{
			Content: content,
			IsError: true,
			Details: map[string]interface{}{"url": url, "elapsed_ms": elapsed.Milliseconds()},
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxContentBytes))
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("failed to read response: %v", err), IsError: true}
	}

	truncated := len(body) == maxContentBytes
	content := fmt.Sprintf("HTTP %d\n\n%s", resp.StatusCode, string(body))
	if truncated {
		content += "\n[CONTENT TRUNCATED — response exceeded 16KB limit]"
	}

	return ToolResult{
		Content: content,
		IsError: resp.StatusCode >= 400,
		Details: map[string]interface{}{
			"skill":       m.Name,
			"url":         url,
			"status_code": resp.StatusCode,
			"elapsed_ms":  elapsed.Milliseconds(),
			"truncated":   truncated,
		},
	}
}

// substituteTemplate replaces {{key}} placeholders in template with the
// corresponding values from params. Non-string values are JSON-encoded.
func substituteTemplate(template string, params map[string]interface{}) string {
	result := template
	for k, v := range params {
		var s string
		if str, ok := v.(string); ok {
			s = str
		} else {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		result = strings.ReplaceAll(result, "{{"+k+"}}", s)
	}
	return result
}
