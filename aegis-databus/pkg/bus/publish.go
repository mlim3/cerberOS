package bus

import (
	"aegis-databus/internal/metrics"
	"aegis-databus/pkg/envelope"
	"aegis-databus/pkg/security"
	"github.com/nats-io/nats.go"
)

// PublishValidated publishes to JetStream only if payload passes CloudEvents validation.
// Returns error if validation fails or publish fails.
func PublishValidated(js nats.JetStreamContext, subject string, payload []byte) (*nats.PubAck, error) {
	if err := envelope.Validate(payload); err != nil {
		metrics.ValidationErrors.Inc()
		return nil, err
	}
	ack, err := js.Publish(subject, payload)
	if err != nil {
		metrics.PublishErrors.Inc()
		return nil, err
	}
	metrics.MessagesPublished.Inc()
	return ack, nil
}

// PublishWithACL checks subject ACL for component, validates CloudEvents, then publishes.
// Use this when components publish to enforce subject-level permissions.
func PublishWithACL(js nats.JetStreamContext, component, subject string, payload []byte) (*nats.PubAck, error) {
	if err := security.CheckPublish(component, subject); err != nil {
		return nil, err
	}
	return PublishValidated(js, subject, payload)
}

// PublishAsync publishes asynchronously and invokes callback with ack or error (Interface 1).
// For high-throughput publishers; does not block on network round-trip.
func PublishAsync(js nats.JetStreamContext, subject string, payload []byte, cb func(*nats.PubAck, error)) {
	go func() {
		ack, err := PublishValidated(js, subject, payload)
		if cb != nil {
			cb(ack, err)
		}
	}()
}

// PublishAsyncWithACL is PublishAsync with subject ACL check.
func PublishAsyncWithACL(js nats.JetStreamContext, component, subject string, payload []byte, cb func(*nats.PubAck, error)) {
	go func() {
		ack, err := PublishWithACL(js, component, subject, payload)
		if cb != nil {
			cb(ack, err)
		}
	}()
}

// BatchMessage is a single message for PublishBatch (Interface 1).
type BatchMessage struct {
	Subject string
	Payload []byte
}

// BatchResult holds the result of a batch publish (Interface 1).
type BatchResult struct {
	Acks []*nats.PubAck
	Err  error // First error encountered, if any (partial_error)
}

// PublishBatch publishes multiple messages; returns acks and first error if any fail (Interface 1).
// Used by Data Ingestion Pipeline. Each payload is validated as CloudEvents.
func PublishBatch(js nats.JetStreamContext, messages []BatchMessage) BatchResult {
	return publishBatchImpl(js, messages)
}

func publishBatchImpl(js nats.JetStreamContext, messages []BatchMessage) BatchResult {
	var acks []*nats.PubAck
	var firstErr error
	for _, m := range messages {
		ack, err := PublishValidated(js, m.Subject, m.Payload)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		acks = append(acks, ack)
	}
	return BatchResult{Acks: acks, Err: firstErr}
}
