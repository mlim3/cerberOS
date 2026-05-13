// Package main — tools_skills_search_cross_domain_test.go
//
// Unit tests for cross-domain skill access behavior in skills_search.
//
// These tests cover two behaviors:
//
//  1. Credential-free tool auto-registration: skills_search finds a static,
//     requires_cred=false skill outside the agent's domain and auto-registers
//     it into DynamicRegistry using builtinRegistry[result.Implementation].
//     The agent is told it can call the tool directly. No spawn_agent, no
//     clarification.
//
//  2. Credentialed tool user-gating: skills_search finds a requires_cred=true
//     skill outside the agent's scope and calls ClarificationSender.
//     If approved → recommend spawn_agent.
//     If denied → explain gracefully, do not register the tool.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cerberOS/agents-component/pkg/types"
)

// ─── Additional stub types ────────────────────────────────────────────────────

// fakeSearcherWithMeta extends fakeSearcher with RequiresCred / Implementation
// / Origin populated so the auto-registration and clarification paths can be
// exercised without a live NATS connection.
type fakeSearcherWithMeta struct {
	results []types.SkillSearchResult
}

func (f *fakeSearcherWithMeta) SearchSkills(_ string, topK int) []types.SkillSearchResult {
	if topK > 0 && topK < len(f.results) {
		return f.results[:topK]
	}
	return f.results
}

// fakeClarificationSender captures calls to SendClarification so tests can
// assert on what was sent and control what is returned.
type fakeClarificationSender struct {
	sent     []types.ClarificationRequest
	response types.ClarificationResponse
	err      error
}

func (f *fakeClarificationSender) SendClarification(req types.ClarificationRequest) (types.ClarificationResponse, error) {
	f.sent = append(f.sent, req)
	return f.response, f.err
}

// minimalCredFreeResult returns a SkillSearchResult for a static, credential-free
// skill outside the querying agent's domain.
func minimalCredFreeResult(domain, name, impl string) types.SkillSearchResult {
	return types.SkillSearchResult{
		Domain:         domain,
		Name:           name,
		Description:    "Test credential-free skill in " + domain,
		Score:          0.92,
		RequiresCred:   false,
		Implementation: impl,
		Origin:         "static",
	}
}

// minimalCredResult returns a SkillSearchResult for a credentialed skill outside
// the querying agent's domain.
func minimalCredResult(domain, name string) types.SkillSearchResult {
	return types.SkillSearchResult{
		Domain:         domain,
		Name:           name,
		Description:    "Test credentialed skill in " + domain,
		Score:          0.91,
		RequiresCred:   true,
		Implementation: "vault_" + name,
		Origin:         "static",
	}
}

// testWebFetchTool is a helper that returns the web_fetch builtin for use in
// seeding a DynamicRegistry in tests. Named to avoid collision with the
// production webFetchTool constructor in tools.go.
func testWebFetchTool() SkillTool {
	factory := builtinRegistry["web_fetch"]
	return factory(nil)
}

// ─── DynamicRegistry.Replace unit tests ──────────────────────────────────────

func TestDynamicRegistry_Replace_SwapsExistingTool(t *testing.T) {
	original := testWebFetchTool()
	registry := newDynamicRegistry([]SkillTool{original})

	// Create a replacement tool with the same name but different label.
	replacement := testWebFetchTool()
	replacement.Label = "Replaced"

	if err := registry.Replace("web_fetch", replacement); err != nil {
		t.Fatalf("Replace: unexpected error: %v", err)
	}

	tools := registry.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool after replace, got %d", len(tools))
	}
	if tools[0].Label != "Replaced" {
		t.Errorf("expected Label=Replaced after replace, got %q", tools[0].Label)
	}
}

func TestDynamicRegistry_Replace_ErrorWhenNotFound(t *testing.T) {
	registry := newDynamicRegistry(nil)
	err := registry.Replace("nonexistent_tool", testWebFetchTool())
	if err == nil {
		t.Error("expected error when replacing a tool not in registry, got nil")
	}
}

func TestDynamicRegistry_Replace_DoesNotAppend(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	_ = registry.Replace("web_fetch", testWebFetchTool())
	if len(registry.Tools()) != 1 {
		t.Errorf("Replace should not append; expected 1 tool, got %d", len(registry.Tools()))
	}
}

