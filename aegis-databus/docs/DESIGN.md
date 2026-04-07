# DataBus Design Document

Design decisions, patterns, workflow, and best practices for the Aegis DataBus component.

---

## 1. Design Decisions and Patterns

### 1.1 Design Patterns (per EDD)

| Pattern | Implementation | Justification |
|---------|----------------|---------------|
| **Publish-Subscribe** | NATS JetStream subjects (`aegis.tasks.>`, etc.) | Decouples producers from consumers; enables fan-out, scaling, and replay. |
| **Outbox Pattern** | `internal/relay/outbox.go` + MemoryClient outbox | Atomic write + async relay guarantees zero message loss on publisher crash (FR-DB-011). |
| **Durable Consumer** | JetStream consumers with `Durable`, `ManualAck` | Survives restarts; redelivery on NAK (FR-DB-003). |
| **Dead Letter Queue** | `AEGIS_DLQ` stream, consumer forwards on MaxDeliver | Failed messages isolated for admin review (FR-DB-009, SR-DB-006). |
| **Queue Groups** | `QueueSubscribe(..., "agent-managers")` | Competing consumers; each message to exactly one (FR-DB-004). |
| **Adapter Pattern** | `MemoryClient` interface | Abstracts storage; demo uses Mock, production uses Orchestrator proxy or direct Memory HTTP. |
| **Dependency Inversion** | Depend on `MemoryClient`, not concrete DB | DataBus never imports libSQL/postgres; swap implementations without code change. |

### 1.2 Key Design Decisions

**Schema validation at API boundary (FR-DB-010)**  
- Validation in `PublishValidated`, `PublishWithACL`, not at NATS protocol layer.  
- **Rationale**: NATS does not inspect payloads; middleware would add latency. Components use our SDK; validation is enforceable in `pkg/validation` and `bus.PublishValidated` / `PublishWithACL`.

**ACL enforcement in application layer**  
- `CheckPublish`, `CheckSubscribe` in `pkg/security/acl.go`; components call `PublishWithACL`, `SubscribeWithACL`.  
- **Rationale**: NATS auth is connection-level; subject-level rules need application logic. Centralized ACL logic keeps policy in one place.

**Fallback to Mock when storage is down (DEGRADED-HOLD)**  
- `FallbackClient` switches to `MockMemoryClient` when `Ping()` fails (primary = Orchestrator proxy or direct Memory).  
- **Rationale**: DataBus stays up; outbox uses in-memory mock; no crash. Instruction: "I use MockMemoryClient and enter DEGRADED-HOLD mode. I do NOT crash."

**Stream setup with retries**  
- `EnsureStreams` retried up to 10× with 2s delay.  
- **Rationale**: 3-node cluster formation and Raft need time; retries handle startup ordering.

---

## 2. Workflow and Dependencies

### 2.1 Dependency Overview

```
DataBus (zero dependency root)
    ├── NATS JetStream (required)
    ├── Memory (optional: HTTP or Mock)
    └── OpenBao (optional: NKey fetch or env)
```

### 2.2 Running Without Dependencies

| Dependency | Absent behavior | Simple implementation |
|------------|-----------------|------------------------|
| **Memory / Orchestrator** | Both unset → `MockMemoryClient` | In-memory maps; outbox, audit in process. |
| **OpenBao** | `OPENBAO_ADDR` unset → `AEGIS_NKEY_SEED` env | NKey from env; no vault. |
| **NKey auth** | `AEGIS_NKEY_SEED` unset → plaintext connect | `make up` (no secure overlay). |

### 2.3 Boot Flow

1. Connect to NATS (TLS optional via `AEGIS_NATS_TLS_CA`).
2. Retry `EnsureStreams` until success.
3. Choose storage: `AEGIS_ORCHESTRATOR_URL` (Option 1: DataBus → Orchestrator → Memory) or `AEGIS_MEMORY_URL` (direct Memory) or Mock.
4. Start outbox relay, heartbeat, HTTP server.
5. Graceful shutdown on SIGTERM/SIGINT.

