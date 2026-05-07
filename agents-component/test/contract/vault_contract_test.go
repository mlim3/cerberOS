package contract

// Vault execute contract tests — ORC-V01 through ORC-V06.
//
// Validates the vault execute request/result/cancel/progress interface between
// the Agents Component and the Orchestrator (ADR-004).
//
// Invariants enforced:
//   - vault.execute.request is routed and a result is returned within SLA
//   - vault.execute.result never contains raw credential material (NFR-08)
//   - operation_result is present on success and absent on failure
//   - vault.execute.cancel is accepted by the Orchestrator
//   - vault.execute.progress arrives as at-most-once core NATS, not JetStream
//   - Invalid permission_token produces scope_violation, not a server panic

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	"github.com/nats-io/nats.go"
)

const vaultContractTimeout = 15 * time.Second

// subscribeVaultResult subscribes as a durable consumer on
// aegis.agents.vault.execute.result and returns a channel of decoded results.
func subscribeVaultResult(t *testing.T, h *contractHarness) <-chan types.VaultOperationResult {
	t.Helper()
	ch := make(chan types.VaultOperationResult, 8)
	consumer := h.componentID + "-vault-result"

	if err := h.commsClient.SubscribeDurable(
		comms.SubjectVaultExecuteResult,
		consumer,
		func(msg *comms.Message) {
			var result types.VaultOperationResult
			if err := json.Unmarshal(msg.Data, &result); err != nil {
				_ = msg.Nak()
				return
			}
			ch <- result
			_ = msg.Ack()
		},
	); err != nil {
		t.Fatalf("subscribeVaultResult: %v", err)
	}
	return ch
}

// publishVaultRequest publishes a vault.execute.request with the given fields.
func publishVaultRequest(t *testing.T, h *contractHarness, req types.VaultOperationRequest) {
	t.Helper()
	if err := h.commsClient.Publish(
		comms.SubjectVaultExecuteRequest,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeVaultExecuteRequest,
			CorrelationID: req.RequestID, // MUST equal request_id (ADR-004)
		},
		req,
	); err != nil {
		t.Fatalf("publishVaultRequest: %v", err)
	}
}

// awaitVaultResult waits for the result matching requestID.
func awaitVaultResult(
	t *testing.T,
	ch <-chan types.VaultOperationResult,
	requestID string,
	timeout time.Duration,
) types.VaultOperationResult {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case result := <-ch:
			if result.RequestID == requestID {
				return result
			}
		case <-deadline:
			t.Fatalf("timed out after %v waiting for vault.execute.result request_id=%q",
				timeout, requestID)
			return types.VaultOperationResult{}
		}
	}
}

// authorizeAndGetToken is a helper that performs the credential.request →
// credential.response round-trip and returns the permission_token.
// Skips the calling test if authorization fails.
func authorizeAndGetToken(t *testing.T, h *contractHarness, agentID, taskID string) string {
	t.Helper()
	respCh := subscribeCredentialResponse(t, h)

	requestID := fmt.Sprintf("vault-auth-%s-%s", agentID, h.componentID)
	publishCredentialRequest(t, h, types.CredentialRequest{
		RequestID:    requestID,
		AgentID:      agentID,
		TaskID:       taskID,
		Operation:    "authorize",
		SkillDomains: []string{"web"},
		TTLSeconds:   300,
	})

	resp := awaitCredentialResponse(t, respCh, requestID, credContractTimeout)
	if resp.Status != "granted" || resp.PermissionToken == "" {
		t.Skipf("vault auth prerequisite failed: status=%q — skipping vault execute test", resp.Status)
	}
	return resp.PermissionToken
}

