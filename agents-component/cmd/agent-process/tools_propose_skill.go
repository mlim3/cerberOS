// Package main — tools_propose_skill.go implements the propose_skill tool.
//
// propose_skill lets the agent persist a newly composed skill to the Memory
// Component's skill_cache so it is available in future sessions without
// requiring another skills_search discovery pass.
//
// The tool validates the supplied parameters against the Tool Contract
// (skills.ValidateCommandContract) before persisting, so the skill is
// guaranteed to be loadable at the next agent spawn.
//
// The skill is scoped to the requesting user ("user" scope) by default.
// Global skills that should be available to all users require operator
// promotion and are out of scope for this tool.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

// proposeSkillTool returns a SkillTool that persists a new synthesized skill
// into the user's skill_cache via the session log.
//
// sl may be nil (NATS unavailable); the tool will return a graceful error.
// userContextID is propagated from SpawnContext so skills are scoped to the
// correct user at hydration time.
func proposeSkillTool(sl *SessionLog, userContextID string) SkillTool {
	return SkillTool{
		Label:                   "Propose Skill",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          15,
		Definition: anthropic.ToolParam{
			Name: "propose_skill",
			Description: anthropic.String(
				"Persist a reusable skill into your skill cache so it is available in future sessions " +
					"without needing skills_search to rediscover it. " +
					"Use this after successfully completing a novel task that required multiple steps or " +
					"external knowledge — distil the solution into a named, parameterised skill so future " +
					"agents can call it directly. " +
					"Do NOT use for trivial one-step tasks, for skills that already exist in the registry, " +
					"or for tasks whose steps change every time. " +
					"The skill is scoped to the current user and loaded at future agent spawns.",
			),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Snake_case tool name, max 64 characters (e.g. \"summarise_github_pr\"). Must be unique within the domain.",
					},
					"domain": map[string]interface{}{
						"type":        "string",
						"description": "The skill domain this tool belongs to (e.g. \"web\", \"data\", \"general\"). Must be an existing domain.",
					},
					"label": map[string]interface{}{
						"type":        "string",
						"description": "Short human-readable display name for monitoring dashboards (e.g. \"Summarise GitHub PR\"). Never shown to the LLM.",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "What the skill does and when NOT to use it. Max 300 characters. Include negative guidance to prevent misuse.",
					},
					"recipe": map[string]interface{}{
						"type":        "string",
						"description": "Step-by-step procedure the agent will follow when this skill is invoked, with {{param_name}} placeholders for each parameter. Be concrete and complete.",
					},
					"parameters": map[string]interface{}{
						"type":        "object",
						"description": "JSON object where each key is a parameter name and each value is an object with: type (string), required (bool), description (string). All parameters must have descriptions.",
						"additionalProperties": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"type":        map[string]interface{}{"type": "string"},
								"required":    map[string]interface{}{"type": "boolean"},
								"description": map[string]interface{}{"type": "string"},
							},
							"required": []string{"type", "description"},
						},
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "How long the skill is allowed to run before timing out. Default 30. Max 300.",
					},
				},
				Required: []string{"name", "domain", "label", "description", "recipe"},
			},
		},
		Execute: func(_ context.Context, raw json.RawMessage) ToolResult {
			return executeProposeSkill(sl, userContextID, raw)
		},
	}
}

func executeProposeSkill(sl *SessionLog, userContextID string, raw json.RawMessage) ToolResult {
	var params struct {
		Name           string                     `json:"name"`
		Domain         string                     `json:"domain"`
		Label          string                     `json:"label"`
		Description    string                     `json:"description"`
		Recipe         string                     `json:"recipe"`
		Parameters     map[string]types.ParameterDef `json:"parameters"`
		TimeoutSeconds int                        `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("propose_skill: invalid parameters: %v", err),
			IsError: true,
		}
	}

	// Validate required fields.
	if params.Name == "" {
		return ToolResult{Content: "propose_skill: name is required", IsError: true}
	}
	if params.Domain == "" {
		return ToolResult{Content: "propose_skill: domain is required", IsError: true}
	}
	if params.Label == "" {
		return ToolResult{Content: "propose_skill: label is required", IsError: true}
	}
	if params.Description == "" {
		return ToolResult{Content: "propose_skill: description is required", IsError: true}
	}
	if params.Recipe == "" {
		return ToolResult{Content: "propose_skill: recipe is required", IsError: true}
	}

	// Build the SkillNode.
	node := &types.SkillNode{
		Name:           params.Name,
		Level:          "command",
		Label:          params.Label,
		Description:    params.Description,
		Recipe:         params.Recipe,
		Origin:         "synthesized",
		TimeoutSeconds: params.TimeoutSeconds,
	}
	if len(params.Parameters) > 0 {
		node.Spec = &types.SkillSpec{
			Parameters: params.Parameters,
		}
	}

	// Validate against the Tool Contract before persisting.
	if err := skills.ValidateCommandContract(node); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("propose_skill: tool contract violation — %v", err),
			IsError: true,
		}
	}

	// Persist via NATS → Orchestrator → Memory Component (skill_cache).
	if err := sl.PersistSkillWithScope(params.Domain, node, userContextID, "user"); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("propose_skill: failed to persist skill: %v", err),
			IsError: true,
		}
	}

	return ToolResult{
		Content: fmt.Sprintf(
			"Skill %q persisted successfully in domain %q. "+
				"It will be available via skills_search in future sessions.",
			params.Name, params.Domain,
		),
		Details: map[string]interface{}{
			"skill_name": params.Name,
			"domain":     params.Domain,
			"scope":      "user",
		},
	}
}
