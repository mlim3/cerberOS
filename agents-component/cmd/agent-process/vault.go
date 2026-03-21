// Package main — vault.go implements the async Vault execute request/result flow
// from EDD ADR-004 and §13.1 Phase 2.
//
// Flow:
//  1. Record the request_id in the session log via state.write BEFORE the
//     goroutine yields — ensures crash recovery can identify in-flight operations.
//  2. Register a result channel keyed by request_id BEFORE publishing the request
//     to avoid a race where the result arrives before we start waiting.
//  3. Publish VaultOperationRequest to aegis.orchestrator.vault.execute.request
//     (JetStream at-least-once). CorrelationID = request_id (required by Comms).
//  4. Block on the result channel with local deadline = timeout_seconds + 5s buffer.
//  5. On deadline: return TOOL_TIMEOUT content to the LLM and publish a cancellation
//     to aegis.orchestrator.vault.execute.cancel so the Vault can abort.
//
// VaultExecutor is initialised once per agent-process and shared across all tool
// invocations. It subscribes to aegis.agents.vault.execute.result with a durable
// JetStream consumer scoped to this agent_id, so delayed results that arrive after
// a crash are still delivered to the recovered process.
package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	nats "github.com/nats-io/nats.go"
)

// VaultExecutor manages the async vault execute request/result flow (ADR-004).
// One instance is created per agent-process; nil means vault execution is
// unavailable (NATS env vars absent) — non-credentialed tools still function.
type VaultExecutor struct {
	nc              *nats.Conn
	js              nats.JetStreamContext
	agentID         string
	taskID          string
	permissionToken string
	log             *slog.Logger

	mu                sync.Mutex
	pending           map[string]chan types.VaultOperationResult    // requestID → result channel
	progressCallbacks map[string]func(types.VaultOperationProgress) // requestID → onUpdate; at-most-once
}

// agentEnvelope is the outbound wire format required by the Orchestrator (mirrors
// comms.outboundEnvelope without importing the private struct).
type agentEnvelope struct {
	MessageID       string      `json:"message_id"`
	MessageType     string      `json:"message_type"`
	SourceComponent string      `json:"source_component"`
	CorrelationID   string      `json:"correlation_id,omitempty"`
	Timestamp       string      `json:"timestamp"`
	SchemaVersion   string      `json:"schema_version"`
	Payload         interface{} `json:"payload"`
}

// NewVaultExecutor connects to NATS, subscribes to vault.execute.result, and
// returns a VaultExecutor ready to dispatch credentialed operations.
//
// Required environment:
//
//	AEGIS_NATS_URL  — NATS server address (injected by Lifecycle Manager)
//	AEGIS_AGENT_ID  — this agent's identity (injected by Lifecycle Manager)
//
// Returns nil (non-fatal) if either env var is absent or NATS is unreachable.
func NewVaultExecutor(log *slog.Logger, taskID, permissionToken string) *VaultExecutor {
	natsURL := os.Getenv("AEGIS_NATS_URL")
	agentID := os.Getenv("AEGIS_AGENT_ID")
	if natsURL == "" || agentID == "" {
		log.Warn("vault executor disabled: AEGIS_NATS_URL or AEGIS_AGENT_ID not set")
		return nil
	}

	nc, err := nats.Connect(natsURL,
		nats.Name("aegis-vault-"+agentID),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		log.Warn("vault executor: NATS connect failed — vault disabled", "error", err)
		return nil
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		log.Warn("vault executor: JetStream init failed — vault disabled", "error", err)
		return nil
	}

	ve := &VaultExecutor{
		nc:                nc,
		js:                js,
		agentID:           agentID,
		taskID:            taskID,
		permissionToken:   permissionToken,
		log:               log,
		pending:           make(map[string]chan types.VaultOperationResult),
		progressCallbacks: make(map[string]func(types.VaultOperationProgress)),
	}

	// Durable consumer name is stable per agent_id: survives crash/respawn so
	// delayed results (arrived after crash) are received on recovery.
	durable := "agent-vault-result-" + agentID
	if err := ve.subscribeResults(durable); err != nil {
		nc.Close()
		log.Warn("vault executor: subscribe result failed — vault disabled", "error", err)
		return nil
	}

	// Progress events are at-most-once (core NATS) — subscribe on the plain
	// connection, not JetStream. Losing a progress event is acceptable.
	if err := ve.subscribeProgress(); err != nil {
		nc.Close()
		log.Warn("vault executor: subscribe progress failed — vault disabled", "error", err)
		return nil
	}

	log.Info("vault executor ready", "agent_id", agentID, "durable", durable)
	return ve
}

