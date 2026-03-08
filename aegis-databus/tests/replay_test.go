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
