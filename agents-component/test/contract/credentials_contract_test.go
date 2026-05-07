package contract

// Credential contract tests — ORC-C01 through ORC-C07.
//
// Validates the M5 Credential Broker ↔ Orchestrator interface:
//   - credential.request (authorize) → credential.response round-trip schema
//   - Permission token is opaque — never a raw credential value (NFR-08)
//   - Revocation is acknowledged
//   - Malformed requests are rejected with a structured error response
//   - Duplicate request_id (idempotent retry) returns the same status
//
// All tests require AEGIS_CONTRACT_NATS_URL and a live Orchestrator that
// processes aegis.orchestrator.credential.request messages.

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
)

const credContractTimeout = 10 * time.Second

// subscribeCredentialResponse subscribes the commsClient as a durable consumer
// on aegis.agents.credential.response with a unique consumer name and returns
// a channel that yields decoded CredentialResponse payloads.
func subscribeCredentialResponse(t *testing.T, h *contractHarness) <-chan types.CredentialResponse {
	t.Helper()
	ch := make(chan types.CredentialResponse, 8)
	consumer := h.componentID + "-cred-resp"

	if err := h.commsClient.SubscribeDurable(
		comms.SubjectCredentialResponse,
		consumer,
		func(msg *comms.Message) {
			var resp types.CredentialResponse
			if err := json.Unmarshal(msg.Data, &resp); err != nil {
				_ = msg.Nak()
				return
			}
			ch <- resp
			_ = msg.Ack()
		},
	); err != nil {
		t.Fatalf("subscribeCredentialResponse: %v", err)
	}
	return ch
}

// publishCredentialRequest publishes a credential.request and returns the request_id.
func publishCredentialRequest(t *testing.T, h *contractHarness, req types.CredentialRequest) {
	t.Helper()
	if err := h.commsClient.Publish(
		comms.SubjectCredentialRequest,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeCredentialRequest,
			CorrelationID: req.RequestID,
		},
		req,
	); err != nil {
		t.Fatalf("publishCredentialRequest: %v", err)
	}
}

// awaitCredentialResponse waits for the response whose RequestID matches requestID.
// Other responses (from parallel tests) are re-queued on ch.
func awaitCredentialResponse(
	t *testing.T,
	ch <-chan types.CredentialResponse,
	requestID string,
	timeout time.Duration,
) types.CredentialResponse {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case resp := <-ch:
			if resp.RequestID == requestID {
				return resp
			}
			// Different request_id — belongs to a concurrent test; discard and keep waiting.
		case <-deadline:
			t.Fatalf("timed out after %v waiting for credential.response request_id=%q "+
				"(Orchestrator may not be running or credential.request is not being processed)",
				timeout, requestID)
			return types.CredentialResponse{}
		}
	}
}

// ORC-C01: credential.request (authorize) must produce credential.response
// with status="granted" and a non-empty permission_token.
func TestContract_Credentials_AuthorizeGranted(t *testing.T) {
	h := newContractHarness(t)
	respCh := subscribeCredentialResponse(t, h)

	requestID := fmt.Sprintf("orc-c01-%s", h.componentID)
	req := types.CredentialRequest{
		RequestID:    requestID,
		AgentID:      "orc-c01-agent",
		TaskID:       "orc-c01-task",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
		TTLSeconds:   300,
	}
	publishCredentialRequest(t, h, req)

	resp := awaitCredentialResponse(t, respCh, requestID, credContractTimeout)

	if resp.RequestID != requestID {
		t.Errorf("ORC-C01 response.request_id: want %q, got %q", requestID, resp.RequestID)
	}
	if resp.Status != "granted" {
		t.Errorf("ORC-C01 response.status: want %q, got %q — error: %s %s",
			"granted", resp.Status, resp.ErrorCode, resp.ErrorMessage)
	}
	if resp.PermissionToken == "" {
		t.Error("ORC-C01: permission_token must not be empty on status=granted")
	}
}

