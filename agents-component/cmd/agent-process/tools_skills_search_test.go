package main

import (
	"encoding/json"
	"testing"
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
	tools := toolsForDomain("web", nil)

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
	tool := skillsSearchTool(getSkillsManager())

	raw, _ := json.Marshal(map[string]interface{}{"query": ""})
	result := tool.Execute(nil, raw)

	if !result.IsError {
		t.Error("expected IsError=true for empty query")
	}
}

func TestSkillsSearchTool_ValidQueryReturnsContent(t *testing.T) {
	tool := skillsSearchTool(getSkillsManager())

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
	tool := skillsSearchTool(getSkillsManager())

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
