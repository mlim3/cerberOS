// Tests for the partner simulator.
//
// These tests require a real NATS JetStream server. Set AEGIS_NATS_URL to
// point at one, or run the default (nats://localhost:4222).
// Tests are skipped automatically when NATS is not reachable.
//
// Quick start:
//
//	docker run --rm -p 4222:4222 nats:latest -js
//	go test ./test/integration/simulator/... -v
package simulator_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/pkg/types"
	"github.com/cerberOS/agents-component/test/integration/simulator"
	"github.com/nats-io/nats.go"
)

// natsURL returns the NATS URL from the environment or the local default.
func natsURL() string {
	if u := os.Getenv("AEGIS_NATS_URL"); u != "" {
		return u
	}
	return "nats://localhost:4222"
}

// newSim creates and starts a simulator, skipping the test if NATS is
// unreachable.
func newSim(t *testing.T) *simulator.Simulator {
	t.Helper()
	sim, err := simulator.New(natsURL())
	if err != nil {
		t.Skipf("NATS unavailable (%v) — set AEGIS_NATS_URL or run: docker run --rm -p 4222:4222 nats:latest -js", err)
	}
	if err := sim.Start(); err != nil {
		sim.Stop() //nolint:errcheck
		t.Fatalf("simulator.Start: %v", err)
	}
	t.Cleanup(func() { sim.Stop() }) //nolint:errcheck
	return sim
}

