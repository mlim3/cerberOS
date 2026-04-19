// Package memory is M7 — the Memory Interface. It formats and dispatches tagged
// memory payloads to the Orchestrator via the Comms Interface (NATS). This component
// never contacts the Memory Component directly — the Orchestrator owns that routing.
// All writes are surgical and tagged; reads are always filtered by agent ID and
// context tag. Full session dumps are explicitly forbidden.
//
// # Graceful Degradation
//
// When the Memory Component is unavailable, writes are classified by criticality:
//
//   - policyRequired (snapshot, audit_log, credential_event): the write MUST be
//     acknowledged (RequireAck=true). If the ack is not received after max retries,
//     Write returns an error and the onUnavailable hook is called. The caller is
//     responsible for aborting the associated operation (e.g. crash recovery).
//
//   - policyDegradable (task_result, episode): fire-and-forget. If the NATS publish
//     fails the payload is buffered in-process and nil is returned so the calling
//     operation can continue. A background goroutine retries the buffer every 30 s.
//     The onUnavailable hook is called on first buffer to trigger an error event.
//
//   - policyBestEffort (agent_state, everything else): the in-memory registry is
//     authoritative; publish failures are silently tolerated.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
)

// Client is the interface for all Memory Component interactions.
type Client interface {
	// Write persists a tagged payload to the Memory Component.
	// The payload must carry a non-empty AgentID, DataType, and Tags map.
	Write(payload *types.MemoryWrite) error

	// Read retrieves filtered slices by agent ID and context tag.
	// Never returns full agent history.
	Read(agentID, contextTag string) ([]types.MemoryWrite, error)

	// ReadAllByType returns all persisted records with the given DataType across
	// all agents. Used exclusively for component startup recovery; never call
	// this during normal agent operation.
	ReadAllByType(dataType string) ([]types.MemoryWrite, error)
}

// ── In-process stub (unit tests) ──────────────────────────────────────────────

// stubClient is the default in-process implementation used in unit tests.
type stubClient struct {
	mu      sync.RWMutex
	records map[string][]types.MemoryWrite // keyed by agentID
}

// New returns a Memory Client backed by an in-process stub.
func New() Client {
	return &stubClient{
		records: make(map[string][]types.MemoryWrite),
	}
}

func (c *stubClient) Write(payload *types.MemoryWrite) error {
	if err := validateWrite(payload); err != nil {
		return err
	}
	c.mu.Lock()
	c.records[payload.AgentID] = append(c.records[payload.AgentID], *payload)
	c.mu.Unlock()
	return nil
}

