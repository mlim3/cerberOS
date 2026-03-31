// Package main — session.go implements the append-only, tree-structured session
// log from EDD §13.4.
//
// Every ReAct loop turn produces one or more state.write events. Each event carries:
//   - entry_id (UUID)          — stable identity for this node in the session tree
//   - parent_entry_id (UUID)   — links this node to its parent; "" on the root entry
//   - turn_type                — one of the five turn type constants below
//   - content                  — human-readable or structured text for this turn
//   - timestamp                — UTC wall-clock time of the write
//
// The in-flight Vault request_id is set on tool_call entries whose execution
// dispatches a vault.execute.request. Recording it BEFORE the goroutine yields is
// the recovery resubmission anchor (EDD §6.3): on recovery the agent reads back
// the session branch and resubmits any tool_call entries whose request_id has no
// matching result.
//
// SessionLog is nil-safe: all methods are no-ops when the receiver is nil, which
// happens when NATS is unavailable. Callers never need to guard on nil.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	"log/slog"

	nats "github.com/nats-io/nats.go"
)

// Turn type constants for SessionEntry.TurnType (EDD §13.4).
const (
	turnTypeUserMessage       = "user_message"
	turnTypeAssistantResponse = "assistant_response"
	turnTypeToolCall          = "tool_call"
	turnTypeToolResult        = "tool_result"
	turnTypeCompaction        = "compaction"
	turnTypeSteeringDirective = "steering_directive" // OQ-08: mid-task steering
)

// sessionReadTimeout is the maximum time ReadSession waits for a
// state.read.response before giving up.
const sessionReadTimeout = 5 * time.Second

// ---- context key types ----
// These are unexported to prevent collisions with keys from other packages.

type ctxKeySessionLog struct{}
type ctxKeyParentEntryID struct{}

// WithSessionLog returns a child context carrying sl. Vault tool dispatch
// functions extract it to persist tool_call entries with a VaultRequestID.
func WithSessionLog(ctx context.Context, sl *SessionLog) context.Context {
	return context.WithValue(ctx, ctxKeySessionLog{}, sl)
}

// SessionLogFromCtx extracts the SessionLog stored by WithSessionLog.
// Returns nil (no-op SessionLog) when the key is absent.
func SessionLogFromCtx(ctx context.Context) *SessionLog {
	sl, _ := ctx.Value(ctxKeySessionLog{}).(*SessionLog)
	return sl
}

// WithParentEntryID returns a child context carrying the entry_id that the
// next session write should use as its parent_entry_id.
func WithParentEntryID(ctx context.Context, parentID string) context.Context {
	return context.WithValue(ctx, ctxKeyParentEntryID{}, parentID)
}

// ParentEntryIDFromCtx extracts the parent entry_id stored by WithParentEntryID.
// Returns "" when the key is absent.
func ParentEntryIDFromCtx(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyParentEntryID{}).(string)
	return id
}

// SessionLog writes an append-only, tree-structured session log to episodic
// memory via state.write (EDD §13.4). It wraps the VaultExecutor's
// JetStream connection so no additional NATS connection is needed.
//
// All public methods are nil-safe — when sl is nil the calls are no-ops.
type SessionLog struct {
	agentID string
	taskID  string
	log     *slog.Logger
	js      nats.JetStreamContext
	nc      *nats.Conn // needed for ephemeral subscribe on state.read.response
}

// NewSessionLog returns a SessionLog backed by ve's NATS/JetStream connection.
// Returns nil when ve is nil (NATS unavailable); all SessionLog methods tolerate nil receivers.
func NewSessionLog(ve *VaultExecutor, log *slog.Logger) *SessionLog {
	if ve == nil {
		return nil
	}
	return &SessionLog{
		agentID: ve.agentID,
		taskID:  ve.taskID,
		log:     log,
		js:      ve.js,
		nc:      ve.nc,
	}
}

