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
docker compose up --build nats-1 orchestrator io

# With agents
docker compose --profile agents up --build

# With observability (Prometheus + Grafana + Loki + Promtail + NATS exporter)
docker compose --profile observability up --build

# Detached
docker compose up -d --build

# Tear down (preserves volumes)
docker compose down

# Tear down and delete volumes
docker compose down -v
```

## Service startup order

1. **nats-1** — single-node JetStream; everything depends on nats-1
2. **memory-db** — Postgres (pgvector); needed by memory-api and openbao
3. **memory-api** — waits for memory-db healthcheck
4. **openbao** — waits for memory-db healthcheck (storage backend)
5. **vault** — waits for openbao
6. **orchestrator** — waits for nats healthcheck
7. **io** — waits for nats healthcheck

## Bootstrap (first run)

A single script handles everything — prerequisites, secrets, stack startup, and OpenBao init + unseal:

```bash
./bootstrap.sh          # build, start, init + unseal OpenBao
./bootstrap.sh down     # stop stack, drop openbao database
./bootstrap.sh down --keep-db        # stop but keep openbao database
./bootstrap.sh down --delete-volumes # stop and remove Docker volumes
```

## Common issues

### NATS not ready

Orchestrator or IO crash-loops with "connection refused". Wait for NATS healthcheck
or check: `curl http://localhost:8222/healthz`

### memory-api exits immediately

Missing `VAULT_MASTER_KEY` or `INTERNAL_VAULT_API_KEY` in `.env`.
Generate with: `openssl rand -hex 32`

### OpenBao sealed after restart

OpenBao does not auto-unseal in dev mode. Re-run `./bootstrap.sh`
or manually unseal with the key from `vault/.openbao-init.json`.

### Port conflicts

Check `docker compose ps` for port bindings. See `cerberos-service-ports.md` for the full map.

## Profiles

| Profile         | Services added                                                                 | Use case                             |
| --------------- | ------------------------------------------------------------------------------ | ------------------------------------ |
| _(default)_     | nats-1, orchestrator, io, memory-db, memory-api, openbao, vault, aegis-databus | Full-stack dev                       |
| `agents`        | simulator, aegis-agents                                                        | Agent lifecycle testing              |
| `observability` | nats-exporter, prometheus, grafana, loki, promtail                             | Monitoring dashboards + log pipeline |
