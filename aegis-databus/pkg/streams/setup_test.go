package streams

import (
	"context"
	"testing"

	"aegis-databus/pkg/bus"

	"github.com/nats-io/nats.go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

func TestEnsureStreamsWithReplicas(t *testing.T) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		t.Skipf("NATS: %v", err)
	}
	defer container.Terminate(ctx)
	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	nc, err := nats.Connect(uri)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	if err := EnsureStreamsWithReplicas(nc, 1); err != nil {
		t.Fatalf("EnsureStreamsWithReplicas: %v", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}

	streams := []string{
		bus.StreamTasks, bus.StreamUI, bus.StreamAgents,
		bus.StreamRuntime, bus.StreamVault, bus.StreamMemory,
		bus.StreamMonitoring, bus.StreamDLQ,
		bus.StreamCapabilityTransient, bus.StreamVaultProgressTransient,
	}
	for _, name := range streams {
		info, err := js.StreamInfo(name)
		if err != nil {
			t.Errorf("StreamInfo(%s): %v", name, err)
			continue
		}
		if info.Config.Name != name {
			t.Errorf("stream %s: config name %q", name, info.Config.Name)
		}
	}
}
