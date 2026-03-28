package main

import (
	"context"
	"encoding/json"
	"log/slog"
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
	"github.com/cerberOS/agents-component/internal/registry"
	"github.com/cerberOS/agents-component/internal/skills"
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

	// Wire NATS JetStream client.
	commsClient, err := comms.NewNATSClient(cfg.NATSURL, cfg.ComponentID)
	if err != nil {
		log.Error("comms init failed", "error", err)
		os.Exit(1)
	}

	reg := registry.New()
	skillMgr := skills.New()
	credBroker := credentials.New(nil) // TODO: wire Vault-backed broker (M3)
	memClient := memory.New()          // TODO: wire NATS-backed Memory Interface (M3)

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

	seedSkills(skillMgr, log)

	f, err = factory.New(factory.Config{
		Registry:      reg,
		Skills:        skillMgr,
		Credentials:   credBroker,
		Lifecycle:     lifecycleMgr,
		Memory:        memClient,
		Comms:         commsClient,
		CrashDetector: crashDetector,
		MaxRetries:    cfg.MaxAgentRetries,
	})
	if err != nil {
		log.Error("factory init failed", "error", err)
		os.Exit(1)
	}

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

			log.Info("task.inbound received",
				"task_id", spec.TaskID,
				"correlation_id", msg.CorrelationID,
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
			}
		},
	); err != nil {
		log.Error("subscribe capability.query failed", "error", err)
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

// seedSkills registers the initial skill tree. In production this is loaded from
// the Memory Component or a config file at startup.
func seedSkills(mgr skills.Manager, log *slog.Logger) {
	domains := []*types.SkillNode{
		{
			Name:  "web",
			Level: "domain",
			Children: map[string]*types.SkillNode{
				"web.fetch": {
					Name:           "web.fetch",
					Level:          "command",
					Label:          "Web Fetch",
					Description:    "Fetch the content of a URL via HTTP. Use for web pages and APIs without authentication. Do NOT use for authenticated operations.",
					TimeoutSeconds: 30,
					Spec: &types.SkillSpec{
						Parameters: map[string]types.ParameterDef{
							"url":    {Type: "string", Required: true, Description: "The fully-qualified URL to fetch."},
							"method": {Type: "string", Required: false, Description: "HTTP method: GET or POST. Defaults to GET."},
						},
					},
				},
			},
		},
		{Name: "data", Level: "domain", Children: map[string]*types.SkillNode{}},
		{Name: "comms", Level: "domain", Children: map[string]*types.SkillNode{}},
		{Name: "storage", Level: "domain", Children: map[string]*types.SkillNode{}},
	}

	for _, d := range domains {
		if err := mgr.RegisterDomain(d); err != nil {
			log.Warn("skill domain registration failed", "domain", d.Name, "error", err)
		}
	}
}
