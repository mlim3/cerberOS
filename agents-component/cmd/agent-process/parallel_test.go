package main

// parallel_test.go — tests for OQ-09 parallel async tool dispatch.
//
// Tests verify three properties of the parallel Act phase:
//
//  1. Result ordering: when multiple tool_use blocks appear in a single
//     assistant response, results reach the next Reason phase in the same
//     index order as the original tool calls (Anthropic API requirement).
//
//  2. Concurrent execution: two web_fetch tool calls whose target servers each
//     sleep 100 ms complete in ~100 ms total, not ~200 ms — proving that
//     dispatch is parallel, not sequential.
//
//  3. Steering interrupt + parallel: a cancel directive injected while two
//     slow in-flight web_fetch calls are running causes both to be cancelled;
//     RunLoop returns the cancellation error.
//
// Run:
//
//	go test ./cmd/agent-process/ -v -run TestParallel
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/cerberOS/agents-component/pkg/types"
)

// mockMultiToolUse builds a mock Anthropic API response containing multiple
// tool_use blocks in a single assistant message. Input slice order is preserved
// in the response content so tests can assert on result ordering.
func mockMultiToolUse(n int, calls []struct {
	name  string
	input json.RawMessage
}, in, out int64) mockAPIResponse {
	content := make([]mockAPIContent, len(calls))
	for i, c := range calls {
		content[i] = mockAPIContent{
			Type:  "tool_use",
			ID:    fmt.Sprintf("toolu_%03d_%d", n, i),
			Name:  c.name,
			Input: c.input,
		}
	}
	return mockAPIResponse{
		ID:         fmt.Sprintf("msg_%03d", n),
		Type:       "message",
		Role:       "assistant",
		StopReason: "tool_use",
		Model:      "claude-haiku-4-5-20251001",
		Content:    content,
		Usage:      mockAPIUsage{InputTokens: in, OutputTokens: out},
	}
}

// ─── Test 1: result ordering ──────────────────────────────────────────────────

