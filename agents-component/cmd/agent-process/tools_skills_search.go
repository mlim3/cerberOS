// Package main — tools_skills_search.go implements the skills_search tool.
//
// skills_search performs semantic similarity search via the Memory Component's
// pgvector index and returns the top-K matches. It is always included in the tool
// registry regardless of domain, enabling cross-domain capability discovery
// during the ReAct loop without requiring a re-spawn (EDD §13.5).
//
// Cross-domain routing behavior
// ──────────────────────────────
//
// Credential-free, out-of-domain skill discovered (Origin="static", RequiresCred=false):
//   The tool auto-registers the skill into DynamicRegistry using the builtinRegistry
//   factory so the agent can call it directly without spawn_agent. A safety check
//   (tool.RequiredCredentialTypes empty) is performed after factory construction to
//   guard against incorrect metadata.
//
// Credentialed, out-of-domain skill discovered (RequiresCred=true):
//   The tool sends a ClarificationRequest via ClarificationSender and returns a
//   "waiting for user approval" sentinel. When the response arrives:
//   - Approved: recommend spawn_agent for the approved domain.
//   - Denied: return a user-facing explanation so the agent can find an alternative.
//
// Synthesized skills (Origin="synthesized") are never auto-registered — they use
// LLM-recipe execution and have no reliable builtinRegistry entry.
//
// When registry or cs is nil, the tool falls back to the legacy spawn_agent
// recommendation to preserve behaviour on nil injection (e.g. early tests).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/logfields"
	"github.com/cerberOS/agents-component/pkg/types"
)

// SkillSearcher is implemented by *SessionLog in production. Tests may inject
// a stub that returns canned results without a live NATS connection.
type SkillSearcher interface {
	SearchSkills(query string, topK int) []types.SkillSearchResult
}

// ClarificationSender is implemented by *SessionLog in production. It publishes
// a clarification.request to the Orchestrator and blocks until the user responds
// or the request times out. Tests may inject a stub for synchronous testing.
type ClarificationSender interface {
	SendClarification(req types.ClarificationRequest) (types.ClarificationResponse, error)
}

// skillsSearchTool returns a SkillTool that searches the skill index semantically.
//
// sr is the SkillSearcher (typically *SessionLog). Passing nil returns an
// "unavailable" error when invoked.
//
// registry, when non-nil, enables credential-free tool auto-registration.
// cs, when non-nil, enables credentialed-tool user-gating via clarification.
// Passing nil for either disables the corresponding feature; the tool falls
// back to the legacy spawn_agent recommendation.
func skillsSearchTool(sr SkillSearcher, currentDomain string, spawnAvailable bool, registry *DynamicRegistry, cs ClarificationSender) SkillTool {
	return SkillTool{
		Label:                   "Skills Search",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          5,
		Definition: anthropic.ToolParam{
			Name: "skills_search",
			Description: anthropic.String(
				"Search for available skill commands across all domains using a natural-language query. " +
					"Returns the top matching commands with their domain and description. " +
					"Use when you need a capability not in your current domain or are unsure which tool to use. " +
					"Do NOT use when you already know the exact tool name."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Natural-language description of the capability you need (e.g. \"fetch a URL with an API key\").",
					},
					"top_k": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results to return. Defaults to 3 when omitted.",
					},
				},
				Required: []string{"query"},
			},
		},
		Execute: func(_ context.Context, raw json.RawMessage) ToolResult {
			return executeSkillsSearch(sr, currentDomain, spawnAvailable, registry, cs, raw)
		},
	}
}

