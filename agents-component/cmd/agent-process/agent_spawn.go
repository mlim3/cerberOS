// Package main — agent_spawn.go implements the async agent.spawn.request/response
// flow for the agent-as-tool pattern (issue #67, EDD §13.6).
//
// Flow:
//  1. Record the request_id in the session log via state.write BEFORE the
//     goroutine yields — ensures crash recovery can identify in-flight spawns.
//  2. Register a result channel keyed by request_id BEFORE publishing the request
//     to avoid a race where the response arrives before we start waiting.
//  3. Publish AgentSpawnRequest to aegis.orchestrator.agent.spawn.request
//     (JetStream at-least-once). CorrelationID = request_id.
//  4. Block on the result channel with local deadline = timeout_seconds + 10s buffer.
//  5. On deadline or context cancel: return TOOL_TIMEOUT content to the LLM.
//
// AgentSpawner is initialised once per agent-process and shared across all spawn_agent
// tool invocations. It subscribes to aegis.agents.agent.spawn.response with a durable
// JetStream consumer scoped to this agent_id so that delayed responses arriving after
// a crash are still delivered to the recovered process.
//
// trace_id and user_context_id are propagated unchanged from the parent so that the
// parent-child chain shares the same distributed trace and user context.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	nats "github.com/nats-io/nats.go"
)

const (
	// spawnPublishMaxAttempts is the default number of JetStream publish attempts
	// before TOOL_TIMEOUT is returned to the LLM. Override via env var.
	spawnPublishMaxAttempts = 3

	// spawnPublishBaseBackoff is the initial sleep between publish retries.
	spawnPublishBaseBackoff = time.Second

	// spawnLocalDeadlineBuffer is added to the requested timeout_seconds to form
	// the local blocking deadline. Accounts for Orchestrator routing overhead and
	// child agent task.accepted latency.
	spawnLocalDeadlineBuffer = 10 * time.Second

	// spawnDefaultTimeoutSeconds is used when the LLM omits timeout_seconds.
	spawnDefaultTimeoutSeconds = 300

	// spawnMinTimeoutSeconds is the floor applied to any explicit timeout_seconds
	// value. Web search + NATS round-trip overhead typically runs 30–50s; a
	// minimum of 120s prevents LLM-chosen low values from causing spurious
	// TOOL_TIMEOUT failures.
	spawnMinTimeoutSeconds = 120
)

// AgentSpawner manages the async agent.spawn.request/response flow (issue #67).
// One instance is created per agent-process; nil means agent spawning is
// unavailable (NATS env vars absent) — the spawn_agent tool is excluded when nil.
type AgentSpawner struct {
	nc            *nats.Conn
	js            nats.JetStreamContext
	agentID       string
	taskID        string
	traceID       string
	userContextID string
	log           *slog.Logger

	publishMaxAttempts int
	publishBaseBackoff time.Duration

	mu      sync.Mutex
	pending map[string]chan types.AgentSpawnResponse // requestID → result channel
}

// NewAgentSpawner connects to NATS, subscribes to agent.spawn.response, and
// returns an AgentSpawner ready to dispatch spawn_agent tool calls.
//
// Required environment:
//
//	AEGIS_NATS_URL  — NATS server address (injected by Lifecycle Manager)
//	AEGIS_AGENT_ID  — this agent's identity (injected by Lifecycle Manager)
//
// Returns nil (non-fatal) if either env var is absent or NATS is unreachable.
func NewAgentSpawner(log *slog.Logger, taskID, traceID, userContextID string) *AgentSpawner {
	natsURL := os.Getenv("AEGIS_NATS_URL")
	agentID := os.Getenv("AEGIS_AGENT_ID")
	if natsURL == "" || agentID == "" {
		log.Warn("agent spawner disabled: AEGIS_NATS_URL or AEGIS_AGENT_ID not set")
		return nil
	}

	nc, err := nats.Connect(natsURL,
		nats.Name("aegis-spawn-"+agentID),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		log.Warn("agent spawner: NATS connect failed — spawn_agent disabled", "error", err)
		return nil
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		log.Warn("agent spawner: JetStream init failed — spawn_agent disabled", "error", err)
		return nil
	}

	as := &AgentSpawner{
		nc:                 nc,
		js:                 js,
		agentID:            agentID,
		taskID:             taskID,
		traceID:            traceID,
		userContextID:      userContextID,
		log:                log,
		publishMaxAttempts: parseEnvInt("AEGIS_SPAWN_PUBLISH_MAX_ATTEMPTS", spawnPublishMaxAttempts),
		publishBaseBackoff: parseEnvDuration("AEGIS_SPAWN_PUBLISH_BASE_BACKOFF", spawnPublishBaseBackoff),
		pending:            make(map[string]chan types.AgentSpawnResponse),
	}

	// Durable consumer name is stable per agent_id: survives crash/respawn so
	// delayed responses (arrived after crash) are received on recovery.
	durable := "agent-spawn-response-" + agentID
	if err := as.subscribeResponses(durable); err != nil {
		nc.Close()
		log.Warn("agent spawner: subscribe response failed — spawn_agent disabled", "error", err)
		return nil
	}

	log.Info("agent spawner ready", "agent_id", agentID, "durable", durable)
	return as
}