// subscribeResults registers a durable JetStream push consumer on
// aegis.agents.vault.execute.result. All results are routed by request_id to
// the waiting goroutine via the pending map.
func (ve *VaultExecutor) subscribeResults(durable string) error {
	_, err := ve.js.Subscribe(
		comms.SubjectVaultExecuteResult,
		func(msg *nats.Msg) {
			_ = msg.Ack() // ack immediately; vault idempotency is request_id-scoped
			ve.routeResult(msg.Data)
		},
		nats.Durable(durable),
		nats.AckExplicit(),
		nats.DeliverNew(),
	)
	return err
}

// subscribeProgress registers a core NATS (at-most-once) subscription on
// aegis.agents.vault.execute.progress. Progress events are forwarded to the
// registered onUpdate callback for the matching request_id; they never enter
// LLM context. Losing a progress event is acceptable and does not affect
// correctness.
func (ve *VaultExecutor) subscribeProgress() error {
	_, err := ve.nc.Subscribe(comms.SubjectVaultExecuteProgress, func(msg *nats.Msg) {
		ve.routeProgress(msg.Data)
	})
	return err
}

// routeProgress unwraps a progress envelope and invokes the onUpdate callback
// registered for the matching request_id. Events for other agents or requests
// with no registered callback are silently dropped (at-most-once semantics).
func (ve *VaultExecutor) routeProgress(data []byte) {
	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		ve.log.Warn("vault progress: unmarshal envelope failed", "error", err)
		return
	}

	var p types.VaultOperationProgress
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		ve.log.Warn("vault progress: unmarshal payload failed", "error", err)
		return
	}

	// Multiple agent-processes share the same NATS subject — filter our own.
	if p.AgentID != ve.agentID {
		return
	}

	ve.mu.Lock()
	cb, ok := ve.progressCallbacks[p.RequestID]
	ve.mu.Unlock()

	if !ok {
		return // no waiter — acceptable at-most-once drop
	}

	cb(p) // invoked outside lock to prevent deadlock
}

// routeResult unwraps an inbound envelope and routes the result to the goroutine
// waiting on the matching request_id. Results for other agents are silently ignored.
func (ve *VaultExecutor) routeResult(data []byte) {
	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		ve.log.Warn("vault result: unmarshal envelope failed", "error", err)
		return
	}

	var result types.VaultOperationResult
	if err := json.Unmarshal(env.Payload, &result); err != nil {
		ve.log.Warn("vault result: unmarshal payload failed", "error", err)
		return
	}

	// Multiple agent-processes share the same JetStream stream — filter our own.
	if result.AgentID != ve.agentID {
		return
	}

	ve.mu.Lock()
	ch, ok := ve.pending[result.RequestID]
	if ok {
		delete(ve.pending, result.RequestID)
		delete(ve.progressCallbacks, result.RequestID)
	}
	ve.mu.Unlock()

	if !ok {
		ve.log.Warn("vault result: no waiter for request_id (late delivery or duplicate)",
			"request_id", result.RequestID)
		return
	}

	select {
	case ch <- result:
	default:
		ve.log.Warn("vault result: channel full, dropping result", "request_id", result.RequestID)
	}
}

