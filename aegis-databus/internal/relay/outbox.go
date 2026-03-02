package relay

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"aegis-databus/pkg/memory"
	"github.com/nats-io/nats.go"
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

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		entries, err := r.MemoryClient.FetchPendingOutbox(ctx, r.MaxBatch)
		if err != nil {
			r.Logger.Printf("fetch failed: %v", err)
			if !sleepWithContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = initialBackoff

		for _, entry := range entries {
			if ctx.Err() != nil {
				return
			}

			ack, err := r.JS.Publish(entry.Subject, entry.Payload)
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
			}
		}
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
