package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/cerberOS/agents-component/pkg/types"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

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

	reg := registry.New(registry.WithStateChangeHook(rec.ObserveStateChange))
	skillMgr := skills.New(skills.WithGetSpecHook(rec.ObserveSkillInvocation))
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

	var lifecycleMgr lifecycle.Manager
	if cfg.AgentProcessPath != "" {
		log.Info("lifecycle: using process manager", "binary", cfg.AgentProcessPath)
		lifecycleMgr = lifecycle.NewProcess(cfg.AgentProcessPath)
	} else {
		log.Warn("lifecycle: AEGIS_AGENT_PROCESS_PATH not set — using in-process stub (not for production)")
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

	if cfg.IdleSuspendTimeout > 0 {
		log.Info("OQ-03/OQ-06 suspension enabled",
			"idle_suspend_timeout", cfg.IdleSuspendTimeout,
			"wake_latency_target", cfg.SuspendWakeLatencyTarget,
		)
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

			if err := f.HandleTaskSpec(&spec); err != nil {
				log.Error("HandleTaskSpec failed",
					"task_id", spec.TaskID,
					"error", err,
				)
				_ = msg.Nak()
				return
			}
			_ = msg.Ack()
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
