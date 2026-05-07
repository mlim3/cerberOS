// Package contract validates all message contracts between the Agents Component
// and the Orchestrator against a live Orchestrator integration environment.
//
// These tests are SKIPPED unless AEGIS_CONTRACT_NATS_URL is set. They are NOT
// intended to run in CI against the simulator — they require the Orchestrator
// team's integration environment.
//
// Run with:
//
//	AEGIS_CONTRACT_NATS_URL=nats://orchestrator-int.example.com:4222 \
//	  go test ./test/contract/... -v -run TestContract -timeout 5m
//
// Test IDs follow the pattern ORC-{area}{nn}:
//
//	ORC-E  Envelope format
//	ORC-D  Delivery semantics
//	ORC-C  Credential (M5) contract
//	ORC-V  Vault execute contract
//	ORC-T  Task lifecycle contract
//	ORC-A  Access control
//
// Each test that corresponds to an open Orchestrator interface issue must be
// annotated with the issue reference so it can be closed when the test passes.
package contract

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/nats-io/nats.go"
)

// contractNATSURL returns the Orchestrator integration NATS URL.
// Returns "" if the env var is not set — callers must skip.
func contractNATSURL() string {
	return os.Getenv("AEGIS_CONTRACT_NATS_URL")
}

// contractComponentID returns a stable component ID for the contract test run.
// Using a fixed prefix makes it easy to filter logs on the Orchestrator side.
func contractComponentID() string {
	if id := os.Getenv("AEGIS_CONTRACT_COMPONENT_ID"); id != "" {
		return id
	}
	return fmt.Sprintf("contract-test-%d", time.Now().UnixNano())
}

// wireEnvelope is the parsed form of any message on the wire.
// Mirrors the outbound envelope emitted by internal/comms.
type wireEnvelope struct {
	MessageID       string          `json:"message_id"`
	MessageType     string          `json:"message_type"`
	SourceComponent string          `json:"source_component"`
	CorrelationID   string          `json:"correlation_id,omitempty"`
	Timestamp       string          `json:"timestamp"`
	SchemaVersion   string          `json:"schema_version"`
	Payload         json.RawMessage `json:"payload"`
}

// contractHarness holds a live NATS connection and a commsClient acting as the
// Agents Component in the Orchestrator integration environment.
type contractHarness struct {
	nc          *nats.Conn
	js          nats.JetStreamContext
	commsClient comms.Client
	componentID string
	t           *testing.T
}

