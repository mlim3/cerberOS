package contract

// Envelope contract tests — ORC-E01 through ORC-E08.
//
// These tests validate the outbound envelope format emitted by internal/comms
// for every publication from the Agents Component. They do NOT require the
// Orchestrator to respond — only a live NATS server is needed.
//
// Spec reference: CLAUDE.md § "Message Envelope (all outbound publications)"
//
// Fields validated:
//   message_id      — UUID v4, globally unique per message
//   message_type    — dot-notation string matching a known MsgType* constant
//   source_component — equals the registered component ID
//   correlation_id  — present and non-empty on messages that require it
//   timestamp       — ISO 8601, within ±60 s of now
//   schema_version  — exactly "1.0"
//   payload         — non-null, valid JSON

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
)

// ORC-E01: message_id must be a valid UUID v4, unique per publication.
func TestContract_Envelope_MessageIDIsUUIDv4(t *testing.T) {
	h := newContractHarness(t)

	outCh := h.observeOutbound(comms.SubjectCredentialRequest)

	req := types.CredentialRequest{
		RequestID:    "e01-req-" + h.componentID,
		AgentID:      "e01-agent",
		TaskID:       "e01-task",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
		TTLSeconds:   300,
	}
	if err := h.commsClient.Publish(
		comms.SubjectCredentialRequest,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeCredentialRequest,
			CorrelationID: req.RequestID,
		},
		req,
	); err != nil {
		t.Fatalf("ORC-E01: publish credential.request: %v", err)
	}

	env := awaitEnvelope(t, outCh, "ORC-E01 credential.request envelope", 5*time.Second)
	assertUUIDV4(t, "ORC-E01 message_id", env.MessageID)
}

// ORC-E02: message_id must be unique — two publications must produce different IDs.
func TestContract_Envelope_MessageIDIsUnique(t *testing.T) {
	h := newContractHarness(t)

	outCh := h.observeOutbound(comms.SubjectCredentialRequest)

	publish := func(requestID string) {
		req := types.CredentialRequest{
			RequestID:    requestID,
			AgentID:      "e02-agent",
			TaskID:       "e02-task",
			Operation:    "authorize",
			SkillDomains: []string{"web"},
		}
		if err := h.commsClient.Publish(
			comms.SubjectCredentialRequest,
			comms.PublishOptions{
				MessageType:   comms.MsgTypeCredentialRequest,
				CorrelationID: requestID,
			},
			req,
		); err != nil {
			t.Errorf("ORC-E02: publish: %v", err)
		}
	}

	publish("e02-req-a")
	env1 := awaitEnvelope(t, outCh, "ORC-E02 first envelope", 5*time.Second)

	publish("e02-req-b")
	env2 := awaitEnvelope(t, outCh, "ORC-E02 second envelope", 5*time.Second)

	if env1.MessageID == env2.MessageID {
		t.Errorf("ORC-E02: message_id collision — both publications produced %q", env1.MessageID)
	}
}

// ORC-E03: source_component must equal the registered component ID.
func TestContract_Envelope_SourceComponentMatchesComponentID(t *testing.T) {
	h := newContractHarness(t)

	outCh := h.observeOutbound(comms.SubjectCredentialRequest)

	req := types.CredentialRequest{
		RequestID:    "e03-req-" + h.componentID,
		AgentID:      "e03-agent",
		TaskID:       "e03-task",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
	}
	if err := h.commsClient.Publish(
		comms.SubjectCredentialRequest,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeCredentialRequest,
			CorrelationID: req.RequestID,
		},
		req,
	); err != nil {
		t.Fatalf("ORC-E03: publish: %v", err)
	}

	env := awaitEnvelope(t, outCh, "ORC-E03 envelope", 5*time.Second)
	if env.SourceComponent != h.componentID {
		t.Errorf("ORC-E03 source_component: want %q, got %q", h.componentID, env.SourceComponent)
	}
}

// ORC-E04: correlation_id must be present and non-empty on vault.execute.request.
// For vault.execute.request, correlation_id MUST be the request_id (ADR-004, CLAUDE.md).
func TestContract_Envelope_CorrelationIDPresentOnVaultRequest(t *testing.T) {
	h := newContractHarness(t)

	outCh := h.observeOutbound(comms.SubjectVaultExecuteRequest)

	requestID := "e04-vault-req-" + h.componentID
	req := types.VaultOperationRequest{
		RequestID:       requestID,
		AgentID:         "e04-agent",
		TaskID:          "e04-task",
		PermissionToken: "e04-token",
		OperationType:   "web_fetch",
		OperationParams: json.RawMessage(`{"url":"https://example.com"}`),
		TimeoutSeconds:  30,
		CredentialType:  "web_api_key",
	}
	if err := h.commsClient.Publish(
		comms.SubjectVaultExecuteRequest,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeVaultExecuteRequest,
			CorrelationID: requestID, // MUST equal request_id per ADR-004
		},
		req,
	); err != nil {
		t.Fatalf("ORC-E04: publish vault.execute.request: %v", err)
	}

	env := awaitEnvelope(t, outCh, "ORC-E04 vault.execute.request envelope", 5*time.Second)

	if env.CorrelationID == "" {
		t.Error("ORC-E04: correlation_id must not be empty on vault.execute.request")
	}
	if env.CorrelationID != requestID {
		t.Errorf("ORC-E04: correlation_id must equal request_id; want %q, got %q",
			requestID, env.CorrelationID)
	}
}

