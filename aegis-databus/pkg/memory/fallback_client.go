package memory

import (
	"context"
	"sync"
	"time"

	"aegis-databus/internal/metrics"
)

// FallbackClient wraps a primary (HTTP) and fallback (Mock) MemoryClient.
// When the primary fails (e.g. Ping), it switches to fallback and enters DEGRADED-HOLD.
// A background health check can switch back to primary when it recovers.
type FallbackClient struct {
	mu         sync.RWMutex
	primary    MemoryClient
	fallback   MemoryClient
	client     MemoryClient // current active client
	degraded   bool
	pingTicker *time.Ticker
	stopCh     chan struct{}
}

// NewFallbackClient creates a client that uses primary when healthy, fallback when not.
func NewFallbackClient(primary, fallback MemoryClient) *FallbackClient {
	fc := &FallbackClient{
		primary:  primary,
		fallback: fallback,
		client:   primary,
		stopCh:   make(chan struct{}),
	}
	if primary == nil {
		fc.client = fallback
		fc.degraded = true
		metrics.DegradedMode.Set(1)
	} else {
		metrics.DegradedMode.Set(0)
	}
	return fc
}

// Init performs initial health check. Call before StartHealthCheck.
func (fc *FallbackClient) Init(ctx context.Context) {
	if fc.primary == nil {
		return
	}
	if err := fc.primary.Ping(ctx); err != nil {
		fc.switchToFallback()
	}
}

// StartHealthCheck pings primary every interval. On failure, switches to fallback.
// On success after degraded, switches back to primary.
func (fc *FallbackClient) StartHealthCheck(ctx context.Context, interval time.Duration) {
	if fc.primary == nil {
		return
	}
	fc.pingTicker = time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ctx.Done():
				fc.pingTicker.Stop()
				return
			case <-fc.pingTicker.C:
				if err := fc.primary.Ping(ctx); err != nil {
					fc.switchToFallback()
				} else {
					fc.trySwitchToPrimary()
				}
			}
		}
	}()
}

func (fc *FallbackClient) switchToFallback() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if !fc.degraded {
		fc.degraded = true
		fc.client = fc.fallback
		metrics.DegradedMode.Set(1)
	}
}

func (fc *FallbackClient) trySwitchToPrimary() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.degraded && fc.primary != nil {
		fc.degraded = false
		fc.client = fc.primary
		metrics.DegradedMode.Set(0)
	}
}

// Degraded returns true if using fallback (DEGRADED-HOLD mode).
func (fc *FallbackClient) Degraded() bool {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.degraded
}

// Client returns the current active client for direct use.
func (fc *FallbackClient) Client() MemoryClient {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.client
}

func (fc *FallbackClient) GetStreamConfig(ctx context.Context, name string) (StreamConfig, error) {
	return fc.Client().GetStreamConfig(ctx, name)
}

func (fc *FallbackClient) ListStreamConfigs(ctx context.Context) ([]StreamConfig, error) {
	return fc.Client().ListStreamConfigs(ctx)
}

func (fc *FallbackClient) UpsertStreamConfig(ctx context.Context, cfg StreamConfig) error {
	return fc.Client().UpsertStreamConfig(ctx, cfg)
}

func (fc *FallbackClient) DeleteStreamConfig(ctx context.Context, name string) error {
	return fc.Client().DeleteStreamConfig(ctx, name)
}

func (fc *FallbackClient) GetConsumerState(ctx context.Context, stream, consumer string) (ConsumerState, error) {
	return fc.Client().GetConsumerState(ctx, stream, consumer)
}

func (fc *FallbackClient) UpdateConsumerAckSeq(ctx context.Context, stream, consumer string, ackSeq uint64) error {
	return fc.Client().UpdateConsumerAckSeq(ctx, stream, consumer, ackSeq)
}

func (fc *FallbackClient) InsertOutboxEntry(ctx context.Context, entry OutboxEntry) error {
	return fc.Client().InsertOutboxEntry(ctx, entry)
}

func (fc *FallbackClient) FetchPendingOutbox(ctx context.Context, limit int) ([]OutboxEntry, error) {
	return fc.Client().FetchPendingOutbox(ctx, limit)
}

func (fc *FallbackClient) MarkOutboxSent(ctx context.Context, id string, sequence uint64) error {
	return fc.Client().MarkOutboxSent(ctx, id, sequence)
}

func (fc *FallbackClient) AppendAuditLog(ctx context.Context, entry AuditLogEntry) error {
	return fc.Client().AppendAuditLog(ctx, entry)
}

func (fc *FallbackClient) ListAuditLogs(ctx context.Context, limit int) ([]AuditLogEntry, error) {
	return fc.Client().ListAuditLogs(ctx, limit)
}

func (fc *FallbackClient) GetNKey(ctx context.Context, component string) (string, error) {
	return fc.Client().GetNKey(ctx, component)
}

func (fc *FallbackClient) Ping(ctx context.Context) error {
	return fc.Client().Ping(ctx)
}

// WasProcessed implements IdempotencyChecker by delegating to the active client when it supports it.
func (fc *FallbackClient) WasProcessed(ctx context.Context, messageID string) (bool, error) {
	c := fc.Client()
	if ic, ok := c.(IdempotencyChecker); ok {
		return ic.WasProcessed(ctx, messageID)
	}
	return false, nil
}

// RecordProcessed implements IdempotencyChecker by delegating to the active client when it supports it.
func (fc *FallbackClient) RecordProcessed(ctx context.Context, messageID string) error {
	c := fc.Client()
	if ic, ok := c.(IdempotencyChecker); ok {
		return ic.RecordProcessed(ctx, messageID)
	}
	return nil
}
