package health

import (
	"context"
	"log"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

func TestNewHeartbeat(t *testing.T) {
	hb := NewHeartbeat(nil, nil)
	if hb == nil {
		t.Fatal("NewHeartbeat returned nil")
	}
	if hb.logger == nil {
		t.Error("logger should be set when nil passed")
	}

	hb2 := NewHeartbeat(nil, log.Default())
	if hb2.logger != log.Default() {
		t.Error("logger should use provided value")
	}
}

func TestHeartbeat_Start(t *testing.T) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		t.Skipf("NATS: %v", err)
	}
	defer container.Terminate(ctx)
	uri, _ := container.ConnectionString(ctx)

	nc, err := nats.Connect(uri)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	hb := NewHeartbeat(nc, log.New(nil, "", 0))
	runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		hb.Start(runCtx)
		close(done)
	}()
	select {
	case <-done:
		// Start exited on context cancel
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not exit within 3s")
	}
}
