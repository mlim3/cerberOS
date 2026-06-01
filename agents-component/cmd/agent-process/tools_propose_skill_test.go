package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cerberOS/agents-component/pkg/types"
)

// ── propose_skill: input validation (no NATS needed) ──────────────────────────

func TestProposeSkill_MissingName(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{
		"domain":      "web",
		"label":       "My Skill",
		"description": "Does something useful. Do NOT use otherwise.",
		"recipe":      "Step 1: do thing.",
	})
	result := executeProposeSkill(nil, "user-1", raw)
	if !result.IsError {
		t.Error("missing name: want IsError=true")
	}
	if !strings.Contains(result.Content, "name") {
		t.Errorf("missing name: error should mention 'name', got %q", result.Content)
	}
}

func TestProposeSkill_MissingDomain(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{
		"name":        "my_skill",
		"label":       "My Skill",
		"description": "Does something useful. Do NOT use otherwise.",
		"recipe":      "Step 1: do thing.",
	})
	result := executeProposeSkill(nil, "user-1", raw)
	if !result.IsError {
		t.Error("missing domain: want IsError=true")
	}
	if !strings.Contains(result.Content, "domain") {
		t.Errorf("missing domain: error should mention 'domain', got %q", result.Content)
	}
}

func TestProposeSkill_MissingLabel(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{
		"name":        "my_skill",
		"domain":      "web",
		"description": "Does something useful. Do NOT use otherwise.",
		"recipe":      "Step 1: do thing.",
	})
	result := executeProposeSkill(nil, "user-1", raw)
	if !result.IsError {
		t.Error("missing label: want IsError=true")
	}
}

func TestProposeSkill_MissingDescription(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{
		"name":   "my_skill",
		"domain": "web",
		"label":  "My Skill",
		"recipe": "Step 1: do thing.",
	})
	result := executeProposeSkill(nil, "user-1", raw)
	if !result.IsError {
		t.Error("missing description: want IsError=true")
	}
}

func TestProposeSkill_MissingRecipe(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{
		"name":        "my_skill",
		"domain":      "web",
		"label":       "My Skill",
		"description": "Does something useful. Do NOT use otherwise.",
	})
	result := executeProposeSkill(nil, "user-1", raw)
	if !result.IsError {
		t.Error("missing recipe: want IsError=true")
	}
}

func TestProposeSkill_InvalidJSON(t *testing.T) {
	result := executeProposeSkill(nil, "user-1", json.RawMessage(`not-json`))
	if !result.IsError {
		t.Error("invalid JSON: want IsError=true")
	}
	if !strings.Contains(result.Content, "invalid parameters") {
		t.Errorf("invalid JSON: expected 'invalid parameters' in error, got %q", result.Content)
	}
}

func TestProposeSkill_ToolContractViolation_DescriptionTooLong(t *testing.T) {
	// description over 300 chars should fail ValidateCommandContract.
	longDesc := strings.Repeat("x", 301)
	raw, _ := json.Marshal(map[string]string{
		"name":        "my_skill",
		"domain":      "web",
		"label":       "My Skill",
		"description": longDesc,
		"recipe":      "Step 1: do thing.",
	})
	result := executeProposeSkill(nil, "user-1", raw)
	if !result.IsError {
		t.Error("description too long: want IsError=true (tool contract violation)")
	}
	if !strings.Contains(result.Content, "contract violation") {
		t.Errorf("description too long: expected 'contract violation', got %q", result.Content)
	}
}

func TestProposeSkill_ToolContractViolation_ParameterMissingDescription(t *testing.T) {
	params := map[string]types.ParameterDef{
		"query": {Type: "string", Required: true, Description: ""},
	}
	raw, _ := json.Marshal(map[string]any{
		"name":        "search_web",
		"domain":      "web",
		"label":       "Search Web",
		"description": "Searches the web. Do NOT use for local queries.",
		"recipe":      "Step 1: search {{query}}.",
		"parameters":  params,
	})
	result := executeProposeSkill(nil, "user-1", raw)
	if !result.IsError {
		t.Error("parameter missing description: want IsError=true (tool contract violation)")
	}
	if !strings.Contains(result.Content, "contract violation") {
		t.Errorf("parameter missing description: expected 'contract violation', got %q", result.Content)
	}
}

func TestProposeSkill_NilSessionLog_ReturnsError(t *testing.T) {
	// With nil sl, PersistSkillWithScope is a no-op (returns nil), so the tool
	// should succeed — the skill is "persisted" (no-op).
	raw, _ := json.Marshal(map[string]string{
		"name":        "valid_skill",
		"domain":      "web",
		"label":       "Valid Skill",
		"description": "Does something useful. Do NOT use otherwise.",
		"recipe":      "Step 1: do the thing.",
	})
	result := executeProposeSkill(nil, "user-1", raw)
	// nil sl means PersistSkillWithScope is a no-op and returns nil.
	// The tool should report success (the no-op is a valid outcome in tests).
	if result.IsError {
		t.Errorf("nil sl: unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "valid_skill") {
		t.Errorf("nil sl: expected skill name in success message, got %q", result.Content)
	}
}

func TestProposeSkill_ValidParams_WithParameters(t *testing.T) {
	// Full valid input with parameters — should pass contract validation and
	// succeed (nil sl means persistence is a no-op).
	params := map[string]types.ParameterDef{
		"query": {Type: "string", Required: true, Description: "The search query to use."},
		"limit": {Type: "integer", Required: false, Description: "Maximum number of results."},
	}
	raw, _ := json.Marshal(map[string]any{
		"name":           "semantic_search",
		"domain":         "data",
		"label":          "Semantic Search",
		"description":    "Runs a semantic search over the knowledge base. Do NOT use for exact-match lookups.",
		"recipe":         "Step 1: embed {{query}}. Step 2: retrieve top {{limit}} results.",
		"parameters":     params,
		"timeout_seconds": 30,
	})
	result := executeProposeSkill(nil, "user-1", raw)
	if result.IsError {
		t.Errorf("valid params: unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "semantic_search") {
		t.Errorf("valid params: expected skill name in result, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "data") {
		t.Errorf("valid params: expected domain in result, got %q", result.Content)
	}
}

// ── proposeSkillTool: definition contract ────────────────────────────────────

func TestProposeSkillTool_DefinitionFields(t *testing.T) {
	tool := proposeSkillTool(nil, "user-1")
	if tool.Definition.Name != "propose_skill" {
		t.Errorf("name: want %q, got %q", "propose_skill", tool.Definition.Name)
	}
	if tool.Label == "" {
		t.Error("label must not be empty")
	}
	if tool.TimeoutSeconds <= 0 {
		t.Error("timeout_seconds must be positive")
	}
	desc := tool.Definition.Description.Value
	if desc == "" {
		t.Error("description must not be empty")
	}
	// Negative guidance present.
	if !strings.Contains(strings.ToLower(desc), "not") {
		t.Errorf("description should include negative guidance ('not'); got: %q", desc)
	}
}
