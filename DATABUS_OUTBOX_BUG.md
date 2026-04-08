# Bug: `aegis-databus` outbox fetch fails with JSON unmarshal error

**Error:**
```
databus fetch failed: json: cannot unmarshal object into Go value of type []memory.OutboxEntry
```

## Root cause

`aegis-databus` calls `GET /v1/databus/outbox/pending` on the orchestrator expecting a JSON **array** of outbox entries (`[]OutboxEntry`). The orchestrator does not implement this endpoint — it returns a JSON **object** (likely a 404 error body or catch-all response), which the databus decoder rejects.

## Where the mismatch lives

| Side | File | What it does |
|---|---|---|
| databus (caller) | `aegis-databus/pkg/memory/orchestrator_client.go:83` | `GET /v1/databus/outbox/pending` — decodes `[]OutboxEntry` |
| orchestrator (server) | no matching route | endpoint not implemented |

## Fix needed

The orchestrator needs the following endpoints proxying through to the memory API:

### `GET /v1/databus/outbox/pending?limit=N`

Returns a JSON array of pending outbox entries:

```json
[
  {
    "id": "string",
    "subject": "string",
    "payload": "<bytes>",
    "status": "pending",
    "attempt_count": 0,
    "next_retry_at": "2026-04-08T00:00:00Z",
    "created_at": "2026-04-08T00:00:00Z"
  }
]
```

### `POST /v1/databus/outbox`

Insert a new outbox entry. Body: single `OutboxEntry` object.

### `PUT /v1/databus/outbox/:id/sent`

Mark an entry as sent. Body:

```json
{ "sequence": 42 }
```

## Workaround

Set `AEGIS_MEMORY_URL` instead of `AEGIS_ORCHESTRATOR_URL` in the databus environment to bypass the orchestrator proxy and hit the memory API directly — provided the memory API has `/outbox/pending` implemented.

In `docker-compose.yml`:

```yaml
aegis-databus:
  environment:
    # AEGIS_ORCHESTRATOR_URL: http://orchestrator:8080  # comment out
    AEGIS_MEMORY_URL: http://memory-api:8081/v1/memory
```