// ─── Credential-free auto-registration ───────────────────────────────────────

// TestSkillsSearch_CredFreeOutOfDomain_AutoRegisters verifies that when
// skills_search returns a credential-free static skill in a domain the agent
// was not provisioned for, the tool is immediately registered into the
// DynamicRegistry and the agent is told it can call it directly.
func TestSkillsSearch_CredFreeOutOfDomain_AutoRegisters(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	cs := &fakeClarificationSender{}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			// e2e_ping is in builtinRegistry, credential-free, outside "web" domain.
			minimalCredFreeResult("e2e_test", "e2e_ping", "e2e_ping"),
		},
	}

	tool := skillsSearchTool(searcher, "web", false, registry, cs)
	raw, _ := json.Marshal(map[string]interface{}{"query": "automated e2e connectivity probe"})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}

	// e2e_ping must now be in the registry — callable without spawn_agent.
	found := false
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "e2e_ping" {
			found = true
			break
		}
	}
	if !found {
		t.Error("e2e_ping was not auto-registered after discovery via skills_search")
	}

	// Response must tell the agent it can call the tool directly.
	if !strings.Contains(result.Content, "e2e_ping") {
		t.Errorf("expected response to mention e2e_ping; got: %q", result.Content)
	}
	if strings.Contains(result.Content, "spawn_agent") {
		t.Errorf("response should NOT recommend spawn_agent for a credential-free tool; got: %q", result.Content)
	}

	// No clarification should have been sent.
	if len(cs.sent) != 0 {
		t.Errorf("expected no clarification request for credential-free tool; got %d", len(cs.sent))
	}

	// recommended_action must be "call_directly".
	if result.Details["recommended_action"] != "call_directly" {
		t.Errorf("expected recommended_action=call_directly, got %v", result.Details["recommended_action"])
	}

	// The auto-registered tool must be dispatchable — not just present in the slice.
	// A broken Execute closure would be silent until the agent actually calls it.
	pingRaw, _ := json.Marshal(map[string]interface{}{"probe": "auto-register-test"})
	dispResult := dispatchTool(context.Background(), registry.Tools(), "e2e_ping", pingRaw)
	if dispResult.IsError {
		t.Errorf("auto-registered e2e_ping returned error when dispatched: %s", dispResult.Content)
	}
	if !strings.Contains(dispResult.Content, "OK") {
		t.Errorf("e2e_ping dispatch: expected OK in content; got %q", dispResult.Content)
	}
}

// TestSkillsSearch_CredFreeOutOfDomain_AutoRegistersWhenImplementationMissing
// verifies that a static credential-free hit can still be auto-registered when
// the Memory payload omits the implementation field, as long as the tool name
// itself matches a builtinRegistry key.
func TestSkillsSearch_CredFreeOutOfDomain_AutoRegistersWhenImplementationMissing(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	cs := &fakeClarificationSender{}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			{
				Domain:       "e2e_test",
				Name:         "e2e_ping",
				Description:  "Test credential-free skill in e2e_test",
				Score:        0.92,
				RequiresCred: false,
				Origin:       "static",
				// Implementation intentionally omitted to mirror the live-memory
				// failure mode that triggered fallback behavior.
			},
		},
	}

	tool := skillsSearchTool(searcher, "general", true, registry, cs)
	raw, _ := json.Marshal(map[string]interface{}{"query": "automated e2e connectivity probe"})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Details["recommended_action"] != "call_directly" {
		t.Fatalf("expected recommended_action=call_directly, got %v", result.Details["recommended_action"])
	}
	if !strings.Contains(result.Content, "e2e_ping") {
		t.Fatalf("expected response to mention e2e_ping, got %q", result.Content)
	}
	if strings.Contains(result.Content, "spawn_agent") {
		t.Fatalf("response should not recommend spawn_agent when builtin fallback is available; got %q", result.Content)
	}

	found := false
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "e2e_ping" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("e2e_ping was not auto-registered when implementation was omitted")
	}
}

