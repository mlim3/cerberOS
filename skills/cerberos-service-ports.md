# cerberOS Service Ports

## When to use

When configuring service-to-service communication, debugging connectivity, or checking for port conflicts.

## Host port map

| Service                  | Container port | Host port | URL                                               |
| ------------------------ | -------------- | --------- | ------------------------------------------------- |
| Grafana                  | 3000           | 3000      | http://localhost:3000 (profile: observability)    |
| io (API/UI)              | 3001           | 3001      | http://localhost:3001                             |
| NATS client (nats-1)     | 4222           | 4222      | nats://localhost:4222                             |
| NATS client (nats-2)     | 4222           | 4223      | nats://localhost:4223                             |
| NATS client (nats-3)     | 4222           | 4224      | nats://localhost:4224                             |
| NATS exporter            | 7777           | 7777      | http://localhost:7777 (profile: observability)    |
| vault engine             | 8000           | 8000      | http://localhost:8000                             |
| orchestrator health      | 8080           | 8080      | http://localhost:8080                             |
| memory API               | 8081           | 8081      | http://localhost:8081/api/v1/healthz              |
| swagger (vault)          | 8080           | 8082      | http://localhost:8082                             |
| OpenBao                  | 8200           | 8200      | http://localhost:8200                             |
| NATS monitoring (nats-1) | 8222           | 8222      | http://localhost:8222                             |
| Postgres                 | 5432           | 5432      | postgres://user:password@localhost:5432/memory_db |
| Prometheus               | 9090           | 9090      | http://localhost:9090 (profile: observability)    |
| DataBus health/metrics   | 9091           | 9091      | http://localhost:9091/healthz                     |
| agents metrics           | 9090           | 9190      | http://localhost:9190/metrics (profile: agents)   |

## Internal DNS names (container-to-container)

| Hostname                  | Service           | Typical env var                                                  |
| ------------------------- | ----------------- | ---------------------------------------------------------------- |
| `nats-1` (alias: `nats`)  | NATS node 1       | `NATS_URL=nats://nats:4222` or `nats://nats-1:4222`              |
| `nats-2`                  | NATS node 2       | —                                                                |
| `nats-3`                  | NATS node 3       | —                                                                |
| `orchestrator`            | Orchestrator      | —                                                                |
| `io`                      | IO component      | `IO_API_BASE=http://io:3001`                                     |
| `memory-db` (alias: `db`) | Postgres          | `DB_HOST=memory-db`                                              |
| `memory-api`              | Memory API        | `MEMORY_ENDPOINT=http://memory-api:8081`                         |
| `openbao`                 | OpenBao           | `VAULT_ADDR=http://openbao:8200`, `BAO_ADDR=http://openbao:8200` |
| `vault`                   | Vault engine      | —                                                                |
| `swagger`                 | Swagger UI        | —                                                                |
| `aegis-databus`           | DataBus           | —                                                                |
| `simulator`               | Partner simulator | (profile: agents)                                                |
| `aegis-agents`            | Agents component  | (profile: agents)                                                |
