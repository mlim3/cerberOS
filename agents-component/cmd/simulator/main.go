// cmd/simulator is a standalone development binary that simulates the Orchestrator
// and Vault for local Docker Compose environments where no real partner services
// are present.
//
// It subscribes to all aegis.orchestrator.* subjects and publishes synthetic
// responses to aegis.agents.* so the Agents Component can complete full task
// flows end-to-end:
//
//   - credential.request   → credential.response  (status: granted)
//   - vault.execute.request → vault.execute.result (status: success, empty result)
//   - state.write          → state.write.ack
//   - state.read.request   → state.read.response  (empty records)
//
// All other outbound subjects (task.accepted, task.result, task.failed,
// agent.status, audit.event, error) are observed and logged only.
//
// NOT for production use.
//
// Required environment:
//
//	AEGIS_NATS_URL — NATS server address (default: nats://localhost:4222)
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cerberOS/agents-component/pkg/types"
	nats "github.com/nats-io/nats.go"
)

// ── stream / subject constants (mirrors internal/comms) ──────────────────────

const (
	streamOrchestrator = "AEGIS_ORCHESTRATOR"
	streamAgents       = "AEGIS_AGENTS"

	subjectCredentialRequest   = "aegis.orchestrator.credential.request"
	subjectCredentialResponse  = "aegis.agents.credential.response"
	subjectVaultExecuteRequest = "aegis.orchestrator.vault.execute.request"
	subjectVaultExecuteResult  = "aegis.agents.vault.execute.result"
	subjectStateWrite          = "aegis.orchestrator.state.write"
	subjectStateWriteAck       = "aegis.agents.state.write.ack"
	subjectStateReadRequest    = "aegis.orchestrator.state.read.request"
	subjectStateReadResponse   = "aegis.agents.state.read.response"

	consumerCredentialRequest   = "sim-credential-request"
	consumerVaultExecuteRequest = "sim-vault-execute-request"
	consumerStateWrite          = "sim-state-write"
	consumerStateReadRequest    = "sim-state-read-request"
)

// ── wire envelope types ───────────────────────────────────────────────────────

