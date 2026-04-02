package relay

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"aegis-databus/internal/metrics"
	"aegis-databus/pkg/bus"
	"aegis-databus/pkg/envelope"
	"aegis-databus/pkg/memory"
	"aegis-databus/pkg/telemetry"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultPollInterval = 500 * time.Millisecond
	initialBackoff      = 500 * time.Millisecond
	maxBackoff          = 60 * time.Second
)

type OutboxRelay struct {
	JS           nats.JetStreamContext
	MemoryClient memory.MemoryClient
	PollInterval time.Duration
	MaxBatch     int
	Logger       *log.Logger
}

func (r *OutboxRelay) Start(ctx context.Context) {
	if r.JS == nil || r.MemoryClient == nil {
		return
	}

	if r.PollInterval == 0 {
		r.PollInterval = defaultPollInterval
	}
	if r.MaxBatch == 0 {
		r.MaxBatch = 100
	}
	if r.Logger == nil {
		r.Logger = log.New(os.Stdout, "outbox-relay ", log.LstdFlags)
	}

	ticker := time.NewTicker(r.PollInterval)
	defer ticker.Stop()

	backoff := initialBackoff

	// Process once immediately, then on each tick
	doFetch := func() {
		entries, err := r.MemoryClient.FetchPendingOutbox(ctx, r.MaxBatch)
		if err != nil {
			r.Logger.Printf("fetch failed: %v", err)
			if !sleepWithContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			return
		}
		backoff = initialBackoff

		for _, entry := range entries {
			if ctx.Err() != nil {
				return
			}

			_, span := telemetry.Tracer().Start(ctx, "outbox.relay.publish",
				trace.WithAttributes(
					attribute.String("messaging.destination", entry.Subject),
					attribute.String("outbox.id", entry.ID),
				))
			if _, _, tid := envelope.ParseMetadata(entry.Payload); tid != "" {
				span.SetAttributes(attribute.String("ce.traceid", tid))
			}
			ack, err := bus.PublishValidated(r.JS, entry.Subject, entry.Payload)
			if err != nil {
				span.RecordError(err)
			}
			span.End()
			if err != nil {
				r.Logger.Printf("publish failed subject=%s size=%d attempt=%d err=%v",
					entry.Subject,
					len(entry.Payload),
					entry.AttemptCount+1,
					err,
				)
				if updateErr := r.applyPublishFailure(ctx, entry); updateErr != nil {
					r.Logger.Printf("update failed id=%s err=%v", entry.ID, updateErr)
				}
				continue
			}

			if err := r.MemoryClient.MarkOutboxSent(ctx, entry.ID, ack.Sequence); err != nil {
				r.Logger.Printf("mark sent failed id=%s err=%v", entry.ID, err)
			} else {
				metrics.OutboxRelayProcessed.Inc()
				// Audit log: metadata only, no payload (SR-DB-005); traceid for Design Principle 4
				source, corrID, traceID := envelope.ParseMetadata(entry.Payload)
				_ = r.MemoryClient.AppendAuditLog(ctx, memory.AuditLogEntry{
					ID:            entry.ID,
					Subject:       entry.Subject,
					Component:     source,
					CorrelationID: corrID,
					TraceID:       traceID,
					Timestamp:     time.Now().UTC(),
					SizeBytes:     len(entry.Payload),
				})
			}
		}
	}

	doFetch() // Process immediately (audit demo seeds)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		doFetch()
	}
}

func (r *OutboxRelay) applyPublishFailure(ctx context.Context, entry memory.OutboxEntry) error {
	entry.AttemptCount++
	entry.NextRetryAt = time.Now().UTC().Add(backoffForAttempt(entry.AttemptCount))
	entry.Status = "pending"
	return r.MemoryClient.InsertOutboxEntry(ctx, entry)
}

func backoffForAttempt(attempt int) time.Duration {
	if attempt <= 0 {
		return initialBackoff
	}
	backoff := initialBackoff
	for i := 1; i < attempt; i++ {
		backoff = nextBackoff(backoff)
		if backoff == maxBackoff {
			break
		}
	}
	return backoff
}

func nextBackoff(current time.Duration) time.Duration {
	if current >= maxBackoff {
		return maxBackoff
	}
	next := current * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

var errMissingFields = errors.New("outbox relay missing fields")

func (r *OutboxRelay) Validate() error {
	if r.JS == nil || r.MemoryClient == nil {
		return errMissingFields
	}
	return nil
}
