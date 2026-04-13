package main

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

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// ─── shouldSynthesize ─────────────────────────────────────────────────────────

func TestShouldSynthesize_BelowThreshold(t *testing.T) {
	if shouldSynthesize("web", skillSynthesisThreshold-1) {
		t.Errorf("tool count %d (< threshold %d) must not trigger synthesis",
			skillSynthesisThreshold-1, skillSynthesisThreshold)
	}
}

func TestShouldSynthesize_AtThreshold(t *testing.T) {
	if !shouldSynthesize("web", skillSynthesisThreshold) {
		t.Errorf("tool count == threshold (%d) must trigger synthesis", skillSynthesisThreshold)
	}
}

func TestShouldSynthesize_AboveThreshold(t *testing.T) {
	if !shouldSynthesize("web", skillSynthesisThreshold+10) {
		t.Error("tool count well above threshold must trigger synthesis")
	}
}

func TestShouldSynthesize_GeneralDomain_NeverTriggers(t *testing.T) {
	if shouldSynthesize("general", 100) {
		t.Error("'general' domain must never trigger synthesis regardless of tool count")
	}
}

func TestShouldSynthesize_GeneralDomain_AtThreshold(t *testing.T) {
	if shouldSynthesize("general", skillSynthesisThreshold) {
		t.Error("'general' domain at threshold must still return false")
	}
}

func TestShouldSynthesize_ZeroToolCalls(t *testing.T) {
	if shouldSynthesize("web", 0) {
		t.Error("0 tool calls must not trigger synthesis")
	}
}

// ─── skillSynthesisSystemPrompt ───────────────────────────────────────────────

func TestSkillSynthesisSystemPrompt_ContainsDomain(t *testing.T) {
	prompt := skillSynthesisSystemPrompt("storage")
	if !strings.Contains(prompt, "storage") {
		t.Errorf("system prompt must include the domain name; got %q", prompt)
	}
}

func TestSkillSynthesisSystemPrompt_ContainsJSONSchema(t *testing.T) {
	prompt := skillSynthesisSystemPrompt("web")
	for _, field := range []string{`"name"`, `"label"`, `"description"`, `"spec"`} {
		if !strings.Contains(prompt, field) {
			t.Errorf("system prompt must include JSON schema field %s", field)
		}
	}
}

func TestSkillSynthesisSystemPrompt_ContainsNegativeGuidance(t *testing.T) {
	prompt := skillSynthesisSystemPrompt("web")
	if !strings.Contains(prompt, "Do NOT") {
		t.Error("system prompt must instruct LLM to include negative guidance")
	}
}

func TestSkillSynthesisSystemPrompt_ContainsFallbackInstruction(t *testing.T) {
	prompt := skillSynthesisSystemPrompt("web")
	// The fallback for "no reusable procedure" is signaled by empty name.
	if !strings.Contains(prompt, `{"name":"","label":"","description":""}`) {
		t.Error("system prompt must include the empty-name fallback JSON example")
	}
}

func TestSkillSynthesisSystemPrompt_DifferentDomains(t *testing.T) {
	p1 := skillSynthesisSystemPrompt("web")
	p2 := skillSynthesisSystemPrompt("data")
	if p1 == p2 {
		t.Error("prompts for different domains must differ")
	}
}

// ─── synthesizeSkill — mock LLM server ────────────────────────────────────────

// validSkillJSON is a well-formed synthesized skill JSON that passes the Tool Contract.
const validSkillJSON = `{
  "name": "web_paginate",
  "label": "Paginate API",
  "description": "Fetches all pages of a paginated REST API endpoint. Do NOT use for single-page fetches or APIs that do not support cursor/page-number pagination.",
  "spec": {
    "parameters": {
      "url":        {"type": "string",  "required": true,  "description": "Base API endpoint URL."},
      "page_param": {"type": "string",  "required": false, "description": "Query parameter name for page number, e.g. 'page'."},
      "max_pages":  {"type": "integer", "required": false, "description": "Maximum pages to fetch; 0 means unlimited."}
    }
  }
}`

