// Package comms is M1 — the single Communications Interface for all inter-component
// messaging. No other module may call external components directly; all messages
// flow through this package.
//
// Only this package may import github.com/nats-io/nats.go. All other internal
// modules communicate exclusively through the Client interface.
package comms

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/cerberOS/agents-component/pkg/types"
)

// PublishOptions carries per-message metadata included in the outbound envelope.
type PublishOptions struct {
	// MessageType is the dot-notation event type, e.g. "task.result".
	// Required on every outbound publication.
	MessageType string

	// CorrelationID links related messages. Set to task_id, request_id, or
	// query_id as appropriate. For vault.execute.request it MUST be the request_id.
	CorrelationID string

	// TraceID propagates the W3C trace_id across the NATS envelope so distributed
	// trace continuity is preserved. Callers SHOULD set this to the trace_id of
	// the inbound message they are reacting to (msg.TraceID), or — for messages
	// that originate a new task — the trace_id assigned by the originating
	// component. When empty, downstream consumers will mint a fresh trace_id.
	TraceID string

	// Transient uses at-most-once core NATS publish instead of JetStream.
	// Use only for explicitly at-most-once subjects (e.g. capability.response).
	Transient bool
}

// outboundEnvelope is the wire format wrapping every outbound publication.
// Callers never build this directly — the Client wraps it automatically.
type outboundEnvelope struct {
	MessageID       string      `json:"message_id"`
	MessageType     string      `json:"message_type"`
	SourceComponent string      `json:"source_component"`
	CorrelationID   string      `json:"correlation_id,omitempty"`
	TraceID         string      `json:"trace_id,omitempty"` // W3C trace_id for distributed trace continuity
	Timestamp       string      `json:"timestamp"`          // ISO 8601
	SchemaVersion   string      `json:"schema_version"`
	Payload         interface{} `json:"payload"`
}