### 2.4 Workflow Without Memory (Demo)

- DataBus uses `MockMemoryClient`.
- Outbox entries stored in memory; relay publishes to NATS.
- Audit log from mock; `/audit` returns seeded entries.
- Full pub/sub, request-reply, and demo flow work.

### 2.5 Option 1: DataBus → Orchestrator → Memory

Per architecture decision: DataBus does **not** call Memory directly. It calls Orchestrator's proxy API, which forwards to Memory.

| Env | Client | Flow |
|-----|--------|------|
| `AEGIS_ORCHESTRATOR_URL` set | `OrchestratorStorageClient` + Fallback | DataBus → Orchestrator → Memory |
| `AEGIS_MEMORY_URL` set | `HTTPClient` + Fallback | DataBus → Memory (legacy) |
| Neither set | `MockMemoryClient` | In-process only |

Stubs for demo: `memory-stub` (Memory API), `orchestrator-stub` (proxy). See [Repository README](../../README.md) (Option 1) and `make demo-orchestrator-memory`.

---

## 3. Code Structure and Build Process

### 3.1 Layout

```
aegis-databus/
├── cmd/
│   ├── databus/       # Main DataBus process
│   ├── demo/          # 6-component EDD demo
│   ├── stubs/         # Standalone stubs (alternative demo)
│   ├── memory-stub/   # Simulates Memory API (Option B demo)
│   ├── orchestrator-stub/  # Simulates Orchestrator proxy (Option B demo)
│   └── setup-zero-trust/
├── pkg/
│   ├── bus/           # Pub/sub, request-reply, replay, subscribe (ACL)
│   ├── envelope/      # CloudEvents
│   ├── memory/        # MemoryClient, Mock, HTTP, Orchestrator proxy, Fallback
│   ├── security/      # ACL, NKey, OpenBao, TLS
│   └── streams/       # JetStream stream setup
├── internal/
│   ├── relay/         # Outbox relay
│   ├── health/        # Heartbeat
│   ├── http/          # /varz, /connz, /jsz proxy
│   └── metrics/       # Prometheus
├── tests/             # TC001–TC006, NFR, harness
├── scripts/           # demo, benchmark-validate, test-*
└── config/            # NATS, Prometheus, Grafana
```

### 3.2 Build

```bash
make build       # Builds bin/aegis-databus, aegis-demo, aegis-stubs
make test-unit   # Fast (no Docker)
make test        # All (integration needs Docker)
make bench       # Throughput, latency
```

- **Separation**: `cmd/` for entry points, `pkg/` for reusable logic, `internal/` for private details.
- **Testing**: Unit tests in `pkg/`, integration in `tests/` (testcontainers).

---

## 4. Best Practices

### 4.1 Security

#### OWASP Top 10 (2021) — web and API surfaces