// subscribeResponses registers a durable JetStream push consumer on
// aegis.agents.agent.spawn.response. All responses are routed by request_id to
// the waiting goroutine via the pending map.
func (as *AgentSpawner) subscribeResponses(durable string) error {
	_, err := as.js.Subscribe(
		comms.SubjectAgentSpawnResponse,
		func(msg *nats.Msg) {
			_ = msg.Ack() // ack immediately; routing is request_id-scoped
			as.routeResponse(msg.Data)
		},
		nats.Durable(durable),
		nats.AckExplicit(),
		nats.DeliverNew(),
	)
	return err
}

// routeResponse unwraps an inbound envelope and routes the AgentSpawnResponse to
// the goroutine waiting on the matching request_id. Responses for other agents
// are silently filtered — multiple agent-processes share the same JetStream stream.
func (as *AgentSpawner) routeResponse(data []byte) {
	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		as.log.Warn("agent spawn response: unmarshal envelope failed", "error", err)
		return
	}

	var resp types.AgentSpawnResponse
	if err := json.Unmarshal(env.Payload, &resp); err != nil {
		as.log.Warn("agent spawn response: unmarshal payload failed", "error", err)
		return
	}

	// Multiple agent-processes share the same JetStream stream — filter our own.
	if resp.ParentAgentID != as.agentID {
		return
	}

	as.mu.Lock()
	ch, ok := as.pending[resp.RequestID]
	if ok {
		delete(as.pending, resp.RequestID)
	}
	as.mu.Unlock()

	if !ok {
		as.log.Warn("agent spawn response: no waiter for request_id (late delivery or duplicate)",
			"request_id", resp.RequestID,
			"child_agent_id", resp.ChildAgentID,
		)
		return
	}

	select {
	case ch <- resp:
	default:
		as.log.Warn("agent spawn response: channel full, dropping response", "request_id", resp.RequestID)
	}
}