// inboundEnvelope is used to unwrap messages received from the Orchestrator.
type inboundEnvelope struct {
	MessageID     string          `json:"message_id"`
	MessageType   string          `json:"message_type"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	TraceID       string          `json:"trace_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// Message is a received message with the envelope already unwrapped.
// Data is the JSON-encoded inner payload — unmarshal it to the expected type.
type Message struct {
	Subject       string
	MessageType   string
	CorrelationID string
	TraceID       string
	Data          []byte

	// Ack acknowledges successful processing (JetStream at-least-once subjects).
	// Handlers on JetStream subjects MUST call Ack on success. Failure to
	// acknowledge causes redelivery after the AckWait timeout.
	// No-op for core NATS at-most-once subjects.
	Ack func() error

	// Nak signals failed processing; JetStream will redeliver the message.
	// No-op for core NATS at-most-once subjects.
	Nak func() error
}

// MessageHandler is the callback invoked for each received message.
type MessageHandler func(msg *Message)

// Client is the interface for all inter-component communication.
// No module other than internal/comms may implement or depend on nats.go.
type Client interface {
	// Publish wraps payload in the standard message envelope and sends to subject.
	// Use opts.Transient=true only for explicitly at-most-once outbound subjects.
	Publish(subject string, opts PublishOptions, payload interface{}) error

	// Subscribe registers a handler for at-most-once delivery (core NATS).
	// Use for transient inbound subjects: aegis.agents.capability.query,
	// aegis.agents.vault.execute.progress.
	Subscribe(subject string, handler MessageHandler) error

	// SubscribeDurable registers a durable push consumer for at-least-once
	// delivery (JetStream). durable is the consumer name — must be stable
	// across restarts and unique per subject within this component.
	// Use for all other inbound subjects.
	SubscribeDurable(subject, durable string, handler MessageHandler) error

	// EnsureStreams creates the two JetStream streams required by this component
	// (AEGIS_AGENTS covering aegis.agents.> and AEGIS_ORCHESTRATOR covering
	// aegis.orchestrator.>) if they do not already exist. Idempotent: safe to
	// call on every startup. In a full deployment the Orchestrator provisions
	// these; call this method for standalone / dev / CI environments where no
	// Orchestrator is present.
	EnsureStreams() error

	// Close drains all subscriptions and the underlying connection.
	Close() error
}

// — NATS JetStream client —————————————————————————————————————————————————

// defaultMaxDeliver is the redelivery budget applied to every durable consumer
// when no WithMaxDeliver option is provided. After this many delivery attempts
// the message is dead-lettered and aegis.orchestrator.error is published.
const defaultMaxDeliver = 5

// ClientOption configures a natsClient at construction time.
type ClientOption func(*natsClient)

// WithMaxDeliver sets the maximum number of JetStream redelivery attempts for
// all durable consumers created by SubscribeDurable. When the budget is
// exhausted and the handler still calls Nak, the message is dead-lettered:
// the full original envelope is published to aegis.orchestrator.error
// (MessageType "dead.letter") and the message is terminally acknowledged
// so JetStream never redelivers it again.
//
// n must be >= 1. Zero or negative values are treated as the default (5).
func WithMaxDeliver(n int) ClientOption {
	return func(c *natsClient) {
		if n >= 1 {
			c.maxDeliver = n
		}
	}
}

// natsClient is the production NATS JetStream implementation.
type natsClient struct {
	nc          *nats.Conn
	js          nats.JetStreamContext
	componentID string
	maxDeliver  int // redelivery budget per durable consumer; default defaultMaxDeliver
	mu          sync.Mutex
	subs        []*nats.Subscription
}

// NewNATSClient connects to NATS at natsURL with automatic reconnect using
// exponential backoff, creates a JetStream context, and returns a Client.
// Pass WithMaxDeliver to override the default redelivery budget (5).
func NewNATSClient(natsURL, componentID string, opts ...ClientOption) (Client, error) {
	natsOpts := []nats.Option{
		nats.Name("aegis-" + componentID),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1), // reconnect indefinitely
		nats.ReconnectWait(500 * time.Millisecond),
		nats.CustomReconnectDelay(func(attempts int) time.Duration {
			// Exponential backoff: 500ms × 2^(attempts-1), capped at 30s.
			delay := time.Duration(math.Pow(2, float64(attempts-1))) * 500 * time.Millisecond
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			return delay
		}),
	}

	nc, err := nats.Connect(natsURL, natsOpts...)
	if err != nil {
		return nil, fmt.Errorf("comms: connect to NATS at %q: %w", natsURL, err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("comms: create JetStream context: %w", err)
	}

	c := &natsClient{
		nc:          nc,
		js:          js,
		componentID: componentID,
		maxDeliver:  defaultMaxDeliver,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

func (c *natsClient) Publish(subject string, opts PublishOptions, payload interface{}) error {
	if opts.MessageType == "" {
		return fmt.Errorf("comms: Publish to %q requires a non-empty MessageType", subject)
	}
	env := outboundEnvelope{
		MessageID:       newMessageID(),
		MessageType:     opts.MessageType,
		SourceComponent: c.componentID,
		CorrelationID:   opts.CorrelationID,
		TraceID:         opts.TraceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         payload,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("comms: marshal envelope for %q: %w", subject, err)
	}

	if opts.Transient {
		if err := c.nc.Publish(subject, data); err != nil {
			return fmt.Errorf("comms: core publish to %q: %w", subject, err)
		}
		return nil
	}

	if _, err := c.js.Publish(subject, data); err != nil {
		return fmt.Errorf("comms: jetstream publish to %q: %w", subject, err)
	}
	return nil
}

func (c *natsClient) Subscribe(subject string, handler MessageHandler) error {
	if handler == nil {
		return fmt.Errorf("comms: nil handler for subject %q", subject)
	}
	sub, err := c.nc.Subscribe(subject, func(natsMsg *nats.Msg) {
		msg := unwrapOrRaw(natsMsg.Data)
		msg.Subject = natsMsg.Subject
		msg.Ack = noop
		msg.Nak = noop
		handler(msg)
	})
	if err != nil {
		return fmt.Errorf("comms: subscribe %q: %w", subject, err)
	}
	c.mu.Lock()
	c.subs = append(c.subs, sub)
	c.mu.Unlock()
	return nil
}

func (c *natsClient) SubscribeDurable(subject, durable string, handler MessageHandler) error {
	if handler == nil {
		return fmt.Errorf("comms: nil handler for subject %q", subject)
	}

	msgHandler := func(natsMsg *nats.Msg) {
		rawData := natsMsg.Data
		msg := unwrapOrRaw(rawData)
		msg.Subject = natsMsg.Subject
		msg.Ack = func() error { return natsMsg.Ack() }
		msg.Nak = func() error { return natsMsg.Nak() }

		// Dead-letter intercept: when this is the last permitted delivery and
		// the handler signals failure (Nak), publish the original envelope to
		// aegis.orchestrator.error and terminally ack so JetStream never
		// redelivers it. The handler still runs normally — we only replace
		// what happens if it chooses to Nak.
		if meta, err := natsMsg.Metadata(); err == nil &&
			int(meta.NumDelivered) >= c.maxDeliver {
			correlationID := msg.CorrelationID
			msgType := msg.MessageType
			msg.Nak = func() error {
				c.publishDeadLetter(subject, durable, rawData,
					int(meta.NumDelivered), msgType, correlationID)
				return natsMsg.Term() // terminal ack — prevents any further redelivery
			}
		}

		handler(msg)
	}

	// Retry binding the durable consumer with backoff. During a rolling update the
	// old pod holds the push-consumer binding until its NATS connection closes.
	// Rather than crashing, we wait for the old pod to drain (typically <30s).
	const maxBindAttempts = 20
	backoff := 2 * time.Second
	var sub *nats.Subscription
	var err error
	for attempt := 1; attempt <= maxBindAttempts; attempt++ {
		sub, err = c.js.Subscribe(
			subject,
			msgHandler,
			nats.Durable(durable),
			nats.DeliverNew(),
			nats.AckExplicit(),
			nats.MaxDeliver(c.maxDeliver),
		)
		if err == nil {
			break
		}
		if attempt == maxBindAttempts {
			break
		}
		slog.Warn("comms: durable consumer already bound, retrying",
			"subject", subject, "consumer", durable,
			"attempt", attempt, "backoff", backoff)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	if err != nil {
		return fmt.Errorf("comms: durable subscribe %q (consumer %q): %w", subject, durable, err)
	}
	c.mu.Lock()
	c.subs = append(c.subs, sub)
	c.mu.Unlock()
	return nil
}

// publishDeadLetter publishes a DeadLetterEvent to aegis.orchestrator.error after
// an inbound message exhausts its redelivery budget. The full original wire-format
// envelope is embedded so the Orchestrator can correlate the failure to a task
// and replay the message if needed.
//
// Failures are logged as warnings and silently dropped — dead-letter publishing
// is best-effort. We must not Nak at this point (Term has been called) and must
// not panic or block the NATS subscription goroutine.
func (c *natsClient) publishDeadLetter(
	subject, consumer string,
	rawEnvelope []byte,
	numDelivered int,
	msgType, correlationID string,
) {
	event := types.DeadLetterEvent{
		OriginalSubject:  subject,
		ConsumerName:     consumer,
		MessageType:      msgType,
		CorrelationID:    correlationID,
		OriginalEnvelope: json.RawMessage(rawEnvelope),
		DeliveryAttempts: numDelivered,
		FailureReason:    "max_redelivery_exceeded",
		DeadLetteredAt:   time.Now().UTC(),
	}
	slog.Warn("comms: dead-lettering message after max redelivery",
		"subject", subject,
		"consumer", consumer,
		"delivery_attempts", numDelivered,
		"correlation_id", correlationID,
		"message_type", msgType,
	)
	if err := c.Publish(SubjectError, PublishOptions{
		MessageType:   MsgTypeDeadLetter,
		CorrelationID: correlationID,
	}, event); err != nil {
		slog.Warn("comms: dead-letter publish failed — message lost",
			"subject", subject,
			"consumer", consumer,
			"correlation_id", correlationID,
			"error", err,
		)
	}
}

// EnsureStreams creates the AEGIS_AGENTS and AEGIS_ORCHESTRATOR JetStream
// streams if they do not already exist. Each stream uses file storage and
// limit-based retention. The call is idempotent: if a stream already exists
// with any configuration it is left unchanged.
func (c *natsClient) EnsureStreams() error {
	streams := []struct {
		name    string
		subject string
	}{
		{"AEGIS_AGENTS", "aegis.agents.>"},
		{"AEGIS_ORCHESTRATOR", "aegis.orchestrator.>"},
	}
	for _, s := range streams {
		if _, err := c.js.StreamInfo(s.name); err == nil {
			continue // stream already exists
		}
		if _, err := c.js.AddStream(&nats.StreamConfig{
			Name:      s.name,
			Subjects:  []string{s.subject},
			Storage:   nats.FileStorage,
			Retention: nats.LimitsPolicy,
		}); err != nil {
			return fmt.Errorf("comms: create stream %q: %w", s.name, err)
		}
	}
	return nil
}

func (c *natsClient) Close() error {
	c.mu.Lock()
	for _, sub := range c.subs {
		_ = sub.Unsubscribe()
	}
	c.subs = nil
	c.mu.Unlock()
	c.nc.Close()
	return nil
}

// — Stub client (in-process, for tests) ——————————————————————————————————

// stubClient is an in-process implementation used in unit tests.
// SubscribeDurable behaves identically to Subscribe — both deliver synchronously
// inline with Publish, which is sufficient for unit testing.
type stubClient struct {
	mu          sync.RWMutex
	subscribers map[string][]MessageHandler
}

// NewStubClient returns a Client backed by in-process synchronous delivery.
// Use in unit tests only.
func NewStubClient() Client {
	return &stubClient{
		subscribers: make(map[string][]MessageHandler),
	}
}

func (c *stubClient) Publish(subject string, opts PublishOptions, payload interface{}) error {
	if opts.MessageType == "" {
		return fmt.Errorf("comms: Publish to %q requires a non-empty MessageType", subject)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("comms: marshal payload for %q: %w", subject, err)
	}

	c.mu.RLock()
	handlers := make([]MessageHandler, len(c.subscribers[subject]))
	copy(handlers, c.subscribers[subject])
	c.mu.RUnlock()

	msg := &Message{
		Subject:       subject,
		MessageType:   opts.MessageType,
		CorrelationID: opts.CorrelationID,
		TraceID:       opts.TraceID,
		Data:          data,
		Ack:           noop,
		Nak:           noop,
	}
	for _, h := range handlers {
		h(msg)
	}
	return nil
}

func (c *stubClient) Subscribe(subject string, handler MessageHandler) error {
	if handler == nil {
		return fmt.Errorf("comms: nil handler for subject %q", subject)
	}
	c.mu.Lock()
	c.subscribers[subject] = append(c.subscribers[subject], handler)
	c.mu.Unlock()
	return nil
}

func (c *stubClient) SubscribeDurable(subject, _ string, handler MessageHandler) error {
	return c.Subscribe(subject, handler)
}

func (c *stubClient) EnsureStreams() error { return nil }

func (c *stubClient) Close() error {
	c.mu.Lock()
	c.subscribers = make(map[string][]MessageHandler)
	c.mu.Unlock()
	return nil
}

// — Helpers ————————————————————————————————————————————————————————————————

// unwrapOrRaw attempts to unwrap an inbound message envelope.
// If the data is not a valid envelope, the raw bytes are returned as Data
// so handlers can still inspect the message.
func unwrapOrRaw(data []byte) *Message {
	var env inboundEnvelope
	if err := json.Unmarshal(data, &env); err != nil || env.Payload == nil {
		return &Message{Data: data}
	}
	return &Message{
		MessageType:   env.MessageType,
		CorrelationID: env.CorrelationID,
		TraceID:       env.TraceID,
		Data:          []byte(env.Payload),
	}
}

// newMessageID returns a UUID v4 string using crypto/rand.
func newMessageID() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// noop is the no-op Ack/Nak for core NATS and stub subjects.
var noop = func() error { return nil }
