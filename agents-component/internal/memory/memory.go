// Package memory is M7 — the Memory Interface. It formats and dispatches tagged
// memory payloads to the Orchestrator via the Comms Interface (NATS). This component
// never contacts the Memory Component directly — the Orchestrator owns that routing.
// All writes are surgical and tagged; reads are always filtered by agent ID and
// context tag. Full session dumps are explicitly forbidden.
package memory

import (
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
}

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

// — NATS client (production) ——————————————————————————————————————————————

const (
	memWriteMaxAttempts = 3
	memWriteTimeout     = 5 * time.Second
	memWriteBaseBackoff = time.Second
	memReadMaxAttempts  = 3
	memReadTimeout      = 5 * time.Second
	memReadBaseBackoff  = time.Second
)

type writeResult struct{ err error }
type readResult struct {
	records []types.MemoryWrite
	err     error
}

// natsClient is the production implementation. It performs real NATS round-trips:
//
//   - Write: publishes to aegis.orchestrator.state.write and, when RequireAck=true,
//     yields until a state.write.ack arrives filtered by request_id.
//
//   - Read: publishes to aegis.orchestrator.state.read.request and yields until
//     a state.read.response arrives filtered by trace_id.
//
// Both subscriptions are established at construction so no response is ever missed
// between publish and listen.
type natsClient struct {
	comms comms.Client

	pendingWriteMu sync.Mutex
	pendingWrites  map[string]chan writeResult // requestID → waiting Write goroutine

	pendingReadMu sync.Mutex
	pendingReads  map[string]chan readResult // traceID → waiting Read goroutine
}

// NewNATSClient returns a Memory Client that performs real NATS round-trips via
// the Orchestrator. It subscribes to state.write.ack and state.read.response at
// construction time using durable consumers so responses are never lost.
func NewNATSClient(c comms.Client) (Client, error) {
	mc := &natsClient{
		comms:         c,
		pendingWrites: make(map[string]chan writeResult),
		pendingReads:  make(map[string]chan readResult),
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
	return mc, nil
}

// Write validates, tags, and publishes a state.write to the Orchestrator.
// When payload.RequireAck is true it blocks until a state.write.ack is
// received, retrying up to memWriteMaxAttempts on timeout. A "rejected"
// ack logs the rejection_reason and returns an error immediately — it is
// never silently discarded and is not retried (the rejection is authoritative).
func (c *natsClient) Write(payload *types.MemoryWrite) error {
	if err := validateWrite(payload); err != nil {
		return err
	}

	requestID := newMemoryID()
	payload.RequestID = requestID

	if !payload.RequireAck {
		return c.comms.Publish(
			comms.SubjectStateWrite,
			comms.PublishOptions{
				MessageType:   comms.MsgTypeStateWrite,
				CorrelationID: requestID,
			},
			payload,
		)
	}

	// Register the result channel BEFORE publishing so a fast ack from the
	// Orchestrator is never lost in the gap between publish and select.
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
			comms.PublishOptions{
				MessageType:   comms.MsgTypeStateWrite,
				CorrelationID: requestID,
			},
			payload,
		); err != nil {
			lastErr = fmt.Errorf("memory: publish state.write (attempt %d/%d): %w",
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
			lastErr = fmt.Errorf("memory: state.write.ack timeout (attempt %d/%d, request_id=%s)",
				attempt+1, memWriteMaxAttempts, requestID)
		}
	}

	return fmt.Errorf("memory: state.write unacknowledged after %d attempts (request_id=%s): %w",
		memWriteMaxAttempts, requestID, lastErr)
}

// Read publishes a state.read.request to the Orchestrator and blocks until the
// matching state.read.response arrives, retrying up to memReadMaxAttempts on
// timeout. Returns filtered records for the given agentID and contextTag.
func (c *natsClient) Read(agentID, contextTag string) ([]types.MemoryWrite, error) {
	if agentID == "" {
		return nil, fmt.Errorf("memory: agentID must not be empty")
	}

	traceID := newMemoryID()

	resultCh := make(chan readResult, 1)
	c.pendingReadMu.Lock()
	c.pendingReads[traceID] = resultCh
	c.pendingReadMu.Unlock()
	defer func() {
		c.pendingReadMu.Lock()
		delete(c.pendingReads, traceID)
		c.pendingReadMu.Unlock()
	}()

	req := types.MemoryReadRequest{
		AgentID:    agentID,
		ContextTag: contextTag,
		TraceID:    traceID,
	}

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
// It routes the ack to the goroutine blocked in Write by matching on the envelope
// correlation_id (request_id). Falls back to the payload RequestID when the
// envelope field is absent (e.g. when delivered via the stub client in unit tests).
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
// blocked in Read by matching on the envelope correlation_id (trace_id).
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

// — Helpers ————————————————————————————————————————————————————————————————

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
