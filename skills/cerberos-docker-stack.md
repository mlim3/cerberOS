# cerberOS Docker Stack

## When to use
When starting, stopping, or troubleshooting the cerberOS development stack.

## Prerequisites
- Docker Desktop (or Docker Engine + Compose v2)
- Copy `.env.example` to `.env` and fill in required values

## Quick start

```bash
# Full stack (core services)
docker compose up --build

# Core only (no memory/vault)
docker compose up --build nats orchestrator io

# With agents
docker compose --profile agents up --build

# Detached
docker compose up -d --build

# Tear down (preserves volumes)
docker compose down

# Tear down and delete volumes
docker compose down -v
```

## Service startup order

1. **nats** — JetStream broker; everything depends on this
2. **memory-db** — Postgres (pgvector); needed by memory-api and openbao
3. **memory-api** — waits for memory-db healthcheck
4. **openbao** — waits for memory-db healthcheck (storage backend)
5. **vault** — waits for openbao
6. **orchestrator** — waits for nats healthcheck
7. **io** — waits for nats healthcheck

## OpenBao bootstrap (first run)

OpenBao requires manual init + unseal. After the stack is running:

```bash
# From repo root — the script creates the openbao DB, initializes, unseals,
# and writes BAO_TOKEN to vault/.env
cd vault && ./bootstrap-up.sh

# Copy the generated BAO_TOKEN into your root .env, then restart vault:
docker compose restart vault
```

## Common issues

### NATS not ready
Orchestrator or IO crash-loops with "connection refused". Wait for NATS healthcheck
or check: `curl http://localhost:8222/healthz`

### memory-api exits immediately
Missing `VAULT_MASTER_KEY` or `INTERNAL_VAULT_API_KEY` in `.env`.
Generate with: `openssl rand -hex 32`

### OpenBao sealed after restart
OpenBao does not auto-unseal in dev mode. Re-run `cd vault && ./bootstrap-up.sh`
or manually unseal with the key from `vault/.openbao-init.json`.

### Port conflicts
Check `docker compose ps` for port bindings. See `cerberos-service-ports.md` for the full map.

## Profiles

| Profile | Services added | Use case |
|---------|---------------|----------|
| _(default)_ | nats, orchestrator, io, memory-db, memory-api, openbao, vault, swagger | Full-stack dev |
| `agents` | simulator, aegis-agents | Agent lifecycle testing |

## DataBus (separate stack)

The aegis-databus component uses a 3-node NATS cluster and is not included in the root compose.
Run it standalone:

```bash
cd aegis-databus
docker compose up -d
docker compose -f docker-compose.apps.yml --profile apps up -d
```