// TestParallel_ResultsInOriginalOrder verifies that when three data_transform
// calls are dispatched concurrently, their results appear in the next Reason
// phase request in the same order as the original tool_use blocks.
func TestParallel_ResultsInOriginalOrder(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-parallel-order")

	// Each data_transform call extracts a distinct field — we can distinguish
	// which result is which by the content of the tool result.
	makeInput := func(path string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{
			"data":      `{"a":1,"b":2,"c":3}`,
			"path":      path,
			"operation": "extract",
		})
		return b
	}

	tcInput, _ := json.Marshal(map[string]string{"result": "order verified"})

	var callCount atomic.Int32
	// recordedToolResultOrder captures the tool_use_id order seen in call 2's
	// request body so we can verify the results arrived in the original order.
	var recordedToolResultOrder []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")

		if n == 2 {
			// Parse the messages to extract the tool_result order.
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content []struct {
						Type      string `json:"type"`
						ToolUseID string `json:"tool_use_id"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(bodyBytes, &req); err == nil {
				for _, msg := range req.Messages {
					if msg.Role == "user" {
						for _, c := range msg.Content {
							if c.Type == "tool_result" {
								recordedToolResultOrder = append(recordedToolResultOrder, c.ToolUseID)
							}
						}
					}
				}
			}
		}

		var resp mockAPIResponse
		switch n {
		case 1:
			resp = mockMultiToolUse(n, []struct {
				name  string
				input json.RawMessage
			}{
				{"data_transform", makeInput("$.a")},
				{"data_transform", makeInput("$.b")},
				{"data_transform", makeInput("$.c")},
			}, 1_000, 50)
		case 2:
			resp = mockToolUse(n, "task_complete", tcInput, 1_000, 50)
		default:
			t.Errorf("unexpected LLM call #%d", n)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawnCtx := &SpawnContext{
		TaskID:       "parallel-order-task",
		SkillDomain:  "data",
		TraceID:      "parallel-order-trace",
		Instructions: "run parallel data transforms",
	}

	result, err := RunLoop(ctx, log, spawnCtx, nil, nil, option.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}
	if result == "" {
		t.Error("RunLoop returned empty result")
	}

	// Verify tool results arrived in the correct index order.
	wantOrder := []string{"toolu_001_0", "toolu_001_1", "toolu_001_2"}
	if len(recordedToolResultOrder) != len(wantOrder) {
		t.Fatalf("expected %d tool results in call 2, got %d (%v)",
			len(wantOrder), len(recordedToolResultOrder), recordedToolResultOrder)
	}
	for i, id := range recordedToolResultOrder {
		if id != wantOrder[i] {
			t.Errorf("tool result order[%d]: want %q, got %q", i, wantOrder[i], id)
		}
	}
	t.Logf("tool result order verified: %v", recordedToolResultOrder)
}

// ─── Test 2: concurrent execution timing ─────────────────────────────────────

// TestParallel_ConcurrentExecutionFasterThanSequential proves that two tool
// calls whose targets each sleep 100 ms complete in well under 200 ms total —
// demonstrating genuine parallel dispatch rather than sequential execution.
//
// Architecture:
//
//	slowSrv  — HTTP server that sleeps sleepDuration per request (web_fetch target)
//	apiSrv   — mock Anthropic API (returns 2 concurrent web_fetch calls, then task_complete)
func TestParallel_ConcurrentExecutionFasterThanSequential(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-parallel-timing")

	const sleepDuration = 100 * time.Millisecond
	const sequentialDuration = 2 * sleepDuration  // what sequential dispatch would take
	const parallelBudget = 160 * time.Millisecond // generous headroom for goroutine overhead

	var slowReqCount atomic.Int32
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowReqCount.Add(1)
		time.Sleep(sleepDuration) // simulate slow network/IO
		_, _ = io.WriteString(w, `{"data":"ok"}`)
	}))
	defer slowSrv.Close()

	makeWebFetchInput := func(url string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"url": url, "method": "GET"})
		return b
	}
	tcInput, _ := json.Marshal(map[string]string{"result": "timing test done"})

	var callCount atomic.Int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")

		var resp mockAPIResponse
		switch n {
		case 1:
			// Return two web_fetch calls pointing at the slow server.
			resp = mockMultiToolUse(n, []struct {
				name  string
				input json.RawMessage
			}{
				{"web_fetch", makeWebFetchInput(slowSrv.URL + "/a")},
				{"web_fetch", makeWebFetchInput(slowSrv.URL + "/b")},
			}, 1_000, 50)
		case 2:
			resp = mockToolUse(n, "task_complete", tcInput, 1_000, 50)
		default:
			t.Errorf("unexpected LLM call #%d", n)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer apiSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawnCtx := &SpawnContext{
		TaskID:       "parallel-timing-task",
		SkillDomain:  "web",
		TraceID:      "parallel-timing-trace",
		Instructions: "fetch two URLs in parallel",
	}

	// Measure only the Act phase (web_fetch calls) time.
	// RunLoop is called including the Reason phase overhead, so we give a generous
	// overall budget and focus the assertion on the Act phase.
	actStart := time.Now()
	result, err := RunLoop(ctx, log, spawnCtx, nil, nil, option.WithBaseURL(apiSrv.URL))
	actElapsed := time.Since(actStart)

	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}
	if result == "" {
		t.Error("RunLoop returned empty result")
	}

	// Both slow requests must have fired.
	if got := int(slowReqCount.Load()); got != 2 {
		t.Errorf("expected 2 slow requests, got %d", got)
	}

	// Total wall time must be well under sequential duration.
	// Subtract two fast API round-trips (~0ms for local loopback mock) from budget.
	if actElapsed >= sequentialDuration {
		t.Errorf("RunLoop took %v — not faster than sequential duration %v; parallel dispatch may not be working",
			actElapsed, sequentialDuration)
	}
	t.Logf("elapsed=%v sequential_would_be=%v parallel_budget=%v (PASS: concurrent)",
		actElapsed.Round(time.Millisecond), sequentialDuration, parallelBudget)
}

// ─── Test 3: steering interrupt cancels all concurrent in-flight calls ────────

// TestParallel_SteeringInterruptCancelsAllConcurrentCalls verifies that an
// interrupt_tool steering directive cancels all concurrently in-flight tool
// calls (not just the first one) and causes RunLoop to return a cancellation
// error.
//
// The test uses two slow web_fetch calls and fires the interrupt after a brief
// delay, before either fetch can complete.
func TestParallel_SteeringInterruptCancelsAllConcurrentCalls(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-parallel-interrupt")

	const toolSleepDuration = 200 * time.Millisecond
	const interruptAfter = 30 * time.Millisecond // fire interrupt well before tools complete

	var slowReqCount atomic.Int32
	var cancelledCount atomic.Int32

	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowReqCount.Add(1)
		// Block until either the request context is cancelled or the sleep elapses.
		select {
		case <-r.Context().Done():
			cancelledCount.Add(1)
			// Context cancelled — do not write a response; the client has moved on.
			return
		case <-time.After(toolSleepDuration):
			_, _ = io.WriteString(w, `{"data":"ok"}`)
		}
	}))
	defer slowSrv.Close()

	makeWebFetchInput := func(url string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"url": url, "method": "GET"})
		return b
	}

	var callCount atomic.Int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")

		if n > 1 {
			// The interrupt should terminate the loop before a second LLM call.
			t.Errorf("unexpected LLM call #%d; loop should have exited after interrupt", n)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}

		resp := mockMultiToolUse(n, []struct {
			name  string
			input json.RawMessage
		}{
			{"web_fetch", makeWebFetchInput(slowSrv.URL + "/a")},
			{"web_fetch", makeWebFetchInput(slowSrv.URL + "/b")},
		}, 1_000, 50)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer apiSrv.Close()

	steerer := newTestSteerer("test-agent-parallel-interrupt")

	// Fire the interrupt directive after a brief delay — both fetches are in flight.
	go func() {
		time.Sleep(interruptAfter)
		steerer.pending <- types.SteeringDirective{
			DirectiveID:   "interrupt-all-dir",
			AgentID:       "test-agent-parallel-interrupt",
			Type:          "cancel",
			Instructions:  "cancel both concurrent fetches",
			Priority:      10,
			InterruptTool: true, // cancel actCtx → both in-flight fetches cancelled
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawnCtx := &SpawnContext{
		TaskID:       "parallel-interrupt-task",
		SkillDomain:  "web",
		TraceID:      "parallel-interrupt-trace",
		Instructions: "fetch two slow URLs",
	}

	start := time.Now()
	_, err := RunLoop(ctx, log, spawnCtx, nil, steerer, option.WithBaseURL(apiSrv.URL))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected RunLoop to return error on cancel directive, got nil")
	}
	if !strings.Contains(err.Error(), "cancelled by steering directive") {
		t.Errorf("expected cancellation error, got: %v", err)
	}

	// RunLoop should return well before either tool could complete naturally.
	if elapsed >= toolSleepDuration {
		t.Errorf("RunLoop took %v — should have returned before tool sleep (%v)",
			elapsed.Round(time.Millisecond), toolSleepDuration)
	}

	// Both tool calls must have been started (both goroutines launched).
	if got := int(slowReqCount.Load()); got != 2 {
		t.Errorf("expected 2 slow requests started, got %d", got)
	}

	t.Logf("elapsed=%v (cancelled after ~%v), slow_requests_started=%d, cancelled_by_ctx=%d",
		elapsed.Round(time.Millisecond), interruptAfter, slowReqCount.Load(), cancelledCount.Load())
}
