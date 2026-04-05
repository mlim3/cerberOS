package lifecycle

import (
	"context"
	"sync"
	"time"
)

// HeartbeatConfig configures the crash-detection watchdog.
type HeartbeatConfig struct {
	// Interval is the expected time between consecutive heartbeats published by
	// the agent process. The watchdog ticks at this same interval.
	// Default: 5 seconds.
	Interval time.Duration

	// MaxMissed is the number of consecutive missed heartbeat intervals before
	// the agent is declared crashed. Default: 3.
	MaxMissed int
}

func (c HeartbeatConfig) withDefaults() HeartbeatConfig {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.MaxMissed <= 0 {
		c.MaxMissed = 3
	}
	return c
}

// agentWatch holds per-agent watchdog state.
type agentWatch struct {
	lastSeen  time.Time
	missed    int
	triggered bool // true once onCrash has fired; prevents duplicate callbacks
}

// CrashDetector tracks per-agent heartbeat timestamps. After MaxMissed
// consecutive missed heartbeat intervals it fires the onCrash callback exactly
// once per agent.
//
// Typical usage:
//
//	d := lifecycle.NewCrashDetector(cfg, func(id string) { /* handle crash */ })
//	go d.Run(ctx)
//	// on agent spawn:
//	d.Watch(agentID)
//	// on heartbeat receipt:
//	d.RecordHeartbeat(agentID)
//	// on intentional termination (not a crash):
//	d.Unwatch(agentID)
type CrashDetector struct {
	cfg     HeartbeatConfig
	onCrash func(agentID string)

	mu      sync.Mutex
	watches map[string]*agentWatch
}

// NewCrashDetector creates a CrashDetector with the given config and crash
// callback. onCrash is called at most once per agent and always from the
// Run goroutine, never from RecordHeartbeat or Watch.
func NewCrashDetector(cfg HeartbeatConfig, onCrash func(agentID string)) *CrashDetector {
	return &CrashDetector{
		cfg:     cfg.withDefaults(),
		onCrash: onCrash,
		watches: make(map[string]*agentWatch),
	}
}

// Watch begins tracking heartbeats for agentID. Stamps lastSeen to now so a
// freshly-spawned agent is not immediately flagged as missed. Safe to call
// concurrently. Calling Watch for an already-watched agent resets its counters.
func (d *CrashDetector) Watch(agentID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.watches[agentID] = &agentWatch{lastSeen: time.Now()}
}

// Unwatch stops tracking an agent. Call this on intentional shutdown so a
// clean teardown is not mistaken for a crash. No-op for unknown agents.
func (d *CrashDetector) Unwatch(agentID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.watches, agentID)
}

// RecordHeartbeat updates the last-seen timestamp for agentID and resets its
// missed counter. No-op for agents that are not currently watched.
func (d *CrashDetector) RecordHeartbeat(agentID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.watches[agentID]
	if !ok {
		return // stale heartbeat from an unwatched (terminated) agent
	}
	w.lastSeen = time.Now()
	w.missed = 0
}

// Run starts the watchdog loop. It ticks at every Interval, inspects all
// watched agents, and fires onCrash for any that have exceeded MaxMissed
// consecutive missed intervals. Blocks until ctx is cancelled.
func (d *CrashDetector) Run(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(time.Now())
		}
	}
}

// tick performs one watchdog pass at the given now time.
// Separated from Run so tests can drive it directly without real timers.
func (d *CrashDetector) tick(now time.Time) {
	d.mu.Lock()
	var crashed []string
	for agentID, w := range d.watches {
		if w.triggered {
			continue
		}
		if now.Sub(w.lastSeen) >= d.cfg.Interval {
			w.missed++
			if w.missed >= d.cfg.MaxMissed {
				w.triggered = true
				crashed = append(crashed, agentID)
			}
		} else {
			w.missed = 0
		}
	}
	d.mu.Unlock()

	// Fire callbacks outside the lock so onCrash can safely call Unwatch.
	for _, agentID := range crashed {
		d.onCrash(agentID)
	}
}

// Watched returns the number of agents currently being tracked. Useful in tests.
func (d *CrashDetector) Watched() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.watches)
}
