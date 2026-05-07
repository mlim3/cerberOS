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
	"context"
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

const (
	// Default vault execute publish retry parameters.
	// Override via AEGIS_VAULT_PUBLISH_MAX_ATTEMPTS and AEGIS_VAULT_PUBLISH_BASE_BACKOFF.
	vaultPublishMaxAttempts = 3
	vaultPublishBaseBackoff = time.Second
)

// VaultExecConfig holds the retry parameters for publishing vault execute requests.
// When NATS is temporarily unavailable, Execute retries with exponential backoff
// before returning TOOL_TIMEOUT to the LLM so it can decide whether to retry.
type VaultExecConfig struct {
	// PublishMaxAttempts is the number of JetStream publish attempts before
	// TOOL_TIMEOUT is returned to the LLM. Zero falls back to 3.
	PublishMaxAttempts int

	// PublishBaseBackoff is the initial sleep between publish retries (doubles
	// on each attempt: 1s → 2s → 4s, …). Zero falls back to 1s.
	PublishBaseBackoff time.Duration
}

func (c VaultExecConfig) withDefaults() VaultExecConfig {
	if c.PublishMaxAttempts <= 0 {
		c.PublishMaxAttempts = vaultPublishMaxAttempts
	}
	if c.PublishBaseBackoff <= 0 {
		c.PublishBaseBackoff = vaultPublishBaseBackoff
	}
	return c
}

// VaultExecutor manages the async vault execute request/result flow (ADR-004).
// One instance is created per agent-process; nil means vault execution is
// unavailable (NATS env vars absent) — non-credentialed tools still function.
type VaultExecutor struct {
	nc              *nats.Conn
	js              nats.JetStreamContext
	agentID         string
	taskID          string
	traceID         string
	permissionToken string
	cfg             VaultExecConfig
	log             *slog.Logger

	mu                sync.Mutex
	pending           map[string]chan types.VaultOperationResult    // requestID → result channel
	progressCallbacks map[string]func(types.VaultOperationProgress) // requestID → onUpdate; at-most-once

	// credMu guards credPending. When N parallel tool calls all hit
	// MISSING_CREDENTIAL for the same credentialType, only the first goroutine
	// publishes a credential.request and shows the IO modal. The rest wait on
	// the shared done channel. When the credential is stored the done channel is
	// closed, broadcasting to all waiters simultaneously.
	credMu      sync.Mutex
	credPending map[string]chan struct{} // credentialType → done channel
}

