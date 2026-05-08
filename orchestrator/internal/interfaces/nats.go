package interfaces

// MessageHandler is the callback signature for NATS message subscriptions.
type MessageHandler func(subject string, data []byte) error

// NATSClient defines all NATS publish/subscribe operations used by
// the Communications Gateway (M1) (§11.5).
// The real implementation uses nats.go with JetStream and mTLS.
// The mock implementation is in internal/mocks/nats_mock.go.
//
// CRITICAL: All NATS connections MUST use mTLS. The Orchestrator's credentials
// MUST only permit publish/subscribe on aegis.orchestrator.> topics.
// Cross-component topic access requires explicit authorization (§11.5).
type NATSClient interface {
	// Publish sends a message to the given NATS subject.
	// Used by Communications Gateway for all outbound messages.
	Publish(subject string, data []byte) error

	// Subscribe registers a core NATS subscription on the given subject.
	// Delivers messages in real-time only; no historical replay on restart.
	Subscribe(subject string, handler MessageHandler) error

	// SubscribeDurable subscribes to a subject using a JetStream push consumer
	// with DeliverAll policy, replaying all historical messages from the stream
	// on every startup before delivering new ones. This ensures the in-process
	// agentStore is rebuilt from the full write history after any restart.
	// Falls back to core NATS subscribe when JetStream is unavailable.
	SubscribeDurable(subject, consumer string, handler MessageHandler) error

	// IsConnected returns true if the NATS connection is active.
	// Used by the health check loop (§12.1).
	IsConnected() bool

	// Close gracefully closes the NATS connection.
	Close()
}
