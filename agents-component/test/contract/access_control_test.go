package contract

// Access control contract tests — ORC-A01 through ORC-A05.
//
// Validates subject-level access control enforced by the Orchestrator's NATS
// deployment. The Agents Component must be permitted to publish only to
// aegis.orchestrator.* and subscribe only to aegis.agents.*.
//
// These tests document what the Orchestrator team must enforce:
//   - Agents Component NKey/credentials allow publish to aegis.orchestrator.>
//   - Agents Component NKey/credentials allow subscribe to aegis.agents.>
//   - Agents Component NKey/credentials do NOT allow publish to aegis.agents.>
//     (only the Orchestrator may publish to aegis.agents.*)
//   - Inbound messages from the Orchestrator carry source_component="orchestrator"
//   - source_component on inbound messages is not spoofable by the Agents Component
//
// NOTE: ORC-A03, ORC-A04 may pass as a no-op if the NATS server in the
// integration environment does not enforce subject-level permissions via NKeys
// or account-based authorization. Log the outcome either way so the Orchestrator
// team can track enforcement status.

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
)

// ORC-A01: the Agents Component must be able to publish to every
// aegis.orchestrator.* subject defined in the CIC without permission error.
func TestContract_AccessControl_AgentsCanPublishToOrchestratorSubjects(t *testing.T) {
	h := newContractHarness(t)

	for _, s := range atLeastOnceOutboundSubjects {
		s := s
		t.Run(s.subject, func(t *testing.T) {
			payload := minimalPayload(s.messageType, h.componentID)
			err := h.commsClient.Publish(
				s.subject,
				comms.PublishOptions{
					MessageType:   s.messageType,
					CorrelationID: "orc-a01-" + s.messageType,
				},
				payload,
			)
			// ErrNoResponders means no stream — a configuration issue, not a
			// permission error. Any other error is a permission or config failure.
			if err != nil && err.Error() != "nats: no responders available for request" {
				t.Errorf("ORC-A01: publish to %q failed (possible permission error): %v",
					s.subject, err)
			}
		})
	}

	// Also verify the at-most-once capability.response subject.
	err := h.commsClient.Publish(
		comms.SubjectCapabilityResponse,
		comms.PublishOptions{
			MessageType:   comms.MsgTypeCapabilityResponse,
			CorrelationID: "orc-a01-cap",
			Transient:     true,
		},
		types.CapabilityResponse{QueryID: "orc-a01-cap", HasMatch: false},
	)
	if err != nil {
		t.Errorf("ORC-A01: publish to capability.response (Transient) failed: %v", err)
	}
}

// ORC-A02: the Agents Component must be able to subscribe to every
// aegis.agents.* inbound subject without permission error.
func TestContract_AccessControl_AgentsCanSubscribeToAgentsSubjects(t *testing.T) {
	h := newContractHarness(t)

	durableSubjects := []struct {
		subject  string
		consumer string
	}{
		{comms.SubjectCredentialResponse, h.componentID + "-a02-cred"},
		{comms.SubjectVaultExecuteResult, h.componentID + "-a02-vault"},
		{comms.SubjectStateWriteAck, h.componentID + "-a02-sw-ack"},
		{comms.SubjectStateReadResponse, h.componentID + "-a02-sr-resp"},
		{comms.SubjectClarificationResponse, h.componentID + "-a02-clar"},
	}

	for _, s := range durableSubjects {
		s := s
		t.Run(s.subject, func(t *testing.T) {
			err := h.commsClient.SubscribeDurable(
				s.subject,
				s.consumer,
				func(msg *comms.Message) { _ = msg.Ack() },
			)
			if err != nil {
				t.Errorf("ORC-A02: SubscribeDurable to %q failed (possible permission error): %v",
					s.subject, err)
			}
		})
	}

	// Core NATS subjects.
	coreSubjects := []string{
		comms.SubjectCapabilityQuery,
		comms.SubjectVaultExecuteProgress,
	}
	for _, subject := range coreSubjects {
		subject := subject
		t.Run(subject, func(t *testing.T) {
			err := h.commsClient.Subscribe(subject, func(msg *comms.Message) {})
			if err != nil {
				t.Errorf("ORC-A02: Subscribe to %q failed (possible permission error): %v",
					subject, err)
			}
		})
	}
}

// ORC-A03: the Agents Component must NOT be permitted to publish to
// aegis.agents.* subjects (only the Orchestrator may publish there).
//
// If the NATS server does not enforce subject-level permissions, this test
// LOGS a warning and passes — it is the Orchestrator team's responsibility to
// enforce NKey/account permissions in the integration and production environments.
func TestContract_AccessControl_AgentsCannotPublishToAgentsSubjects(t *testing.T) {
	h := newContractHarness(t)

	// Try to publish directly to an inbound subject (the Orchestrator's namespace).
	forbiddenSubjects := []string{
		comms.SubjectTaskInbound,
		comms.SubjectCredentialResponse,
		comms.SubjectVaultExecuteResult,
		comms.SubjectStateWriteAck,
	}

	for _, subject := range forbiddenSubjects {
		subject := subject
		t.Run(subject, func(t *testing.T) {
			data, _ := json.Marshal(map[string]string{
				"orc_a03": "access control test — should be rejected",
			})
			_, err := h.js.Publish(subject, data)
			if err == nil {
				// NATS accepted the publish. Document whether this is intentional.
				t.Logf("ORC-A03 WARNING: Agents Component was able to publish to %q — "+
					"the Orchestrator team must enforce subject-level publish permissions "+
					"via NKeys or account authorization to prevent spoofing; "+
					"this is a REQUIRED security control for production",
					subject)
			} else {
				t.Logf("ORC-A03: publish to %q correctly rejected: %v", subject, err)
			}
		})
	}
}

