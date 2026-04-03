// Package main — specbudget_tools.go integrates the SpecBudget with the
// agent-process tool registry.
//
// applySpecBudget loads each tool's definition into the budget at startup and
// returns a filtered tool slice with evicted tools removed. Pinned tools
// (task_complete, spawn_agent) are never loaded into the budget — they must
// always be available to the agent regardless of context pressure.
//
// estimateToolTokens approximates the LLM context token cost of a single tool
// definition by marshalling its InputSchema.Properties to JSON and dividing
// by 4 (a conservative ~4 bytes/token estimate for English technical text).
package main

import (
	"encoding/json"
	"log/slog"

	"github.com/cerberOS/agents-component/internal/skills"
)

// pinnedTools are never registered in the SpecBudget and are never evicted,
// because the ReAct loop requires them to be available at every iteration.
var pinnedTools = map[string]bool{
	toolNameTaskComplete: true,
	"spawn_agent":        true,
}

// applySpecBudget registers non-pinned tools into the budget and returns a
// filtered slice with evicted tools removed. If budget is nil the original
// slice is returned unchanged.
//
// Tools whose spec cost alone exceeds the budget ceiling are excluded from the
// returned slice (they can never fit). Pinned tools are always retained.
func applySpecBudget(budget *skills.SpecBudget, domain string, tools []SkillTool, log *slog.Logger) []SkillTool {
	if budget == nil {
		return tools
	}

	evictedNames := make(map[string]bool)

	for _, tool := range tools {
		if pinnedTools[tool.Definition.Name] {
			continue // Pinned — never registered in the budget.
		}
		tokens := estimateToolTokens(tool)
		evictions, err := budget.LoadSpec(domain, tool.Definition.Name, tokens)
		if err != nil {
			// Single spec exceeds the ceiling — permanently exclude it.
			log.Warn("specbudget: tool spec exceeds ceiling, excluding from active tools",
				"tool", tool.Definition.Name,
				"tokens", tokens,
				"ceiling", budget.Ceiling(),
			)
			evictedNames[tool.Definition.Name] = true
			continue
		}
		for _, e := range evictions {
			if !pinnedTools[e.Command] {
				evictedNames[e.Command] = true
				log.Info("specbudget: evicted LRU spec to make room",
					"evicted_tool", e.Command,
					"evicted_tokens", e.Tokens,
					"remaining_budget", budget.Remaining(),
				)
			}
		}
	}

	if len(evictedNames) == 0 {
		return tools
	}

	filtered := make([]SkillTool, 0, len(tools)-len(evictedNames))
	for _, t := range tools {
		if !evictedNames[t.Definition.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// estimateToolTokens approximates the token cost of a tool definition in the
// LLM context. It serialises the tool's InputSchema.Properties to JSON and
// divides by 4 (conservative bytes-per-token estimate). Returns 1 minimum.
func estimateToolTokens(tool SkillTool) int {
	data, err := json.Marshal(tool.Definition.InputSchema.Properties)
	if err != nil || len(data) == 0 {
		return 1
	}
	if cost := len(data) / 4; cost >= 1 {
		return cost
	}
	return 1
}
