# cerberOS

Autonomous AI operating system with NATS-based messaging, memory, credential vault, and agent orchestration.

## Architecture

```
┌─────────┐    ┌──────────────┐    ┌─────────────┐
│   io    │◄──►│  nats-1      │◄──►│ orchestrator│
│ (UI/API)│    │ (JetStream)  │    │             │
└─────────┘    └──────┬───────┘    └──────┬──────┘
                      │                   │
               ┌──────┴───────┐    ┌──────┴──────┐
               │ aegis-databus│    │ memory-api  │
               └──────────────┘    └──────┬──────┘
                                          │
         ┌────────────┐            ┌──────┴──────┐
         │  openbao   │◄─────────►│  memory-db  │
         │  (vault)   │           │ (pgvector)  │
         └─────┬──────┘           └─────────────┘
               │
         ┌─────┴──────┐
         │vault engine│
         └────────────┘
```

## Prerequisites

- Docker Desktop (or Docker Engine + Compose v2)
- Copy `.env.example` to `.env` and fill in required values:
  ```bash
  cp .env.example .env
  # Generate secrets:
  openssl rand -base64 24 | head -c 32  # → VAULT_MASTER_KEY
  openssl rand -hex 32                  # → INTERNAL_VAULT_API_KEY
  ```
- **`ANTHROPIC_API_KEY`** — required for agent task execution. Set in `.env` before starting the stack.
- **`TAVILY_API_KEY`** — required for the `web_search` skill. Obtain a key from [tavily.com](https://tavily.com), add it to `.env`, then run `./bootstrap.sh` so it is seeded into OpenBao. Without it, any agent task that invokes web search will fail with a scope/credential error. Free-tier keys work.

## Quick start

```bash
# Full stack (core services)
docker compose up --build

# Core only (no memory/vault)
docker compose up --build nats-1 orchestrator io

# With agents
docker compose --profile agents up --build

# With observability (Prometheus, Grafana, Loki, Promtail, NATS exporter)
docker compose --profile observability up --build

# Detached
docker compose up -d --build

# Tear down (preserves volumes)
docker compose down

# Tear down and delete volumes
docker compose down -v
```

## Running on Kubernetes

Each service runs in its own pod distributed across nodes (cloud-ready). Requires `kind`, `kubectl`, and `helm`.

```bash
# One command: create cluster, build images, install everything
./deploy/scripts/kind-up.sh

# Web UI → http://localhost:3001
# Grafana → http://localhost:3000 (admin/admin)
```

Full guide including extension recipes (HA NATS, Ingress+TLS, NetworkPolicies, Firecracker, managed cloud): **[deploy/k8s-README.md](deploy/k8s-README.md)**

---

## Bootstrap (first run)

A single script handles prerequisites, secrets generation, stack startup, and OpenBao init + unseal:

```bash
./bootstrap.sh
```

This creates `.env` (if missing), generates `VAULT_MASTER_KEY` and `INTERNAL_VAULT_API_KEY`, starts all services, initializes and unseals OpenBao, and writes `BAO_TOKEN` to `.env`.

To tear down:

```bash
./bootstrap.sh down                # stop stack, drop openbao database
./bootstrap.sh down --keep-db      # stop stack, keep openbao database
./bootstrap.sh down --delete-volumes  # stop stack, remove Docker volumes
```

## Services

| Service           | Port       | Description                              |
| ----------------- | ---------- | ---------------------------------------- |
| **io**            | 3001       | API and web UI                           |
| **orchestrator**  | 8080       | Control plane / task orchestration       |
| **nats-1**        | 4222, 8222 | NATS JetStream messaging + monitoring    |
| **memory-db**     | 5432       | Postgres with pgvector                   |
| **memory-api**    | 8081       | Memory storage and retrieval API         |
| **openbao**       | 8200       | Secret management (HashiCorp Vault fork) |
| **vault engine**  | 8000       | Vault abstraction API                    |
| **aegis-databus** | 9091       | Event routing and metrics                |

### Optional profiles

| Profile         | Services                                           | Port(s)                |
| --------------- | -------------------------------------------------- | ---------------------- |
| `agents`        | simulator, aegis-agents                            | 9190 (metrics)         |
| `observability` | prometheus, grafana, loki, promtail, nats-exporter | 9090, 3000, 3100, 7777 |

## Service startup order

1. **nats-1** — single-node JetStream; everything depends on it
2. **memory-db** — Postgres; needed by memory-api and openbao
3. **memory-api** — waits for memory-db healthcheck
4. **openbao** — waits for memory-db healthcheck (storage backend)
5. **vault** — waits for openbao
6. **orchestrator** — waits for nats healthcheck
7. **io** — waits for nats healthcheck

## Project structure

```
cerberOS/
├── docker-compose.yml    # Single source of truth for the full stack
├── .env.example          # Environment variable template
├── orchestrator/         # Control plane service
├── io/                   # User-facing API and web UI
├── memory/               # Memory API + DB migrations
├── vault/                # OpenBao config + vault engine
├── aegis-databus/        # Event bus / data routing
├── agents-component/     # Agent lifecycle (profile: agents)
├── scripts/              # Shared bootstrap scripts
├── design_docs/          # Architecture and design documents
└── skills/               # Claude Code skill definitions
```

## Common issues

| Problem                                               | Fix                                                                                                |
| ----------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| Orchestrator/IO crash-loops with "connection refused" | NATS not ready — wait for healthcheck or check `curl http://localhost:8222/healthz`                |
| memory-api exits immediately                          | Missing `VAULT_MASTER_KEY` or `INTERNAL_VAULT_API_KEY` in `.env`                                   |
| `web_search` skill returns credential error           | `TAVILY_API_KEY` not set in `.env` or not seeded into OpenBao — add the key then re-run `./bootstrap.sh` |
| OpenBao sealed after restart                          | Re-run `./bootstrap.sh` or manually unseal with key from `vault/.openbao-init.json` |
| Port conflicts                                        | Run `docker compose ps` to check bindings; see `skills/cerberos-service-ports.md` for full map     |