func synthServer(t *testing.T, responses ...mockAPIResponse) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := int(callCount.Add(1)) - 1 // 0-based index
		w.Header().Set("Content-Type", "application/json")
		if n >= len(responses) {
			t.Errorf("unexpected LLM call #%d — only %d responses programmed", n+1, len(responses))
			http.Error(w, "unexpected call", http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(responses[n]); err != nil {
			t.Errorf("encode mock response: %v", err)
		}
	}))
	return srv, &callCount
}

func newSynthClient(t *testing.T, srv *httptest.Server) *anthropic.Client {
	t.Helper()
	c := anthropic.NewClient(
		option.WithAPIKey("sk-ant-test-synth"),
		option.WithBaseURL(srv.URL),
	)
	return &c
}

func TestSynthesizeSkill_ValidResponse(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	srv, _ := synthServer(t, mockEndTurn(1, validSkillJSON, 100, 80))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	node, err := synthesizeSkill(context.Background(), client, log, "web", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node == nil {
		t.Fatal("expected a synthesized SkillNode, got nil")
	}
	if node.Name != "web_paginate" {
		t.Errorf("Name: want 'web_paginate', got %q", node.Name)
	}
	if node.Origin != "synthesized" {
		t.Errorf("Origin: want 'synthesized', got %q", node.Origin)
	}
	if node.SynthesizedAt == nil {
		t.Error("SynthesizedAt must be set on a synthesized node")
	}
	if node.Level != "command" {
		t.Errorf("Level: want 'command', got %q", node.Level)
	}
	if node.Spec == nil {
		t.Error("Spec must be populated from the LLM JSON response")
	}
}

func TestSynthesizeSkill_EmptyNameReturnsNil(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	srv, _ := synthServer(t, mockEndTurn(1, `{"name":"","label":"","description":""}`, 100, 20))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	node, err := synthesizeSkill(context.Background(), client, log, "web", nil)
	if err != nil {
		t.Fatalf("unexpected error for no-procedure signal: %v", err)
	}
	if node != nil {
		t.Errorf("empty-name response must return nil node; got %+v", node)
	}
}

func TestSynthesizeSkill_MarkdownFenceStripped(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	fenced := "```json\n" + validSkillJSON + "\n```"
	srv, _ := synthServer(t, mockEndTurn(1, fenced, 100, 80))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	node, err := synthesizeSkill(context.Background(), client, log, "web", nil)
	if err != nil {
		t.Fatalf("markdown-fenced JSON must parse correctly, got error: %v", err)
	}
	if node == nil || node.Name != "web_paginate" {
		t.Errorf("fenced response: expected node 'web_paginate', got %v", node)
	}
}

func TestSynthesizeSkill_PlainFenceStripped(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	fenced := "```\n" + validSkillJSON + "\n```"
	srv, _ := synthServer(t, mockEndTurn(1, fenced, 100, 80))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	node, err := synthesizeSkill(context.Background(), client, log, "web", nil)
	if err != nil {
		t.Fatalf("plain-fenced JSON must parse correctly, got error: %v", err)
	}
	if node == nil {
		t.Error("plain-fenced response: expected non-nil node")
	}
}

func TestSynthesizeSkill_InvalidJSON_ReturnsError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	srv, _ := synthServer(t, mockEndTurn(1, "this is not json at all", 100, 20))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	node, err := synthesizeSkill(context.Background(), client, log, "web", nil)
	if err == nil {
		t.Errorf("invalid JSON must return an error; got node=%v", node)
	}
	if node != nil {
		t.Error("on parse error, node must be nil")
	}
}

