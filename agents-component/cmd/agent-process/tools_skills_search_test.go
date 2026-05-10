package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cerberOS/agents-component/pkg/types"
)

// fakeSearcher is a test stub that implements SkillSearcher without NATS.
type fakeSearcher struct {
	results []types.SkillSearchResult
}

func (f *fakeSearcher) SearchSkills(_ string, topK int) []types.SkillSearchResult {
	if topK > 0 && topK < len(f.results) {
		return f.results[:topK]
	}
	return f.results
}

func TestToolsForDomain_IncludesSkillsSearch(t *testing.T) {
	tools := toolsForDomain("web", nil, nil, nil)

	found := false
	for _, tool := range tools {
		if tool.Definition.Name == "skills_search" {
			found = true
			break
		}
	}
	if !found {
		names := make([]string, len(tools))
		for i, t := range tools {
			names[i] = t.Definition.Name
		}
		t.Errorf("skills_search not in tool registry; registered tools: %v", names)
	}
}

func TestSkillsSearchTool_EmptyQueryReturnsError(t *testing.T) {
	tool := skillsSearchTool(&fakeSearcher{}, "web", false)

	raw, _ := json.Marshal(map[string]interface{}{"query": ""})
	result := tool.Execute(nil, raw)

	if !result.IsError {
		t.Error("expected IsError=true for empty query")
	}
}

func TestSkillsSearchTool_NilSearcherReturnsUnavailable(t *testing.T) {
	tool := skillsSearchTool(nil, "web", false)

	raw, _ := json.Marshal(map[string]interface{}{"query": "fetch a URL"})
	result := tool.Execute(nil, raw)

	if !result.IsError {
		t.Error("expected IsError=true when SkillSearcher is nil")
	}
	if !strings.Contains(result.Content, "unavailable") {
		t.Errorf("expected 'unavailable' in content, got %q", result.Content)
	}
}

func TestSkillsSearchTool_NoResultsReturnsNotFound(t *testing.T) {
	tool := skillsSearchTool(&fakeSearcher{results: nil}, "web", false)

	raw, _ := json.Marshal(map[string]interface{}{"query": "something obscure"})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "No matching skills") {
		t.Errorf("expected 'No matching skills' in content, got %q", result.Content)
	}
}

func TestSkillsSearchTool_TopKRespected(t *testing.T) {
	sr := &fakeSearcher{results: []types.SkillSearchResult{
		{Domain: "web", Name: "web_fetch", Description: "Fetch a URL"},
		{Domain: "web", Name: "web_parse", Description: "Parse HTML"},
		{Domain: "data", Name: "data_query", Description: "Query data"},
	}}
	tool := skillsSearchTool(sr, "web", false)

	raw, _ := json.Marshal(map[string]interface{}{"query": "fetch URL", "top_k": 1})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	count, ok := result.Details["result_count"].(int)
	if !ok || count != 1 {
		t.Errorf("expected result_count=1 in details, got %v", result.Details["result_count"])
	}
}

func TestSkillsSearchTool_CrossDomainSuggestsSpawnAgent(t *testing.T) {
	sr := &fakeSearcher{results: []types.SkillSearchResult{
		{Domain: "web", Name: "web_fetch", Description: "Fetch a public URL"},
	}}
	tool := skillsSearchTool(sr, "general", true)

	raw, _ := json.Marshal(map[string]interface{}{"query": "fetch a public URL"})
	result := tool.Execute(nil, raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Call spawn_agent next") {
		t.Fatalf("expected spawn_agent guidance in content, got %q", result.Content)
	}
	if result.Details["recommended_action"] != "spawn_agent" {
		t.Fatalf("expected recommended_action=spawn_agent, got %v", result.Details["recommended_action"])
	}
}

func TestSkillsSearchTool_CrossDomainReturnsSpawnInstructions(t *testing.T) {
	sr := &fakeSearcher{results: []types.SkillSearchResult{
		{Domain: "web", Name: "web_fetch", Description: "Fetch a public URL"},
	}}
	tool := skillsSearchTool(sr, "general", true)

	raw, _ := json.Marshal(map[string]interface{}{"query": "fetch a public URL"})
	result := tool.Execute(nil, raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	requiredSkills, ok := result.Details["spawn_required_skills"].([]string)
	if !ok {
		t.Fatalf("expected []string spawn_required_skills, got %#v", result.Details["spawn_required_skills"])
	}
	if len(requiredSkills) != 1 || requiredSkills[0] != "web" {
		t.Fatalf("unexpected spawn_required_skills: %v", requiredSkills)
	}
	spawnInstructions, ok := result.Details["spawn_instructions"].(string)
	if !ok {
		t.Fatalf("expected string spawn_instructions, got %#v", result.Details["spawn_instructions"])
	}
	if !strings.Contains(spawnInstructions, "fetch a public URL") {
		t.Fatalf("expected query in spawn instructions, got %q", spawnInstructions)
	}
	if !strings.Contains(spawnInstructions, "web.web_fetch") {
		t.Fatalf("expected recommended command in spawn instructions, got %q", spawnInstructions)
	}
}

func TestSkillsSearchTool_WithinDomainStaysDirect(t *testing.T) {
	sr := &fakeSearcher{results: []types.SkillSearchResult{
		{Domain: "web", Name: "web_fetch", Description: "Fetch a public URL"},
	}}
	tool := skillsSearchTool(sr, "web", true)

	raw, _ := json.Marshal(map[string]interface{}{"query": "fetch a public URL"})
	result := tool.Execute(nil, raw)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if _, ok := result.Details["recommended_action"]; ok {
		t.Fatalf("did not expect recommended_action for same-domain result, got %v", result.Details["recommended_action"])
	}
	if !strings.Contains(result.Content, "Use the tool name directly") {
		t.Fatalf("expected direct-use guidance, got %q", result.Content)
	}
}