// Write appends a session entry and returns the new entry_id.
//
// parentID is the entry_id of the preceding entry in this session branch.
// Pass "" for the root user_message (initial instructions).
//
// vaultRequestID must be set (non-empty) for tool_call entries that dispatch a
// vault.execute.request — this is the crash-recovery resubmission anchor
// (EDD §6.3, §13.4). Pass "" for every other turn type.
//
// Write is a no-op and returns "" when sl is nil.
func (sl *SessionLog) Write(turnType, content, parentID, vaultRequestID string) string {
	if sl == nil {
		return ""
	}

	entryID := newUUID()
	entry := types.SessionEntry{
		EntryID:        entryID,
		ParentEntryID:  parentID,
		TurnType:       turnType,
		Content:        content,
		Timestamp:      time.Now().UTC(),
		VaultRequestID: vaultRequestID,
	}
	tags := map[string]string{
		"turn_type": turnType,
		"context":   "session",
	}
	if vaultRequestID != "" {
		tags["vault_request_id"] = vaultRequestID
	}
	mw := types.MemoryWrite{
		AgentID:   sl.agentID,
		SessionID: sl.taskID,
		DataType:  "episode",
		TTLHint:   86400,
		Payload:   entry,
		Tags:      tags,
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeStateWrite,
		SourceComponent: "agents",
		CorrelationID:   entryID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         mw,
	}
	data, err := json.Marshal(env)
	if err != nil {
		sl.log.Warn("session log: marshal failed",
			"turn_type", turnType, "entry_id", entryID, "error", err)
		return entryID // return the ID even on publish failure for chaining
	}
	if _, err := sl.js.Publish(comms.SubjectStateWrite, data); err != nil {
		sl.log.Warn("session log: publish failed",
			"turn_type", turnType, "entry_id", entryID, "error", err)
		if turnType == turnTypeToolCall && vaultRequestID != "" {
			// Crash recovery depends on this entry being durable. Log prominently.
			sl.log.Error("session log: vault tool_call entry NOT persisted — crash recovery may miss this operation",
				"entry_id", entryID,
				"vault_request_id", vaultRequestID,
			)
		}
	}
	return entryID
}

// ReadSession issues a state.read.request for this agent's session entries and
// waits up to sessionReadTimeout for the response. Returns the recovered session
// entries (all DataType "episode" entries for this task) so the caller can
// identify in-flight vault operations and rebuild conversation context.
//
// ReadSession is a no-op and returns nil when sl is nil or NATS is unavailable.
func (sl *SessionLog) ReadSession(traceID string) []types.SessionEntry {
	if sl == nil {
		return nil
	}

	// Subscribe to the response subject BEFORE publishing the request to
	// avoid a race where the response arrives before we start listening.
	sub, err := sl.js.SubscribeSync(
		comms.SubjectStateReadResponse,
		nats.DeliverNew(),
		nats.AckNone(),
	)
	if err != nil {
		sl.log.Warn("session read: failed to subscribe to state.read.response", "error", err)
		return nil
	}
	defer func() { _ = sub.Unsubscribe() }()

	if err := sl.publishStateReadRequest(traceID); err != nil {
		sl.log.Warn("session read: failed to publish state.read.request", "error", err)
		return nil
	}

	deadline := time.Now().Add(sessionReadTimeout)
	for time.Now().Before(deadline) {
		msg, err := sub.NextMsg(time.Until(deadline))
		if err != nil {
			break // timeout or drain
		}
		_ = msg.Ack()

		var env struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			sl.log.Warn("session read: unmarshal envelope failed", "error", err)
			continue
		}
		var resp types.MemoryResponse
		if err := json.Unmarshal(env.Payload, &resp); err != nil {
			sl.log.Warn("session read: unmarshal payload failed", "error", err)
			continue
		}
		// Filter: only our own agent's response.
		if resp.AgentID != sl.agentID {
			continue
		}
		return extractSessionEntries(resp.Records, sl.log)
	}

	sl.log.Warn("session read: timed out waiting for state.read.response",
		"timeout", sessionReadTimeout)
	return nil
}

// publishStateReadRequest publishes a MemoryReadRequest to the Orchestrator.
func (sl *SessionLog) publishStateReadRequest(traceID string) error {
	req := types.MemoryReadRequest{
		AgentID:    sl.agentID,
		DataType:   "episode",
		ContextTag: "session",
		TraceID:    traceID,
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeStateReadRequest,
		SourceComponent: "agents",
		CorrelationID:   sl.taskID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         req,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal state.read.request: %w", err)
	}
	if _, err := sl.js.Publish(comms.SubjectStateReadRequest, data); err != nil {
		return fmt.Errorf("publish state.read.request: %w", err)
	}
	return nil
}

// extractSessionEntries unpacks MemoryWrite.Payload into SessionEntry values.
// Records whose payload cannot be decoded are logged and skipped.
func extractSessionEntries(records []types.MemoryWrite, log *slog.Logger) []types.SessionEntry {
	entries := make([]types.SessionEntry, 0, len(records))
	for _, r := range records {
		raw, err := json.Marshal(r.Payload)
		if err != nil {
			log.Warn("session read: marshal payload for decode failed", "error", err)
			continue
		}
		var entry types.SessionEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			log.Warn("session read: decode SessionEntry failed", "error", err)
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}
