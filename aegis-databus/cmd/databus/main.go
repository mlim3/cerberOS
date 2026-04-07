package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"aegis-databus/internal/dlq"
	"aegis-databus/internal/health"
	httpproxy "aegis-databus/internal/http"
	"aegis-databus/internal/jetstreammetrics"
	"aegis-databus/internal/relay"
	"aegis-databus/pkg/envelope"
	"aegis-databus/pkg/memory"
	"aegis-databus/pkg/security"
	"aegis-databus/pkg/streams"
	"aegis-databus/pkg/telemetry"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// seedAuditDemo inserts pending outbox entries so the relay publishes them and /audit shows metadata.
func seedAuditDemo(ctx context.Context, m *memory.MockMemoryClient) {
	ev1 := envelope.Build("aegis/databus-demo", "aegis.tasks.audit_seed", map[string]string{"demo": "audit"})
	ev2 := envelope.Build("aegis/databus-demo", "aegis.memory.audit_seed", map[string]string{"demo": "audit"})
	now := time.Now().UTC()
	subjects := []string{"aegis.tasks.audit_seed", "aegis.memory.audit_seed"}
	m.InsertOutboxEntry(ctx, memory.OutboxEntry{
		ID:           "audit-demo-1",
		Subject:      subjects[0],
		Payload:      ev1.MustMarshal(),
		Status:       "pending",
		AttemptCount: 0,
		NextRetryAt:  now.Add(-time.Second),
		CreatedAt:    now,
	})
	m.InsertOutboxEntry(ctx, memory.OutboxEntry{
		ID:           "audit-demo-2",
		Subject:      subjects[1],
		Payload:      ev2.MustMarshal(),
		Status:       "pending",
		AttemptCount: 0,
		NextRetryAt:  now.Add(-time.Second),
		CreatedAt:    now,
	})
}

const (
	defaultNatsURL      = "nats://127.0.0.1:4222"
	defaultNatsHTTPURL  = "http://127.0.0.1:8222"
	metricsAddr         = ":9091"
	memoryPingInterval  = 15 * time.Second
)

