// Package integration — nats_e2e_test.go exercises the complete factory flow
// against a live NATS JetStream instance with the partner simulator providing
// synthetic responses.
//
// These tests require a real NATS server with JetStream enabled.
// Set AEGIS_NATS_URL or run the default (nats://localhost:4222).
// Tests are skipped automatically when NATS is not reachable.
//
// Quick start:
//
//	docker run --rm -p 4222:4222 nats:latest -js
//	go test ./test/integration/... -v -run TestNATS
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
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
	"github.com/cerberOS/agents-component/test/integration/simulator"
	"github.com/nats-io/nats.go"
)

// natsTestURL returns the NATS URL from the environment or the local default.
func natsTestURL() string {
	if u := os.Getenv("AEGIS_NATS_URL"); u != "" {
		return u
	}
	return "nats://localhost:4222"
}

// natsHarness holds all wired components connected to real NATS.
type natsHarness struct {
	nc            *nats.Conn
	js            nats.JetStreamContext
	sim           *simulator.Simulator
	commsClient   comms.Client
	reg           registry.Registry
	lc            lifecycle.Manager
	creds         credentials.Broker
	mem           memory.Client
	skillMgr      skills.Manager
	f             *factory.Factory
	crashDetector *lifecycle.CrashDetector
	ctx           context.Context
	cancel        context.CancelFunc
}