// Execute submits a vault operation and blocks until the result arrives or the
// local deadline fires (EDD §13.1 Phase 2).
//
// onUpdate is called for each aegis.agents.vault.execute.progress event that
// arrives for this request while it is in-flight. It is invoked on the NATS
// subscription goroutine — implementations must be non-blocking. Pass nil to
// ignore progress events. Progress events must not enter LLM context; onUpdate
// is for monitoring output only. At-most-once delivery: losing an event is
// acceptable and does not affect correctness.
//
// Sequence:
//  1. Record request_id in session log (state.write) BEFORE yielding — supports
//     crash recovery resubmission (EDD §6.3).
//  2. Register pending channel and onUpdate callback BEFORE publishing to avoid
//     result-before-wait race and progress-before-register races.
//  3. Publish VaultOperationRequest (JetStream at-least-once).
//  4. Wait: local deadline = timeout_seconds + 5s buffer.
//  5. On deadline or context cancel: return TOOL_TIMEOUT, publish cancellation.
func (ve *VaultExecutor) Execute(operationType, credentialType string, operationParams json.RawMessage, timeoutSeconds int, onUpdate func(types.VaultOperationProgress)) ToolResult {
	req := types.VaultOperationRequest{
		RequestID:       newUUID(),
		AgentID:         ve.agentID,
		TaskID:          ve.taskID,
		PermissionToken: ve.permissionToken,
		OperationType:   operationType,
		OperationParams: operationParams,
		TimeoutSeconds:  timeoutSeconds,
		CredentialType:  credentialType,
	}

	// Step 1: Record request_id in session log BEFORE the goroutine yields.
	// This is the critical invariant for crash recovery (EDD §6.3, §13.1).
	ve.recordSessionEntry(req.RequestID, req.OperationType)

	// Step 2: Register pending channel and onUpdate callback BEFORE publishing.
	// Both are registered under the same lock to ensure no progress event or
	// result can arrive between channel registration and publishing.
	resultCh := make(chan types.VaultOperationResult, 1)
	ve.mu.Lock()
	ve.pending[req.RequestID] = resultCh
	if onUpdate != nil {
		ve.progressCallbacks[req.RequestID] = onUpdate
	}
	ve.mu.Unlock()

	// Step 3: Publish VaultOperationRequest (JetStream). CorrelationID = request_id
	// as required by the Comms envelope contract.
	if err := ve.publishRequest(req); err != nil {
		ve.mu.Lock()
		delete(ve.pending, req.RequestID)
		delete(ve.progressCallbacks, req.RequestID)
		ve.mu.Unlock()
		ve.log.Error("vault execute: publish request failed",
			"request_id", req.RequestID, "error", err)
		return ToolResult{
			Content: fmt.Sprintf("vault execute: could not dispatch request for %q: %v", operationType, err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error(), "request_id": req.RequestID},
		}
	}

	ve.log.Info("vault execute: request dispatched",
		"request_id", req.RequestID,
		"operation_type", req.OperationType,
		"timeout_seconds", req.TimeoutSeconds,
	)

	// Step 4: Block with local deadline = timeout_seconds + 5s buffer (§13.1 Phase 2).
	localDeadline := time.Duration(req.TimeoutSeconds+5) * time.Second
	timer := time.NewTimer(localDeadline)
	defer timer.Stop()

	select {
	case result := <-resultCh:
		ve.log.Info("vault execute: result received",
			"request_id", req.RequestID,
			"status", result.Status,
			"elapsed_ms", result.ElapsedMS,
		)
		return vaultResultToToolResult(result)

	case <-timer.C:
		// Step 5: Local deadline exceeded.
		ve.mu.Lock()
		delete(ve.pending, req.RequestID)
		delete(ve.progressCallbacks, req.RequestID)
		ve.mu.Unlock()

		ve.publishCancellation(req.RequestID, req.OperationType, "local_timeout")

		ve.log.Warn("vault execute: TOOL_TIMEOUT — local deadline exceeded",
			"request_id", req.RequestID,
			"deadline_seconds", req.TimeoutSeconds+5,
		)
		return ToolResult{
			Content: fmt.Sprintf(
				"TOOL_TIMEOUT: vault operation %q did not complete within %ds (timeout=%ds + 5s buffer)",
				req.OperationType, req.TimeoutSeconds+5, req.TimeoutSeconds,
			),
			IsError: true,
			Details: map[string]interface{}{
				"request_id":       req.RequestID,
				"operation_type":   req.OperationType,
				"deadline_seconds": req.TimeoutSeconds + 5,
			},
		}
	}
}

// publishRequest wraps and publishes a VaultOperationRequest to the Orchestrator.
func (ve *VaultExecutor) publishRequest(req types.VaultOperationRequest) error {
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeVaultExecuteRequest,
		SourceComponent: "agents",
		CorrelationID:   req.RequestID, // required: correlation_id MUST be request_id
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         req,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal vault request envelope: %w", err)
	}
	if _, err := ve.js.Publish(comms.SubjectVaultExecuteRequest, data); err != nil {
		return fmt.Errorf("jetstream publish vault request: %w", err)
	}
	return nil
}

