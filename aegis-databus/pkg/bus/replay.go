package bus

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"
)

// ReplayFromTime creates a consumer that delivers from the given time (FR-DB-008).
func ReplayFromTime(js nats.JetStreamContext, stream, consumerName string, from time.Time) (*nats.Subscription, error) {
	_, err := js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable:       consumerName,
		DeliverPolicy: nats.DeliverByStartTimePolicy,
		OptStartTime:  &from,
		AckPolicy:     nats.AckExplicitPolicy,
	})
	if err != nil {
		return nil, err
	}
	return js.Subscribe("", func(m *nats.Msg) { m.Ack() },
		nats.BindStream(stream),
		nats.Durable(consumerName),
		nats.ManualAck(),
	)
}

// ReplayFromSequence creates a consumer that delivers from the given sequence.
func ReplayFromSequence(js nats.JetStreamContext, stream, consumerName string, seq uint64) (*nats.Subscription, error) {
	_, err := js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable:       consumerName,
		DeliverPolicy: nats.DeliverByStartSequencePolicy,
		OptStartSeq:   seq,
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
	})
	if err != nil {
		return nil, err
	}
	return js.Subscribe("", func(m *nats.Msg) { m.Ack() },
		nats.BindStream(stream),
		nats.Durable(consumerName),
		nats.ManualAck(),
	)
}

// ReplayLastN fetches the last N messages from a stream for replay (FR-DB-008).
func ReplayLastN(ctx context.Context, js nats.JetStreamContext, stream string, n int) ([]*nats.Msg, error) {
	info, err := js.StreamInfo(stream)
	if err != nil {
		return nil, err
	}
	start := uint64(1)
	if info.State.Msgs > uint64(n) {
		start = info.State.Msgs - uint64(n) + 1
	}
	consName := "replay-" + time.Now().Format("20060102150405")
	_, err = js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable:       consName,
		DeliverPolicy: nats.DeliverByStartSequencePolicy,
		OptStartSeq:   start,
		AckPolicy:     nats.AckExplicitPolicy,
	})
	if err != nil {
		return nil, err
	}
	sub, err := js.PullSubscribe("", consName, nats.BindStream(stream))
	if err != nil {
		return nil, err
	}
	defer sub.Unsubscribe()
	batch, err := sub.Fetch(n, nats.Context(ctx))
	if err != nil {
		return nil, err
	}
	var msgs []*nats.Msg
	for _, m := range batch {
		msgs = append(msgs, m)
		m.Ack()
	}
	return msgs, nil
}
