// Package audit emits structured audit events to aegis.orchestrator.audit.event
// (EDD §8.8). Emission is off the critical path: every Emit call dispatches a
// background goroutine and logs the failure without propagating it. Audit
// failures must never block or fail the business operation that triggered them.
//
// Callers must never place raw credential values, permission tokens,
// operation_result payloads, or PII into AuditEvent.Details.
package audit

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
)

// Emitter publishes audit events via the Comms Interface. It is safe for
// concurrent use; each Emit spawns an independent goroutine.
type Emitter struct {
	c   comms.Client
	log *slog.Logger
}

// New returns an Emitter backed by the given Comms Client.
func New(c comms.Client, log *slog.Logger) *Emitter {
	return &Emitter{c: c, log: log}
}

// Emit publishes event to aegis.orchestrator.audit.event in a background
// goroutine. EventID and Timestamp are populated if not already set.
// A failed publish is logged at Error level and never returned to the caller.
func (e *Emitter) Emit(event types.AuditEvent) {
	if event.EventID == "" {
		event.EventID = newID()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	go func() {
		if err := e.c.Publish(
			comms.SubjectAuditEvent,
			comms.PublishOptions{
				MessageType:   comms.MsgTypeAuditEvent,
				CorrelationID: event.TraceID,
			},
			event,
		); err != nil {
			e.log.Error("audit.event publish failed",
				"event_type", event.EventType,
				"agent_id", event.AgentID,
				"task_id", event.TaskID,
				"trace_id", event.TraceID,
				"error", err,
			)
		}
	}()
}

func newID() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