func main() {
	startTime := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTel, errInit := telemetry.Init(ctx)
	if errInit != nil {
		log.Fatalf("telemetry: %v", errInit)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTel(sctx)
	}()

	logger := log.New(os.Stdout, "databus ", log.LstdFlags)
	if telemetry.Enabled() {
		logger.Println("OpenTelemetry OTLP export enabled")
	}

	url := os.Getenv("AEGIS_NATS_URL")
	if url == "" {
		url = defaultNatsURL
	}

	var nc *nats.Conn
	var err error
	// SR-DB-004: NKey from OpenBao (OPENBAO_ADDR) or env (AEGIS_NKEY_SEED)
	seed, seedErr := security.GetNKeyFromOpenBao(ctx, "databus")
	if seedErr == nil && seed != "" {
		nc, err = security.NewConnectionWithNKeySeed(url, seed)
		if err != nil {
			logger.Fatalf("connect (NKey): %v", err)
		}
		if os.Getenv("OPENBAO_ADDR") != "" {
			logger.Println("connected with NKey from OpenBao (SR-DB-004)")
		} else {
			logger.Println("connected with NKey (Zero Trust)")
		}
	} else {
		opts := []nats.Option{
			nats.Name("aegis-databus"),
			nats.RetryOnFailedConnect(true),
			nats.MaxReconnects(-1),
			nats.ReconnectWait(500 * time.Millisecond),
		}
		if cfg, _ := security.TLSConfigFromEnv(); cfg != nil {
			opts = append(opts, nats.Secure(cfg))
		}
		nc, err = nats.Connect(url, opts...)
		if err != nil {
			logger.Fatalf("connect: %v", err)
		}
	}
	defer nc.Close()

	// Listen on :9091 before EnsureStreams so Docker/curl get TCP + HTTP (503) instead of RST
	// while JetStream reconciles (streams_not_ready).
	var streamsReady atomic.Bool
	var mem memory.MemoryClient

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !streamsReady.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ok":false,"reason":"streams_not_ready"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/audit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if mem == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		logs, err := mem.ListAuditLogs(r.Context(), 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(logs)
	})
	natsHTTP := os.Getenv("AEGIS_NATS_HTTP_URL")
	if natsHTTP == "" {
		natsHTTP = defaultNatsHTTPURL
	}
	proxy := httpproxy.ProxyToNATSMonitoring(strings.TrimSuffix(natsHTTP, "/"))
	mux.HandleFunc("/varz", proxy)
	mux.HandleFunc("/connz", proxy)
	mux.HandleFunc("/jsz", proxy)

	srv := &http.Server{Addr: metricsAddr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Printf("metrics server: %v", err)
		}
	}()
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()
	logger.Println("HTTP listening on :9091 (healthz=503 until JetStream streams ready)")

	// Retry EnsureStreams — cluster may need time to form; JetStream Raft requires quorum.
	const ensureMaxAttempts = 25
	for attempt := 1; attempt <= ensureMaxAttempts; attempt++ {
		if err := streams.EnsureStreams(nc); err != nil {
			logger.Printf("ensure streams (attempt %d/%d): %v", attempt, ensureMaxAttempts, err)
			if attempt < ensureMaxAttempts {
				time.Sleep(3 * time.Second)
				continue
			}
			logger.Fatalf("ensure streams failed after %d attempts: %v", ensureMaxAttempts, err)
		}
		elapsed := time.Since(startTime)
		logger.Printf("streams ready (startup %.2fs, NFR-DB-005 target < 3s)", elapsed.Seconds())
		break
	}
	streamsReady.Store(true)

	// Storage client: Option 1 architecture — DataBus → Orchestrator → Memory.
	// AEGIS_ORCHESTRATOR_URL: use Orchestrator proxy (simulate full chain with stubs).
	// AEGIS_MEMORY_URL: legacy direct Memory (for backward compat).
	// Else: Mock only (local demo).
	mock := memory.NewMockMemoryClient()
	seedAuditDemo(ctx, mock)
	mem = mock
	if orchURL := os.Getenv("AEGIS_ORCHESTRATOR_URL"); orchURL != "" {
		primary := memory.NewOrchestratorStorageClient(orchURL)
		fc := memory.NewFallbackClient(primary, mock)
		fc.Init(ctx)
		if fc.Degraded() {
			logger.Println("Orchestrator proxy unavailable, DEGRADED-HOLD active (using mock)")
		}
		fc.StartHealthCheck(ctx, memoryPingInterval)
		mem = fc
		logger.Println("storage via Orchestrator proxy (DataBus → Orchestrator → Memory)")
	} else if memURL := os.Getenv("AEGIS_MEMORY_URL"); memURL != "" {
		primary := memory.NewHTTPClient(memURL)
		fc := memory.NewFallbackClient(primary, mock)
		fc.Init(ctx)
		if fc.Degraded() {
			logger.Println("Memory API unavailable, DEGRADED-HOLD active (using mock)")
		}
		fc.StartHealthCheck(ctx, memoryPingInterval)
		mem = fc
	}

	// Outbox relay
	js, _ := nc.JetStream()
	relay := &relay.OutboxRelay{
		JS:           js,
		MemoryClient: mem,
		Logger:       logger,
	}
	go relay.Start(ctx)

	// DLQ replay (optional): check-before-republish to avoid duplicates when upstream already succeeded
	var checker memory.IdempotencyChecker
	if c, ok := mem.(memory.IdempotencyChecker); ok {
		checker = c
	}
	if os.Getenv("AEGIS_DLQ_REPLAY_ENABLED") == "1" {
		rh := &dlq.ReplayHandler{JS: js, Checker: checker, Logger: logger, Component: "aegis-databus"}
		go rh.Start(ctx)
		logger.Println("DLQ replay handler started (idempotency check before republish)")
	}

	// Health heartbeat
	hb := health.NewHeartbeat(nc, logger)
	go hb.Start(ctx)

	// JetStream gauges for Grafana (stream messages, bytes, pending)
	go jetstreammetrics.Start(ctx, nc, jetstreammetrics.DefaultPollInterval, logger)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Println("shutting down")
	cancel()
}
