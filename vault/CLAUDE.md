# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

cerberOS Vault is a **credential broker** for agents. Agents send shell scripts containing `{{PLACEHOLDER}}` markers; the service resolves secrets and returns the **injected script** over HTTP for the agent to run locally. Resolution is **atomic**: if any referenced secret is missing or denied, the whole request fails with no partial substitution.

The HTTP service (`engine/main.go`) uses an in-memory `SecretManager` mock by default. **OpenBao** is included in Docker Compose for persistent secrets storage and can be wired to replace the mock (see `setup-openbao.sh`, `openbao.hcl`).

## Commands

### Build & run (from `vault/`)

`compose.yaml` attaches services to the external Docker network `memory_default`. Create it first if needed (for example by starting the [`memory`](../memory) stack, which defines that network), then:

```bash
docker compose build
docker compose up
```

**Services:** `vault` (:8000, Go HTTP API), `ui` (:80, static UI + nginx proxy to vault), `openbao` (:8200), `swagger` (:8080, serves `openapi.yaml`).

### OpenBao bootstrap (optional)

From `vault/`, after memory’s Postgres is available:

```bash
./setup-openbao.sh
```

This initializes OpenBao against Postgres, unseals, and mounts a KV v2 engine. Details: `setup-openbao.sh`.

### Tests

Unit tests (no Docker):

```bash
cd engine && go test ./cmd/vault/
cd engine && go test -v ./cmd/vault/
```

Integration tests (build tag `integration`; brings up `vault/compose.yaml` via Docker):

```bash
cd engine && go test -tags integration -timeout 5m ./cmd/vault/
# Single test:
cd engine && go test -tags integration -v -timeout 5m -run TestIntegration_InlineInject ./cmd/vault/
```

### UI development

```bash
cd internal-ui && npm run dev    # or: bun run dev
cd internal-ui && npm run lint
cd internal-ui && npm run build
```

Production UI is served by nginx in the `ui` image (`internal-ui/Dockerfile` + `nginx.conf`); `/inject` is proxied to the vault container.

## Architecture

### Request flow

```
Agent / CLI → POST /inject → preprocessor (placeholder scan + batch resolve)
                                    │
                                    ▼
                            SecretManager (mock today; pluggable)
                                    │
                                    ▼
                         JSON: injected script (or 403 on failure)
```

The **CLI** (`engine/cmd/vault/`) posts to `/inject` and prints or writes the returned script.

### Layout

| Path                    | Role                                             |
| ----------------------- | ------------------------------------------------ |
| `engine/main.go`        | HTTP server, `/inject`                           |
| `engine/preprocessor/`  | `{{KEY}}` parsing and substitution               |
| `engine/secretmanager/` | `SecretManager` interface + mock                 |
| `engine/audit/`         | Audit events (key names, not values)             |
| `engine/cmd/vault/`     | `vault inject` CLI                               |
| `internal-ui/`          | Next.js app + nginx for static + `/inject` proxy |

### Design notes

- **Strategy** — `SecretManager` / `preprocessor.SecretStore` allow swapping mock for OpenBao, cloud KMS, etc., without changing the preprocessor.
- **Pipeline** — collect keys → single `Resolve` → audit → substitute.

See `docs/architecture.md` for architecture, patterns, and historical QEMU notes. `engine/README.md` covers the HTTP API and CLI in detail.

## Key implementation notes

### Go dependencies

`engine/` uses **Go 1.24** and **stdlib only** (`go.mod`). Do not add third-party Go modules without strong justification.

### Audit and safety

- Audit logs record **secret key names**, not values (`engine/audit/`).
- Failed resolution returns **403** with a JSON `error` field; no partial injection.

### Environment

The running server does not require extra env vars for the mock path. When wiring a real backend, configuration will live alongside `main.go` / `secretmanager` (see `engine/README.md`).

## Testing approach

- **Unit tests** (`engine/cmd/vault/main_test.go`) mock `/inject` with `httptest` — fast, no Docker.
- **Integration tests** (`engine/cmd/vault/integration_test.go`, `integration` tag) run `docker compose` against `vault/compose.yaml` and exercise the real `vault inject` CLI against the container.