// TestSkillsSearch_CredFreeOutOfDomain_UnknownImpl treats a credential-free
// result whose Implementation is not in builtinRegistry as unregisterable.
// The tool should fall back to recommending spawn_agent (if available),
// not panic or hard-error.
func TestSkillsSearch_CredFreeOutOfDomain_UnknownImpl(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	cs := &fakeClarificationSender{}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			{
				Domain:         "unknown_domain",
				Name:           "mystery_tool",
				Description:    "Some tool not in builtinRegistry",
				Score:          0.88,
				RequiresCred:   false,
				Implementation: "mystery_tool_impl_not_in_registry",
				Origin:         "static",
			},
		},
	}

	tool := skillsSearchTool(searcher, "web", true, registry, cs)
	raw, _ := json.Marshal(map[string]interface{}{"query": "do some mysterious thing"})
	result := tool.Execute(nil, raw)

	// Must not error; mystery_tool must not appear in registry.
	if result.IsError {
		t.Fatalf("unexpected error for unknown impl: %s", result.Content)
	}
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "mystery_tool" {
			t.Error("unknown-implementation tool should not be registered")
		}
	}
	// Auto-registration failed, so the agent must be directed to spawn_agent instead.
	if result.Details["recommended_action"] != "spawn_agent" {
		t.Errorf("expected spawn_agent fallback when impl unknown; got %v", result.Details["recommended_action"])
	}
	if !strings.Contains(result.Content, "spawn_agent") {
		t.Errorf("expected spawn_agent in content when impl unknown; got %q", result.Content)
	}
}

// TestSkillsSearch_CredFreeOutOfDomain_SynthesizedSkipped verifies that
// synthesized skills (Origin="synthesized") are NOT auto-registered, even when
// requires_cred=false. Synthesized skills use LLM-recipe execution and are
// unreliable; only static builtins are safe to auto-register.
func TestSkillsSearch_CredFreeOutOfDomain_SynthesizedSkipped(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	cs := &fakeClarificationSender{}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			{
				Domain:         "e2e_test",
				Name:           "execute_e2e_connectivity_probe",
				Description:    "LLM recipe that calls e2e_ping",
				Score:          0.95,
				RequiresCred:   false,
				Implementation: "", // synthesized — no builtinRegistry key
				Origin:         "synthesized",
			},
		},
	}

	tool := skillsSearchTool(searcher, "web", true, registry, cs)
	raw, _ := json.Marshal(map[string]interface{}{"query": "automated e2e connectivity probe"})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "execute_e2e_connectivity_probe" {
			t.Error("synthesized skill should not be auto-registered")
		}
	}
	// Synthesized skills have no builtinRegistry entry, so the agent falls back to spawn_agent.
	if result.Details["recommended_action"] != "spawn_agent" {
		t.Errorf("expected spawn_agent fallback for synthesized skill; got %v", result.Details["recommended_action"])
	}
	if !strings.Contains(result.Content, "spawn_agent") {
		t.Errorf("expected spawn_agent in content for synthesized skill; got %q", result.Content)
	}
}

// TestSkillsSearch_CredFreeOutOfDomain_VaultToolSkipped verifies that even if
// a skill is stored with requires_cred=false (incorrect metadata), the safety
// check in tryAutoRegister catches vault tools via RequiredCredentialTypes
// and refuses to register them.
func TestSkillsSearch_CredFreeOutOfDomain_VaultToolSkipped(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	cs := &fakeClarificationSender{}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			{
				// vault_web_fetch requires credentials but is incorrectly tagged.
				Domain:         "web",
				Name:           "vault_web_fetch",
				Description:    "Vault-backed web fetch",
				Score:          0.9,
				RequiresCred:   false, // wrong metadata, but factory should catch it
				Implementation: "vault_web_fetch",
				Origin:         "static",
			},
		},
	}

	tool := skillsSearchTool(searcher, "general", true, registry, cs)
	raw, _ := json.Marshal(map[string]interface{}{"query": "fetch with vault credentials"})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}

	// vault_web_fetch must not be auto-registered — it has RequiredCredentialTypes set.
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "vault_web_fetch" {
			t.Error("vault tool must not be auto-registered even if stored requires_cred=false")
		}
	}
	// After the safety check rejects the tool, the agent falls back to spawn_agent
	// (RequiresCred=false in metadata means the clarification path is also skipped).
	if result.Details["recommended_action"] != "spawn_agent" {
		t.Errorf("expected spawn_agent fallback after vault safety check; got %v", result.Details["recommended_action"])
	}
	if !strings.Contains(result.Content, "spawn_agent") {
		t.Errorf("expected spawn_agent in content after vault safety check; got %q", result.Content)
	}
}

