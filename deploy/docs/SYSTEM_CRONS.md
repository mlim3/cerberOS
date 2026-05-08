# System crons (maintenance)

Two mechanisms run **system-level** schedules in cerberOS:

1. **Memory API** — `POST /api/v1/system/maintenance/run` (`X-Internal-API-Key`) runs deterministic **`jobType`** steps (scheduled sweeps, DB ping, inventories, stubs). Helm: `deploy/helm/charts/memory-api` (`systemMaintenanceCron`).
2. **Orchestrator** — `POST /v1/cron/wake` (`X-Aegis-Cron-Secret`) enqueues a **planner** user task when `CRON_WAKE_SECRET` is set. Helm: `deploy/helm/charts/orchestrator` (`systemCronWake`).

The in-process Memory **RunDue ticker** (~1 minute) continues to dispatch due `scheduled_jobs`; CronJobs mainly add jitter-free visibility, cross-replica redundancy, or explicit planner passes.

---

## Supported `jobType` values (`/api/v1/system/maintenance/run`)

| Area | `jobType` | What it does today |
|------|-----------|--------------------|
| Dead job / queue sweep | `scheduled_run_due_sweep`, `dead_job_reprocessing_sweep` | Calls **`RunDue`** (same logic as `/api/v1/scheduled_jobs/run_due`). |
| Monitoring / heartbeat | `system_monitoring_heartbeat` | Counts active / due jobs + completed/failed runs (24h) + booleans whether NATS / orchestrator webhook env is set (no secrets in response). |
| Data integrity | `reconciliation_inventory` | Active jobs, due jobs, orphan run estimate (normally **0** with FK). |
| Orphans | `orphan_cleanup_inventory` | Orphan run count (**delete** stays operator-reviewed). |
| Maintenance | `fact_decay_scan` | User-fact row count stub (extend with real TTL/decay logic). |
| Credential / key posture | `credential_rotation_audit` | Booleans only: internal API key configured, NATS URL, webhook URL — **JWT / signing keys rotate in IO / orchestrator / cluster Secrets**. |
| Performance | `performance_health_check` | DB **Ping** latency + hints to scrape `/internal/metrics`. |
| Messaging / backlog | `journal_queue_audit` | NATS/JetStream monitoring reminders (observe `:8222`, consumer lag, databus DLQ replay policy). |
| DR / backups | `disaster_recovery_coordination`, `backup_verification_ping` | Operator notes for Postgres backups; **`backup_verification_ping`** only checks DB reachability ping. |

**Not implemented in Memory** (use platform primitives): revoke refresh tokens (`IO`/`auth`), material OpenBao key rotation workflows, Postgres `pg_dump`/Velero restores, NATS DLQ replay actions.

---

## Default schedules (`memory-api` chart)

Defined in **`deploy/helm/charts/memory-api/values.yaml`** under **`systemMaintenanceCron.jobs`**. CronJobs exist only when **`systemMaintenanceCron.enabled`** is true (kind dev: **`deploy/helm/cerberos/values-dev.yaml`** sets this on).

| Job (name) | Cron (cluster TZ, usually UTC) | `jobType` |
|------------|--------------------------------|------------|
| `monitor-heartbeat` | `*/10 * * * *` | `system_monitoring_heartbeat` |
| `run-due-sweep` | `*/3 * * * *` | `scheduled_run_due_sweep` |
| `reconcile-daily` | `0 6 * * *` | `reconciliation_inventory` |
| `orphan-inventory-weekly` | `0 7 * * 0` | `orphan_cleanup_inventory` |
| `credential-audit-daily` | `30 8 * * *` | `credential_rotation_audit` |
| `perf-health` | `*/30 * * * *` | `performance_health_check` |
| `dr-coord-weekly` | `15 9 * * 1` | `disaster_recovery_coordination` |
| `backup-verify-hourly` | `0 * * * *` | `backup_verification_ping` |
| `journal-queue-reminder` | `0 */6 * * *` | `journal_queue_audit` |

Override or trim jobs in environment-specific Helm values.

---

## Enabling CronJobs

**Memory**

- **`systemMaintenanceCron.jobs`** are preconfigured in the **memory-api** subchart (see table above). Set **`memory-api.systemMaintenanceCron.enabled=true`** to create CronJobs (`values-dev.yaml` does this for kind).
- You can override **`jobs`** entirely or add **`suspend: true`** per job in custom values.

**Orchestrator**

- Create a Secret with the webhook shared secret (`secretKey`, default **`secret`**).
- Set `orchestrator.cronWake.secretName` so **`CRON_WAKE_SECRET`** is injected into the Deployment.
- Set `orchestrator.systemCronWake.enabled=true` and define `jobs` with **`name`, `schedule`, `jobName`, `rawInput`**, optional **`systemPrompt`**.

Example Secret:

```bash
kubectl -n cerberos create secret generic orchestrator-cron-wake \
  --from-literal=secret="$(openssl rand -hex 24)"
```

Then set `cronWake.secretName=orchestrator-cron-wake`.

---

## Security

- Do **not** log raw credentials, JWT signing material, or full request bodies containing secrets from cron handlers or CronJob logs.
- Restrict who can install Helm values that enable CronJobs (`systemCronWake` / `systemMaintenanceCron`).
