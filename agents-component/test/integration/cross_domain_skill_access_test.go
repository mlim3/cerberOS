// Package integration — cross_domain_skill_access_test.go
//
// Integration tests for cross-domain skill access behavior at the NATS
// infrastructure level.
//
// These tests verify the plumbing that makes cross-domain skill access work:
//
//  1. Credential-free auto-registration: the partner fixture correctly routes
//     a skill_cache state.read.request from any publisher and returns a
//     SkillSearchResult slice that the agent's SearchSkills call can consume.
//     The test verifies the request → response round-trip at the NATS level.
//
//  2. Credentialed skill + user approval: a clarification.request published
//     on aegis.orchestrator.clarification.request is received by the partner
//     fixture, which calls onClarificationRequest and publishes the approved
//     clarification.response on aegis.agents.clarification.response.
//     The test verifies the complete round-trip including the approval payload.
//
//  3. Credentialed skill + user denial: same round-trip with Approved=false.
//     Verifies the denial payload is preserved and delivered correctly.
//
// What these tests cover vs. unit tests
// ──────────────────────────────────────
// The unit tests in tools_skills_search_cross_domain_test.go verify the
// decision logic inside the agent process: when to auto-register, when to
// send clarification, and how to handle the response.
//
// These integration tests verify the NATS message routing: that
// clarification.request published on aegis.orchestrator.clarification.request
// reaches the partner fixture, and that clarification.response published on
// aegis.agents.clarification.response is consumable by the agent's comms layer.
//
// These tests do NOT use a live agent binary. They manually publish the
// messages that the agent process would publish in production, then verify
// the partner fixture processes and responds correctly. The actual end-to-end
// behavior (agent ReAct loop → clarification round-trip → resume) requires a
// running agent-process binary and is validated by the shell e2e test in
// tests/e2e/agents_cross_domain_skill_access.sh.
package integration

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	"github.com/nats-io/nats.go"
)

// ─── Scenario 6: Skill-cache routing — skill_cache request → response ────────

