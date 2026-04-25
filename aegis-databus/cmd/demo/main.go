// DEMO: Full EDD demo with all 6 components.
// Each component uses its own NATS connection so Grafana "Traffic by component" shows distinct names.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aegis-databus/pkg/security"
	"github.com/nats-io/nats.go"
)

const defaultNatsURL = "nats://127.0.0.1:4222"

func connectAs(ctx context.Context, connName, url string, logger *slog.Logger) (*nats.Conn, nats.JetStreamContext, error) {
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

	baseLogger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
		With("service", "databus")
	logger := baseLogger.With("component", "demo")
	url := os.Getenv("AEGIS_NATS_URL")
	if url == "" {
		url = defaultNatsURL
	}

	seed, _ := security.GetNKeyFromOpenBao(ctx, "aegis-demo")
	if seed != "" {
		logger.Info("connected with NKey", "mode", "zero-trust")
	}

	// Each component gets its own connection for distinct Grafana "Traffic by component" names.
	// acl is the subject ACL id (EDD §9.2); must match security.CheckPublish / CheckSubscribe.
	components := []struct {
		name string
		acl  string
		run  func(context.Context, *nats.Conn, nats.JetStreamContext, *slog.Logger, string)
	}{
		{"aegis-io", "io", runIO},
		{"aegis-orchestrator", "orchestrator", runOrchestrator},
		{"aegis-memory", "memory", runMemory},
		{"aegis-vault", "vault", runVault},
		{"aegis-agent", "agent", runAgent},
		{"aegis-monitoring", "monitoring", runMonitoring},
	}

	logger.Info("starting demo components", "count", len(components))
	for _, c := range components {
		connName, aclName, run := c.name, c.acl, c.run
		go func() {
			componentLogger := baseLogger.With("component", aclName)
			nc, js, err := connectAs(ctx, connName, url, componentLogger)
			if err != nil {
				componentLogger.Error("connect failed", "conn_name", connName, "error", err)
				return
			}
			defer nc.Close()
			componentLogger.Info("component connected", "conn_name", connName)
			run(ctx, nc, js, componentLogger, aclName)
		}()
	}
	time.Sleep(500 * time.Millisecond) // allow connections to establish

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Info("shutting down")
	cancel()
}
