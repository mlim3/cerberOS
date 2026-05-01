// Package main — skillsynthesis.go implements post-task autonomous skill creation.
//
// After a task completes with skillSynthesisThreshold or more tool calls, the agent
// makes an internal LLM call to extract a reusable skill definition from the session
// history. The synthesized SkillNode is validated against the Tool Contract (EDD §13.2)
// and persisted as data_type "skill_cache" via the session log.
//
// Synthesis is best-effort: a failed call is logged and discarded — the task result
// is returned regardless. Synthesized skills are rehydrated into the Skill Hierarchy
// Manager by the Agent Factory at component startup (Factory.LoadSynthesizedSkills).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

const (
	// skillSynthesisThreshold is the minimum number of tool calls dispatched
	// during a task for synthesis to be attempted. Plan Executor subtasks
	// return via end_turn (no task_complete call), so threshold=1 means
	// "at least 1 domain tool call". Tasks in the "general" domain are still
	// excluded by shouldSynthesize regardless of count.
	skillSynthesisThreshold = 1

	// skillSynthesisMaxTokens caps the LLM output for the synthesis call.
	// 768 tokens accommodates the extra recipe field (step-by-step procedure
	// with parameter placeholders) on top of the core metadata fields.
	skillSynthesisMaxTokens = 768
)

// synthesizedSkillJSON is the JSON shape requested from the synthesis LLM call.
// Fields map directly to command-level SkillNode properties (EDD §13.2).
// Recipe is a step-by-step execution procedure with {{param_name}} placeholders;
// it is used at invocation time to drive an inline LLM execution call.
type synthesizedSkillJSON struct {
	Name        string           `json:"name"`
	Label       string           `json:"label"`
	Description string           `json:"description"`
	Recipe      string           `json:"recipe"`
	Spec        *types.SkillSpec `json:"spec,omitempty"`
}

// shouldSynthesize reports whether the completed task warrants skill synthesis.
// The "general" domain has no stable tool surface to capture; tasks with too
// few tool calls are too simple to produce a useful reusable procedure.
func shouldSynthesize(domain string, toolCallCount int) bool {
	return domain != "general" && toolCallCount >= skillSynthesisThreshold
}

// synthesizeSkill issues an internal LLM call to extract a reusable skill
// definition from the completed task's session history.
//
// Returns (nil, nil) when the LLM determines the task has no novel procedure
// worth capturing (signalled by an empty "name" in the JSON response).
//
// Returns (nil, error) on LLM failure or Tool Contract violation. The caller
// should log and continue; synthesis failure must never fail the task.
func synthesizeSkill(
	ctx context.Context,
	client *anthropic.Client,
	log *slog.Logger,
	domain string,
	history []anthropic.MessageParam,
) (*types.SkillNode, error) {
	historyJSON, err := json.Marshal(history)
	if err != nil {
		return nil, fmt.Errorf("skill synthesis: marshal history: %w", err)
	}

	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeHaiku4_5,
		MaxTokens: skillSynthesisMaxTokens,
		System:    []anthropic.TextBlockParam{{Text: skillSynthesisSystemPrompt(domain)}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(fmt.Sprintf(
				"Extract a reusable skill from this completed task. Output ONLY valid JSON.\n\n%s",
				string(historyJSON),
			))),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("skill synthesis: LLM call: %w", err)
	}
	if len(resp.Content) == 0 || resp.Content[0].Text == "" {
		return nil, fmt.Errorf("skill synthesis: empty LLM response")
	}

	// Strip markdown code fences the model may wrap around JSON output.
	raw := strings.TrimSpace(resp.Content[0].Text)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var sj synthesizedSkillJSON
	if err := json.Unmarshal([]byte(raw), &sj); err != nil {
		return nil, fmt.Errorf("skill synthesis: parse JSON output: %w", err)
	}

	// Empty name is the LLM's signal that this task has no reusable procedure.
	if sj.Name == "" {
		log.Info("skill synthesis: LLM found no reusable procedure", "domain", domain)
		return nil, nil
	}

	// Clamp LLM-generated fields to Tool Contract limits before validation.
	// Synthesis output is untrusted — the model occasionally generates a few
	// extra characters despite the prompt constraint.
	if len(sj.Name) > 64 {
		sj.Name = sj.Name[:64]
	}
	if len(sj.Description) > 300 {
		sj.Description = sj.Description[:300]
	}

	now := time.Now().UTC()
	node := &types.SkillNode{
		Name:          sj.Name,
		Level:         "command",
		Label:         sj.Label,
		Description:   sj.Description,
		Recipe:        sj.Recipe,
		Spec:          sj.Spec,
		Origin:        "synthesized",
		SynthesizedAt: &now,
	}

	// Run Tool Contract validation before accepting — synthesis output is
	// LLM-generated and must be treated as untrusted input (EDD §13.2).
	if err := skills.ValidateCommandContract(node); err != nil {
		return nil, fmt.Errorf("skill synthesis: Tool Contract violation: %w", err)
	}

	return node, nil
}

