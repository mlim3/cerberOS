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

	mux := http.NewServeMux()

	injHandler := &inject.Handler{PP: pp, Auditor: auditor}
	injHandler.Register(mux)

	secHandler := &secrets.Handler{Manager: manager, Auditor: auditor}
	secHandler.Register(mux)

	hzHandler := &healthz.Handler{Auditor: auditor}
	hzHandler.Register(mux)

	execHandler := execute.New(manager, auditor)
	execHandler.Register(mux)

	httpSrv := &http.Server{Addr: ":8000", Handler: mux}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
		With("service", "vault", "component", "server")

	// Heartbeat emitter — non-fatal if NATS is unavailable.
	if natsURL := os.Getenv("NATS_URL"); natsURL != "" {
		nc, err := nats.Connect(natsURL,
			nats.Name("vault-heartbeat"),
			nats.RetryOnFailedConnect(true),
			nats.MaxReconnects(-1),
			nats.ReconnectWait(500*time.Millisecond),
		)
		if err != nil {
			logger.Warn("heartbeat: NATS connect failed — liveness will not be published", "error", err)
		} else {
			defer nc.Close()
			emitter := heartbeat.New(nc, "vault", logger)
			go emitter.Start(sigCtx)
		}
	} else {
		logger.Info("heartbeat: NATS_URL unset — emitter disabled")
	}

	go func() {
		<-sigCtx.Done()
		_ = httpSrv.Shutdown(context.Background())
	}()

	logger.Info("vault listening", "addr", ":8000")
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}
