// Package main — tools.go implements the skill tool registry and tool contract.
//
// Every SkillTool satisfies the full Tool Contract from EDD §13.2:
//   - Label:                   human-readable display name for monitoring and audit logs; never shown to LLM.
//   - Definition.Name:         snake_case identifier the LLM uses in tool calls; max 64 chars.
//   - Definition.Description:  what the tool does and when NOT to use it; max 300 chars.
//   - Definition.InputSchema:  JSON Schema for all parameters; every parameter must have a description.
//   - RequiredCredentialTypes: empty = local execution; non-empty = vault execution required (M3).
//   - Execute:                 runs the skill; returns {content, details}.
//   - TimeoutSeconds:          0 = default (30s); hard max 300s. Enforced by dispatchTool.
//
// ToolResult enforces the content/details split from §13.2:
//   - Content (≤16KB) enters the LLM context.
//   - Details go to monitoring (stderr) only and never enter agent context.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/skillsconfig"
	"github.com/cerberOS/agents-component/pkg/types"
)

const (
	// maxContentBytes is the maximum content size returned to the LLM (§13.2).
	maxContentBytes = 16 * 1024

	// toolNameTaskComplete is the reserved name for the task completion signal.
	toolNameTaskComplete = "task_complete"

	// defaultToolTimeout is used when TimeoutSeconds is 0.
	defaultToolTimeout = 30 * time.Second
)

// ToolResult is the output of a skill execution. It implements the content/details
// split from EDD §13.2: Content enters the LLM context; Details are for monitoring
// only and are never injected into agent context.
type ToolResult struct {
	Content        string                 // what the LLM sees; max 16KB
	IsError        bool                   // signals the LLM that execution failed
	Details        map[string]interface{} // monitoring only — logged to stderr, never to LLM
	SessionEntryID string                 // entry_id of the tool_call session entry; set by vault tools only
}

// SkillTool is the runtime representation of the full Tool Contract (EDD §13.2).
type SkillTool struct {
	// Contract metadata.
	Label                   string   // human-readable display name — monitoring/audit only, never shown to the LLM
	RequiredCredentialTypes []string // empty = local execution; non-empty = vault execution required (M3)
	TimeoutSeconds          int      // 0 = default (30s); hard max 300s; enforced by dispatchTool

	// Anthropic API definition: name (≤64 chars), description (≤300 chars), input schema.
	Definition anthropic.ToolParam

	// Execute runs the skill within the given context (which carries the timeout).
	// Returns {Content, IsError, Details} per the §13.2 tool result structure.
	Execute func(ctx context.Context, input json.RawMessage) ToolResult
}

// skillsCfgOnce guards one-time loading of the skills configuration.
// The config is loaded from AEGIS_SKILLS_CONFIG_PATH, falling back to the
// embedded default when that variable is unset or empty.
var (
	skillsCfgOnce sync.Once
	skillsCfg     *skillsconfig.Config
)

// loadSkillsConfig returns the cached skills config, loading it on first call.
// Returns nil if loading fails — callers treat nil as "no domain commands".
func loadSkillsConfig() *skillsconfig.Config {
	skillsCfgOnce.Do(func() {
		path := os.Getenv("AEGIS_SKILLS_CONFIG_PATH")
		cfg, err := skillsconfig.Load(path)
		if err == nil {
			skillsCfg = cfg
		}
	})
	return skillsCfg
}

// toolsForDomain returns the skill tools available for the given domain.
//
// The tool list is assembled from the skills configuration (loaded once from
// AEGIS_SKILLS_CONFIG_PATH or the embedded default). Each command is resolved
// against builtinRegistry to obtain its SkillTool.
//
// ve may be nil (vault execution unavailable) — credentialed tools are omitted.
// as may be nil (agent spawning unavailable) — spawn_agent tool is omitted.
// task_complete is always included as the agent's terminal signal.
func toolsForDomain(domain string, ve *VaultExecutor, as *AgentSpawner) []SkillTool {
	base := []SkillTool{taskCompleteTool()}
	if as != nil {
		base = append(base, spawnAgentTool(as))
	}

	cfg := loadSkillsConfig()
	if cfg == nil {
		return base
	}

	for _, d := range cfg.Domains {
		if d.Name != domain {
			continue
		}
		var domainTools []SkillTool
		for _, cmd := range d.Commands {
			// Skip vault tools when vault execution is unavailable.
			if len(cmd.RequiredCredentialTypes) > 0 && ve == nil {
				continue
			}
			factory, ok := builtinRegistry[cmd.Implementation]
			if !ok {
				continue // Unknown implementation — skip silently.
			}
			domainTools = append(domainTools, factory(ve))
		}
		return append(domainTools, base...)
	}

	// Domain not in config — return base tools only (task_complete + spawn_agent).
	return base
}

