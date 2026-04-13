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
	traceID string
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
		traceID: ve.traceID,
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
		TraceID:         sl.traceID,
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

// PersistSkill writes a synthesized SkillNode to the Memory Component as
// data_type "skill_cache". The domain tag is used by Factory.LoadSynthesizedSkills
// at startup to route each skill into its parent domain's command tree.
//
// PersistSkill is a no-op and returns nil when sl is nil.
func (sl *SessionLog) PersistSkill(domain string, node *types.SkillNode) error {
	if sl == nil {
		return nil
	}
	mw := types.MemoryWrite{
		AgentID:   sl.agentID,
		SessionID: sl.taskID,
		DataType:  "skill_cache",
		TTLHint:   0, // synthesized skills do not expire
		Payload:   node,
		Tags: map[string]string{
			"domain":     domain,
			"origin":     "synthesized",
			"skill_name": node.Name,
		},
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeStateWrite,
		SourceComponent: "agents",
		CorrelationID:   node.Name,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         mw,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("session log: persist skill: marshal: %w", err)
	}
	if _, err := sl.js.Publish(comms.SubjectStateWrite, data); err != nil {
		return fmt.Errorf("session log: persist skill: publish: %w", err)
	}
	sl.log.Info("session log: skill persisted",
		"domain", domain, "skill_name", node.Name)
	return nil
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
		TraceID:         traceID,
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

// PersistAgentMemory appends a distilled fact to domain-scoped agent memory
// (data_type "agent_memory"). The synthetic agent ID "domain:<domain>" is used
// so the Factory can retrieve all facts for a domain at spawn without knowing
// specific agent IDs.
//
// PersistAgentMemory is a no-op and returns nil when sl is nil.
func (sl *SessionLog) PersistAgentMemory(domain, fact string) error {
	if sl == nil {
		return nil
	}
	syntheticAgentID := "domain:" + domain
	mw := types.MemoryWrite{
		AgentID:   syntheticAgentID,
		SessionID: sl.taskID,
		DataType:  "agent_memory",
		TTLHint:   0,
		Payload:   fact,
		Tags: map[string]string{
			"domain":  domain,
			"context": "agent_memory",
		},
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeStateWrite,
		SourceComponent: "agents",
		CorrelationID:   sl.taskID,
		TraceID:         sl.traceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         mw,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("session log: persist agent memory: marshal: %w", err)
	}
	if _, err := sl.js.Publish(comms.SubjectStateWrite, data); err != nil {
		return fmt.Errorf("session log: persist agent memory: publish: %w", err)
	}
	sl.log.Info("session log: agent memory updated", "domain", domain)
	return nil
}

// PersistUserProfile appends a distilled user preference observation to the
// user profile (data_type "user_profile"). The synthetic agent ID
// "user:<userContextID>" is used for stable keying across agents and sessions.
//
// PersistUserProfile is a no-op and returns nil when sl is nil.
func (sl *SessionLog) PersistUserProfile(userContextID, observation string) error {
	if sl == nil {
		return nil
	}
	if userContextID == "" {
		return nil // no user context to key against; silently skip
	}
	syntheticAgentID := "user:" + userContextID
	mw := types.MemoryWrite{
		AgentID:   syntheticAgentID,
		SessionID: sl.taskID,
		DataType:  "user_profile",
		TTLHint:   0,
		Payload:   observation,
		Tags: map[string]string{
			"user_context_id": userContextID,
			"context":         "user_profile",
		},
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeStateWrite,
		SourceComponent: "agents",
		CorrelationID:   sl.taskID,
		TraceID:         sl.traceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         mw,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("session log: persist user profile: marshal: %w", err)
	}
	if _, err := sl.js.Publish(comms.SubjectStateWrite, data); err != nil {
		return fmt.Errorf("session log: persist user profile: publish: %w", err)
	}
	sl.log.Info("session log: user profile updated", "user_context_id", userContextID)
	return nil
}

// SearchSessions issues a full-text state.read.request and returns up to
// maxResults past session excerpts formatted as plain text. When NATS is
// unavailable or the request times out an informative string is returned so
// the LLM can continue without a hard error.
//
// SearchSessions is safe to call when sl is nil; it returns a notice string.
func (sl *SessionLog) SearchSessions(query string, maxResults int) string {
	if sl == nil {
		return "(memory search unavailable: NATS not connected)"
	}

	sub, err := sl.js.SubscribeSync(
		comms.SubjectStateReadResponse,
		nats.DeliverNew(),
		nats.AckNone(),
	)
	if err != nil {
		sl.log.Warn("memory search: subscribe failed", "error", err)
		return "(memory search unavailable: subscribe failed)"
	}
	defer func() { _ = sub.Unsubscribe() }()

	req := types.MemoryReadRequest{
		AgentID:     sl.agentID,
		DataType:    "episode",
		ContextTag:  "session",
		SearchQuery: query,
		MaxResults:  maxResults,
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeStateReadRequest,
		SourceComponent: "agents",
		CorrelationID:   sl.taskID,
		TraceID:         sl.traceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         req,
	}
	data, err := json.Marshal(env)
	if err != nil {
		sl.log.Warn("memory search: marshal failed", "error", err)
		return "(memory search unavailable: marshal failed)"
	}
	if _, err := sl.js.Publish(comms.SubjectStateReadRequest, data); err != nil {
		sl.log.Warn("memory search: publish failed", "error", err)
		return "(memory search unavailable: publish failed)"
	}

	deadline := time.Now().Add(sessionReadTimeout)
	for time.Now().Before(deadline) {
		msg, err := sub.NextMsg(time.Until(deadline))
		if err != nil {
			break
		}
		_ = msg.Ack()

		var envelope struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(msg.Data, &envelope); err != nil {
			continue
		}
		var resp types.MemoryResponse
		if err := json.Unmarshal(envelope.Payload, &resp); err != nil {
			continue
		}
		if resp.AgentID != sl.agentID {
			continue
		}
		entries := extractSessionEntries(resp.Records, sl.log)
		if len(entries) == 0 {
			return "(no results found)"
		}
		return formatSearchResults(entries)
	}
	return "(memory search timed out)"
}

// formatSearchResults formats a slice of SessionEntry values as plain text
// excerpts suitable for injection into LLM context.
func formatSearchResults(entries []types.SessionEntry) string {
	var b []byte
	for i, e := range entries {
		if i > 0 {
			b = append(b, '\n')
		}
		b = append(b, fmt.Sprintf("[%d] (%s) %s", i+1, e.TurnType, e.Content)...)
	}
	return string(b)
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
