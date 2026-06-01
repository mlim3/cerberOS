package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClaimAction_MissingKey(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"ttl_seconds": 60})
	result := executeClaimAction(nil, raw)
	if !result.IsError {
		t.Fatal("want error for missing key")
	}
	if !strings.Contains(result.Content, "key") {
		t.Fatalf("unexpected message: %q", result.Content)
	}
}

func TestClaimAction_InvalidTTL(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"key": "demo", "ttl_seconds": 0})
	result := executeClaimAction(nil, raw)
	if !result.IsError {
		t.Fatal("want error for invalid ttl")
	}
	if !strings.Contains(result.Content, "ttl_seconds") {
		t.Fatalf("unexpected message: %q", result.Content)
	}
}

func TestClaimAction_NilSessionLog_ReturnsUnavailable(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"key": "demo", "ttl_seconds": 60})
	result := executeClaimAction(nil, raw)
	if !result.IsError {
		t.Fatal("want error when session log unavailable")
	}
	if !strings.Contains(strings.ToLower(result.Content), "unavailable") {
		t.Fatalf("unexpected message: %q", result.Content)
	}
}

func TestClaimActionTool_DefinitionFields(t *testing.T) {
	tool := claimActionTool(nil)
	if tool.Definition.Name != "claim_action" {
		t.Fatalf("name = %q", tool.Definition.Name)
	}
	if tool.Label == "" {
		t.Fatal("label must not be empty")
	}
	if !strings.Contains(strings.ToLower(tool.Definition.Description.Value), "not") {
		t.Fatalf("description should include negative guidance, got %q", tool.Definition.Description.Value)
	}
}

func TestCompleteAction_MissingKey(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"status": "completed"})
	result := executeCompleteAction(nil, raw)
	if !result.IsError {
		t.Fatal("want error for missing key")
	}
	if !strings.Contains(result.Content, "key") {
		t.Fatalf("unexpected message: %q", result.Content)
	}
}

func TestCompleteAction_NilSessionLog_ReturnsUnavailable(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"key": "demo"})
	result := executeCompleteAction(nil, raw)
	if !result.IsError {
		t.Fatal("want error when session log unavailable")
	}
	if !strings.Contains(strings.ToLower(result.Content), "unavailable") {
		t.Fatalf("unexpected message: %q", result.Content)
	}
}

func TestCompleteActionTool_DefinitionFields(t *testing.T) {
	tool := completeActionTool(nil)
	if tool.Definition.Name != "complete_action" {
		t.Fatalf("name = %q", tool.Definition.Name)
	}
	if tool.Label == "" {
		t.Fatal("label must not be empty")
	}
	if !strings.Contains(strings.ToLower(tool.Definition.Description.Value), "not") {
		t.Fatalf("description should include negative guidance, got %q", tool.Definition.Description.Value)
	}
}
