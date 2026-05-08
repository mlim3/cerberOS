package dispatcher

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

func TestExtractSystemPrompt(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{"raw_input":"hello","system_prompt":"Do maintenance."}`)
	got := extractSystemPrompt(payload)
	if got != "Do maintenance." {
		t.Fatalf("extractSystemPrompt = %q, want %q", got, "Do maintenance.")
	}
	if extractSystemPrompt(json.RawMessage(`{}`)) != "" {
		t.Fatal("want empty for missing system_prompt")
	}
}

func TestIsMaintenancePayload(t *testing.T) {
	t.Parallel()
	if !isMaintenancePayload(json.RawMessage(`{"maintenance":true,"raw_input":"x"}`)) {
		t.Fatal("expected maintenance true")
	}
	if isMaintenancePayload(json.RawMessage(`{"maintenance":false}`)) {
		t.Fatal("expected false")
	}
	if isMaintenancePayload(json.RawMessage(`{"raw_input":"x"}`)) {
		t.Fatal("expected false when maintenance omitted")
	}
}

func TestBuildDecompositionInstructionsWithSystemPrompt(t *testing.T) {
	t.Parallel()
	scope := types.PolicyScope{Domains: []string{"general"}}
	raw := "Run decay."
	sys := "SYSTEM: prioritize fact extraction."
	out := buildDecompositionInstructionsWithFacts("550e8400-e29b-41d4-a716-446655440000", raw, scope, nil, sys)
	if !strings.Contains(out, sys) {
		t.Fatalf("instructions missing system prompt: %s", out)
	}
	if !strings.Contains(out, raw) {
		t.Fatalf("instructions missing raw_input: %s", out)
	}
	// Empty system prompt should match historic shape (no "Scheduled maintenance" header)
	outNoSys := buildDecompositionInstructionsWithFacts("550e8400-e29b-41d4-a716-446655440000", raw, scope, nil, "")
	if strings.Contains(outNoSys, "Scheduled maintenance directives") {
		t.Fatal("unexpected maintenance header when system prompt empty")
	}
}

func TestBuildDecompositionInstructionsWithFactsAndSystemPrompt(t *testing.T) {
	t.Parallel()
	scope := types.PolicyScope{Domains: []string{"general"}}
	facts := []string{"user likes tea"}
	sys := "SYS LINE"
	out := buildDecompositionInstructionsWithFacts("550e8400-e29b-41d4-a716-446655440000", "task", scope, facts, sys)
	if !strings.Contains(out, "User facts") {
		t.Fatal("missing facts section")
	}
	if !strings.Contains(out, sys) {
		t.Fatal("missing system section")
	}
}
