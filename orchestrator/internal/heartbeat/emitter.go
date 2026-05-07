// Package heartbeat owns both the orchestrator's own liveness emitter
// (on `aegis.heartbeat.service.orchestrator`) and the cross-service
// sweeper that subscribes to `aegis.heartbeat.service.*` and decides
// which services are stale.
//
// Subject scheme:
//
//	aegis.heartbeat.<agent_id>         — per-agent beats (owned by agents-component)
//	aegis.heartbeat.service.<service>  — per-service beats (this package)
//
// The two schemes do not overlap: `aegis.heartbeat.*` matches exactly
// one token, so service beats are never delivered to agent crash
// detectors and vice versa.
//
// Payload is raw JSON (no NATS envelope) so consumers do not need
// cross-component message framing.
package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
)

const (
	// DefaultEmitInterval is how often a beat is published.
	DefaultEmitInterval = 10 * time.Second

	// SubjectPrefix is prepended to the service name to form the
	// NATS subject.
	SubjectPrefix = "aegis.heartbeat.service."

	// SubjectWildcard is the subscription pattern used by the
	// sweeper to catch every service's beats.
	SubjectWildcard = "aegis.heartbeat.service.*"
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

// Emitter publishes a service's own heartbeat periodically.
type Emitter struct {
	client     interfaces.NATSClient
	service    string
	instanceID string
	hostname   string
	pid        int
	subject    string
	interval   time.Duration
	startedAt  time.Time
}

// NewEmitter builds an Emitter for the given service name.
func NewEmitter(client interfaces.NATSClient, service string) *Emitter {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	pid := os.Getpid()
	return &Emitter{
		client:     client,
		service:    service,
		instanceID: fmt.Sprintf("%s-%s-%d", service, host, pid),
		hostname:   host,
		pid:        pid,
		subject:    SubjectPrefix + service,
		interval:   DefaultEmitInterval,
		startedAt:  time.Now().UTC(),
	}
}

// Start publishes beats until ctx is done.
func (e *Emitter) Start(ctx context.Context) {
	// The orchestrator binary owns this emitter, so component stays as
	// "orchestrator". `peer_service` is the heartbeat subject's service token,
	// which for the orchestrator's own beats equals "orchestrator".
	log := observability.LogFromContext(observability.WithModule(ctx, "heartbeat_emitter")).With(
		"peer_service", e.service,
	)

	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	e.emit(log)
	log.Info("heartbeat emitter started",
		"subject", e.subject,
		"interval", e.interval,
		"instance_id", e.instanceID,
	)

	for {
		select {
		case <-ctx.Done():
			log.Info("heartbeat emitter stopped")
			return
		case <-ticker.C:
			e.emit(log)
		}
	}
}

func (e *Emitter) emit(log *slog.Logger) {
	if e.client == nil {
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
		log.Warn("marshal beat failed", "error", err)
		return
	}
	if err := e.client.Publish(e.subject, data); err != nil {
		log.Warn("publish beat failed", "error", err)
	}
}
