package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/cerberOS/agents-component/config"
	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/internal/credentials"
	"github.com/cerberOS/agents-component/internal/factory"
	"github.com/cerberOS/agents-component/internal/lifecycle"
	"github.com/cerberOS/agents-component/internal/memory"
	"github.com/cerberOS/agents-component/internal/metrics"
	"github.com/cerberOS/agents-component/internal/registry"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/internal/skillsconfig"
	"github.com/cerberOS/agents-component/internal/telemetry"
	"github.com/cerberOS/agents-component/pkg/types"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
		With("service", "agents", "component", "aegis-agents")

	cfg, err := config.Load()
	if err != nil {
		log.Error("config load failed", "error", err)
		os.Exit(1)
	}

	log.Info("starting aegis-agents",
		"component_id", cfg.ComponentID,
		"nats_url", cfg.NATSURL,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// OTLP tracing — no-op when OTEL_EXPORTER_OTLP_ENDPOINT is unset so local
	// dev without Tempo is unaffected. When enabled, spans started from
	// incoming TaskSpec.TraceID continue the trace from IO / Orchestrator.
	traceShutdown, err := telemetry.Init(ctx)
	if err != nil {
		log.Warn("telemetry init failed — continuing without traces", "error", err)
	} else if telemetry.Enabled() {
		log.Info("telemetry initialized", "endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	}
	defer func() {
		if traceShutdown != nil {
			_ = traceShutdown(context.Background())
		}
	}()

	// SIGHUP triggers a hot-reload of the skill configuration. The reload runs
	// in a dedicated goroutine so the signal is never missed even if a previous
	// reload is still running. The goroutine exits when the component shuts down.
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	defer signal.Stop(sighupCh)

	// Prometheus metrics recorder — wired into registry, skills, factory, and comms.
	rec := metrics.NewRecorder()

	// Start the /metrics HTTP server in a background goroutine.
	metricsAddr := fmt.Sprintf(":%d", cfg.MetricsPort)
	mux := http.NewServeMux()
	mux.Handle("/metrics", rec.Handler())
	metricsSrv := &http.Server{Addr: metricsAddr, Handler: mux}
	go func() {
		log.Info("metrics server listening", "addr", metricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server error", "error", err)
		}
	}()
	defer func() {
		if err := metricsSrv.Shutdown(context.Background()); err != nil {
			log.Warn("metrics server shutdown error", "error", err)
		}
	}()

	// Wire NATS JetStream client and wrap with metrics instrumentation.
	rawComms, err := comms.NewNATSClient(cfg.NATSURL, cfg.ComponentID,
		comms.WithMaxDeliver(cfg.CommsMaxDeliver),
	)
	if err != nil {
		log.Error("comms init failed", "error", err)
		os.Exit(1)
	}
	commsClient := metrics.WrapComms(rawComms, rec)

	// Provision the two JetStream streams this component publishes to and
	// consumes from. In a full deployment the Orchestrator owns stream creation;
	// in standalone / dev / CI environments (like this Docker Compose setup)
	// no Orchestrator is running, so we create them ourselves. The call is
	// idempotent and safe to run on every startup.
	if err := commsClient.EnsureStreams(); err != nil {
		log.Error("stream provisioning failed", "error", err)
		os.Exit(1)
	}

	reg := registry.New(registry.WithStateChangeHook(rec.ObserveStateChange))

	// Wire the embedding model. If VOYAGE_API_KEY is present, use the Voyage AI
	// production embedder (voyage-3-lite or a custom model from config). Otherwise
	// fall back to the default feature-hashing embedder — sufficient for
	// development and integration tests but not for production similarity quality.
	skillOpts := []skills.Option{skills.WithGetSpecHook(rec.ObserveSkillInvocation)}
	if voyageAPIKey := os.Getenv("VOYAGE_API_KEY"); voyageAPIKey != "" {
		log.Info("embedding: using Voyage AI model", "model", cfg.EmbeddingModel)
		skillOpts = append(skillOpts, skills.WithEmbedder(newVoyageEmbedder(voyageAPIKey, cfg.EmbeddingModel)))
	} else {
		log.Warn("embedding: VOYAGE_API_KEY not set — using hash embedder (not for production)")
	}
	skillMgr := skills.New(skillOpts...)
	credBroker, err := credentials.NewNATSBroker(commsClient,
		credentials.WithBrokerConfig(credentials.BrokerConfig{
			MaxAttempts: cfg.CredAuthMaxAttempts,
			Timeout:     cfg.CredAuthTimeout,
			BaseBackoff: cfg.CredAuthBaseBackoff,
		}),
	)
	if err != nil {
		log.Error("credential broker init failed", "error", err)
		os.Exit(1)
	}
	memClient, err := memory.NewNATSClient(commsClient,
		memory.WithWriteUnavailableHook(func(dataType, requestID, reason string) {
			_ = commsClient.Publish(
				comms.SubjectError,
				comms.PublishOptions{
					MessageType:   comms.MsgTypeError,
					CorrelationID: requestID,
					Transient:     true,
				},
				map[string]interface{}{
					"error_code":    "MEMORY_UNAVAILABLE",
					"error_message": fmt.Sprintf("state.write failed: data_type=%s: %s", dataType, reason),
					"data_type":     dataType,
					"request_id":    requestID,
				},
			)
		}),
	)
	if err != nil {
		log.Error("memory client init failed", "error", err)
		os.Exit(1)
	}

	// Lifecycle manager selection (highest-priority wins):
	//   1. Firecracker microVMs — when AEGIS_FIRECRACKER_SOCKET_DIR is set (production).
	//   2. OS process manager  — when AEGIS_AGENT_PROCESS_PATH is set (local dev / CI).
	//   3. In-process stub     — fallback for unit tests; never use in production.
	var lifecycleMgr lifecycle.Manager
	if fcSocketDir := os.Getenv("AEGIS_FIRECRACKER_SOCKET_DIR"); fcSocketDir != "" {
		log.Info("lifecycle: using Firecracker microVM manager", "socket_dir", fcSocketDir)
		lifecycleMgr = lifecycle.NewFirecracker(fcSocketDir)
	} else if cfg.AgentProcessPath != "" {
		log.Info("lifecycle: using process manager", "binary", cfg.AgentProcessPath)
		lifecycleMgr = lifecycle.NewProcess(cfg.AgentProcessPath)
	} else {
		log.Warn("lifecycle: AEGIS_AGENT_PROCESS_PATH and AEGIS_FIRECRACKER_SOCKET_DIR not set — using in-process stub (not for production)")
		lifecycleMgr = lifecycle.New()
	}

	// Crash detector fires when an agent misses MaxMissed consecutive heartbeats.
	// f is set after factory.New below; the closure captures the pointer.
	var f *factory.Factory
	crashDetector := lifecycle.NewCrashDetector(
		lifecycle.HeartbeatConfig{
			Interval:  cfg.HeartbeatInterval,
			MaxMissed: cfg.HeartbeatMaxMissed,
		},
		func(agentID string) {
			log.Warn("agent crash detected — initiating recovery sequence",
				"agent_id", agentID,
			)
			if err := f.HandleCrash(agentID); err != nil {
				log.Error("crash recovery failed",
					"agent_id", agentID,
					"error", err,
				)
			}
		},
	)
	go crashDetector.Run(ctx)

	// Subscribe to agent heartbeats (core NATS, at-most-once).
	// Heartbeats are published directly by agent-process binaries on
	// aegis.heartbeat.<agent_id> and are not captured by any JetStream stream.
	if err := commsClient.Subscribe(
		comms.SubjectHeartbeatAll,
		func(msg *comms.Message) {
			// Extract agent_id from the subject (aegis.heartbeat.<agent_id>).
			agentID := strings.TrimPrefix(msg.Subject, comms.SubjectHeartbeatPrefix)
			if agentID == "" {
				return
			}
			crashDetector.RecordHeartbeat(agentID)
		},
	); err != nil {
		log.Error("subscribe heartbeat failed", "error", err)
		os.Exit(1)
	}

	loadSkills(cfg.SkillsConfigPath, skillMgr, log)

	// Start the SIGHUP hot-reload goroutine now that the skill manager is
	// initialised and the initial skill tree is loaded.
	go func() {
		for {
			select {
			case <-sighupCh:
				log.Info("SIGHUP received: reloading skill configuration",
					"path", cfg.SkillsConfigPath,
				)
				reloadSkills(cfg.SkillsConfigPath, skillMgr, log)
			case <-ctx.Done():
				return
			}
		}
	}()

	if cfg.IdleSuspendTimeout > 0 {
		log.Info("OQ-03/OQ-06 suspension enabled",
			"idle_suspend_timeout", cfg.IdleSuspendTimeout,
			"wake_latency_target", cfg.SuspendWakeLatencyTarget,
		)
	}

	// Load the permission policy when AEGIS_PERMISSION_POLICY_PATH is set.
	// When unset, the factory falls back to the legacy domain.credential stub
	// which is acceptable for local dev but must not run in production.
	var permPolicy *factory.PermissionPolicy
	if policyPath := os.Getenv("AEGIS_PERMISSION_POLICY_PATH"); policyPath != "" {
		pp, err := factory.LoadPermissionPolicy(policyPath)
		if err != nil {
			log.Error("permission policy load failed", "path", policyPath, "error", err)
			os.Exit(1)
		}
		permPolicy = pp
		log.Info("permission policy loaded", "path", policyPath)
	} else {
		log.Warn("AEGIS_PERMISSION_POLICY_PATH not set — using legacy domain.credential stub (not for production)")
	}

	f, err = factory.New(factory.Config{
		Registry:                 reg,
		Skills:                   skillMgr,
		Credentials:              credBroker,
		Lifecycle:                lifecycleMgr,
		Memory:                   memClient,
		Comms:                    commsClient,
		Log:                      log,
		CrashDetector:            crashDetector,
		MaxRetries:               cfg.MaxAgentRetries,
		Policy:                   permPolicy,
		IdleSuspendTimeout:       cfg.IdleSuspendTimeout,
		SuspendWakeLatencyTarget: cfg.SuspendWakeLatencyTarget,
		OnSpawn:                  func(_ string) { rec.ObserveLifecycleEvent("spawn") },
		OnTerminate:              func(_ string) { rec.ObserveLifecycleEvent("terminate") },
		OnRecover:                func(_ string) { rec.ObserveLifecycleEvent("recover") },
		OnSuspend:                func(_ string) { rec.ObserveLifecycleEvent("suspend") },
		OnWake:                   func(_ string) { rec.ObserveLifecycleEvent("wake") },
	})
	if err != nil {
		log.Error("factory init failed", "error", err)
		os.Exit(1)
	}

	f.StartIdleSweep(ctx)

	// Bounded-concurrency worker pool for inbound task dispatch. Without this,
	// the JetStream durable subscriber runs HandleTaskSpec (which includes
	// Firecracker provisioning — several seconds) inline before Ack'ing,
	// serializing planner dispatches across tasks. Under load the second
	// user's task sits in the consumer's pending queue long enough to hit
	// the overall task timeout, producing "Your task exceeded its time limit"
	// even though the orchestrator already published task_accepted.
	//
	// The semaphore caps concurrent provisions (each of which owns a
	// Firecracker VM) so we do not exhaust host resources. Tune with
	// AEGIS_AGENTS_MAX_CONCURRENT_TASKS (default 8). The NATS callback
	// blocks on the semaphore when all workers are busy — that is the
	// intended back-pressure path; JetStream holds the pending messages
	// until a slot frees up.
	maxConcurrentTasks := 8
	if v := strings.TrimSpace(os.Getenv("AEGIS_AGENTS_MAX_CONCURRENT_TASKS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxConcurrentTasks = n
		}
	}
	taskWorkerSlots := make(chan struct{}, maxConcurrentTasks)
	log.Info("task.inbound worker pool configured", "max_concurrent_tasks", maxConcurrentTasks)

	// Subscribe to inbound task assignments from the Orchestrator (at-least-once).
	// Handlers MUST call msg.Ack() on success or msg.Nak() on failure.
	if err := commsClient.SubscribeDurable(
		comms.SubjectTaskInbound,
		comms.ConsumerTaskInbound,
		func(msg *comms.Message) {
			var spec types.TaskSpec
			if err := json.Unmarshal(msg.Data, &spec); err != nil {
				log.Error("task.inbound unmarshal failed",
					"correlation_id", msg.CorrelationID,
					"error", err,
				)
				_ = msg.Nak()
				return
			}

			log.Info("msg.inbound",
				"topic", comms.SubjectTaskInbound,
				"message_type", msg.MessageType,
				"task_id", spec.TaskID,
				"correlation_id", msg.CorrelationID,
				"trace_id", spec.TraceID,
			)

			// Acquire a worker slot. Blocks when the pool is saturated,
			// which exerts back-pressure on JetStream without dropping work.
			taskWorkerSlots <- struct{}{}

			go func() {
				defer func() { <-taskWorkerSlots }()

				// Continue the incoming trace seeded on the NATS envelope. When
				// telemetry is disabled the span is a no-op. Span is created
				// inside the worker so concurrent tasks get distinct spans.
				spanCtx := telemetry.ContextWithTraceID(context.Background(), spec.TraceID)
				_, span := telemetry.Tracer().Start(spanCtx, "task.inbound")
				defer span.End()

				if err := f.HandleTaskSpec(&spec); err != nil {
					log.Error("HandleTaskSpec failed",
						"task_id", spec.TaskID,
						"error", err,
					)
					_ = msg.Nak()
					return
				}
				_ = msg.Ack()
			}()
		},
	); err != nil {
		log.Error("subscribe task.inbound failed", "error", err)
		os.Exit(1)
	}

	// Subscribe to vault.execute.result from the Orchestrator (at-least-once).
	// Completing a request removes it from the in-flight tracking set so it is not
	// flagged UNKNOWN if the agent later crashes for an unrelated reason.
	if err := commsClient.SubscribeDurable(
		comms.SubjectVaultExecuteResult,
		comms.ConsumerVaultExecuteResult,
		func(msg *comms.Message) {
			var result types.VaultOperationResult
			if err := json.Unmarshal(msg.Data, &result); err != nil {
				log.Error("vault.execute.result unmarshal failed",
					"correlation_id", msg.CorrelationID,
					"error", err,
				)
				_ = msg.Nak()
				return
			}
			log.Info("msg.inbound",
				"topic", comms.SubjectVaultExecuteResult,
				"message_type", msg.MessageType,
				"agent_id", result.AgentID,
				"request_id", result.RequestID,
				"correlation_id", msg.CorrelationID,
			)
			f.CompleteVaultRequest(result.AgentID, result.RequestID)
			_ = msg.Ack()
		},
	); err != nil {
		log.Error("subscribe vault.execute.result failed", "error", err)
		os.Exit(1)
	}

	// Subscribe to capability queries from the Orchestrator (at-most-once).
	if err := commsClient.Subscribe(
		comms.SubjectCapabilityQuery,
		func(msg *comms.Message) {
			var query types.CapabilityQuery
			if err := json.Unmarshal(msg.Data, &query); err != nil {
				log.Error("capability.query unmarshal failed", "error", err)
				return
			}

			log.Info("msg.inbound",
				"topic", comms.SubjectCapabilityQuery,
				"message_type", msg.MessageType,
				"query_id", query.QueryID,
				"trace_id", query.TraceID,
				"correlation_id", msg.CorrelationID,
			)

			candidates, err := reg.FindBySkills(query.Domains)
			if err != nil {
				log.Error("capability.query registry lookup failed",
					"query_id", query.QueryID,
					"error", err,
				)
				return
			}

			resp := types.CapabilityResponse{
				QueryID:  query.QueryID,
				Domains:  query.Domains,
				HasMatch: len(candidates) > 0,
				TraceID:  query.TraceID,
			}
			if err := commsClient.Publish(
				comms.SubjectCapabilityResponse,
				comms.PublishOptions{
					MessageType:   comms.MsgTypeCapabilityResponse,
					CorrelationID: query.QueryID,
					TraceID:       query.TraceID,
					Transient:     true, // at-most-once
				},
				resp,
			); err != nil {
				log.Error("publish capability.response failed",
					"query_id", query.QueryID,
					"error", err,
				)
			} else {
				log.Info("msg.outbound",
					"topic", comms.SubjectCapabilityResponse,
					"message_type", comms.MsgTypeCapabilityResponse,
					"query_id", query.QueryID,
					"trace_id", query.TraceID,
					"correlation_id", query.QueryID,
					"has_match", resp.HasMatch,
				)
			}
		},
	); err != nil {
		log.Error("subscribe capability.query failed", "error", err)
		os.Exit(1)
	}

	// Subscribe to intra-component metrics events published by agent-process
	// subprocesses (core NATS, at-most-once — no JetStream required).
	// These drive counters and histograms that cannot be observed directly in the
	// main process (vault execute latencies, context compaction, CONTEXT_OVERFLOW).
	if err := commsClient.Subscribe(
		comms.SubjectMetricsEvent,
		func(msg *comms.Message) {
			var evt types.MetricsEvent
			if err := json.Unmarshal(msg.Data, &evt); err != nil {
				return // drop malformed event — at-most-once, no retry
			}
			switch evt.EventType {
			case types.MetricsEventVaultExecuteComplete:
				rec.ObserveVaultExecute(evt.OperationType, evt.ElapsedMS)
			case types.MetricsEventCompactionTriggered:
				rec.IncCompactionTriggered()
			case types.MetricsEventContextOverflow:
				rec.IncContextOverflow()
			case types.MetricsEventLLMCacheHit:
				rec.IncLLMCacheHit()
			case types.MetricsEventLLMCacheMiss:
				rec.IncLLMCacheMiss()
			}
		},
	); err != nil {
		log.Error("subscribe metrics.event failed", "error", err)
		os.Exit(1)
	}

	log.Info("aegis-agents ready")

	<-ctx.Done()

	log.Info("shutdown signal received, stopping")
	if err := commsClient.Close(); err != nil {
		log.Error("comms close failed", "error", err)
	}
	log.Info("aegis-agents stopped")
}

// loadSkills loads skill definitions from configPath (YAML or JSON) and registers
// every domain with the Skill Hierarchy Manager. When configPath is empty the
// embedded default_skills.yaml is used automatically.
//
// Registration failures are logged as warnings rather than fatal errors so that
// a single malformed command definition does not prevent all other domains from
// being served.
func loadSkills(configPath string, mgr skills.Manager, log *slog.Logger) {
	cfg, err := skillsconfig.Load(configPath)
	if err != nil {
		log.Error("skills config load failed — no skills registered",
			"path", configPath,
			"error", err,
		)
		return
	}

	source := "embedded default"
	if configPath != "" {
		source = configPath
	}
	log.Info("loading skills from config", "source", source, "domains", len(cfg.Domains))

	for _, node := range cfg.ToSkillNodes() {
		if err := mgr.RegisterDomain(node); err != nil {
			log.Warn("skill domain registration failed", "domain", node.Name, "error", err)
		} else {
			log.Info("skill domain registered",
				"domain", node.Name,
				"commands", len(node.Children),
			)
		}
	}
}

// reloadSkills hot-reloads the skill tree from configPath into mgr on SIGHUP.
//
// Config load or parse errors are logged and the existing tree is kept intact
// (safe fallback). If mgr does not implement skills.Reloader (e.g. a test
// stub), the call is a no-op with a warning log.
func reloadSkills(configPath string, mgr skills.Manager, log *slog.Logger) {
	cfg, err := skillsconfig.Load(configPath)
	if err != nil {
		log.Error("skill hot-reload: config load failed — keeping current tree",
			"path", configPath,
			"error", err,
		)
		return
	}

	reloader, ok := mgr.(skills.Reloader)
	if !ok {
		log.Warn("skill hot-reload: Manager does not implement Reloader — skipping")
		return
	}

	result, err := reloader.Reload(cfg.ToSkillNodes())
	if err != nil {
		log.Error("skill hot-reload: reload failed — keeping current tree", "error", err)
		return
	}

	log.Info("skill hot-reload complete",
		"added", result.Added,
		"removed", result.Removed,
		"modified", result.Modified,
	)
}
