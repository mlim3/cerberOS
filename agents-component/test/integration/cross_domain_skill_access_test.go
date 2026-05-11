// Package integration — cross_domain_skill_access_test.go
//
// Integration tests for cross-domain skill access behavior at the NATS
// infrastructure level.
//
// These tests verify the plumbing that makes cross-domain skill access work:
//
//  1. Credential-free auto-registration: a credential-free tool discovered via
//     skills_search is available to the discovering agent without requiring a
//     separate spawn. At the infrastructure level this is observable as the
//     agent completing its task WITHOUT a spawn_agent delegation.
//
//  2. Credentialed skill + user approval: when the agent discovers a credentialed
//     out-of-scope skill, it sends a clarification.request. The orchestrator routes
//     this to the user. When the user approves, a clarification.response with
//     Approved=true is returned. The agent then spawns a child for the approved
//     domain — the orchestrator issues a scoped credential for the child.
//
//  3. Credentialed skill + user denial: same flow, but the user responds with
//     Approved=false. No child spawn occurs. The agent receives the denial and
//     must find an alternative or report it cannot complete the task.
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
// The actual end-to-end behavior (agent ReAct loop → clarification round-trip
// → resume) requires a running agent-process binary and is validated by the
// shell e2e test in tests/e2e/agents_cross_domain_skill_access.sh.
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

// ─── Scenario 6: Credential-free cross-domain — no spawn, no clarification ───

// TestNATS_CrossDomainCredFree_CompletesWithoutSpawn verifies that a task
// assigned to the "general" domain that exercises a credential-free tool from
// an out-of-domain source (e2e_test / e2e_ping) completes without triggering a
// child-agent spawn.
//
// After the auto-registration fix, skills_search should register e2e_ping into
// the discovering agent's DynamicRegistry and the agent calls it directly.
// At the NATS level this is observable as: task.result published, no
// spawn-related messages (no child credential.request for e2e_test domain).
//
// NOTE: this test currently reflects the INTENDED behavior after the
// auto-registration feature is implemented. Until then, the agent will either
// fail (spawn rejected by orchestrator) or use spawn_agent unnecessarily.
// The test is gated on the same infrastructure harness as the other NATS tests
// but includes an assertion comment marking where current vs. intended behavior
// diverges.
func TestNATS_CrossDomainCredFree_CompletesWithoutSpawn(t *testing.T) {
	h := newNATSHarness(t)

	taskResultCh := subscribeObserve(t, h.js, comms.SubjectTaskResult)
	credRequestCh := subscribeObserve(t, h.js, comms.SubjectCredentialRequest)

	spec := types.TaskSpec{
		TaskID:         fmt.Sprintf("e2e-cdfree-%d", time.Now().UnixNano()),
		RequiredSkills: []string{"web"},
		// Agent is provisioned for "web" only. e2e_ping lives in "e2e_test" domain,
		// so the agent must use skills_search to discover it. With auto-registration
		// the agent should call e2e_ping directly without spawning a child.
		Instructions: `Run an automated e2e connectivity probe with identifier "cdfree-integ-test". ` +
			`Use skills_search to find the right tool. ` +
			`Return the probe result.`,
	}
	if err := h.partner.publishTaskInbound(spec); err != nil {
		t.Fatalf("publishTaskInbound: %v", err)
	}

	// Wait for task to complete.
	raw := awaitMsg(t, taskResultCh, "task.result", 60*time.Second)
	var taskResult types.TaskResult
	if err := json.Unmarshal(raw, &taskResult); err != nil {
		t.Fatalf("unmarshal task.result: %v", err)
	}
	if !taskResult.Success {
		t.Errorf("task should have succeeded; error: %s", taskResult.Error)
	}

	// INTENDED BEHAVIOR: no second credential.request for e2e_test domain.
	// A second credential.request would indicate the agent spawned a child
	// agent for e2e_test, which is the wasteful pre-fix behavior.
	//
	// We drain the credential request channel and check how many were issued.
	// Exactly one is expected: the initial web-domain credential at spawn.
	credRequestCount := 0
	drain := time.After(500 * time.Millisecond)
drainLoop:
	for {
		select {
		case <-credRequestCh:
			credRequestCount++
		case <-drain:
			break drainLoop
		}
	}
	if credRequestCount > 1 {
		t.Errorf(
			"credential.request count: want 1 (initial spawn only), got %d — "+
				"agent may have unnecessarily spawned a child for e2e_test domain",
			credRequestCount,
		)
	}
}

