package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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

	// Subscribe to status_update to print to console for interactive feedback
	commsClient.Subscribe("status_update", func(msg *comms.Message) {
		var update types.StatusUpdate
		if err := json.Unmarshal(msg.Data, &update); err == nil {
			fmt.Printf("\n[STATUS] Agent: %s | Task: %s | State: %s\n> ", update.AgentID, update.TaskID, update.State)
		}
	})

	log.Info("aegis-agents ready")

	// Interactive CLI loop
	go func() {
		reader := bufio.NewReader(os.Stdin)
		fmt.Println("\n--- Interactive Mode ---")
		fmt.Println("Commands: task <id> <skill>, query <skill>, list, exit")
		fmt.Print("> ")

		for {
			text, _ := reader.ReadString('\n')
			text = strings.TrimSpace(text)
			parts := strings.Fields(text)

			if len(parts) == 0 {
				fmt.Print("> ")
				continue
			}

			switch parts[0] {
			case "exit":
				syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
				return
			case "list":
				agents := reg.List()
				fmt.Printf("Registered Agents: %d\n", len(agents))
				for _, a := range agents {
					fmt.Printf("- %s [%s] Skills: %v\n", a.AgentID, a.State, a.SkillDomains)
				}
			case "task":
				if len(parts) < 3 {
					fmt.Println("Usage: task <task_id> <skill_domain>")
					break
				}
				taskID := parts[1]
				skill := parts[2]
				spec := types.TaskSpec{
					TaskID:         taskID,
					RequiredSkills: []string{skill},
					TraceID:        fmt.Sprintf("trace-%d", time.Now().Unix()),
				}
				env := types.Envelope{
					MessageID:   fmt.Sprintf("msg-%d", time.Now().Unix()),
					Source:      "cli",
					Destination: "agents-component",
					Timestamp:   time.Now().UTC(),
					Payload:     spec,
					TraceID:     spec.TraceID,
				}
				if err := commsClient.Publish("task_spec", env); err != nil {
					fmt.Printf("Error publishing task: %v\n", err)
				} else {
					fmt.Println("Task published.")
				}
			case "query":
				if len(parts) < 2 {
					fmt.Println("Usage: query <skill_domain>")
					break
				}
				skill := parts[1]
				query := types.CapabilityQuery{
					QueryID: fmt.Sprintf("q-%d", time.Now().Unix()),
					Domains: []string{skill},
					TraceID: fmt.Sprintf("trace-%d", time.Now().Unix()),
				}
				env := types.Envelope{
					MessageID:   fmt.Sprintf("msg-%d", time.Now().Unix()),
					Source:      "cli",
					Destination: "agents-component",
					Timestamp:   time.Now().UTC(),
					Payload:     query,
					TraceID:     query.TraceID,
				}
				// Subscribe to response temporarily
				commsClient.Subscribe("capability_response", func(msg *comms.Message) {
					var resp types.CapabilityResponse
					json.Unmarshal(msg.Data, &resp)
					if resp.QueryID == query.QueryID {
						fmt.Printf("\n[QUERY RESULT] Match: %v\n> ", resp.HasMatch)
					}
				})
				if err := commsClient.Publish("capability_query", env); err != nil {
					fmt.Printf("Error publishing query: %v\n", err)
				} else {
					fmt.Println("Query published.")
				}
			default:
				fmt.Println("Unknown command.")
			}
			fmt.Print("> ")
		}
	}()

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
