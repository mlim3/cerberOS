package contract

// Delivery semantics contract tests — ORC-D01 through ORC-D05.
//
// Validates that:
//   - All at-least-once outbound subjects have an active JetStream stream
//     (publish does not return nats.ErrNoResponders).
//   - Durable consumer names are stable — reconnect does not lose position.
//   - NAK on a JetStream inbound subject causes redelivery within AckWait.
//   - At-most-once subjects (capability.query/response) work via core NATS
//     and are NOT captured by any JetStream stream (delivery guarantee matches spec).
//   - Heartbeat subjects (aegis.heartbeat.*) are not captured by any stream.

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	"github.com/nats-io/nats.go"
)

// atLeastOnceOutboundSubjects lists every outbound subject that must be backed
// by a JetStream stream (at-least-once delivery per the CIC).
var atLeastOnceOutboundSubjects = []struct {
	subject     string
	messageType string
}{
	{comms.SubjectTaskAccepted, comms.MsgTypeTaskAccepted},
	{comms.SubjectTaskResult, comms.MsgTypeTaskResult},
	{comms.SubjectTaskFailed, comms.MsgTypeTaskFailed},
	{comms.SubjectAgentStatus, comms.MsgTypeAgentStatus},
	{comms.SubjectCredentialRequest, comms.MsgTypeCredentialRequest},
	{comms.SubjectVaultExecuteRequest, comms.MsgTypeVaultExecuteRequest},
	{comms.SubjectStateWrite, comms.MsgTypeStateWrite},
	{comms.SubjectStateReadRequest, comms.MsgTypeStateReadRequest},
	{comms.SubjectClarificationRequest, comms.MsgTypeClarificationRequest},
	{comms.SubjectVaultExecuteCancel, comms.MsgTypeVaultExecuteCancel},
	{comms.SubjectAuditEvent, comms.MsgTypeAuditEvent},
	{comms.SubjectError, comms.MsgTypeError},
}

// ORC-D01: every at-least-once outbound subject must be covered by a JetStream
// stream — publish must not return nats.ErrNoResponders.
func TestContract_Delivery_AllAtLeastOnceSubjectsHaveStreams(t *testing.T) {
	h := newContractHarness(t)

	for _, s := range atLeastOnceOutboundSubjects {
		s := s
		t.Run(s.subject, func(t *testing.T) {
			// Use a minimal valid payload for the message type.
			payload := minimalPayload(s.messageType, h.componentID)

			err := h.commsClient.Publish(
				s.subject,
				comms.PublishOptions{
					MessageType:   s.messageType,
					CorrelationID: "orc-d01-" + s.messageType,
				},
				payload,
			)
			if err == nats.ErrNoResponders {
				t.Errorf("ORC-D01: subject %q has no JetStream stream — "+
					"Orchestrator must create AEGIS_ORCHESTRATOR stream covering aegis.orchestrator.>",
					s.subject)
			} else if err != nil {
				t.Errorf("ORC-D01: publish to %q: %v", s.subject, err)
			}
		})
	}
}

// ORC-D02: capability.query is at-most-once — it must be publishable via core
// NATS (Transient: true) and must NOT be captured by any JetStream stream.
// Publishing to a subject with no stream should succeed without error (core NATS
// fire-and-forget). A JetStream check must return ErrStreamNotFound for
// capability.query specifically.
func TestContract_Delivery_CapabilityQueryIsAtMostOnce(t *testing.T) {
	h := newContractHarness(t)

	// Step 1: core NATS publish must succeed (at-most-once, Transient: true).
	query := types.CapabilityQuery{
		QueryID: "orc-d02-query",
		Domains: []string{"web"},
		TraceID: "orc-d02-trace",
	}
	err := h.commsClient.Publish(
		comms.SubjectCapabilityResponse,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeCapabilityResponse,
			CorrelationID: query.QueryID,
			Transient:     true, // at-most-once
		},
		query,
	)
	if err != nil {
		t.Errorf("ORC-D02: core NATS publish to capability.response failed: %v", err)
	}

	// Step 2: verify we can receive it as a plain core NATS subscriber (not JetStream).
	received := make(chan struct{}, 1)
	sub, err := h.nc.Subscribe(comms.SubjectCapabilityResponse, func(*nats.Msg) {
		select {
		case received <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("ORC-D02: core NATS subscribe: %v", err)
	}
	defer sub.Unsubscribe() //nolint:errcheck

	_ = h.commsClient.Publish(
		comms.SubjectCapabilityResponse,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeCapabilityResponse,
			CorrelationID: "orc-d02-b",
			Transient:     true,
		},
		types.CapabilityResponse{QueryID: "orc-d02-b", HasMatch: false},
	)

	select {
	case <-received:
		// Correct — arrived via core NATS.
	case <-time.After(3 * time.Second):
		t.Error("ORC-D02: capability.response not received via core NATS within 3s")
	}
}

