package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"aegis-databus/internal/health"
	httpproxy "aegis-databus/internal/http"
	"aegis-databus/internal/relay"
	"aegis-databus/pkg/memory"
	"aegis-databus/pkg/security"
	"aegis-databus/pkg/streams"
	"aegis-databus/pkg/envelope"

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

	logger := log.New(os.Stdout, "databus ", log.LstdFlags)

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

	// Retry EnsureStreams — cluster may need time to form; JetStream Raft requires quorum.
	for attempt := 1; attempt <= 10; attempt++ {
		if err := streams.EnsureStreams(nc); err != nil {
			logger.Printf("ensure streams (attempt %d/10): %v", attempt, err)
			if attempt < 10 {
				time.Sleep(2 * time.Second)
				continue
			}
			logger.Fatalf("ensure streams failed after 10 attempts: %v", err)
		}
		elapsed := time.Since(startTime)
		logger.Printf("streams ready (startup %.2fs, NFR-DB-005 target < 3s)", elapsed.Seconds())
		break
	}

	// Memory client: HTTP if AEGIS_MEMORY_URL set, else Mock. FallbackClient handles DEGRADED-HOLD.
	memURL := os.Getenv("AEGIS_MEMORY_URL")
	mock := memory.NewMockMemoryClient()
	// Seed outbox for audit demo — relay will publish these so /audit shows metadata
	seedAuditDemo(ctx, mock)
	var mem memory.MemoryClient = mock
	if memURL != "" {
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

	// Health heartbeat
	hb := health.NewHeartbeat(nc, logger)
	go hb.Start(ctx)

	// HTTP endpoints: /metrics, /healthz, /audit
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/audit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		logs, err := mem.ListAuditLogs(r.Context(), 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.Encode(logs)
	})
	// Interface 4: /varz, /connz, /jsz — proxy to NATS monitoring
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
	defer srv.Shutdown(context.Background())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Println("shutting down")
	cancel()
}
