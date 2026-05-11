// Package main — tools_skills_search_cross_domain_test.go
//
// TDD tests for cross-domain skill access via skills_search.
//
// These tests define the intended behavior for two cases that are NOT YET
// IMPLEMENTED. They will fail to compile until:
//
//  1. executeSkillsSearch (or skillsSearchTool) accepts a *DynamicRegistry so
//     it can auto-register credential-free discovered tools at search time.
//
//  2. skillsSearchTool accepts a ClarificationSender so it can surface
//     out-of-scope credentialed skills to the user via clarification.request
//     rather than blindly recommending spawn_agent (which the orchestrator
//     would reject for undeclared domains).
//
// Intended behavior summary
// ─────────────────────────
//
// Credential-free, out-of-domain skill discovered:
//   skills_search finds a static, requires_cred=false skill in a domain not
//   declared at spawn. The tool auto-registers it into DynamicRegistry using
//   builtinRegistry[result.Implementation]. The agent is told it can call the
//   tool directly. No spawn_agent. No clarification.
//
// Credentialed, out-of-domain skill discovered:
//   skills_search finds a requires_cred=true skill outside the agent's scope.
//   The tool calls ClarificationSender.SendClarification. The agent receives
//   a "waiting for user approval" response and pauses. When the response
//   arrives (approved or denied), the loop resumes:
//   - Approved: spawn_agent is recommended with the new domain (orchestrator
//     will issue a scoped permission_token for the child).
//   - Denied: the tool returns a message saying the user declined and the
//     agent should either find an alternative or explain it cannot complete
//     the task.
//
// Build tag
// ─────────
//   //go:build cross_domain_tdd
//
// Remove the build tag (and the t.Skip calls) once the feature is implemented.

//go:build cross_domain_tdd

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cerberOS/agents-component/pkg/types"
)

// ─── Fixtures ────────────────────────────────────────────────────────────────

// fakeSearcherWithMeta extends fakeSearcher with the new RequiresCred /
// Implementation / Origin fields populated so the auto-registration and
// clarification paths can be exercised.
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
// assert on what was sent.
type fakeClarificationSender struct {
	sent []types.ClarificationRequest
}

func (f *fakeClarificationSender) SendClarification(req types.ClarificationRequest) {
	f.sent = append(f.sent, req)
}

// minimalCredFreeResult returns a SkillSearchResult for a static,
// credential-free skill outside the querying agent's domain.
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

// minimalCredResult returns a SkillSearchResult for a credentialed skill
// outside the querying agent's domain.
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

// ─── Credential-free auto-registration ───────────────────────────────────────

// TestSkillsSearch_CredFreeOutOfDomain_AutoRegisters verifies that when
// skills_search returns a credential-free static skill in a domain the agent
// was not provisioned for, the tool is immediately registered into the
// DynamicRegistry and the agent is told it can call it directly.
//
// This is the core of the "fix the gate" approach: domain scoping should only
// gate credential-requiring tools (where the vault needs a scoped token).
// Credential-free tools have no vault implication and should be available to
// any agent that discovers them.
func TestSkillsSearch_CredFreeOutOfDomain_AutoRegisters(t *testing.T) {
	t.Skip("TODO: implement credential-free cross-domain auto-registration")

	registry := newDynamicRegistry([]SkillTool{
		// agent was provisioned for "web" only — no e2e_test tools
		webFetchTool(),
	})

	cs := &fakeClarificationSender{}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			// e2e_ping: credential-free, static, outside "web" domain
			minimalCredFreeResult("e2e_test", "e2e_ping", "e2e_ping"),
		},
	}

	// TODO: update skillsSearchTool (or executeSkillsSearch) to accept:
	//   registry *DynamicRegistry — for auto-registration
	//   cs ClarificationSender   — for credentialed out-of-scope skills
	tool := skillsSearchTool(searcher, "web", false, registry, cs)

	raw, _ := json.Marshal(map[string]interface{}{
		"query": "automated e2e connectivity probe",
	})
	result := tool.Execute(nil, raw)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}

	// 1. e2e_ping must now be in the registry — callable without spawn_agent.
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

	// 2. Response must tell the agent it can call the tool directly.
	if !strings.Contains(result.Content, "e2e_ping") {
		t.Errorf("expected response to mention e2e_ping; got: %q", result.Content)
	}
	if strings.Contains(result.Content, "spawn_agent") {
		t.Errorf("response should NOT recommend spawn_agent for a credential-free tool; got: %q", result.Content)
	}

	// 3. No clarification should have been sent.
	if len(cs.sent) != 0 {
		t.Errorf("expected no clarification request for credential-free tool; got %d", len(cs.sent))
	}
}

// TestSkillsSearch_CredFreeOutOfDomain_UnknownImpl treats a credential-free
// result whose Implementation is not in builtinRegistry as unregisterable.
// The tool should fall back to recommending spawn_agent (or silently skip),
// not panic or hard-error.
func TestSkillsSearch_CredFreeOutOfDomain_UnknownImpl(t *testing.T) {
	t.Skip("TODO: implement credential-free cross-domain auto-registration")

	registry := newDynamicRegistry([]SkillTool{webFetchTool()})
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
}

