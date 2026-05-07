// Package main — tools_skills_search.go implements the skills_search tool.
//
// skills_search performs semantic similarity search across all registered skill
// commands and returns the top-K matches. It is always included in the tool
// registry regardless of domain, enabling cross-domain capability discovery
// during the ReAct loop without requiring a re-spawn (EDD §13.5).
//
// A skills.Manager is lazily initialised once per process from the static domain
// metadata defined in agentProcessDomainNodes. The manager is search-only: it
// holds names and descriptions for embedding, not executable tool logic.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/logfields"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/internal/skillsconfig"
)

var (
	skillsMgrOnce sync.Once
	skillsMgr     skills.Manager
)

// getSkillsManager returns the process-wide skills.Manager, initialising it on
// first call. Safe for concurrent use.
func getSkillsManager() skills.Manager {
	skillsMgrOnce.Do(func() {
		mgr := skills.New()
		cfg := loadSkillsConfig()
		if cfg == nil {
			cfg = &skillsconfig.Config{}
		}
		for _, domain := range cfg.ToSkillNodes() {
			_ = mgr.RegisterDomain(domain) // validation errors mean a bug in this file
		}
		skillsMgr = mgr
	})
	return skillsMgr
}

// skillsSearchTool returns a SkillTool that searches the skill index semantically.
// mgr is the skills.Manager holding the pre-computed embedding index.
func skillsSearchTool(mgr skills.Manager, currentDomain string, spawnAvailable bool) SkillTool {
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
			return executeSkillsSearch(mgr, currentDomain, spawnAvailable, raw)
		},
	}
}

func executeSkillsSearch(mgr skills.Manager, currentDomain string, spawnAvailable bool, raw json.RawMessage) ToolResult {
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

	results, err := mgr.Search(params.Query, params.TopK)
	if err != nil {
		log.Error("skills_search: search failed",
			"current_domain", currentDomain,
			"query_preview", logfields.PreviewWords(params.Query, 20, 180),
			"error", err,
		)
		return ToolResult{
			Content: fmt.Sprintf("skills search failed: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

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
	if top.Domain != "" && top.Domain != currentDomain && spawnAvailable {
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
	} else {
		if top.Domain == currentDomain {
			log.Info("skills_search: top result stays within current domain; agent can use it directly",
				"current_domain", currentDomain,
				"top_command", top.Name,
			)
		} else {
			log.Info("skills_search: top result outside current domain but no spawn available; agent must continue without handoff",
				"current_domain", currentDomain,
				"top_domain", top.Domain,
				"top_command", top.Name,
			)
		}
		sb.WriteString("Use the tool name directly in your next action if it matches what you need.")
	}

	return ToolResult{
		Content: sb.String(),
		Details: details,
	}
}