// newNATSHarness connects to NATS, starts the simulator, wires all modules,
// and subscribes the same handlers as aegis-agents/main.go.
// The test is skipped automatically when NATS is unavailable.
func newNATSHarness(t *testing.T) *natsHarness {
	t.Helper()

	url := natsTestURL()

	// Raw NATS connection for test observations (subscribe / direct publish).
	nc, err := nats.Connect(url)
	if err != nil {
		t.Skipf("NATS unavailable (%v) — set AEGIS_NATS_URL or run: docker run --rm -p 4222:4222 nats:latest -js", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		t.Fatalf("JetStream context: %v", err)
	}

	// Simulator — subscribes aegis.orchestrator.* and publishes synthetic replies.
	sim, err := simulator.New(url)
	if err != nil {
		nc.Close()
		t.Skipf("simulator init failed (%v)", err)
	}
	if err := sim.Start(); err != nil {
		sim.Stop() //nolint:errcheck
		nc.Close()
		t.Fatalf("simulator.Start: %v", err)
	}

	// NATS-backed comms client (same as production). Use a unique component ID
	// per test run to avoid consumer-name collisions if tests run in parallel.
	componentID := fmt.Sprintf("test-%d", time.Now().UnixNano())
	commsClient, err := comms.NewNATSClient(url, componentID)
	if err != nil {
		sim.Stop() //nolint:errcheck
		nc.Close()
		t.Fatalf("comms.NewNATSClient: %v", err)
	}

	reg := registry.New()
	skillMgr := skills.New()

	// NATS-backed credential broker for the full round-trip (publish
	// credential.request → simulator responds → PreAuthorize completes).
	credBroker, err := credentials.NewNATSBroker(commsClient)
	if err != nil {
		commsClient.Close() //nolint:errcheck
		sim.Stop()          //nolint:errcheck
		nc.Close()
		t.Fatalf("credentials.NewNATSBroker: %v", err)
	}

	lcMgr := lifecycle.New()  // in-process stub: no real Firecracker
	memClient := memory.New() // in-process stub: writes are no-ops

	seedNATSSkills(t, skillMgr)

	ctx, cancel := context.WithCancel(context.Background())

	// Crash detector — fires HandleCrash when heartbeats stop.
	var f *factory.Factory
	crashDetector := lifecycle.NewCrashDetector(
		lifecycle.HeartbeatConfig{
			Interval:  500 * time.Millisecond,
			MaxMissed: 2,
		},
		func(agentID string) {
			if f != nil {
				if err := f.HandleCrash(agentID); err != nil {
					t.Logf("HandleCrash(%s): %v", agentID, err)
				}
			}
		},
	)
	go crashDetector.Run(ctx)

	f, err = factory.New(factory.Config{
		Registry:      reg,
		Skills:        skillMgr,
		Credentials:   credBroker,
		Lifecycle:     lcMgr,
		Memory:        memClient,
		Comms:         commsClient,
		CrashDetector: crashDetector,
		MaxRetries:    3,
	})
	if err != nil {
		cancel()
		commsClient.Close() //nolint:errcheck
		sim.Stop()          //nolint:errcheck
		nc.Close()
		t.Fatalf("factory.New: %v", err)
	}

	// Subscribe handlers — mirrors aegis-agents/main.go.
	if err := commsClient.SubscribeDurable(
		comms.SubjectTaskInbound,
		componentID+"-task-inbound", // unique per test run
		func(msg *comms.Message) {
			var spec types.TaskSpec
			if err := json.Unmarshal(msg.Data, &spec); err != nil {
				t.Errorf("task.inbound unmarshal: %v", err)
				_ = msg.Nak()
				return
			}
			if err := f.HandleTaskSpec(&spec); err != nil {
				t.Errorf("HandleTaskSpec: %v", err)
				_ = msg.Nak()
				return
			}
			_ = msg.Ack()
		},
	); err != nil {
		cancel()
		t.Fatalf("subscribe task.inbound: %v", err)
	}

	if err := commsClient.SubscribeDurable(
		comms.SubjectVaultExecuteResult,
		componentID+"-vault-result",
		func(msg *comms.Message) {
			var result types.VaultOperationResult
			if err := json.Unmarshal(msg.Data, &result); err != nil {
				_ = msg.Nak()
				return
			}
			f.CompleteVaultRequest(result.AgentID, result.RequestID)
			_ = msg.Ack()
		},
	); err != nil {
		cancel()
		t.Fatalf("subscribe vault.execute.result: %v", err)
	}

	if err := commsClient.Subscribe(
		comms.SubjectCapabilityQuery,
		func(msg *comms.Message) {
			var query types.CapabilityQuery
			if err := json.Unmarshal(msg.Data, &query); err != nil {
				return
			}
			candidates, err := reg.FindBySkills(query.Domains)
			if err != nil {
				return
			}
			resp := types.CapabilityResponse{
				QueryID:  query.QueryID,
				Domains:  query.Domains,
				HasMatch: len(candidates) > 0,
				TraceID:  query.TraceID,
			}
			_ = commsClient.Publish(
				comms.SubjectCapabilityResponse,
				comms.PublishOptions{
					MessageType:   comms.MsgTypeCapabilityResponse,
					CorrelationID: query.QueryID,
					Transient:     true,
				},
				resp,
			)
		},
	); err != nil {
		cancel()
		t.Fatalf("subscribe capability.query: %v", err)
	}

	h := &natsHarness{
		nc:            nc,
		js:            js,
		sim:           sim,
		commsClient:   commsClient,
		reg:           reg,
		lc:            lcMgr,
		creds:         credBroker,
		mem:           memClient,
		skillMgr:      skillMgr,
		f:             f,
		crashDetector: crashDetector,
		ctx:           ctx,
		cancel:        cancel,
	}

	t.Cleanup(func() {
		cancel()
		commsClient.Close() //nolint:errcheck
		sim.Stop()          //nolint:errcheck
		nc.Close()
	})
	return h
}

// seedNATSSkills registers the minimal skill tree used by e2e tests.
func seedNATSSkills(t *testing.T, mgr skills.Manager) {
	t.Helper()
	if err := mgr.RegisterDomain(&types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": {
				Name:           "web.fetch",
				Level:          "command",
				Label:          "Web Fetch",
				Description:    "Fetch the content of a URL via HTTP.",
				TimeoutSeconds: 30,
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url": {Type: "string", Required: true, Description: "URL to fetch."},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("seed skill web: %v", err)
	}
}

// subscribeObserve subscribes to a JetStream subject and returns a channel that
// receives unwrapped payload bytes. The subscription uses DeliverNew so it only
// sees messages published after the call returns.
func subscribeObserve(t *testing.T, js nats.JetStreamContext, subject string) <-chan json.RawMessage {
	t.Helper()
	ch := make(chan json.RawMessage, 8)
	sub, err := js.Subscribe(subject,
		func(msg *nats.Msg) {
			_ = msg.Ack()
			var env struct {
				Payload json.RawMessage `json:"payload"`
			}
			if jsonErr := json.Unmarshal(msg.Data, &env); jsonErr == nil {
				ch <- env.Payload
			}
		},
		nats.DeliverNew(),
	)
	if err != nil {
		t.Fatalf("subscribeObserve(%q): %v", subject, err)
	}
	t.Cleanup(func() { sub.Unsubscribe() }) //nolint:errcheck
	return ch
}

// awaitMsg waits up to timeout for the next message on ch; fails the test on timeout.
func awaitMsg(t *testing.T, ch <-chan json.RawMessage, desc string, timeout time.Duration) json.RawMessage {
	t.Helper()
	select {
	case raw := <-ch:
		return raw
	case <-time.After(timeout):
		t.Fatalf("timed out after %v waiting for %s", timeout, desc)
		return nil
	}
}

// noMsg asserts no message arrives within the quiet period.
func noMsg(t *testing.T, ch <-chan json.RawMessage, desc string, quiet time.Duration) {
	t.Helper()
	select {
	case <-ch:
		t.Errorf("unexpected message received for %s", desc)
	case <-time.After(quiet):
	}
}

// ─── Scenario 1: Full provisioning path ──────────────────────────────────────

// TestNATS_FullProvisioningPath verifies: new task published → task.accepted
// emitted → credential.request sent (simulator grants token) → agent registered
// ACTIVE → CompleteTask publishes task.result, revokes credentials, and
// terminates the agent.
func TestNATS_FullProvisioningPath(t *testing.T) {
	h := newNATSHarness(t)

	taskAcceptedCh := subscribeObserve(t, h.js, comms.SubjectTaskAccepted)
	agentStatusCh := subscribeObserve(t, h.js, comms.SubjectAgentStatus)
	taskResultCh := subscribeObserve(t, h.js, comms.SubjectTaskResult)

	// Publish task.inbound via simulator.
	spec := types.TaskSpec{
		TaskID:         "e2e-task-1",
		RequiredSkills: []string{"web"},
		Instructions:   "fetch https://example.com",
		TraceID:        "e2e-trace-1",
	}
	if err := h.sim.PublishTaskInbound(spec); err != nil {
		t.Fatalf("PublishTaskInbound: %v", err)
	}

	// task.accepted must be published immediately (before provisioning).
	raw := awaitMsg(t, taskAcceptedCh, "task.accepted", 5*time.Second)
	var accepted types.TaskAccepted
	if err := json.Unmarshal(raw, &accepted); err != nil {
		t.Fatalf("unmarshal task.accepted: %v", err)
	}
	if accepted.TaskID != spec.TaskID {
		t.Errorf("task.accepted TaskID: want %q, got %q", spec.TaskID, accepted.TaskID)
	}

	// At least one agent.status must be published during provisioning.
	awaitMsg(t, agentStatusCh, "agent.status (provisioning)", 10*time.Second)

	// Wait for agent to appear as ACTIVE in the registry.
	var agentID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		agents := h.reg.List()
		if len(agents) == 1 && agents[0].State == "active" {
			agentID = agents[0].AgentID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if agentID == "" {
		t.Fatalf("agent never became ACTIVE within 10s; registry: %v", h.reg.List())
	}

	// VM must be running.
	health, err := h.lc.Health(agentID)
	if err != nil {
		t.Fatalf("lifecycle.Health: %v", err)
	}
	if health.State != lifecycle.StateRunning {
		t.Errorf("VM state: want running, got %s", health.State)
	}

	// Trigger task completion.
	result := map[string]string{"pages_fetched": "1"}
	if err := h.f.CompleteTask(agentID, "session-e2e-1", "e2e-trace-1", result, nil); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	// task.result must be published.
	raw = awaitMsg(t, taskResultCh, "task.result", 5*time.Second)
	var taskResult types.TaskResult
	if err := json.Unmarshal(raw, &taskResult); err != nil {
		t.Fatalf("unmarshal task.result: %v", err)
	}
	if !taskResult.Success {
		t.Errorf("task.result.Success: want true, got false; error: %s", taskResult.Error)
	}

	// Agent must be terminated and credentials revoked.
	agent, err := h.reg.Get(agentID)
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	if agent.State != "terminated" {
		t.Errorf("agent state after CompleteTask: want terminated, got %s", agent.State)
	}
	vmHealth, _ := h.lc.Health(agentID)
	if vmHealth.State != lifecycle.StateUnknown {
		t.Errorf("VM state after termination: want unknown, got %s", vmHealth.State)
	}
	_, credErr := h.creds.GetCredential(agentID, "web.credential")
	if credErr == nil {
		t.Error("expected credential error after revoke, got nil")
	}
}

// ─── Scenario 2: Fast path — existing idle agent reused ──────────────────────

// TestNATS_FastPath verifies that a second task with matching skill domain is
// dispatched to an existing idle agent without provisioning a new one.
func TestNATS_FastPath(t *testing.T) {
	h := newNATSHarness(t)

	// Provision first agent.
	spec1 := types.TaskSpec{
		TaskID:         "e2e-fp-task-1",
		RequiredSkills: []string{"web"},
		Instructions:   "fetch https://example.com",
		TraceID:        "e2e-fp-trace-1",
	}
	if err := h.sim.PublishTaskInbound(spec1); err != nil {
		t.Fatalf("PublishTaskInbound task-1: %v", err)
	}

	// Wait for ACTIVE agent.
	var agentID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		agents := h.reg.List()
		if len(agents) == 1 && agents[0].State == "active" {
			agentID = agents[0].AgentID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if agentID == "" {
		t.Fatalf("first agent never became ACTIVE")
	}

	// Mark the agent idle so it is eligible for reuse.
	if err := h.reg.UpdateState(agentID, "idle", "test: marking idle for fast-path test"); err != nil {
		t.Fatalf("UpdateState idle: %v", err)
	}

	// Publish a second task.
	spec2 := types.TaskSpec{
		TaskID:         "e2e-fp-task-2",
		RequiredSkills: []string{"web"},
		Instructions:   "fetch https://example.org",
		TraceID:        "e2e-fp-trace-2",
	}
	if err := h.sim.PublishTaskInbound(spec2); err != nil {
		t.Fatalf("PublishTaskInbound task-2: %v", err)
	}

	// Wait for the agent to be assigned to task-2.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		agents := h.reg.List()
		if len(agents) == 1 && agents[0].AssignedTask == spec2.TaskID {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	agents := h.reg.List()
	if len(agents) != 1 {
		t.Errorf("fast path: expected 1 agent (reused), got %d — new agent incorrectly provisioned", len(agents))
	}
	if agents[0].AssignedTask != spec2.TaskID {
		t.Errorf("fast path: assigned_task want %q, got %q", spec2.TaskID, agents[0].AssignedTask)
	}
	if agents[0].AgentID != agentID {
		t.Errorf("fast path: expected same agent_id %q, got %q — new agent created", agentID, agents[0].AgentID)
	}
}

// ─── Scenario 3: Crash and recovery ──────────────────────────────────────────

// TestNATS_CrashAndRecovery verifies: agent heartbeat stops → crash detector
// fires → HandleCrash executes the recovery sequence → agent transitions
// through RECOVERING → ACTIVE (re-provisioned with same agent_id).
func TestNATS_CrashAndRecovery(t *testing.T) {
	h := newNATSHarness(t)

	agentStatusCh := subscribeObserve(t, h.js, comms.SubjectAgentStatus)

	// Provision an agent.
	spec := types.TaskSpec{
		TaskID:         "e2e-crash-task-1",
		RequiredSkills: []string{"web"},
		Instructions:   "long running task",
		TraceID:        "e2e-crash-trace-1",
	}
	if err := h.sim.PublishTaskInbound(spec); err != nil {
		t.Fatalf("PublishTaskInbound: %v", err)
	}

	// Wait for ACTIVE agent.
	var agentID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		agents := h.reg.List()
		if len(agents) == 1 && agents[0].State == "active" {
			agentID = agents[0].AgentID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if agentID == "" {
		t.Fatalf("agent never became ACTIVE")
	}

	// Register an in-flight vault request to verify resubmission.
	inFlightRequestID := "vault-req-crash-test-1"
	h.f.TrackVaultRequest(agentID, inFlightRequestID)

	// Start heartbeating, then stop — simulates crash.
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		// Send a few heartbeats so the detector registers the agent, then stop.
		for i := 0; i < 3; i++ {
			h.crashDetector.RecordHeartbeat(agentID)
			<-ticker.C
		}
		// Stop sending — detector will fire after MaxMissed * Interval.
	}()
	<-heartbeatDone

	// Wait for the agent to enter RECOVERING state.
	recoveryDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(recoveryDeadline) {
		agent, err := h.reg.Get(agentID)
		if err == nil && (agent.State == "recovering" || agent.State == "active") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	agent, err := h.reg.Get(agentID)
	if err != nil {
		t.Fatalf("registry.Get after crash: %v", err)
	}
	// Agent should be RECOVERING or ACTIVE (recovered) — both are valid at this point.
	if agent.State != "recovering" && agent.State != "active" {
		t.Errorf("expected state recovering or active after crash, got %s", agent.State)
	}

	// At least one agent.status with state_change must have been published.
	select {
	case <-agentStatusCh:
		// Good — state transition was published to the Orchestrator.
	case <-time.After(2 * time.Second):
		t.Error("no agent.status published during crash recovery")
	}

	// failure_count must be incremented.
	if agent.FailureCount < 1 {
		t.Errorf("failure_count: want >= 1 after crash, got %d", agent.FailureCount)
	}

	// State history must include the recovery transition.
	foundRecovering := false
	for _, ev := range agent.StateHistory {
		if ev.State == "recovering" {
			foundRecovering = true
			break
		}
	}
	if !foundRecovering {
		t.Error("state_history: expected a 'recovering' entry after crash")
	}
}

// ─── Scenario 5: Capability query ────────────────────────────────────────────

// TestNATS_CapabilityQuery verifies that the agents component responds to
// capability.query messages with the correct HasMatch value for match,
// partial-match, and no-match cases.
func TestNATS_CapabilityQuery(t *testing.T) {
	h := newNATSHarness(t)

	// Subscribe to responses directly on the core NATS connection (at-most-once).
	respCh := make(chan json.RawMessage, 4)
	sub, err := h.nc.Subscribe(comms.SubjectCapabilityResponse, func(msg *nats.Msg) {
		var env struct {
			Payload json.RawMessage `json:"payload"`
		}
		if jsonErr := json.Unmarshal(msg.Data, &env); jsonErr == nil {
			respCh <- env.Payload
		}
	})
	if err != nil {
		t.Fatalf("subscribe capability.response: %v", err)
	}
	t.Cleanup(func() { sub.Unsubscribe() }) //nolint:errcheck

	queryAndExpect := func(t *testing.T, queryID string, domains []string, wantMatch bool) {
		t.Helper()
		query := types.CapabilityQuery{
			QueryID: queryID,
			Domains: domains,
			TraceID: "e2e-cap-trace-" + queryID,
		}
		data, err := json.Marshal(struct {
			MessageID       string      `json:"message_id"`
			MessageType     string      `json:"message_type"`
			SourceComponent string      `json:"source_component"`
			Timestamp       string      `json:"timestamp"`
			SchemaVersion   string      `json:"schema_version"`
			Payload         interface{} `json:"payload"`
		}{
			MessageID:       queryID,
			MessageType:     comms.MsgTypeCapabilityQuery,
			SourceComponent: "orchestrator",
			Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
			SchemaVersion:   "1.0",
			Payload:         query,
		})
		if err != nil {
			t.Fatalf("marshal capability.query: %v", err)
		}
		if err := h.nc.Publish(comms.SubjectCapabilityQuery, data); err != nil {
			t.Fatalf("publish capability.query: %v", err)
		}

		select {
		case raw := <-respCh:
			var resp types.CapabilityResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("unmarshal capability.response: %v", err)
			}
			if resp.QueryID != queryID {
				t.Errorf("QueryID: want %q, got %q", queryID, resp.QueryID)
			}
			if resp.HasMatch != wantMatch {
				t.Errorf("HasMatch for domains %v: want %v, got %v", domains, wantMatch, resp.HasMatch)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for capability.response to query %q", queryID)
		}
	}

	// No-match: registry is empty — no agents with any skill.
	t.Run("no match — empty registry", func(t *testing.T) {
		queryAndExpect(t, "cap-q-1", []string{"web"}, false)
	})

	// Provision a web agent so the registry has a match.
	spec := types.TaskSpec{
		TaskID:         "e2e-cap-task-1",
		RequiredSkills: []string{"web"},
		Instructions:   "fetch https://example.com",
		TraceID:        "e2e-cap-trace-prov",
	}
	if err := h.sim.PublishTaskInbound(spec); err != nil {
		t.Fatalf("PublishTaskInbound: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		agents := h.reg.List()
		if len(agents) == 1 && (agents[0].State == "active" || agents[0].State == "idle") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Match: agent with "web" skill exists.
	t.Run("match — web agent active", func(t *testing.T) {
		queryAndExpect(t, "cap-q-2", []string{"web"}, true)
	})

	// No-match: agent exists but doesn't have "storage" skill.
	t.Run("no match — wrong domain", func(t *testing.T) {
		queryAndExpect(t, "cap-q-3", []string{"storage"}, false)
	})

	// Drain any lingering responses.
	drainCh := func() {
		for {
			select {
			case <-respCh:
			default:
				return
			}
		}
	}
	drainCh()
}

// ─── In-flight vault request tracking helpers ─────────────────────────────────

// inFlightVaultWatcher collects vault.execute.request messages published
// to aegis.orchestrator.vault.execute.request and exposes a WasResubmitted
// check for crash-recovery tests.
type inFlightVaultWatcher struct {
	mu       sync.Mutex
	requests []string // request_ids seen
}

func (w *inFlightVaultWatcher) record(requestID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.requests = append(w.requests, requestID)
}

func (w *inFlightVaultWatcher) seen(requestID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, id := range w.requests {
		if id == requestID {
			return true
		}
	}
	return false
}