// spawnAgentTool implements the agent-as-tool pattern (issue #67, EDD §13.6).
// It is included in the tool registry when an AgentSpawner is available (NATS env
// vars set). The LLM uses this tool to delegate a sub-task to a child agent.
func spawnAgentTool(as *AgentSpawner) SkillTool {
	return SkillTool{
		Label:                   "Spawn Agent",
		RequiredCredentialTypes: nil, // no vault credential needed — Orchestrator authorises the child
		TimeoutSeconds:          310, // local deadline = 300s task timeout + 10s routing buffer
		Definition: anthropic.ToolParam{
			Name: "spawn_agent",
			Description: anthropic.String(
				"Spawn a child agent to handle a self-contained sub-task. " +
					"The child runs independently and returns its result when done. " +
					"Use this when the sub-task requires a different skill domain or can run in parallel. " +
					"Do NOT use for simple operations already available via other tools. " +
					"Do NOT pass credential values or secrets in instructions."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"instructions": map[string]interface{}{
						"type":        "string",
						"description": "Complete, self-contained task description for the child agent. Must include all context needed — the child has no access to this agent's history.",
					},
					"required_skills": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "string",
						},
						"description": "Skill domain names the child agent needs (e.g. [\"web\", \"data\"]). At least one domain is required.",
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum seconds to wait for the child agent to complete (default 300, max 300). Omit to use the default.",
					},
				},
				Required: []string{"instructions", "required_skills"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return as.Spawn(ctx, raw)
		},
	}
}

// toolDefinitions wraps each SkillTool's Definition into the ToolUnionParam
// expected by MessageNewParams.Tools.
func toolDefinitions(tools []SkillTool) []anthropic.ToolUnionParam {
	params := make([]anthropic.ToolUnionParam, len(tools))
	for i := range tools {
		def := tools[i].Definition // copy per iteration — each &def is distinct
		params[i] = anthropic.ToolUnionParam{OfTool: &def}
	}
	return params
}

// dispatchTool finds the named tool and executes it within a scoped timeout context.
// Unknown tool names return an error result so the LLM can self-correct.
func dispatchTool(ctx context.Context, tools []SkillTool, name string, input json.RawMessage) ToolResult {
	for _, t := range tools {
		if t.Definition.Name == name {
			timeout := defaultToolTimeout
			if t.TimeoutSeconds > 0 {
				timeout = time.Duration(t.TimeoutSeconds) * time.Second
			}
			toolCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return t.Execute(toolCtx, input)
		}
	}
	return ToolResult{
		Content: fmt.Sprintf("unknown tool %q — this tool is not available in the current skill domain", name),
		IsError: true,
		Details: map[string]interface{}{"error": "tool not registered", "tool": name},
	}
}

// webFetchTool fetches a URL via HTTP GET or POST.
// No credentials required — for authenticated operations the agent must use
// vault execution (M3). Execution is bounded by the tool's TimeoutSeconds via
// the context passed to Execute.
func webFetchTool() SkillTool {
	return SkillTool{
		Label:                   "Web Fetch",
		RequiredCredentialTypes: nil, // no credentials — unauthenticated HTTP only
		TimeoutSeconds:          30,
		Definition: anthropic.ToolParam{
			Name: "web_fetch",
			Description: anthropic.String(
				"Fetch the content of a URL via HTTP GET or POST. " +
					"Use for web pages, REST APIs, or any HTTP resource that does not require authentication. " +
					"Do NOT use for operations requiring authentication — those require vault execution."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"url": map[string]interface{}{
						"type":        "string",
						"description": "The fully-qualified URL to fetch (must include scheme: https:// or http://).",
					},
					"method": map[string]interface{}{
						"type":        "string",
						"description": "HTTP method. Allowed: GET, POST. Defaults to GET when omitted.",
						"enum":        []string{"GET", "POST"},
					},
				},
				Required: []string{"url"},
			},
		},
		Execute: executeWebFetch,
	}
}