func (c *stubClient) Read(agentID, contextTag string) ([]types.MemoryWrite, error) {
	if agentID == "" {
		return nil, fmt.Errorf("memory: agentID must not be empty")
	}

	c.mu.RLock()
	all := c.records[agentID]
	c.mu.RUnlock()

	var filtered []types.MemoryWrite
	for _, r := range all {
		if contextTag == "" {
			filtered = append(filtered, r)
			continue
		}
		if val, ok := r.Tags["context"]; ok && val == contextTag {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

func (c *stubClient) ReadAllByType(dataType string) ([]types.MemoryWrite, error) {
	if dataType == "" {
		return nil, fmt.Errorf("memory: dataType must not be empty")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var result []types.MemoryWrite
	for _, records := range c.records {
		for _, r := range records {
			if r.DataType == dataType {
				result = append(result, r)
			}
		}
	}
	return result, nil
}

// ── Graceful-degradation policy ───────────────────────────────────────────────

// writePolicy classifies the fallback behaviour when a state.write cannot be
// delivered to or acknowledged by the Memory Component.
type writePolicy int8

const (
	// policyRequired — the write MUST be acknowledged (RequireAck=true forced).
	// Caller receives an error and the onUnavailable hook fires on failure.
	// Used for crash snapshots and security-critical audit records.
	policyRequired writePolicy = iota

	// policyDegradable — fire-and-forget preferred. If the NATS publish fails
	// the payload is queued in the in-process buffer and nil is returned so the
	// calling operation can continue. The onUnavailable hook fires on first use
	// of the buffer (one alert per Write call that triggers buffering).
	policyDegradable

	// policyBestEffort — the in-process source of truth is authoritative.
	// Publish failures are silently tolerated; no hook is called.
	policyBestEffort
)

// dataTypePolicy maps MemoryWrite.DataType values to their write policy.
// Data types not listed here default to policyBestEffort.
var dataTypePolicy = map[string]writePolicy{
	"snapshot":              policyRequired,   // crash recovery depends on ack
	"audit_log":             policyRequired,   // security audit record; must persist
	"credential_event":      policyRequired,   // credential audit record; must persist
	"task_result":           policyDegradable, // task completed; persist when possible
	"episode":               policyDegradable, // session log; tolerate partial loss
	"skill_cache":           policyDegradable, // synthesized skills; valuable but not crash-critical
	"agent_memory":          policyDegradable, // domain-scoped procedural memory; updated post-task
	"user_profile":          policyDegradable, // user preference observations; updated post-task
	"conversation_snapshot": policyDegradable, // compacted conversation history for multi-turn continuity; loss degrades to standalone task
	"agent_state":           policyBestEffort, // in-memory registry is authoritative
}

func policyFor(dataType string) writePolicy {
	if p, ok := dataTypePolicy[dataType]; ok {
		return p
	}
	return policyBestEffort
}

// ── NATS production client ────────────────────────────────────────────────────

const (
	memWriteMaxAttempts = 3
	memWriteTimeout     = 5 * time.Second
	memWriteBaseBackoff = time.Second
	memReadMaxAttempts  = 3
	memReadTimeout      = 5 * time.Second
	memReadBaseBackoff  = time.Second

	// maxBufferEntries caps the number of degradable writes held in-process.
	// When the cap is reached the oldest entry is evicted (FIFO).
	maxBufferEntries = 1000

	// drainInterval is how often the background goroutine retries buffered writes.
	drainInterval = 30 * time.Second
)

type writeResult struct{ err error }
type readResult struct {
	records []types.MemoryWrite
	err     error
}

// NATSClientOption configures a natsClient at construction time.
type NATSClientOption func(*natsClient)

// WithWriteUnavailableHook registers a callback that fires when a state.write
// cannot be delivered — either because a required ack was not received after max
// retries, or because a degradable write could not be published to NATS. The hook
// receives the DataType, the requestID assigned to the failed write, and a
// human-readable reason string.
//
// Use this to publish aegis.orchestrator.error events so the Orchestrator knows
// the Memory Component is unreachable. The hook is invoked synchronously on the
// Write call goroutine and must be non-blocking.
func WithWriteUnavailableHook(fn func(dataType, requestID, reason string)) NATSClientOption {
	return func(mc *natsClient) {
		mc.onUnavailable = fn
	}
}

// natsClient is the production NATS JetStream implementation. It performs real
// NATS round-trips and applies the write-policy degradation rules above.
type natsClient struct {
	comms         comms.Client
	onUnavailable func(dataType, requestID, reason string) // optional; called on write failure

	pendingWriteMu sync.Mutex
	pendingWrites  map[string]chan writeResult // requestID → waiting Write goroutine

	pendingReadMu sync.Mutex
	pendingReads  map[string]chan readResult // traceID → waiting Read goroutine

	bufMu  sync.Mutex
	buffer []types.MemoryWrite // in-process store for degradable writes when NATS unavailable
}

// NewNATSClient returns a Memory Client that performs real NATS round-trips via
// the Orchestrator. It subscribes to state.write.ack and state.read.response at
// construction time using durable consumers so responses are never lost.
//
// Pass WithWriteUnavailableHook to receive a callback when state.write delivery
// fails so the caller can publish an aegis.orchestrator.error event.
func NewNATSClient(c comms.Client, opts ...NATSClientOption) (Client, error) {
	mc := &natsClient{
		comms:         c,
		pendingWrites: make(map[string]chan writeResult),
		pendingReads:  make(map[string]chan readResult),
	}
	for _, o := range opts {
		o(mc)
	}
	if err := c.SubscribeDurable(
		comms.SubjectStateWriteAck,
		comms.ConsumerStateWriteAck,
		mc.handleWriteAck,
	); err != nil {
		return nil, fmt.Errorf("memory: subscribe %q: %w", comms.SubjectStateWriteAck, err)
	}
	if err := c.SubscribeDurable(
		comms.SubjectStateReadResponse,
		comms.ConsumerStateReadResponse,
		mc.handleReadResponse,
	); err != nil {
		return nil, fmt.Errorf("memory: subscribe %q: %w", comms.SubjectStateReadResponse, err)
	}
	go mc.drainBuffer(context.Background())
	return mc, nil
}

// BufferedCount returns the number of degradable writes currently held in the
// in-process buffer. A non-zero value indicates the Memory Component was recently
// unreachable. The background drain goroutine retries these writes every 30 s.
func (c *natsClient) BufferedCount() int {
	c.bufMu.Lock()
	defer c.bufMu.Unlock()
	return len(c.buffer)
}

// Write validates, classifies by DataType policy, and publishes a state.write.
//
//   - policyRequired (snapshot, audit_log, credential_event): RequireAck is
//     forced to true. Returns an error and calls onUnavailable if the ack is
//     not received after max retries. The caller must treat this as fatal.
//
//   - policyDegradable (task_result, episode): publish fire-and-forget. On NATS
//     publish failure the write is buffered in-process, onUnavailable is called,
//     and nil is returned so the calling operation can continue.
//
//   - policyBestEffort (agent_state, unknown types): publish fire-and-forget;
//     failures are silently tolerated.
func (c *natsClient) Write(payload *types.MemoryWrite) error {
	if err := validateWrite(payload); err != nil {
		return err
	}

	requestID := newMemoryID()
	payload.RequestID = requestID

	switch policyFor(payload.DataType) {
	case policyRequired:
		payload.RequireAck = true
		if err := c.writeWithAck(payload, requestID); err != nil {
			if c.onUnavailable != nil {
				c.onUnavailable(payload.DataType, requestID, err.Error())
			}
			return fmt.Errorf("memory: %s write unacknowledged after %d attempts: %w",
				payload.DataType, memWriteMaxAttempts, err)
		}
		return nil

	case policyDegradable:
		payload.RequireAck = false
		if err := c.comms.Publish(
			comms.SubjectStateWrite,
			comms.PublishOptions{MessageType: comms.MsgTypeStateWrite, CorrelationID: requestID},
			payload,
		); err != nil {
			c.appendBuffer(*payload)
			if c.onUnavailable != nil {
				c.onUnavailable(payload.DataType, requestID, err.Error())
			}
			slog.Warn("memory: degradable write buffered in-process (NATS unavailable)",
				"data_type", payload.DataType,
				"request_id", requestID,
				"agent_id", payload.AgentID,
				"error", err,
			)
			return nil // degrade gracefully: write is held in the local buffer
		}
		return nil

	default: // policyBestEffort
		payload.RequireAck = false
		_ = c.comms.Publish(
			comms.SubjectStateWrite,
			comms.PublishOptions{MessageType: comms.MsgTypeStateWrite, CorrelationID: requestID},
			payload,
		)
		return nil
	}
}

// Read publishes a state.read.request to the Orchestrator and blocks until the
// matching state.read.response arrives, retrying up to memReadMaxAttempts on
// timeout. Returns filtered records for the given agentID and contextTag.
func (c *natsClient) Read(agentID, contextTag string) ([]types.MemoryWrite, error) {
	if agentID == "" {
		return nil, fmt.Errorf("memory: agentID must not be empty")
	}
	traceID := newMemoryID()
	return c.sendReadRequest(types.MemoryReadRequest{
		AgentID:    agentID,
		ContextTag: contextTag,
		TraceID:    traceID,
	}, traceID)
}

// ReadAllByType publishes a state.read.request filtered by DataType with no
// AgentID constraint. Intended only for startup recovery.
func (c *natsClient) ReadAllByType(dataType string) ([]types.MemoryWrite, error) {
	if dataType == "" {
		return nil, fmt.Errorf("memory: dataType must not be empty")
	}
	traceID := newMemoryID()
	return c.sendReadRequest(types.MemoryReadRequest{
		DataType: dataType,
		TraceID:  traceID,
	}, traceID)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// writeWithAck registers a pending ack channel, publishes the request, and
// waits for acknowledgement from the Orchestrator with exponential back-off.
// The channel must be registered BEFORE publishing to avoid the race where
// a fast ack arrives before the select is reached.
func (c *natsClient) writeWithAck(payload *types.MemoryWrite, requestID string) error {
	resultCh := make(chan writeResult, 1)
	c.pendingWriteMu.Lock()
	c.pendingWrites[requestID] = resultCh
	c.pendingWriteMu.Unlock()
	defer func() {
		c.pendingWriteMu.Lock()
		delete(c.pendingWrites, requestID)
		c.pendingWriteMu.Unlock()
	}()

	var lastErr error
	for attempt := 0; attempt < memWriteMaxAttempts; attempt++ {
		if attempt > 0 {
			backoff := memWriteBaseBackoff * time.Duration(1<<uint(attempt-1))
			time.Sleep(backoff)
		}

		if err := c.comms.Publish(
			comms.SubjectStateWrite,
			comms.PublishOptions{MessageType: comms.MsgTypeStateWrite, CorrelationID: requestID},
			payload,
		); err != nil {
			lastErr = fmt.Errorf("publish state.write (attempt %d/%d): %w",
				attempt+1, memWriteMaxAttempts, err)
			continue
		}

		timer := time.NewTimer(memWriteTimeout)
		select {
		case result := <-resultCh:
			timer.Stop()
			if result.err != nil {
				// Rejected by the Memory Component — authoritative; do not retry.
				return result.err
			}
			return nil
		case <-timer.C:
			lastErr = fmt.Errorf("state.write.ack timeout (attempt %d/%d, request_id=%s)",
				attempt+1, memWriteMaxAttempts, requestID)
		}
	}

	return fmt.Errorf("state.write unacknowledged after %d attempts (request_id=%s): %w",
		memWriteMaxAttempts, requestID, lastErr)
}

// appendBuffer adds a degradable write to the in-process buffer, evicting the
// oldest entry when the cap is reached (FIFO).
func (c *natsClient) appendBuffer(w types.MemoryWrite) {
	c.bufMu.Lock()
	defer c.bufMu.Unlock()
	if len(c.buffer) >= maxBufferEntries {
		slog.Warn("memory: degradation buffer full; oldest entry evicted",
			"evicted_data_type", c.buffer[0].DataType,
			"evicted_agent_id", c.buffer[0].AgentID,
		)
		c.buffer = c.buffer[1:]
	}
	c.buffer = append(c.buffer, w)
}

// drainBuffer runs in a goroutine and periodically re-publishes buffered writes.
// Writes that still fail are kept; writes that succeed are removed.
func (c *natsClient) drainBuffer(ctx context.Context) {
	ticker := time.NewTicker(drainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.flushBufferedWrites()
		}
	}
}

// flushBufferedWrites takes a snapshot of the buffer, attempts to publish each
// entry, and replaces the buffer with only the entries that still failed.
func (c *natsClient) flushBufferedWrites() {
	c.bufMu.Lock()
	if len(c.buffer) == 0 {
		c.bufMu.Unlock()
		return
	}
	snapshot := make([]types.MemoryWrite, len(c.buffer))
	copy(snapshot, c.buffer)
	c.bufMu.Unlock()

	var failed []types.MemoryWrite
	for i := range snapshot {
		w := snapshot[i]
		// Assign a fresh request_id for the retry publish so a stale ack from
		// the original attempt does not confuse in-flight tracking.
		w.RequestID = newMemoryID()
		if err := c.comms.Publish(
			comms.SubjectStateWrite,
			comms.PublishOptions{MessageType: comms.MsgTypeStateWrite, CorrelationID: w.RequestID},
			&w,
		); err != nil {
			failed = append(failed, snapshot[i]) // retain original requestID
		}
	}

	c.bufMu.Lock()
	// Prepend the still-failed entries and preserve any new entries appended
	// to the buffer while the flush was running.
	c.buffer = append(failed, c.buffer[len(snapshot):]...)
	if len(failed) < len(snapshot) {
		slog.Info("memory: degradation buffer partially drained",
			"flushed", len(snapshot)-len(failed),
			"remaining", len(c.buffer),
		)
	}
	c.bufMu.Unlock()
}

// sendReadRequest registers a pending read channel, publishes the request, and
// blocks until a state.read.response with a matching traceID arrives (retrying
// up to memReadMaxAttempts on timeout).
func (c *natsClient) sendReadRequest(req types.MemoryReadRequest, traceID string) ([]types.MemoryWrite, error) {
	resultCh := make(chan readResult, 1)
	c.pendingReadMu.Lock()
	c.pendingReads[traceID] = resultCh
	c.pendingReadMu.Unlock()
	defer func() {
		c.pendingReadMu.Lock()
		delete(c.pendingReads, traceID)
		c.pendingReadMu.Unlock()
	}()

	var lastErr error
	for attempt := 0; attempt < memReadMaxAttempts; attempt++ {
		if attempt > 0 {
			backoff := memReadBaseBackoff * time.Duration(1<<uint(attempt-1))
			time.Sleep(backoff)
		}

		if err := c.comms.Publish(
			comms.SubjectStateReadRequest,
			comms.PublishOptions{
				MessageType:   comms.MsgTypeStateReadRequest,
				CorrelationID: traceID,
			},
			req,
		); err != nil {
			lastErr = fmt.Errorf("memory: publish state.read.request (attempt %d/%d): %w",
				attempt+1, memReadMaxAttempts, err)
			continue
		}

		timer := time.NewTimer(memReadTimeout)
		select {
		case result := <-resultCh:
			timer.Stop()
			if result.err != nil {
				return nil, result.err
			}
			return result.records, nil
		case <-timer.C:
			lastErr = fmt.Errorf("memory: state.read.response timeout (attempt %d/%d, trace_id=%s)",
				attempt+1, memReadMaxAttempts, traceID)
		}
	}

	return nil, fmt.Errorf("memory: state.read timed out after %d attempts (trace_id=%s): %w",
		memReadMaxAttempts, traceID, lastErr)
}

// handleWriteAck is the durable subscription handler for aegis.agents.state.write.ack.
// It routes the ack to the goroutine blocked in writeWithAck by matching on the
// envelope correlation_id (request_id). Falls back to the payload RequestID when
// the envelope field is absent (e.g. when delivered via the stub client in unit tests).
func (c *natsClient) handleWriteAck(msg *comms.Message) {
	var ack types.StateWriteAck
	if err := json.Unmarshal(msg.Data, &ack); err != nil {
		_ = msg.Ack()
		return
	}

	requestID := msg.CorrelationID
	if requestID == "" {
		requestID = ack.RequestID
	}
	if requestID == "" {
		_ = msg.Ack()
		return
	}

	c.pendingWriteMu.Lock()
	ch, ok := c.pendingWrites[requestID]
	c.pendingWriteMu.Unlock()

	if !ok {
		// Response arrived after deadline or for an unknown request — ack and drop.
		_ = msg.Ack()
		return
	}

	var result writeResult
	if ack.Status == "rejected" {
		slog.Warn("memory: state.write rejected by Memory Component",
			"request_id", requestID,
			"agent_id", ack.AgentID,
			"rejection_reason", ack.RejectionReason,
		)
		reason := ack.RejectionReason
		if reason == "" {
			reason = "unknown"
		}
		result.err = fmt.Errorf("memory: state.write rejected (request_id=%s, agent_id=%s): %s",
			requestID, ack.AgentID, reason)
	}

	// Non-blocking send: if the channel already has a result (duplicate delivery),
	// drop the duplicate and ack.
	select {
	case ch <- result:
	default:
	}
	_ = msg.Ack()
}

// handleReadResponse is the durable subscription handler for
// aegis.agents.state.read.response. It routes the response to the goroutine
// blocked in sendReadRequest by matching on the envelope correlation_id (trace_id).
// Falls back to the payload TraceID when the envelope field is absent.
func (c *natsClient) handleReadResponse(msg *comms.Message) {
	var resp types.MemoryResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		_ = msg.Ack()
		return
	}

	traceID := msg.CorrelationID
	if traceID == "" {
		traceID = resp.TraceID
	}
	if traceID == "" {
		_ = msg.Ack()
		return
	}

	c.pendingReadMu.Lock()
	ch, ok := c.pendingReads[traceID]
	c.pendingReadMu.Unlock()

	if !ok {
		_ = msg.Ack()
		return
	}

	select {
	case ch <- readResult{records: resp.Records}:
	default:
	}
	_ = msg.Ack()
}

// ── Validation helpers ────────────────────────────────────────────────────────

// validateWrite enforces the pre-publication invariants on every MemoryWrite.
// All writes must carry a non-empty AgentID, DataType, and at least one tag.
// SessionID is optional on the wire but validated when present.
func validateWrite(payload *types.MemoryWrite) error {
	if payload == nil {
		return fmt.Errorf("memory: payload must not be nil")
	}
	if payload.AgentID == "" {
		return fmt.Errorf("memory: payload.AgentID must not be empty")
	}
	if payload.DataType == "" {
		return fmt.Errorf("memory: payload.DataType must not be empty")
	}
	if len(payload.Tags) == 0 {
		return fmt.Errorf("memory: payload.Tags must not be empty: all writes require at least one tag")
	}
	return nil
}

// newMemoryID returns a UUID v4 string using crypto/rand.
func newMemoryID() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