// ORC-C02: permission_token must be opaque — must not contain raw credential
// values such as Bearer tokens, PEM headers, AWS keys, or long base64 strings
// that resemble secrets (NFR-08).
func TestContract_Credentials_PermissionTokenIsOpaque(t *testing.T) {
	h := newContractHarness(t)
	respCh := subscribeCredentialResponse(t, h)

	requestID := fmt.Sprintf("orc-c02-%s", h.componentID)
	publishCredentialRequest(t, h, types.CredentialRequest{
		RequestID:    requestID,
		AgentID:      "orc-c02-agent",
		TaskID:       "orc-c02-task",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
	})

	resp := awaitCredentialResponse(t, respCh, requestID, credContractTimeout)
	if resp.Status != "granted" {
		t.Skipf("ORC-C02: skipping opaque check — status=%q (not granted)", resp.Status)
	}

	// Encode the token as raw JSON so assertNoCredentialLeak can inspect it.
	tokenJSON, _ := json.Marshal(resp.PermissionToken)
	assertNoCredentialLeak(t, "ORC-C02 permission_token", tokenJSON)
}

// ORC-C03: credential.request (authorize) response must include expires_at
// when granted, and it must be a valid ISO 8601 timestamp in the future.
func TestContract_Credentials_ExpiresAtIsFuture(t *testing.T) {
	h := newContractHarness(t)
	respCh := subscribeCredentialResponse(t, h)

	requestID := fmt.Sprintf("orc-c03-%s", h.componentID)
	publishCredentialRequest(t, h, types.CredentialRequest{
		RequestID:    requestID,
		AgentID:      "orc-c03-agent",
		TaskID:       "orc-c03-task",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
		TTLSeconds:   300,
	})

	resp := awaitCredentialResponse(t, respCh, requestID, credContractTimeout)
	if resp.Status != "granted" {
		t.Skipf("ORC-C03: skipping — status=%q", resp.Status)
	}
	if resp.ExpiresAt == "" {
		t.Error("ORC-C03: expires_at must be set on status=granted")
		return
	}
	assertISO8601(t, "ORC-C03 expires_at", resp.ExpiresAt)

	expiry, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		expiry, err = time.Parse(time.RFC3339Nano, resp.ExpiresAt)
	}
	if err == nil && !expiry.After(time.Now()) {
		t.Errorf("ORC-C03: expires_at %v is not in the future", expiry)
	}
}

// ORC-C04: credential.request (revoke) must be acknowledged. The response may
// have status "revoked", "ok", or "error" — but a response must arrive and must
// not hang indefinitely (i.e., the Orchestrator must not silently drop it).
func TestContract_Credentials_RevokeIsAcknowledged(t *testing.T) {
	h := newContractHarness(t)
	respCh := subscribeCredentialResponse(t, h)

	// First authorize so there is a token to revoke.
	authRequestID := fmt.Sprintf("orc-c04-auth-%s", h.componentID)
	publishCredentialRequest(t, h, types.CredentialRequest{
		RequestID:    authRequestID,
		AgentID:      "orc-c04-agent",
		TaskID:       "orc-c04-task",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
	})
	authResp := awaitCredentialResponse(t, respCh, authRequestID, credContractTimeout)
	if authResp.Status != "granted" {
		t.Skipf("ORC-C04: authorize returned %q — skipping revoke test", authResp.Status)
	}

	// Now revoke.
	revokeRequestID := fmt.Sprintf("orc-c04-rev-%s", h.componentID)
	publishCredentialRequest(t, h, types.CredentialRequest{
		RequestID: revokeRequestID,
		AgentID:   "orc-c04-agent",
		TaskID:    "orc-c04-task",
		Operation: "revoke",
	})

	revokeResp := awaitCredentialResponse(t, respCh, revokeRequestID, credContractTimeout)
	// The Orchestrator must respond — any status is acceptable as long as a
	// response arrives. "error" with an error_code is also valid.
	if revokeResp.RequestID != revokeRequestID {
		t.Errorf("ORC-C04: revoke response request_id mismatch: want %q, got %q",
			revokeRequestID, revokeResp.RequestID)
	}
	t.Logf("ORC-C04: revoke response status=%q error_code=%q", revokeResp.Status, revokeResp.ErrorCode)
}

// ORC-C05: credential.request with an empty agent_id must not produce a
// permission_token. The Orchestrator must either return status="error" or
// "denied" — never "granted" with a token for an anonymous agent.
func TestContract_Credentials_EmptyAgentIDIsNotGranted(t *testing.T) {
	h := newContractHarness(t)
	respCh := subscribeCredentialResponse(t, h)

	requestID := fmt.Sprintf("orc-c05-%s", h.componentID)
	publishCredentialRequest(t, h, types.CredentialRequest{
		RequestID:    requestID,
		AgentID:      "", // intentionally empty
		TaskID:       "orc-c05-task",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
	})

	select {
	case resp := <-respCh:
		if resp.RequestID != requestID {
			t.Logf("ORC-C05: received unrelated response (request_id=%q) — may belong to a parallel test", resp.RequestID)
			return
		}
		if resp.Status == "granted" && resp.PermissionToken != "" {
			t.Errorf("ORC-C05: Orchestrator granted credentials to an empty agent_id — " +
				"this is a security violation; access control must reject empty agent_id")
		}
		t.Logf("ORC-C05: empty agent_id response: status=%q error_code=%q", resp.Status, resp.ErrorCode)
	case <-time.After(credContractTimeout):
		// Orchestrator may also silently drop invalid requests — that is acceptable.
		t.Log("ORC-C05: no response for empty agent_id request (silent drop is acceptable)")
	}
}