func TestSynthesizeSkill_ToolContractViolation_ReturnsError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	// Missing label and missing description — fails ValidateCommandContract.
	badSkill := `{"name":"web_paginate","label":"","description":"","spec":{"parameters":{}}}`
	srv, _ := synthServer(t, mockEndTurn(1, badSkill, 100, 30))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	node, err := synthesizeSkill(context.Background(), client, log, "web", nil)
	if err == nil {
		t.Errorf("Tool Contract violation must return an error; got node=%v", node)
	}
	if !strings.Contains(err.Error(), "Tool Contract") {
		t.Errorf("error must mention 'Tool Contract'; got %q", err.Error())
	}
}

func TestSynthesizeSkill_SynthesizedAtIsRecent(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	srv, _ := synthServer(t, mockEndTurn(1, validSkillJSON, 100, 80))
	defer srv.Close()

	before := time.Now().UTC()
	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	node, err := synthesizeSkill(context.Background(), client, log, "web", nil)
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node == nil || node.SynthesizedAt == nil {
		t.Fatal("expected non-nil node with SynthesizedAt")
	}
	if node.SynthesizedAt.Before(before) || node.SynthesizedAt.After(after) {
		t.Errorf("SynthesizedAt %v not within [%v, %v]", node.SynthesizedAt, before, after)
	}
}

// ─── attemptSkillSynthesis — guard conditions ─────────────────────────────────

func TestAttemptSkillSynthesis_NilSessionLog_NoLLMCall(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawnCtx := &SpawnContext{SkillDomain: "web"}

	// nil sl → should return immediately without calling the LLM.
	attemptSkillSynthesis(context.Background(), client, log, spawnCtx, nil, nil, skillSynthesisThreshold+1)

	if n := callCount.Load(); n != 0 {
		t.Errorf("nil SessionLog: expected 0 LLM calls, got %d", n)
	}
}

func TestAttemptSkillSynthesis_BelowThreshold_NoLLMCall(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawnCtx := &SpawnContext{SkillDomain: "web"}
	// Non-nil sl but below threshold.
	sl := &SessionLog{log: log}

	attemptSkillSynthesis(context.Background(), client, log, spawnCtx, sl, nil, skillSynthesisThreshold-1)

	if n := callCount.Load(); n != 0 {
		t.Errorf("below threshold: expected 0 LLM calls, got %d", n)
	}
}

func TestAttemptSkillSynthesis_GeneralDomain_NoLLMCall(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawnCtx := &SpawnContext{SkillDomain: "general"}
	sl := &SessionLog{log: log}

	attemptSkillSynthesis(context.Background(), client, log, spawnCtx, sl, nil, 100)

	if n := callCount.Load(); n != 0 {
		t.Errorf("general domain: expected 0 LLM calls, got %d", n)
	}
}

func TestAttemptSkillSynthesis_LLMFailure_NoTaskFailure(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		// Return HTTP 500 — simulates LLM unreachable.
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawnCtx := &SpawnContext{SkillDomain: "web"}
	sl := &SessionLog{log: log}

	// Must not panic; LLM failure is logged and discarded.
	attemptSkillSynthesis(context.Background(), client, log, spawnCtx, sl, nil, skillSynthesisThreshold)

	if n := callCount.Load(); n == 0 {
		t.Error("expected at least one LLM call attempt")
	}
}

func TestAttemptSkillSynthesis_EmptyNameFromLLM_NoPersist(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-synth")
	srv, callCount := synthServer(t, mockEndTurn(1, `{"name":"","label":"","description":""}`, 100, 20))
	defer srv.Close()

	client := newSynthClient(t, srv)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	spawnCtx := &SpawnContext{SkillDomain: "web"}
	sl := &SessionLog{log: log}

	// Should call LLM (1 call) but not try to persist (node is nil).
	// If it tries to persist with nil js it would panic — absence of panic proves the nil guard.
	attemptSkillSynthesis(context.Background(), client, log, spawnCtx, sl, nil, skillSynthesisThreshold)

	if n := callCount.Load(); n != 1 {
		t.Errorf("expected exactly 1 LLM call, got %d", n)
	}
}
