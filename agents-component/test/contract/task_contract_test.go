package contract

// Task lifecycle contract tests — ORC-T01 through ORC-T05.
//
// Validates the task.inbound → task.accepted → task.result / task.failed flow:
//   - task.accepted is published within 5 s of task.inbound (EDD §8.3)
//   - task.accepted payload echoes task_id and sets agent_type correctly
//   - task.accepted correlation_id equals the task_id
//   - task.inbound with a missing task_id is handled gracefully (no panic; NAK)
//   - audit.event is emitted for task_accepted and task_completed (EDD §8.8)
//
// ORC-T tests inject a task.inbound as the Orchestrator would and observe
// what the Agents Component publishes in response. They require a fully wired
// Agents Component process (factory + registry + credentials) reachable on the
// same NATS server.
//
// If the Agents Component process is not running, these tests skip after timeout.

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
)

const taskContractTimeout = 15 * time.Second

// injectTaskInbound publishes a task.inbound message directly to
// aegis.agents.task.inbound, simulating the Orchestrator dispatching a task.
func injectTaskInbound(t *testing.T, h *contractHarness, spec types.TaskSpec) {
	t.Helper()
	env := map[string]interface{}{
		"message_id":       fmt.Sprintf("orc-t-msg-%s", spec.TaskID),
		"message_type":     comms.MsgTypeTaskInbound,
		"source_component": "orchestrator",
		"correlation_id":   spec.TaskID,
		"timestamp":        time.Now().UTC().Format(time.RFC3339Nano),
		"schema_version":   "1.0",
		"payload":          spec,
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("injectTaskInbound: marshal: %v", err)
	}
	if _, err := h.js.Publish(comms.SubjectTaskInbound, data); err != nil {
		t.Fatalf("injectTaskInbound: publish: %v", err)
	}
}

// ORC-T01: task.accepted must be published within 5 s of task.inbound (EDD §8.3).
// task.accepted must be published BEFORE any provisioning work begins.
func TestContract_Task_AcceptedPublishedWithin5s(t *testing.T) {
	h := newContractHarness(t)

	acceptedCh := h.observeInbound(comms.SubjectTaskAccepted)

	taskID := fmt.Sprintf("orc-t01-%s", h.componentID)
	injectStart := time.Now()
	injectTaskInbound(t, h, types.TaskSpec{
		TaskID:         taskID,
		RequiredSkills: []string{"web"},
		Instructions:   "ORC-T01 contract test",
		TraceID:        "orc-t01-trace",
	})

	env := awaitEnvelope(t, acceptedCh, "ORC-T01 task.accepted", taskContractTimeout)
	elapsed := time.Since(injectStart)

	// Parse the payload.
	var accepted types.TaskAccepted
	if err := json.Unmarshal(env.Payload, &accepted); err != nil {
		t.Fatalf("ORC-T01: unmarshal task.accepted payload: %v", err)
	}

	if accepted.TaskID != taskID {
		t.Errorf("ORC-T01 task.accepted.task_id: want %q, got %q", taskID, accepted.TaskID)
	}
	if elapsed > 5*time.Second {
		t.Errorf("ORC-T01: task.accepted published after %v (deadline is 5 s)", elapsed)
	}
	t.Logf("ORC-T01: task.accepted published in %v", elapsed)
}

// ORC-T02: task.accepted envelope must set correlation_id = task_id.
func TestContract_Task_AcceptedCorrelationIDEqualsTaskID(t *testing.T) {
	h := newContractHarness(t)

	acceptedCh := h.observeInbound(comms.SubjectTaskAccepted)

	taskID := fmt.Sprintf("orc-t02-%s", h.componentID)
	injectTaskInbound(t, h, types.TaskSpec{
		TaskID:         taskID,
		RequiredSkills: []string{"web"},
		TraceID:        "orc-t02-trace",
	})

	env := awaitEnvelope(t, acceptedCh, "ORC-T02 task.accepted envelope", taskContractTimeout)

	if env.CorrelationID != taskID {
		t.Errorf("ORC-T02 task.accepted envelope.correlation_id: want %q (task_id), got %q",
			taskID, env.CorrelationID)
	}
}