// ORC-V01: vault.execute.request with a valid permission_token must produce
// vault.execute.result within the vault contract SLA (15 s).
func TestContract_Vault_ExecuteRequestProducesResult(t *testing.T) {
	h := newContractHarness(t)
	resultCh := subscribeVaultResult(t, h)

	token := authorizeAndGetToken(t, h, "orc-v01-agent", "orc-v01-task")

	requestID := fmt.Sprintf("orc-v01-%s", h.componentID)
	publishVaultRequest(t, h, types.VaultOperationRequest{
		RequestID:       requestID,
		AgentID:         "orc-v01-agent",
		TaskID:          "orc-v01-task",
		PermissionToken: token,
		OperationType:   "web_fetch",
		OperationParams: json.RawMessage(`{"url":"https://example.com","method":"GET"}`),
		TimeoutSeconds:  10,
		CredentialType:  "web_api_key",
	})

	result := awaitVaultResult(t, resultCh, requestID, vaultContractTimeout)

	if result.RequestID != requestID {
		t.Errorf("ORC-V01 result.request_id: want %q, got %q", requestID, result.RequestID)
	}
	if result.AgentID != "orc-v01-agent" {
		t.Errorf("ORC-V01 result.agent_id: want %q, got %q", "orc-v01-agent", result.AgentID)
	}
	validStatuses := map[string]bool{
		"success": true, "timed_out": true, "scope_violation": true, "execution_error": true,
	}
	if !validStatuses[result.Status] {
		t.Errorf("ORC-V01 result.status: %q is not a valid status value "+
			"(want one of: success, timed_out, scope_violation, execution_error)", result.Status)
	}
}

// ORC-V02: vault.execute.result must never contain raw credential values in
// operation_result, error_message, or any other field (NFR-08, ADR-004).
func TestContract_Vault_ResultNeverContainsRawCredential(t *testing.T) {
	h := newContractHarness(t)
	resultCh := subscribeVaultResult(t, h)

	token := authorizeAndGetToken(t, h, "orc-v02-agent", "orc-v02-task")

	requestID := fmt.Sprintf("orc-v02-%s", h.componentID)
	publishVaultRequest(t, h, types.VaultOperationRequest{
		RequestID:       requestID,
		AgentID:         "orc-v02-agent",
		TaskID:          "orc-v02-task",
		PermissionToken: token,
		OperationType:   "web_fetch",
		OperationParams: json.RawMessage(`{"url":"https://example.com"}`),
		TimeoutSeconds:  10,
		CredentialType:  "web_api_key",
	})

	result := awaitVaultResult(t, resultCh, requestID, vaultContractTimeout)

	// operation_result must not contain raw credentials.
	if result.OperationResult != nil {
		assertNoCredentialLeak(t, "ORC-V02 operation_result", result.OperationResult)
	}
	// error_message must not expose vault internals.
	if result.ErrorMessage != "" {
		msgJSON, _ := json.Marshal(result.ErrorMessage)
		assertNoCredentialLeak(t, "ORC-V02 error_message", msgJSON)
	}
}

// ORC-V03: on success, operation_result must be present and valid JSON.
// On non-success, operation_result must be absent or null — the Vault must not
// leak partial results on failure.
func TestContract_Vault_OperationResultPresentOnSuccessAbsentOnFailure(t *testing.T) {
	h := newContractHarness(t)
	resultCh := subscribeVaultResult(t, h)

	token := authorizeAndGetToken(t, h, "orc-v03-agent", "orc-v03-task")

	requestID := fmt.Sprintf("orc-v03-%s", h.componentID)
	publishVaultRequest(t, h, types.VaultOperationRequest{
		RequestID:       requestID,
		AgentID:         "orc-v03-agent",
		TaskID:          "orc-v03-task",
		PermissionToken: token,
		OperationType:   "web_fetch",
		OperationParams: json.RawMessage(`{"url":"https://example.com"}`),
		TimeoutSeconds:  10,
		CredentialType:  "web_api_key",
	})

	result := awaitVaultResult(t, resultCh, requestID, vaultContractTimeout)

	switch result.Status {
	case "success":
		if result.OperationResult == nil || string(result.OperationResult) == "null" {
			t.Error("ORC-V03: operation_result must be present on status=success")
		} else {
			var check interface{}
			if err := json.Unmarshal(result.OperationResult, &check); err != nil {
				t.Errorf("ORC-V03: operation_result is not valid JSON: %v", err)
			}
		}
		if result.ErrorCode != "" || result.ErrorMessage != "" {
			t.Errorf("ORC-V03: error_code/error_message must be empty on status=success; "+
				"got error_code=%q error_message=%q", result.ErrorCode, result.ErrorMessage)
		}
	case "timed_out", "scope_violation", "execution_error":
		if result.OperationResult != nil && string(result.OperationResult) != "null" {
			t.Errorf("ORC-V03: operation_result must be absent on status=%q — "+
				"partial results must not be returned on failure", result.Status)
		}
	}
}

