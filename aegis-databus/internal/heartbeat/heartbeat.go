// Package heartbeat publishes a periodic liveness beat on
// `aegis.heartbeat.service.<service>` so the orchestrator's heartbeat
// sweeper (a cron-style loop) can track which services are alive.
//
// The subject layout intentionally sits one token *below* the per-agent
// heartbeat subject `aegis.heartbeat.<agent_id>` used by the
// agents-component crash detector: the crash detector subscribes to
// `aegis.heartbeat.*` which only matches one token, so service beats on
// `aegis.heartbeat.service.<service>` are not delivered to it.
//
// The orchestrator's sweeper subscribes to `aegis.heartbeat.service.*`.
// Payload is raw JSON (no NATS envelope) so the sweeper does not need
// to understand any cross-component message framing.
package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	// DefaultInterval is how often a beat is published.
	DefaultInterval = 10 * time.Second

	// SubjectPrefix is prepended to the service name to form the
	// NATS subject. See package doc.
	SubjectPrefix = "aegis.heartbeat.service."
)

// Beat is the wire format for a service heartbeat.
type Beat struct {
	Service      string    `json:"service"`
	InstanceID   string    `json:"instance_id"`
	Status       string    `json:"status"`
	Timestamp    time.Time `json:"timestamp"`
	PID          int       `json:"pid"`
	Hostname     string    `json:"hostname"`
	UptimeSecond int64     `json:"uptime_s"`
}

// Emitter publishes Beat records on a fixed interval.
type Emitter struct {
	nc         *nats.Conn
	service    string
	instanceID string
	hostname   string
	pid        int
	subject    string
	log        *slog.Logger
	interval   time.Duration
	startedAt  time.Time
}

// New builds an Emitter. Falls back to "unknown" when hostname is
// unavailable so the heartbeat remains useful in constrained
// environments (e.g. minimal scratch containers).
func New(nc *nats.Conn, service string, log *slog.Logger) *Emitter {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	pid := os.Getpid()
	if log == nil {
		// Fallback path: ensure component is present even if the caller
		// passed a bare default logger. component is required by
		// docs/logging.md.
		log = slog.Default().With("component", "databus")
	}
	return &Emitter{
		nc:         nc,
		service:    service,
		instanceID: fmt.Sprintf("%s-%s-%d", service, host, pid),
		hostname:   host,
		pid:        pid,
		subject:    SubjectPrefix + service,
		log:        log.With("module", "heartbeat"),
		interval:   DefaultInterval,
		startedAt:  time.Now().UTC(),
	}
}

// Start publishes beats until ctx is done. Intended to be launched in
// its own goroutine: `go emitter.Start(ctx)`.
func (e *Emitter) Start(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	e.emit()
	e.log.Info("service heartbeat emitter started; will publish liveness beats on the orchestrator's heartbeat subject",
		"subject", e.subject,
		"interval", e.interval,
		"instance_id", e.instanceID,
	)

	for {
		select {
		case <-ctx.Done():
			e.log.Info("service heartbeat emitter stopped after shutdown signal; orchestrator will mark this instance dead at next sweep",
				"subject", e.subject)
			return
		case <-ticker.C:
			e.emit()
		}
	}
}

func (e *Emitter) emit() {
	if e.nc == nil {
		return
	}
	beat := Beat{
		Service:      e.service,
		InstanceID:   e.instanceID,
		Status:       "ok",
		Timestamp:    time.Now().UTC(),
		PID:          e.pid,
		Hostname:     e.hostname,
		UptimeSecond: int64(time.Since(e.startedAt).Seconds()),
	}
	data, err := json.Marshal(beat)
	if err != nil {
		e.log.Warn("could not marshal heartbeat beat to json; skipping this tick", "error", err)
		return
	}
	if err := e.nc.Publish(e.subject, data); err != nil {
		e.log.Warn("could not publish heartbeat beat to nats; orchestrator may mark this instance unhealthy",
			"subject", e.subject, "error", err)
	}
}
