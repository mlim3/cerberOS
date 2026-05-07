// Package main — memorytools.go provides the three in-loop memory tools that
// agents use to read and write multi-layer memory during task execution.
//
// Tools registered here:
//
//   - memory_update  — append a distilled fact to domain-scoped agent memory.
//     Used when the agent discovers something reusable across future tasks in
//     the same domain (e.g., "this API endpoint returns paginated results").
//
//   - profile_update — append a distilled preference to the user profile.
//     Used when the agent observes a user behaviour pattern worth persisting
//     (e.g., "user prefers bullet-point summaries over prose").
//
//   - memory_search  — full-text search across past session turns.
//     Returns the top-N most relevant excerpts for the current query so the
//     agent can recall specific past results without re-running tools.
//
// All three tools are no-ops when sl is nil (NATS unavailable).
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// memoryUpdateMaxChars is the maximum length of a single memory_update or
// profile_update fact string. Longer inputs are truncated to prevent bloat.
const memoryUpdateMaxChars = 500

// memoryTools returns the memory tool set for the agent's domain and user context.
// sl may be nil — all tools silently succeed without persistence when NATS is absent.
func memoryTools(sl *SessionLog, domain, userContextID string) []SkillTool {
	return []SkillTool{
		memoryUpdateTool(sl, domain),
		profileUpdateTool(sl, userContextID),
		memorySearchTool(sl),
	}
}

// ─── memory_update ───────────────────────────────────────────────────────────

func memoryUpdateTool(sl *SessionLog, domain string) SkillTool {
	return SkillTool{
		Label: "Append fact to domain memory",
		Definition: anthropic.ToolParam{
			Name: "memory_update",
			Description: anthropic.String(
				"Append a distilled, reusable fact to domain-scoped agent memory. " +
					"Use only for observations that will help future agents working in this domain — " +
					"not for task-specific details, raw tool outputs, or user personal data. " +
					"Do NOT call this for every turn; call only when a genuinely novel and reusable " +
					"fact is discovered. Maximum 500 characters."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"fact": map[string]interface{}{
						"type":        "string",
						"description": "A single, distilled, reusable fact about the domain or external systems. Must be ≤500 characters.",
					},
				},
				Required: []string{"fact"},
			},
		},
		Execute: func(_ context.Context, input json.RawMessage) ToolResult {
			var params struct {
				Fact string `json:"fact"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return ToolResult{IsError: true, Content: fmt.Sprintf("memory_update: invalid input: %v", err)}
			}
			if params.Fact == "" {
				return ToolResult{IsError: true, Content: "memory_update: fact must not be empty"}
			}
			fact := truncateChars(params.Fact, memoryUpdateMaxChars)
			if err := sl.PersistAgentMemory(domain, fact); err != nil {
				// Non-fatal: agent memory is best-effort.
				return ToolResult{Content: "memory updated (persisted with warning)"}
			}
			return ToolResult{Content: "memory updated"}
		},
	}
}

// ─── profile_update ──────────────────────────────────────────────────────────

func profileUpdateTool(sl *SessionLog, userContextID string) SkillTool {
	return SkillTool{
		Label: "Append preference to user profile",
		Definition: anthropic.ToolParam{
			Name: "profile_update",
			Description: anthropic.String(
				"Append a distilled user preference observation to the user profile. " +
					"Use only for clear, durable patterns about how the user likes to work " +
					"(e.g. 'user prefers concise summaries', 'user always wants code in Go'). " +
					"Do NOT call this for task content, one-off requests, or speculative inferences. " +
					"Do NOT include personal identifying information. Maximum 500 characters."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"observation": map[string]interface{}{
						"type":        "string",
						"description": "A single, durable user preference observation. Must be ≤500 characters.",
					},
				},
				Required: []string{"observation"},
			},
		},
		Execute: func(_ context.Context, input json.RawMessage) ToolResult {
			var params struct {
				Observation string `json:"observation"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return ToolResult{IsError: true, Content: fmt.Sprintf("profile_update: invalid input: %v", err)}
			}
			if params.Observation == "" {
				return ToolResult{IsError: true, Content: "profile_update: observation must not be empty"}
			}
			obs := truncateChars(params.Observation, memoryUpdateMaxChars)
			if err := sl.PersistUserProfile(userContextID, obs); err != nil {
				return ToolResult{Content: "profile updated (persisted with warning)"}
			}
			return ToolResult{Content: "profile updated"}
		},
	}
}

// ─── memory_search ───────────────────────────────────────────────────────────

func memorySearchTool(sl *SessionLog) SkillTool {
	return SkillTool{
		Label: "Search past session history",
		Definition: anthropic.ToolParam{
			Name: "memory_search",
			Description: anthropic.String(
				"Full-text search across past session turns to recall specific prior results. " +
					"Use when you need to reference something from an earlier step without re-running tools. " +
					"Do NOT use as a substitute for reading fresh data from external systems. " +
					"Returns up to 3 relevant excerpts by default; set max_results to override (max 10)."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "The search query. Plain language or keywords — full-text search is applied.",
					},
					"max_results": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results to return. Default 3, max 10.",
						"minimum":     1,
						"maximum":     10,
					},
				},
				Required: []string{"query"},
			},
		},
		Execute: func(_ context.Context, input json.RawMessage) ToolResult {
			var params struct {
				Query      string `json:"query"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return ToolResult{IsError: true, Content: fmt.Sprintf("memory_search: invalid input: %v", err)}
			}
			if params.Query == "" {
				return ToolResult{IsError: true, Content: "memory_search: query must not be empty"}
			}
			if params.MaxResults <= 0 {
				params.MaxResults = 3
			}
			if params.MaxResults > 10 {
				params.MaxResults = 10
			}
			result := sl.SearchSessions(params.Query, params.MaxResults)
			return ToolResult{Content: result}
		},
	}
}

// truncateChars returns s truncated to maxChars rune positions, with "..."
// appended when truncation occurs.
func truncateChars(s string, maxChars int) string {
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return string(runes[:maxChars-3]) + "..."
}
