// Package main — steering.go implements the mid-task agent steering subscriber
// (OQ-08). The Steerer subscribes to the per-agent steering subject on core NATS
// and buffers the latest directive for consumption by the ReAct loop during the
// Act phase.
//
// Wire protocol:
//   - Inbound:  aegis.agents.steering.<agent_id>  (core NATS, at-most-once)
//   - Outbound: aegis.orchestrator.steering.ack   (JetStream, at-least-once)
//
// Design constraints:
//   - At-most-once delivery is intentional. Stale steering directives that arrive
//     after the target Act phase are silently dropped. The channel buffer is 1;
//     a new directive replaces a pending one if the loop hasn't consumed it yet
//     (highest-priority steering wins).
//   - The Steerer shares the VaultExecutor's NATS connection when possible but
//     opens its own when ve is nil (non-credentialed agents still need steering).
//   - Steering never enters the LLM context directly — the loop injects a
//     structured [STEERING] message into history so the model sees it cleanly.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	nats "github.com/nats-io/nats.go"
)

// Steerer subscribes to aegis.agents.steering.<agent_id> and buffers incoming
// SteeringDirective messages for the ReAct loop. One instance per agent-process.
type Steerer struct {
	nc      *nats.Conn
	js      nats.JetStreamContext // for ack publishing
	agentID string
	taskID  string
	traceID string
	log     *slog.Logger

	// pending buffers the most recent unprocessed directive. Buffer size 1:
	// if the loop is busy, a newer directive replaces the pending one (the
	// drain in the NATS handler ensures the channel never blocks).
	pending chan types.SteeringDirective
}

// NewSteerer connects to NATS, subscribes to the per-agent steering subject,
// and returns a Steerer ready to deliver directives to the ReAct loop.
//
// Required environment:
//
//	AEGIS_NATS_URL  — NATS server (injected by Lifecycle Manager)
//	AEGIS_AGENT_ID  — this agent's identity (injected by Lifecycle Manager)
//
// Returns nil (non-fatal) if either env var is absent or NATS is unreachable.
// A nil Steerer causes the loop to run without steering support — all existing
// behaviour is preserved.
func NewSteerer(log *slog.Logger, taskID, traceID string) *Steerer {
	natsURL := os.Getenv("AEGIS_NATS_URL")
	agentID := os.Getenv("AEGIS_AGENT_ID")
	if natsURL == "" || agentID == "" {
		log.Warn("steerer disabled: AEGIS_NATS_URL or AEGIS_AGENT_ID not set")
		return nil
	}

	nc, err := nats.Connect(natsURL,
		nats.Name("aegis-steerer-"+agentID),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		log.Warn("steerer: NATS connect failed — steering disabled", "error", err)
		return nil
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		log.Warn("steerer: JetStream init failed — steering disabled", "error", err)
		return nil
	}

	s := &Steerer{
		nc:      nc,
		js:      js,
		agentID: agentID,
		taskID:  taskID,
		traceID: traceID,
		log:     log,
		pending: make(chan types.SteeringDirective, 1),
	}

	subject := comms.SteeringSubject(agentID)
	if _, err := nc.Subscribe(subject, s.handle); err != nil {
		nc.Close()
		log.Warn("steerer: subscribe failed — steering disabled",
			"subject", subject, "error", err)
		return nil
	}

	log.Info("steerer ready", "agent_id", agentID, "subject", subject)
	return s
}

// handle is the NATS subscription callback. It unwraps the envelope, validates
// the directive, and delivers it to the pending channel. If a lower-priority
// directive is already pending, it is replaced by the new one.
func (s *Steerer) handle(msg *nats.Msg) {
	var env struct {
		MessageType   string          `json:"message_type"`
		CorrelationID string          `json:"correlation_id"`
		Payload       json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		s.log.Warn("steerer: unmarshal envelope failed", "error", err)
		return
	}

	var d types.SteeringDirective
	if err := json.Unmarshal(env.Payload, &d); err != nil {
		s.log.Warn("steerer: unmarshal directive failed", "error", err)
		return
	}

	// Reject directives not addressed to this agent.
	if d.AgentID != s.agentID {
		return
	}

	s.log.Info("steerer: directive received",
		"directive_id", d.DirectiveID,
		"type", d.Type,
		"interrupt_tool", d.InterruptTool,
		"priority", d.Priority,
	)

	// Replace a pending lower-priority directive; drop if equal or higher
	// (the in-flight directive wins on ties — callers should use unique IDs).
	select {
	case existing := <-s.pending:
		if d.Priority > existing.Priority {
			// New directive wins — deliver it.
			s.pending <- d
			s.log.Info("steerer: replaced lower-priority pending directive",
				"evicted_id", existing.DirectiveID,
				"new_id", d.DirectiveID,
			)
		} else {
			// Existing directive wins — put it back and drop the new one.
			s.pending <- existing
			s.log.Info("steerer: dropped lower-priority directive",
				"dropped_id", d.DirectiveID,
				"kept_id", existing.DirectiveID,
			)
		}
	default:
		// No pending directive — deliver immediately.
		s.pending <- d
	}
}

// Chan returns the read-only channel the ReAct loop selects on during the Act
// phase. Receives a SteeringDirective when the Orchestrator sends one.
func (s *Steerer) Chan() <-chan types.SteeringDirective {
	return s.pending
}

// Ack publishes a SteeringAck to aegis.orchestrator.steering.ack (JetStream,
// at-least-once) to confirm receipt and application of a directive.
// Best-effort: failures are logged but never propagated.
// A nil receiver or nil JetStream context (e.g. in tests without NATS) is a no-op.
func (s *Steerer) Ack(directiveID, status, reason string) {
	if s == nil || s.js == nil {
		return
	}
	ack := types.SteeringAck{
		DirectiveID: directiveID,
		AgentID:     s.agentID,
		TaskID:      s.taskID,
		TraceID:     s.traceID,
		Status:      status,
		Reason:      reason,
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeSteeringAck,
		SourceComponent: "agents",
		CorrelationID:   directiveID,
		TraceID:         s.traceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         ack,
	}
	data, err := json.Marshal(env)
	if err != nil {
		s.log.Warn("steerer: ack marshal failed", "directive_id", directiveID, "error", err)
		return
	}
	if _, err := s.js.Publish(comms.SubjectSteeringAck, data); err != nil {
		s.log.Warn("steerer: ack publish failed", "directive_id", directiveID, "error", err)
	}
}

// Close drains the NATS connection.
func (s *Steerer) Close() {
	if s.nc != nil {
		s.nc.Close()
	}
}

// formatSteeringMessage renders a SteeringDirective as the structured message
// text injected into the LLM history. It is a user-turn message so the model
// treats it with the same weight as user instructions.
func formatSteeringMessage(d *types.SteeringDirective) string {
	switch d.Type {
	case "cancel":
		return fmt.Sprintf(
			"[STEERING: task cancelled by operator]\n"+
				"The task has been cancelled. Reason: %s\n"+
				"Call task_complete with any partial results you have so far.",
			d.Instructions,
		)
	case "abort_tool":
		return fmt.Sprintf(
			"[STEERING: tool execution was interrupted by operator]\n"+
				"The in-flight tool call was aborted. New directive: %s\n"+
				"Continue from this point with the updated instructions.",
			d.Instructions,
		)
	default: // "redirect" | "inject_context"
		return fmt.Sprintf(
			"[STEERING: updated instructions from operator]\n%s",
			d.Instructions,
		)
	}
}
