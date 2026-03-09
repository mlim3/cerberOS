# Memory Service Demos

This folder contains runnable demos for the main Memory API flows.

## What is included

- `00_prepare.sh`
- Starts Docker Postgres (`docker compose up -d`) and seeds demo users in `identity_schema.users`.
- `01_health.sh`
- Calls `GET /api/v1/healthz`.
- `02_chat_idempotency.sh`
- Demonstrates chat write, idempotent replay, and conflict on mismatched replay.
- `03_personal_info_lifecycle.sh`
- Demonstrates save -> semantic query -> export all -> update fact -> delete fact.
- `04_agent_logs.sh`
- Demonstrates agent execution logging and timeline retrieval with limit.
- `05_system_and_vault.sh`
- Demonstrates system event logging plus vault unauthorized/authorized access behavior.
- `run_all.sh`
- Runs all demos except preparation.

## Prerequisites

- `docker`
- `curl`
- `jq`
- `uuidgen`
- Memory API server running on `http://localhost:8080` with:
- `VAULT_MASTER_KEY`
- `INTERNAL_VAULT_API_KEY` (default expected by demos: `test-vault-key`)

## Quick start

From `/Users/colbydobson/cs/cerberOS/memory`:

```bash
chmod +x demo/*.sh
./demo/00_prepare.sh

# in another terminal, start API server if not already running
VAULT_MASTER_KEY=0123456789abcdef0123456789abcdef \
INTERNAL_VAULT_API_KEY=test-vault-key \
go run ./cmd/server

# run demos
./demo/run_all.sh
```

## Useful environment overrides

- `BASE_URL` (default: `http://localhost:8080/api/v1`)
- `VAULT_API_KEY` (default: `test-vault-key`)
- `DEMO_USER_ID`
- `OTHER_USER_ID`
- `DEMO_SESSION_ID`
- `DEMO_TASK_ID`

Example:

```bash
BASE_URL=http://localhost:8080/api/v1 VAULT_API_KEY=my-key ./demo/05_system_and_vault.sh
```
