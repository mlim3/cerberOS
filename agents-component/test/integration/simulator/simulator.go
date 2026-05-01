// Package simulator is the partner simulator for integration testing.
//
// It subscribes to all aegis.orchestrator.* topics and publishes synthetic
// responses to the corresponding aegis.agents.* topics. Required because real
// partner implementations (Orchestrator, Vault) will not be available during
// M3 and M4 development.
//
// Synthetic responses provided:
//   - credential.response    on receipt of credential.request
//   - vault.execute.result   on receipt of vault.execute.request
//   - state.write.ack        on receipt of state.write
//   - state.read.response    on receipt of state.read.request
//
// Observation-only (no response): task.accepted, task.result, task.failed,
// agent.status, clarification.request, audit.event, error.
//
// Driving integration tests:
//
//	sim.PublishTaskInbound(spec) — publish a task.inbound to the Agents Component
package simulator

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/cerberOS/agents-component/pkg/types"
	"github.com/nats-io/nats.go"
)

// Stream names.
const (
	streamOrchestrator = "AEGIS_ORCHESTRATOR"
	streamAgents       = "AEGIS_AGENTS"
)

// Durable consumer names for the simulator — must not collide with the
// agents component's own consumer names (prefixed "agents-").
const (
	consumerCredentialRequest   = "sim-credential-request"
	consumerVaultExecuteRequest = "sim-vault-execute-request"
	consumerStateWrite          = "sim-state-write"
	consumerStateReadRequest    = "sim-state-read-request"
	consumerTaskAccepted        = "sim-task-accepted"
	consumerTaskResult          = "sim-task-result"
	consumerTaskFailed          = "sim-task-failed"
	consumerAgentStatus         = "sim-agent-status"
	consumerClarificationReq    = "sim-clarification-request"
	consumerAuditEvent          = "sim-audit-event"
	consumerError               = "sim-error"
)

// — Wire types ————————————————————————————————————————————————————————————

// inboundEnvelope is the standard envelope format the Agents Component sends.
// Mirrors comms.outboundEnvelope — duplicated here so the simulator package
// has no import dependency on internal/comms.
type inboundEnvelope struct {
	MessageID     string          `json:"message_id"`
	MessageType   string          `json:"message_type"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// outboundEnvelope is the standard envelope format the simulator publishes.
type outboundEnvelope struct {
	MessageID       string      `json:"message_id"`
	MessageType     string      `json:"message_type"`
	SourceComponent string      `json:"source_component"`
	CorrelationID   string      `json:"correlation_id,omitempty"`
	Timestamp       string      `json:"timestamp"`
	SchemaVersion   string      `json:"schema_version"`
	Payload         interface{} `json:"payload"`
}

// vaultExecuteRequest mirrors the vault.execute.request payload shape (ADR-004).
// Defined locally because pkg/types does not yet expose this type.
type vaultExecuteRequest struct {
	RequestID       string          `json:"request_id"`
	AgentID         string          `json:"agent_id"`
	TaskID          string          `json:"task_id"`
	PermissionToken string          `json:"permission_token"`
	OperationType   string          `json:"operation_type"`
	OperationParams json.RawMessage `json:"operation_params"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
	CredentialType  string          `json:"credential_type"`
}