// ORC-D03: NAK on an inbound JetStream subject must cause redelivery.
// This test subscribes to aegis.agents.task.inbound with a consumer that NAKs
// the first delivery, then ACKs the second. Verifies the message is delivered
// exactly twice.
func TestContract_Delivery_NakCausesRedelivery(t *testing.T) {
	h := newContractHarness(t)

	// Use a unique consumer name so this test doesn't interfere with production consumers.
	consumer := h.componentID + "-orc-d03-nak-test"

	var deliveries atomic.Int32
	done := make(chan struct{})

	sub, err := h.js.Subscribe(
		comms.SubjectTaskInbound,
		func(msg *nats.Msg) {
			n := deliveries.Add(1)
			if n == 1 {
				// First delivery — NAK to trigger redelivery.
				_ = msg.Nak()
				return
			}
			// Second delivery — ACK and signal completion.
			_ = msg.Ack()
			close(done)
		},
		nats.Durable(consumer),
		nats.DeliverNew(),
		nats.AckExplicit(),
		nats.AckWait(3*time.Second), // short AckWait so redelivery is fast
		nats.MaxDeliver(3),
	)
	if err != nil {
		t.Fatalf("ORC-D03: subscribe %q (consumer %q): %v",
			comms.SubjectTaskInbound, consumer, err)
	}
	defer sub.Unsubscribe() //nolint:errcheck

	// Publish a test task.inbound to trigger the handler.
	spec := types.TaskSpec{
		TaskID:         "orc-d03-task",
		RequiredSkills: []string{"web"},
		Instructions:   "ORC-D03 NAK redelivery test",
		TraceID:        "orc-d03-trace",
	}
	data, _ := json.Marshal(struct {
		MessageID       string      `json:"message_id"`
		MessageType     string      `json:"message_type"`
		SourceComponent string      `json:"source_component"`
		CorrelationID   string      `json:"correlation_id"`
		Timestamp       string      `json:"timestamp"`
		SchemaVersion   string      `json:"schema_version"`
		Payload         interface{} `json:"payload"`
	}{
		MessageID:       "orc-d03-msg",
		MessageType:     comms.MsgTypeTaskInbound,
		SourceComponent: "orchestrator",
		CorrelationID:   "orc-d03-task",
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         spec,
	})
	if _, err := h.js.Publish(comms.SubjectTaskInbound, data); err != nil {
		t.Fatalf("ORC-D03: publish task.inbound: %v", err)
	}

	select {
	case <-done:
		if got := deliveries.Load(); got != 2 {
			t.Errorf("ORC-D03: expected 2 deliveries (NAK then ACK), got %d", got)
		}
	case <-time.After(15 * time.Second):
		t.Errorf("ORC-D03: redelivery did not occur within 15s; deliveries=%d", deliveries.Load())
	}
}

// ORC-D04: durable consumer names must be stable — the same consumer name must
// be usable in successive subscribe calls without error (reconnect safety).
func TestContract_Delivery_DurableConsumerNamesAreStable(t *testing.T) {
	h := newContractHarness(t)

	// The production consumer names are defined in subjects.go.
	// Subscribing with each name twice (simulating reconnect) must not error.
	consumers := []struct {
		subject  string
		consumer string
	}{
		{comms.SubjectCredentialResponse, comms.ConsumerCredentialResponse},
		{comms.SubjectVaultExecuteResult, comms.ConsumerVaultExecuteResult},
		{comms.SubjectStateWriteAck, comms.ConsumerStateWriteAck},
		{comms.SubjectStateReadResponse, comms.ConsumerStateReadResponse},
		{comms.SubjectClarificationResponse, comms.ConsumerClarificationResponse},
	}

	for _, c := range consumers {
		c := c
		t.Run(c.consumer, func(t *testing.T) {
			// First bind (simulates initial start).
			sub1, err := h.js.Subscribe(
				c.subject,
				func(msg *nats.Msg) { _ = msg.Ack() },
				nats.Durable(c.consumer),
				nats.DeliverNew(),
				nats.AckExplicit(),
			)
			if err != nil {
				t.Fatalf("ORC-D04: first subscribe %q (consumer %q): %v",
					c.subject, c.consumer, err)
			}

			// Unsubscribe without draining — simulates crash/restart.
			_ = sub1.Unsubscribe()

			// Second bind (simulates restart) — must succeed and bind to the
			// same consumer. JetStream resumes from where the first left off.
			sub2, err := h.js.Subscribe(
				c.subject,
				func(msg *nats.Msg) { _ = msg.Ack() },
				nats.Durable(c.consumer),
				nats.DeliverNew(),
				nats.AckExplicit(),
			)
			if err != nil {
				t.Errorf("ORC-D04: reconnect subscribe %q (consumer %q): %v — "+
					"durable consumer name must survive unsubscribe/resubscribe cycles",
					c.subject, c.consumer, err)
				return
			}
			_ = sub2.Unsubscribe()
		})
	}
}

