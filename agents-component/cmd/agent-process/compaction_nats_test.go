package main

// TestNATS_CompactionAndContinue exercises scenario 4: a task drives the agent
// past the 80 % context threshold, compaction fires, and the agent continues to
// successful completion.
//
// A mock Anthropic HTTP server handles all LLM calls so the test requires no
// real API key or network access. Real NATS is not required — ve is nil so
// session persistence is skipped; all compaction code paths are still exercised.
//
// Run:
//
//	go test ./cmd/agent-process/ -v -run TestNATS_Compaction
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// ─── Minimal Anthropic API wire types for the mock server ────────────────────

type mockAPIResponse struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Content      []mockAPIContent `json:"content"`
	Model        string           `json:"model"`
	StopReason   string           `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence"`
	Usage        mockAPIUsage     `json:"usage"`
}

type mockAPIContent struct {
	Type  string          `json:"type"`
	ID    string          `json:"id,omitempty"`    // tool_use only
	Name  string          `json:"name,omitempty"`  // tool_use only
	Input json.RawMessage `json:"input,omitempty"` // tool_use only
	Text  string          `json:"text,omitempty"`  // text only
}

type mockAPIUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

func mockToolUse(n int, toolName string, input json.RawMessage, in, out int64) mockAPIResponse {
	return mockAPIResponse{
		ID: fmt.Sprintf("msg_%03d", n), Type: "message", Role: "assistant",
		StopReason: "tool_use",
		Model:      "claude-haiku-4-5-20251001",
		Content: []mockAPIContent{
			{Type: "tool_use", ID: fmt.Sprintf("toolu_%03d", n), Name: toolName, Input: input},
		},
		Usage: mockAPIUsage{InputTokens: in, OutputTokens: out},
	}
}

func mockEndTurn(n int, text string, in, out int64) mockAPIResponse {
	return mockAPIResponse{
		ID: fmt.Sprintf("msg_%03d", n), Type: "message", Role: "assistant",
		StopReason: "end_turn",
		Model:      "claude-haiku-4-5-20251001",
		Content:    []mockAPIContent{{Type: "text", Text: text}},
		Usage:      mockAPIUsage{InputTokens: in, OutputTokens: out},
	}
}

// ─── Test ─────────────────────────────────────────────────────────────────────

// TestNATS_CompactionAndContinue verifies that:
//  1. Repeated LLM iterations accumulate enough assistant turns for compaction.
//  2. When the token count reaches ≥ 80 % of the context window, compactionPending is set.
//  3. compact() fires before the next Reason phase and calls the LLM for summarisation.
//  4. After compaction the loop continues and completes correctly via task_complete.
func TestNATS_CompactionAndContinue(t *testing.T) {
	// The Anthropic client requires a non-empty API key; the mock server ignores it.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-compaction-mock")

	// We need ≥ 11 assistant turns in history before compact() will find a non-zero
	// retention boundary (compactionRetainTurns = 10).
	//
	// Call sequence:
	//   Calls 1–11:  Phase 1 → tool_use(data_transform), low tokens
	//                → 11 assistant turns accumulated; compactionPending = false
	//   Call 12:     Phase 1 → tool_use(data_transform), 160 000 tokens (= 80 %)
	//                → compactionPending = true
	//   Call 13:     compact() → summariseHistory internal call → text summary
	//                (quality gate: summary must be < 25 % of original window JSON)
	//   Call 14:     Phase 1 (iteration 13 after compaction) → tool_use(task_complete)
	//                → RunLoop returns with result

	const (
		lowIn  int64 = 1_000
		lowOut int64 = 50
		// 159 950 input + 50 output = 160 000 = 80 % of 200 000 context window.
		highIn int64 = 159_950
	)

	dtInput, _ := json.Marshal(map[string]string{
		"data": `{"value":42}`,
		"path": "$.value",
	})
	tcInput, _ := json.Marshal(map[string]string{
		"result": "compaction test completed successfully",
	})

	var callCount atomic.Int32
	var compactionCallN atomic.Int32 // set to the call number when the summarisation call is served

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")

		var resp mockAPIResponse
		switch {
		case n >= 1 && n <= 11:
			resp = mockToolUse(n, "data_transform", dtInput, lowIn, lowOut)
		case n == 12:
			resp = mockToolUse(n, "data_transform", dtInput, highIn, lowOut)
		case n == 13:
			compactionCallN.Store(int32(n))
			// Return a short summary that passes the 25 % quality gate.
			// The original history JSON for 1 turn is large; this short string
			// will be well below 25 % of that.
			resp = mockEndTurn(n, "Prior turns: 11 data_transform calls completed successfully.", 200, 20)
		case n == 14:
			resp = mockToolUse(n, "task_complete", tcInput, lowIn, lowOut)
		default:
			t.Errorf("unexpected LLM call #%d — sequence exhausted", n)
			http.Error(w, "unexpected call", http.StatusInternalServerError)
			return
		}

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode mock response call #%d: %v", n, err)
		}
	}))
	defer srv.Close()

	spawnCtx := &SpawnContext{
		TaskID:       "compaction-task-1",
		SkillDomain:  "data", // only data_transform + task_complete; no network calls
		TraceID:      "compaction-trace-1",
		Instructions: "Process the data through multiple transformation steps and report the results.",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	result, err := RunLoop(ctx, log, spawnCtx, nil /* ve */, option.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("RunLoop returned error: %v", err)
	}
	if result == "" {
		t.Error("RunLoop returned empty result")
	}
	t.Logf("RunLoop result: %q", result)

	// compactionCallN is non-zero iff the summarisation LLM call was served
	// (call 13), meaning compact() ran and made a real LLM request.
	if compactionCallN.Load() == 0 {
		t.Error("expected compaction summarisation call (call 13) but it was never served")
	}

	total := int(callCount.Load())
	if total < 14 {
		t.Errorf("expected at least 14 LLM calls total, got %d", total)
	}
	t.Logf("total LLM calls: %d", total)
}