// TestSkillsSearch_CredFreeOutOfDomain_NilRegistry_FallsBack verifies that
// when registry is nil, the tool falls back to the legacy spawn_agent
// recommendation rather than panicking.
func TestSkillsSearch_CredFreeOutOfDomain_NilRegistry_FallsBack(t *testing.T) {
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			minimalCredFreeResult("e2e_test", "e2e_ping", "e2e_ping"),
		},
	}

	// No registry, no cs — nil safe.
	tool := skillsSearchTool(searcher, "web", true, nil, nil)
	raw, _ := json.Marshal(map[string]interface{}{"query": "automated probe"})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Fatalf("unexpected error with nil registry: %s", result.Content)
	}
	// Without registry, should fall back to spawn_agent.
	if result.Details["recommended_action"] != "spawn_agent" {
		t.Errorf("expected spawn_agent fallback with nil registry, got %v", result.Details["recommended_action"])
	}
}

// TestSkillsSearch_CredFreeOutOfDomain_AlreadyRegistered verifies that calling
// skills_search twice for the same credential-free tool does not panic or error
// (Register returns duplicate error which tryAutoRegister treats as "already registered").
func TestSkillsSearch_CredFreeOutOfDomain_AlreadyRegistered(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	cs := &fakeClarificationSender{}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			minimalCredFreeResult("e2e_test", "e2e_ping", "e2e_ping"),
		},
	}

	tool := skillsSearchTool(searcher, "web", false, registry, cs)
	raw, _ := json.Marshal(map[string]interface{}{"query": "probe"})

	// First call — registers.
	r1 := tool.Execute(nil, raw)
	if r1.IsError {
		t.Fatalf("first call: unexpected error: %s", r1.Content)
	}

	// Second call — duplicate, must not error.
	r2 := tool.Execute(nil, raw)
	if r2.IsError {
		t.Fatalf("second call (duplicate): unexpected error: %s", r2.Content)
	}

	// Registry still contains exactly one e2e_ping entry.
	count := 0
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "e2e_ping" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 e2e_ping in registry, got %d", count)
	}
}

// ─── Credentialed skill clarification ────────────────────────────────────────

// TestSkillsSearch_CredentialedOutOfDomain_SendsClarification verifies that
// when skills_search finds a credentialed skill outside the agent's scope, it
// calls ClarificationSender.SendClarification with the correct fields and
// returns a response indicating the user is being consulted.
func TestSkillsSearch_CredentialedOutOfDomain_SendsClarification(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	cs := &fakeClarificationSender{
		// Simulate the response arriving (but we check the send before the response).
		response: types.ClarificationResponse{Approved: false},
	}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			minimalCredResult("google_workspace", "vault_gmail_send"),
		},
	}

	tool := skillsSearchTool(searcher, "web", true, registry, cs)
	raw, _ := json.Marshal(map[string]interface{}{"query": "send an email to the team"})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}

	// Clarification must have been sent.
	if len(cs.sent) != 1 {
		t.Fatalf("expected 1 clarification request, got %d", len(cs.sent))
	}
	req := cs.sent[0]

	// The request must identify the skill and domain clearly.
	if req.SkillName != "vault_gmail_send" {
		t.Errorf("SkillName: want vault_gmail_send, got %q", req.SkillName)
	}
	if req.SkillDomain != "google_workspace" {
		t.Errorf("SkillDomain: want google_workspace, got %q", req.SkillDomain)
	}

	// The request must explain why (non-empty Reason).
	if req.Reason == "" {
		t.Error("ClarificationRequest.Reason must not be empty")
	}
	// The request must have a non-empty question for the user.
	if req.Question == "" {
		t.Error("ClarificationRequest.Question must not be empty")
	}
	// RequestID must be set.
	if req.RequestID == "" {
		t.Error("ClarificationRequest.RequestID must not be empty")
	}

	// vault_gmail_send must NOT be in the registry (not auto-registered).
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "vault_gmail_send" {
			t.Error("credentialed tool must not be auto-registered before user approval")
		}
	}
}