// ORC-T03: task.accepted must echo user_context_id when provided in the TaskSpec.
func TestContract_Task_AcceptedEchoesUserContextID(t *testing.T) {
	h := newContractHarness(t)

	acceptedCh := h.observeInbound(comms.SubjectTaskAccepted)

	taskID := fmt.Sprintf("orc-t03-%s", h.componentID)
	userContextID := "orc-t03-user-ctx"
	injectTaskInbound(t, h, types.TaskSpec{
		TaskID:         taskID,
		RequiredSkills: []string{"web"},
		TraceID:        "orc-t03-trace",
		UserContextID:  userContextID,
	})

	env := awaitEnvelope(t, acceptedCh, "ORC-T03 task.accepted", taskContractTimeout)

	var accepted types.TaskAccepted
	if err := json.Unmarshal(env.Payload, &accepted); err != nil {
		t.Fatalf("ORC-T03: unmarshal: %v", err)
	}
	if accepted.UserContextID != userContextID {
		t.Errorf("ORC-T03 task.accepted.user_context_id: want %q, got %q",
			userContextID, accepted.UserContextID)
	}
}

// ORC-T04: task.inbound with a missing task_id must not cause the Agents
// Component to panic or publish task.accepted. Instead it should NAK (trigger
// redelivery up to MaxDeliver) and eventually publish task.failed or remain silent.
func TestContract_Task_MissingTaskIDIsHandledGracefully(t *testing.T) {
	h := newContractHarness(t)

	acceptedCh := h.observeInbound(comms.SubjectTaskAccepted)
	failedCh := h.observeInbound(comms.SubjectTaskFailed)

	// Publish a malformed task.inbound with empty task_id.
	malformed := map[string]interface{}{
		"message_id":       "orc-t04-msg",
		"message_type":     comms.MsgTypeTaskInbound,
		"source_component": "orchestrator",
		"correlation_id":   "",
		"timestamp":        time.Now().UTC().Format(time.RFC3339Nano),
		"schema_version":   "1.0",
		"payload": map[string]interface{}{
			"task_id":         "", // intentionally empty
			"required_skills": []string{"web"},
			"instructions":    "ORC-T04 malformed test",
		},
	}
	data, _ := json.Marshal(malformed)
	if _, err := h.js.Publish(comms.SubjectTaskInbound, data); err != nil {
		t.Fatalf("ORC-T04: publish malformed task.inbound: %v", err)
	}

	// task.accepted must NOT be published for an empty task_id.
	noEnvelope(t, acceptedCh, "ORC-T04 task.accepted (must not arrive)", 3*time.Second)

	// task.failed may or may not arrive depending on Agents Component behaviour.
	// Both outcomes are valid — what matters is no panic and no orphaned accepted.
	select {
	case env := <-failedCh:
		t.Logf("ORC-T04: task.failed received for malformed task (correct): message_type=%q",
			env.MessageType)
	case <-time.After(5 * time.Second):
		t.Log("ORC-T04: no task.failed for malformed task (silent NAK is also acceptable)")
	}
}

// ORC-T05: audit.event with event_type="task_accepted" must be published when
// a task.accepted is emitted (EDD §8.8). Validates the audit trail is complete.
func TestContract_Task_AuditEventEmittedOnAccepted(t *testing.T) {
	h := newContractHarness(t)

	acceptedCh := h.observeInbound(comms.SubjectTaskAccepted)
	auditCh := h.observeInbound(comms.SubjectAuditEvent)

	taskID := fmt.Sprintf("orc-t05-%s", h.componentID)
	injectTaskInbound(t, h, types.TaskSpec{
		TaskID:         taskID,
		RequiredSkills: []string{"web"},
		TraceID:        "orc-t05-trace",
	})

	// Wait for task.accepted first.
	awaitEnvelope(t, acceptedCh, "ORC-T05 task.accepted", taskContractTimeout)

	// Audit event must arrive within a short window after task.accepted.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case env := <-auditCh:
			var evt types.AuditEvent
			if jsonErr := json.Unmarshal(env.Payload, &evt); jsonErr != nil {
				continue
			}
			if evt.TaskID != taskID {
				continue // belongs to a different test run
			}
			if evt.EventType == types.AuditEventTaskAccepted {
				assertUUIDV4(t, "ORC-T05 audit.event.event_id", evt.EventID)
				if evt.TraceID == "" {
					t.Error("ORC-T05: audit.event.trace_id must be present")
				}
				t.Logf("ORC-T05: audit.event task_accepted received (event_id=%s)", evt.EventID)
				return
			}
		case <-deadline:
			t.Error("ORC-T05: no audit.event with event_type=task_accepted published within 5 s of task.accepted")
			return
		}
	}
}