// publishCancellation notifies the Orchestrator that the local deadline fired so
// the Vault can abort the operation and free resources.
func (ve *VaultExecutor) publishCancellation(requestID, operationType, reason string) {
	cancel := types.VaultCancelRequest{
		RequestID:     requestID,
		AgentID:       ve.agentID,
		TaskID:        ve.taskID,
		OperationType: operationType,
		Reason:        reason,
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeVaultExecuteCancel,
		SourceComponent: "agents",
		CorrelationID:   requestID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         cancel,
	}
	data, err := json.Marshal(env)
	if err != nil {
		ve.log.Warn("vault cancel: marshal failed", "error", err)
		return
	}
	if _, err := ve.js.Publish(comms.SubjectVaultExecuteCancel, data); err != nil {
		ve.log.Warn("vault cancel: publish failed", "error", err)
	}
}

// recordSessionEntry writes the request_id into the session log BEFORE the
// goroutine yields (EDD §6.3 crash recovery invariant). On recovery the factory
// inspects session log entries with VaultRequestID set and no matching result —
// those request_ids are resubmitted with the SAME request_id (idempotent).
func (ve *VaultExecutor) recordSessionEntry(requestID, operationType string) {
	entry := types.SessionEntry{
		EntryID:        newUUID(),
		TurnType:       "tool_call",
		Content:        fmt.Sprintf("vault.execute.request dispatched: operation=%s request_id=%s", operationType, requestID),
		Timestamp:      time.Now().UTC(),
		VaultRequestID: requestID,
	}
	mw := types.MemoryWrite{
		AgentID:   ve.agentID,
		SessionID: ve.taskID,
		DataType:  "episode",
		TTLHint:   86400,
		Payload:   entry,
		Tags: map[string]string{
			"turn_type":        "tool_call",
			"vault_request_id": requestID,
		},
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeStateWrite,
		SourceComponent: "agents",
		CorrelationID:   requestID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         mw,
	}
	data, err := json.Marshal(env)
	if err != nil {
		ve.log.Warn("session log: marshal state.write failed", "error", err)
		return
	}
	if _, err := ve.js.Publish(comms.SubjectStateWrite, data); err != nil {
		ve.log.Warn("session log: publish state.write failed — request_id NOT persisted; crash recovery may miss this operation",
			"request_id", requestID, "error", err)
	}
}

// Close drains the NATS connection used by the vault executor.
func (ve *VaultExecutor) Close() {
	if ve.nc != nil {
		ve.nc.Close()
	}
}

// vaultResultToToolResult converts a VaultOperationResult to the ToolResult that
// enters the LLM context. OperationResult content is truncated to 16KB (§13.2).
// Error messages must not expose vault internals or paths (NFR-08).
func vaultResultToToolResult(r types.VaultOperationResult) ToolResult {
	switch r.Status {
	case "success":
		content := string(r.OperationResult)
		if len(content) > maxContentBytes {
			content = content[:maxContentBytes] + "\n[CONTENT TRUNCATED — vault result exceeded 16KB limit]"
		}
		return ToolResult{
			Content: content,
			IsError: false,
			Details: map[string]interface{}{
				"request_id": r.RequestID,
				"status":     r.Status,
				"elapsed_ms": r.ElapsedMS,
			},
		}

	case "timed_out":
		return ToolResult{
			Content: "TOOL_TIMEOUT: the vault operation timed out on the server side",
			IsError: true,
			Details: map[string]interface{}{
				"request_id": r.RequestID,
				"status":     r.Status,
				"elapsed_ms": r.ElapsedMS,
			},
		}

	default: // "scope_violation" | "execution_error"
		// ErrorMessage from the Vault must not expose internals (NFR-08).
		// We surface it as-is, trusting the Vault contract that it is scrubbed.
		msg := r.ErrorMessage
		if msg == "" {
			msg = r.ErrorCode
		}
		return ToolResult{
			Content: fmt.Sprintf("vault execute error [%s]: %s", r.Status, msg),
			IsError: true,
			Details: map[string]interface{}{
				"request_id": r.RequestID,
				"status":     r.Status,
				"error_code": r.ErrorCode,
				"elapsed_ms": r.ElapsedMS,
			},
		}
	}
}

// newUUID returns a random UUID v4 string.
func newUUID() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
