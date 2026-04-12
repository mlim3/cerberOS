# Plan: mTLS CA Bootstrap for Vault + Component Communication

## Context

All inter-component communication in cerberOS currently runs over plain HTTP/NATS with no transport encryption. The partner onboarding doc already lists mTLS as a requirement, but nothing is implemented. The goal is to add a **self-signed internal CA** that bootstraps mTLS so that:

1. Vault engine <-> OpenBao communication is encrypted and mutually authenticated
2. NATS connections use mTLS (components must present a valid client cert)
3. **Agents never get a client cert or direct Vault access** — the existing architecture already enforces this (agents talk only to Orchestrator via NATS), but mTLS makes it cryptographic rather than just a code convention

Everything should come up with a single `docker compose up` from the project root.

---

## Architecture

```
                    ┌─────────────────────────────────┐
                    │         step-ca (CA)             │
                    │  Issues all certs at boot via    │
                    │  init container / entrypoint     │
                    └──────────┬──────────────────────┘
                               │ CA root cert
                    ┌──────────┴──────────────────────┐
                    │      Shared volume: /certs       │
                    └──┬────────┬────────┬────────┬───┘
                       │        │        │        │
              ┌────────┴─┐ ┌───┴────┐ ┌─┴─────┐ ┌┴────────┐
              │ OpenBao   │ │ Vault  │ │ NATS  │ │ IO API  │
              │ server    │ │ engine │ │       │ │         │
              │ cert +    │ │ client │ │server │ │ client  │
              │ client    │ │ cert   │ │cert + │ │ cert    │
              │ verify    │ │        │ │client │ │ (NATS)  │
              └───────────┘ └────────┘ │verify │ └─────────┘
                                       └───────┘
              Agents Component: gets a NATS client cert
              but NO vault client cert — can only reach
              Orchestrator topics on NATS, never OpenBao.
```

---

## Decisions

- **Root-level compose**: Yes — a root `docker-compose.yml` with `include:` to bring up the full stack in one command.
- **NATS auth**: Simple CN-based mapping for now (see alternatives section below).
- **Postgres TLS**: Not in scope for this phase — noted as a future hardening step (see below).
- **CA mode**: Ephemeral for dev, with a documented path to persistent CA for production (see below).

---

## CA Modes: Ephemeral vs Persistent

### Ephemeral (default for dev)

The cert-init container generates a fresh root CA + all leaf certs on every `docker compose up`. Certs live in a named Docker volume (`certs`). Tearing down with `docker compose down -v` destroys them; next `up` regenerates everything. No secrets to manage, no key rotation concerns — ideal for local development.

### Persistent (production path)

For production or staging environments where services need stable identities across restarts:

1. **Pre-generate the root CA** offline (air-gapped machine or HSM)
2. **Mount the CA cert + key** as a Docker secret or bind-mount from a secure location (not checked into git)
3. **bootstrap.sh detects** an existing `ca.crt` + `ca.key` in the volume and skips generation — only issues/renews leaf certs
4. **Leaf cert rotation**: bootstrap.sh checks leaf cert expiry on startup; re-issues if within renewal window (e.g., <12h remaining on a 24h cert)
5. **CA key isolation**: in persistent mode, the CA key should be removed from the volume after leaf issuance (or kept in a separate, more restricted volume)

This mode is not implemented yet but the bootstrap.sh script should be written with this path in mind (check for existing CA before generating).

---

## Postgres TLS (future scope)

Currently `sslmode=disable` in two places:

- `vault/openbao.hcl` — OpenBao's Postgres storage backend connection
- `memory/` — memory service's Postgres connection

To add Postgres TLS later:

1. Issue a server cert for the Postgres container (SAN: `db`, `localhost`)
2. Update `pgvector` container to mount the cert and enable `ssl = on` in `postgresql.conf`
3. Update connection strings to `sslmode=verify-full` with `sslrootcert=/certs/ca.crt`
4. Optionally require client certs for Postgres connections (mTLS to the database)

This is lower priority than the OpenBao and NATS mTLS since Postgres is on an internal Docker network with no agent access, but it completes the defense-in-depth picture.

---

## Approach: `step` CLI + init container

