package jetstreammetrics

import (
	"context"
	"log"
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
func Start(ctx context.Context, nc *nats.Conn, interval time.Duration, logger *log.Logger) {
	if nc == nil {
		return
	}
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if logger == nil {
		logger = log.New(os.Stdout, "jetstream-metrics ", log.LstdFlags)
	}
	js, err := nc.JetStream()
	if err != nil {
		logger.Printf("JetStream: %v", err)
		return
	}

	tick := time.NewTicker(interval)
	defer tick.Stop()

	do := func() {
		for _, name := range bus.AegisStreamNames() {
			info, err := js.StreamInfo(name)
			if err != nil {
				metrics.JetStreamPollErrors.Inc()
				logger.Printf("StreamInfo %s: %v", name, err)
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
