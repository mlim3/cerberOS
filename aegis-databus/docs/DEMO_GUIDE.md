# Aegis DataBus — Demo Guide

## 1. Is the connz Output Correct?

**Yes.** Your `/connz` output is what you should see:

| Connection | Count | What it is |
|------------|-------|------------|
| **aegis-demo** | 2 | 6-component demo (may show 2 if reconnected or multiple processes) |
| **aegis-databus** | 3 | DataBus (main + outbox relay + heartbeat) |
| **aegis-stubs** | 1 | Simulated TaskRouter, Orchestrator, AgentFactory, Vault, Monitoring |

- `aegis-demo` and `aegis-stubs` have high `in_msgs` / `out_msgs` — they publish and subscribe.
- `aegis-databus` has `in_msgs` (receives) and low/zero `out_msgs` per connection — it publishes via JetStream context and relay; the breakdown depends on how Prometheus/NATS reports it.

---

## 2. How to Demo Each Requirement

### FR-DB-001: Publish-Subscribe

**What to show:** I/O → Orchestrator → Memory → Agent flow.

**Steps:**
1. Start: `docker compose up -d`, then `./bin/aegis-databus &` and `./bin/aegis-demo`.
2. Open Grafana → **Traffic by component** and watch `aegis-demo` traffic.
3. In the demo terminal, you’ll see `[I/O] published`, `[Orchestrator] received`, `[Memory] published`, `[Agent] published`.
4. Open **http://localhost:8222/jsz** to show stream message counts (AEGIS_TASKS, AEGIS_AGENTS, AEGIS_MEMORY, etc.) increasing.

---

### FR-DB-002: Request-Reply

**What to show:** Personalization request-reply.

**Steps:**
1. I/O sends `aegis.personalization.get`; Memory responds.
2. See it in the demo logs or run `make test` and point to `bus.Request` / `bus.SubscribeRequestReply` in the code.
3. Optional: add a small CLI that does `bus.Request("aegis.personalization.get", ...)` and prints the reply.

---

### FR-DB-004: Queue Groups

**What to show:** One message delivered to one consumer in a queue group.

**Steps:**
1. Run `make test` → TC002 passes (3 subscribers, each message goes to one).
2. In code: show `QueueSubscribe(..., "agent-managers")` in `cmd/demo/components.go`.
3. Explain: with 3 “agent-manager” subscribers, each `plan_created` is delivered to only one of them.

---

### FR-DB-005: Wildcard Routing

**What to show:** Monitoring subscribes to multiple subjects via wildcards.

**Steps:**
1. Show `cmd/demo/components.go` → `runMonitoring` with `aegis.tasks.>`, `aegis.agents.>`, etc.
2. Open **http://localhost:8222/connz?subs=1** and find the Monitoring connection; show its subscription list with wildcard subjects.

---

### FR-DB-008: Event Replay

**What to show:** Replay last N messages.

**Steps:**
1. Run `make test` and show `TestReplayLastN` in `tests/replay_test.go`.
2. Explain `bus.ReplayLastN(js, stream, subject, 10)` and how it uses a pull consumer + `Fetch`.

---

### FR-DB-009: Ack & DLQ

**What to show:** After 5 failed acks, message goes to DLQ.

**Steps:**
1. Run `make test` → TC005.
2. Show `tests/harness_test.go` TC005: consumer intentionally NAKs 5 times, then the message is moved to `aegis.dlq`.

---

### FR-DB-010: Schema Validation

**What to show:** Invalid CloudEvents are rejected.

**Steps:**
1. Run `go test ./pkg/envelope/... -v` and show `TestValidate`.
2. Or add a quick demo:
   ```go
   err := envelope.Validate([]byte(`{"invalid":"json"}`))
   // err != nil
   ```

---

### FR-DB-011: Outbox Pattern

**What to show:** Outbox relay replays and publishes.

**Steps:**
1. Run `make test` → TC004.
2. Show `internal/relay/outbox.go` — poll, publish, mark sent.
3. Grafana: `aegis-databus` traffic reflects outbox relay activity when there is outbox data.

---

### Zero Trust (NKey Auth)

**What to show:** Only clients with valid NKeys can connect.

**Current state:** NATS is not configured with auth. The demo uses unauthenticated connections. NKey support exists in `pkg/security/nkey.go` and `cmd/stubs` (via `AEGIS_NKEY_SEED`).

**Demo options:**

1. **Code walkthrough**
   - Show `pkg/security/nkey.go`: `NewSecureConnection`, `NewConnectionWithEnvSeed`.
   - Show `GenerateNKey` and how the public key goes to a registry.
   - Explain: with NATS operator/account configured for NKey, unauthenticated clients fail; only clients with a valid seed succeed.

2. **With NATS auth enabled** (advanced)
   - Add an operator/account with NKey to `config/nats-node*.conf`.
   - Run without a seed → connection fails.
   - Run with `AEGIS_NKEY_SEED_xxx` set → connection succeeds.
   - Grafana/`connz`: only authenticated clients appear.

---

### Failure Recovery

**What to show:** System continues working after NATS node or client failure.

**Demo 1: Kill a NATS node**

1. Run the stack and demo.
2. `docker stop nats-2`
3. Watch Grafana:
   - Connections may drop or reconnect.
   - Traffic may dip, then recover.
4. `docker start nats-2` — cluster and clients recover.
5. Optional: `docker stop nats-1` (leader) — JetStream elects a new leader and traffic recovers.

**Demo 2: Kill and restart DataBus**

1. `pkill aegis-databus` (or `kill <pid>`).
2. Grafana: connections drop; traffic from `aegis-databus` stops.
3. Restart: `./bin/aegis-databus &`.
4. Connections reappear in `connz`; traffic resumes.
5. Point out `nats.MaxReconnects(-1)` in `cmd/databus/main.go`.

**Demo 3: Kill and restart demo**

1. Stop `aegis-demo` (Ctrl+C).
2. Grafana: `aegis-demo` traffic stops.
3. Restart `./bin/aegis-demo`.
4. Traffic reappears; `connz` shows the demo connection again.

---

## 3. Quick Reference

| Requirement      | Demo method                                      |
|------------------|--------------------------------------------------|
| Pub/Sub          | Run demo, watch Grafana + logs + `/jsz`          |
| Request-Reply    | Code walkthrough, `make test`                    |
| Queue Groups     | TC002, show `QueueSubscribe` in code             |
| Wildcards        | `connz?subs=1`, show Monitoring subs             |
| Replay           | `TestReplayLastN`                                |
| Ack & DLQ        | TC005                                            |
| Schema validation| `envelope_test.go`                               |
| Outbox           | TC004, `outbox.go` code                          |
| Zero Trust       | Code walkthrough; with NATS auth: fail/succeed   |
| Failure recovery | Kill NATS node or DataBus, watch Grafana recover |