// skillSynthesisSystemPrompt returns the system prompt for the synthesis LLM call.
func skillSynthesisSystemPrompt(domain string) string {
	return fmt.Sprintf(`You are a skill extraction assistant for the %q domain of an AI agent system.

Analyse the completed task conversation and extract ONE reusable skill definition.

Output ONLY a single JSON object with this exact schema:
{
  "name": "<snake_case identifier, max 64 chars — describe the general procedure, not the specific task data>",
  "label": "<human-readable display name>",
  "description": "<max 300 chars — what the skill does AND at least one explicit 'Do NOT use when...' clause>",
  "recipe": "<numbered step-by-step execution procedure, max 500 chars — use {{param_name}} placeholders for every parameter defined in spec.parameters. Each step must be a concrete action. Example: '1. Fetch {{url}} using {{method}}. 2. Extract the JSON field {{field_name}} from the response. 3. Return the extracted value.'",
  "spec": {
    "parameters": {
      "<param_name>": {
        "type": "<string|integer|boolean|array|object>",
        "required": <true|false>,
        "description": "<required — what this parameter controls>"
      }
    }
  }
}

Rules:
- name must be snake_case, max 64 characters, unique within the domain.
- description must contain negative guidance: at least one 'Do NOT use when' clause.
- Every parameter in spec.parameters must have a non-empty description.
- recipe must reference every parameter defined in spec.parameters using {{param_name}} syntax.
- Generalise the procedure: replace task-specific values with named parameters.
- If the task used no novel procedure worth reusing (e.g. a trivial single-step lookup),
  output exactly: {"name":"","label":"","description":"","recipe":""}`, domain)
}

// attemptSkillSynthesis is the top-level driver called at task completion.
// It checks the threshold, calls synthesizeSkill, persists the result, and
// emits a skill_synthesized audit event so the UI can toast the user.
// All errors are logged and discarded — synthesis is never allowed to fail the task.
func attemptSkillSynthesis(
	ctx context.Context,
	client *anthropic.Client,
	log *slog.Logger,
	spawnCtx *SpawnContext,
	sl *SessionLog,
	ve *VaultExecutor,
	history []anthropic.MessageParam,
	toolCallCount int,
) {
	// No session log means NATS is unavailable — synthesis result cannot be
	// persisted, so there is nothing to gain from the LLM call.
	if sl == nil {
		return
	}
	if !shouldSynthesize(spawnCtx.SkillDomain, toolCallCount) {
		return
	}
	log.Info("skill synthesis: threshold met, attempting",
		"domain", spawnCtx.SkillDomain,
		"tool_call_count", toolCallCount,
	)

	node, err := synthesizeSkill(ctx, client, log, spawnCtx.SkillDomain, history)
	if err != nil {
		log.Warn("skill synthesis: failed", "domain", spawnCtx.SkillDomain, "error", err)
		return
	}
	if node == nil {
		return // LLM found nothing worth capturing
	}

	if err := sl.PersistSkill(spawnCtx.SkillDomain, node); err != nil {
		log.Warn("skill synthesis: persist failed",
			"skill_name", node.Name, "domain", spawnCtx.SkillDomain, "error", err)
		return
	}
	log.Info("skill synthesis: complete",
		"skill_name", node.Name, "domain", spawnCtx.SkillDomain)

	// Notify the orchestrator (and via it, the UI) that a new skill was created.
	ve.EmitSkillSynthesized(spawnCtx.SkillDomain, node.Name)
}
