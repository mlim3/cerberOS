package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aegis-databus/internal/health"
	"aegis-databus/internal/relay"
	"aegis-databus/pkg/memory"
	"aegis-databus/pkg/streams"
	"github.com/nats-io/nats.go"
)

const (
	defaultNatsURL = "nats://127.0.0.1:4222"
	metricsAddr    = ":9091"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := log.New(os.Stdout, "databus ", log.LstdFlags)

	url := os.Getenv("AEGIS_NATS_URL")
	if url == "" {
		url = defaultNatsURL
	}

	nc, err := nats.Connect(url,
		nats.Name("aegis-databus"),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		logger.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	if err := streams.EnsureStreams(nc); err != nil {
		logger.Fatalf("ensure streams: %v", err)
	}
	logger.Println("streams ready")

	mem := memory.NewMockMemoryClient()

	// Outbox relay
	js, _ := nc.JetStream()
	relay := &relay.OutboxRelay{
		JS:           js,
		MemoryClient: mem,
		Logger:       logger,
	}
	go relay.Start(ctx)

	// Health heartbeat
	hb := health.NewHeartbeat(nc, logger)
	go hb.Start(ctx)

	// Metrics endpoint
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("# Aegis DataBus metrics placeholder\n"))
	})
	srv := &http.Server{Addr: metricsAddr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Printf("metrics server: %v", err)
		}
	}()
	defer srv.Shutdown(context.Background())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Println("shutting down")
	cancel()
}
