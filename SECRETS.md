# Secrets reference

How every secret/credential flows from your shell or [`.env`](.env.example) into the
running stack — both `docker compose` (via [`bootstrap.sh`](bootstrap.sh)) and
`kind` (via [`k8s/scripts/kind-up.sh`](k8s/scripts/kind-up.sh)).

Three buckets:

1. **Wired from `.env`** — set them in `.env` (or export in your shell) and both
   stacks pick them up.
2. **Auto-generated if missing** — bootstrap and kind-up generate a strong value
   and persist it to `.env` so subsequent runs (and both stacks) reuse it.
3. **Not wired through `.env`** — accepted by the stack but currently undocumented
   in `.env.example`; either set via shell export or, for k8s, via `helm --set`.

---

## 1. Wired from `.env`

| Key | docker-compose target | k8s target (Secret) | Notes |
|---|---|---|---|
| `POSTGRES_USER` | `memory-db` env | `postgres.auth.user` (hardcoded `user` in k8s — see gaps) | Default `user` |
| `POSTGRES_PASSWORD` | `memory-db` env | `postgres-secrets` + `memory-api-secrets.DB_PASSWORD` | Default `password` |
| `POSTGRES_DB` | `memory-db` env | hardcoded `memory_db` in k8s (see gaps) | Default `memory_db` |
| `BAO_TOKEN` | written by `bootstrap.sh` after init/unseal | `vault-engine-secrets.BAO_TOKEN` | k8s dev defaults to `"root"` (devMode OpenBao) |
| `ANTHROPIC_API_KEY` | `aegis-agents` env + seeded into OpenBao | `aegis-agents-secrets.ANTHROPIC_API_KEY` | Required for agent LLM |
| `ANTHROPIC_BASE_URL` | `aegis-agents` env | `aegis-agents` plain env | Optional proxy URL |
| `HF_TOKEN` | `embedding-api` env | `embedding-api-secrets.HF_TOKEN` (via `global.embedding.hfToken`) | Required only for gated HF models |
| `TAVILY_API_KEY` | seeded into OpenBao | `vault-engine-secrets.TAVILY_API_KEY` | web_search skill |
| `OPENAI_API_KEY` | seeded into OpenBao + `memory-api`/`io`/`vault` envs | `cerberos-shared-secrets.OPENAI_API_KEY` | memory embedder + optional GPT skills |
| `GOOGLE_CLIENT_ID` | `io` + `vault` env | `cerberos-shared-secrets.GOOGLE_CLIENT_ID` | Admin UI "Connect Google" |
| `GOOGLE_CLIENT_SECRET` | `io` + `vault` env | `cerberos-shared-secrets.GOOGLE_CLIENT_SECRET` | Admin UI "Connect Google" |
| `GOOGLE_REDIRECT_URI` | `io` env | `cerberos-shared-secrets.GOOGLE_REDIRECT_URI` | OAuth callback URL |
| `GMAIL_DEMO_EMAIL` | seeded into OpenBao | `vault-engine-secrets.GMAIL_DEMO_EMAIL` | gmail_send / calendar_create_event |
| `GMAIL_DEMO_APP_PASSWORD` | seeded into OpenBao | `vault-engine-secrets.GMAIL_DEMO_APP_PASSWORD` | App Password (16 chars, spaces stripped) |

For both stacks: leave blank for keys you don't need — the feature that consumes
each key fails gracefully (web_search → unavailable, OAuth flows → disabled, etc).

---

## 2. Auto-generated if missing

These are required for the stack to function, so both `bootstrap.sh` and
`kind-up.sh` will generate one on first run and persist it to `.env` (via the
same `upsert_env_var` helper). After the first run, the same value lives in
`.env` for both stacks.

| Key | Generator | Constraint | Persisted to |
|---|---|---|---|
| `VAULT_MASTER_KEY` | `openssl rand -base64 24 \| head -c 32` | Exactly 32 ASCII chars (raw bytes, not hex) | `.env` |
| `INTERNAL_VAULT_API_KEY` | `openssl rand -hex 32` | Any strong random string | `.env` |

Code references:
- bootstrap path: [bootstrap.sh](bootstrap.sh) lines 162–174
- kind-up path: [k8s/scripts/kind-up.sh](k8s/scripts/kind-up.sh) (post `.env`-sourcing block)

