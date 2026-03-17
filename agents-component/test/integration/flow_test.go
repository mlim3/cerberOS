// Package integration is the end-to-end flow test for the Agents Component.
// It wires all seven modules with their in-process stubs and exercises the
// full task lifecycle: provisioning, agent reuse, capability queries, and teardown.
//
// No external services (NATS, Firecracker, Orchestrator) are required.
// All communication happens in-process via the stub comms client.
package integration

import (
	"encoding/json"
	"testing"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/internal/credentials"
	"github.com/cerberOS/agents-component/internal/factory"
	"github.com/cerberOS/agents-component/internal/lifecycle"
	"github.com/cerberOS/agents-component/internal/memory"
	"github.com/cerberOS/agents-component/internal/registry"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

// harness holds all wired stubs for a single test run.
type harness struct {
	comms   comms.Client
	reg     registry.Registry
	creds   credentials.Broker
	lc      lifecycle.Manager
	mem     memory.Client
	factory *factory.Factory
}

// newHarness wires all modules and subscribes the same handlers that main.go uses.
func newHarness(t *testing.T) *harness {
	t.Helper()

	commsClient := comms.NewStubClient()
	reg := registry.New()
	skillMgr := skills.New()
	credBroker := credentials.New(nil)
	lcMgr := lifecycle.New()
	memClient := memory.New()

	// Seed the same skill tree as main.go.
	if err := skillMgr.RegisterDomain(&types.SkillNode{
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
	}); err != nil {
		t.Fatalf("seed skills: %v", err)
	}

	f, err := factory.New(factory.Config{
		Registry:    reg,
		Skills:      skillMgr,
		Credentials: credBroker,
		Lifecycle:   lcMgr,
		Memory:      memClient,
		Comms:       commsClient,
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}

	// task.inbound subscription — mirrors main.go handler.
	// The stub delivers msg.Data as the raw JSON payload (no envelope wrapping).
	if err := commsClient.SubscribeDurable("aegis.agents.task.inbound", "agents-task-inbound", func(msg *comms.Message) {
		var spec types.TaskSpec
		if err := json.Unmarshal(msg.Data, &spec); err != nil {
			t.Errorf("task.inbound: unmarshal TaskSpec: %v", err)
			_ = msg.Nak()
			return
		}
		if err := f.HandleTaskSpec(&spec); err != nil {
			t.Errorf("HandleTaskSpec: %v", err)
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	}); err != nil {
		t.Fatalf("subscribe task.inbound: %v", err)
	}

	// capability.query subscription — mirrors main.go handler.
	if err := commsClient.Subscribe("aegis.agents.capability.query", func(msg *comms.Message) {
		var query types.CapabilityQuery
		if err := json.Unmarshal(msg.Data, &query); err != nil {
			t.Errorf("capability.query: unmarshal CapabilityQuery: %v", err)
			return
		}
		candidates, err := reg.FindBySkills(query.Domains)
		if err != nil {
			t.Errorf("capability.query: FindBySkills: %v", err)
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
			comms.PublishOptions{MessageType: "capability.response", CorrelationID: query.QueryID, Transient: true},
			resp,
		); err != nil {
			t.Errorf("capability.query: publish response: %v", err)
		}
	}); err != nil {
		t.Fatalf("subscribe capability.query: %v", err)
	}

	return &harness{
		comms:   commsClient,
		reg:     reg,
		creds:   credBroker,
		lc:      lcMgr,
		mem:     memClient,
		factory: f,
	}
}

// publishTaskSpec publishes a TaskSpec to the correct inbound subject.
func publishTaskSpec(t *testing.T, c comms.Client, spec types.TaskSpec) {
	t.Helper()
	if err := c.Publish(
		"aegis.agents.task.inbound",
		comms.PublishOptions{MessageType: "task.inbound", CorrelationID: spec.TaskID},
		spec,
	); err != nil {
		t.Fatalf("publish task.inbound: %v", err)
	}
}

// TestAgentComponentFlow exercises the complete task lifecycle in four sequential
// scenarios. Each scenario builds on the state left by the previous one — this is
// intentional: it mirrors the real runtime flow.
func TestAgentComponentFlow(t *testing.T) {
	h := newHarness(t)

	// ------------------------------------------------------------------
	// Scenario 1 — New agent provisioned
	//
	// The registry is empty. Publishing a task.inbound must trigger the full
	// provisioning sequence: skills resolved → credentials pre-authorized
	// → VM spawned → agent registered → agent.status published.
	// ------------------------------------------------------------------
	t.Run("new agent provisioned", func(t *testing.T) {
		var statusUpdates []types.StatusUpdate
		if err := h.comms.Subscribe("aegis.orchestrator.agent.status", func(msg *comms.Message) {
			var su types.StatusUpdate
			if err := json.Unmarshal(msg.Data, &su); err != nil {
				t.Errorf("unmarshal agent.status: %v", err)
				return
			}
			statusUpdates = append(statusUpdates, su)
		}); err != nil {
			t.Fatalf("subscribe agent.status: %v", err)
		}

		publishTaskSpec(t, h.comms, types.TaskSpec{
			TaskID:         "task-1",
			RequiredSkills: []string{"web"},
			TraceID:        "trace-1",
		})

		agents := h.reg.List()
		if len(agents) != 1 {
			t.Fatalf("expected 1 agent in registry, got %d", len(agents))
		}
		agent := agents[0]

		if agent.State != "active" {
			t.Errorf("agent state: want active, got %s", agent.State)
		}
		if agent.AssignedTask != "task-1" {
			t.Errorf("assigned task: want task-1, got %s", agent.AssignedTask)
		}

		// VM must be running.
		health, err := h.lc.Health(agent.AgentID)
		if err != nil {
			t.Fatalf("lifecycle.Health: %v", err)
		}
		if health.State != lifecycle.StateRunning {
			t.Errorf("VM state: want running, got %s", health.State)
		}

		// At least one agent.status must have been published.
		if len(statusUpdates) == 0 {
			t.Error("expected at least one agent.status published to Orchestrator")
		}
	})

	// ------------------------------------------------------------------
	// Scenario 2 — Existing idle agent reused
	//
	// After marking the agent idle, a second task with the same skill domain
	// must be assigned to the existing agent — not a new one.
	// ------------------------------------------------------------------
	t.Run("existing agent reused", func(t *testing.T) {
		agentID := h.reg.List()[0].AgentID

		if err := h.reg.UpdateState(agentID, "idle"); err != nil {
			t.Fatalf("UpdateState idle: %v", err)
		}

		publishTaskSpec(t, h.comms, types.TaskSpec{
			TaskID:         "task-2",
			RequiredSkills: []string{"web"},
			TraceID:        "trace-2",
		})

		agents := h.reg.List()
		if len(agents) != 1 {
			t.Errorf("expected 1 agent (reused), got %d — a second agent was incorrectly provisioned", len(agents))
		}
		if agents[0].AssignedTask != "task-2" {
			t.Errorf("assigned task: want task-2, got %s", agents[0].AssignedTask)
		}
	})

	// ------------------------------------------------------------------
	// Scenario 3 — Capability query answered
	//
	// Publishing a capability.query must produce a capability.response with
	// HasMatch: true, since a web-capable agent exists.
	// ------------------------------------------------------------------
	t.Run("capability query answered", func(t *testing.T) {
		var capResp types.CapabilityResponse
		if err := h.comms.Subscribe("aegis.orchestrator.capability.response", func(msg *comms.Message) {
			if err := json.Unmarshal(msg.Data, &capResp); err != nil {
				t.Errorf("unmarshal capability.response: %v", err)
			}
		}); err != nil {
			t.Fatalf("subscribe capability.response: %v", err)
		}

		query := types.CapabilityQuery{
			QueryID: "query-1",
			Domains: []string{"web"},
			TraceID: "trace-3",
		}
		if err := h.comms.Publish(
			"aegis.agents.capability.query",
			comms.PublishOptions{MessageType: "capability.query", CorrelationID: query.QueryID},
			query,
		); err != nil {
			t.Fatalf("publish capability.query: %v", err)
		}

		if !capResp.HasMatch {
			t.Error("capability.response.HasMatch: want true, got false")
		}
		if capResp.QueryID != "query-1" {
			t.Errorf("capability.response.QueryID: want query-1, got %s", capResp.QueryID)
		}
	})

	// ------------------------------------------------------------------
	// Scenario 4 — Task completion and teardown
	//
	// CompleteTask must: write a tagged result to Memory, publish task.result
	// to the Orchestrator, terminate the VM, revoke credentials, and mark
	// the agent terminated in the registry.
	// ------------------------------------------------------------------
	t.Run("task completion and teardown", func(t *testing.T) {
		agent := h.reg.List()[0]

		var taskResults []types.TaskResult
		if err := h.comms.Subscribe("aegis.orchestrator.task.result", func(msg *comms.Message) {
			var tr types.TaskResult
			if err := json.Unmarshal(msg.Data, &tr); err != nil {
				t.Errorf("unmarshal task.result: %v", err)
				return
			}
			taskResults = append(taskResults, tr)
		}); err != nil {
			t.Fatalf("subscribe task.result: %v", err)
		}

		output := map[string]string{"summary": "fetched 42 records"}
		if err := h.factory.CompleteTask(agent.AgentID, "session-1", "trace-4", output, nil); err != nil {
			t.Fatalf("CompleteTask: %v", err)
		}

		// Memory record must be written with the result tag.
		records, err := h.mem.Read(agent.AgentID, "result")
		if err != nil {
			t.Fatalf("memory.Read: %v", err)
		}
		if len(records) == 0 {
			t.Error("expected tagged memory record after CompleteTask, got none")
		}

		// task.result must be published to the Orchestrator.
		if len(taskResults) == 0 {
			t.Fatal("expected task.result published to Orchestrator, got none")
		}
		if !taskResults[0].Success {
			t.Errorf("task.result.Success: want true, got false: %s", taskResults[0].Error)
		}

		// VM must be terminated.
		health, _ := h.lc.Health(agent.AgentID)
		if health.State != lifecycle.StateUnknown {
			t.Errorf("VM state after teardown: want unknown (terminated), got %s", health.State)
		}

		// Credentials must be revoked.
		_, err = h.creds.GetCredential(agent.AgentID, "web.credential")
		if err == nil {
			t.Error("expected error on GetCredential after Revoke, got nil")
		}

		// Registry must reflect terminated state.
		updated, err := h.reg.Get(agent.AgentID)
		if err != nil {
			t.Fatalf("registry.Get: %v", err)
		}
		if updated.State != "terminated" {
			t.Errorf("agent state after teardown: want terminated, got %s", updated.State)
		}
	})
}
