package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

func TestGetSkillsManager_InitialisesWithKnownDomains(t *testing.T) {
	mgr := getSkillsManager()

	domains := mgr.ListDomains()
	if len(domains) == 0 {
		t.Fatal("expected at least one domain, got none")
	}

	found := false
	for _, d := range domains {
		if d == "web" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected \"web\" domain to be registered; got %v", domains)
	}
}

func TestGetSkillsManager_WebCommandsIndexed(t *testing.T) {
	mgr := getSkillsManager()

	results, err := mgr.Search("fetch a URL", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results for \"fetch a URL\", got none")
	}

	found := false
	for _, r := range results {
		if r.Name == "web_fetch" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected web_fetch in results; got %+v", results)
	}
}

func TestToolsForDomain_IncludesSkillsSearch(t *testing.T) {
	tools := toolsForDomain("web", nil, nil)

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
	tool := skillsSearchTool(getSkillsManager(), "web", false)

	raw, _ := json.Marshal(map[string]interface{}{"query": ""})
	result := tool.Execute(nil, raw)

	if !result.IsError {
		t.Error("expected IsError=true for empty query")
	}
}

func TestSkillsSearchTool_ValidQueryReturnsContent(t *testing.T) {
	tool := skillsSearchTool(getSkillsManager(), "web", false)

	raw, _ := json.Marshal(map[string]interface{}{"query": "fetch a web page"})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if result.Content == "" {
		t.Error("expected non-empty content for valid query")
	}
}

func TestSkillsSearchTool_TopKRespected(t *testing.T) {
	tool := skillsSearchTool(getSkillsManager(), "web", false)

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
	mgr := newTestSkillsManager(t)
	tool := skillsSearchTool(mgr, "general", true)

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
	mgr := newTestSkillsManager(t)
	tool := skillsSearchTool(mgr, "general", true)

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
	mgr := newTestSkillsManager(t)
	tool := skillsSearchTool(mgr, "web", true)

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

func newTestSkillsManager(t *testing.T) skills.Manager {
	t.Helper()

	mgr := skills.New()
	for _, domain := range []*types.SkillNode{
		{
			Name:  "general",
			Level: "domain",
			Children: map[string]*types.SkillNode{},
		},
		{
			Name:  "web",
			Level: "domain",
			Children: map[string]*types.SkillNode{
				"web_fetch": {
					Name:        "web_fetch",
					Level:       "command",
					Label:       "Web Fetch",
					Description: "Fetch a public URL and return the response body. Do NOT use for authenticated requests.",
					Spec: &types.SkillSpec{Parameters: map[string]types.ParameterDef{
						"url": {Type: "string", Required: true, Description: "URL to fetch"},
					}},
				},
			},
		},
	} {
		if err := mgr.RegisterDomain(domain); err != nil {
			t.Fatalf("RegisterDomain(%s): %v", domain.Name, err)
		}
	}
	return mgr
}
