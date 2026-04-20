package main

// steering_test.go — unit tests for the Steerer (OQ-08) and the interruptible
// Act phase in RunLoop.
//
// Steerer tests construct the struct directly (bypassing NewSteerer) so no
// NATS server is required. Loop tests use the same mock Anthropic HTTP server
// pattern as compaction_nats_test.go.
//
// Run:
//
//	go test ./cmd/agent-process/ -v -run TestSteerer
//	go test ./cmd/agent-process/ -v -run TestLoop_Steering

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	nats "github.com/nats-io/nats.go"

	"github.com/cerberOS/agents-component/pkg/types"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// newTestSteerer builds a Steerer with the pending channel wired but no NATS
// connection — suitable for directly calling handle() or pre-loading directives.
func newTestSteerer(agentID string) *Steerer {
	return &Steerer{
		agentID: agentID,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		pending: make(chan types.SteeringDirective, 1),
	}
}

// wrapDirective marshals d into the wire envelope expected by Steerer.handle.
func wrapDirective(d types.SteeringDirective) []byte {
	payload, _ := json.Marshal(d)
	env := map[string]interface{}{
		"message_type":   "steering.directive",
		"correlation_id": d.DirectiveID,
		"payload":        json.RawMessage(payload),
	}
	b, _ := json.Marshal(env)
	return b
}

// ─── Steerer.handle unit tests ────────────────────────────────────────────────

func TestSteerer_DeliverDirectiveWhenChannelEmpty(t *testing.T) {
	s := newTestSteerer("agent-1")
	d := types.SteeringDirective{
		DirectiveID: "dir-1",
		AgentID:     "agent-1",
		Type:        "redirect",
		Priority:    5,
	}
	s.handle(&nats.Msg{Data: wrapDirective(d)})

	select {
	case got := <-s.pending:
		if got.DirectiveID != "dir-1" {
			t.Errorf("want directive_id %q, got %q", "dir-1", got.DirectiveID)
		}
	default:
		t.Fatal("expected directive in pending channel, got none")
	}
}

func TestSteerer_ReplaceLowerPriorityPending(t *testing.T) {
	s := newTestSteerer("agent-1")

	low := types.SteeringDirective{
		DirectiveID: "low",
		AgentID:     "agent-1",
		Type:        "redirect",
		Priority:    3,
	}
	high := types.SteeringDirective{
		DirectiveID: "high",
		AgentID:     "agent-1",
		Type:        "cancel",
		Priority:    9,
	}

	// Deliver low-priority first so it sits in the channel.
	s.handle(&nats.Msg{Data: wrapDirective(low)})
	// Deliver high-priority — it should evict the low one.
	s.handle(&nats.Msg{Data: wrapDirective(high)})

	select {
	case got := <-s.pending:
		if got.DirectiveID != "high" {
			t.Errorf("expected high-priority directive, got %q", got.DirectiveID)
		}
	default:
		t.Fatal("expected directive in pending channel, got none")
	}
}

func TestSteerer_KeepHigherPriorityPending(t *testing.T) {
	s := newTestSteerer("agent-1")

	high := types.SteeringDirective{
		DirectiveID: "high",
		AgentID:     "agent-1",
		Type:        "cancel",
		Priority:    9,
	}
	low := types.SteeringDirective{
		DirectiveID: "low",
		AgentID:     "agent-1",
		Type:        "redirect",
		Priority:    2,
	}

	s.handle(&nats.Msg{Data: wrapDirective(high)})
	s.handle(&nats.Msg{Data: wrapDirective(low)})

	select {
	case got := <-s.pending:
		if got.DirectiveID != "high" {
			t.Errorf("expected high-priority directive to be kept, got %q", got.DirectiveID)
		}
	default:
		t.Fatal("expected directive in pending channel, got none")
	}
}