// Spawn parses the LLM tool input, builds an AgentSpawnRequest, publishes it to
// the Orchestrator, and blocks until the child agent completes or the deadline fires.
//
// Session log entry is written BEFORE yielding so crash recovery can identify
// in-flight spawn requests.
func (as *AgentSpawner) Spawn(ctx context.Context, raw json.RawMessage) ToolResult {
	var params struct {
		Instructions   string   `json:"instructions"`
		RequiredSkills []string `json:"required_skills"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("spawn_agent: invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.Instructions == "" {
		return ToolResult{
			Content: "spawn_agent: instructions must not be empty",
			IsError: true,
			Details: map[string]interface{}{"error": "instructions required"},
		}
	}
	if len(params.RequiredSkills) == 0 {
		return ToolResult{
			Content: "spawn_agent: required_skills must contain at least one skill domain",
			IsError: true,
			Details: map[string]interface{}{"error": "required_skills required"},
		}
	}
	if params.TimeoutSeconds <= 0 {
		params.TimeoutSeconds = spawnDefaultTimeoutSeconds
	} else if params.TimeoutSeconds < spawnMinTimeoutSeconds {
		params.TimeoutSeconds = spawnMinTimeoutSeconds
	}

	req := types.AgentSpawnRequest{
		RequestID:      newUUID(),
		ParentAgentID:  as.agentID,
		ParentTaskID:   as.taskID,
		RequiredSkills: params.RequiredSkills,
		Instructions:   params.Instructions,
		TimeoutSeconds: params.TimeoutSeconds,
		TraceID:        as.traceID,
		UserContextID:  as.userContextID,
	}

	// Step 1: Record request_id in session log BEFORE the goroutine yields.
	// This is the invariant for crash recovery — an in-flight spawn with no
	// response can be identified and re-requested on recovery.
	sl := SessionLogFromCtx(ctx)
	parentID := ParentEntryIDFromCtx(ctx)
	toolCallEntryID := sl.Write(
		turnTypeToolCall,
		fmt.Sprintf("agent.spawn.request dispatched: skills=%v request_id=%s", req.RequiredSkills, req.RequestID),
		parentID,
		req.RequestID,
	)

	// Step 2: Register pending channel BEFORE publishing to avoid result-before-wait race.
	resultCh := make(chan types.AgentSpawnResponse, 1)
	as.mu.Lock()
	as.pending[req.RequestID] = resultCh
	as.mu.Unlock()

	// Step 3: Publish AgentSpawnRequest (JetStream at-least-once).
	// CorrelationID = request_id as required by the Comms envelope contract.
	if err := as.publishRequest(req); err != nil {
		as.mu.Lock()
		delete(as.pending, req.RequestID)
		as.mu.Unlock()
		as.log.Error("agent spawn: publish failed after retries — returning TOOL_TIMEOUT",
			"request_id", req.RequestID,
			"attempts", as.publishMaxAttempts,
			"error", err,
		)
		as.emitAudit(types.AuditEventAgentSpawnRequest, map[string]string{
			"request_id":      req.RequestID,
			"required_skills": fmt.Sprintf("%v", req.RequiredSkills),
			"reason":          "publish_failed",
		})
		return ToolResult{
			Content: fmt.Sprintf(
				"TOOL_TIMEOUT: spawn_agent request could not be dispatched after %d attempts (NATS unavailable)",
				as.publishMaxAttempts,
			),
			IsError:        true,
			SessionEntryID: toolCallEntryID,
			Details: map[string]interface{}{
				"request_id":      req.RequestID,
				"required_skills": req.RequiredSkills,
				"reason":          "publish_failed",
				"attempts":        as.publishMaxAttempts,
			},
		}
	}

	as.log.Info("agent spawn: request dispatched",
		"request_id", req.RequestID,
		"required_skills", req.RequiredSkills,
		"timeout_seconds", req.TimeoutSeconds,
	)
	as.emitAudit(types.AuditEventAgentSpawnRequest, map[string]string{
		"request_id":      req.RequestID,
		"required_skills": fmt.Sprintf("%v", req.RequiredSkills),
		"timeout_seconds": fmt.Sprintf("%d", req.TimeoutSeconds),
	})

	// Step 4: Block with local deadline = timeout_seconds + buffer (§13.1 Phase 2 pattern).
	localDeadline := time.Duration(req.TimeoutSeconds)*time.Second + spawnLocalDeadlineBuffer
	timer := time.NewTimer(localDeadline)
	defer timer.Stop()

	select {
	case spawnResp := <-resultCh:
		as.log.Info("agent spawn: response received",
			"request_id", req.RequestID,
			"child_agent_id", spawnResp.ChildAgentID,
			"status", spawnResp.Status,
		)
		as.emitAudit(types.AuditEventAgentSpawnResponse, map[string]string{
			"request_id":     req.RequestID,
			"child_agent_id": spawnResp.ChildAgentID,
			"status":         spawnResp.Status,
		})
		result := spawnResponseToToolResult(spawnResp)
		result.SessionEntryID = toolCallEntryID
		return result

	case <-ctx.Done():
		// Context cancelled — steering directive interrupted this tool call (OQ-08).
		as.mu.Lock()
		delete(as.pending, req.RequestID)
		as.mu.Unlock()

		as.log.Warn("agent spawn: TOOL_INTERRUPTED — context cancelled by steering directive",
			"request_id", req.RequestID,
		)
		return ToolResult{
			Content: fmt.Sprintf(
				"[TOOL_INTERRUPTED: spawn_agent for skills %v was cancelled by steering directive or task cancellation]",
				req.RequiredSkills,
			),
			IsError:        false,
			SessionEntryID: toolCallEntryID,
			Details: map[string]interface{}{
				"request_id":      req.RequestID,
				"required_skills": req.RequiredSkills,
				"reason":          "context_cancelled",
			},
		}

	case <-timer.C:
		// Local deadline exceeded.
		as.mu.Lock()
		delete(as.pending, req.RequestID)
		as.mu.Unlock()

		as.log.Warn("agent spawn: TOOL_TIMEOUT — local deadline exceeded",
			"request_id", req.RequestID,
			"deadline_seconds", req.TimeoutSeconds+int(spawnLocalDeadlineBuffer.Seconds()),
		)
		return ToolResult{
			Content: fmt.Sprintf(
				"TOOL_TIMEOUT: spawn_agent did not receive a response within %ds (timeout=%ds + %ds buffer)",
				req.TimeoutSeconds+int(spawnLocalDeadlineBuffer.Seconds()),
				req.TimeoutSeconds,
				int(spawnLocalDeadlineBuffer.Seconds()),
			),
			IsError:        true,
			SessionEntryID: toolCallEntryID,
			Details: map[string]interface{}{
				"request_id":       req.RequestID,
				"required_skills":  req.RequiredSkills,
				"deadline_seconds": req.TimeoutSeconds + int(spawnLocalDeadlineBuffer.Seconds()),
			},
		}
	}
}

// publishRequest wraps and publishes an AgentSpawnRequest to the Orchestrator
// with exponential backoff retries. A fresh MessageID is used on each attempt so
// the Comms deduplication window does not suppress legitimate retries; the
// CorrelationID (= request_id) is stable across attempts.
//
// Returns an error only after all attempts are exhausted.
func (as *AgentSpawner) publishRequest(req types.AgentSpawnRequest) error {
	var lastErr error
	for attempt := 0; attempt < as.publishMaxAttempts; attempt++ {
		if attempt > 0 {
			backoff := as.publishBaseBackoff * time.Duration(1<<uint(attempt-1))
			as.log.Info("agent spawn: retrying publish after backoff",
				"request_id", req.RequestID,
				"attempt", attempt+1,
				"backoff", backoff,
			)
			time.Sleep(backoff)
		}

		env := agentEnvelope{
			MessageID:       newUUID(), // fresh per attempt — avoids comms dedup suppression
			MessageType:     comms.MsgTypeAgentSpawnRequest,
			SourceComponent: "agents",
			CorrelationID:   req.RequestID, // stable — Orchestrator routing key
			Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
			SchemaVersion:   "1.0",
			Payload:         req,
		}
		data, err := json.Marshal(env)
		if err != nil {
			// Marshal failure is not transient — abort immediately.
			return fmt.Errorf("marshal agent spawn request envelope: %w", err)
		}
		if _, err := as.js.Publish(comms.SubjectAgentSpawnRequest, data); err != nil {
			lastErr = fmt.Errorf("jetstream publish agent spawn request (attempt %d/%d): %w",
				attempt+1, as.publishMaxAttempts, err)
			continue
		}
		return nil
	}
	return fmt.Errorf("agent spawn: NATS unavailable after %d attempts: %w",
		as.publishMaxAttempts, lastErr)
}

// emitAudit publishes an audit event to aegis.orchestrator.audit.event in a
// background goroutine. Failures are logged and never propagated.
func (as *AgentSpawner) emitAudit(eventType string, details map[string]string) {
	event := types.AuditEvent{
		EventID:   newUUID(),
		EventType: eventType,
		AgentID:   as.agentID,
		TaskID:    as.taskID,
		TraceID:   as.traceID,
		Timestamp: time.Now().UTC(),
		Details:   details,
	}
	go func() {
		env := agentEnvelope{
			MessageID:       newUUID(),
			MessageType:     comms.MsgTypeAuditEvent,
			SourceComponent: "agents",
			CorrelationID:   as.traceID,
			Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
			SchemaVersion:   "1.0",
			Payload:         event,
		}
		data, err := json.Marshal(env)
		if err != nil {
			as.log.Error("audit.event marshal failed", "event_type", eventType, "error", err)
			return
		}
		if _, err := as.js.Publish(comms.SubjectAuditEvent, data); err != nil {
			as.log.Error("audit.event publish failed", "event_type", eventType, "error", err)
		}
	}()
}

// Close drains the NATS connection used by the agent spawner.
func (as *AgentSpawner) Close() {
	if as.nc != nil {
		as.nc.Close()
	}
}

// spawnResponseToToolResult converts an AgentSpawnResponse to the ToolResult
// that enters the LLM context. Result content is truncated to 16KB (§13.2).
// Error messages must not expose internal paths or vault details (NFR-08).
func spawnResponseToToolResult(r types.AgentSpawnResponse) ToolResult {
	switch r.Status {
	case "success":
		content := r.Result
		if len(content) > maxContentBytes {
			content = content[:maxContentBytes] + "\n[CONTENT TRUNCATED — child agent result exceeded 16KB limit]"
		}
		return ToolResult{
			Content: content,
			IsError: false,
			Details: map[string]interface{}{
				"request_id":     r.RequestID,
				"child_agent_id": r.ChildAgentID,
				"status":         r.Status,
			},
		}

	default: // "failed"
		msg := r.ErrorMessage
		if msg == "" {
			msg = r.ErrorCode
		}
		if msg == "" {
			msg = "child agent failed without a reason"
		}
		return ToolResult{
			Content: fmt.Sprintf("spawn_agent error [%s]: %s", r.Status, msg),
			IsError: true,
			Details: map[string]interface{}{
				"request_id":     r.RequestID,
				"child_agent_id": r.ChildAgentID,
				"status":         r.Status,
				"error_code":     r.ErrorCode,
			},
		}
	}
}
