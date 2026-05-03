package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"aegis-databus/pkg/streams"

	"github.com/nats-io/nats.go"
)

// aegisDatabusDir is the aegis-databus module root (parent of tests/).
func aegisDatabusDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
}

// TestFRDB007_ClusterFailover verifies FR-DB-007: 3-node cluster, node failure,
// leader election < 5s, no message loss.
//
// Prerequisites: make up (3-node NATS cluster running)
// Run with: AEGIS_TEST_CLUSTER=1 go test -v -run TestFRDB007 ./tests/...
func TestFRDB007_ClusterFailover(t *testing.T) {
	if os.Getenv("AEGIS_TEST_CLUSTER") != "1" {
		t.Skip("set AEGIS_TEST_CLUSTER=1 and run 'make up' to run cluster failover test")
	}

	// Use all 3 cluster nodes so we can reconnect when nats-1 is killed
	url := "nats://127.0.0.1:4222,nats://127.0.0.1:4223,nats://127.0.0.1:4224"
	nc, err := nats.Connect(url,
		nats.Name("cluster-failover-test"),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(10),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		t.Skipf("cluster not available: %v", err)
	}
	defer nc.Close()

	if err := streams.EnsureStreamsWithReplicas(nc, 3); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}

	subject := "aegis.tasks.failover_test"
	var recvCount atomic.Int64

	sub, err := js.Subscribe(subject, func(m *nats.Msg) {
		recvCount.Add(1)
		m.Ack()
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	time.Sleep(200 * time.Millisecond)

	// Phase 1: publish 50 messages, wait for all
	for i := 0; i < 50; i++ {
		payload := []byte(fmt.Sprintf(`{"specversion":"1.0","id":"f1-%d","source":"test","type":"test","n":%d}`, i, i))
		if _, err := js.Publish(subject, payload); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for recvCount.Load() < 50 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if recvCount.Load() < 50 {
		t.Fatalf("phase 1: received %d, want 50", recvCount.Load())
	}

	// Phase 2: stop nats-1 service, measure reconnect time (FR-DB-007: < 5s)
	root := aegisDatabusDir(t)
	defer func() {
		start := exec.Command("docker", "compose", "-f", "docker-compose.yml", "start", "nats-1")
		start.Dir = root
		start.Env = os.Environ()
		if err := start.Run(); err != nil {
			t.Logf("docker compose start nats-1: %v", err)
		}
	}()
	stop := exec.Command("docker", "compose", "-f", "docker-compose.yml", "stop", "nats-1")
	stop.Dir = root
	stop.Env = os.Environ()
	t0 := time.Now()
	if err := stop.Run(); err != nil {
		t.Logf("docker compose stop nats-1: %v (set COMPOSE_PROJECT_NAME to match your stack)", err)
		t.Skip("cannot stop nats-1; run manually: docker compose -f docker-compose.yml stop nats-1")
	}

	// Wait for reconnect (client reconnects to nats-2 or nats-3)
	deadline = time.Now().Add(10 * time.Second)
	for nc.Status() != nats.CONNECTED && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	t1 := time.Now()
	if nc.Status() != nats.CONNECTED {
		t.Fatalf("did not reconnect within 10s")
	}
	elapsed := t1.Sub(t0)
	if elapsed > 5*time.Second {
		t.Errorf("FR-DB-007: reconnect took %v, want < 5s", elapsed)
	}
	t.Logf("FR-DB-007: reconnect elapsed %v", elapsed)

	// Phase 3: publish 50 more, verify total 100 (no message loss)
	time.Sleep(500 * time.Millisecond)
	for i := 50; i < 100; i++ {
		payload := []byte(fmt.Sprintf(`{"specversion":"1.0","id":"f2-%d","source":"test","type":"test","n":%d}`, i, i))
		if _, err := js.Publish(subject, payload); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	deadline = time.Now().Add(15 * time.Second)
	for recvCount.Load() < 100 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if recvCount.Load() < 100 {
		t.Errorf("FR-DB-007: message loss: received %d, want 100", recvCount.Load())
	}
}