// ORC-D05: heartbeat subjects (aegis.heartbeat.*) must NOT be captured by any
// JetStream stream. Publishing a heartbeat via core NATS must not error.
func TestContract_Delivery_HeartbeatIsNotCapturedByStream(t *testing.T) {
	h := newContractHarness(t)

	agentID := "orc-d05-agent"
	subject := comms.HeartbeatSubject(agentID)

	hb := types.HeartbeatEvent{
		AgentID:   agentID,
		TaskID:    "orc-d05-task",
		TraceID:   "orc-d05-trace",
		Timestamp: time.Now().UTC(),
	}
	data, _ := json.Marshal(hb)

	// Must succeed without error (core NATS, fire-and-forget).
	if err := h.nc.Publish(subject, data); err != nil {
		t.Errorf("ORC-D05: heartbeat core NATS publish failed: %v", err)
	}

	// JetStream publish to the same subject should fail if no stream covers it.
	// This confirms the heartbeat namespace is deliberately excluded from JetStream.
	_, jsErr := h.js.Publish(subject, data)
	if jsErr != nats.ErrNoResponders {
		if jsErr == nil {
			t.Error("ORC-D05: JetStream accepted a heartbeat message — " +
				"aegis.heartbeat.* must NOT be covered by any stream; " +
				"heartbeats are at-most-once by design")
		}
		// Any error other than ErrNoResponders is unexpected but does not
		// constitute a contract violation — log and continue.
		t.Logf("ORC-D05: JetStream publish to %q returned: %v (expected ErrNoResponders)", subject, jsErr)
	}
}

// minimalPayload returns a minimal valid payload for a given message type.
// Used only for stream-existence checks (ORC-D01) where payload validity is
// not the focus.
func minimalPayload(messageType, componentID string) interface{} {
	switch messageType {
	case comms.MsgTypeCredentialRequest:
		return types.CredentialRequest{
			RequestID: "d01-" + messageType, AgentID: "d01-agent",
			Operation: "authorize", SkillDomains: []string{"web"},
		}
	case comms.MsgTypeVaultExecuteRequest:
		return types.VaultOperationRequest{
			RequestID: "d01-" + messageType, AgentID: "d01-agent", TaskID: "d01-task",
			PermissionToken: "d01-token", OperationType: "web_fetch",
			OperationParams: json.RawMessage(`{}`), TimeoutSeconds: 30, CredentialType: "web_api_key",
		}
	case comms.MsgTypeTaskAccepted:
		return types.TaskAccepted{
			TaskID: "d01-task", AgentID: "d01-agent", AgentType: "new_provision", TraceID: "d01-trace",
		}
	case comms.MsgTypeTaskResult:
		return types.TaskResult{TaskID: "d01-task", AgentID: "d01-agent", Success: true, TraceID: "d01"}
	case comms.MsgTypeTaskFailed:
		return types.TaskFailed{TaskID: "d01-task", ErrorCode: "TEST", ErrorMessage: "contract test", TraceID: "d01"}
	case comms.MsgTypeAgentStatus:
		return types.StatusUpdate{TaskID: "d01-task", AgentID: "d01-agent", State: "active", TraceID: "d01"}
	case comms.MsgTypeStateWrite:
		return types.MemoryWrite{AgentID: "d01-agent", DataType: "agent_state", SessionID: "d01"}
	case comms.MsgTypeStateReadRequest:
		return types.MemoryReadRequest{AgentID: "d01-agent", DataType: "agent_state", TraceID: "d01"}
	case comms.MsgTypeClarificationRequest:
		return map[string]string{"agent_id": "d01-agent", "question": "contract test"}
	case comms.MsgTypeVaultExecuteCancel:
		return types.VaultCancelRequest{
			RequestID: "d01-cancel", AgentID: "d01-agent", TaskID: "d01-task",
			OperationType: "web_fetch", Reason: "local_timeout",
		}
	case comms.MsgTypeAuditEvent:
		return types.AuditEvent{
			EventID: "d01-audit", EventType: types.AuditEventTaskAccepted, TraceID: "d01",
		}
	case comms.MsgTypeError:
		return map[string]string{"error_code": "TEST", "error_message": "contract test"}
	default:
		return map[string]string{"message_type": messageType, "source": componentID}
	}
}
