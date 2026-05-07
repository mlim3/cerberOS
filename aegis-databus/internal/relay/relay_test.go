package relay

import (
	"context"
	"testing"
	"time"

	"aegis-databus/pkg/envelope"
	"aegis-databus/pkg/memory"
	"aegis-databus/pkg/streams"

	"github.com/nats-io/nats.go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

func TestRelayAppendsAuditLogOnPublish(t *testing.T) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		t.Skipf("NATS: %v", err)
	}
	defer container.Terminate(ctx)
	uri, _ := container.ConnectionString(ctx)

	nc, _ := nats.Connect(uri)
	defer nc.Close()
	streams.EnsureStreamsWithReplicas(nc, 1)
	js, _ := nc.JetStream()

	mem := memory.NewMockMemoryClient()
	ev1 := envelope.Build("aegis/test", "aegis.tasks.audit", map[string]string{"x": "1"})
	mem.InsertOutboxEntry(ctx, memory.OutboxEntry{
		ID: "a1", Subject: "aegis.tasks.audit_seed", Payload: ev1.MustMarshal(),
		Status: "pending", AttemptCount: 0, NextRetryAt: time.Now().Add(-time.Second),
	})

	r := &OutboxRelay{JS: js, MemoryClient: mem, PollInterval: 10 * time.Millisecond}
	rCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	go r.Start(rCtx)
	time.Sleep(150 * time.Millisecond)

	logs, err := mem.ListAuditLogs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) < 1 {
		t.Errorf("expected audit log entries, got %d", len(logs))
	}
}
