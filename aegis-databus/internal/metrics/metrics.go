package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// MessagesPublished counts messages published via outbox relay.
	MessagesPublished = promauto.NewCounter(prometheus.CounterOpts{
		Name: "aegis_databus_messages_published_total",
		Help: "Total messages published to JetStream",
	})

	// PublishErrors counts publish failures.
	PublishErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "aegis_databus_publish_errors_total",
		Help: "Total publish errors",
	})

	// ValidationErrors counts CloudEvents validation failures.
	ValidationErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "aegis_databus_validation_errors_total",
		Help: "Total CloudEvents validation errors",
	})

	// HeartbeatsPublished counts heartbeats sent.
	HeartbeatsPublished = promauto.NewCounter(prometheus.CounterOpts{
		Name: "aegis_databus_heartbeats_total",
		Help: "Total heartbeats published",
	})

	// DegradedMode is 1 when in DEGRADED-HOLD (Memory unavailable), 0 otherwise.
	DegradedMode = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "aegis_databus_degraded",
		Help: "1 if in DEGRADED-HOLD mode (Memory API unavailable), 0 otherwise",
	})

	// OutboxRelayProcessed counts outbox entries successfully relayed.
	OutboxRelayProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "aegis_databus_outbox_relay_processed_total",
		Help: "Total outbox entries relayed to JetStream",
	})
)
