package main

// TestRunLoop_TaskDoneHistoryEndsOnToolResult is the regression guard for the
// snapshot ordering bug fixed alongside PR #142 (feat: multi prompting - v1).
//
// Before the fix the task_complete exit path wrote the ConversationSnapshot
// before appending the final tool_result user turn, so the persisted history
// ended with an assistant turn containing dangling tool_use blocks. When the
// next task in that conversation replayed the snapshot as prior_turns the
// Anthropic API rejected it with:
//
//	messages.N: tool_use ids were found without tool_result blocks
//	immediately after: toolu_…
//
// After the fix RunLoop must append the tool_result user turn to history
// before returning, so finalHistory always ends on a well-formed turn
// boundary — either assistant-text (end_turn path) or user-tool_result
// (task_complete path).
//
// Run:
//
//	go test ./cmd/agent-process/ -v -run TestRunLoop_TaskDoneHistoryEndsOnToolResult

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestRunLoop_TaskDoneHistoryEndsOnToolResult(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-snapshot-mock")

	tcInput, _ := json.Marshal(map[string]string{"result": "snapshot regression ok"})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		// A single iteration: tool_use(task_complete) → RunLoop terminates via
		// the taskDone branch. This is the exact path that produced the
		// malformed snapshot before the fix.
		resp := mockToolUse(1, "task_complete", tcInput, 1_000, 50)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode mock response: %v", err)
		}
	}))
	defer srv.Close()

	spawnCtx := &SpawnContext{
		TaskID:         "snapshot-regression-1",
		SkillDomain:    "data",
		TraceID:        "snapshot-trace-1",
		Instructions:   "Complete immediately.",
		ConversationID: "conv-regression-1", // non-empty triggers the snapshot write path
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	result, finalHistory, err := RunLoop(ctx, log, spawnCtx,
		nil /* ve */, nil /* steerer */, nil /* as */, nil, /* budget */
		nil, /* priorTurns */
		option.WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("RunLoop returned error: %v", err)
	}
	if result == "" {
		t.Error("RunLoop returned empty result")
	}
	if len(finalHistory) < 2 {
		t.Fatalf("finalHistory too short: want >=2 turns, got %d", len(finalHistory))
	}

	// Invariant 1: the last turn must be a USER turn (tool_result sits in the
	// user role), not an assistant turn with dangling tool_use blocks.
	last := finalHistory[len(finalHistory)-1]
	if last.Role != anthropic.MessageParamRoleUser {
		t.Fatalf("last turn role: want user (tool_result), got %q — taskDone path wrote snapshot before appending tool_result", last.Role)
	}

	// Invariant 2: the last user turn must contain at least one tool_result
	// block — this is the closure for the assistant's tool_use(task_complete).
	hasToolResult := false
	for _, block := range last.Content {
		if block.OfToolResult != nil {
			hasToolResult = true
			break
		}
	}
	if !hasToolResult {
		t.Fatal("last turn has no tool_result block — the snapshot would leave the prior assistant tool_use unmatched when replayed as prior_turns")
	}

	// Invariant 3: no earlier assistant turn may end the history with a
	// dangling tool_use — every assistant tool_use block must be followed
	// somewhere by a user turn containing a matching tool_result. This
	// double-checks the shape the Anthropic API enforces on replay.
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