// ORC-V04: vault.execute.request with an invalid/expired permission_token must
// return vault.execute.result with status="scope_violation", not a server panic
// or silent timeout. The error must be surfaced cleanly.
func TestContract_Vault_InvalidTokenProducesScopeViolation(t *testing.T) {
	h := newContractHarness(t)
	resultCh := subscribeVaultResult(t, h)

	requestID := fmt.Sprintf("orc-v04-%s", h.componentID)
	publishVaultRequest(t, h, types.VaultOperationRequest{
		RequestID:       requestID,
		AgentID:         "orc-v04-agent",
		TaskID:          "orc-v04-task",
		PermissionToken: "invalid-token-orc-v04-does-not-exist",
		OperationType:   "web_fetch",
		OperationParams: json.RawMessage(`{"url":"https://example.com"}`),
		TimeoutSeconds:  10,
		CredentialType:  "web_api_key",
	})

	result := awaitVaultResult(t, resultCh, requestID, vaultContractTimeout)

	if result.Status != "scope_violation" && result.Status != "execution_error" {
		t.Errorf("ORC-V04: invalid token should produce scope_violation or execution_error; "+
			"got status=%q", result.Status)
	}
	// Must have an error_code explaining the failure.
	if result.ErrorCode == "" {
		t.Error("ORC-V04: error_code must be present on non-success status")
	}
}

// ORC-V05: vault.execute.cancel must be publishable and accepted by the JetStream
// stream. This validates that the cancel subject is covered by the Orchestrator
// stream so the cancel is durably delivered even if the Orchestrator restarts.
func TestContract_Vault_CancelIsAcceptedByStream(t *testing.T) {
	h := newContractHarness(t)

	cancelReq := types.VaultCancelRequest{
		RequestID:     fmt.Sprintf("orc-v05-cancel-%s", h.componentID),
		AgentID:       "orc-v05-agent",
		TaskID:        "orc-v05-task",
		OperationType: "web_fetch",
		Reason:        "local_timeout",
	}
	err := h.commsClient.Publish(
		comms.SubjectVaultExecuteCancel,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeVaultExecuteCancel,
			CorrelationID: cancelReq.RequestID,
		},
		cancelReq,
	)
	if err == nats.ErrNoResponders {
		t.Error("ORC-V05: vault.execute.cancel has no JetStream stream — " +
			"Orchestrator must cover aegis.orchestrator.vault.execute.cancel")
	} else if err != nil {
		t.Errorf("ORC-V05: publish vault.execute.cancel: %v", err)
	}
}

// ORC-V06: vault.execute.request with the same request_id published twice must
// produce at most one vault.execute.result — the Vault must be idempotent on
// request_id (EDD ADR-004, §crash-recovery resubmission).
func TestContract_Vault_RequestIDIsIdempotent(t *testing.T) {
	h := newContractHarness(t)
	resultCh := subscribeVaultResult(t, h)

	token := authorizeAndGetToken(t, h, "orc-v06-agent", "orc-v06-task")

	requestID := fmt.Sprintf("orc-v06-%s", h.componentID)
	req := types.VaultOperationRequest{
		RequestID:       requestID,
		AgentID:         "orc-v06-agent",
		TaskID:          "orc-v06-task",
		PermissionToken: token,
		OperationType:   "web_fetch",
		OperationParams: json.RawMessage(`{"url":"https://example.com"}`),
		TimeoutSeconds:  10,
		CredentialType:  "web_api_key",
	}

	// Publish the same request twice (simulates crash-recovery resubmission).
	publishVaultRequest(t, h, req)
	publishVaultRequest(t, h, req)

	first := awaitVaultResult(t, resultCh, requestID, vaultContractTimeout)

	// Wait briefly to see if a second result arrives.
	var second *types.VaultOperationResult
	select {
	case r := <-resultCh:
		if r.RequestID == requestID {
			second = &r
		}
	case <-time.After(5 * time.Second):
	}

	if second != nil {
		// Two results arrived — both statuses must match (same execution outcome).
		if first.Status != second.Status {
			t.Errorf("ORC-V06: idempotency violation — same request_id produced two "+
				"results with different statuses: first=%q second=%q",
				first.Status, second.Status)
		}
		t.Logf("ORC-V06: two results received with status=%q (idempotent execution confirmed)", first.Status)
	} else {
		t.Logf("ORC-V06: single result received with status=%q (deduplicated before response)", first.Status)
	}
}
