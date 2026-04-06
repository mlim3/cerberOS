package memory

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MockMemoryClient struct {
	mu               sync.RWMutex
	streamConfigs    map[string]StreamConfig
	consumerState    map[string]ConsumerState
	outbox           map[string]OutboxEntry
	auditLogs        []AuditLogEntry
	nkeys            map[string]string
	processedMessage map[string]struct{} // for IdempotencyChecker
}

func NewMockMemoryClient() *MockMemoryClient {
	return &MockMemoryClient{
		streamConfigs:    make(map[string]StreamConfig),
		consumerState:    make(map[string]ConsumerState),
		outbox:           make(map[string]OutboxEntry),
		auditLogs:        make([]AuditLogEntry, 0),
		nkeys:            make(map[string]string),
		processedMessage: make(map[string]struct{}),
	}
}

func (m *MockMemoryClient) GetStreamConfig(ctx context.Context, name string) (StreamConfig, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.streamConfigs[name]
	if !ok {
		return StreamConfig{}, ErrNotFound
	}
	return cfg, nil
}

func (m *MockMemoryClient) ListStreamConfigs(ctx context.Context) ([]StreamConfig, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]StreamConfig, 0, len(m.streamConfigs))
	for _, cfg := range m.streamConfigs {
		out = append(out, cfg)
	}
	return out, nil
}

func (m *MockMemoryClient) UpsertStreamConfig(ctx context.Context, cfg StreamConfig) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg.UpdatedAt = time.Now().UTC()
	m.streamConfigs[cfg.Name] = cfg
	return nil
}

func (m *MockMemoryClient) DeleteStreamConfig(ctx context.Context, name string) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.streamConfigs[name]; !ok {
		return ErrNotFound
	}
	delete(m.streamConfigs, name)
	return nil
}

func (m *MockMemoryClient) GetConsumerState(ctx context.Context, stream, consumer string) (ConsumerState, error) {
	_ = ctx
	key := stream + ":" + consumer
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.consumerState[key]
	if !ok {
		return ConsumerState{}, ErrNotFound
	}
	return state, nil
}

func (m *MockMemoryClient) UpdateConsumerAckSeq(ctx context.Context, stream, consumer string, ackSeq uint64) error {
	_ = ctx
	key := stream + ":" + consumer
	m.mu.Lock()
	defer m.mu.Unlock()
	m.consumerState[key] = ConsumerState{
		Stream:    stream,
		Consumer:  consumer,
		AckSeq:    ackSeq,
		UpdatedAt: time.Now().UTC(),
	}
	return nil
}

func (m *MockMemoryClient) InsertOutboxEntry(ctx context.Context, entry OutboxEntry) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if entry.Status == "" {
		entry.Status = "pending"
	}
	if entry.NextRetryAt.IsZero() {
		entry.NextRetryAt = time.Now().UTC()
	}
	m.outbox[entry.ID] = entry
	return nil
}

func (m *MockMemoryClient) FetchPendingOutbox(ctx context.Context, limit int) ([]OutboxEntry, error) {
	_ = ctx
	now := time.Now().UTC()
	m.mu.RLock()
	defer m.mu.RUnlock()
	pending := make([]OutboxEntry, 0)
	for _, entry := range m.outbox {
		if entry.Status != "pending" {
			continue
		}
		if entry.NextRetryAt.After(now) {
			continue
		}
		pending = append(pending, entry)
	}
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})
	if limit > 0 && len(pending) > limit {
		pending = pending[:limit]
	}
	return pending, nil
}

func (m *MockMemoryClient) MarkOutboxSent(ctx context.Context, id string, sequence uint64) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.outbox[id]
	if !ok {
		return ErrNotFound
	}
	entry.Status = "sent"
	entry.NatsSequence = sequence
	entry.NextRetryAt = time.Time{}
	m.outbox[id] = entry
	return nil
}

func (m *MockMemoryClient) AppendAuditLog(ctx context.Context, entry AuditLogEntry) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	m.auditLogs = append(m.auditLogs, entry)
	return nil
}

func (m *MockMemoryClient) ListAuditLogs(ctx context.Context, limit int) ([]AuditLogEntry, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := len(m.auditLogs)
	if limit <= 0 || limit > n {
		limit = n
	}
	start := n - limit
	if start < 0 {
		start = 0
	}
	out := make([]AuditLogEntry, limit)
	copy(out, m.auditLogs[start:n])
	return out, nil
}

func (m *MockMemoryClient) GetNKey(ctx context.Context, component string) (string, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	seed, ok := m.nkeys[component]
	if !ok {
		return "", ErrNotFound
	}
	return seed, nil
}

func (m *MockMemoryClient) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

// WasProcessed implements IdempotencyChecker. Returns true if messageID was recorded.
func (m *MockMemoryClient) WasProcessed(ctx context.Context, messageID string) (bool, error) {
	_ = ctx
	if messageID == "" {
		return false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.processedMessage[messageID]
	return ok, nil
}

// RecordProcessed implements IdempotencyChecker. Call after successful consumer processing.
func (m *MockMemoryClient) RecordProcessed(ctx context.Context, messageID string) error {
	_ = ctx
	if messageID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processedMessage[messageID] = struct{}{}
	return nil
}
