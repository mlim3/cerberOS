# aegis-databus

## Standalone development

The compose files in this directory are for **standalone databus development only** — they spin up isolated NATS clusters and stub services that are not part of the main stack.

| File | Purpose |
|------|---------|
| `docker-compose.yml` | 3-node NATS cluster for databus standalone dev |
| `docker-compose.apps.yml` | DataBus + stubs (orchestrator-stub, memory-stub, demo containers) |
| `docker-compose.prometheus-apps.yml` | Prometheus volume overlay for containerized DataBus |
| `docker-compose.secure.yml` | NKey auth overlay for NATS nodes |
| `docker-compose.tls.yml` | TLS 1.3 overlay for NATS nodes |

## Full stack

The root `docker-compose.yml` is the **source of truth** for the full cerberOS stack. Use it for all non-databus development:

```bash
# From repo root
docker compose up --build
docker compose --profile observability up --build
docker compose --profile agents up --build
```
