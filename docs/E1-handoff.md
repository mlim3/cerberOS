# E1 — Calendar Read Handoff (David)

Short handoff for picking up E1 ("What is my next calendar event?") on this branch.

## What's working end-to-end

A chat prompt like *"What is the title and start time of my next calendar event?"* now:

1. Hits the planner, which routes to `comms` (not `storage` — that was the bug).
2. The agent dispatches the `calendar_list_upcoming` tool.
3. Vault fetches the configured "Secret address in iCal format" URL, parses VEVENTs, returns upcoming events sorted by start time.
4. The LLM composes the reply.

The whole thing avoids Google OAuth — the iCal URL is itself a bearer token, stored as a field on the existing `gmail_app_password` vault blob.

## What changed in this commit

| File | Why |
|---|---|
| `orchestrator/internal/dispatcher/dispatcher.go` | Planner-prompt "Skill domain guide" now explicitly says calendar reads/creates live in `comms` (with a "never `storage`" hint). Without this the LLM hallucinated `storage`. |
| `agents-component/cmd/agent-process/tools_google.go` | New `vaultGmailCalendarListTool` factory + `executeVaultGmailCalendarList`. Mirrors the invite-tool pattern; dispatches to the existing `vault_gmail_calendar_list` op. |
| `agents-component/cmd/agent-process/builtin_registry.go` | Registered `"vault_gmail_calendar_list"` → factory. Without this, the YAML entry in `default_skills.yaml` was silently skipped (see `tools.go:121`) and the tool surfaced as `"tool not registered"`. |

The vault op itself (`ops_calendar_list.go`) and the YAML entry already existed on the branch.

## Bring it up on your machine

```bash
# from repo root
./bootstrap up --keep-volumes
```

You can `--keep-volumes` or not — fresh state is fine, you just need to re-create your demo user and re-enter creds. Once it's up, open `http://localhost:3001`.

## Configure the demo Gmail account (one-time per machine)

Vault data is per-machine (lives in OpenBao's local volume), so you'll need to enter Stefan's demo credentials yourself. Ask Stefan for:

- Demo Gmail address
- Gmail App Password (16 chars, App Passwords page in Google Account)
- The "Secret address in iCal format" URL from Google Calendar → Settings → "Settings for my calendars" → pick a calendar → "Integrate calendar"

Then in the cerberOS web UI:

**Admin → Gmail Demo Account**: paste email + App Password + iCal URL → **Save**. The status row should read `<email> · calendar read enabled`.

Quick way from PowerShell if you'd rather skip the browser (replace the placeholders):

```powershell
$body = @{
  email             = 'demo@example.com'
  app_password      = 'xxxxxxxxxxxxxxxx'
  calendar_ical_url = 'https://calendar.google.com/calendar/ical/.../basic.ics'
} | ConvertTo-Json
Invoke-RestMethod -Method POST -Uri http://localhost:3001/api/admin/gmail-credentials `
  -Headers @{ 'X-Active-User' = '00000000-0000-0000-0000-000000000001'; 'Content-Type'='application/json' } `
  -Body $body
```

## Verify

In any chat, ask:

```
What is the title and start time of my next calendar event in Pacific time?
```

Append "in Pacific time" — the LLM reports UTC by default, which is the main rough edge (see below). You can also try:

```
What do I have on my calendar this week?
List my next 3 events.
```

## Where to look if something goes wrong

```bash
# Planner picked the right domain?
docker logs cerberos-aegis-agents-1 2>&1 | grep -E "required_skill_domains" | tail -5

# Tool dispatched + vault returned a result?
docker logs cerberos-aegis-agents-1 2>&1 | grep -E "calendar_list_upcoming|vault_gmail_calendar_list" | tail -10

# Vault engine made the HTTP fetch?
docker logs cerberos-vault-1 2>&1 | grep -E "vault_gmail_calendar_list" | tail -5
```

`default_skills.yaml` is `//go:embed`-ed — there is no on-disk path inside the container. To verify the skill is in the binary:

```bash
docker exec cerberos-aegis-agents-1 sh -c 'strings /app/agent-process | grep -m1 calendar_list_upcoming'
```

## Known rough edges (good follow-up work)

1. **UTC-only replies.** The LLM reports `2026-05-12T22:00:00Z` rather than "3pm Pacific". The iCal feed gives us UTC; we don't pass the user's tz into the prompt or convert in the tool. Easy fix: either inject the user's tz into the system prompt alongside the wall-clock injection that already happens in `agents-component/cmd/agent-process/loop.go`, or convert in `executeVaultGmailCalendarList` based on a `tz` param.
2. **Recurring events show base DTSTART only.** RRULE is captured on the event payload but not expanded. For one-off demo events this is fine; for "what's my recurring 1:1 next week" it's wrong. Expansion belongs in `vault/engine/handlers/execute/ops_calendar_list.go`.
3. **Single calendar URL per demo account.** Per-user calendar isolation isn't modeled — everyone sharing the demo account sees the same calendar. Fine for FP, but if we ever want per-user calendars we'd extend the gmail credential blob or split the credential type.
4. **Skill synthesis side-effect.** After the first successful read, the synthesis pipeline auto-creates a `get_next_calendar_event` skill in `comms`. Harmless, but if you don't want it ask Stefan how to suppress synthesis for this skill (or just delete it via the CLI).

## Tests passing

```bash
cd orchestrator       && go test ./internal/dispatcher/ -count=1
cd ../agents-component && go test ./cmd/agent-process/ -count=1
cd ../agents-component && go test ./internal/skillsconfig/ -count=1
cd ../vault/engine     && go test ./handlers/execute/ -count=1
```