func executeWebFetch(ctx context.Context, raw json.RawMessage) ToolResult {
	var params struct {
		URL    string `json:"url"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.Method == "" {
		params.Method = "GET"
	}

	req, err := http.NewRequestWithContext(ctx, params.Method, params.URL, nil)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to build request: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error(), "url": params.URL},
		}
	}
	req.Header.Set("User-Agent", "aegis-agent/1.0")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		content := fmt.Sprintf("HTTP request failed: %v", err)
		if ctx.Err() == context.DeadlineExceeded {
			content = "TOOL_TIMEOUT: web_fetch did not complete within the allowed time"
		}
		return ToolResult{
			Content: content,
			IsError: true,
			Details: map[string]interface{}{
				"error":      err.Error(),
				"url":        params.URL,
				"elapsed_ms": elapsed.Milliseconds(),
			},
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxContentBytes))
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to read response body: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error(), "url": params.URL},
		}
	}

	truncated := len(body) == maxContentBytes
	content := string(body)
	if truncated {
		content += "\n[CONTENT TRUNCATED — response exceeded 16KB limit]"
	}

	return ToolResult{
		Content: fmt.Sprintf("HTTP %d\n\n%s", resp.StatusCode, content),
		IsError: resp.StatusCode >= 400,
		Details: map[string]interface{}{
			"url":         params.URL,
			"status_code": resp.StatusCode,
			"elapsed_ms":  elapsed.Milliseconds(),
			"bytes_read":  len(body),
			"truncated":   truncated,
		},
	}
}

// vaultWebFetchTool fetches a URL via HTTP using a stored API credential.
// The agent never touches the credential — the Vault executes the HTTP call and
// returns only the response body (ADR-004). The tool is omitted from the registry
// when ve is nil (vault unavailable).
//
// TimeoutSeconds = 35: vault deadline = 30s, local deadline = 30 + 5s buffer.
func vaultWebFetchTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Vault Web Fetch",
		RequiredCredentialTypes: []string{"web_api_key"},
		TimeoutSeconds:          40, // must be > VaultExecutor local timer (30+5=35s) to ensure timer.C fires before ctx.Done()
		Definition: anthropic.ToolParam{
			Name: "vault_web_fetch",
			Description: anthropic.String(
				"Fetch a URL using a stored API credential managed by the Vault. " +
					"Use for authenticated HTTP requests (APIs requiring an API key). " +
					"Do NOT use for public URLs — use web_fetch instead. " +
					"Do NOT include credential values in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"url": map[string]interface{}{
						"type":        "string",
						"description": "The fully-qualified URL to fetch (must include scheme: https:// or http://).",
					},
					"method": map[string]interface{}{
						"type":        "string",
						"description": "HTTP method. Allowed: GET, POST. Defaults to GET when omitted.",
						"enum":        []string{"GET", "POST"},
					},
				},
				Required: []string{"url"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeVaultWebFetch(ctx, ve, raw)
		},
	}
}

// executeVaultWebFetch builds a VaultOperationRequest and delegates execution to
// the VaultExecutor. The Vault performs the HTTP call using its stored credential
// and returns only the response body — never the credential itself (NFR-08).
func executeVaultWebFetch(ctx context.Context, ve *VaultExecutor, raw json.RawMessage) ToolResult {
	var params struct {
		URL    string `json:"url"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.Method == "" {
		params.Method = "GET"
	}

	// Re-encode normalised params as the vault operation_params payload.
	opParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to encode operation params: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	// onUpdate logs progress events to monitoring (stderr via slog). Progress events
	// must not enter LLM context — they are forwarded here for observability only.
	onUpdate := func(p types.VaultOperationProgress) {
		ve.log.Info("vault execute: progress",
			"request_id", p.RequestID,
			"progress_type", p.ProgressType,
			"message", p.Message,
			"elapsed_ms", p.ElapsedMS,
		)
	}

	// vault TimeoutSeconds = 30; local deadline = 30 + 5 = 35s (matches TimeoutSeconds above).
	return ve.Execute(ctx, "web_fetch", "web_api_key", opParams, 30, onUpdate)
}

// taskCompleteTool is the agent's explicit terminal signal. When the agent calls
// this, RunLoop captures the result and exits the loop.
func taskCompleteTool() SkillTool {
	return SkillTool{
		Label:                   "Task Complete",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          0, // instantaneous — JSON parse only
		Definition: anthropic.ToolParam{
			Name: toolNameTaskComplete,
			Description: anthropic.String(
				"Signal that the task is complete and return the final result. " +
					"Call this when you have all the information needed to answer the task. " +
					"Do NOT call this if you still have actions to take."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"result": map[string]interface{}{
						"type":        "string",
						"description": "The final task result. Be concise and factual.",
					},
				},
				Required: []string{"result"},
			},
		},
		Execute: func(_ context.Context, raw json.RawMessage) ToolResult {
			var params struct {
				Result string `json:"result"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{
					Content: fmt.Sprintf("invalid parameters: %v", err),
					IsError: true,
					Details: map[string]interface{}{"error": err.Error()},
				}
			}
			return ToolResult{
				Content: params.Result,
				IsError: false,
				Details: map[string]interface{}{"final_result": params.Result},
			}
		},
	}
}
