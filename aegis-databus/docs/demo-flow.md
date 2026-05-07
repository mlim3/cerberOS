# Aegis DataBus — Visual Demo Guide

## Architecture Diagram

```mermaid
flowchart TB
    subgraph UI [I/O - UI Layer]
        U[User]
    end

    subgraph Bus [Data Bus - NATS JetStream]
        T[AEGIS_TASKS]
        AG[AEGIS_AGENTS]
        ME[AEGIS_MEMORY]
        RT[AEGIS_RUNTIME]
    end

    subgraph Orch [Orchestrator]
        TR[Task Router]
        TP[Task Planner]
        AM[Agent Manager]
    end

    subgraph Mem [Memory]
        M[Context Mgr]
    end

    subgraph Agent [Agent]
        A[Agent Runtime]
    end

    U -->|1. Submit| UI
    UI -->|2. aegis.ui.action| T
    T -->|3. deliver| TR
    TR -->|4. tasks.routed| T
    T -->|5. deliver| TP
    TP -->|6. plan_created| T
    T -->|7. deliver| AM
    AM -->|8. agents.created x3| AG
    AG -->|9. deliver| M
    M -->|10. memory.saved| ME
    AG -->|11. deliver| A
    A -->|12. runtime.completed| RT
    RT -->|13. deliver| UI
    UI -->|14. Show result| U
```

## Sequence Diagram

```mermaid
sequenceDiagram
    participant U as User
    participant IO as I/O
    participant DB as Data Bus
    participant O as Orchestrator
    participant M as Memory
    participant A as Agent

    U->>IO: Submit task
    IO->>DB: publish(aegis.ui.action)
    DB->>O: deliver
    O->>DB: publish(aegis.tasks.routed)
    DB->>O: deliver
    O->>DB: publish(aegis.tasks.plan_created)
    DB->>O: deliver
    O->>DB: publish(aegis.agents.created) x3
    DB->>M: deliver
    M->>DB: publish(aegis.memory.saved)
    DB->>A: deliver
    A->>DB: publish(aegis.runtime.completed)
    DB->>IO: deliver
    IO->>U: Show result
```

## Live Monitoring

### 1. NATS Monitoring (built-in) — Works immediately

Start the cluster, then open in your browser:

| URL | What it shows |
|-----|---------------|
| http://localhost:8222 | NATS server info |
| http://localhost:8222/varz | Server vars (version, uptime, mem) |
| http://localhost:8222/connz | **Active connections** — each component (aegis-demo, aegis-databus) |
| http://localhost:8222/subsz | Subscriptions |
| http://localhost:8222/jsz | **JetStream** — streams, message counts, bytes |

**Best for live demo:** Open `/connz` and `/jsz` in separate tabs while the demo runs — you’ll see connections and message flow update in real time.

### 2. Grafana

1. Start: `docker compose up -d`
2. Open: **http://localhost:3000**
3. Login: `admin` / `admin`
4. Prometheus is auto-provisioned.
5. Go to **Dashboards → Browse** — the **Aegis DataBus - NATS** dashboard should appear under folder **Aegis**.
6. Optional: **Dashboards → New → Import** → ID **7423** for a richer NATS dashboard.

### 3. Prometheus

- **http://localhost:9090** — query metrics
- Try: `gnatsd_varz_connections` or `gnatsd_connz_num_connections`

## Quick Start

```bash
cd aegis-databus
docker compose up -d
sleep 5
./bin/aegis-databus &
./bin/aegis-demo
```

Then open:
- **Grafana**: http://localhost:3000
- **NATS /connz**: http://localhost:8222/connz
- **NATS /jsz**: http://localhost:8222/jsz

See [MONITORING.md](../MONITORING.md) for full live monitoring instructions.

## Port Reference

| Port | Service |
|------|---------|
| 4222 | NATS client (nats-1) |
| 8222 | NATS monitoring (nats-1) |
| 8223 | NATS monitoring (nats-2) |
| 8224 | NATS monitoring (nats-3) |
| 9090 | Prometheus |
| 3000 | Grafana |