// agentEnvelope is the outbound wire format required by the Orchestrator (mirrors
// comms.outboundEnvelope without importing the private struct).
type agentEnvelope struct {
	MessageID       string      `json:"message_id"`
	MessageType     string      `json:"message_type"`
	SourceComponent string      `json:"source_component"`
	CorrelationID   string      `json:"correlation_id,omitempty"`
	TraceID         string      `json:"trace_id,omitempty"`
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
func NewVaultExecutor(log *slog.Logger, taskID, permissionToken, traceID string) *VaultExecutor {
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

	cfg := VaultExecConfig{
		PublishMaxAttempts: parseEnvInt("AEGIS_VAULT_PUBLISH_MAX_ATTEMPTS", 0),
		PublishBaseBackoff: parseEnvDuration("AEGIS_VAULT_PUBLISH_BASE_BACKOFF", 0),
	}.withDefaults()

	ve := &VaultExecutor{
		nc:                nc,
		js:                js,
		agentID:           agentID,
		taskID:            taskID,
		traceID:           traceID,
		permissionToken:   permissionToken,
		cfg:               cfg,
		log:               log,
		pending:           make(map[string]chan types.VaultOperationResult),
		progressCallbacks: make(map[string]func(types.VaultOperationProgress)),
		credPending:       make(map[string]chan struct{}),
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

// EmitSkillInvocation publishes a skill_invocation audit event for the given
// tool call outcome. It is nil-safe — when ve == nil (NATS absent) it is a
// no-op so telemetry never affects the ReAct loop.
func (ve *VaultExecutor) EmitSkillInvocation(domain, command, depth string, elapsedMS int64, outcome string) {
	if ve == nil {
		return
	}
	ve.emitAudit(types.AuditEventSkillInvocation, map[string]string{
		"domain":           domain,
		"command":          command,
		"drill_down_depth": depth,
		"elapsed_ms":       fmt.Sprintf("%d", elapsedMS),
		"outcome":          outcome,
	})
}

// emitAudit publishes an audit event to aegis.orchestrator.audit.event in a
// background goroutine. Failures are logged and never propagated — audit
// emission must not affect the vault execute flow.
func (ve *VaultExecutor) emitAudit(eventType string, details map[string]string) {
	event := types.AuditEvent{
		EventID:   newUUID(),
		EventType: eventType,
		AgentID:   ve.agentID,
		TaskID:    ve.taskID,
		TraceID:   ve.traceID,
		Timestamp: time.Now().UTC(),
		Details:   details,
	}
	go func() {
		env := agentEnvelope{
			MessageID:       newUUID(),
			MessageType:     comms.MsgTypeAuditEvent,
			SourceComponent: "agents",
			CorrelationID:   ve.traceID,
			TraceID:         ve.traceID,
			Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
			SchemaVersion:   "1.0",
			Payload:         event,
		}
		data, err := json.Marshal(env)
		if err != nil {
			ve.log.Error("audit.event marshal failed", "event_type", eventType, "error", err)
			return
		}
		if _, err := ve.js.Publish(comms.SubjectAuditEvent, data); err != nil {
			ve.log.Error("audit.event publish failed", "event_type", eventType, "error", err)
		}
	}()
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
func (ve *VaultExecutor) Execute(ctx context.Context, operationType, credentialType string, operationParams json.RawMessage, timeoutSeconds int, onUpdate func(types.VaultOperationProgress)) ToolResult {
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
	// SessionLog and parent entry ID are threaded via context (EDD §13.4).
	sl := SessionLogFromCtx(ctx)
	parentID := ParentEntryIDFromCtx(ctx)
	toolCallEntryID := sl.Write(
		turnTypeToolCall,
		fmt.Sprintf("vault.execute.request dispatched: operation=%s request_id=%s", operationType, req.RequestID),
		parentID,
		req.RequestID,
	)

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
	// as required by the Comms envelope contract. Retried with exponential backoff
	// (see VaultExecConfig) — NATS may be transiently unavailable.
	if err := ve.publishRequest(req); err != nil {
		ve.mu.Lock()
		delete(ve.pending, req.RequestID)
		delete(ve.progressCallbacks, req.RequestID)
		ve.mu.Unlock()
		ve.log.Error("vault execute: publish failed after retries — returning TOOL_TIMEOUT",
			"request_id", req.RequestID,
			"attempts", ve.cfg.PublishMaxAttempts,
			"error", err,
		)
		ve.emitAudit(types.AuditEventVaultExecuteTimeout, map[string]string{
			"request_id":     req.RequestID,
			"operation_type": req.OperationType,
			"reason":         "publish_failed",
			"attempts":       fmt.Sprintf("%d", ve.cfg.PublishMaxAttempts),
		})
		return ToolResult{
			Content: fmt.Sprintf(
				"TOOL_TIMEOUT: vault execute request for %q could not be dispatched after %d attempts (NATS unavailable)",
				operationType, ve.cfg.PublishMaxAttempts,
			),
			IsError:        true,
			SessionEntryID: toolCallEntryID,
			Details: map[string]interface{}{
				"request_id":     req.RequestID,
				"operation_type": req.OperationType,
				"reason":         "publish_failed",
				"attempts":       ve.cfg.PublishMaxAttempts,
			},
		}
	}

	ve.log.Info("vault execute: request dispatched",
		"request_id", req.RequestID,
		"operation_type", req.OperationType,
		"timeout_seconds", req.TimeoutSeconds,
	)
	ve.emitAudit(types.AuditEventVaultExecuteRequest, map[string]string{
		"request_id":      req.RequestID,
		"operation_type":  req.OperationType,
		"credential_type": req.CredentialType,
		"timeout_seconds": fmt.Sprintf("%d", req.TimeoutSeconds),
	})

	// Step 4: Block with local deadline = timeout_seconds + 5s buffer (§13.1 Phase 2).
	localDeadline := time.Duration(req.TimeoutSeconds+5) * time.Second
	timer := time.NewTimer(localDeadline)
	defer timer.Stop()

	select {
	case vaultResult := <-resultCh:
		ve.log.Info("vault execute: result received",
			"request_id", req.RequestID,
			"operation_type", req.OperationType,
			"status", vaultResult.Status,
			"elapsed_ms", vaultResult.ElapsedMS,
		)
		ve.PublishMetricsEvent(types.MetricsEventVaultExecuteComplete, req.OperationType, vaultResult.ElapsedMS)
		ve.emitAudit(types.AuditEventVaultExecuteResult, map[string]string{
			"request_id":     req.RequestID,
			"operation_type": req.OperationType,
			"status":         vaultResult.Status,
			"elapsed_ms":     fmt.Sprintf("%d", vaultResult.ElapsedMS),
		})
		if vaultResult.Status == "scope_violation" {
			ve.emitAudit(types.AuditEventScopeViolation, map[string]string{
				"request_id":     req.RequestID,
				"operation_type": req.OperationType,
				"error_code":     vaultResult.ErrorCode,
			})
			// When the vault reports a missing credential, ask the user to supply
			// it via IO and then poll until it appears.
			// Skip if we're already inside a credential-retry loop (prevents recursion).
			if vaultResult.ErrorCode == "MISSING_CREDENTIAL" && !isCredentialRetryCtx(ctx) {
				if retried := ve.requestCredentialAndRetry(ctx, req.OperationType, credentialType, operationParams, timeoutSeconds, onUpdate, toolCallEntryID); retried != nil {
					return *retried
				}
			}
		}
		result := vaultResultToToolResult(vaultResult)
		result.SessionEntryID = toolCallEntryID
		return result

	case <-ctx.Done():
		// Step 5a: Context cancelled — steering directive interrupted this tool call (OQ-08).
		ve.mu.Lock()
		delete(ve.pending, req.RequestID)
		delete(ve.progressCallbacks, req.RequestID)
		ve.mu.Unlock()

		ve.publishCancellation(req.RequestID, req.OperationType, "context_cancelled")
		ve.emitAudit(types.AuditEventVaultExecuteTimeout, map[string]string{
			"request_id":     req.RequestID,
			"operation_type": req.OperationType,
			"reason":         "context_cancelled",
		})
		ve.log.Warn("vault execute: TOOL_INTERRUPTED — context cancelled by steering directive",
			"request_id", req.RequestID,
			"operation_type", req.OperationType,
		)
		return ToolResult{
			Content: fmt.Sprintf(
				"[TOOL_INTERRUPTED: %s was cancelled by steering directive or task cancellation]",
				req.OperationType,
			),
			IsError:        false,
			SessionEntryID: toolCallEntryID,
			Details: map[string]interface{}{
				"request_id":     req.RequestID,
				"operation_type": req.OperationType,
				"reason":         "context_cancelled",
			},
		}

	case <-timer.C:
		// Step 5b: Local deadline exceeded.
		ve.mu.Lock()
		delete(ve.pending, req.RequestID)
		delete(ve.progressCallbacks, req.RequestID)
		ve.mu.Unlock()

		ve.publishCancellation(req.RequestID, req.OperationType, "local_timeout")
		ve.emitAudit(types.AuditEventVaultExecuteTimeout, map[string]string{
			"request_id":       req.RequestID,
			"operation_type":   req.OperationType,
			"deadline_seconds": fmt.Sprintf("%d", req.TimeoutSeconds+5),
		})

		ve.log.Warn("vault execute: TOOL_TIMEOUT — local deadline exceeded",
			"request_id", req.RequestID,
			"deadline_seconds", req.TimeoutSeconds+5,
		)
		return ToolResult{
			Content: fmt.Sprintf(
				"TOOL_TIMEOUT: vault operation %q did not complete within %ds (timeout=%ds + 5s buffer)",
				req.OperationType, req.TimeoutSeconds+5, req.TimeoutSeconds,
			),
			IsError:        true,
			SessionEntryID: toolCallEntryID,
			Details: map[string]interface{}{
				"request_id":       req.RequestID,
				"operation_type":   req.OperationType,
				"deadline_seconds": req.TimeoutSeconds + 5,
			},
		}
	}
}

// credentialRequestTimeout is how long requestCredentialAndRetry polls for the
// user to enter a missing API key via the IO credential modal before giving up.
const (
	credentialRequestTimeout = 5 * time.Minute
	credentialPollInterval   = 8 * time.Second
)

// credentialRetryKey is the context key that marks a vault execute call as
// originating from inside requestCredentialAndRetry. Execute() skips the
// MISSING_CREDENTIAL retry path when this key is present, preventing infinite
// recursion when the credential is still missing during a poll attempt.
type credentialRetryKey struct{}

func withCredentialRetryCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, credentialRetryKey{}, true)
}

func isCredentialRetryCtx(ctx context.Context) bool {
	return ctx.Value(credentialRetryKey{}) != nil
}

// credentialUserInputRequest is the wire payload for a user_input credential
// request sent to aegis.orchestrator.credential.request. The gateway forwards
// this to IO which surfaces the CredentialModal to the user.
type credentialUserInputRequest struct {
	RequestID   string `json:"request_id"`
	AgentID     string `json:"agent_id"`
	TaskID      string `json:"task_id"`
	Operation   string `json:"operation"` // always "user_input"
	KeyName     string `json:"key_name"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// requestCredentialAndRetry is called when vault execute returns MISSING_CREDENTIAL.
//
// Deduplication: when N parallel tool calls all hit MISSING_CREDENTIAL for the same
// credentialType, only the FIRST goroutine publishes the credential.request and shows
// the IO modal. All others wait on the shared doneCh and retry once it closes.
//
// The retry loop polls vault_google_search every credentialPollInterval using a
// retry context (so Execute() does not re-enter this function). This avoids relying
// on a NATS credential.response subscription, which was unreliable due to stale
// spawn-time authorize messages being replayed on the same NATS subject.
//
// Returns nil if the credential flow cannot be started — the caller returns the
// original MISSING_CREDENTIAL error to the LLM.
func (ve *VaultExecutor) requestCredentialAndRetry(
	ctx context.Context,
	operationType, credentialType string,
	operationParams json.RawMessage,
	timeoutSeconds int,
	onUpdate func(types.VaultOperationProgress),
	sessionEntryID string,
) *ToolResult {
	if ve.nc == nil {
		return nil
	}

	// Deduplication: check if another goroutine is already handling this credential.
	ve.credMu.Lock()
	doneCh, alreadyPending := ve.credPending[credentialType]
	if !alreadyPending {
		doneCh = make(chan struct{})
		ve.credPending[credentialType] = doneCh
	}
	ve.credMu.Unlock()

	if alreadyPending {
		// Another goroutine owns the modal — wait for it to finish, then retry.
		ve.log.Info("vault execute: credential request already in progress — waiting",
			"credential_type", credentialType,
		)
		timer := time.NewTimer(credentialRequestTimeout)
		defer timer.Stop()
		select {
		case <-doneCh:
			retried := ve.Execute(withCredentialRetryCtx(ctx), operationType, credentialType, operationParams, timeoutSeconds, onUpdate)
			return &retried
		case <-ctx.Done():
			return nil
		case <-timer.C:
			r := ToolResult{
				Content:        fmt.Sprintf("No %s was provided within %s. Add your API key in Settings and try again.", credentialType, credentialRequestTimeout),
				IsError:        true,
				SessionEntryID: sessionEntryID,
			}
			return &r
		}
	}

	// This goroutine is the designated requester — broadcast to waiters when done.
	defer func() {
		ve.credMu.Lock()
		delete(ve.credPending, credentialType)
		ve.credMu.Unlock()
		close(doneCh)
	}()

	// Publish credential.request (user_input) — orchestrator routes to IO modal.
	credReqID := newUUID()
	label := credentialLabelFor(credentialType)
	credReq := credentialUserInputRequest{
		RequestID:   credReqID,
		AgentID:     ve.agentID,
		TaskID:      ve.taskID,
		Operation:   "user_input",
		KeyName:     credentialType,
		Label:       label,
		Description: fmt.Sprintf("%s requires a %s that has not been stored yet. Please enter it to continue.", operationType, credentialType),
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeCredentialRequest,
		SourceComponent: "agents",
		CorrelationID:   credReqID,
		TraceID:         ve.traceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         credReq,
	}
	data, err := json.Marshal(env)
	if err != nil {
		ve.log.Warn("vault execute: missing credential — marshal failed", "error", err)
		return nil
	}
	if _, err := ve.js.Publish(comms.SubjectCredentialRequest, data); err != nil {
		ve.log.Warn("vault execute: missing credential — publish credential.request failed", "error", err)
		return nil
	}

	ve.log.Info("vault execute: missing credential — polling for credential, modal shown to user",
		"request_id", credReqID,
		"credential_type", credentialType,
		"poll_interval", credentialPollInterval,
		"timeout", credentialRequestTimeout,
	)

	// Poll vault_google_search with withCredentialRetryCtx so Execute() does not
	// re-enter this function. The poll succeeds as soon as the user stores the key.
	deadline := time.Now().Add(credentialRequestTimeout)
	retryCtx := withCredentialRetryCtx(ctx)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(credentialPollInterval):
		}

		retried := ve.Execute(retryCtx, operationType, credentialType, operationParams, timeoutSeconds, onUpdate)
		if code, _ := retried.Details["error_code"].(string); code != "MISSING_CREDENTIAL" {
			// Either success or a different error — done.
			ve.log.Info("vault execute: credential poll result",
				"credential_type", credentialType,
				"is_error", retried.IsError,
			)
			retried.SessionEntryID = sessionEntryID
			return &retried
		}
		ve.log.Info("vault execute: credential still missing, continuing to poll",
			"credential_type", credentialType,
		)
	}

	r := ToolResult{
		Content:        fmt.Sprintf("No %s was provided within %s. Add your API key in Settings and try again.", credentialType, credentialRequestTimeout),
		IsError:        true,
		SessionEntryID: sessionEntryID,
	}
	return &r
}


// credentialLabelFor returns a human-readable label for the IO credential modal.
func credentialLabelFor(credentialType string) string {
	switch credentialType {
	case "serper_api_key":
		return "Serper API Key (Google Search)"
	case "github_token":
		return "GitHub Personal Access Token"
	case "web_api_key":
		return "Web API Key"
	case "search_api_key":
		return "Search API Key (Tavily)"
	default:
		return credentialType
	}
}

// publishRequest wraps and publishes a VaultOperationRequest to the Orchestrator
// with exponential backoff retries (VaultExecConfig.PublishMaxAttempts attempts,
// doubling from PublishBaseBackoff). A fresh MessageID is used on each attempt so
// the Comms deduplication window does not suppress legitimate retries; the
// CorrelationID (= request_id) is stable across attempts for Vault idempotency.
//
// Returns an error only after all attempts are exhausted — callers treat this as
// TOOL_TIMEOUT rather than a distinct error code.
func (ve *VaultExecutor) publishRequest(req types.VaultOperationRequest) error {
	var lastErr error
	for attempt := 0; attempt < ve.cfg.PublishMaxAttempts; attempt++ {
		if attempt > 0 {
			backoff := ve.cfg.PublishBaseBackoff * time.Duration(1<<uint(attempt-1))
			ve.log.Info("vault execute: retrying publish after backoff",
				"request_id", req.RequestID,
				"attempt", attempt+1,
				"backoff", backoff,
			)
			time.Sleep(backoff)
		}

		env := agentEnvelope{
			MessageID:       newUUID(), // fresh per attempt — avoids comms dedup suppression
			MessageType:     comms.MsgTypeVaultExecuteRequest,
			SourceComponent: "agents",
			CorrelationID:   req.RequestID, // stable — Vault idempotency key
			TraceID:         ve.traceID,
			Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
			SchemaVersion:   "1.0",
			Payload:         req,
		}
		data, err := json.Marshal(env)
		if err != nil {
			// Marshal failure is not transient — abort immediately.
			return fmt.Errorf("marshal vault request envelope: %w", err)
		}
		if _, err := ve.js.Publish(comms.SubjectVaultExecuteRequest, data); err != nil {
			lastErr = fmt.Errorf("jetstream publish vault request (attempt %d/%d): %w",
				attempt+1, ve.cfg.PublishMaxAttempts, err)
			continue
		}
		return nil
	}
	return fmt.Errorf("vault execute: NATS unavailable after %d attempts: %w",
		ve.cfg.PublishMaxAttempts, lastErr)
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
		TraceID:       ve.traceID,
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeVaultExecuteCancel,
		SourceComponent: "agents",
		CorrelationID:   requestID,
		TraceID:         ve.traceID,
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

// agentErrorEvent is the payload for error events published to
// aegis.orchestrator.error by the agent-process binary.
type agentErrorEvent struct {
	AgentID      string `json:"agent_id"`
	TaskID       string `json:"task_id"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	TraceID      string `json:"trace_id"`
}

// PublishError publishes an error event to aegis.orchestrator.error (JetStream
// at-least-once). Called by the ReAct loop on hard abort (e.g. CONTEXT_OVERFLOW).
// Best-effort: failures are logged but do not block the caller from returning
// the error and exiting.
func (ve *VaultExecutor) PublishError(errorCode, errorMessage, traceID string) {
	payload := agentErrorEvent{
		AgentID:      ve.agentID,
		TaskID:       ve.taskID,
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
		TraceID:      traceID,
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeError,
		SourceComponent: "agents",
		CorrelationID:   ve.taskID,
		TraceID:         traceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         payload,
	}
	data, err := json.Marshal(env)
	if err != nil {
		ve.log.Warn("publish error event: marshal failed", "error", err)
		return
	}
	if _, err := ve.js.Publish(comms.SubjectError, data); err != nil {
		ve.log.Warn("publish error event: jetstream publish failed", "error", err)
	}
}

// PublishMetricsEvent publishes a lightweight at-most-once metrics event to
// aegis.metrics.event so the aegis-agents process can aggregate Prometheus
// counters for events that occur inside agent-process subprocesses (EDD §13.3).
// Failures are silently logged — losing a metrics event is acceptable.
func (ve *VaultExecutor) PublishMetricsEvent(eventType, operationType string, elapsedMS int) {
	payload := types.MetricsEvent{
		AgentID:       ve.agentID,
		EventType:     eventType,
		OperationType: operationType,
		ElapsedMS:     elapsedMS,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		ve.log.Warn("metrics event: marshal failed", "event_type", eventType, "error", err)
		return
	}
	// Core NATS at-most-once publish — no JetStream acknowledgement required.
	if err := ve.nc.Publish(comms.SubjectMetricsEvent, data); err != nil {
		ve.log.Warn("metrics event: publish failed", "event_type", eventType, "error", err)
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

// parseEnvInt reads a positive integer from an env var.
// Returns defaultVal (which may be 0 to signal "use package default") if the
// variable is unset, empty, or not a positive integer.
func parseEnvInt(key string, defaultVal int) int {
	s := os.Getenv(key)
	if s == "" {
		return defaultVal
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 1 {
		return defaultVal
	}
	return n
}

// parseEnvDuration reads a Go duration string from an env var.
// Returns defaultVal if the variable is unset, empty, or not a valid positive duration.
func parseEnvDuration(key string, defaultVal time.Duration) time.Duration {
	s := os.Getenv(key)
	if s == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return defaultVal
	}
	return d
}

// newUUID returns a random UUID v4 string.
func newUUID() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
