package integration

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/pkg/types"
	"github.com/nats-io/nats.go"
)

const testOwnerMetadataKey = "integration_test_owner"

type inboundEnvelope struct {
	MessageID       string          `json:"message_id"`
	MessageType     string          `json:"message_type"`
	SourceComponent string          `json:"source_component"`
	CorrelationID   string          `json:"correlation_id,omitempty"`
	TraceID         string          `json:"trace_id,omitempty"`
	Payload         json.RawMessage `json:"payload"`
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

// partnerFixture provides only the partner-side behaviors needed by the
// integration package's NATS tests.
type partnerFixture struct {
	t      *testing.T
	nc     *nats.Conn
	js     nats.JetStreamContext
	owner  string
	mu     sync.Mutex
	subs   []*nats.Subscription
	credMu sync.RWMutex
	creds  map[string]string

	// onClarificationRequest is an optional hook called when the fixture
	// receives a clarification.request from the agent. The hook must return
	// the response to send back. If nil, clarification requests are ack'd
	// and silently dropped (simulates no user present).
	onClarificationRequest func(types.ClarificationRequest) types.ClarificationResponse
}

func newPartnerFixture(t *testing.T, natsURL, owner string) *partnerFixture {
	t.Helper()

	nc, err := nats.Connect(natsURL, nats.Name("aegis-partner-fixture-"+owner))
	if err != nil {
		t.Fatalf("partner fixture connect: %v", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		t.Fatalf("partner fixture jetstream: %v", err)
	}

	f := &partnerFixture{
		t:     t,
		nc:    nc,
		js:    js,
		owner: owner,
		creds: make(map[string]string),
	}
	if err := f.ensureStreams(); err != nil {
		nc.Close()
		t.Fatalf("partner fixture ensureStreams: %v", err)
	}
	if err := f.start(); err != nil {
		nc.Close()
		t.Fatalf("partner fixture start: %v", err)
	}

	t.Cleanup(func() {
		_ = f.stop()
	})
	return f
}

func (f *partnerFixture) start() error {
	type subscription struct {
		subject string
		base    string
		handler nats.MsgHandler
	}

	subs := []subscription{
		{subject: "aegis.orchestrator.credential.request", base: "itest-credential-request", handler: f.handleCredentialRequest},
		{subject: "aegis.orchestrator.vault.execute.request", base: "itest-vault-execute-request", handler: f.handleVaultExecuteRequest},
		{subject: "aegis.orchestrator.state.write", base: "itest-state-write", handler: f.handleStateWrite},
		{subject: "aegis.orchestrator.state.read.request", base: "itest-state-read-request", handler: f.handleStateReadRequest},
		{subject: "aegis.orchestrator.clarification.request", base: "itest-clarification-request", handler: f.handleClarificationRequest},
	}

	for _, sub := range subs {
		natsSub, err := f.js.Subscribe(
			sub.subject,
			sub.handler,
			nats.Durable(sub.base+"-"+f.owner),
			nats.DeliverNew(),
			nats.AckExplicit(),
		)
		if err != nil {
			return fmt.Errorf("subscribe %q: %w", sub.subject, err)
		}
		f.mu.Lock()
		f.subs = append(f.subs, natsSub)
		f.mu.Unlock()
	}

	return nil
}

func (f *partnerFixture) stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subs {
		_ = sub.Unsubscribe()
	}
	f.subs = nil
	f.nc.Close()
	return nil
}

func (f *partnerFixture) publishTaskInbound(spec types.TaskSpec) error {
	if spec.Metadata == nil {
		spec.Metadata = map[string]string{}
	}
	spec.Metadata[testOwnerMetadataKey] = f.owner
	return f.publish("aegis.agents.task.inbound", "task.inbound", spec.TaskID, spec)
}

func (f *partnerFixture) credentialTokens() map[string]string {
	f.credMu.RLock()
	defer f.credMu.RUnlock()
	out := make(map[string]string, len(f.creds))
	for k, v := range f.creds {
		out[k] = v
	}
	return out
}

func (f *partnerFixture) handleCredentialRequest(msg *nats.Msg) {
	env, ok := unwrapEnvelope(msg.Data)
	if !ok {
		_ = msg.Ack()
		return
	}
	if env.SourceComponent != f.owner {
		_ = msg.Ack()
		return
	}

	var req types.CredentialRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		_ = msg.Nak()
		return
	}

	if req.Operation == "revoke" {
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

	f.credMu.Lock()
	f.creds[req.AgentID] = token
	f.credMu.Unlock()

	if err := f.publish("aegis.agents.credential.response", "credential.response", req.RequestID, resp); err != nil {
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func (f *partnerFixture) handleVaultExecuteRequest(msg *nats.Msg) {
	env, ok := unwrapEnvelope(msg.Data)
	if !ok {
		_ = msg.Ack()
		return
	}
	if env.SourceComponent != f.owner {
		_ = msg.Ack()
		return
	}

	var req types.VaultOperationRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		_ = msg.Nak()
		return
	}

	resp := types.VaultOperationResult{
		RequestID:       req.RequestID,
		AgentID:         req.AgentID,
		Status:          "success",
		OperationResult: json.RawMessage(`{"simulated":true,"operation_type":"` + req.OperationType + `"}`),
		ElapsedMS:       1,
	}
	if err := f.publish("aegis.agents.vault.execute.result", "vault.execute.result", req.RequestID, resp); err != nil {
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func (f *partnerFixture) handleStateWrite(msg *nats.Msg) {
	env, ok := unwrapEnvelope(msg.Data)
	if !ok {
		_ = msg.Ack()
		return
	}
	if env.SourceComponent != f.owner {
		_ = msg.Ack()
		return
	}

	var write types.MemoryWrite
	if err := json.Unmarshal(env.Payload, &write); err != nil {
		_ = msg.Nak()
		return
	}

	correlationID := env.CorrelationID
	if correlationID == "" {
		correlationID = write.RequestID
	}
	if correlationID == "" {
		correlationID = write.AgentID
	}

	resp := types.StateWriteAck{
		RequestID: write.RequestID,
		AgentID:   write.AgentID,
		Status:    "accepted",
	}
	if err := f.publish("aegis.agents.state.write.ack", "state.write.ack", correlationID, resp); err != nil {
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func (f *partnerFixture) handleStateReadRequest(msg *nats.Msg) {
	env, ok := unwrapEnvelope(msg.Data)
	if !ok {
		_ = msg.Ack()
		return
	}
	if env.SourceComponent != f.owner {
		_ = msg.Ack()
		return
	}

	var req types.MemoryReadRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		_ = msg.Nak()
		return
	}

	switch req.DataType {
	case "system_log", "skill_cache":
		_ = msg.Ack()
		return
	}

	resp := types.MemoryResponse{
		AgentID: req.AgentID,
		Records: []types.MemoryWrite{},
		TraceID: req.TraceID,
	}
	if err := f.publish("aegis.agents.state.read.response", "state.read.response", req.TraceID, resp); err != nil {
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func (f *partnerFixture) handleClarificationRequest(msg *nats.Msg) {
	env, ok := unwrapEnvelope(msg.Data)
	if !ok {
		_ = msg.Ack()
		return
	}
	if env.SourceComponent != f.owner {
		_ = msg.Ack()
		return
	}

	var req types.ClarificationRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		_ = msg.Nak()
		return
	}

	f.mu.Lock()
	hook := f.onClarificationRequest
	f.mu.Unlock()

	if hook == nil {
		// No handler registered — silently drop (simulates no user present).
		_ = msg.Ack()
		return
	}

	resp := hook(req)
	if err := f.publish("aegis.agents.clarification.response", "clarification.response", req.RequestID, resp); err != nil {
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func (f *partnerFixture) ensureStreams() error {
	streams := []struct {
		name     string
		subjects []string
	}{
		{name: "AEGIS_ORCHESTRATOR", subjects: []string{"aegis.orchestrator.>"}},
		{name: "AEGIS_AGENTS", subjects: []string{"aegis.agents.>"}},
	}

	for _, stream := range streams {
		_, err := f.js.StreamInfo(stream.name)
		if err == nats.ErrStreamNotFound {
			_, err = f.js.AddStream(&nats.StreamConfig{
				Name:      stream.name,
				Subjects:  stream.subjects,
				Retention: nats.LimitsPolicy,
				MaxAge:    24 * time.Hour,
			})
			if err != nil {
				return fmt.Errorf("create stream %q: %w", stream.name, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("check stream %q: %w", stream.name, err)
		}
	}

	return nil
}

func (f *partnerFixture) publish(subject, messageType, correlationID string, payload interface{}) error {
	env := outboundEnvelope{
		MessageID:       newFixtureMessageID(),
		MessageType:     messageType,
		SourceComponent: "orchestrator-test-fixture",
		CorrelationID:   correlationID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         payload,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal %q envelope: %w", subject, err)
	}
	if _, err := f.js.Publish(subject, data); err != nil {
		return fmt.Errorf("publish %q: %w", subject, err)
	}
	return nil
}

func unwrapEnvelope(data []byte) (*inboundEnvelope, bool) {
	var env inboundEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, false
	}
	return &env, true
}

func newFixtureMessageID() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
