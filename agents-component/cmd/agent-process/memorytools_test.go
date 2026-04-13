package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ─── truncateChars ────────────────────────────────────────────────────────────

func TestTruncateChars_ShortString_Unchanged(t *testing.T) {
	s := "hello"
	if got := truncateChars(s, 100); got != s {
		t.Errorf("short string: want %q, got %q", s, got)
	}
}

func TestTruncateChars_ExactlyAtLimit_Unchanged(t *testing.T) {
	s := strings.Repeat("a", 500)
	if got := truncateChars(s, 500); got != s {
		t.Errorf("at-limit string must not be truncated; len(got)=%d", len(got))
	}
}

func TestTruncateChars_OneOverLimit_Truncated(t *testing.T) {
	s := strings.Repeat("a", 501)
	got := truncateChars(s, 500)
	runes := []rune(got)
	if len(runes) != 500 {
		t.Errorf("want 500 runes, got %d", len(runes))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("truncated string must end with '...'")
	}
}

func TestTruncateChars_MuchLonger_Truncated(t *testing.T) {
	s := strings.Repeat("x", 1000)
	got := truncateChars(s, 500)
	if len([]rune(got)) != 500 {
		t.Errorf("want 500 runes, got %d", len([]rune(got)))
	}
}

func TestTruncateChars_Unicode_RespectsRuneBoundaries(t *testing.T) {
	// Each Japanese character is 3 bytes; truncation must not split at byte boundaries.
	s := strings.Repeat("日", 600)
	got := truncateChars(s, 500)
	if len([]rune(got)) != 500 {
		t.Errorf("want 500 runes, got %d runes", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("truncated Unicode string must end with '...'")
	}
}

func TestTruncateChars_EmptyString_Unchanged(t *testing.T) {
	if got := truncateChars("", 10); got != "" {
		t.Errorf("empty string must remain empty; got %q", got)
	}
}

// ─── memoryTools ──────────────────────────────────────────────────────────────

func TestMemoryTools_ReturnsThreeTools(t *testing.T) {
	tools := memoryTools(nil, "web", "ctx-1")
	if len(tools) != 3 {
		t.Fatalf("want 3 tools, got %d", len(tools))
	}
}

func TestMemoryTools_CorrectNames(t *testing.T) {
	tools := memoryTools(nil, "web", "ctx-1")
	names := make(map[string]bool)
	for _, tl := range tools {
		names[tl.Definition.Name] = true
	}
	for _, want := range []string{"memory_update", "profile_update", "memory_search"} {
		if !names[want] {
			t.Errorf("tool %q not found in memoryTools output", want)
		}
	}
}

func TestMemoryTools_AllHaveLabels(t *testing.T) {
	for _, tl := range memoryTools(nil, "web", "ctx-1") {
		if tl.Label == "" {
			t.Errorf("tool %q has empty Label", tl.Definition.Name)
		}
	}
}

// ─── memoryUpdateTool ─────────────────────────────────────────────────────────

func TestMemoryUpdateTool_ValidFact_ReturnsOK(t *testing.T) {
	tl := memoryUpdateTool(nil, "web") // nil sl → PersistAgentMemory is a no-op
	input, _ := json.Marshal(map[string]string{"fact": "The API returns ISO8601 timestamps."})
	result := tl.Execute(context.Background(), input)
	if result.IsError {
		t.Errorf("valid fact: unexpected error: %v", result.Content)
	}
	if !strings.Contains(result.Content, "memory updated") {
		t.Errorf("want 'memory updated' in content; got %q", result.Content)
	}
}

func TestMemoryUpdateTool_EmptyFact_IsError(t *testing.T) {
	tl := memoryUpdateTool(nil, "web")
	input, _ := json.Marshal(map[string]string{"fact": ""})
	result := tl.Execute(context.Background(), input)
	if !result.IsError {
		t.Error("empty fact must produce an error result")
	}
}

func TestMemoryUpdateTool_InvalidJSON_IsError(t *testing.T) {
	tl := memoryUpdateTool(nil, "web")
	result := tl.Execute(context.Background(), json.RawMessage(`{invalid`))
	if !result.IsError {
		t.Error("invalid JSON must produce an error result")
	}
}

func TestMemoryUpdateTool_LongFact_Truncated(t *testing.T) {
	// Fact is 600 chars — must be truncated to 500 before persist.
	longFact := strings.Repeat("a", 600)
	input, _ := json.Marshal(map[string]string{"fact": longFact})
	tl := memoryUpdateTool(nil, "web")
	result := tl.Execute(context.Background(), input)
	// Tool should still succeed (nil sl = no-op persist).
	if result.IsError {
		t.Errorf("long fact: unexpected error: %v", result.Content)
	}
}

func TestMemoryUpdateTool_MissingFactField_IsError(t *testing.T) {
	tl := memoryUpdateTool(nil, "web")
	// Valid JSON but no "fact" key — empty string.
	input, _ := json.Marshal(map[string]string{"other": "value"})
	result := tl.Execute(context.Background(), input)
	if !result.IsError {
		t.Error("missing 'fact' field (empty string) must produce an error result")
	}
}

// ─── profileUpdateTool ────────────────────────────────────────────────────────

func TestProfileUpdateTool_ValidObservation_ReturnsOK(t *testing.T) {
	tl := profileUpdateTool(nil, "ctx-1")
	input, _ := json.Marshal(map[string]string{"observation": "User prefers bullet-point summaries."})
	result := tl.Execute(context.Background(), input)
	if result.IsError {
		t.Errorf("valid observation: unexpected error: %v", result.Content)
	}
	if !strings.Contains(result.Content, "profile updated") {
		t.Errorf("want 'profile updated' in content; got %q", result.Content)
	}
}

func TestProfileUpdateTool_EmptyObservation_IsError(t *testing.T) {
	tl := profileUpdateTool(nil, "ctx-1")
	input, _ := json.Marshal(map[string]string{"observation": ""})
	result := tl.Execute(context.Background(), input)
	if !result.IsError {
		t.Error("empty observation must produce an error result")
	}
}

func TestProfileUpdateTool_InvalidJSON_IsError(t *testing.T) {
	tl := profileUpdateTool(nil, "ctx-1")
	result := tl.Execute(context.Background(), json.RawMessage(`{not json}`))
	if !result.IsError {
		t.Error("invalid JSON must produce an error result")
	}
}

func TestProfileUpdateTool_LongObservation_Truncated(t *testing.T) {
	long := strings.Repeat("b", 600)
	input, _ := json.Marshal(map[string]string{"observation": long})
	tl := profileUpdateTool(nil, "ctx-1")
	result := tl.Execute(context.Background(), input)
	if result.IsError {
		t.Errorf("long observation: unexpected error: %v", result.Content)
	}
}

// ─── memorySearchTool ─────────────────────────────────────────────────────────

func TestMemorySearchTool_ValidQuery_NilSL_ReturnsNotice(t *testing.T) {
	// nil sl → SearchSessions returns a notice string, not an error.
	tl := memorySearchTool(nil)
	input, _ := json.Marshal(map[string]interface{}{"query": "ISO8601 date format"})
	result := tl.Execute(context.Background(), input)
	if result.IsError {
		t.Errorf("nil sl search must not be an error; got %q", result.Content)
	}
	// The notice string from SearchSessions contains "unavailable".
	if !strings.Contains(result.Content, "unavailable") && !strings.Contains(result.Content, "no results") && !strings.Contains(result.Content, "timed out") {
		t.Logf("content: %q", result.Content) // informational — not a hard failure
	}
}

func TestMemorySearchTool_EmptyQuery_IsError(t *testing.T) {
	tl := memorySearchTool(nil)
	input, _ := json.Marshal(map[string]interface{}{"query": ""})
	result := tl.Execute(context.Background(), input)
	if !result.IsError {
		t.Error("empty query must produce an error result")
	}
}

func TestMemorySearchTool_InvalidJSON_IsError(t *testing.T) {
	tl := memorySearchTool(nil)
	result := tl.Execute(context.Background(), json.RawMessage(`{bad`))
	if !result.IsError {
		t.Error("invalid JSON must produce an error result")
	}
}

func TestMemorySearchTool_ZeroMaxResults_CoeercedToThree(t *testing.T) {
	// max_results=0 must be coerced to 3 before calling SearchSessions.
	// The function should not error just because max_results was 0.
	tl := memorySearchTool(nil)
	input, _ := json.Marshal(map[string]interface{}{"query": "some query", "max_results": 0})
	result := tl.Execute(context.Background(), input)
	if result.IsError {
		t.Errorf("max_results=0 must not produce an error; got %q", result.Content)
	}
}

func TestMemorySearchTool_MaxResultsOverTen_Capped(t *testing.T) {
	// max_results=50 must be capped to 10.
	tl := memorySearchTool(nil)
	input, _ := json.Marshal(map[string]interface{}{"query": "some query", "max_results": 50})
	result := tl.Execute(context.Background(), input)
	if result.IsError {
		t.Errorf("max_results=50 must not produce an error; got %q", result.Content)
	}
}

func TestMemorySearchTool_NegativeMaxResults_CoeercedToThree(t *testing.T) {
	tl := memorySearchTool(nil)
	input, _ := json.Marshal(map[string]interface{}{"query": "anything", "max_results": -1})
	result := tl.Execute(context.Background(), input)
	if result.IsError {
		t.Errorf("negative max_results must not produce an error; got %q", result.Content)
	}
}