// TestNATS_CompactionFallbackAndContinue verifies that when the compact LLM
// call fails (or returns a summary that exceeds the quality gate), the loop
// falls back to extractive summary and continues correctly.
func TestNATS_CompactionFallbackAndContinue(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-compaction-fallback")

	dtInput, _ := json.Marshal(map[string]string{
		"data": `{"value":7}`,
		"path": "$.value",
	})
	tcInput, _ := json.Marshal(map[string]string{
		"result": "fallback compaction test completed",
	})

	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")

		var resp mockAPIResponse
		switch {
		case n >= 1 && n <= 11:
			resp = mockToolUse(n, "data_transform", dtInput, 1_000, 50)
		case n == 12:
			// High token count triggers compaction.
			resp = mockToolUse(n, "data_transform", dtInput, 159_950, 50)
		case n == 13:
			// compact() summarisation call: return a summary that is too large to
			// pass the 25 % quality gate (len(summary) > originalSize * 0.25).
			// This causes summariseHistory to return an error and compact() to
			// fall back to the extractive strategy — without any SDK retries.
			hugeSummary := ""
			for i := 0; i < 40; i++ {
				hugeSummary += "This sentence pads the summary past the quality gate threshold. "
			}
			resp = mockEndTurn(n, hugeSummary, 200, 80)
		case n == 14:
			resp = mockToolUse(n, "task_complete", tcInput, 1_000, 50)
		default:
			t.Errorf("unexpected LLM call #%d", n)
			http.Error(w, "unexpected call", http.StatusInternalServerError)
			return
		}

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode mock response call #%d: %v", n, err)
		}
	}))
	defer srv.Close()

	spawnCtx := &SpawnContext{
		TaskID:       "compaction-fallback-task-1",
		SkillDomain:  "data",
		TraceID:      "compaction-fallback-trace-1",
		Instructions: "Run many data transforms then complete.",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// RunLoop must complete successfully even when compact() fails and falls
	// back to the extractive summary strategy.
	result, err := RunLoop(ctx, log, spawnCtx, nil /* ve */, option.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("RunLoop returned error after compact fallback: %v", err)
	}
	if result == "" {
		t.Error("RunLoop returned empty result after compact fallback")
	}
	t.Logf("RunLoop result (fallback): %q", result)

	if total := int(callCount.Load()); total < 14 {
		t.Errorf("expected at least 14 LLM calls, got %d", total)
	}
}

// Ensure time and slog packages are used.
var _ = time.Second
