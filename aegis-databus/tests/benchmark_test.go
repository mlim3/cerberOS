package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"aegis-databus/pkg/streams"

	"github.com/nats-io/nats.go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

// BenchmarkPubSub measures publish throughput (NFR-DB-001: 50K msgs/sec target).
func BenchmarkPubSub(b *testing.B) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		b.Skipf("NATS container: %v", err)
	}
	defer container.Terminate(ctx)
	uri, _ := container.ConnectionString(ctx)

	nc, err := nats.Connect(uri)
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	if err := streams.EnsureStreamsWithReplicas(nc, 1); err != nil {
		b.Fatalf("streams: %v", err)
	}
	js, _ := nc.JetStream()
	subject := "aegis.tasks.bench"
	payload := []byte(`{"n":1}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		js.Publish(subject, payload)
	}
	b.StopTimer()
}

// BenchmarkPubSubLatency measures P99 latency (NFR-DB-002: < 5ms target).
func BenchmarkPubSubLatency(b *testing.B) {
	ctx := context.Background()
	container, err := tcnats.RunContainer(ctx)
	if err != nil {
		b.Skipf("NATS container: %v", err)
	}
	defer container.Terminate(ctx)
	uri, _ := container.ConnectionString(ctx)

	nc, err := nats.Connect(uri)
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	if err := streams.EnsureStreamsWithReplicas(nc, 1); err != nil {
		b.Fatalf("streams: %v", err)
	}
	js, _ := nc.JetStream()
	subject := "aegis.tasks.latency"
	recv := make(chan struct{}, 1)
	js.Subscribe(subject, func(m *nats.Msg) { recv <- struct{}{}; m.Ack() })
	time.Sleep(50 * time.Millisecond)

	latencies := make([]time.Duration, 0, b.N)
	for i := 0; i < b.N; i++ {
		start := time.Now()
		js.Publish(subject, []byte(fmt.Sprintf(`{"n":%d}`, i)))
		select {
		case <-recv:
			latencies = append(latencies, time.Since(start))
		case <-time.After(100 * time.Millisecond):
			b.Logf("timeout at i=%d", i)
		}
	}
	if len(latencies) > 0 {
		sortDurations(latencies)
		p99Idx := int(float64(len(latencies)) * 0.99)
		if p99Idx >= len(latencies) {
			p99Idx = len(latencies) - 1
		}
		p99 := latencies[p99Idx]
		b.ReportMetric(float64(p99.Nanoseconds())/1e6, "ms_P99")
	}
}