// TestSkillsSearch_CredentialedOutOfDomain_Approved verifies the post-approval
// path: the agent receives a recommendation to call spawn_agent for the
// approved domain.
func TestSkillsSearch_CredentialedOutOfDomain_Approved(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	cs := &fakeClarificationSender{
		response: types.ClarificationResponse{
			Approved:    true,
			UserMessage: "Go ahead.",
		},
	}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			minimalCredResult("google_workspace", "vault_gmail_send"),
		},
	}

	tool := skillsSearchTool(searcher, "web", true, registry, cs)
	raw, _ := json.Marshal(map[string]interface{}{"query": "send an email to the team"})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Fatalf("unexpected error after approval: %s", result.Content)
	}

	// After approval: agent should be told to spawn_agent for google_workspace.
	if !strings.Contains(result.Content, "spawn_agent") {
		t.Errorf("approved result should recommend spawn_agent; got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "google_workspace") {
		t.Errorf("approved result should name the approved domain; got: %q", result.Content)
	}
	if result.Details["approved"] != true {
		t.Errorf("expected approved=true in details; got %v", result.Details["approved"])
	}
	if result.Details["recommended_action"] != "spawn_agent" {
		t.Errorf("expected recommended_action=spawn_agent; got %v", result.Details["recommended_action"])
	}
}

// TestSkillsSearch_CredentialedOutOfDomain_Denied verifies the post-denial
// path: the agent receives a user-facing explanation; vault_gmail_send is not
// registered; no internal error.
func TestSkillsSearch_CredentialedOutOfDomain_Denied(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	cs := &fakeClarificationSender{
		response: types.ClarificationResponse{
			Approved:    false,
			UserMessage: "I don't want the agent sending emails",
		},
	}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			minimalCredResult("google_workspace", "vault_gmail_send"),
		},
	}

	tool := skillsSearchTool(searcher, "web", true, registry, cs)
	raw, _ := json.Marshal(map[string]interface{}{"query": "send an email to the team"})
	result := tool.Execute(nil, raw)

	// Must not error — denial is a policy decision, not an error.
	if result.IsError {
		t.Fatalf("denial should not produce an error result: %s", result.Content)
	}

	// vault_gmail_send must not be in the registry after denial.
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "vault_gmail_send" {
			t.Error("credentialed tool must not be registered after user denial")
		}
	}

	// Response must explain the denial.
	if !strings.Contains(result.Content, "not authorized") &&
		!strings.Contains(result.Content, "declined") &&
		!strings.Contains(result.Content, "denied") {
		t.Errorf("denial response should explain the tool was not authorized; got: %q", result.Content)
	}
	if result.Details["denied"] != true {
		t.Errorf("expected denied=true in details; got %v", result.Details["denied"])
	}
}

// TestSkillsSearch_CredentialedOutOfDomain_NilCS_FallsBackToSpawn verifies
// that when cs is nil (no clarification sender), the tool falls back to the
// legacy spawn_agent recommendation rather than panicking.
func TestSkillsSearch_CredentialedOutOfDomain_NilCS_FallsBackToSpawn(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			minimalCredResult("google_workspace", "vault_gmail_send"),
		},
	}

	tool := skillsSearchTool(searcher, "web", true, registry, nil)
	raw, _ := json.Marshal(map[string]interface{}{"query": "send an email"})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Fatalf("unexpected error with nil cs: %s", result.Content)
	}
	if result.Details["recommended_action"] != "spawn_agent" {
		t.Errorf("expected spawn_agent fallback with nil cs, got %v", result.Details["recommended_action"])
	}
}

// TestSkillsSearch_CredentialedOutOfDomain_ClarificationError treats a
// SendClarification error as an implicit denial — the agent is told to find
// an alternative, but the result is not marked IsError.
func TestSkillsSearch_CredentialedOutOfDomain_ClarificationError(t *testing.T) {
	registry := newDynamicRegistry([]SkillTool{testWebFetchTool()})
	cs := &fakeClarificationSender{
		err: fmt.Errorf("timed out waiting for user response"),
	}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			minimalCredResult("google_workspace", "vault_gmail_send"),
		},
	}

	tool := skillsSearchTool(searcher, "web", true, registry, cs)
	raw, _ := json.Marshal(map[string]interface{}{"query": "send email"})
	result := tool.Execute(nil, raw)

	// Not an IsError — it is a graceful message.
	if result.IsError {
		t.Fatalf("clarification error should produce graceful message, not IsError: %s", result.Content)
	}
	if result.Details["clarification_error"] == nil {
		t.Error("expected clarification_error in details")
	}
}