// ORC-C06: duplicate request_id (idempotent retry) must not produce two distinct
// tokens. Republishing the same credential.request with identical request_id should
// return the same status — the Orchestrator must be idempotent on this field.
func TestContract_Credentials_DuplicateRequestIDIsIdempotent(t *testing.T) {
	h := newContractHarness(t)
	respCh := subscribeCredentialResponse(t, h)

	requestID := fmt.Sprintf("orc-c06-%s", h.componentID)
	req := types.CredentialRequest{
		RequestID:    requestID,
		AgentID:      "orc-c06-agent",
		TaskID:       "orc-c06-task",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
	}

	// Publish the same request twice.
	publishCredentialRequest(t, h, req)
	publishCredentialRequest(t, h, req)

	resp1 := awaitCredentialResponse(t, respCh, requestID, credContractTimeout)

	// Wait briefly for a potential second response.
	var resp2 *types.CredentialResponse
	select {
	case r := <-respCh:
		if r.RequestID == requestID {
			resp2 = &r
		}
	case <-time.After(3 * time.Second):
	}

	if resp2 != nil {
		// Two responses arrived — both statuses must match.
		if resp1.Status != resp2.Status {
			t.Errorf("ORC-C06: idempotency violation — first response status=%q, second=%q",
				resp1.Status, resp2.Status)
		}
		// Permission tokens must also match (same scope = same token reference).
		if resp1.PermissionToken != "" && resp2.PermissionToken != "" &&
			resp1.PermissionToken != resp2.PermissionToken {
			t.Errorf("ORC-C06: duplicate request_id produced two different permission tokens — "+
				"Orchestrator must be idempotent on request_id: token1=%q token2=%q",
				resp1.PermissionToken, resp2.PermissionToken)
		}
	}
	// If only one response arrives, that is also acceptable — the Orchestrator
	// may deduplicate before publishing a response.
	t.Logf("ORC-C06: idempotency: responses=%d first_status=%q", func() int {
		if resp2 != nil {
			return 2
		}
		return 1
	}(), resp1.Status)
}

// ORC-C07: error_message on a denied or error response must not expose Vault
// internals, credential paths, or raw secret material (NFR-08).
func TestContract_Credentials_ErrorMessageDoesNotLeakVaultInternals(t *testing.T) {
	h := newContractHarness(t)
	respCh := subscribeCredentialResponse(t, h)

	// Request with an unknown skill domain — may trigger denied or error.
	requestID := fmt.Sprintf("orc-c07-%s", h.componentID)
	publishCredentialRequest(t, h, types.CredentialRequest{
		RequestID:    requestID,
		AgentID:      "orc-c07-agent",
		TaskID:       "orc-c07-task",
		Operation:    "authorize",
		SkillDomains: []string{"nonexistent-domain-orc-c07"},
	})

	select {
	case resp := <-respCh:
		if resp.RequestID != requestID {
			return
		}
		if resp.ErrorMessage != "" {
			msgJSON, _ := json.Marshal(resp.ErrorMessage)
			assertNoCredentialLeak(t, "ORC-C07 error_message", msgJSON)

			// Must not expose internal paths or vault implementation details.
			for _, forbidden := range []string{"openbao", "/v1/", "vault:", "kv/data/"} {
				if contains(resp.ErrorMessage, forbidden) {
					t.Errorf("ORC-C07: error_message exposes vault internal path %q: %q",
						forbidden, resp.ErrorMessage)
				}
			}
		}
		t.Logf("ORC-C07: unknown domain response: status=%q error_code=%q", resp.Status, resp.ErrorCode)
	case <-time.After(credContractTimeout):
		t.Log("ORC-C07: no response for unknown domain (Orchestrator may reject silently)")
	}
}

// contains is a simple substring check.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsRune(s, substr))
}

func containsRune(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
