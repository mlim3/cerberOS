package bus

import (
	"context"

	"aegis-databus/internal/metrics"
	"aegis-databus/pkg/envelope"
	"aegis-databus/pkg/telemetry"
	"aegis-databus/pkg/validation"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// HeaderOriginalSubject is set when forwarding a message to DLQ so replay knows the target subject.
const HeaderOriginalSubject = "X-Aegis-Original-Subject"

func validatorOrDefault(v []validation.Validator) validation.Validator {
	if len(v) > 0 {
		return v[0]
	}
	return validation.Strict
}

// PublishValidated publishes to JetStream only if payload passes validation.
// When the payload is valid CloudEvents JSON with an "id" field, sets Nats-Msg-Id for JetStream deduplication (120s window).
// Optional v: use validation.NoOp for tests. Default: validation.Strict.
func PublishValidated(js nats.JetStreamContext, subject string, payload []byte, v ...validation.Validator) (*nats.PubAck, error) {
	msgID := envelope.ExtractID(payload)
	return publishJetStream(js, subject, payload, msgID, v...)
}

func publishJetStream(js nats.JetStreamContext, subject string, payload []byte, msgID string, v ...validation.Validator) (*nats.PubAck, error) {
	// Continue the upstream trace when the CloudEvents envelope carries a traceid.
	base := context.Background()
	if _, _, tid := envelope.ParseMetadata(payload); tid != "" {
		base = telemetry.ContextFromTraceID(base, tid)
	}
	_, span := telemetry.Tracer().Start(base, "PublishValidated",
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination", subject),
		))
	defer span.End()

	val := validatorOrDefault(v)
	if err := val.ValidatePayload(payload); err != nil {
		metrics.ValidationErrors.Inc()
		span.RecordError(err)
		return nil, err
	}
	if id := envelope.ExtractID(payload); id != "" {
		span.SetAttributes(attribute.String("ce.id", id))
	}
	if _, _, tid := envelope.ParseMetadata(payload); tid != "" {
		span.SetAttributes(attribute.String("ce.traceid", tid))
	}
	msg := &nats.Msg{Subject: subject, Data: payload}
	if msg.Header == nil {
		msg.Header = make(nats.Header)
	}
	if msgID != "" {
		msg.Header.Set("Nats-Msg-Id", msgID)
	}
	ack, err := js.PublishMsg(msg)
	if err != nil {
		metrics.PublishErrors.Inc()
		span.RecordError(err)
		return nil, err
	}
	metrics.MessagesPublished.Inc()
	return ack, nil
}

// PublishWithACL validates (ACL + CloudEvents) then publishes.
// Optional v: use validation.NoOp for tests. Default: validation.Strict.
func PublishWithACL(js nats.JetStreamContext, component, subject string, payload []byte, v ...validation.Validator) (*nats.PubAck, error) {
	val := validatorOrDefault(v)
	if err := val.ValidatePublish(component, subject, payload); err != nil {
		return nil, err
	}
	return PublishValidated(js, subject, payload, v...)
}

// PublishWithMsgID publishes with an explicit Nats-Msg-Id (e.g. DLQ replay). Overrides ExtractID from payload when msgID is non-empty.
func PublishWithMsgID(js nats.JetStreamContext, subject string, payload []byte, msgID string, v ...validation.Validator) (*nats.PubAck, error) {
	if msgID == "" {
		msgID = envelope.ExtractID(payload)
	}
	return publishJetStream(js, subject, payload, msgID, v...)
}

// ForwardToDLQ publishes a failed message to the DLQ with X-Aegis-Original-Subject header.
// Consumers should use this when forwarding after MaxDeliver so replay can restore the target subject.
func ForwardToDLQ(js nats.JetStreamContext, originalSubject string, payload []byte) error {
	msg := &nats.Msg{Subject: SubjectDLQ, Data: payload}
	if msg.Header == nil {
		msg.Header = make(nats.Header)
	}
	msg.Header.Set(HeaderOriginalSubject, originalSubject)
	if id := envelope.ExtractID(payload); id != "" {
		msg.Header.Set("Nats-Msg-Id", id)
	}
	_, err := js.PublishMsg(msg)
	if err == nil {
		metrics.DLQForwardedTotal.Inc()
	}
	return err
}

// PublishAsync publishes asynchronously and invokes callback with ack or error (Interface 1).
func PublishAsync(js nats.JetStreamContext, subject string, payload []byte, cb func(*nats.PubAck, error), v ...validation.Validator) {
	go func() {
		ack, err := PublishValidated(js, subject, payload, v...)
		if cb != nil {
			cb(ack, err)
		}
	}()
}

// PublishAsyncWithACL is PublishAsync with subject ACL check.
func PublishAsyncWithACL(js nats.JetStreamContext, component, subject string, payload []byte, cb func(*nats.PubAck, error), v ...validation.Validator) {
	go func() {
		ack, err := PublishWithACL(js, component, subject, payload, v...)
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

// PublishBatch publishes multiple messages; each payload is validated.
func PublishBatch(js nats.JetStreamContext, messages []BatchMessage, v ...validation.Validator) BatchResult {
	return publishBatchImpl(js, messages, validatorOrDefault(v))
}

func publishBatchImpl(js nats.JetStreamContext, messages []BatchMessage, val validation.Validator) BatchResult {
	var acks []*nats.PubAck
	var firstErr error
	for _, m := range messages {
		ack, err := PublishValidated(js, m.Subject, m.Payload, val)
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