type inboundEnvelope struct {
	MessageID     string          `json:"message_id"`
	MessageType   string          `json:"message_type"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type outboundEnvelope struct {
	MessageID       string      `json:"message_id"`
	MessageType     string      `json:"message_type"`
	SourceComponent string      `json:"source_component"`
	CorrelationID   string      `json:"correlation_id,omitempty"`
	Timestamp       string      `json:"timestamp"`
	SchemaVersion   string      `json:"schema_version"`
	Payload         interface{} `json:"payload"`
}

// vaultExecuteRequest mirrors the vault.execute.request payload (ADR-004).
type vaultExecuteRequest struct {
	RequestID       string          `json:"request_id"`
	AgentID         string          `json:"agent_id"`
	TaskID          string          `json:"task_id"`
	OperationType   string          `json:"operation_type"`
	OperationParams json.RawMessage `json:"operation_params"`
}

// ── simulator ─────────────────────────────────────────────────────────────────

type simulator struct {
	nc   *nats.Conn
	js   nats.JetStreamContext
	log  *slog.Logger
	subs []*nats.Subscription
}

func newSimulator(natsURL string, log *slog.Logger) (*simulator, error) {
	nc, err := nats.Connect(natsURL, nats.Name("aegis-partner-simulator"))
	if err != nil {
		return nil, fmt.Errorf("simulator: connect to %q: %w", natsURL, err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("simulator: jetstream: %w", err)
	}
	return &simulator{nc: nc, js: js, log: log}, nil
}

func (s *simulator) start() error {
	if err := s.ensureStreams(); err != nil {
		return err
	}

	type sub struct {
		subject string
		durable string
		handler nats.MsgHandler
	}

	subs := []sub{
		{subjectCredentialRequest, consumerCredentialRequest, s.handleCredentialRequest},
		{subjectStateWrite, consumerStateWrite, s.handleStateWrite},
		{subjectStateReadRequest, consumerStateReadRequest, s.handleStateReadRequest},
	}

	// When SIMULATOR_SKIP_VAULT_EXECUTE=true a real vault engine is handling
	// vault.execute.request — skip the mock handler to avoid a race where the
	// simulator's empty {} result wins over the real vault's response.
	if os.Getenv("SIMULATOR_SKIP_VAULT_EXECUTE") != "true" {
		subs = append(subs, sub{subjectVaultExecuteRequest, consumerVaultExecuteRequest, s.handleVaultExecuteRequest})
	} else {
		s.log.Info("simulator: vault execute simulation disabled (SIMULATOR_SKIP_VAULT_EXECUTE=true)")
	}

	for _, entry := range subs {
		sub, err := s.js.Subscribe(entry.subject, entry.handler,
			nats.Durable(entry.durable),
			nats.DeliverNew(),
			nats.AckExplicit(),
		)
		if err != nil {
			return fmt.Errorf("simulator: subscribe %q: %w", entry.subject, err)
		}
		s.subs = append(s.subs, sub)
	}

	s.log.Info("simulator ready", "subscriptions", len(s.subs))
	return nil
}

func (s *simulator) stop() {
	for _, sub := range s.subs {
		_ = sub.Unsubscribe()
	}
	s.nc.Close()
}

func (s *simulator) ensureStreams() error {
	for _, cfg := range []*nats.StreamConfig{
		{Name: streamOrchestrator, Subjects: []string{"aegis.orchestrator.>"}, Storage: nats.FileStorage},
		{Name: streamAgents, Subjects: []string{"aegis.agents.>"}, Storage: nats.FileStorage},
	} {
		if _, err := s.js.StreamInfo(cfg.Name); err == nil {
			continue
		}
		if _, err := s.js.AddStream(cfg); err != nil {
			return fmt.Errorf("simulator: create stream %q: %w", cfg.Name, err)
		}
		s.log.Info("simulator: created stream", "name", cfg.Name, "subjects", cfg.Subjects)
	}
	return nil
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (s *simulator) handleCredentialRequest(msg *nats.Msg) {
	env := s.unwrap(msg)
	if env == nil {
		_ = msg.Nak()
		return
	}

	var req types.CredentialRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		s.log.Error("simulator: unmarshal credential.request", "error", err)
		_ = msg.Nak()
		return
	}

	if req.Operation == "revoke" {
		s.log.Info("simulator: credential.revoke acknowledged", "agent_id", req.AgentID)
		_ = msg.Ack()
		return
	}

	token := "sim-token-" + req.AgentID
	resp := types.CredentialResponse{
		RequestID:       req.RequestID,
		Status:          "granted",
		PermissionToken: token,
		ExpiresAt:       time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if err := s.publish(subjectCredentialResponse, "credential.response", req.RequestID, resp); err != nil {
		s.log.Error("simulator: publish credential.response", "error", err, "agent_id", req.AgentID)
		_ = msg.Nak()
		return
	}
	s.log.Info("simulator: credential.response sent", "agent_id", req.AgentID, "request_id", req.RequestID)
	_ = msg.Ack()
}

func (s *simulator) handleVaultExecuteRequest(msg *nats.Msg) {
	env := s.unwrap(msg)
	if env == nil {
		_ = msg.Nak()
		return
	}

	var req vaultExecuteRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		s.log.Error("simulator: unmarshal vault.execute.request", "error", err)
		_ = msg.Nak()
		return
	}

	result := types.VaultOperationResult{
		RequestID:       req.RequestID,
		AgentID:         req.AgentID,
		Status:          "success",
		OperationResult: json.RawMessage(`{}`),
		ElapsedMS:       1,
	}
	if err := s.publish(subjectVaultExecuteResult, "vault.execute.result", req.RequestID, result); err != nil {
		s.log.Error("simulator: publish vault.execute.result", "error", err, "request_id", req.RequestID)
		_ = msg.Nak()
		return
	}
	s.log.Info("simulator: vault.execute.result sent", "request_id", req.RequestID, "operation_type", req.OperationType)
	_ = msg.Ack()
}

func (s *simulator) handleStateWrite(msg *nats.Msg) {
	env := s.unwrap(msg)
	if env == nil {
		_ = msg.Ack()
		return
	}
	var write types.MemoryWrite
	if err := json.Unmarshal(env.Payload, &write); err != nil {
		s.log.Error("simulator: unmarshal state.write", "error", err)
		_ = msg.Nak()
		return
	}
	correlationID := env.CorrelationID
	if correlationID == "" {
		correlationID = write.RequestID
	}
	ack := types.StateWriteAck{
		RequestID: write.RequestID,
		AgentID:   write.AgentID,
		Status:    "accepted",
	}
	if err := s.publish(subjectStateWriteAck, "state.write.ack", correlationID, ack); err != nil {
		s.log.Error("simulator: publish state.write.ack", "error", err)
		_ = msg.Nak()
		return
	}
	s.log.Info("simulator: state.write.ack sent", "agent_id", write.AgentID, "data_type", write.DataType)
	_ = msg.Ack()
}

func (s *simulator) handleStateReadRequest(msg *nats.Msg) {
	env := s.unwrap(msg)
	if env == nil {
		_ = msg.Ack()
		return
	}
	var req types.MemoryReadRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		s.log.Error("simulator: unmarshal state.read.request", "error", err)
		_ = msg.Nak()
		return
	}
	resp := types.MemoryResponse{
		AgentID: req.AgentID,
		Records: []types.MemoryWrite{},
		TraceID: req.TraceID,
	}
	if err := s.publish(subjectStateReadResponse, "state.read.response", req.AgentID, resp); err != nil {
		s.log.Error("simulator: publish state.read.response", "error", err)
		_ = msg.Nak()
		return
	}
	s.log.Info("simulator: state.read.response sent", "agent_id", req.AgentID)
	_ = msg.Ack()
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (s *simulator) publish(subject, messageType, correlationID string, payload interface{}) error {
	env := outboundEnvelope{
		MessageID:       newID(),
		MessageType:     messageType,
		SourceComponent: "simulator",
		CorrelationID:   correlationID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         payload,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = s.js.Publish(subject, data)
	return err
}

func (s *simulator) unwrap(msg *nats.Msg) *inboundEnvelope {
	var env inboundEnvelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		s.log.Warn("simulator: unmarshal envelope failed", "error", err)
		return nil
	}
	return &env
}

func newID() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	natsURL := os.Getenv("AEGIS_NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	log.Info("starting partner simulator", "nats_url", natsURL)

	sim, err := newSimulator(natsURL, log)
	if err != nil {
		log.Error("simulator init failed", "error", err)
		os.Exit(1)
	}

	if err := sim.start(); err != nil {
		log.Error("simulator start failed", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()

	log.Info("simulator stopping")
	sim.stop()
}