// ─── Nil-safety for SendClarification ────────────────────────────────────────

// ─── RunLoop bootstrap wiring ─────────────────────────────────────────────────

// TestLoopBootstrap_SkillsSearchUpgrade verifies the two-phase bootstrap used
// in RunLoop:
//  1. toolsForDomain builds an initial tool list containing a nil-registry skills_search.
//  2. newDynamicRegistry wraps that list.
//  3. registry.Replace swaps in an upgraded, registry-aware skills_search.
//  4. The upgraded tool auto-registers a credential-free cross-domain skill,
//     proving the replacement (not the original) is the one the loop dispatches.
func TestLoopBootstrap_SkillsSearchUpgrade(t *testing.T) {
	// Step 1 & 2 — mirror RunLoop's bootstrap.
	tools := toolsForDomain("web", nil, nil, nil)
	registry := newDynamicRegistry(tools)

	// Confirm skills_search is present after toolsForDomain.
	found := false
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "skills_search" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("skills_search must be in registry after toolsForDomain")
	}

	// Step 3 — mirror RunLoop's upgrade call.
	sr := &fakeSearcherWithMeta{results: []types.SkillSearchResult{
		minimalCredFreeResult("e2e_test", "e2e_ping", "e2e_ping"),
	}}
	upgraded := skillsSearchTool(sr, "web", false, registry, nil)
	if err := registry.Replace("skills_search", upgraded); err != nil {
		t.Fatalf("registry.Replace(skills_search): %v", err)
	}

	// Step 4 — execute the replaced tool through the registry to prove the
	// upgrade is live. The original (nil-registry) version cannot auto-register;
	// the upgraded version can. Successful auto-registration of e2e_ping means
	// the replacement is the one being dispatched.
	var search SkillTool
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "skills_search" {
			search = rt
			break
		}
	}
	raw, _ := json.Marshal(map[string]interface{}{"query": "automated e2e probe"})
	result := search.Execute(nil, raw)
	if result.IsError {
		t.Fatalf("upgraded skills_search: unexpected error: %s", result.Content)
	}

	// If the upgrade worked, e2e_ping must now be in the registry.
	pingRegistered := false
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "e2e_ping" {
			pingRegistered = true
			break
		}
	}
	if !pingRegistered {
		t.Error("bootstrap wiring failed: e2e_ping was not auto-registered, indicating skills_search was not upgraded (nil-registry version was not replaced)")
	}
	if result.Details["auto_registered"] != true {
		t.Errorf("expected auto_registered=true in details; got %v", result.Details["auto_registered"])
	}
}

// TestLoopBootstrap_ReplaceFailsGracefully verifies that if registry.Replace
// were to fail (e.g. skills_search missing from the initial tool list), it
// does not panic — it returns an error that the caller can log and continue
// with the original (non-upgraded) version. This mirrors the log.Warn path in
// RunLoop.
func TestLoopBootstrap_ReplaceFailsGracefully(t *testing.T) {
	// Build a registry without skills_search (simulates a missing base tool).
	registry := newDynamicRegistry([]SkillTool{taskCompleteTool()})

	sr := &fakeSearcherWithMeta{}
	upgraded := skillsSearchTool(sr, "web", false, registry, nil)
	err := registry.Replace("skills_search", upgraded)
	if err == nil {
		t.Error("expected error when replacing a tool not in registry, got nil")
	}
	// Registry contents must be unchanged.
	if len(registry.Tools()) != 1 {
		t.Errorf("registry must be unchanged after failed Replace; got %d tools", len(registry.Tools()))
	}
}

// ─── Nil-safety for SendClarification ────────────────────────────────────────

// TestSessionLog_SendClarification_NilSafe verifies that calling SendClarification
// on a nil *SessionLog returns an error and does not panic.
func TestSessionLog_SendClarification_NilSafe(t *testing.T) {
	var sl *SessionLog
	_, err := sl.SendClarification(types.ClarificationRequest{
		RequestID:   "test-id",
		SkillName:   "some_skill",
		SkillDomain: "some_domain",
	})
	if err == nil {
		t.Error("expected error from nil SessionLog.SendClarification, got nil")
	}
}
