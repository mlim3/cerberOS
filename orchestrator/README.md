## Orchestrator

### Running with Docker Compose (recommended for e2e testing)

The orchestrator joins the shared `cerberos` Docker network so it can reach the NATS broker started by the agents-component.

**1. Create the shared network (once)**
```bash
docker network create cerberos 2>/dev/null || true
```

**2. Start agents-component and attach its NATS to the shared network**
```bash
cd agents-component
docker compose up --build -d
docker network connect --alias nats cerberos agents-component-nats-1
```

**3. Start the orchestrator (skip its own nats, use the shared one)**
```bash
cd orchestrator
docker compose up --no-deps --build orchestrator
```

**4. Start IO**
```bash
cd io
docker compose up --no-deps --build io
```

---

### Running locally (without Docker)

```bash
cd orchestrator
go build ./cmd/orchestrator/
NATS_URL=nats://localhost:4222 IO_API_BASE=http://localhost:3001 ./orchestrator
```

---

### Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `NATS_URL` | Yes* | `mock://nats` | NATS JetStream server URL |
| `IO_API_BASE` | No | _(disabled)_ | IO component base URL for status push |
| `VAULT_ADDR` | No* | _(demo mode)_ | OpenBao API endpoint |
| `MEMORY_ENDPOINT` | No* | _(demo mode)_ | Memory component API |
| `NODE_ID` | No | hostname | Orchestrator node identity |
| `MAX_TASK_RETRIES` | No | `3` | Max retries per task |
| `DECOMPOSITION_TIMEOUT_SECONDS` | No | `30` | Planner agent timeout |
| `MAX_SUBTASKS_PER_PLAN` | No | `20` | Max subtasks per execution plan |

*If `VAULT_ADDR` and `MEMORY_ENDPOINT` are missing, the orchestrator starts in demo mode with mock implementations.

---

### Health check

```bash
curl http://localhost:8080/health
```

---

### Manual e2e test (NATS CLI)

Subscribe to see tasks dispatched to agents:
```bash
nats sub "aegis.agents.task.inbound" --server nats://localhost:4222
```

Subscribe to see task results:
```bash
nats sub "aegis.orchestrator.task.result" --server nats://localhost:4222
```

Publish a test task directly:
```bash
cd cerberOS
nats pub aegis.orchestrator.tasks.inbound --server nats://localhost:4222 < orchestrator/testdata/nats/user_task.json
```