// TestNATS_CrossDomainCredFree_SkillCacheRouting verifies that a skill_cache
// state.read.request is correctly routed to the partner fixture and that the
// fixture's onSkillSearch hook response is delivered back to the requesting
// subscriber on aegis.agents.state.read.response.
//
// This tests the NATS plumbing that the agent's SearchSkills call relies on:
// publish request → partner fixture processes → response arrives.
//
// Note: the subscriber here acts as a stand-in for the agent process's
// session.go SearchSkills loop.
func TestNATS_CrossDomainCredFree_SkillCacheRouting(t *testing.T) {
	h := newNATSHarness(t)

	// Configure the partner fixture to return a credential-free cross-domain skill.
	const wantToolName = "e2e_ping"
	const wantDomain = "e2e_test"
	h.partner.onSkillSearch = func(query string) []types.SkillSearchResult {
		return []types.SkillSearchResult{{
			Domain:         wantDomain,
			Name:           wantToolName,
			Description:    "Automated e2e connectivity probe. Echoes a probe string.",
			Score:          0.95,
			RequiresCred:   false,
			Implementation: "e2e_ping",
			Origin:         "", // config-loaded static skills have empty origin (zero value)
		}}
	}

	// Subscribe to the state.read.response subject BEFORE publishing the request
	// to avoid the race where the fixture responds before we subscribe.
	traceID := fmt.Sprintf("skill-cache-test-%d", time.Now().UnixNano())
	agentID := fmt.Sprintf("agent-%d", time.Now().UnixNano())

	respCh := make(chan json.RawMessage, 4)
	sub, err := h.js.Subscribe(
		comms.SubjectStateReadResponse,
		func(msg *nats.Msg) {
			_ = msg.Ack()
			respCh <- msg.Data
		},
		nats.DeliverNew(),
		nats.AckExplicit(),
	)
	if err != nil {
		t.Fatalf("subscribe state.read.response: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Publish a synthetic skill_cache state.read.request as the agent would.
	req := types.MemoryReadRequest{
		AgentID:       agentID,
		DataType:      "skill_cache",
		SemanticQuery: "e2e connectivity probe",
		TraceID:       traceID,
	}
	env := outboundEnvelope{
		MessageID:       newFixtureMessageID(),
		MessageType:     comms.MsgTypeStateReadRequest,
		SourceComponent: h.componentID, // use harness componentID so partner fixture accepts it
		CorrelationID:   traceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         req,
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal state.read.request: %v", err)
	}
	if _, err := h.js.Publish(comms.SubjectStateReadRequest, data); err != nil {
		t.Fatalf("publish state.read.request: %v", err)
	}

	// Wait for the response from the partner fixture.
	var raw json.RawMessage
	select {
	case raw = <-respCh:
	case <-time.After(10 * time.Second):
		t.Fatal("no state.read.response within 10s — partner fixture may not be routing skill_cache requests")
	}

	// Unwrap the response envelope.
	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}

	// Decode the skill search response payload.
	var skillResp struct {
		AgentID string                    `json:"agent_id"`
		Records []types.SkillSearchResult `json:"records"`
		TraceID string                    `json:"trace_id"`
	}
	if err := json.Unmarshal(wrapper.Payload, &skillResp); err != nil {
		t.Fatalf("unmarshal skill_cache response payload: %v", err)
	}

	if len(skillResp.Records) == 0 {
		t.Fatal("skill_cache response contains no records — onSkillSearch hook not called or response not delivered")
	}
	got := skillResp.Records[0]
	if got.Name != wantToolName {
		t.Errorf("skill name: want %q, got %q", wantToolName, got.Name)
	}
	if got.Domain != wantDomain {
		t.Errorf("skill domain: want %q, got %q", wantDomain, got.Domain)
	}
	if got.RequiresCred {
		t.Error("skill RequiresCred: want false for credential-free tool")
	}
	if got.Implementation == "" {
		t.Error("skill Implementation: must not be empty for auto-registration to work")
	}
	t.Logf("skill_cache routing verified: tool=%s.%s impl=%s", got.Domain, got.Name, got.Implementation)
}

// ─── Scenario 7: Clarification round-trip — user approves ────────────────────

// TestNATS_CrossDomainCredentialed_ClarificationApproved verifies that a
// clarification.request published to aegis.orchestrator.clarification.request
// is received by the partner fixture, processed by the onClarificationRequest
// hook, and the approved response is delivered to
// aegis.agents.clarification.response.
//
// This tests the NATS plumbing that the agent's SendClarification call relies on.
func TestNATS_CrossDomainCredentialed_ClarificationApproved(t *testing.T) {
	h := newNATSHarness(t)

	requestID := fmt.Sprintf("clar-req-%d", time.Now().UnixNano())
	agentID := fmt.Sprintf("agent-%d", time.Now().UnixNano())

	// Track the clarification request received by the fixture.
	hookCalled := make(chan types.ClarificationRequest, 1)
	h.partner.onClarificationRequest = func(req types.ClarificationRequest) types.ClarificationResponse {
		hookCalled <- req
		return types.ClarificationResponse{
			RequestID:   req.RequestID,
			AgentID:     req.AgentID,
			Approved:    true,
			UserMessage: "Go ahead and use it.",
		}
	}

	// Subscribe to clarification.response BEFORE publishing the request.
	respCh := make(chan json.RawMessage, 4)
	sub, err := h.js.Subscribe(
		comms.SubjectClarificationResponse,
		func(msg *nats.Msg) {
			_ = msg.Ack()
			respCh <- msg.Data
		},
		nats.DeliverNew(),
		nats.AckExplicit(),
	)
	if err != nil {
		t.Fatalf("subscribe clarification.response: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Publish a synthetic clarification.request as the agent's SendClarification would.
	clarReq := types.ClarificationRequest{
		RequestID:   requestID,
		AgentID:     agentID,
		SkillName:   "vault_storage_read",
		SkillDomain: "storage",
		Question:    `The task requires "vault_storage_read" in the "storage" domain. Allow access?`,
		Reason:      `Best match for the requested capability requires credentials outside current agent scope.`,
	}
	env := outboundEnvelope{
		MessageID:       newFixtureMessageID(),
		MessageType:     comms.MsgTypeClarificationRequest,
		SourceComponent: h.componentID, // use harness componentID so partner fixture accepts it
		CorrelationID:   requestID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         clarReq,
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal clarification.request: %v", err)
	}
	if _, err := h.js.Publish(comms.SubjectClarificationRequest, data); err != nil {
		t.Fatalf("publish clarification.request: %v", err)
	}

	// Fixture's hook must be called.
	var receivedReq types.ClarificationRequest
	select {
	case receivedReq = <-hookCalled:
	case <-time.After(10 * time.Second):
		t.Fatal("onClarificationRequest hook not called within 10s — partner fixture may not be routing clarification requests")
	}
	if receivedReq.AgentID == "" {
		t.Error("clarification.request: AgentID must not be empty")
	}
	if receivedReq.SkillDomain == "" {
		t.Error("clarification.request: SkillDomain must not be empty")
	}
	if receivedReq.RequestID != requestID {
		t.Errorf("clarification.request: RequestID want %q, got %q", requestID, receivedReq.RequestID)
	}
	t.Logf("clarification.request received by fixture: skill=%s domain=%s", receivedReq.SkillName, receivedReq.SkillDomain)

	// Approved response must be delivered on aegis.agents.clarification.response.
	var raw json.RawMessage
	select {
	case raw = <-respCh:
	case <-time.After(10 * time.Second):
		t.Fatal("no clarification.response within 10s after fixture hook returned")
	}

	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatalf("unmarshal clarification.response envelope: %v", err)
	}
	var resp types.ClarificationResponse
	if err := json.Unmarshal(wrapper.Payload, &resp); err != nil {
		t.Fatalf("unmarshal clarification.response payload: %v", err)
	}

	if resp.RequestID != requestID {
		t.Errorf("clarification.response: RequestID want %q, got %q", requestID, resp.RequestID)
	}
	if !resp.Approved {
		t.Error("clarification.response: Approved want true, got false")
	}
	if resp.UserMessage == "" {
		t.Error("clarification.response: UserMessage must not be empty")
	}
	t.Logf("clarification.response routing verified: approved=%v message=%q", resp.Approved, resp.UserMessage)
}

// ─── Scenario 8: Clarification round-trip — user denies ──────────────────────

// TestNATS_CrossDomainCredentialed_ClarificationDenied verifies the denial path
// of the clarification round-trip. The partner fixture responds with Approved=false
// and the denial is delivered to aegis.agents.clarification.response with the
// user's message preserved.
func TestNATS_CrossDomainCredentialed_ClarificationDenied(t *testing.T) {
	h := newNATSHarness(t)

	requestID := fmt.Sprintf("clar-deny-%d", time.Now().UnixNano())
	agentID := fmt.Sprintf("agent-%d", time.Now().UnixNano())

	hookCalled := make(chan struct{}, 1)
	const wantUserMessage = "No, do not use storage for this task."
	h.partner.onClarificationRequest = func(req types.ClarificationRequest) types.ClarificationResponse {
		hookCalled <- struct{}{}
		return types.ClarificationResponse{
			RequestID:   req.RequestID,
			AgentID:     req.AgentID,
			Approved:    false,
			UserMessage: wantUserMessage,
		}
	}

	// Subscribe BEFORE publishing.
	respCh := make(chan json.RawMessage, 4)
	sub, err := h.js.Subscribe(
		comms.SubjectClarificationResponse,
		func(msg *nats.Msg) {
			_ = msg.Ack()
			respCh <- msg.Data
		},
		nats.DeliverNew(),
		nats.AckExplicit(),
	)
	if err != nil {
		t.Fatalf("subscribe clarification.response: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Publish clarification.request.
	clarReq := types.ClarificationRequest{
		RequestID:   requestID,
		AgentID:     agentID,
		SkillName:   "vault_storage_read",
		SkillDomain: "storage",
		Question:    `Allow access to "vault_storage_read" in "storage" domain?`,
		Reason:      `Best match requires credentials outside current agent scope.`,
	}
	env := outboundEnvelope{
		MessageID:       newFixtureMessageID(),
		MessageType:     comms.MsgTypeClarificationRequest,
		SourceComponent: h.componentID,
		CorrelationID:   requestID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         clarReq,
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal clarification.request: %v", err)
	}
	if _, err := h.js.Publish(comms.SubjectClarificationRequest, data); err != nil {
		t.Fatalf("publish clarification.request: %v", err)
	}

	// Hook must fire.
	select {
	case <-hookCalled:
	case <-time.After(10 * time.Second):
		t.Fatal("onClarificationRequest hook not called within 10s")
	}

	// Denial response must arrive.
	var raw json.RawMessage
	select {
	case raw = <-respCh:
	case <-time.After(10 * time.Second):
		t.Fatal("no clarification.response within 10s after denial hook returned")
	}

	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	var resp types.ClarificationResponse
	if err := json.Unmarshal(wrapper.Payload, &resp); err != nil {
		t.Fatalf("unmarshal response payload: %v", err)
	}

	if resp.RequestID != requestID {
		t.Errorf("clarification.response: RequestID want %q, got %q", requestID, resp.RequestID)
	}
	if resp.Approved {
		t.Error("clarification.response: Approved want false, got true")
	}
	if resp.UserMessage != wantUserMessage {
		t.Errorf("clarification.response: UserMessage want %q, got %q", wantUserMessage, resp.UserMessage)
	}
	t.Logf("denial routing verified: approved=%v message=%q", resp.Approved, resp.UserMessage)
}

// containsIgnoreCase is a case-insensitive substring check used for error
// phrase matching in denial assertions.
func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) &&
		func() bool {
			sl, subl := []rune(s), []rune(substr)
			for i := 0; i <= len(sl)-len(subl); i++ {
				match := true
				for j := range subl {
					a, b := sl[i+j], subl[j]
					if a >= 'A' && a <= 'Z' {
						a += 32
					}
					if b >= 'A' && b <= 'Z' {
						b += 32
					}
					if a != b {
						match = false
						break
					}
				}
				if match {
					return true
				}
			}
			return false
		}()
}

// Compile-time check: ensure types used in tests are imported correctly.
var _ = types.ClarificationRequest{}
var _ = types.ClarificationResponse{}
var _ *nats.Conn
var _ = containsIgnoreCase("", "")
