// Package main — synthesizedexec.go builds dynamic SkillTool entries for skills
// that were created by post-task synthesis in a prior session.
//
// Each synthesized skill has a Recipe — a step-by-step procedure with
// {{param_name}} placeholders extracted at synthesis time. At invocation time,
// buildSynthesizedTools returns a SkillTool whose Execute function:
//
//  1. Substitutes the caller's concrete parameter values into the recipe.
//  2. Makes a targeted Haiku LLM call: "Execute this procedure: <recipe>".
//  3. Returns the LLM response as a ToolResult.
//
// This keeps synthesized skill execution self-contained inside the agent process
// with no extra NATS round-trips or new Go code required at runtime.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/pkg/types"
)

const (
	// synthesizedExecMaxTokens caps the inline LLM call for synthesized skill
	// execution. 4096 is enough for typical procedural results while keeping
	// latency and cost bounded.
	synthesizedExecMaxTokens = 4096
)

// buildSynthesizedTools returns a SkillTool for each SynthesizedSkillRecord.
// Tools whose name already exists in existingNames are skipped to avoid
// shadowing a builtin that happens to share a name.
//
// Each returned SkillTool has Synthesized = true so EmitSkillInvocation can
// set synthesized=true in the audit event, triggering the "learned" UI toast.
func buildSynthesizedTools(
	client *anthropic.Client,
	records []types.SynthesizedSkillRecord,
	existingNames map[string]bool,
) []SkillTool {
	tools := make([]SkillTool, 0, len(records))
	for _, r := range records {
		if existingNames[r.Name] {
			continue // builtin takes precedence
		}
		rec := r // capture loop variable
		tools = append(tools, SkillTool{
			Label:       rec.Name,
			Synthesized: true,
			Definition: anthropic.ToolParam{
				Name:        rec.Name,
				Description: anthropic.String(rec.Description),
				InputSchema: specToInputSchema(rec.Spec),
			},
			Execute: makeRecipeExecutor(client, rec),
		})
	}
	return tools
}

// specToInputSchema converts a SkillSpec into an Anthropic ToolInputSchemaParam.
// When spec is nil an empty schema (no parameters) is returned.
func specToInputSchema(spec *types.SkillSpec) anthropic.ToolInputSchemaParam {
	props := make(map[string]interface{})
	var required []string
	if spec != nil {
		for name, param := range spec.Parameters {
			props[name] = map[string]interface{}{
				"type":        param.Type,
				"description": param.Description,
			}
			if param.Required {
				required = append(required, name)
			}
		}
	}
	return anthropic.ToolInputSchemaParam{
		Properties: props,
		Required:   required,
	}
}

// makeRecipeExecutor returns the Execute function for a synthesized SkillTool.
// It closes over client and record so each synthesized tool gets its own
// executor bound to the correct recipe and parameter spec.
func makeRecipeExecutor(client *anthropic.Client, record types.SynthesizedSkillRecord) func(ctx context.Context, input json.RawMessage) ToolResult {
	return func(ctx context.Context, input json.RawMessage) ToolResult {
		// Parse the caller's parameter values.
		var params map[string]interface{}
		if err := json.Unmarshal(input, &params); err != nil {
			return ToolResult{
				Content: fmt.Sprintf("synthesized skill %q: invalid input: %v", record.Name, err),
				IsError: true,
			}
		}

		procedure := substituteRecipe(record.Recipe, params)
		if procedure == "" {
			return ToolResult{
				Content: fmt.Sprintf("synthesized skill %q has no execution recipe", record.Name),
				IsError: true,
			}
		}

		resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.ModelClaudeHaiku4_5,
			MaxTokens: synthesizedExecMaxTokens,
			System: []anthropic.TextBlockParam{{
				Text: fmt.Sprintf(
					"You are executing a learned procedure for an AI agent. " +
						"Carry out each step exactly as described and return the result. " +
						"Be concise and factual. Do not explain your reasoning — just perform the steps and return the output."),
			}},
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock(
					"Execute this procedure:\n\n" + procedure,
				)),
			},
		})
		if err != nil {
			return ToolResult{
				Content: fmt.Sprintf("synthesized skill %q: execution failed: %v", record.Name, err),
				IsError: true,
			}
		}
		if len(resp.Content) == 0 || resp.Content[0].Text == "" {
			return ToolResult{
				Content: fmt.Sprintf("synthesized skill %q: empty response from LLM", record.Name),
				IsError: true,
			}
		}
		return ToolResult{Content: resp.Content[0].Text}
	}
}

// substituteRecipe replaces {{param_name}} placeholders in the recipe template
// with the corresponding values from params. Values are formatted as strings;
// complex types (objects, arrays) are JSON-encoded.
func substituteRecipe(recipe string, params map[string]interface{}) string {
	result := recipe
	for key, val := range params {
		placeholder := "{{" + key + "}}"
		var strVal string
		switch v := val.(type) {
		case string:
			strVal = v
		case float64:
			strVal = fmt.Sprintf("%g", v)
		case bool:
			if v {
				strVal = "true"
			} else {
				strVal = "false"
			}
		default:
			b, err := json.Marshal(v)
			if err != nil {
				strVal = fmt.Sprintf("%v", v)
			} else {
				strVal = string(b)
			}
		}
		result = strings.ReplaceAll(result, placeholder, strVal)
	}
	return result
}
