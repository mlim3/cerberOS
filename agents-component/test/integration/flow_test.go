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
	"time"

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

	// task_spec subscription — mirrors main.go handler.
	if err := commsClient.Subscribe("task_spec", func(msg *comms.Message) {
		var env types.Envelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			t.Errorf("task_spec: unmarshal envelope: %v", err)
			return
		}
		payloadBytes, err := json.Marshal(env.Payload)
		if err != nil {
			t.Errorf("task_spec: marshal payload: %v", err)
			return
		}
		var spec types.TaskSpec
		if err := json.Unmarshal(payloadBytes, &spec); err != nil {
			t.Errorf("task_spec: unmarshal TaskSpec: %v", err)
			return
		}
		if err := f.HandleTaskSpec(&spec); err != nil {
			t.Errorf("HandleTaskSpec: %v", err)
		}
	}); err != nil {
		t.Fatalf("subscribe task_spec: %v", err)
	}

	// capability_query subscription — mirrors main.go handler.
	if err := commsClient.Subscribe("capability_query", func(msg *comms.Message) {
		var env types.Envelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			t.Errorf("capability_query: unmarshal envelope: %v", err)
			return
		}
		payloadBytes, err := json.Marshal(env.Payload)
		if err != nil {
			t.Errorf("capability_query: marshal payload: %v", err)
			return
		}
		var query types.CapabilityQuery
		if err := json.Unmarshal(payloadBytes, &query); err != nil {
			t.Errorf("capability_query: unmarshal CapabilityQuery: %v", err)
			return
		}
		candidates, err := reg.FindBySkills(query.Domains)
		if err != nil {
			t.Errorf("capability_query: FindBySkills: %v", err)
			return
		}
		resp := types.CapabilityResponse{
			QueryID:  query.QueryID,
			Domains:  query.Domains,
			HasMatch: len(candidates) > 0,
			TraceID:  query.TraceID,
		}
		if err := commsClient.Publish("capability_response", resp); err != nil {
			t.Errorf("capability_query: publish response: %v", err)
		}
	}); err != nil {
		t.Fatalf("subscribe capability_query: %v", err)
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

// publishTaskSpec wraps a TaskSpec in the standard Envelope and publishes it.
func publishTaskSpec(t *testing.T, c comms.Client, spec types.TaskSpec) {
	t.Helper()
	env := types.Envelope{
		MessageID:   "msg-" + spec.TaskID,
		Source:      "orchestrator",
		Destination: "agents-component",
		Timestamp:   time.Now().UTC(),
		Payload:     spec,
		TraceID:     spec.TraceID,
	}
	if err := c.Publish("task_spec", env); err != nil {
		t.Fatalf("publish task_spec: %v", err)
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
	// The registry is empty. Publishing a task_spec must trigger the full
	// provisioning sequence: skills resolved → credentials pre-authorized
	// → VM spawned → agent registered → status_update published.
	// ------------------------------------------------------------------
	t.Run("new agent provisioned", func(t *testing.T) {
		var statusUpdates []types.StatusUpdate
		if err := h.comms.Subscribe("status_update", func(msg *comms.Message) {
			var su types.StatusUpdate
			if err := json.Unmarshal(msg.Data, &su); err != nil {
				t.Errorf("unmarshal status_update: %v", err)
				return
			}
			statusUpdates = append(statusUpdates, su)
		}); err != nil {
			t.Fatalf("subscribe status_update: %v", err)
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

		// At least one status_update must have been published.
		if len(statusUpdates) == 0 {
			t.Error("expected at least one status_update published to Orchestrator")
		}
	})

	// ------------------------------------------------------------------
	// Scenario 2 — Existing idle agent reused
	//
	// After marking the agent idle (simulating task completion by the VM),
	// a second task_spec with the same skill domain must be assigned to the
	// existing agent rather than provisioning a new one.
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
	// The Orchestrator asks whether any agent exists with the "web" domain.
	// The component must respond synchronously with HasMatch: true.
	// ------------------------------------------------------------------
	t.Run("capability query answered", func(t *testing.T) {
		var capResp types.CapabilityResponse
		if err := h.comms.Subscribe("capability_response", func(msg *comms.Message) {
			if err := json.Unmarshal(msg.Data, &capResp); err != nil {
				t.Errorf("unmarshal capability_response: %v", err)
			}
		}); err != nil {
			t.Fatalf("subscribe capability_response: %v", err)
		}

		env := types.Envelope{
			MessageID:   "msg-q1",
			Source:      "orchestrator",
			Destination: "agents-component",
			Timestamp:   time.Now().UTC(),
			Payload: types.CapabilityQuery{
				QueryID: "query-1",
				Domains: []string{"web"},
				TraceID: "trace-3",
			},
			TraceID: "trace-3",
		}
		if err := h.comms.Publish("capability_query", env); err != nil {
			t.Fatalf("publish capability_query: %v", err)
		}

		if !capResp.HasMatch {
			t.Error("capability_response.HasMatch: want true, got false")
		}
		if capResp.QueryID != "query-1" {
			t.Errorf("capability_response.QueryID: want query-1, got %s", capResp.QueryID)
		}
	})

	// ------------------------------------------------------------------
	// Scenario 4 — Task completion and teardown
	//
	// CompleteTask must: write a tagged result to Memory, publish task_result
	// to the Orchestrator, terminate the VM, revoke credentials, and mark
	// the agent terminated in the registry.
	// ------------------------------------------------------------------
	t.Run("task completion and teardown", func(t *testing.T) {
		agent := h.reg.List()[0]

		var taskResults []types.TaskResult
		if err := h.comms.Subscribe("task_result", func(msg *comms.Message) {
			var tr types.TaskResult
			if err := json.Unmarshal(msg.Data, &tr); err != nil {
				t.Errorf("unmarshal task_result: %v", err)
				return
			}
			taskResults = append(taskResults, tr)
		}); err != nil {
			t.Fatalf("subscribe task_result: %v", err)
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

		// task_result must be published to the Orchestrator.
		if len(taskResults) == 0 {
			t.Fatal("expected task_result published to Orchestrator, got none")
		}
		if !taskResults[0].Success {
			t.Errorf("task_result.Success: want true, got false: %s", taskResults[0].Error)
		}

		// VM must be terminated (removed from stub — Health returns StateUnknown).
		health, _ := h.lc.Health(agent.AgentID)
		if health.State != lifecycle.StateUnknown {
			t.Errorf("VM state after teardown: want unknown (terminated), got %s", health.State)
		}

		// Credentials must be revoked — any lookup must fail.
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
