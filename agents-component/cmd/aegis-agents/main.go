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

	// Wire dependencies.
	commsClient := comms.NewStubClient() // TODO: replace with NATS-backed client
	reg := registry.New()
	skillMgr := skills.New()
	credBroker := credentials.New(nil) // TODO: wire OpenBao secrets
	lifecycleMgr := lifecycle.New()    // TODO: wire Firecracker
	memClient := memory.New()          // TODO: replace with HTTP client to Memory Component

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

	// Subscribe to inbound task_spec messages from the Orchestrator.
	if err := commsClient.Subscribe("task_spec", func(msg *comms.Message) {
		var env types.Envelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			log.Error("malformed envelope", "error", err)
			return
		}

		// Payload unmarshals as map[string]interface{}; re-encode to extract TaskSpec.
		payloadBytes, err := json.Marshal(env.Payload)
		if err != nil {
			log.Error("envelope payload marshal failed", "trace_id", env.TraceID, "error", err)
			return
		}

		var spec types.TaskSpec
		if err := json.Unmarshal(payloadBytes, &spec); err != nil {
			log.Error("task_spec unmarshal failed", "trace_id", env.TraceID, "error", err)
			return
		}

		log.Info("task_spec received", "task_id", spec.TaskID, "trace_id", spec.TraceID)

		if err := f.HandleTaskSpec(&spec); err != nil {
			log.Error("handle task_spec failed",
				"task_id", spec.TaskID,
				"trace_id", spec.TraceID,
				"error", err,
			)
		}
	}); err != nil {
		log.Error("subscribe task_spec failed", "error", err)
		os.Exit(1)
	}

	// Subscribe to capability_query — Orchestrator asking whether a capable agent exists.
	if err := commsClient.Subscribe("capability_query", func(msg *comms.Message) {
		var env types.Envelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			log.Error("malformed envelope", "error", err)
			return
		}

		payloadBytes, err := json.Marshal(env.Payload)
		if err != nil {
			log.Error("capability_query payload marshal failed", "trace_id", env.TraceID, "error", err)
			return
		}

		var query types.CapabilityQuery
		if err := json.Unmarshal(payloadBytes, &query); err != nil {
			log.Error("capability_query unmarshal failed", "trace_id", env.TraceID, "error", err)
			return
		}

		candidates, err := reg.FindBySkills(query.Domains)
		if err != nil {
			log.Error("capability_query registry lookup failed", "trace_id", query.TraceID, "error", err)
			return
		}

		resp := types.CapabilityResponse{
			QueryID:  query.QueryID,
			Domains:  query.Domains,
			HasMatch: len(candidates) > 0,
			TraceID:  query.TraceID,
		}
		if err := commsClient.Publish("capability_response", resp); err != nil {
			log.Error("publish capability_response failed", "trace_id", query.TraceID, "error", err)
		}
	}); err != nil {
		log.Error("subscribe capability_query failed", "error", err)
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
		{
			Name:     "data",
			Level:    "domain",
			Children: map[string]*types.SkillNode{},
		},
		{
			Name:     "comms",
			Level:    "domain",
			Children: map[string]*types.SkillNode{},
		},
		{
			Name:     "storage",
			Level:    "domain",
			Children: map[string]*types.SkillNode{},
		},
	}

	for _, d := range domains {
		if err := mgr.RegisterDomain(d); err != nil {
			log.Warn("skill domain registration failed", "domain", d.Name, "error", err)
		}
	}
}
