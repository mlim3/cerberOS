# Container integration guide

This module ships **multi-stage Dockerfiles**, **Compose profiles**, and **hardening defaults** (non-root, `no-new-privileges`, read-only root where possible) so another team can ship the same stack in Kubernetes or a shared Docker host.

**Quick start and env vars:** [Repository README](../../README.md). **Architecture:** [DESIGN.md](DESIGN.md).

## Images

Build targets (see `Dockerfile`):

| Target | Purpose |
|--------|---------|
| `aegis-databus` | Main DataBus (`/metrics`, `/healthz` on `:9091`) |
| `aegis-demo` | Six-component EDD demo (NATS client only) |
| `aegis-stubs` | Alternate stub traffic |
| `aegis-memory-stub` | Memory API stub (`:8090`, `/v1/memory/ping`) |
| `aegis-orchestrator-stub` | Orchestrator proxy (`:8091`, `/healthz`) |

Example:

```bash
docker build --target aegis-databus -t aegis-databus:latest .
```

Multi-arch (buildx):

```bash
docker buildx build --platform linux/amd64,linux/arm64 --target aegis-databus -t your-registry/aegis-databus:v1 --push .
```

## Compose: NATS + observability + apps

**Infra only** (default):

```bash
docker compose up -d
```

**Apps** (DataBus, stubs, demo) on the same network as NATS:

```bash
docker compose -f docker-compose.yml -f docker-compose.apps.yml --profile apps up -d --build
```

**Prometheus scrape** in container DataBus (optional third file):

```bash
docker compose -f docker-compose.yml -f docker-compose.apps.yml -f docker-compose.prometheus-apps.yml --profile apps up -d --build
```

Optional: `aegis-stubs` profile `stubs` instead of/in addition to `demo`:

```bash
docker compose -f docker-compose.yml -f docker-compose.apps.yml --profile apps --profile stubs up -d --build
```

## Environment

See `.env.example`. Prefer **runtime env** and **mounted secrets** over baking credentials into images.

When DataBus runs **inside Compose**, set **`AEGIS_NATS_HTTP_URL=http://nats-1:8222`** so `/varz`, `/connz`, and `/jsz` on `:9091` proxy to NATS monitoring (defaults are set in `docker-compose.apps.yml`). On the **host**, `http://127.0.0.1:8222` is correct.

## Security checklist for integrators

- Run containers as **non-root** (images use UID/GID `65532`).
- Use **TLS** to NATS in production (`AEGIS_NATS_TLS_CA`, optional mTLS cert/key); **TLS 1.2+** and strong ciphers at any ingress or load balancer; see [DESIGN.md](DESIGN.md) §4.1.
- For **Internet-facing** HTTP (metrics, health, proxies), use a **WAF** at the edge (e.g. AWS WAF, Cloudflare, Sucuri), not inside this binary.
- Keep `read_only: true` where possible; use `tmpfs` for writable paths if needed.
- Supply **NKeys** via OpenBao or mounted secrets; never commit seeds.
- Scan images with your registry policy (Trivy, etc.) and pin base image digests in CI.

## Makefile helpers

```bash
make docker-build-all      # build all image targets locally
make docker-compose-apps   # print suggested compose command
```