// newContractHarness connects to the Orchestrator integration NATS server and
// wires a commsClient. The test is skipped when AEGIS_CONTRACT_NATS_URL is unset.
func newContractHarness(t *testing.T) *contractHarness {
	t.Helper()

	url := contractNATSURL()
	if url == "" {
		t.Skip("contract tests require AEGIS_CONTRACT_NATS_URL — set to the Orchestrator integration environment")
	}

	componentID := contractComponentID()

	// Raw connection for observations (subscribe to both sides of traffic).
	nc, err := nats.Connect(url, nats.Name("aegis-contract-observer"))
	if err != nil {
		t.Skipf("contract: NATS unavailable at %q: %v", url, err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		t.Fatalf("contract: JetStream context: %v", err)
	}

	// commsClient acts as the Agents Component — publishes to aegis.orchestrator.*
	// and subscribes to aegis.agents.*.
	commsClient, err := comms.NewNATSClient(url, componentID)
	if err != nil {
		nc.Close()
		t.Skipf("contract: comms.NewNATSClient: %v", err)
	}

	t.Cleanup(func() {
		commsClient.Close() //nolint:errcheck
		nc.Close()
	})

	return &contractHarness{
		nc:          nc,
		js:          js,
		commsClient: commsClient,
		componentID: componentID,
		t:           t,
	}
}

// observeOutbound subscribes to a single aegis.orchestrator.* subject on the
// raw NATS connection and returns a channel that receives parsed wireEnvelopes.
// Because core NATS subscriptions receive JetStream-published messages too,
// this captures everything the commsClient sends to that subject.
//
// Subscribe before the commsClient.Publish call to avoid a race.
func (h *contractHarness) observeOutbound(subject string) <-chan wireEnvelope {
	ch := make(chan wireEnvelope, 16)
	sub, err := h.nc.Subscribe(subject, func(msg *nats.Msg) {
		var env wireEnvelope
		if jsonErr := json.Unmarshal(msg.Data, &env); jsonErr == nil {
			ch <- env
		}
	})
	if err != nil {
		h.t.Fatalf("observeOutbound(%q): %v", subject, err)
	}
	h.t.Cleanup(func() { sub.Unsubscribe() }) //nolint:errcheck
	return ch
}

// observeInbound subscribes to a single aegis.agents.* subject on the raw
// NATS connection (core subscriber, DeliverNew) and returns a channel that
// receives parsed wireEnvelopes sent by the Orchestrator.
func (h *contractHarness) observeInbound(subject string) <-chan wireEnvelope {
	ch := make(chan wireEnvelope, 16)
	sub, err := h.js.Subscribe(subject, func(msg *nats.Msg) {
		_ = msg.Ack()
		var env wireEnvelope
		if jsonErr := json.Unmarshal(msg.Data, &env); jsonErr == nil {
			ch <- env
		}
	}, nats.DeliverNew())
	if err != nil {
		h.t.Fatalf("observeInbound(%q): %v", subject, err)
	}
	h.t.Cleanup(func() { sub.Unsubscribe() }) //nolint:errcheck
	return ch
}

// awaitEnvelope waits up to timeout for the next wireEnvelope on ch.
// Fails the test on timeout.
func awaitEnvelope(t *testing.T, ch <-chan wireEnvelope, desc string, timeout time.Duration) wireEnvelope {
	t.Helper()
	select {
	case env := <-ch:
		return env
	case <-time.After(timeout):
		t.Fatalf("timed out after %v waiting for %s", timeout, desc)
		return wireEnvelope{}
	}
}

// noEnvelope asserts no envelope arrives within the quiet period.
func noEnvelope(t *testing.T, ch <-chan wireEnvelope, desc string, quiet time.Duration) {
	t.Helper()
	select {
	case env := <-ch:
		t.Errorf("unexpected envelope received for %s: message_type=%q", desc, env.MessageType)
	case <-time.After(quiet):
	}
}

// uuidV4RE matches a well-formed UUID v4 string.
var uuidV4RE = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// assertUUIDV4 fails the test if s is not a valid UUID v4.
func assertUUIDV4(t *testing.T, label, s string) {
	t.Helper()
	if !uuidV4RE.MatchString(s) {
		t.Errorf("%s: want UUID v4, got %q", label, s)
	}
}

// assertISO8601 fails the test if s cannot be parsed as RFC3339/ISO 8601.
func assertISO8601(t *testing.T, label, s string) {
	t.Helper()
	if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
		if _, err2 := time.Parse(time.RFC3339, s); err2 != nil {
			t.Errorf("%s: want ISO 8601 timestamp, got %q", label, s)
		}
	}
}

// assertNoCredentialLeak fails if raw looks like a raw credential value.
// Checks for common patterns: Bearer tokens, AWS keys, PEM headers, long
// base64 strings (> 80 chars) that look like opaque secrets.
func assertNoCredentialLeak(t *testing.T, label string, raw json.RawMessage) {
	t.Helper()
	s := string(raw)
	patterns := []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{"Bearer token", regexp.MustCompile(`(?i)bearer\s+[a-z0-9\-._~+/]+=*`)},
		{"PEM header", regexp.MustCompile(`-----BEGIN [A-Z ]+-----`)},
		{"AWS access key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
		{"long base64 (>80 chars)", regexp.MustCompile(`[A-Za-z0-9+/]{80,}={0,2}`)},
	}
	for _, p := range patterns {
		if p.pattern.MatchString(s) {
			t.Errorf("%s: potential raw credential leak — matched pattern %q in payload: %.200s",
				label, p.name, s)
		}
	}
}