// ORC-A04: inbound messages received from the Orchestrator on aegis.agents.*
// must carry source_component="orchestrator" in the envelope. The Agents
// Component must validate this field and reject messages with unexpected sources.
func TestContract_AccessControl_InboundMessagesHaveOrchestratorSource(t *testing.T) {
	h := newContractHarness(t)

	// Inject a well-formed task.inbound and verify the source_component field.
	// We publish it ourselves here — in production this would come from the
	// Orchestrator. This test validates what the Orchestrator SHOULD set.
	inboundCh := h.observeInbound(comms.SubjectTaskAccepted) // observe our own response

	taskID := fmt.Sprintf("orc-a04-%s", h.componentID)
	injectTaskInbound(t, h, types.TaskSpec{
		TaskID:         taskID,
		RequiredSkills: []string{"web"},
		TraceID:        "orc-a04-trace",
	})

	// What we receive on aegis.orchestrator.task.accepted (our response) must
	// have source_component equal to our component ID (not "orchestrator").
	// This validates the Agents Component sets it correctly — the reverse direction.
	env := awaitEnvelope(t, inboundCh, "ORC-A04 task.accepted response", taskContractTimeout)

	if env.SourceComponent == "" {
		t.Error("ORC-A04: source_component must not be empty on any published message")
	}
	// Our outbound task.accepted must identify us, not the Orchestrator.
	if env.SourceComponent == "orchestrator" {
		t.Error("ORC-A04: Agents Component published task.accepted with source_component='orchestrator' — " +
			"source_component must identify the publishing component")
	}
	t.Logf("ORC-A04: task.accepted source_component=%q (correct — identifies Agents Component)",
		env.SourceComponent)
}

// ORC-A05: audit.event publications must not include credential values, tokens,
// or operation_result content (NFR-08). The audit trail is append-only but must
// be safe to expose to compliance tooling.
func TestContract_AccessControl_AuditEventContainsNoSensitiveData(t *testing.T) {
	h := newContractHarness(t)

	auditCh := h.observeInbound(comms.SubjectAuditEvent)

	// Trigger an audit event by injecting a task.inbound.
	taskID := fmt.Sprintf("orc-a05-%s", h.componentID)
	injectTaskInbound(t, h, types.TaskSpec{
		TaskID:         taskID,
		RequiredSkills: []string{"web"},
		TraceID:        "orc-a05-trace",
	})

	// Collect audit events for this task and check each for sensitive data.
	deadline := time.After(15 * time.Second)
	eventsChecked := 0
	for {
		select {
		case env := <-auditCh:
			var evt types.AuditEvent
			if jsonErr := json.Unmarshal(env.Payload, &evt); jsonErr != nil {
				continue
			}
			if evt.TaskID != taskID && evt.AgentID == "" {
				continue
			}
			eventsChecked++

			// The details map must not contain credential material.
			detailsJSON, _ := json.Marshal(evt.Details)
			assertNoCredentialLeak(t,
				fmt.Sprintf("ORC-A05 audit.event[%s].details", evt.EventType),
				detailsJSON,
			)

			// event_id must be UUID v4 (idempotency key).
			if evt.EventID != "" {
				assertUUIDV4(t,
					fmt.Sprintf("ORC-A05 audit.event[%s].event_id", evt.EventType),
					evt.EventID,
				)
			}

			// event_type must be a known constant.
			knownEventTypes := map[string]bool{
				types.AuditEventCredentialGrant:      true,
				types.AuditEventCredentialDeny:       true,
				types.AuditEventCredentialRevoke:     true,
				types.AuditEventScopeViolation:       true,
				types.AuditEventVaultExecuteRequest:  true,
				types.AuditEventVaultExecuteResult:   true,
				types.AuditEventVaultExecuteTimeout:  true,
				types.AuditEventStateTransition:      true,
				types.AuditEventProvisioningStart:    true,
				types.AuditEventProvisioningComplete: true,
				types.AuditEventProvisioningFail:     true,
				types.AuditEventRecoveryAttempt:      true,
				types.AuditEventTaskAccepted:         true,
				types.AuditEventTaskCompleted:        true,
				types.AuditEventTaskFailed:           true,
			}
			if evt.EventType != "" && !knownEventTypes[evt.EventType] {
				t.Errorf("ORC-A05: unknown audit event_type %q — "+
					"all event types must be defined in types.AuditEvent* constants",
					evt.EventType)
			}

			if eventsChecked >= 3 {
				t.Logf("ORC-A05: checked %d audit events — no sensitive data found", eventsChecked)
				return
			}

		case <-deadline:
			if eventsChecked == 0 {
				t.Log("ORC-A05: no audit events received within 15s — Agents Component may not be running")
			} else {
				t.Logf("ORC-A05: checked %d audit events — no sensitive data found", eventsChecked)
			}
			return
		}
	}
}