**Why both?** If you switch between `docker compose` and `kind` against the
same repo, you don't want two different `INTERNAL_VAULT_API_KEY` values racing
between the two clusters and breaking the memory-api / io handshake.

---

## 3. Not wired through `.env` (yet)

These are accepted by the stack but **not** documented in
[`.env.example`](.env.example). You can still set them via shell export
before `bootstrap.sh` / `kind-up.sh`, or pass them directly to Helm.

| Key | Consumer | How to set today |
|---|---|---|
| `CRON_WAKE_SECRET` | `orchestrator` POST `/v1/cron/wake` (cron-triggered planner) | `export CRON_WAKE_SECRET=$(openssl rand -hex 16)` before bootstrap / kind-up. kind-up will pre-create `orchestrator-cron-wake` Secret. |
| `PLAN_APPROVAL_MODE` | `orchestrator` plan-approval gate (`off` / `manager` / `admin`) | `export PLAN_APPROVAL_MODE=manager` or `helm --set orchestrator.planApprovalMode=manager` |
| `SKILL_LOAD_ALLOWED_USERS` | `orchestrator` skill-load allowlist (comma-separated user ids) | `export SKILL_LOAD_ALLOWED_USERS=u1,u2` or `helm --set orchestrator.skillLoadAllowedUsers=u1,u2` |

Add them to `.env.example` if you want them surfaced as first-class config.

---

## Known gaps / parity caveats

- **`POSTGRES_USER` / `POSTGRES_DB`**: docker-compose honors these from `.env`;
  k8s hardcodes `user` / `memory_db` in
  [k8s/helm/charts/postgres/values.yaml](k8s/helm/charts/postgres/values.yaml).
  Change the values via `--set postgres.auth.user=<x>` and
  `--set postgres.auth.database=<x>` if you need to override in kind.
- **OpenBao in kind runs in `devMode`**: in-memory storage, auto-unsealed, root
  token fixed to `"root"`. The full `bootstrap.sh` init+unseal flow is
  docker-compose only. To use a persisted OpenBao in kind, set
  `openbao.devMode=false` and supply a real `BAO_TOKEN`.
- **`MEMORY_API_KEY`**: not a separate secret — both stacks mirror
  `INTERNAL_VAULT_API_KEY` into `MEMORY_API_KEY` on the `io` container so
  `/api/user-crons` can proxy to memory-api with the same shared key.

---

## Layout (k8s Secrets)

```
cerberos-shared-secrets         ← umbrella chart (cross-chart values)
  INTERNAL_VAULT_API_KEY        →  memory-api, io
  OPENAI_API_KEY                →  memory-api, io, vault-engine
  GOOGLE_CLIENT_ID              →  io, vault-engine
  GOOGLE_CLIENT_SECRET          →  io, vault-engine
  GOOGLE_REDIRECT_URI           →  io

memory-api-secrets              ← per-chart, single owner
  DB_PASSWORD
  VAULT_MASTER_KEY

vault-engine-secrets            ← per-chart, single owner
  BAO_TOKEN
  TAVILY_API_KEY
  GMAIL_DEMO_EMAIL
  GMAIL_DEMO_APP_PASSWORD

aegis-agents-secrets            ← per-chart, rendered only when set
  ANTHROPIC_API_KEY

embedding-api-secrets           ← per-chart, rendered only when set
  HF_TOKEN

postgres-secrets                ← per-chart
  POSTGRES_USER
  POSTGRES_PASSWORD
  POSTGRES_DB

orchestrator-cron-wake          ← created out-of-band by kind-up.sh when CRON_WAKE_SECRET set
  CRON_WAKE_SECRET
```

All `Deployment`s consume these via `secretKeyRef` — no plaintext sensitive
values remain in Deployment env. Verify with:

```bash
helm template cerberos k8s/helm/cerberos -f k8s/helm/cerberos/values-dev.yaml \
  | grep -B1 -E 'name: (VAULT_MASTER_KEY|INTERNAL_VAULT_API_KEY|BAO_TOKEN|ANTHROPIC_API_KEY|OPENAI_API_KEY|GOOGLE_CLIENT_SECRET|GMAIL_DEMO_APP_PASSWORD|TAVILY_API_KEY|DB_PASSWORD)$'
# Every match should be followed by `valueFrom: secretKeyRef:`, not a literal `value:`.
```