// ORC-E05: timestamp must be ISO 8601 and within ±60 s of now.
func TestContract_Envelope_TimestampIsRecentISO8601(t *testing.T) {
	h := newContractHarness(t)

	outCh := h.observeOutbound(comms.SubjectCredentialRequest)

	publishBefore := time.Now().UTC().Add(-2 * time.Second)
	req := types.CredentialRequest{
		RequestID:    "e05-req-" + h.componentID,
		AgentID:      "e05-agent",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
	}
	if err := h.commsClient.Publish(
		comms.SubjectCredentialRequest,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeCredentialRequest,
			CorrelationID: req.RequestID,
		},
		req,
	); err != nil {
		t.Fatalf("ORC-E05: publish: %v", err)
	}
	publishAfter := time.Now().UTC().Add(2 * time.Second)

	env := awaitEnvelope(t, outCh, "ORC-E05 envelope", 5*time.Second)
	assertISO8601(t, "ORC-E05 timestamp", env.Timestamp)

	ts, err := time.Parse(time.RFC3339Nano, env.Timestamp)
	if err != nil {
		ts, err = time.Parse(time.RFC3339, env.Timestamp)
	}
	if err != nil {
		t.Fatalf("ORC-E05: timestamp parse failed: %v", err)
	}
	if ts.Before(publishBefore) || ts.After(publishAfter) {
		t.Errorf("ORC-E05: timestamp %v out of expected window [%v, %v]",
			ts, publishBefore, publishAfter)
	}
}

// ORC-E06: schema_version must be exactly "1.0".
func TestContract_Envelope_SchemaVersionIs1_0(t *testing.T) {
	h := newContractHarness(t)

	outCh := h.observeOutbound(comms.SubjectCredentialRequest)

	req := types.CredentialRequest{
		RequestID:    "e06-req-" + h.componentID,
		AgentID:      "e06-agent",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
	}
	if err := h.commsClient.Publish(
		comms.SubjectCredentialRequest,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeCredentialRequest,
			CorrelationID: req.RequestID,
		},
		req,
	); err != nil {
		t.Fatalf("ORC-E06: publish: %v", err)
	}

	env := awaitEnvelope(t, outCh, "ORC-E06 envelope", 5*time.Second)
	if env.SchemaVersion != "1.0" {
		t.Errorf("ORC-E06 schema_version: want %q, got %q", "1.0", env.SchemaVersion)
	}
}

// ORC-E07: payload must be non-null valid JSON on every publication.
func TestContract_Envelope_PayloadIsNonNullJSON(t *testing.T) {
	h := newContractHarness(t)

	// Exercise a sample of outbound subjects to verify payload is always present.
	subjects := []struct {
		subject     string
		messageType string
		payload     interface{}
		corrID      string
	}{
		{
			subject:     comms.SubjectCredentialRequest,
			messageType: comms.MsgTypeCredentialRequest,
			corrID:      "e07-cred-" + h.componentID,
			payload: types.CredentialRequest{
				RequestID: "e07-cred-" + h.componentID,
				AgentID:   "e07-agent", Operation: "authorize", SkillDomains: []string{"web"},
			},
		},
		{
			subject:     comms.SubjectTaskAccepted,
			messageType: comms.MsgTypeTaskAccepted,
			corrID:      "e07-task",
			payload: types.TaskAccepted{
				TaskID: "e07-task", AgentID: "e07-agent", AgentType: "new_provision", TraceID: "e07-trace",
			},
		},
		{
			subject:     comms.SubjectAgentStatus,
			messageType: comms.MsgTypeAgentStatus,
			corrID:      "e07-status",
			payload: types.StatusUpdate{
				TaskID: "e07-task", AgentID: "e07-agent", State: "active", TraceID: "e07-trace",
			},
		},
	}

	var wg sync.WaitGroup
	for _, s := range subjects {
		outCh := h.observeOutbound(s.subject)
		wg.Add(1)
		go func(s struct {
			subject, messageType string
			payload              interface{}
			corrID               string
		}) {
			defer wg.Done()
			if err := h.commsClient.Publish(
				s.subject,
				comms.PublishOptions{MessageType: s.messageType, CorrelationID: s.corrID},
				s.payload,
			); err != nil {
				t.Errorf("ORC-E07: publish to %q: %v", s.subject, err)
				return
			}
			env := awaitEnvelope(t, outCh, "ORC-E07 "+s.subject, 5*time.Second)
			if env.Payload == nil || string(env.Payload) == "null" {
				t.Errorf("ORC-E07: payload is null on subject %q", s.subject)
			}
			var check interface{}
			if err := json.Unmarshal(env.Payload, &check); err != nil {
				t.Errorf("ORC-E07: payload is not valid JSON on subject %q: %v", s.subject, err)
			}
		}(s)
	}
	wg.Wait()
}

// ORC-E08: message_type must be a non-empty dot-notation string matching a
// known MsgType* constant. An empty message_type must be rejected by comms.Publish.
func TestContract_Envelope_EmptyMessageTypeIsRejected(t *testing.T) {
	h := newContractHarness(t)

	err := h.commsClient.Publish(
		comms.SubjectCredentialRequest,
		comms.PublishOptions{MessageType: ""}, // empty — must be rejected
		map[string]string{"agent_id": "e08-agent"},
	)
	if err == nil {
		t.Error("ORC-E08: expected error when MessageType is empty, got nil")
	}
}