// ─── Scenario 7: Credentialed cross-domain — user approves ───────────────────

// TestNATS_CrossDomainCredentialed_UserApproves verifies the clarification
// round-trip when the user grants access to an out-of-scope credentialed skill.
//
// Flow:
//  1. Agent (web domain) discovers a credentialed skill outside its scope
//  2. Agent sends clarification.request to orchestrator
//  3. Partner fixture (simulating orchestrator+user) responds with Approved=true
//  4. Agent proceeds: spawns a child agent for the approved domain
//  5. Child completes; task.result is published with success
func TestNATS_CrossDomainCredentialed_UserApproves(t *testing.T) {
	h := newNATSHarness(t)

	// Register the partner fixture to handle clarification requests by approving them.
	clarificationSent := make(chan types.ClarificationRequest, 1)
	h.partner.onClarificationRequest = func(req types.ClarificationRequest) types.ClarificationResponse {
		clarificationSent <- req
		return types.ClarificationResponse{
			RequestID:   req.RequestID,
			AgentID:     req.AgentID,
			Approved:    true,
			UserMessage: "Go ahead and use it.",
		}
	}

	taskResultCh := subscribeObserve(t, h.js, comms.SubjectTaskResult)

	spec := types.TaskSpec{
		TaskID:         fmt.Sprintf("e2e-cred-approve-%d", time.Now().UnixNano()),
		RequiredSkills: []string{"web"},
		// The agent needs to read from storage — credentialed, outside web scope.
		// It should discover vault_storage_read via skills_search and ask the user.
		Instructions: `Use skills_search to find a tool for reading a file named "report.json" from storage. ` +
			`If the tool requires expanded permissions, ask the user. ` +
			`Once approved, read the file and return its contents.`,
	}
	if err := h.partner.publishTaskInbound(spec); err != nil {
		t.Fatalf("publishTaskInbound: %v", err)
	}

	// A clarification.request must arrive before the task completes.
	select {
	case req := <-clarificationSent:
		if req.AgentID == "" {
			t.Error("clarification.request: AgentID must not be empty")
		}
		if req.SkillDomain == "" {
			t.Error("clarification.request: SkillDomain must not be empty")
		}
		if req.Reason == "" {
			t.Error("clarification.request: Reason must not be empty")
		}
		t.Logf("clarification received: skill=%s domain=%s", req.SkillName, req.SkillDomain)
	case <-time.After(30 * time.Second):
		t.Fatal("no clarification.request received within 30s — agent may not be sending clarification for out-of-scope credentialed skills")
	}

	// After approval, task should complete successfully.
	raw := awaitMsg(t, taskResultCh, "task.result", 60*time.Second)
	var result types.TaskResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal task.result: %v", err)
	}
	if !result.Success {
		t.Errorf("task should succeed after user approval; error: %s", result.Error)
	}
}

// ─── Scenario 8: Credentialed cross-domain — user denies ─────────────────────

