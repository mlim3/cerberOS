// DEMO: Full EDD demo with all 6 components.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
)

const defaultNatsURL = "nats://127.0.0.1:4222"

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := log.New(os.Stdout, "demo ", log.LstdFlags)
	url := os.Getenv("AEGIS_NATS_URL")
	if url == "" {
		url = defaultNatsURL
	}

	nc, err := nats.Connect(url,
		nats.Name("aegis-demo"),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		logger.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		logger.Fatalf("jetstream: %v", err)
	}

	logger.Println("Starting 6 components: I/O, Orchestrator, Memory, Vault, Agent, Monitoring")
	go runIO(ctx, nc, js, logger)
	go runOrchestrator(ctx, nc, js, logger)
	go runMemory(ctx, nc, js, logger)
	go runVault(ctx, nc, js, logger)
	go runAgent(ctx, nc, js, logger)
	go runMonitoring(ctx, nc, js, logger)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Println("shutting down")
	cancel()
}