// vaultExecuteResult is the synthetic vault.execute.result sent back to agents.
type vaultExecuteResult struct {
	RequestID       string          `json:"request_id"`
	AgentID         string          `json:"agent_id"`
	Status          string          `json:"status"`
	OperationResult json.RawMessage `json:"operation_result,omitempty"`
	ErrorCode       string          `json:"error_code,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	ElapsedMS       int             `json:"elapsed_ms"`
}

// stateWriteAck has been replaced by types.StateWriteAck.

// — Simulator —————————————————————————————————————————————————————————————

// Simulator subscribes to all aegis.orchestrator.* topics and publishes
// synthetic responses to the corresponding aegis.agents.* topics.
type Simulator struct {
	nc             *nats.Conn
	js             nats.JetStreamContext
	log            *slog.Logger
	mu             sync.Mutex
	subs           []*nats.Subscription
	credMu         sync.RWMutex
	credTokens     map[string]string // agentID → permission_token granted
	consumerSuffix string            // appended to all durable consumer names when non-empty
}

// New connects to NATS at natsURL and returns a Simulator ready to be started.
func New(natsURL string) (*Simulator, error) {
	nc, err := nats.Connect(
		natsURL,
		nats.Name("aegis-partner-simulator"),
	)
	if err != nil {
		return nil, fmt.Errorf("simulator: connect to %q: %w", natsURL, err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("simulator: create JetStream context: %w", err)
	}

	return &Simulator{
		nc:         nc,
		js:         js,
		log:        slog.Default(),
		credTokens: make(map[string]string),
	}, nil
}

// WithLogger replaces the default logger.
func (s *Simulator) WithLogger(l *slog.Logger) *Simulator {
	s.log = l
	return s
}

// WithConsumerSuffix appends suffix to every durable consumer name used by
// Start. Use in tests to give each simulator instance a unique set of
// consumers and avoid "consumer is already bound" errors when multiple
// simulator instances share the same NATS server.
func (s *Simulator) WithConsumerSuffix(suffix string) *Simulator {
	s.consumerSuffix = suffix
	return s
}

// Start creates the required JetStream streams (idempotent) and subscribes to
// all aegis.orchestrator.* topics. Synthetic responses are published
// automatically upon receipt of each request.
func (s *Simulator) Start() error {
	if err := s.ensureStreams(); err != nil {
		return err
	}

	type subscription struct {
		subject string
		durable string
		handler func(*nats.Msg)
	}

	subscriptions := []subscription{
		// Request-response pairs — simulator publishes a synthetic reply.
		{
			subject: "aegis.orchestrator.credential.request",
			durable: consumerCredentialRequest,
			handler: s.handleCredentialRequest,
		},
		{
			subject: "aegis.orchestrator.vault.execute.request",
			durable: consumerVaultExecuteRequest,
			handler: s.handleVaultExecuteRequest,
		},
		{
			subject: "aegis.orchestrator.state.write",
			durable: consumerStateWrite,
			handler: s.handleStateWrite,
		},
		{
			subject: "aegis.orchestrator.state.read.request",
			durable: consumerStateReadRequest,
			handler: s.handleStateReadRequest,
		},
		// Observation-only — logged but no synthetic reply needed.
		{
			subject: "aegis.orchestrator.task.accepted",
			durable: consumerTaskAccepted,
			handler: s.handleObserve,
		},
		{
			subject: "aegis.orchestrator.task.result",
			durable: consumerTaskResult,
			handler: s.handleObserve,
		},
		{
			subject: "aegis.orchestrator.task.failed",
			durable: consumerTaskFailed,
			handler: s.handleObserve,
		},
		{
			subject: "aegis.orchestrator.agent.status",
			durable: consumerAgentStatus,
			handler: s.handleObserve,
		},
		{
			subject: "aegis.orchestrator.clarification.request",
			durable: consumerClarificationReq,
			handler: s.handleObserve,
		},
		{
			subject: "aegis.orchestrator.audit.event",
			durable: consumerAuditEvent,
			handler: s.handleObserve,
		},
		{
			subject: "aegis.orchestrator.error",
			durable: consumerError,
			handler: s.handleObserve,
		},
	}

	for _, sub := range subscriptions {
		consumer := sub.durable
		if s.consumerSuffix != "" {
			consumer = sub.durable + "-" + s.consumerSuffix
		}
		natsSub, err := s.js.Subscribe(
			sub.subject,
			sub.handler,
			nats.Durable(consumer),
			nats.DeliverNew(),
			nats.AckExplicit(),
		)
		if err != nil {
			return fmt.Errorf("simulator: subscribe %q (consumer %q): %w", sub.subject, consumer, err)
		}
		s.mu.Lock()
		s.subs = append(s.subs, natsSub)
		s.mu.Unlock()
	}

	s.log.Info("simulator started", "subscriptions", len(subscriptions))
	return nil
}

// Stop unsubscribes all consumers and closes the NATS connection.
func (s *Simulator) Stop() error {
	s.mu.Lock()
	for _, sub := range s.subs {
		_ = sub.Unsubscribe()
	}
	s.subs = nil
	s.mu.Unlock()
	s.nc.Close()
	return nil
}

// PublishTaskInbound publishes a TaskSpec to aegis.agents.task.inbound,
// simulating the Orchestrator dispatching a task to the Agents Component.
// Use this to drive integration tests.
func (s *Simulator) PublishTaskInbound(spec types.TaskSpec) error {
	if err := s.publish("aegis.agents.task.inbound", "task.inbound", spec.TaskID, spec); err != nil {
		return fmt.Errorf("simulator: PublishTaskInbound: %w", err)
	}
	s.log.Info("simulator: task.inbound published", "task_id", spec.TaskID, "trace_id", spec.TraceID)
	return nil
}

// CredentialTokens returns a snapshot of the permission tokens issued by the
// simulator, keyed by agent_id. Use in load tests to verify that each agent
// received a distinct token (credential scope bleed check).
func (s *Simulator) CredentialTokens() map[string]string {
	s.credMu.RLock()
	defer s.credMu.RUnlock()
	out := make(map[string]string, len(s.credTokens))
	for k, v := range s.credTokens {
		out[k] = v
	}
	return out
}

// — Handlers ——————————————————————————————————————————————————————————————

func (s *Simulator) handleCredentialRequest(msg *nats.Msg) {
	env := s.unwrap(msg)
	if env == nil {
		_ = msg.Ack()
		return
	}

	var req types.CredentialRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		s.log.Error("simulator: unmarshal credential.request payload", "err", err)
		_ = msg.Nak()
		return
	}

	token := "sim-token-" + req.AgentID
	resp := types.CredentialResponse{
		RequestID:       req.RequestID,
		Status:          "granted",
		PermissionToken: token,
	}
	s.credMu.Lock()
	s.credTokens[req.AgentID] = token
	s.credMu.Unlock()

	// CorrelationID MUST be set to req.RequestID so the natsBroker can route the
	// response to the waiting PreAuthorize goroutine via msg.CorrelationID.
	if err := s.publish("aegis.agents.credential.response", "credential.response", req.RequestID, resp); err != nil {
		s.log.Error("simulator: publish credential.response", "err", err, "agent_id", req.AgentID)
		_ = msg.Nak()
		return
	}

	s.log.Info("simulator: credential.response sent", "agent_id", req.AgentID, "request_id", req.RequestID)
	_ = msg.Ack()
}

func (s *Simulator) handleVaultExecuteRequest(msg *nats.Msg) {
	env := s.unwrap(msg)
	if env == nil {
		_ = msg.Ack()
		return
	}

	var req vaultExecuteRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		s.log.Error("simulator: unmarshal vault.execute.request payload", "err", err)
		_ = msg.Nak()
		return
	}

	result := vaultExecuteResult{
		RequestID:       req.RequestID,
		AgentID:         req.AgentID,
		Status:          "success",
		OperationResult: json.RawMessage(`{"simulated":true,"operation_type":"` + req.OperationType + `"}`),
		ElapsedMS:       1,
	}
	if err := s.publish("aegis.agents.vault.execute.result", "vault.execute.result", req.RequestID, result); err != nil {
		s.log.Error("simulator: publish vault.execute.result", "err", err, "request_id", req.RequestID)
		_ = msg.Nak()
		return
	}

	s.log.Info("simulator: vault.execute.result sent",
		"request_id", req.RequestID,
		"agent_id", req.AgentID,
		"operation_type", req.OperationType,
	)
	_ = msg.Ack()
}

func (s *Simulator) handleStateWrite(msg *nats.Msg) {
	env := s.unwrap(msg)
	if env == nil {
		_ = msg.Ack()
		return
	}

	var write types.MemoryWrite
	if err := json.Unmarshal(env.Payload, &write); err != nil {
		s.log.Error("simulator: unmarshal state.write payload", "err", err)
		_ = msg.Nak()
		return
	}

	// Prefer the envelope correlation_id (= request_id set by the natsClient) so
	// the ack can be routed back to the correct waiting goroutine. Fall back to the
	// payload RequestID, then to AgentID for simulator backward compatibility.
	correlationID := env.CorrelationID
	if correlationID == "" {
		correlationID = write.RequestID
	}
	if correlationID == "" {
		correlationID = write.AgentID
	}

	ack := types.StateWriteAck{
		RequestID: write.RequestID,
		AgentID:   write.AgentID,
		Status:    "accepted",
	}
	if err := s.publish("aegis.agents.state.write.ack", "state.write.ack", correlationID, ack); err != nil {
		s.log.Error("simulator: publish state.write.ack", "err", err, "agent_id", write.AgentID)
		_ = msg.Nak()
		return
	}

	s.log.Info("simulator: state.write.ack sent",
		"agent_id", write.AgentID,
		"data_type", write.DataType,
		"request_id", write.RequestID,
		"require_ack", write.RequireAck,
	)
	_ = msg.Ack()
}

func (s *Simulator) handleStateReadRequest(msg *nats.Msg) {
	env := s.unwrap(msg)
	if env == nil {
		_ = msg.Ack()
		return
	}

	var req types.MemoryReadRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		s.log.Error("simulator: unmarshal state.read.request payload", "err", err)
		_ = msg.Nak()
		return
	}

	// Some data types are answered by the orchestrator (e.g. system_log via the
	// Loki bridge, skill_cache from the agentStore). If the simulator responds
	// first with empty records it races and beats the real answer, so skip these.
	switch req.DataType {
	case "system_log":
		_ = msg.Ack()
		s.log.Info("simulator: skipping system_log read (routed to Loki via orchestrator)", "agent_id", req.AgentID)
		return
	case "skill_cache":
		_ = msg.Ack()
		s.log.Info("simulator: skipping skill_cache read (answered by orchestrator agentStore)", "agent_id", req.AgentID)
		return
	}

	resp := types.MemoryResponse{
		AgentID: req.AgentID,
		Records: []types.MemoryWrite{},
		TraceID: req.TraceID,
	}
	if err := s.publish("aegis.agents.state.read.response", "state.read.response", req.AgentID, resp); err != nil {
		s.log.Error("simulator: publish state.read.response", "err", err, "agent_id", req.AgentID)
		_ = msg.Nak()
		return
	}

	s.log.Info("simulator: state.read.response sent", "agent_id", req.AgentID, "trace_id", req.TraceID)
	_ = msg.Ack()
}

// handleObserve logs observation-only messages and acks them without reply.
func (s *Simulator) handleObserve(msg *nats.Msg) {
	env := s.unwrap(msg)
	if env != nil {
		s.log.Info("simulator: observed",
			"subject", msg.Subject,
			"message_type", env.MessageType,
			"correlation_id", env.CorrelationID,
		)
	}
	_ = msg.Ack()
}

// — Internals —————————————————————————————————————————————————————————————

// ensureStreams creates AEGIS_ORCHESTRATOR and AEGIS_AGENTS streams if absent.
// Safe to call repeatedly — does nothing if streams already exist.
func (s *Simulator) ensureStreams() error {
	streams := []struct {
		name     string
		subjects []string
	}{
		{
			name:     streamOrchestrator,
			subjects: []string{"aegis.orchestrator.>"},
		},
		{
			name:     streamAgents,
			subjects: []string{"aegis.agents.>"},
		},
	}

	for _, cfg := range streams {
		_, err := s.js.StreamInfo(cfg.name)
		if err == nats.ErrStreamNotFound {
			_, err = s.js.AddStream(&nats.StreamConfig{
				Name:      cfg.name,
				Subjects:  cfg.subjects,
				Retention: nats.LimitsPolicy,
				MaxAge:    24 * time.Hour,
			})
			if err != nil {
				return fmt.Errorf("simulator: create stream %q: %w", cfg.name, err)
			}
			s.log.Info("simulator: created stream", "name", cfg.name, "subjects", cfg.subjects)
		} else if err != nil {
			return fmt.Errorf("simulator: check stream %q: %w", cfg.name, err)
		}
	}
	return nil
}

// publish wraps payload in the standard envelope and publishes via JetStream.
func (s *Simulator) publish(subject, messageType, correlationID string, payload interface{}) error {
	env := outboundEnvelope{
		MessageID:       newMessageID(),
		MessageType:     messageType,
		SourceComponent: "orchestrator-simulator",
		CorrelationID:   correlationID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         payload,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope for %q: %w", subject, err)
	}
	if _, err := s.js.Publish(subject, data); err != nil {
		return fmt.Errorf("jetstream publish to %q: %w", subject, err)
	}
	return nil
}

// unwrap parses an inbound envelope from a raw NATS message.
// Returns nil and logs a warning if parsing fails.
func (s *Simulator) unwrap(msg *nats.Msg) *inboundEnvelope {
	var env inboundEnvelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		s.log.Warn("simulator: could not parse envelope", "subject", msg.Subject, "err", err)
		return nil
	}
	return &env
}

// newMessageID returns a UUID v4 string using crypto/rand.
func newMessageID() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
