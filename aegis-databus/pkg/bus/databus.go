package bus

import (
	"context"

	"github.com/nats-io/nats.go"
)

// Databus provides a thin abstraction over NATS JetStream for the Aegis Data Bus.
// Use PublishValidated or PublishWithACL for publishing; subscribe via js.Subscribe
// or js.QueueSubscribe with subject constants from topics.go.
//
// Stream names: StreamTasks, StreamAgents, StreamRuntime, StreamVault, StreamMemory, StreamMonitoring.
// Subject constants: SubjectTasks, SubjectAgents, SubjectTasksRouted, etc.
type Databus struct {
	JS nats.JetStreamContext
}

// NewDatabus wraps a JetStream context for bus operations.
func NewDatabus(js nats.JetStreamContext) *Databus {
	return &Databus{JS: js}
}

// Publish publishes a validated CloudEvent to subject. For ACL-checked publish, use
// PublishWithACL from publish.go.
func (d *Databus) Publish(subject string, payload []byte) (*nats.PubAck, error) {
	return PublishValidated(d.JS, subject, payload)
}

// PublishAsync publishes asynchronously (Interface 1; high-throughput).
func (d *Databus) PublishAsync(subject string, payload []byte, cb func(*nats.PubAck, error)) {
	PublishAsync(d.JS, subject, payload, cb)
}

// PublishBatch publishes multiple messages (Interface 1; Data Ingestion Pipeline).
func (d *Databus) PublishBatch(messages []BatchMessage) BatchResult {
	return PublishBatch(d.JS, messages)
}

// PullFetch fetches a batch from a durable consumer (Interface 2; Data Ingestion Pipeline).
func (d *Databus) PullFetch(ctx context.Context, stream, consumer string, batchSize int) ([]*nats.Msg, error) {
	return PullFetch(ctx, d.JS, stream, consumer, batchSize)
}
