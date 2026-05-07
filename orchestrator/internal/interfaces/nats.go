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

	// Subscribe registers a durable JetStream consumer on the given subject.
	// handler is called for each message. The implementation ACKs on success,
	// NAKs on handler error. Dead-letters after max_redelivery (§11.1).
	Subscribe(subject string, handler MessageHandler) error

	// IsConnected returns true if the NATS connection is active.
	// Used by the health check loop (§12.1).
	IsConnected() bool

	// Close gracefully closes the NATS connection.
	Close()
}