func TestSteerer_RejectWrongAgentID(t *testing.T) {
	s := newTestSteerer("agent-1")
	d := types.SteeringDirective{
		DirectiveID: "dir-wrong",
		AgentID:     "agent-99", // does not match s.agentID
		Type:        "redirect",
		Priority:    5,
	}
	s.handle(&nats.Msg{Data: wrapDirective(d)})

	select {
	case got := <-s.pending:
		t.Errorf("expected channel empty, got directive %q", got.DirectiveID)
	default:
		// Correct: wrong agent_id is filtered.
	}
}

func TestSteerer_MalformedEnvelopeIsDropped(t *testing.T) {
	s := newTestSteerer("agent-1")
	s.handle(&nats.Msg{Data: []byte("not json at all")})

	select {
	case got := <-s.pending:
		t.Errorf("expected empty channel after malformed message, got %q", got.DirectiveID)
	default:
	}
}

// ─── formatSteeringMessage tests ─────────────────────────────────────────────

func TestFormatSteeringMessage_Cancel(t *testing.T) {
	d := &types.SteeringDirective{
		Type:         "cancel",
		Instructions: "operator terminated task",
	}
	msg := formatSteeringMessage(d)
	if !strings.Contains(msg, "[STEERING: task cancelled") {
		t.Errorf("cancel message missing expected prefix, got: %q", msg)
	}
	if !strings.Contains(msg, "operator terminated task") {
		t.Errorf("cancel message missing instructions, got: %q", msg)
	}
	if !strings.Contains(msg, "task_complete") {
		t.Errorf("cancel message must instruct agent to call task_complete, got: %q", msg)
	}
}

func TestFormatSteeringMessage_AbortTool(t *testing.T) {
	d := &types.SteeringDirective{
		Type:         "abort_tool",
		Instructions: "stop the fetch and refetch",
	}
	msg := formatSteeringMessage(d)
	if !strings.Contains(msg, "[STEERING: tool execution was interrupted") {
		t.Errorf("abort_tool message missing expected prefix, got: %q", msg)
	}
	if !strings.Contains(msg, "stop the fetch and refetch") {
		t.Errorf("abort_tool message missing instructions, got: %q", msg)
	}
}

func TestFormatSteeringMessage_Redirect(t *testing.T) {
	d := &types.SteeringDirective{
		Type:         "redirect",
		Instructions: "focus on the second URL instead",
	}
	msg := formatSteeringMessage(d)
	if !strings.Contains(msg, "[STEERING: updated instructions") {
		t.Errorf("redirect message missing expected prefix, got: %q", msg)
	}
	if !strings.Contains(msg, "focus on the second URL instead") {
		t.Errorf("redirect message missing instructions, got: %q", msg)
	}
}

func TestFormatSteeringMessage_InjectContext(t *testing.T) {
	d := &types.SteeringDirective{
		Type:         "inject_context",
		Instructions: "here is additional context",
	}
	msg := formatSteeringMessage(d)
	// inject_context falls through to the default case.
	if !strings.Contains(msg, "[STEERING:") {
		t.Errorf("inject_context message missing [STEERING:] prefix, got: %q", msg)
	}
}

// ─── Interruptible loop tests ─────────────────────────────────────────────────

