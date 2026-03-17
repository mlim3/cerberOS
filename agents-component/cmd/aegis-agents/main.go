package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
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

	// Wire NATS JetStream client.
	commsClient, err := comms.NewNATSClient(cfg.NATSURL, cfg.ComponentID)
	if err != nil {
		log.Error("comms init failed", "error", err)
		os.Exit(1)
	}

	reg := registry.New()
	skillMgr := skills.New()
	credBroker := credentials.New(nil) // TODO: wire Vault-backed broker (M3)
	lifecycleMgr := lifecycle.New()    // TODO: wire Firecracker (M3)
	memClient := memory.New()          // TODO: wire NATS-backed Memory Interface (M3)

	seedSkills(skillMgr, log)

	f, err := factory.New(factory.Config{
		Registry:    reg,
		Skills:      skillMgr,
		Credentials: credBroker,
		Lifecycle:   lifecycleMgr,
		Memory:      memClient,
		Comms:       commsClient,
	})
	if err != nil {
		log.Error("factory init failed", "error", err)
		os.Exit(1)
	}

	// Subscribe to inbound task assignments from the Orchestrator (at-least-once).
	// Handlers MUST call msg.Ack() on success or msg.Nak() on failure.
	if err := commsClient.SubscribeDurable(
		"aegis.agents.task.inbound",
		"agents-task-inbound",
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

	// Subscribe to capability queries from the Orchestrator (at-most-once).
	if err := commsClient.Subscribe(
		"aegis.agents.capability.query",
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
				"aegis.orchestrator.capability.response",
				comms.PublishOptions{
					MessageType:   "capability.response",
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

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

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
					Name:  "web.fetch",
					Level: "command",
					Spec: &types.SkillSpec{
						Parameters: map[string]types.ParameterDef{
							"url":    {Type: "string", Required: true},
							"method": {Type: "string", Required: false},
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
