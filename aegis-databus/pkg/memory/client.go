package memory

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type StreamConfig struct {
	Name       string
	Subjects   []string
	MaxAge     time.Duration
	MaxBytes   int64
	Replicas   int
	Discard    string
	Duplicates time.Duration
	UpdatedAt  time.Time
}

type ConsumerState struct {
	Stream    string
	Consumer  string
	AckSeq    uint64
	UpdatedAt time.Time
}

type OutboxEntry struct {
	ID           string
	Subject      string
	Payload      []byte
	Status       string
	AttemptCount int
	NextRetryAt  time.Time
	CreatedAt    time.Time
	NatsSequence uint64
}

type AuditLogEntry struct {
	ID            string
	Subject       string
	Component     string
	CorrelationID string
	TraceID       string // Design Principle 4: observable operations — trace for message flows
	Timestamp     time.Time
	SizeBytes     int
}

type MemoryClient interface {
	GetStreamConfig(ctx context.Context, name string) (StreamConfig, error)
	ListStreamConfigs(ctx context.Context) ([]StreamConfig, error)
	UpsertStreamConfig(ctx context.Context, cfg StreamConfig) error
	DeleteStreamConfig(ctx context.Context, name string) error

	GetConsumerState(ctx context.Context, stream, consumer string) (ConsumerState, error)
	UpdateConsumerAckSeq(ctx context.Context, stream, consumer string, ackSeq uint64) error

	InsertOutboxEntry(ctx context.Context, entry OutboxEntry) error
	FetchPendingOutbox(ctx context.Context, limit int) ([]OutboxEntry, error)
	MarkOutboxSent(ctx context.Context, id string, sequence uint64) error

	AppendAuditLog(ctx context.Context, entry AuditLogEntry) error
	ListAuditLogs(ctx context.Context, limit int) ([]AuditLogEntry, error)
	GetNKey(ctx context.Context, component string) (string, error)
	Ping(ctx context.Context) error
}

// IdempotencyChecker is optional. When implemented, DLQ replay checks WasProcessed before
// republishing to avoid duplicate processing if upstream already retried and succeeded.
// MemoryClient implementations may optionally implement this interface.
type IdempotencyChecker interface {
	WasProcessed(ctx context.Context, messageID string) (bool, error)
	RecordProcessed(ctx context.Context, messageID string) error
}