func executeSkillsSearch(sr SkillSearcher, currentDomain string, spawnAvailable bool, registry *DynamicRegistry, cs ClarificationSender, raw json.RawMessage) ToolResult {
	log := slog.Default()
	var params struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		log.Warn("skills_search: invalid parameters",
			"current_domain", currentDomain,
			"error", err,
		)
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.Query == "" {
		log.Warn("skills_search: empty query",
			"current_domain", currentDomain,
		)
		return ToolResult{
			Content: "query must not be empty",
			IsError: true,
			Details: map[string]interface{}{"error": "empty query"},
		}
	}
	if params.TopK <= 0 {
		params.TopK = 3
	}

	log.Info("skills_search: executing semantic search",
		"current_domain", currentDomain,
		"top_k", params.TopK,
		"query_preview", logfields.PreviewWords(params.Query, 20, 180),
	)

	if sr == nil {
		log.Warn("skills_search: semantic search unavailable (NATS not connected)",
			"current_domain", currentDomain,
		)
		return ToolResult{
			Content: "skills semantic search is unavailable in this environment",
			IsError: true,
			Details: map[string]interface{}{"error": "SkillSearcher is nil"},
		}
	}

	results := sr.SearchSkills(params.Query, params.TopK)
	if len(results) == 0 {
		log.Info("skills_search: no matching skills found",
			"current_domain", currentDomain,
			"query_preview", logfields.PreviewWords(params.Query, 20, 180),
		)
		return ToolResult{
			Content: "No matching skills found for that query.",
			Details: map[string]interface{}{"query": params.Query, "result_count": 0},
		}
	}

	top := results[0]
	log.Info("skills_search: results ready",
		"current_domain", currentDomain,
		"query_preview", logfields.PreviewWords(params.Query, 20, 180),
		"result_count", len(results),
		"top_domain", top.Domain,
		"top_command", top.Name,
		"top_description", logfields.PreviewWords(top.Description, 24, 200),
		"top_requires_cred", top.RequiresCred,
		"top_origin", top.Origin,
	)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d matching skill(s):\n\n", len(results))
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. %s.%s\n   %s\n\n", i+1, r.Domain, r.Name, r.Description)
	}
	details := map[string]interface{}{
		"query":          params.Query,
		"result_count":   len(results),
		"current_domain": currentDomain,
		"top_domain":     top.Domain,
		"top_command":    top.Name,
	}

	// ── Same-domain: always call directly ──────────────────────────────────────
	if top.Domain == currentDomain {
		log.Info("skills_search: top result stays within current domain; agent can use it directly",
			"current_domain", currentDomain,
			"top_command", top.Name,
		)
		sb.WriteString("Use the tool name directly in your next action if it matches what you need.")
		return ToolResult{Content: sb.String(), Details: details}
	}

	// ── Cross-domain: credential-free + static → auto-register ────────────────
	if !top.RequiresCred && top.Origin == "static" && top.Implementation != "" && registry != nil {
		tool, registered := tryAutoRegister(top, registry, log)
		if registered {
			log.Info("skills_search: credential-free cross-domain tool auto-registered",
				"current_domain", currentDomain,
				"tool_name", top.Name,
				"implementation", top.Implementation,
			)
			details["recommended_action"] = "call_directly"
			details["auto_registered"] = true
			fmt.Fprintf(&sb, "Tool %q has been registered in your current session. Call it directly with its parameters.\n", tool.Definition.Name)
			return ToolResult{Content: sb.String(), Details: details}
		}
		// Registration failed (unknown impl or vault tool) — fall through to spawn logic.
		log.Warn("skills_search: auto-registration failed; falling back to spawn logic",
			"tool_name", top.Name,
			"implementation", top.Implementation,
		)
	}

	// ── Cross-domain: credentialed → user clarification ────────────────────────
	if top.RequiresCred && cs != nil {
		return handleCredentialedCrossDomain(top, params.Query, currentDomain, cs, &sb, details, log)
	}

	// ── Fallback: synthesized skill or no registry/cs → legacy spawn_agent ─────
	if !spawnAvailable {
		log.Info("skills_search: top result outside current domain but no spawn available; agent must continue without handoff",
			"current_domain", currentDomain,
			"top_domain", top.Domain,
			"top_command", top.Name,
		)
		sb.WriteString("Use the tool name directly in your next action if it matches what you need.")
		return ToolResult{Content: sb.String(), Details: details}
	}

	spawnInstructions := fmt.Sprintf(
		"Handle this request using the %s skill domain: %s\n\nPrefer the %s.%s capability if it fits. Return only the final result.",
		top.Domain,
		params.Query,
		top.Domain,
		top.Name,
	)
	log.Info("skills_search: top result outside current domain; recommending spawn_agent handoff",
		"current_domain", currentDomain,
		"query_preview", logfields.PreviewWords(params.Query, 20, 180),
		"delegated_domain", top.Domain,
		"delegated_command", top.Name,
	)
	fmt.Fprintf(&sb, "Top result is outside the current %q domain. Call spawn_agent next with required_skills=[%q]. Suggested instructions: %q\n", currentDomain, top.Domain, spawnInstructions)
	details["recommended_action"] = "spawn_agent"
	details["spawn_required_skills"] = []string{top.Domain}
	details["spawn_instructions"] = spawnInstructions

	return ToolResult{Content: sb.String(), Details: details}
}

