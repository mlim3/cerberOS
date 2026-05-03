package tests

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"aegis-databus/pkg/streams"

	"github.com/nats-io/nats.go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

// TestNFRDB003_Availability exercises NFR-DB-003: sustained connectivity.
// Publishes every 500ms for N seconds, asserts no disconnects.
// Duration: NFR_AVAILABILITY_DURATION_SEC (default 60). 99.99% = ~8.6min downtime/year;
// this test is a sanity check; production measures over days.
func TestNFRDB003_Availability(t *testing.T) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		t.Skipf("NATS: %v", err)
	}
	defer container.Terminate(ctx)
	uri, _ := container.ConnectionString(ctx)

	nc, err := nats.Connect(uri,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(10),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	if err := streams.EnsureStreamsWithReplicas(nc, 1); err != nil {
		t.Fatalf("streams: %v", err)
	}
	js, _ := nc.JetStream()

	subject := "aegis.tasks.avail"
	var published, disconnected atomic.Int64
	nc.SetDisconnectErrHandler(func(_ *nats.Conn, _ error) {
		disconnected.Add(1)
	})

	durationSec := 60
	if s := os.Getenv("NFR_AVAILABILITY_DURATION_SEC"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			durationSec = n
		}
	}
	duration := time.Duration(durationSec) * time.Second
	interval := 500 * time.Millisecond
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if nc.Status() != nats.CONNECTED {
			t.Fatalf("disconnected during availability test")
		}
		_, err := js.Publish(subject, []byte(`{"n":1}`))
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
		published.Add(1)
		time.Sleep(interval)
	}

	if disconnected.Load() > 0 {
		t.Errorf("NFR-DB-003: saw %d disconnects during %v", disconnected.Load(), duration)
	}
	t.Logf("NFR-DB-003: %d publishes, 0 disconnects over %v", published.Load(), duration)
}
