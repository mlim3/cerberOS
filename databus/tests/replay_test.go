package tests

import (
	"context"
	"testing"
	"time"

	"aegis-databus/pkg/bus"
	"aegis-databus/pkg/streams"
	"github.com/nats-io/nats.go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

// TestReplay demonstrates FR-DB-008: Event Replay from sequence.
func TestReplay(t *testing.T) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		t.Fatalf("start NATS: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	uri, _ := container.ConnectionString(ctx)

	nc, _ := nats.Connect(uri)
	t.Cleanup(func() { nc.Close() })
	_ = streams.EnsureStreamsWithReplicas(nc, 1)

	js, _ := nc.JetStream()
	subject := "aegis.tasks.replay"
	for i := 0; i < 10; i++ {
		js.Publish(subject, []byte("msg"))
	}
	time.Sleep(100 * time.Millisecond)

	// Replay last 5 from AEGIS_TASKS
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	msgs, err := bus.ReplayLastN(ctx2, js, bus.StreamTasks, 5)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(msgs) < 1 {
		t.Errorf("expected at least 1 replayed message, got %d", len(msgs))
	}
	t.Logf("replay: got %d messages", len(msgs))
}

// TestPullFetch demonstrates Interface 2: pull_fetch for Data Ingestion Pipeline.
func TestPullFetch(t *testing.T) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		t.Fatalf("start NATS: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	uri, _ := container.ConnectionString(ctx)

	nc, _ := nats.Connect(uri)
	t.Cleanup(func() { nc.Close() })
	_ = streams.EnsureStreamsWithReplicas(nc, 1)

	js, _ := nc.JetStream()
	stream := bus.StreamTasks
	consumer := "pull-fetch-test"
	subject := "aegis.tasks.pullfetch"

	// Create durable pull consumer
	_, err = js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable:       consumer,
		DeliverPolicy: nats.DeliverAllPolicy,
		AckPolicy:     nats.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("add consumer: %v", err)
	}

	// Publish 3 messages
	for i := 0; i < 3; i++ {
		js.Publish(subject, []byte("pull-msg"))
	}
	time.Sleep(100 * time.Millisecond)

	// PullFetch batch of 5
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	msgs, err := bus.PullFetch(ctx2, js, stream, consumer, 5)
	if err != nil {
		t.Fatalf("PullFetch: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}
	for _, m := range msgs {
		m.Ack()
	}
}