// tryAutoRegister attempts to register a credential-free static tool from
// builtinRegistry into the DynamicRegistry. Returns the registered tool and
// true on success. Returns the zero SkillTool and false on any failure (unknown
// implementation, factory produces a tool that requires credentials, or
// registry.Register returns a duplicate error).
//
// The safety check (RequiredCredentialTypes empty after factory construction)
// guards against incorrect RequiresCred metadata in stored payloads: even if
// a skill is mistakenly stored as requires_cred=false, a vault tool factory
// produces a tool with RequiredCredentialTypes set, which we catch here.
func tryAutoRegister(r types.SkillSearchResult, registry *DynamicRegistry, log *slog.Logger) (SkillTool, bool) {
	factory, ok := builtinRegistry[r.Implementation]
	if !ok {
		log.Warn("skills_search: auto-register: implementation not in builtinRegistry",
			"tool_name", r.Name,
			"implementation", r.Implementation,
		)
		return SkillTool{}, false
	}

	// Construct with nil VaultExecutor — credential-free tools must not panic on nil.
	tool := factory(nil)

	// Authoritative safety check: tool must not declare any credential requirements.
	if len(tool.RequiredCredentialTypes) > 0 {
		log.Warn("skills_search: auto-register: factory produced a vault tool despite requires_cred=false; skipping",
			"tool_name", r.Name,
			"implementation", r.Implementation,
			"credential_types", tool.RequiredCredentialTypes,
		)
		return SkillTool{}, false
	}

	if err := registry.Register(tool); err != nil {
		// Duplicate — already registered (e.g. from prior skills_search call).
		// This is not an error from the caller's perspective.
		log.Info("skills_search: auto-register: tool already in registry",
			"tool_name", r.Name,
			"error", err,
		)
		return tool, true
	}

	return tool, true
}

// handleCredentialedCrossDomain sends a ClarificationRequest to the user and
// returns the appropriate ToolResult based on the response.
// sb is passed by pointer to avoid the "Builder copied by value" panic that
// occurs when strings.Builder is copied after data has been written to it.
func handleCredentialedCrossDomain(
	top types.SkillSearchResult,
	query string,
	currentDomain string,
	cs ClarificationSender,
	sb *strings.Builder,
	details map[string]interface{},
	log *slog.Logger,
) ToolResult {
	req := types.ClarificationRequest{
		RequestID:   newUUID(),
		SkillName:   top.Name,
		SkillDomain: top.Domain,
		Question: fmt.Sprintf(
			"The task requires the %q skill in the %q domain, which is outside your current authorized scope (%s). "+
				"Do you want to allow access to this skill?",
			top.Name, top.Domain, currentDomain,
		),
		Reason: fmt.Sprintf(
			"The best matching tool for the requested capability (%q) is %s.%s, "+
				"which requires credentials outside the current agent's scope.",
			query, top.Domain, top.Name,
		),
	}

	log.Info("skills_search: credentialed cross-domain skill discovered; sending clarification request",
		"skill_name", top.Name,
		"skill_domain", top.Domain,
		"current_domain", currentDomain,
		"request_id", req.RequestID,
	)

	resp, err := cs.SendClarification(req)
	if err != nil {
		log.Warn("skills_search: clarification request failed",
			"request_id", req.RequestID,
			"error", err,
		)
		// Treat failure to get a response as implicit denial — do not proceed.
		sb.WriteString(fmt.Sprintf(
			"Could not obtain user approval to use %s.%s (clarification request failed: %v). "+
				"Please find an alternative approach or inform the user this capability is unavailable.",
			top.Domain, top.Name, err,
		))
		details["clarification_error"] = err.Error()
		return ToolResult{Content: sb.String(), Details: details}
	}

	if resp.Approved {
		log.Info("skills_search: user approved credentialed cross-domain access; recommending spawn_agent",
			"skill_domain", top.Domain,
			"approved", true,
		)
		spawnInstructions := fmt.Sprintf(
			"Handle this request using the %s skill domain: %s\n\nPrefer the %s.%s capability if it fits. Return only the final result.",
			top.Domain, query, top.Domain, top.Name,
		)
		fmt.Fprintf(sb,
			"User approved access to %s.%s. Call spawn_agent next with required_skills=[%q]. Suggested instructions: %q\n",
			top.Domain, top.Name, top.Domain, spawnInstructions,
		)
		details["approved"] = true
		details["recommended_action"] = "spawn_agent"
		details["spawn_required_skills"] = []string{top.Domain}
		details["spawn_instructions"] = spawnInstructions
		return ToolResult{Content: sb.String(), Details: details}
	}

	// Denied.
	log.Info("skills_search: user denied credentialed cross-domain access",
		"skill_domain", top.Domain,
		"user_message", resp.UserMessage,
	)
	denial := fmt.Sprintf(
		"The user has not authorized access to %s.%s.", top.Domain, top.Name,
	)
	if resp.UserMessage != "" {
		denial += " User note: " + resp.UserMessage
	}
	denial += " Please find an alternative approach or explain to the user why the task cannot be completed."
	sb.WriteString(denial)
	details["approved"] = false
	details["denied"] = true
	return ToolResult{Content: sb.String(), Details: details}
}
