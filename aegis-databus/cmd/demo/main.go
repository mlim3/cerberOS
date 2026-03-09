// DEMO: Full EDD demo with all 6 components.
// Each component uses its own NATS connection so Grafana "Traffic by component" shows distinct names.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aegis-databus/pkg/security"
	"github.com/nats-io/nats.go"
)

const defaultNatsURL = "nats://127.0.0.1:4222"

func connectAs(ctx context.Context, connName, url string, logger *log.Logger) (*nats.Conn, nats.JetStreamContext, error) {
	seed, _ := security.GetNKeyFromOpenBao(ctx, "aegis-demo")
	if seed != "" {
		nc, err := security.NewConnectionWithNKeySeedAndName(url, seed, connName)
		if err != nil {
			return nil, nil, err
		}
		js, err := nc.JetStream()
		if err != nil {
			nc.Close()
			return nil, nil, err
		}
		return nc, js, nil
	}
	opts := []nats.Option{
		nats.Name(connName),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500 * time.Millisecond),
	}
	if cfg, _ := security.TLSConfigFromEnv(); cfg != nil {
		opts = append(opts, nats.Secure(cfg))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, nil, err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, nil, err
	}
	return nc, js, nil
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := log.New(os.Stdout, "demo ", log.LstdFlags)
	url := os.Getenv("AEGIS_NATS_URL")
	if url == "" {
		url = defaultNatsURL
	}

	seed, _ := security.GetNKeyFromOpenBao(ctx, "aegis-demo")
	if seed != "" {
		logger.Println("connected with NKey (Zero Trust)")
	}

	// Each component gets its own connection for distinct Grafana "Traffic by component" names
	components := []struct {
		name string
		run  func(context.Context, *nats.Conn, nats.JetStreamContext, *log.Logger)
	}{
		{"aegis-io", runIO},
		{"aegis-orchestrator", runOrchestrator},
		{"aegis-memory", runMemory},
		{"aegis-vault", runVault},
		{"aegis-agent", runAgent},
		{"aegis-monitoring", runMonitoring},
	}

	logger.Println("Starting 6 components: I/O, Orchestrator, Memory, Vault, Agent, Monitoring")
	for _, c := range components {
		connName, run := c.name, c.run
		go func() {
			nc, js, err := connectAs(ctx, connName, url, logger)
			if err != nil {
				logger.Printf("[%s] connect failed: %v", connName, err)
				return
			}
			defer nc.Close()
			run(ctx, nc, js, logger)
		}()
	}
	time.Sleep(500 * time.Millisecond) // allow connections to establish

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Println("shutting down")
	cancel()
}