// TestNATS_CrossDomainCredentialed_UserDenies verifies the denial path.
//
// Flow:
//  1. Agent discovers a credentialed out-of-scope skill via skills_search
//  2. Agent sends clarification.request
//  3. Partner fixture responds with Approved=false
//  4. Agent must NOT spawn a child for that domain
//  5. Agent either finds an alternative route or completes with a graceful
//     explanation that the capability was not authorized
//  6. task.result is published — Success may be false but the result must
//     contain a user-facing explanation, not a raw internal error
func TestNATS_CrossDomainCredentialed_UserDenies(t *testing.T) {
	h := newNATSHarness(t)

	clarificationSent := make(chan types.ClarificationRequest, 1)
	h.partner.onClarificationRequest = func(req types.ClarificationRequest) types.ClarificationResponse {
		clarificationSent <- req
		return types.ClarificationResponse{
			RequestID:   req.RequestID,
			AgentID:     req.AgentID,
			Approved:    false,
			UserMessage: "No, do not use storage for this task.",
		}
	}

	// Watch for child credential requests — there should be none after denial.
	credRequestCh := subscribeObserve(t, h.js, comms.SubjectCredentialRequest)
	taskResultCh := subscribeObserve(t, h.js, comms.SubjectTaskResult)

	spec := types.TaskSpec{
		TaskID:         fmt.Sprintf("e2e-cred-deny-%d", time.Now().UnixNano()),
		RequiredSkills: []string{"web"},
		Instructions: `Use skills_search to find a tool for reading a file named "report.json" from storage. ` +
			`If the tool requires expanded permissions, ask the user. ` +
			`If permission is denied, explain why you cannot complete the task.`,
	}
	if err := h.partner.publishTaskInbound(spec); err != nil {
		t.Fatalf("publishTaskInbound: %v", err)
	}

	// Clarification must be sent.
	select {
	case <-clarificationSent:
		// good
	case <-time.After(30 * time.Second):
		t.Fatal("no clarification.request within 30s")
	}

	// Task must complete — either with a graceful alternative or a user-facing
	// explanation. A raw panic or infrastructure error is a test failure.
	raw := awaitMsg(t, taskResultCh, "task.result after denial", 60*time.Second)
	var result types.TaskResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal task.result: %v", err)
	}

	// If the task failed, the error must be user-facing (not an internal panic
	// or "unknown tool" error from an incorrect implementation).
	if !result.Success {
		if result.Error == "" {
			t.Error("task failed but Error field is empty — agent must explain the denial")
		}
		forbiddenPhrases := []string{"unknown tool", "panic", "nil pointer", "runtime error"}
		for _, phrase := range forbiddenPhrases {
			if containsIgnoreCase(result.Error, phrase) {
				t.Errorf("task error contains internal error phrase %q — denial must produce a user-facing message, not a crash: %s", phrase, result.Error)
			}
		}
		t.Logf("task failed gracefully after denial (expected): %s", result.Error)
	} else {
		t.Log("task succeeded after denial — agent found an alternative route (also valid)")
	}

	// After denial: no child credential.request for the denied domain should
	// have been issued. Drain and count; only the initial spawn credential is expected.
	childCredCount := 0
	drain := time.After(500 * time.Millisecond)
drainLoop:
	for {
		select {
		case credRaw := <-credRequestCh:
			// Parse to check if it's for a child domain (not the initial web spawn).
			var req types.CredentialRequest
			if err := json.Unmarshal(credRaw, &req); err == nil {
				for _, domain := range req.SkillDomains {
					if domain != "web" {
						childCredCount++
					}
				}
			}
		case <-drain:
			break drainLoop
		}
	}
	if childCredCount > 0 {
		t.Errorf("expected no child credential requests after denial; got %d — agent spawned a child despite user denial", childCredCount)
	}
}

// ─── Partner fixture extension ────────────────────────────────────────────────
//
// The existing partnerFixture does not handle clarification.request.
// These tests require it. The onClarificationRequest hook below is set by
// individual tests; the fixture's start() method must subscribe to
// aegis.orchestrator.clarification.request and call the hook when set.
//
// TODO: add to partnerFixture:
//
//	type partnerFixture struct {
//	    ...
//	    onClarificationRequest func(types.ClarificationRequest) types.ClarificationResponse
//	}
//
// And in start():
//
//	{subject: comms.SubjectClarificationRequest, handler: f.handleClarificationRequest}
//
// And a new handler:
//
//	func (f *partnerFixture) handleClarificationRequest(msg *nats.Msg) {
//	    env, ok := unwrapEnvelope(msg.Data)
//	    if !ok { _ = msg.Ack(); return }
//	    if env.SourceComponent != f.owner { _ = msg.Ack(); return }
//	    var req types.ClarificationRequest
//	    if err := json.Unmarshal(env.Payload, &req); err != nil { _ = msg.Nak(); return }
//	    f.mu.Lock()
//	    hook := f.onClarificationRequest
//	    f.mu.Unlock()
//	    if hook == nil { _ = msg.Ack(); return } // no handler: silently drop
//	    resp := hook(req)
//	    _ = f.publish(comms.SubjectClarificationResponse, comms.MsgTypeClarificationResponse, req.RequestID, resp)
//	    _ = msg.Ack()
//	}

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
