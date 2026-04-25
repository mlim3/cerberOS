package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"aegis-databus/internal/metrics"
	"aegis-databus/pkg/bus"
	"github.com/nats-io/nats.go"
)

const interval = 5 * time.Second

// Heartbeat publishes aegis.health.databus status for Self-Healing to monitor.
type Heartbeat struct {
	nc     *nats.Conn
	logger *slog.Logger
}

func NewHeartbeat(nc *nats.Conn, logger *slog.Logger) *Heartbeat {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil)).
			With("service", "databus", "component", "health-heartbeat")
	}
	return &Heartbeat{nc: nc, logger: logger}
}

func (h *Heartbeat) Start(ctx context.Context) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			payload, _ := json.Marshal(map[string]string{
				"status":    "ok",
				"component": "databus",
				"time":      time.Now().UTC().Format(time.RFC3339Nano),
			})
			if err := h.nc.Publish(bus.SubjectHealthDatabus, payload); err != nil {
				h.logger.Warn("heartbeat publish failed", "subject", bus.SubjectHealthDatabus, "error", err)
			} else {
				metrics.HeartbeatsPublished.Inc()
			}
		}
	}
}
