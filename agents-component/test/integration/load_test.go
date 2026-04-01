// Package integration — load_test.go exercises the factory under concurrent
// load against a live NATS JetStream instance.
//
// Run with:
//
//	docker run --rm -p 4222:4222 nats:latest -js
//	go test ./test/integration/... -v -run TestLoad -timeout 3m
package integration

import (
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	"github.com/nats-io/nats.go"
)

const loadAgentCount = 50

// TestLoad_50ConcurrentAgents spawns, executes, and terminates 50 concurrent
// agents under real NATS and verifies four invariants:
//
//  1. No goroutine leaks — delta between baseline and post-teardown is bounded.
//  2. No registry state corruption — all 50 agents reach TERMINATED cleanly.
//  3. No credential scope bleed — each agent receives a distinct permission token.
//  4. Vault execute round-trip latency under load meets the NFR-04 target (p99 < 50ms).
func TestLoad_50ConcurrentAgents(t *testing.T) {
	h := newNATSHarness(t)
	seedLoadSkills(t, h)

	// ── 1. Goroutine baseline ────────────────────────────────────────────────
	// Allow background goroutines started by newNATSHarness to settle.
	time.Sleep(200 * time.Millisecond)
	baseGoroutines := runtime.NumGoroutine()
	t.Logf("goroutine baseline: %d", baseGoroutines)

	// ── 2. Publish 50 tasks concurrently ────────────────────────────────────
	provisionStart := time.Now()
	var publishWG sync.WaitGroup
	publishWG.Add(loadAgentCount)
	for i := 0; i < loadAgentCount; i++ {
		go func(i int) {
			defer publishWG.Done()
			spec := types.TaskSpec{
				TaskID:         fmt.Sprintf("load-task-%d", i),
				RequiredSkills: []string{fmt.Sprintf("load-%d", i)},
				Instructions:   fmt.Sprintf("load test agent %d", i),
				TraceID:        fmt.Sprintf("load-trace-%d", i),
			}
			if err := h.sim.PublishTaskInbound(spec); err != nil {
				t.Errorf("PublishTaskInbound[%d]: %v", i, err)
			}
		}(i)
	}
	publishWG.Wait()

	// ── 3. Wait for all 50 agents to become ACTIVE ──────────────────────────
	t.Log("waiting for all agents to become ACTIVE…")
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if countByState(h, "active") == loadAgentCount {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	provisionDuration := time.Since(provisionStart)

	activeAgents := agentsByState(h, "active")
	if len(activeAgents) != loadAgentCount {
		all := h.reg.List()
		stateCounts := map[string]int{}
		for _, a := range all {
			stateCounts[a.State]++
		}
		t.Fatalf("expected %d ACTIVE agents after %v; got %v (state counts: %v)",
			loadAgentCount, provisionDuration, len(activeAgents), stateCounts)
	}
	t.Logf("all %d agents ACTIVE in %v", loadAgentCount, provisionDuration)

	// Snapshot agent list before completing (state may change under us).
	agentSnapshot := h.reg.List()

	// ── 4. Complete all tasks concurrently ──────────────────────────────────
	var completeWG sync.WaitGroup
	for _, agent := range agentSnapshot {
		completeWG.Add(1)
		go func(a *types.AgentRecord) {
			defer completeWG.Done()
			if err := h.f.CompleteTask(
				a.AgentID,
				"session-"+a.AgentID,
				a.AssignedTask,
				map[string]string{"load_test": "complete"},
				nil,
			); err != nil {
				t.Errorf("CompleteTask(%s): %v", a.AgentID, err)
			}
		}(agent)
	}
	completeWG.Wait()

	// ── 5. Wait for all 50 agents to reach TERMINATED ────────────────────────
	t.Log("waiting for all agents to reach TERMINATED…")
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if countByState(h, "terminated") == loadAgentCount {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// ── Assertion A: Registry state — no corruption ──────────────────────────
	t.Run("registry_no_corruption", func(t *testing.T) {
		all := h.reg.List()
		if len(all) != loadAgentCount {
			t.Errorf("registry count: want %d, got %d", loadAgentCount, len(all))
		}
		for _, a := range all {
			if a.State != "terminated" {
				t.Errorf("agent %s stuck in state %q (want terminated)", a.AgentID, a.State)
			}
			if len(a.StateHistory) == 0 {
				t.Errorf("agent %s has empty state_history", a.AgentID)
			}
		}
	})

	// ── Assertion B: Credential scope bleed ─────────────────────────────────
	t.Run("credential_scope_no_bleed", func(t *testing.T) {
		tokens := h.sim.CredentialTokens()
		if len(tokens) != loadAgentCount {
			t.Errorf("credential token count: want %d, got %d", loadAgentCount, len(tokens))
		}
		seen := map[string]string{} // token → agentID (first holder)
		for agentID, token := range tokens {
			if token == "" {
				t.Errorf("agent %s received empty permission token", agentID)
				continue
			}
			if prior, dup := seen[token]; dup {
				t.Errorf("credential scope bleed: agents %q and %q share token %q",
					prior, agentID, token)
			} else {
				seen[token] = agentID
			}
		}
	})

	// ── Assertion C: Goroutine leak ──────────────────────────────────────────
	t.Run("no_goroutine_leak", func(t *testing.T) {
		// Give goroutines a moment to wind down after termination.
		time.Sleep(500 * time.Millisecond)
		afterGoroutines := runtime.NumGoroutine()
		delta := afterGoroutines - baseGoroutines
		t.Logf("goroutines: baseline=%d after=%d delta=%d", baseGoroutines, afterGoroutines, delta)
		// Allow a small headroom for test infrastructure goroutines.
		const maxAllowedDelta = 10
		if delta > maxAllowedDelta {
			t.Errorf("possible goroutine leak: delta %d > %d (baseline=%d, after=%d)",
				delta, maxAllowedDelta, baseGoroutines, afterGoroutines)
		}
	})

	// ── Assertion D: Vault execute round-trip latency ────────────────────────
	// Drive 50 concurrent vault.execute.request messages directly through NATS
	// and measure the round-trip to vault.execute.result. This exercises the
	// full simulator dispatch path under load without requiring real agent processes.
	t.Run("vault_execute_latency", func(t *testing.T) {
		latencies := measureVaultExecuteLatencies(t, h, loadAgentCount)
		if len(latencies) == 0 {
			t.Fatal("no vault execute latency samples collected")
		}
		sort.Float64s(latencies)
		p50 := percentile(latencies, 50)
		p95 := percentile(latencies, 95)
		p99 := percentile(latencies, 99)
		t.Logf("vault execute round-trip latency (%d samples): p50=%.2fms p95=%.2fms p99=%.2fms",
			len(latencies), p50, p95, p99)
		// NFR-04: vault execute request routing latency < 50ms p99.
		const nfr04MaxP99MS = 50.0
		if p99 > nfr04MaxP99MS {
			t.Errorf("NFR-04 violation: p99 %.2fms exceeds %.0fms target", p99, nfr04MaxP99MS)
		}
	})
}

// measureVaultExecuteLatencies publishes n vault.execute.request messages
// concurrently via JetStream and measures the round-trip to the corresponding
// vault.execute.result response from the simulator. Returns latencies in ms.
func measureVaultExecuteLatencies(t *testing.T, h *natsHarness, n int) []float64 {
	t.Helper()

	type sample struct {
		requestID string
		sent      time.Time
	}

	mu := sync.Mutex{}
	sent := make(map[string]time.Time, n)
	latencies := make([]float64, 0, n)
	done := make(chan struct{})
	received := 0

	// Subscribe to vault.execute.result on the test's raw NATS connection.
	// The simulator publishes results to aegis.agents.vault.execute.result (JetStream).
	resultSub, err := h.js.Subscribe(
		comms.SubjectVaultExecuteResult,
		func(msg *nats.Msg) {
			_ = msg.Ack()
			var env struct {
				Payload json.RawMessage `json:"payload"`
			}
			if jsonErr := json.Unmarshal(msg.Data, &env); jsonErr != nil {
				return
			}
			var result types.VaultOperationResult
			if jsonErr := json.Unmarshal(env.Payload, &result); jsonErr != nil {
				return
			}
			mu.Lock()
			if sentAt, ok := sent[result.RequestID]; ok {
				latencies = append(latencies, float64(time.Since(sentAt).Microseconds())/1000.0)
				received++
				if received == n {
					close(done)
				}
			}
			mu.Unlock()
		},
		nats.DeliverNew(),
	)
	if err != nil {
		t.Fatalf("vault latency: subscribe vault.execute.result: %v", err)
	}
	t.Cleanup(func() { resultSub.Unsubscribe() }) //nolint:errcheck

	// Publish n vault.execute.request messages concurrently.
	// Use a real agent from the registry (any terminated agent) as the agent_id.
	// The simulator doesn't validate agent state — it responds to any well-formed request.
	allAgents := h.reg.List()
	if len(allAgents) == 0 {
		t.Skip("vault latency: no agents in registry")
		return nil
	}
	agentID := allAgents[0].AgentID

	var publishWG sync.WaitGroup
	publishWG.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer publishWG.Done()
			requestID := fmt.Sprintf("vault-lat-%d-%d", i, time.Now().UnixNano())
			req := types.VaultOperationRequest{
				RequestID:       requestID,
				AgentID:         agentID,
				TaskID:          fmt.Sprintf("vault-lat-task-%d", i),
				PermissionToken: "sim-token-" + agentID,
				OperationType:   "web_fetch",
				OperationParams: json.RawMessage(`{"url":"https://example.com"}`),
				TimeoutSeconds:  30,
				CredentialType:  "web_api_key",
			}
			env := map[string]interface{}{
				"message_id":       fmt.Sprintf("vault-lat-msg-%d", i),
				"message_type":     comms.MsgTypeVaultExecuteRequest,
				"source_component": "load-test",
				"correlation_id":   requestID,
				"timestamp":        time.Now().UTC().Format(time.RFC3339Nano),
				"schema_version":   "1.0",
				"payload":          req,
			}
			data, err := json.Marshal(env)
			if err != nil {
				t.Errorf("vault latency: marshal request %d: %v", i, err)
				return
			}
			mu.Lock()
			sent[requestID] = time.Now()
			mu.Unlock()
			if _, err := h.js.Publish(comms.SubjectVaultExecuteRequest, data); err != nil {
				mu.Lock()
				delete(sent, requestID)
				mu.Unlock()
				t.Errorf("vault latency: publish request %d: %v", i, err)
			}
		}(i)
	}
	publishWG.Wait()

	// Wait for all results or timeout.
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		mu.Lock()
		got := received
		mu.Unlock()
		t.Logf("vault latency: timeout waiting for results — got %d/%d", got, n)
	}

	mu.Lock()
	defer mu.Unlock()
	return latencies
}

// seedLoadSkills registers 50 unique skill domains "load-0" … "load-49"
// so each concurrent task forces provisioning of a distinct new agent.
func seedLoadSkills(t *testing.T, h *natsHarness) {
	t.Helper()
	for i := 0; i < loadAgentCount; i++ {
		domain := fmt.Sprintf("load-%d", i)
		if err := h.skillMgr.RegisterDomain(&types.SkillNode{
			Name:     domain,
			Level:    "domain",
			Children: map[string]*types.SkillNode{},
		}); err != nil {
			t.Fatalf("seed skill %s: %v", domain, err)
		}
	}
}

// countByState returns the number of agents in the registry with the given state.
func countByState(h *natsHarness, state string) int {
	n := 0
	for _, a := range h.reg.List() {
		if a.State == state {
			n++
		}
	}
	return n
}

// agentsByState returns all agents in the registry with the given state.
func agentsByState(h *natsHarness, state string) []*types.AgentRecord {
	var out []*types.AgentRecord
	for _, a := range h.reg.List() {
		if a.State == state {
			out = append(out, a)
		}
	}
	return out
}

// percentile returns the p-th percentile value from a sorted slice of float64.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := p / 100.0 * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}
