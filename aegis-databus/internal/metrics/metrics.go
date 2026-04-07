package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// JetStreamStreamMessages mirrors JetStream stream State.Msgs (updated by poller).
var JetStreamStreamMessages = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "aegis_databus_jetstream_stream_messages",
	Help: "JetStream stream message count (from StreamInfo)",
}, []string{"stream"})

// JetStreamStreamBytes mirrors JetStream stream State.Bytes.
var JetStreamStreamBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "aegis_databus_jetstream_stream_bytes",
	Help: "JetStream stream total bytes (from StreamInfo)",
}, []string{"stream"})

// JetStreamStreamPending is the sum of NumPending across all consumers on the stream.
var JetStreamStreamPending = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "aegis_databus_jetstream_stream_pending",
	Help: "Sum of pending messages across consumers on the stream",
}, []string{"stream"})

// JetStreamPollErrors counts failures refreshing JetStream gauges.
var JetStreamPollErrors = promauto.NewCounter(prometheus.CounterOpts{
	Name: "aegis_databus_jetstream_poll_errors_total",
	Help: "Errors while polling JetStream stream/consumer info",
})

// DLQForwardedTotal counts messages published to the DLQ via ForwardToDLQ.
var DLQForwardedTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "aegis_databus_dlq_forwarded_total",
	Help: "Total messages forwarded to the dead-letter queue",
})

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
