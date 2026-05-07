package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"aegis-databus/internal/dlq"
	"aegis-databus/internal/health"
	"aegis-databus/internal/heartbeat"
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
	defaultNatsURL     = "nats://127.0.0.1:4222"
	defaultNatsHTTPURL = "http://127.0.0.1:8222"
	metricsAddr        = ":9091"
	memoryPingInterval = 15 * time.Second
)

func main() {
	startTime := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTel, errInit := telemetry.Init(ctx)
	if errInit != nil {
		slog.New(slog.NewJSONHandler(os.Stdout, nil)).
			With("component", "databus", "module", "server").
			Error("telemetry init failed", "error", errInit)
		os.Exit(1)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTel(sctx)
	}()

	baseLogger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
		With("component", "databus")
	// Make component=databus the default for global slog calls (e.g. retry
	// helpers in pkg/bus). Without this, those records would be missing the
	// canonical component/module fields required by docs/logging.md.
	slog.SetDefault(baseLogger.With("module", "default"))
	logger := baseLogger.With("module", "server")
	httpLogger := baseLogger.With("module", "http")
	if telemetry.Enabled() {
		logger.Info("opentelemetry otlp exporter is enabled; spans will be shipped to the otel collector")
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
			logger.Error("could not connect to nats with nkey credential; databus cannot continue", "error", err, "nats_url", url)
			os.Exit(1)
		}
		if os.Getenv("OPENBAO_ADDR") != "" {
			logger.Info("connected to nats using nkey credential sourced from openbao", "nats_url", url)
		} else {
			logger.Info("connected to nats using nkey credential (zero trust mode)", "nats_url", url)
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
			logger.Error("could not connect to nats with non-nkey options; databus cannot continue", "error", err, "nats_url", url)
			os.Exit(1)
		}
	}
	defer nc.Close()

	// Listen on :9091 before EnsureStreams so Docker/curl get TCP + HTTP (503) instead of RST
	// while JetStream reconciles (streams_not_ready).
	var streamsReady atomic.Bool
	var mem memory.MemoryClient

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		reqLog := requestLogger(httpLogger, r, "health.check")
		start := time.Now()
		w.Header().Set("Content-Type", "application/json")
		if !streamsReady.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ok":false,"reason":"streams_not_ready"}`))
			reqLog.Warn("served databus healthz probe with not-ready response; jetstream streams have not finished reconciling",
				"status", http.StatusServiceUnavailable,
				"reason", "streams_not_ready",
				"elapsed_ms", time.Since(start).Milliseconds())
			return
		}
		w.Write([]byte(`{"ok":true}`))
		reqLog.Debug("served databus healthz probe with ok response",
			"status", http.StatusOK,
			"elapsed_ms", time.Since(start).Milliseconds())
	})
	mux.HandleFunc("/audit", func(w http.ResponseWriter, r *http.Request) {
		reqLog := requestLogger(httpLogger, r, "audit.list")
		start := time.Now()
		if r.Method != http.MethodGet {
			reqLog.Warn("rejected audit list request: method not allowed; only GET is accepted",
				"status", http.StatusMethodNotAllowed,
				"elapsed_ms", time.Since(start).Milliseconds())
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if mem == nil {
			reqLog.Warn("rejected audit list request: storage client not yet initialized; databus is still starting up",
				"status", http.StatusServiceUnavailable,
				"elapsed_ms", time.Since(start).Milliseconds())
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		logs, err := mem.ListAuditLogs(r.Context(), 50)
		if err != nil {
			reqLog.Error("could not load audit log entries from storage; returning 500 to caller",
				"status", http.StatusInternalServerError,
				"error", err,
				"elapsed_ms", time.Since(start).Milliseconds())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(logs)
		reqLog.Info("served audit log listing to caller; metadata only (payloads never returned)",
			"status", http.StatusOK,
			"entry_count", len(logs),
			"elapsed_ms", time.Since(start).Milliseconds())
	})
	natsHTTP := os.Getenv("AEGIS_NATS_HTTP_URL")
	if natsHTTP == "" {
		natsHTTP = defaultNatsHTTPURL
	}
	proxy := httpproxy.ProxyToNATSMonitoring(strings.TrimSuffix(natsHTTP, "/"))
	loggedProxy := func(opType string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			reqLog := requestLogger(httpLogger, r, opType)
			reqLog.Debug("proxying nats monitoring request to upstream nats http endpoint",
				"upstream", natsHTTP)
			proxy(w, r)
		}
	}
	mux.HandleFunc("/varz", loggedProxy("nats.monitoring.varz"))
	mux.HandleFunc("/connz", loggedProxy("nats.monitoring.connz"))
	mux.HandleFunc("/jsz", loggedProxy("nats.monitoring.jsz"))

	srv := &http.Server{Addr: metricsAddr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Warn("databus http server stopped unexpectedly; metrics, healthz, and audit endpoints are no longer reachable", "error", err)
		}
	}()
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()
	logger.Info("databus http server starting; serving metrics, healthz, audit, and nats monitoring proxy",
		"addr", ":9091",
		"note", "healthz returns 503 until jetstream streams are ready")

	// Retry EnsureStreams — cluster may need time to form; JetStream Raft requires quorum.
	// AEGIS_NATS_REPLICAS defaults to 1 (single-node); set to 3 for a NATS cluster.
	natsReplicas := 1
	if v := os.Getenv("AEGIS_NATS_REPLICAS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			natsReplicas = n
		}
	}
	const ensureMaxAttempts = 25
	for attempt := 1; attempt <= ensureMaxAttempts; attempt++ {
		if err := streams.EnsureStreamsWithReplicas(nc, natsReplicas); err != nil {
			logger.Warn("could not ensure jetstream streams; will retry until quorum forms",
				"attempt", attempt, "max_attempts", ensureMaxAttempts, "error", err)
			if attempt < ensureMaxAttempts {
				time.Sleep(3 * time.Second)
				continue
			}
			logger.Error("could not ensure jetstream streams after max attempts; databus cannot continue",
				"max_attempts", ensureMaxAttempts, "error", err)
			os.Exit(1)
		}
		elapsed := time.Since(startTime)
		logger.Info("jetstream streams reconciled and ready; databus will now flip healthz to 200 ok",
			"startup_seconds", elapsed.Seconds())
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
			logger.Warn("orchestrator proxy unavailable, DEGRADED-HOLD active (using mock)")
		}
		fc.StartHealthCheck(ctx, memoryPingInterval)
		mem = fc
		logger.Info("storage via orchestrator proxy")
	} else if memURL := os.Getenv("AEGIS_MEMORY_URL"); memURL != "" {
		primary := memory.NewHTTPClient(memURL)
		fc := memory.NewFallbackClient(primary, mock)
		fc.Init(ctx)
		if fc.Degraded() {
			logger.Warn("memory API unavailable, DEGRADED-HOLD active (using mock)")
		}
		fc.StartHealthCheck(ctx, memoryPingInterval)
		mem = fc
	}

	// Outbox relay
	js, _ := nc.JetStream()
	relay := &relay.OutboxRelay{
		JS:           js,
		MemoryClient: mem,
		Logger:       baseLogger.With("module", "outbox-relay"),
	}
	go relay.Start(ctx)

	// DLQ replay (optional): check-before-republish to avoid duplicates when upstream already succeeded
	var checker memory.IdempotencyChecker
	if c, ok := mem.(memory.IdempotencyChecker); ok {
		checker = c
	}
	if os.Getenv("AEGIS_DLQ_REPLAY_ENABLED") == "1" {
		rh := &dlq.ReplayHandler{JS: js, Checker: checker, Logger: baseLogger.With("module", "dlq-replay"), Component: "aegis-databus"}
		go rh.Start(ctx)
		logger.Info("dlq replay handler started; will pull dlq messages, check idempotency, and republish on success")
	}

	// Health heartbeat (legacy subject aegis.health.databus).
	hb := health.NewHeartbeat(nc, baseLogger.With("module", "health-heartbeat"))
	go hb.Start(ctx)

	// Standardized service heartbeat (aegis.heartbeat.service.databus).
	// See docs/heartbeat.md. This is what the orchestrator's sweeper
	// subscribes to.
	serviceHB := heartbeat.New(nc, "databus", logger)
	go serviceHB.Start(ctx)

	// JetStream gauges for Grafana (stream messages, bytes, pending)
	go jetstreammetrics.Start(ctx, nc, jetstreammetrics.DefaultPollInterval, baseLogger.With("module", "jetstream-metrics"))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Info("databus received shutdown signal; cancelling background goroutines and closing nats connection")
	cancel()
}

// requestLogger derives a per-request logger annotated with canonical
// correlation fields. It accepts an X-Request-ID header from the caller and
// generates a fresh one if missing.
func requestLogger(base *slog.Logger, r *http.Request, operationType string) *slog.Logger {
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err == nil {
			requestID = hex.EncodeToString(b[:])
		} else {
			requestID = "req-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		}
	}
	logger := base.With(
		"request_id", requestID,
		"operation_type", operationType,
		"method", r.Method,
		"path", r.URL.Path,
	)
	if traceID := r.Header.Get("X-Trace-ID"); traceID != "" {
		logger = logger.With("trace_id", traceID)
	}
	return logger
}