DataBus is primarily **NATS messaging**; **HTTP** is used for health, metrics, NATS proxy, and audit. The following maps [OWASP Top 10 Web Application Security Risks](https://owasp.org/Top10/) to controls in this stack and adjacent operations.

| OWASP | Risk | How we address it |
|-------|------|-------------------|
| **A01** | Broken Access Control | Subject ACLs (`CheckPublish` / `CheckSubscribe`); DLQ subscribe restricted to admin role (SR-DB-006); least privilege for NKey users |
| **A02** | Cryptographic Failures | TLS to NATS; CA verification and optional mTLS; no message payload in audit logs (SR-DB-005); secrets from env or OpenBao, not committed configs |
| **A03** | Injection | Schema validation at publish boundary (`PublishValidated` / `PublishWithACL`); avoid building subjects from untrusted input |
| **A04** | Insecure Design | Outbox, DLQ, durable consumers, and ACL model are explicit design choices (sections 1–2) |
| **A05** | Security Misconfiguration | Secure Compose overlays (`docker-compose.secure.yml`, `docker-compose.tls.yml`); document env vars; reject plaintext when NKey is required (`test-plaintext-rejected.sh`) |
| **A06** | Vulnerable and Outdated Components | Track Go and module versions; rebuild images on security patches |
| **A07** | Identification and Authentication Failures | NKey per component; optional OpenBao for seeds; NATS connection auth |
| **A08** | Software and Data Integrity Failures | JetStream persistence and ack semantics; signed releases and image provenance are an org process |
| **A09** | Security Logging and Monitoring Failures | Prometheus metrics; audit metadata; trace IDs on CloudEvents; alert rules under `config/prometheus/` |
| **A10** | Server-Side Request Forgery (SSRF) | DataBus calls Memory/Orchestrator over configured base URLs only; restrict egress and URL allowlists in deployment |

#### TLS (SSL) — channel encryption and party validation

| Goal | Mechanism |
|------|-----------|
| **Encrypt the channel** | TLS to NATS (`tls://` / `nats+tls://` URLs and `nats.Secure`) |
| **Validate the server** | `AEGIS_NATS_TLS_CA` — trust anchor for the NATS server certificate (`TLSConfigFromEnv` → `RootCAs`) |
| **Validate the client** (optional) | **mTLS**: `AEGIS_NATS_TLS_CERT` and `AEGIS_NATS_TLS_KEY` — client certificate presented to the server |

Production should not rely on cleartext NATS; use TLS plus NKey (or deployment-specific auth) as documented in the [repository README](../../README.md) and `docker-compose.tls.yml`.

#### TLS version and cipher suites

- **Policy:** Use at least **TLS 1.2** in production for any TLS endpoint; **disable** SSLv3, TLS 1.0, and TLS 1.1.
- **This codebase:** NATS client config sets **`MinVersion: tls.VersionTLS13`** in `pkg/security/nkey.go`, which meets and exceeds that policy and uses Go’s modern default cipher suites for TLS 1.3.
- **Termination at a load balancer or ingress:** Configure **TLS 1.2+** with a **strong, modern cipher suite** list (e.g. ECDHE key exchange, **AES-GCM** or **ChaCha20-Poly1305**); disable weak or legacy ciphers and follow your cloud provider’s “TLS security policy” presets where available.

#### Web Application Firewall (WAF) for external HTTP

DataBus does **not** embed a WAF. Any **Internet-exposed** HTTP surface (metrics, health, admin, or future APIs) should sit behind an edge **WAF** and DDoS protection, for example:

- **AWS WAF** (in front of ALB/API Gateway/CloudFront)
- **Cloudflare** (proxy + WAF rules)
- **Sucuri** / similar managed WAF vendors

Internal-only deployments may rely on network segmentation instead; public endpoints should assume WAF + TLS at the edge.

#### Implementation practices

| Practice | Implementation |
|----------|----------------|
| **No payload in logs** | `ParseMetadata`; audit stores subject, size, correlationId, traceID only (SR-DB-005) |
| **Subject ACLs** | `CheckPublish`, `CheckSubscribe`; DLQ admin-only (SR-DB-006) |
| **NKey auth** | Per-component Ed25519; `setup-zero-trust`, OpenBao or env |
| **TLS 1.3 (client)** | `make up-tls`, `AEGIS_NATS_TLS_CA` for verification; optional mTLS env vars |
| **Plaintext rejected** | `scripts/test-plaintext-rejected.sh` with NKey-enabled NATS |

### 4.2 Deployment

- **Docker Compose**: 3-node NATS, Prometheus, Grafana.
- **Overlays**: `docker-compose.secure.yml` (NKey), `docker-compose.tls.yml` (TLS 1.3).
- **Config**: Env vars; no secrets in config files.

### 4.3 High Availability

| Mechanism | Implementation |
|-----------|----------------|
| **3-node cluster** | Raft; leader election &lt; 5s (FR-DB-007) |
| **Fallback client** | DEGRADED-HOLD when Memory down |
| **Retries** | Stream setup, outbox relay backoff |
| **Heartbeat** | `aegis.health.databus` for Self-Healing |
| **Reconnect** | NATS client `MaxReconnects(-1)`, exponential backoff |

### 4.4 Observability

- **Metrics**: `/metrics` (Prometheus)
- **Health**: `/healthz` for liveness
- **Audit**: `/audit` (metadata only)
- **NATS proxy**: `/varz`, `/connz`, `/jsz` (Interface 4)
- **TraceID**: `CloudEvent.TraceID`, `AuditLogEntry.TraceID` (Design Principle 4)

---

## 5. Functional Demo and Test Harness

### 5.1 Test Harness (tests/harness_test.go)

| Test | Requirement | What it verifies |
|------|-------------|------------------|
| TC001 | FR-DB-001 | Pub/sub latency &lt; 5ms |
| TC001b | FR-DB-001 | 100 msgs P99 &lt; 5ms |
| TC002 | FR-DB-004 | Queue group; ~33 msgs each of 100 |
| TC003 | FR-DB-003 | Durable consumer recovery |
| TC004 | FR-DB-008 | Outbox relay replay |
| TC005 | FR-DB-009, SR-DB-006 | DLQ after 5 NAKs; admin subscribe |
| TC006 | FR-DB-011 | Outbox zero-loss |
| FR-DB-002 | Request-reply | 5s timeout, reply received |
| FR-DB-006 | Priority | Health vs resource ordering |

### 5.2 Demo Utilities

- **`make demo`**: Build + run stubs.
- **`make demo-full`**: `scripts/demo.sh` orchestration.
- **`./bin/aegis-demo`**: 6-component flow (I/O, Orchestrator, Memory, Vault, Agent, Monitoring).
- **`./bin/aegis-stubs`**: Standalone stubs.

### 5.3 Scripts

| Script | Purpose |
|--------|---------|
| `benchmark-validate.sh` | NFR 50K msg/s, 5ms P99 |
| `test-cluster-failover.sh` | FR-DB-007 |
| `test-scaling.sh` | NFR-DB-010 (1.5× scaling) |
| `test-plaintext-rejected.sh` | SR-DB-001 |

### 5.4 Running the Demo

```bash
make up
./bin/aegis-databus &
./bin/aegis-demo
# Or: make demo-full
# Tests: make test-integration
```

---

## 6. Requirements Verification (Option 1 Compatible)

All EDD requirements remain intact with the Orchestrator proxy architecture:

| ID | Requirement | Status |
|----|-------------|--------|
| FR-DB-001 | Publish-Subscribe | ✓ Unchanged |
| FR-DB-002 | Request-Reply | ✓ Unchanged |
| FR-DB-003 | Durable Consumer | ✓ Unchanged |
| FR-DB-004 | Queue Groups | ✓ Unchanged |
| FR-DB-005 | Wildcard routing | ✓ Unchanged |
| FR-DB-006 | Priority | ✓ Unchanged |
| FR-DB-007 | Cluster failover | ✓ Unchanged |
| FR-DB-008 | Replay | ✓ Unchanged |
| FR-DB-009 | Ack & DLQ | ✓ Unchanged |
| FR-DB-010 | Schema validation | ✓ Unchanged |
| FR-DB-011 | Outbox pattern | ✓ Relay uses MemoryClient; OrchestratorStorageClient provides outbox/audit |
| SR-DB-003 | Subject ACL | ✓ Unchanged |
| SR-DB-005 | No payload in audit | ✓ Unchanged |
| SR-DB-006 | DLQ admin-only | ✓ Unchanged |
| NFR-DB-003 | Availability / DEGRADED-HOLD | ✓ FallbackClient wraps OrchestratorStorageClient |

---

## References

- EDD DataBus-EDD-Final.pdf (course artifact)
- [Repository README](../../README.md) — quick start, TLS, monitoring URLs
