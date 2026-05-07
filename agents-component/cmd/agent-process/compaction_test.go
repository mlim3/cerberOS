package main

import (
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// assistantMsgWithTools constructs an assistant-role MessageParam containing
// one ToolUse block per provided name. Used to build synthetic history for
// retention-boundary and extractive-summary tests.
func assistantMsgWithTools(names ...string) anthropic.MessageParam {
	content := make([]anthropic.ContentBlockParamUnion, len(names))
	for i, name := range names {
		content[i] = anthropic.ContentBlockParamUnion{
			OfToolUse: &anthropic.ToolUseBlockParam{
				ID:   "tool-id-" + name,
				Name: name,
			},
		}
	}
	return anthropic.MessageParam{
		Role:    anthropic.MessageParamRoleAssistant,
		Content: content,
	}
}

// userMsgPlain constructs a plain user text MessageParam.
func userMsgPlain(text string) anthropic.MessageParam {
	return anthropic.NewUserMessage(anthropic.NewTextBlock(text))
}

// ---- findRetentionBoundary tests ----

func TestFindRetentionBoundary_EmptyHistory(t *testing.T) {
	if got := findRetentionBoundary(nil); got != 0 {
		t.Errorf("empty history: want 0, got %d", got)
	}
}

func TestFindRetentionBoundary_BelowThreshold(t *testing.T) {
	// 9 assistant turns — all within the retention window; nothing to compact.
	history := make([]anthropic.MessageParam, 9)
	for i := range history {
		history[i] = assistantMsgWithTools("web_fetch")
	}
	if got := findRetentionBoundary(history); got != 0 {
		t.Errorf("9 assistant turns (< threshold): want 0, got %d", got)
	}
}

func TestFindRetentionBoundary_AtThreshold(t *testing.T) {
	// Exactly 10 assistant turns — boundary must be 0 (still nothing to compact).
	history := make([]anthropic.MessageParam, 10)
	for i := range history {
		history[i] = assistantMsgWithTools("web_fetch")
	}
	if got := findRetentionBoundary(history); got != 0 {
		t.Errorf("10 assistant turns (= threshold): want 0, got %d", got)
	}
}

func TestFindRetentionBoundary_AboveThreshold(t *testing.T) {
	// 11 assistant turns — 1 should fall outside the retention window.
	history := make([]anthropic.MessageParam, 11)
	for i := range history {
		history[i] = assistantMsgWithTools("web_fetch")
	}
	boundary := findRetentionBoundary(history)
	if boundary == 0 {
		t.Fatal("11 assistant turns: expected non-zero boundary")
	}
	// The retained slice must contain exactly compactionRetainTurns assistant turns.
	retained := history[boundary:]
	count := 0
	for _, m := range retained {
		if m.Role == anthropic.MessageParamRoleAssistant {
			count++
		}
	}
	if count != compactionRetainTurns {
		t.Errorf("retained window: want %d assistant turns, got %d", compactionRetainTurns, count)
	}
}

func TestFindRetentionBoundary_MixedRoles(t *testing.T) {
	// Interleaved user/assistant messages; 11 assistant turns total.
	var history []anthropic.MessageParam
	for i := 0; i < 11; i++ {
		history = append(history, userMsgPlain("user input"))
		history = append(history, assistantMsgWithTools("web_fetch"))
	}
	boundary := findRetentionBoundary(history)
	if boundary == 0 {
		t.Fatal("mixed history with 11 assistant turns: expected non-zero boundary")
	}
	retained := history[boundary:]
	count := 0
	for _, m := range retained {
		if m.Role == anthropic.MessageParamRoleAssistant {
			count++
		}
	}
	if count != compactionRetainTurns {
		t.Errorf("retained window (mixed): want %d assistant turns, got %d", compactionRetainTurns, count)
	}
}

func TestFindRetentionBoundary_OnlyUserMessages(t *testing.T) {
	// No assistant turns — boundary must be 0.
	history := []anthropic.MessageParam{
		userMsgPlain("a"),
		userMsgPlain("b"),
		userMsgPlain("c"),
	}
	if got := findRetentionBoundary(history); got != 0 {
		t.Errorf("only user messages: want 0, got %d", got)
	}
}

// ---- extractiveSummary tests ----

func TestExtractiveSummary_HeaderFormat(t *testing.T) {
	summary := extractiveSummary(nil, 3, 7)
	want := "[COMPACTED SUMMARY — turns 3 through 7]"
	if !strings.HasPrefix(summary, want) {
		t.Errorf("header format: want prefix %q\ngot: %q", want, summary)
	}
}

func TestExtractiveSummary_ToolNamesIncluded(t *testing.T) {
	history := []anthropic.MessageParam{
		assistantMsgWithTools("web_fetch", "vault_web_fetch"),
	}
	summary := extractiveSummary(history, 1, 2)
	for _, name := range []string{"web_fetch", "vault_web_fetch"} {
		if !strings.Contains(summary, name) {
			t.Errorf("tool name %q missing from extractive summary:\n%s", name, summary)
		}
	}
}

func TestExtractiveSummary_ToolResultOkStatus(t *testing.T) {
	history := []anthropic.MessageParam{
		assistantMsgWithTools("web_fetch"),
		anthropic.NewUserMessage(
			anthropic.NewToolResultBlock("tool-id-web_fetch", "page content here", false),
		),
	}
	summary := extractiveSummary(history, 1, 2)
	if !strings.Contains(summary, "[ok]") {
		t.Errorf("expected '[ok]' status in extractive summary:\n%s", summary)
	}
	if !strings.Contains(summary, "page content here") {
		t.Errorf("expected result snippet in extractive summary:\n%s", summary)
	}
}

func TestExtractiveSummary_ToolResultErrorStatus(t *testing.T) {
	history := []anthropic.MessageParam{
		assistantMsgWithTools("web_fetch"),
		anthropic.NewUserMessage(
			anthropic.NewToolResultBlock("tool-id-web_fetch", "connection refused", true),
		),
	}
	summary := extractiveSummary(history, 1, 2)
	if !strings.Contains(summary, "[error]") {
		t.Errorf("expected '[error]' status in extractive summary:\n%s", summary)
	}
	if !strings.Contains(summary, "connection refused") {
		t.Errorf("expected error snippet in extractive summary:\n%s", summary)
	}
}

func TestExtractiveSummary_SnippetTruncated(t *testing.T) {
	// Content longer than compactionSnippetBytes must be truncated.
	longContent := strings.Repeat("x", compactionSnippetBytes+50)
	history := []anthropic.MessageParam{
		assistantMsgWithTools("web_fetch"),
		anthropic.NewUserMessage(
			anthropic.NewToolResultBlock("tool-id-web_fetch", longContent, false),
		),
	}
	summary := extractiveSummary(history, 1, 2)
	if !strings.Contains(summary, "…") {
		t.Errorf("expected truncation marker '…' for long content:\n%s", summary)
	}
	if strings.Contains(summary, longContent) {
		t.Error("full long content must not appear verbatim in truncated summary")
	}
}

func TestExtractiveSummary_EmptyHistory(t *testing.T) {
	// Empty history produces only the header — no panic.
	summary := extractiveSummary(nil, 1, 1)
	if !strings.Contains(summary, "[COMPACTED SUMMARY") {
		t.Errorf("expected header in empty-history summary, got: %q", summary)
	}
}

func TestExtractiveSummary_MultipleToolsAndResults(t *testing.T) {
	history := []anthropic.MessageParam{
		assistantMsgWithTools("web_fetch"),
		anthropic.NewUserMessage(
			anthropic.NewToolResultBlock("tool-id-web_fetch", "first result", false),
		),
		assistantMsgWithTools("vault_web_fetch"),
		anthropic.NewUserMessage(
			anthropic.NewToolResultBlock("tool-id-vault_web_fetch", "second result", true),
		),
	}
	summary := extractiveSummary(history, 1, 4)
	for _, want := range []string{"web_fetch", "vault_web_fetch", "[ok]", "[error]", "first result", "second result"} {
		if !strings.Contains(summary, want) {
			t.Errorf("expected %q in multi-call extractive summary:\n%s", want, summary)
		}
	}
}