// TestSkillsSearch_CredFreeOutOfDomain_SynthesizedSkipped verifies that
// synthesized skills (origin="synthesized") are NOT auto-registered, even
// when requires_cred=false. Synthesized skills use LLM-recipe execution and
// are unreliable; only static builtins are safe to auto-register.
func TestSkillsSearch_CredFreeOutOfDomain_SynthesizedSkipped(t *testing.T) {
	t.Skip("TODO: implement credential-free cross-domain auto-registration")

	registry := newDynamicRegistry([]SkillTool{webFetchTool()})
	cs := &fakeClarificationSender{}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			{
				Domain:         "e2e_test",
				Name:           "execute_e2e_connectivity_probe", // synthesized from prior run
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
	tool.Execute(nil, raw)

	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "execute_e2e_connectivity_probe" {
			t.Error("synthesized skill should not be auto-registered")
		}
	}
}

// ─── Credentialed skill clarification ────────────────────────────────────────

// TestSkillsSearch_CredentialedOutOfDomain_SendsClarification verifies that
// when skills_search finds a credentialed skill outside the agent's scope, it
// calls ClarificationSender.SendClarification with enough context for the user
// to make an informed decision.
//
// The agent must NOT blindly recommend spawn_agent — the orchestrator would
// reject the spawn because the parent's policy scope doesn't include the domain.
// Instead it surfaces the decision to the user.
func TestSkillsSearch_CredentialedOutOfDomain_SendsClarification(t *testing.T) {
	t.Skip("TODO: implement credentialed cross-domain user clarification")

	registry := newDynamicRegistry([]SkillTool{webFetchTool()})
	cs := &fakeClarificationSender{}
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

	// 1. Clarification must have been sent.
	if len(cs.sent) != 1 {
		t.Fatalf("expected 1 clarification request, got %d", len(cs.sent))
	}
	req := cs.sent[0]

	// 2. The request must identify the skill and domain clearly.
	if req.SkillName != "vault_gmail_send" {
		t.Errorf("SkillName: want vault_gmail_send, got %q", req.SkillName)
	}
	if req.SkillDomain != "google_workspace" {
		t.Errorf("SkillDomain: want google_workspace, got %q", req.SkillDomain)
	}

	// 3. The request must explain why (non-empty Reason).
	if req.Reason == "" {
		t.Error("ClarificationRequest.Reason must not be empty")
	}

	// 4. The response to the agent must indicate it is waiting for approval.
	if !strings.Contains(result.Content, "approval") && !strings.Contains(result.Content, "permission") {
		t.Errorf("response should mention waiting for approval; got: %q", result.Content)
	}

	// 5. vault_gmail_send must NOT be in the registry (not auto-registered).
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "vault_gmail_send" {
			t.Error("credentialed tool must not be auto-registered before user approval")
		}
	}
}

// TestSkillsSearch_CredentialedOutOfDomain_Approved verifies the post-approval
// path: after the user approves, the agent receives a recommendation to call
// spawn_agent for the approved domain. The orchestrator will issue a scoped
// permission_token for that domain when handling the spawn.
func TestSkillsSearch_CredentialedOutOfDomain_Approved(t *testing.T) {
	t.Skip("TODO: implement credentialed cross-domain user clarification")

	registry := newDynamicRegistry([]SkillTool{webFetchTool()})

	// Simulate a clarification sender that immediately calls back with approval.
	// In production this would be async (NATS round-trip); the test uses a
	// synchronous stub to keep the test deterministic.
	approvingCS := &autoRespondClarificationSender{approved: true, message: ""}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			minimalCredResult("google_workspace", "vault_gmail_send"),
		},
	}

	tool := skillsSearchTool(searcher, "web", true, registry, approvingCS)
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
}

// TestSkillsSearch_CredentialedOutOfDomain_Denied verifies the post-denial
// path: after the user denies, the agent receives a message explaining the
// skill was not authorized and is told to find an alternative or report that
// the task cannot be completed.
func TestSkillsSearch_CredentialedOutOfDomain_Denied(t *testing.T) {
	t.Skip("TODO: implement credentialed cross-domain user clarification")

	registry := newDynamicRegistry([]SkillTool{webFetchTool()})
	denyingCS := &autoRespondClarificationSender{approved: false, message: "I don't want the agent sending emails"}
	searcher := &fakeSearcherWithMeta{
		results: []types.SkillSearchResult{
			minimalCredResult("google_workspace", "vault_gmail_send"),
		},
	}

	tool := skillsSearchTool(searcher, "web", true, registry, denyingCS)
	raw, _ := json.Marshal(map[string]interface{}{"query": "send an email to the team"})
	result := tool.Execute(nil, raw)

	// Must not error — denial is not an error, it is a policy decision.
	if result.IsError {
		t.Fatalf("denial should not produce an error result: %s", result.Content)
	}

	// vault_gmail_send must not be in the registry after denial.
	for _, rt := range registry.Tools() {
		if rt.Definition.Name == "vault_gmail_send" {
			t.Error("credentialed tool must not be registered after user denial")
		}
	}

	// Response must explain the denial so the agent can try an alternative.
	if !strings.Contains(result.Content, "not authorized") && !strings.Contains(result.Content, "declined") && !strings.Contains(result.Content, "denied") {
		t.Errorf("denial response should explain the tool was not authorized; got: %q", result.Content)
	}
}

// autoRespondClarificationSender is a test stub that immediately resolves a
// ClarificationRequest synchronously. In production, clarification is async
// (NATS round-trip to the user via the I/O component), but the synchronous
// stub keeps unit tests deterministic.
//
// TODO: this interface implies that skillsSearchTool must block or return a
// "pending clarification" sentinel until the response arrives. The exact
// blocking mechanism (channel, callback, or a new ReAct loop phase) is an
// implementation detail left to the developer.
type autoRespondClarificationSender struct {
	approved bool
	message  string
}

func (a *autoRespondClarificationSender) SendClarification(req types.ClarificationRequest) {
	// no-op in stub — the response is injected via the tool's return value
}
