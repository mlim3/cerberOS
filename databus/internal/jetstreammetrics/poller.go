package jetstreammetrics

import (
	"context"
	"log/slog"
	"os"
	"time"

	"aegis-databus/internal/metrics"
	"aegis-databus/pkg/bus"
	"github.com/nats-io/nats.go"
)

// DefaultPollInterval is how often JetStream gauges refresh.
const DefaultPollInterval = 30 * time.Second

// Start runs a background loop that updates Prometheus gauges from JetStream StreamInfo
// until ctx is done.
func Start(ctx context.Context, nc *nats.Conn, interval time.Duration, logger *slog.Logger) {
	if nc == nil {
		return
	}
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil)).
			With("component", "databus", "module", "jetstream-metrics")
	}
	js, err := nc.JetStream()
	if err != nil {
		logger.Error("could not obtain a jetstream context from nats; jetstream gauges will not be refreshed",
			"error", err)
		return
	}

	tick := time.NewTicker(interval)
	defer tick.Stop()

	do := func() {
		streamNames := bus.AegisStreamNames()
		failures := 0
		for _, name := range streamNames {
			info, err := js.StreamInfo(name)
			if err != nil {
				metrics.JetStreamPollErrors.Inc()
				failures++
				logger.Warn("could not load stream info from jetstream; gauge will be stale until next poll",
					"stream", name, "error", err)
				continue
			}
			if info == nil {
				continue
			}
			metrics.JetStreamStreamMessages.WithLabelValues(name).Set(float64(info.State.Msgs))
			metrics.JetStreamStreamBytes.WithLabelValues(name).Set(float64(info.State.Bytes))

			var pendingSum uint64
			for ci := range js.ConsumersInfo(name) {
				if ci == nil {
					continue
				}
				pendingSum += ci.NumPending
			}
			metrics.JetStreamStreamPending.WithLabelValues(name).Set(float64(pendingSum))
		}
		logger.Debug("refreshed jetstream gauges from stream info",
			"stream_count", len(streamNames),
			"failure_count", failures)
	}

	do()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			do()
		}
	}
}
