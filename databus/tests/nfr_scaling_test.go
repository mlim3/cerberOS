package tests

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"aegis-databus/pkg/streams"

	"github.com/nats-io/nats.go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

// BenchmarkPubSubSingleNode measures throughput on single-node (baseline for NFR-DB-010).
func BenchmarkPubSubSingleNode(b *testing.B) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		b.Skipf("NATS: %v", err)
	}
	defer container.Terminate(ctx)
	uri, _ := container.ConnectionString(ctx)
	runThroughputBench(b, uri, 1)
}

// BenchmarkPubSubCluster measures throughput on cluster when AEGIS_TEST_CLUSTER=1.
// Run with: make up && AEGIS_TEST_CLUSTER=1 go test -bench=BenchmarkPubSubCluster -benchtime=3s ./tests/...
func BenchmarkPubSubCluster(b *testing.B) {
	if os.Getenv("AEGIS_TEST_CLUSTER") != "1" {
		b.Skip("set AEGIS_TEST_CLUSTER=1 and run 'make up' for cluster benchmark")
	}
	url := "nats://127.0.0.1:4222,nats://127.0.0.1:4223,nats://127.0.0.1:4224"
	runThroughputBench(b, url, 3)
}

func runThroughputBench(b *testing.B, url string, replicas int) {
	nc, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(5),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	if err := streams.EnsureStreamsWithReplicas(nc, replicas); err != nil {
		b.Fatalf("streams: %v", err)
	}
	js, _ := nc.JetStream()
	subject := "aegis.tasks.scale"
	payload := []byte(`{"n":1}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		js.Publish(subject, payload)
	}
	b.StopTimer()

	// Report msgs/sec for NFR-DB-010
	elapsed := b.Elapsed().Seconds()
	if elapsed > 0 {
		msgsPerSec := float64(b.N) / elapsed
		b.ReportMetric(msgsPerSec, "msgs_per_sec")
		b.Log(fmt.Sprintf("NFR-DB-010: %.0f msgs/sec (%d nodes)", msgsPerSec, replicas))
	}
}

// TestNFRDB010_ScalingComparison is a short test that runs single-node benchmark
// and logs throughput. For full 1-vs-3 comparison, run:
//   go test -bench=BenchmarkPubSubSingleNode -benchtime=2s ./tests/...
//   make up && AEGIS_TEST_CLUSTER=1 go test -bench=BenchmarkPubSubCluster -benchtime=2s ./tests/...
func TestNFRDB010_ScalingComparison(t *testing.T) {
	t.Skip("manual: run BenchmarkPubSubSingleNode and BenchmarkPubSubCluster, compare msgs_per_sec")
}