// directNATS opens a raw NATS+JetStream connection for publishing test
// stimuli and subscribing to verify responses.
func directNATS(t *testing.T) (*nats.Conn, nats.JetStreamContext) {
	t.Helper()
	nc, err := nats.Connect(natsURL())
	if err != nil {
		t.Skipf("NATS unavailable: %v", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		t.Fatalf("JetStream context: %v", err)
	}
	t.Cleanup(func() { nc.Close() })
	return nc, js
}

// envelope wraps a payload in the standard outbound envelope so the simulator
// can unwrap it — mirrors what the Agents Component sends.
func envelope(t *testing.T, messageType string, payload interface{}) []byte {
	t.Helper()
	type env struct {
		MessageID       string      `json:"message_id"`
		MessageType     string      `json:"message_type"`
		SourceComponent string      `json:"source_component"`
		Timestamp       string      `json:"timestamp"`
		SchemaVersion   string      `json:"schema_version"`
		Payload         interface{} `json:"payload"`
	}
	data, err := json.Marshal(env{
		MessageID:       "test-msg-id",
		MessageType:     messageType,
		SourceComponent: "agents",
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         payload,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return data
}

// subscribePayloads returns a channel that receives unwrapped payloads from
// subject. The subscription is set up before this function returns, so it is
// safe to publish the stimulus immediately after calling this.
func subscribePayloads(t *testing.T, js nats.JetStreamContext, subject string) <-chan json.RawMessage {
	t.Helper()

	ch := make(chan json.RawMessage, 4)
	sub, err := js.Subscribe(subject,
		func(msg *nats.Msg) {
			_ = msg.Ack()
			var env struct {
				Payload json.RawMessage `json:"payload"`
			}
			if jsonErr := json.Unmarshal(msg.Data, &env); jsonErr == nil {
				ch <- env.Payload
			}
		},
		nats.DeliverNew(),
	)
	if err != nil {
		t.Fatalf("subscribe %q: %v", subject, err)
	}
	t.Cleanup(func() { sub.Unsubscribe() }) //nolint:errcheck
	return ch
}

// awaitPayload waits for the next payload from ch within timeout, or fails.
func awaitPayload(t *testing.T, ch <-chan json.RawMessage, subject string, timeout time.Duration) json.RawMessage {
	t.Helper()
	select {
	case raw := <-ch:
		return raw
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for message on %q", subject)
		return nil
	}
}

// — Tests —————————————————————————————————————————————————————————————————

// TestCredentialResponse verifies that publishing a credential.request to
// aegis.orchestrator.credential.request produces a credential.response on
// aegis.agents.credential.response with a synthetic token.
func TestCredentialResponse(t *testing.T) {
	newSim(t)
	_, js := directNATS(t)

	req := types.CredentialRequest{
		RequestID:    "req-test-1",
		AgentID:      "agent-test-1",
		TaskID:       "task-test-1",
		Operation:    "authorize",
		SkillDomains: []string{"web"},
		TTLSeconds:   3600,
	}

	// Subscribe BEFORE publishing so we don't miss the response.
	respCh := make(chan json.RawMessage, 1)
	sub, err := js.Subscribe("aegis.agents.credential.response",
		func(msg *nats.Msg) {
			_ = msg.Ack()
			var env struct {
				Payload json.RawMessage `json:"payload"`
			}
			if jsonErr := json.Unmarshal(msg.Data, &env); jsonErr == nil {
				respCh <- env.Payload
			}
		},
		nats.DeliverNew(),
	)
	if err != nil {
		t.Fatalf("subscribe credential.response: %v", err)
	}
	defer sub.Unsubscribe() //nolint:errcheck

	if _, err := js.Publish("aegis.orchestrator.credential.request",
		envelope(t, "credential.request", req)); err != nil {
		t.Fatalf("publish credential.request: %v", err)
	}

	select {
	case raw := <-respCh:
		var resp types.CredentialResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal CredentialResponse: %v", err)
		}
		if resp.RequestID != req.RequestID {
			t.Errorf("RequestID: want %q, got %q", req.RequestID, resp.RequestID)
		}
		if resp.Status != "granted" {
			t.Errorf("Status: want %q, got %q", "granted", resp.Status)
		}
		if resp.PermissionToken == "" {
			t.Error("PermissionToken must not be empty")
		}
		t.Logf("received permission_token: %s", resp.PermissionToken)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for credential.response")
	}
}

// TestVaultExecuteResult verifies that publishing a vault.execute.request
// produces a vault.execute.result with status "success".
func TestVaultExecuteResult(t *testing.T) {
	newSim(t)
	_, js := directNATS(t)

	type vaultReq struct {
		RequestID     string `json:"request_id"`
		AgentID       string `json:"agent_id"`
		OperationType string `json:"operation_type"`
	}
	req := vaultReq{
		RequestID:     "req-vault-1",
		AgentID:       "agent-test-2",
		OperationType: "web_fetch",
	}

	// Subscribe before publishing so the response is not missed.
	ch := subscribePayloads(t, js, "aegis.agents.vault.execute.result")

	if _, err := js.Publish("aegis.orchestrator.vault.execute.request",
		envelope(t, "vault.execute.request", req)); err != nil {
		t.Fatalf("publish vault.execute.request: %v", err)
	}

	raw := awaitPayload(t, ch, "aegis.agents.vault.execute.result", 3*time.Second)

	var result struct {
		RequestID string `json:"request_id"`
		AgentID   string `json:"agent_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal vault.execute.result: %v", err)
	}
	if result.RequestID != req.RequestID {
		t.Errorf("RequestID: want %q, got %q", req.RequestID, result.RequestID)
	}
	if result.AgentID != req.AgentID {
		t.Errorf("AgentID: want %q, got %q", req.AgentID, result.AgentID)
	}
	if result.Status != "success" {
		t.Errorf("Status: want %q, got %q", "success", result.Status)
	}
}

// TestStateWriteAck verifies that publishing a state.write produces a
// state.write.ack with status "ok".
func TestStateWriteAck(t *testing.T) {
	newSim(t)
	_, js := directNATS(t)

	write := types.MemoryWrite{
		AgentID:  "agent-test-3",
		DataType: "agent_state",
		TTLHint:  3600,
		Payload:  map[string]string{"key": "value"},
	}

	ch := subscribePayloads(t, js, "aegis.agents.state.write.ack")

	if _, err := js.Publish("aegis.orchestrator.state.write",
		envelope(t, "state.write", write)); err != nil {
		t.Fatalf("publish state.write: %v", err)
	}

	raw := awaitPayload(t, ch, "aegis.agents.state.write.ack", 3*time.Second)

	var ack struct {
		AgentID string `json:"agent_id"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("unmarshal state.write.ack: %v", err)
	}
	if ack.AgentID != write.AgentID {
		t.Errorf("AgentID: want %q, got %q", write.AgentID, ack.AgentID)
	}
	if ack.Status != "ok" {
		t.Errorf("Status: want %q, got %q", "ok", ack.Status)
	}
}

// TestStateReadResponse verifies that publishing a state.read.request produces
// a state.read.response with an empty (non-nil) record list.
func TestStateReadResponse(t *testing.T) {
	newSim(t)
	_, js := directNATS(t)

	req := types.MemoryReadRequest{
		AgentID:    "agent-test-4",
		ContextTag: "result",
		TraceID:    "trace-test-4",
	}

	ch := subscribePayloads(t, js, "aegis.agents.state.read.response")

	if _, err := js.Publish("aegis.orchestrator.state.read.request",
		envelope(t, "state.read.request", req)); err != nil {
		t.Fatalf("publish state.read.request: %v", err)
	}

	raw := awaitPayload(t, ch, "aegis.agents.state.read.response", 3*time.Second)

	var resp types.MemoryResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal MemoryResponse: %v", err)
	}
	if resp.AgentID != req.AgentID {
		t.Errorf("AgentID: want %q, got %q", req.AgentID, resp.AgentID)
	}
	if resp.TraceID != req.TraceID {
		t.Errorf("TraceID: want %q, got %q", req.TraceID, resp.TraceID)
	}
	if resp.Records == nil {
		t.Error("Records must be non-nil (empty slice, not null)")
	}
}

// TestPublishTaskInbound verifies that PublishTaskInbound delivers a
// properly-enveloped task.inbound message on aegis.agents.task.inbound.
func TestPublishTaskInbound(t *testing.T) {
	sim := newSim(t)
	_, js := directNATS(t)

	spec := types.TaskSpec{
		TaskID:         "task-sim-test-1",
		RequiredSkills: []string{"web"},
		Instructions:   "fetch https://example.com",
		TraceID:        "trace-sim-1",
	}

	// Subscribe before publishing.
	msgCh := make(chan *nats.Msg, 1)
	sub, err := js.Subscribe("aegis.agents.task.inbound",
		func(msg *nats.Msg) { _ = msg.Ack(); msgCh <- msg },
		nats.DeliverNew(),
	)
	if err != nil {
		t.Fatalf("subscribe task.inbound: %v", err)
	}
	defer sub.Unsubscribe() //nolint:errcheck

	if err := sim.PublishTaskInbound(spec); err != nil {
		t.Fatalf("PublishTaskInbound: %v", err)
	}

	select {
	case msg := <-msgCh:
		var env struct {
			MessageType string          `json:"message_type"`
			Payload     json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if env.MessageType != "task.inbound" {
			t.Errorf("MessageType: want %q, got %q", "task.inbound", env.MessageType)
		}
		var received types.TaskSpec
		if err := json.Unmarshal(env.Payload, &received); err != nil {
			t.Fatalf("unmarshal TaskSpec: %v", err)
		}
		if received.TaskID != spec.TaskID {
			t.Errorf("TaskID: want %q, got %q", spec.TaskID, received.TaskID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for task.inbound")
	}
}