// TestLoop_CancelDirectiveTerminatesTask verifies that a cancel directive
// pre-loaded into the steerer's pending channel causes RunLoop to return an
// error once the Act phase has completed its current tool call.
func TestLoop_CancelDirectiveTerminatesTask(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-steering-cancel")

	dtInput, _ := json.Marshal(map[string]string{"data": `{"v":1}`, "path": "$.v"})
	var callCount atomic.Int32

	// capturedMessages records the "messages" field from each Reason phase request
	// so we can verify the steering message appeared in the history.
	var capturedMessages []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if msgs, ok := body["messages"]; ok {
			capturedMessages = append(capturedMessages, string(msgs))
		}

		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")

		var resp mockAPIResponse
		switch n {
		case 1:
			// Reason phase: return a tool_use(data_transform).
			resp = mockToolUse(n, "data_transform", dtInput, 1_000, 50)
		default:
			// Should not be reached — cancel directive terminates the loop before
			// a second Reason phase can execute.
			t.Errorf("unexpected LLM call #%d; loop should have exited after cancel", n)
			http.Error(w, "unexpected call", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	steerer := newTestSteerer("test-agent-cancel")
	// Pre-load a cancel directive — the Act phase goroutine will pick it up
	// immediately (interrupt_tool: false, so the tool still runs to completion).
	steerer.pending <- types.SteeringDirective{
		DirectiveID:   "cancel-dir-1",
		AgentID:       "test-agent-cancel",
		Type:          "cancel",
		Instructions:  "test cancellation",
		Priority:      5,
		InterruptTool: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawnCtx := &SpawnContext{
		TaskID:       "steering-cancel-task",
		SkillDomain:  "data",
		TraceID:      "steering-cancel-trace",
		Instructions: "transform data and complete",
	}

	_, _, err := RunLoop(ctx, log, spawnCtx, nil, steerer, nil, nil, nil, option.WithBaseURL(srv.URL))
	if err == nil {
		t.Fatal("expected RunLoop to return error on cancel directive, got nil")
	}
	if !strings.Contains(err.Error(), "cancelled by steering directive") {
		t.Errorf("expected 'cancelled by steering directive' in error, got: %v", err)
	}
	t.Logf("RunLoop returned (expected): %v", err)
}

// TestLoop_RedirectDirectiveInjectedIntoHistory verifies that a redirect
// directive captured during the Act phase is injected as a [STEERING:] user
// message in the subsequent Reason phase.
func TestLoop_RedirectDirectiveInjectedIntoHistory(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-steering-redirect")

	dtInput, _ := json.Marshal(map[string]string{"data": `{"v":1}`, "path": "$.v"})
	tcInput, _ := json.Marshal(map[string]string{"result": "redirect test done"})
	var callCount atomic.Int32

	// steeringSeenInCall2 is set true when the second Reason call's request body
	// contains the [STEERING:] message, confirming history injection.
	var steeringSeenInCall2 atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string            `json:"role"`
				Content []json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &body)

		n := int(callCount.Add(1))
		w.Header().Set("Content-Type", "application/json")

		// Check whether call 2's request includes the [STEERING:] injection.
		if n == 2 {
			raw, _ := json.Marshal(body.Messages)
			if strings.Contains(string(raw), "[STEERING:") {
				steeringSeenInCall2.Store(true)
			}
		}

		var resp mockAPIResponse
		switch n {
		case 1:
			resp = mockToolUse(n, "data_transform", dtInput, 1_000, 50)
		case 2:
			// After steering injection the loop continues; complete the task.
			resp = mockToolUse(n, "task_complete", tcInput, 1_000, 50)
		default:
			t.Errorf("unexpected LLM call #%d", n)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	steerer := newTestSteerer("test-agent-redirect")
	steerer.pending <- types.SteeringDirective{
		DirectiveID:   "redirect-dir-1",
		AgentID:       "test-agent-redirect",
		Type:          "redirect",
		Instructions:  "focus on result set B instead",
		Priority:      5,
		InterruptTool: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawnCtx := &SpawnContext{
		TaskID:       "steering-redirect-task",
		SkillDomain:  "data",
		TraceID:      "steering-redirect-trace",
		Instructions: "transform data and complete",
	}

	result, _, err := RunLoop(ctx, log, spawnCtx, nil, steerer, nil, nil, nil, option.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("RunLoop returned unexpected error: %v", err)
	}
	if result == "" {
		t.Error("RunLoop returned empty result")
	}
	if !steeringSeenInCall2.Load() {
		t.Error("expected [STEERING:] message to appear in history for call 2, but it was absent")
	}
	t.Logf("RunLoop result: %q (steering injected: %v)", result, steeringSeenInCall2.Load())
}
