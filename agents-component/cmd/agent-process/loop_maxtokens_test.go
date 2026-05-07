// Regression test for the max_tokens graceful-handling fix.
//
// Before the fix, an Anthropic response with stop_reason == "max_tokens"
// caused RunLoop to return:
//
//	"unexpected stop reason: max_tokens"
//
// which bubbled through the orchestrator and surfaced in the UI as the
// misleading "Maximum recovery attempts exceeded. Task could not complete."
// (the executor's hardcoded default error code for any subtask failure).
//
// The 4096-token cap that triggered this was the real cause — a
// "please expand this plan" follow-up naturally emits thousands of tokens.
//
// After the fix:
//   - maxOutputTokens is 16_384 (plenty of headroom for long-form content).
//   - When the model *does* still hit the cap, RunLoop returns the partial
//     text with a truncation notice appended instead of failing the task.
//
// This test pins the graceful-path behaviour for the second property.
//
// Run:
//
//	go test ./cmd/agent-process/ -v -run TestRunLoop_MaxTokensReturnsPartialTextWithNotice

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func mockMaxTokens(n int, text string, in, out int64) mockAPIResponse {
	return mockAPIResponse{
		ID: "msg_mt_" + string(rune('0'+n)), Type: "message", Role: "assistant",
		StopReason: "max_tokens",
		Model:      "claude-haiku-4-5-20251001",
		Content:    []mockAPIContent{{Type: "text", Text: text}},
		Usage:      mockAPIUsage{InputTokens: in, OutputTokens: out},
	}
}

func TestRunLoop_MaxTokensReturnsPartialTextWithNotice(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-max-tokens-mock")

	const partial = "Business plan, section 1: executive summary covering positioning, market gap, and …"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		resp := mockMaxTokens(1, partial, 6_000, 16_384)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode mock response: %v", err)
		}
	}))
	defer srv.Close()

	spawnCtx := &SpawnContext{
		TaskID:         "max-tokens-regression-1",
		SkillDomain:    "data",
		TraceID:        "max-tokens-trace-1",
		Instructions:   "Please expand the business plan — much more detail.",
		ConversationID: "conv-max-tokens-1",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	result, finalHistory, err := RunLoop(ctx, log, spawnCtx,
		nil /* ve */, nil /* steerer */, nil /* as */, nil /* budget */, nil, /* priorTurns */
		option.WithBaseURL(srv.URL),
	)
	// Invariant 1: the max_tokens path must NOT fail the task.
	if err != nil {
		t.Fatalf("RunLoop returned error on max_tokens stop reason: %v — this is the regression (pre-fix behaviour)", err)
	}
	// Invariant 2: the partial text that the model produced must be surfaced to the caller.
	if !strings.Contains(result, partial) {
		t.Fatalf("result does not contain the partial text from the response:\n  got:  %q\n  want: contains %q", result, partial)
	}
	// Invariant 3: the result must include a truncation notice so the UI / user
	// know the output is not the full answer.
	if !strings.Contains(strings.ToLower(result), "truncated") {
		t.Fatalf("result missing truncation notice (expected word \"truncated\"):\n  got: %q", result)
	}
	// Invariant 4: history must still be well-formed — a single assistant turn
	// with the partial text appended. No dangling tool_use anywhere.
	if len(finalHistory) < 2 {
		t.Fatalf("finalHistory too short: want >=2 turns (user instructions + assistant partial), got %d", len(finalHistory))
	}
	last := finalHistory[len(finalHistory)-1]
	if last.Role != anthropic.MessageParamRoleAssistant {
		t.Fatalf("last turn role: want assistant (partial response), got %q", last.Role)
	}
	// Defense-in-depth: no dangling tool_use IDs.
	openToolUseIDs := map[string]bool{}
	for _, msg := range finalHistory {
		if msg.Role == anthropic.MessageParamRoleAssistant {
			for _, b := range msg.Content {
				if tu := b.OfToolUse; tu != nil {
					openToolUseIDs[tu.ID] = true
				}
			}
			continue
		}
		for _, b := range msg.Content {
			if tr := b.OfToolResult; tr != nil {
				delete(openToolUseIDs, tr.ToolUseID)
			}
		}
	}
	if len(openToolUseIDs) > 0 {
		t.Fatalf("history contains %d tool_use blocks with no matching tool_result: %v", len(openToolUseIDs), openToolUseIDs)
	}
}
