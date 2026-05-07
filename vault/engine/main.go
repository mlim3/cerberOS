package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/credentials"
	"github.com/mlim3/cerberOS/vault/engine/handlers/execute"
	"github.com/mlim3/cerberOS/vault/engine/handlers/healthz"
	"github.com/mlim3/cerberOS/vault/engine/handlers/inject"
	"github.com/mlim3/cerberOS/vault/engine/handlers/secrets"
	"github.com/mlim3/cerberOS/vault/engine/heartbeat"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

func main() {
	auditor := audit.New(audit.NewJSONExporter(os.Stdout))

	manager := secretmanager.NewOpenBaoSecretManager(auditor)
	pp := preprocessor.New(manager, auditor)

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
		With("component", "vault", "module", "server")
	httpLogger := logger.With("module", "http")

	mux := http.NewServeMux()

	injHandler := &inject.Handler{PP: pp, Auditor: auditor, Logger: httpLogger}
	injHandler.Register(mux)

	secHandler := &secrets.Handler{Manager: manager, Auditor: auditor, Logger: httpLogger}
	secHandler.Register(mux)

	credHandler := &credentials.Handler{Manager: manager, Auditor: auditor, Logger: httpLogger}
	credHandler.Register(mux)

	execHandler := &execute.Handler{Manager: manager, Auditor: auditor, Logger: httpLogger}
	execHandler.Register(mux)

	hzHandler := &healthz.Handler{Auditor: auditor, Logger: httpLogger}
	hzHandler.Register(mux)

	httpSrv := &http.Server{Addr: ":8000", Handler: mux}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Heartbeat emitter — non-fatal if NATS is unavailable.
	if natsURL := os.Getenv("NATS_URL"); natsURL != "" {
		nc, err := nats.Connect(natsURL,
			nats.Name("vault-heartbeat"),
			nats.RetryOnFailedConnect(true),
			nats.MaxReconnects(-1),
			nats.ReconnectWait(500*time.Millisecond),
		)
		if err != nil {
			logger.Warn("could not connect to nats for heartbeat emitter; vault will run without publishing liveness", "error", err)
		} else {
			defer nc.Close()
			emitter := heartbeat.New(nc, "vault", logger)
			go emitter.Start(sigCtx)
		}
	} else {
		logger.Info("nats_url not configured; heartbeat emitter disabled (vault will not publish liveness)")
	}

	go func() {
		<-sigCtx.Done()
		_ = httpSrv.Shutdown(context.Background())
	}()

	logger.Info("vault http server starting; ready to accept secret/credential/execute/inject requests", "addr", ":8000")
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("vault http server stopped unexpectedly; process will exit", "error", err)
		os.Exit(1)
	}
	logger.Info("vault http server stopped cleanly after shutdown signal")
}
