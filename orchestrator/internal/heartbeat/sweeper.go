package heartbeat

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
)

const (
	// DefaultSweepInterval is how often the sweeper scans the
	// last-seen map and logs stale services. This is the "cron"
	// side of the heartbeat system.
	DefaultSweepInterval = 30 * time.Second

	// DefaultStaleAfter is the window after which a service is
	// considered stale (no beat received). Roughly 4 missed 10s
	// emits.
	DefaultStaleAfter = 45 * time.Second
)

// Health is the derived liveness state for one (service, instance).
type Health string

const (
	HealthAlive   Health = "alive"
	HealthStale   Health = "stale"
	HealthUnknown Health = "unknown"
)

// ServiceHealth is a point-in-time snapshot of one instance's
// liveness. Rendered by Sweeper.Snapshot for the /heartbeats endpoint.
type ServiceHealth struct {
	Service    string    `json:"service"`
	InstanceID string    `json:"instance_id"`
	Hostname   string    `json:"hostname"`
	LastSeen   time.Time `json:"last_seen"`
	AgeSeconds float64   `json:"age_seconds"`
	Status     string    `json:"status"`
	Health     Health    `json:"health"`
	UptimeSec  int64     `json:"uptime_s"`
}

// Sweeper subscribes to service heartbeats and marks instances that
// stop beating as stale. It is the consumer half of the heartbeat
// system.
type Sweeper struct {
	client        interfaces.NATSClient
	sweepInterval time.Duration
	staleAfter    time.Duration

	mu       sync.RWMutex
	entries  map[string]*entry // key = service + "/" + instanceID
	lastScan time.Time

	onStaleCb func(ServiceHealth)
}

type entry struct {
	beat     Beat
	received time.Time
	stale    bool
}

// Option customizes a Sweeper.
type Option func(*Sweeper)

// WithSweepInterval overrides the default 30s scan interval.
func WithSweepInterval(d time.Duration) Option {
	return func(s *Sweeper) {
		if d > 0 {
			s.sweepInterval = d
		}
	}
}

// WithStaleAfter overrides the default 45s stale window.
func WithStaleAfter(d time.Duration) Option {
	return func(s *Sweeper) {
		if d > 0 {
			s.staleAfter = d
		}
	}
}

// OnStale registers a callback fired once when an instance transitions
// from alive to stale. Use for recovery hooks; the Sweeper itself only
// logs.
func OnStale(cb func(ServiceHealth)) Option {
	return func(s *Sweeper) { s.onStaleCb = cb }
}

// NewSweeper builds a Sweeper. Call Start to begin consuming.
func NewSweeper(client interfaces.NATSClient, opts ...Option) *Sweeper {
	s := &Sweeper{
		client:        client,
		sweepInterval: DefaultSweepInterval,
		staleAfter:    DefaultStaleAfter,
		entries:       make(map[string]*entry),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start subscribes to service beats and begins the periodic sweep.
// Returns an error if subscription fails; otherwise runs until ctx is
// done.
func (s *Sweeper) Start(ctx context.Context) error {
	log := observability.LogFromContext(observability.WithModule(ctx, "heartbeat_sweeper"))

	if err := s.client.Subscribe(SubjectWildcard, s.handleMessage); err != nil {
		return err
	}
	log.Info("heartbeat sweeper subscribed",
		"subject", SubjectWildcard,
		"sweep_interval", s.sweepInterval,
		"stale_after", s.staleAfter,
	)

	go s.sweepLoop(ctx)
	return nil
}

func (s *Sweeper) handleMessage(subject string, data []byte) error {
	var beat Beat
	if err := json.Unmarshal(data, &beat); err != nil {
		observability.LogFromContext(context.Background()).Warn(
			"heartbeat: unmarshal failed",
			"subject", subject,
			"error", err,
		)
		return nil // core NATS, no redelivery — just drop
	}
	if beat.Service == "" {
		beat.Service = strings.TrimPrefix(subject, SubjectPrefix)
	}
	if beat.InstanceID == "" {
		beat.InstanceID = beat.Service
	}

	key := beat.Service + "/" + beat.InstanceID
	now := time.Now().UTC()

	s.mu.Lock()
	prev, had := s.entries[key]
	s.entries[key] = &entry{
		beat:     beat,
		received: now,
		stale:    false,
	}
	s.mu.Unlock()

	if had && prev.stale {
		observability.LogFromContext(context.Background()).Info(
			"heartbeat: service recovered",
			"service", beat.Service,
			"instance_id", beat.InstanceID,
		)
	}
	return nil
}

func (s *Sweeper) sweepLoop(ctx context.Context) {
	log := observability.LogFromContext(observability.WithModule(ctx, "heartbeat_sweeper"))
	ticker := time.NewTicker(s.sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("heartbeat sweeper stopped")
			return
		case <-ticker.C:
			s.sweep(log)
		}
	}
}

func (s *Sweeper) sweep(log *slog.Logger) {
	now := time.Now().UTC()
	cutoff := now.Add(-s.staleAfter)

	s.mu.Lock()
	s.lastScan = now
	// Pull out state snapshots so the callback runs without holding the lock.
	var newlyStale []ServiceHealth
	for _, e := range s.entries {
		if e.received.Before(cutoff) && !e.stale {
			e.stale = true
			newlyStale = append(newlyStale, toServiceHealth(e, now, s.staleAfter))
		}
	}
	s.mu.Unlock()

	for _, sh := range newlyStale {
		log.Warn("heartbeat: service stale",
			"service", sh.Service,
			"instance_id", sh.InstanceID,
			"last_seen", sh.LastSeen.Format(time.RFC3339),
			"age_seconds", sh.AgeSeconds,
			"stale_after", s.staleAfter,
		)
		if s.onStaleCb != nil {
			s.onStaleCb(sh)
		}
	}
}

// Snapshot returns the current view of all known service instances,
// sorted by service then instance_id for stable output. Health is
// computed from the real-time age on read so callers see staleness
// immediately — the sweep tick only owns the one-shot transition log
// and OnStale callback.
func (s *Sweeper) Snapshot() []ServiceHealth {
	now := time.Now().UTC()
	s.mu.RLock()
	out := make([]ServiceHealth, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, toServiceHealth(e, now, s.staleAfter))
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out
}

func toServiceHealth(e *entry, now time.Time, staleAfter time.Duration) ServiceHealth {
	age := now.Sub(e.received)
	health := HealthAlive
	if age > staleAfter {
		health = HealthStale
	}
	return ServiceHealth{
		Service:    e.beat.Service,
		InstanceID: e.beat.InstanceID,
		Hostname:   e.beat.Hostname,
		LastSeen:   e.received,
		AgeSeconds: age.Seconds(),
		Status:     e.beat.Status,
		Health:     health,
		UptimeSec:  e.beat.UptimeSecond,
	}
}