Use **Smallstep's `step` CLI** (not the full CA server) in a lightweight init container that:

1. Generates an ECDSA root CA key + self-signed root cert (unless existing CA detected — see persistent mode)
2. Issues leaf certs for each service (OpenBao, vault-engine, NATS, io-api, agents)
3. Writes everything to a shared Docker volume (`certs`)
4. Exits — it's a one-shot init, not a running service

**Alternative considered:** `cfssl` or raw `openssl` commands. `step` is preferred because it has a single binary, sane defaults (ECDSA P-256, 24h leaf lifetime), and JSON-based config — less shell scripting than openssl.

---

## Implementation Steps

### Step 1: Add `infra/certs/` directory

```
infra/
├── certs/
│   ├── bootstrap.sh          # Init container entrypoint
│   └── Dockerfile            # Minimal image with step CLI
```

`bootstrap.sh` generates:

- `ca.crt` + `ca.key` (root CA) — skipped if already present (persistent mode)
- `openbao-server.crt` + `openbao-server.key` (SANs: openbao, localhost)
- `vault-engine.crt` + `vault-engine.key` (client cert for vault engine -> OpenBao)
- `nats-server.crt` + `nats-server.key` (SANs: nats, localhost)
- `nats-client-io.crt` + `nats-client-io.key` (CN: io)
- `nats-client-agents.crt` + `nats-client-agents.key` (CN: agents)
- Sets permissions (read-only for certs, restricted for keys)

### Step 2: Update `vault/openbao.hcl` for TLS

```hcl
listener "tcp" {
  address       = "0.0.0.0:8200"
  tls_cert_file = "/certs/openbao-server.crt"
  tls_key_file  = "/certs/openbao-server.key"
  tls_client_ca_file = "/certs/ca.crt"
  tls_require_and_verify_client_cert = true
}
```

Remove `tls_disable = 1`. Update `api_addr` to `https://`.

### Step 3: Update `vault/compose.yaml`

- Add `cert-init` service (runs bootstrap.sh, exits)
- Add shared `certs` volume
- Mount `certs` volume into `openbao` and `vault` services
- Update `BAO_ADDR` to `https://openbao:8200`
- Add env vars for vault engine: `VAULT_CACERT=/certs/ca.crt`, `VAULT_CLIENT_CERT=/certs/vault-engine.crt`, `VAULT_CLIENT_KEY=/certs/vault-engine.key`
- Add `depends_on: cert-init: condition: service_completed_successfully`

### Step 4: Add NATS TLS config

Create `infra/nats-server.conf`:

```
jetstream {}
store_dir: /data

tls {
  cert_file: "/certs/nats-server.crt"
  key_file: "/certs/nats-server.key"
  ca_file: "/certs/ca.crt"
  verify_and_map: true
}

authorization {
  users = [
    { user: "agents", permissions: { publish: "aegis.orchestrator.>", subscribe: "aegis.agents.>" } }
    { user: "io",     permissions: { publish: "aegis.io.>",          subscribe: "aegis.io.>" } }
  ]
}
```

`verify_and_map: true` maps the client cert CN to the NATS user — agents get their CN ("agents") mapped to the restricted permission set. No vault topics are subscribable by agents.

### Step 5: Update `agents-component/docker-compose.yml`

- Mount `certs` volume
- Add NATS TLS env or config: point to `nats-client-agents.crt/key` and `ca.crt`
- Update NATS URL to `tls://nats:4222`

### Step 6: Update `io/docker-compose.yml`

- Mount `certs` volume
- Add NATS TLS config pointing to `nats-client-io.crt/key` and `ca.crt`

### Step 7: Update `vault/bootstrap-up.sh`

- Update `BAO_ADDR` to `https://127.0.0.1:8200`
- Add `--cacert` / `--cert` / `--key` flags to all `curl` commands
- Or use `bao` CLI with `VAULT_CACERT`, `VAULT_CLIENT_CERT`, `VAULT_CLIENT_KEY` env vars

### Step 8: Add root-level `docker-compose.yml`

A root-level compose file that brings up the full stack:

```yaml
# docker-compose.yml (root)
include:
  - memory/docker-compose.yml
  - vault/compose.yaml
  - agents-component/docker-compose.yml
  - io/docker-compose.yml

services:
  cert-init:
    build: infra/certs
    volumes:
      - certs:/certs

volumes:
  certs:
```

All other compose files reference `certs` as an external volume. One `docker compose up` brings everything up in dependency order.

---

## What the agent CANNOT do (enforced cryptographically)

1. **No vault client cert** — the agents service gets `nats-client-agents.crt`, not `vault-engine.crt`. It literally cannot complete a TLS handshake with OpenBao.
2. **NATS topic restriction** — NATS `verify_and_map` maps the agents cert CN to a user that can only publish to `aegis.orchestrator.>` and subscribe to `aegis.agents.>`. It cannot subscribe to vault response topics meant for other components.
3. **No CA key access** — the CA key exists only in the init container and the certs volume (with restrictive file permissions). The agents container mounts only its own cert + key + the CA public cert.

---

## NATS Authorization: Simple vs Account-Based

### Current plan: CN mapping (simple)

`verify_and_map: true` maps the client certificate's Common Name (CN) to a NATS user. Each user has a static permission block defining which subjects they can publish/subscribe to. This is configured in a single `nats-server.conf` file.

**Pros:** Simple, easy to reason about, no additional tooling needed.
**Cons:** Flat permission model — all agents share one identity. No per-agent isolation on the NATS layer. Adding a new component means editing the config file and restarting NATS.

### Alternative: NATS Accounts + NKeys

NATS has a built-in multi-tenant account system:

- Each component gets its own **account** (agents, io, orchestrator, vault)
- Accounts are isolated by default — messages in one account are invisible to others
- Cross-account communication uses **exports/imports** — the orchestrator account exports specific subjects, and each component account imports only what it needs
- Authentication uses **NKeys** (Ed25519 keypairs) instead of or alongside client certs
- Managed via `nsc` CLI tool, which generates account JWTs

**Pros:**

- True multi-tenant isolation (agents can't even see other accounts' subjects)
- Per-agent NKeys possible (each Firecracker VM gets a unique key)
- Dynamic — account JWTs can be pushed without NATS restart
- Better audit trail (per-account connection metrics)

**Cons:**

- More complex to set up and understand
- Requires `nsc` tooling in the bootstrap flow
- Overkill for the current number of components

**Recommendation:** Start with CN mapping. Migrate to accounts when we need per-agent isolation (i.e., when Firecracker VMs are running and each agent needs its own NATS identity). The migration path is additive — we can layer accounts on top of TLS without removing mTLS.

---

## Files to create/modify

| File                                  | Action                                                                   |
| ------------------------------------- | ------------------------------------------------------------------------ |
| `infra/certs/Dockerfile`              | **Create** — minimal image with `step` CLI                               |
| `infra/certs/bootstrap.sh`            | **Create** — cert generation script (supports ephemeral + persistent CA) |
| `infra/nats-server.conf`              | **Create** — NATS TLS + authorization config                             |
| `vault/openbao.hcl`                   | **Modify** — enable TLS, add client cert verification                    |
| `vault/compose.yaml`                  | **Modify** — add cert-init, certs volume, HTTPS env vars                 |
| `vault/bootstrap-up.sh`               | **Modify** — use HTTPS + client certs for curl/bao commands              |
| `agents-component/docker-compose.yml` | **Modify** — mount certs, TLS NATS URL                                   |
| `io/docker-compose.yml`               | **Modify** — mount certs, TLS NATS URL                                   |
| `docker-compose.yml` (root)           | **Create** — top-level compose with `include`                            |

---

## Verification

1. `docker compose up` from project root — all services start, cert-init exits 0
2. `docker compose exec openbao bao status` — shows initialized, unsealed, TLS enabled
3. `curl --cacert certs/ca.crt --cert certs/vault-engine.crt --key certs/vault-engine.key https://localhost:8200/v1/sys/health` — 200
4. `curl https://localhost:8200/v1/sys/health` (no client cert) — TLS handshake failure
5. Agents container logs show successful NATS TLS connection
6. Agents container cannot connect to OpenBao (no client cert for it)
7. NATS monitoring shows TLS-verified connections from both io and agents clients
