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
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
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
		for _, domain := range agentProcessDomainNodes() {
			_ = mgr.RegisterDomain(domain) // validation errors mean a bug in this file
		}
		skillsMgr = mgr
	})
	return skillsMgr
}

// agentProcessDomainNodes returns metadata-only SkillNode trees for every domain
// known to this binary. These mirror the tool definitions in tools.go but contain
// only the fields needed to populate the search index (name, label, description).
// Keep this in sync with toolsForDomain when adding new tools.
func agentProcessDomainNodes() []*types.SkillNode {
	return []*types.SkillNode{
		{
			Name:  "web",
			Level: "domain",
			Children: map[string]*types.SkillNode{
				"web_fetch": {
					Name:           "web_fetch",
					Level:          "command",
					Label:          "Web Fetch",
					Description:    "Fetch the content of a URL via HTTP GET or POST. Use for public web pages and unauthenticated REST APIs. Do NOT use for operations requiring authentication — use vault_web_fetch instead.",
					TimeoutSeconds: 30,
				},
				"vault_web_fetch": {
					Name:                    "vault_web_fetch",
					Level:                   "command",
					Label:                   "Vault Web Fetch",
					RequiredCredentialTypes: []string{"web_api_key"},
					Description:             "Fetch a URL using a stored API credential via the Vault. Use for authenticated HTTP requests requiring an API key. Do NOT use for public URLs — use web_fetch instead.",
					TimeoutSeconds:          35,
				},
			},
		},
	}
}

// skillsSearchTool returns a SkillTool that searches the skill index semantically.
// mgr is the skills.Manager holding the pre-computed embedding index.
func skillsSearchTool(mgr skills.Manager) SkillTool {
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
			return executeSkillsSearch(mgr, raw)
		},
	}
}

func executeSkillsSearch(mgr skills.Manager, raw json.RawMessage) ToolResult {
	var params struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.Query == "" {
		return ToolResult{
			Content: "query must not be empty",
			IsError: true,
			Details: map[string]interface{}{"error": "empty query"},
		}
	}

	results, err := mgr.Search(params.Query, params.TopK)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("skills search failed: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	if len(results) == 0 {
		return ToolResult{
			Content: "No matching skills found for that query.",
			Details: map[string]interface{}{"query": params.Query, "result_count": 0},
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d matching skill(s):\n\n", len(results))
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. %s.%s\n   %s\n\n", i+1, r.Domain, r.Name, r.Description)
	}
	sb.WriteString("Use the tool name directly in your next action if it matches what you need.")

	return ToolResult{
		Content: sb.String(),
		Details: map[string]interface{}{
			"query":        params.Query,
			"result_count": len(results),
		},
	}
}
