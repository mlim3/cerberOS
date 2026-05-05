# IO Component — Subtask Confirmation Contract

**For:** IO team
**Feature:** Multi-step subtask confirmation (Orchestrator v3.3)

---

## What the Orchestrator Does

When an agent plan contains a high-impact subtask (e.g. `send_email`, `delete_file`), the orchestrator pauses before dispatching it and:

1. Pushes a `confirmation_request` event to the IO component via the existing HTTP endpoint (`POST /api/orchestrator/stream-events`).
2. Waits for the user's decision to arrive on NATS.

---

## What the IO Component Needs to Implement

### 1. Handle `confirmation_request` events

The existing `stream-events` endpoint receives a new event type:

```json
{
  "type": "confirmation_request",
  "payload": {
    "planId":    "plan-uuid",
    "subtaskId": "subtask-uuid",
    "taskId":    "task-uuid",
    "action":    "send_email",
    "prompt":    "Send a follow-up email to alice@example.com summarising today's meeting."
  }
}
```

The IO component should display a modal / card to the user showing `action` as the title and `prompt` as the explanation.

### 2. Publish the user's response to NATS

After the user clicks **Approve** or **Reject**, publish to:

**Topic:** `aegis.orchestrator.task.confirmation_response`

**Payload:**

```json
{
  "plan_id":    "plan-uuid",
  "subtask_id": "subtask-uuid",
  "task_id":    "task-uuid",
  "confirmed":  true,
  "reason":     ""
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `plan_id` | string | yes | Echo from request |
| `subtask_id` | string | yes | Echo from request |
| `task_id` | string | yes | Echo from request |
| `confirmed` | bool | yes | `true` = approved, `false` = rejected |
| `reason` | string | no | Optional rejection reason shown in logs |

---

## Behaviour Details

- **One response per request.** Send exactly one response per `confirmation_request`. Sending duplicates is safe — the orchestrator ignores responses for already-resolved subtasks.
- **Timeout/dismiss = reject.** If the user dismisses or the modal times out, publish `confirmed: false`.
- **IO down.** If IO cannot display the modal, the orchestrator will log a warning and the subtask stays in `AWAITING_CONFIRMATION` state until a response arrives or the parent task times out.
- **Status update.** Immediately before `confirmation_request`, the orchestrator also pushes a `status` event with `status: "awaiting_feedback"`. No extra handling needed — this is informational.

---

## No Changes to Existing Endpoints

The `confirmation_response` goes **outbound from IO to NATS** — the same NATS connection the IO component already uses for other orchestrator events. There is no new HTTP endpoint required on either side.
