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

Vault data is per-machine (lives in OpenBao's local volume), so you'll need to enter Stefan's demo credentials yourself before the calendar features will work.

### What Stefan handed you, and what each thing is for

Stefan sent you **three** strings. They look similar but do different jobs — only two of them ever go into cerberOS.

| What | Looks like | What it is | Where it goes |
|---|---|---|---|
| **Google account password** | regular password you type into the Google sign-in page | Logs you into the demo Gmail/Google Calendar **in a browser** so you can inspect inbox/events. | **Nowhere in cerberOS.** Browser only. Never paste this into the Admin Panel or any API. |
| **Gmail App Password** | 16 lowercase letters, often shown grouped as 4×4 (e.g. `abcd efgh ijkl mnop`) | A Google-issued SMTP-only token tied to the demo account. cerberOS uses this to send mail and calendar invites without ever seeing the real password. | cerberOS — Admin → Gmail Demo Account → **App Password** field. Strip the spaces before pasting (or leave them; the API strips them). |
| **iCal secret URL** | `https://calendar.google.com/calendar/ical/<account>/private-<token>/basic.ics` | The "Secret address in iCal format" for one specific Google Calendar. Anyone with this URL can read every event on that calendar (it's a bearer token). | cerberOS — Admin → Gmail Demo Account → **Calendar iCal URL (optional)** field. |

Treat the App Password and the iCal URL like passwords: don't paste them into commits, screenshots, or Slack channels. If either leaks, regenerate them from the Google Account / Google Calendar settings pages.

### Step-by-step

1. Open `http://localhost:3001`. You should land as the seeded `root` user.
2. Top right → **Admin**.
3. Scroll to the **Gmail Demo Account** section.
4. Fill in:
   - **Email**: the demo Gmail address (e.g. `stefanfinal5@gmail.com`).
   - **App Password**: the 16-char string Stefan gave you. Spaces are OK.
   - **Calendar iCal URL (optional)**: the `https://calendar.google.com/calendar/ical/.../basic.ics` URL. **This field is what enables E1 reads** — without it, you can still send mail and create event invites but you can't list upcoming events.
5. Click **Save**. The status row should change to `<email> · calendar read enabled`. If it just says `<email>` without the second clause, the iCal URL didn't save — re-check that it starts with `https://` and ends in `.ics`.

You should not need to restart any container — vault resolves credentials per-call.

### Same thing via PowerShell (skips the browser)

```powershell
$body = @{
  email             = 'demo@example.com'              # the Gmail address
  app_password      = 'abcdefghijklmnop'              # 16 chars, spaces optional
  calendar_ical_url = 'https://calendar.google.com/calendar/ical/.../basic.ics'
} | ConvertTo-Json
Invoke-RestMethod -Method POST -Uri http://localhost:3001/api/admin/gmail-credentials `
  -Headers @{ 'X-Active-User' = '00000000-0000-0000-0000-000000000001'; 'Content-Type'='application/json' } `
  -Body $body
```

Expected reply: `ok: true, calendar_ical_url_configured: true`.

### Verifying the credentials landed in vault

```powershell
$body = @{ agent_id='io'; keys=@('gmail_app_password') } | ConvertTo-Json
Invoke-RestMethod -Method POST -Uri http://localhost:8000/secrets/get `
  -Headers @{ 'Content-Type'='application/json' } -Body $body | ConvertTo-Json -Depth 5
```

You should see a JSON blob with `email`, `app_password`, and `calendar_ical_url` fields. If `calendar_ical_url` is missing, redo the Admin step.

### When you'd ever need the Google account password

You don't need it to run E1. You'd only use it if you want to:

- Open the demo inbox at `mail.google.com` to see invites/emails that cerberOS sent.
- Open `calendar.google.com` to add or rotate the iCal URL ("Settings for my calendars" → pick the calendar → "Integrate calendar" → "Secret address in iCal format" → "Reset" if you want to invalidate the one you have).
- Generate a new App Password at `myaccount.google.com/apppasswords` (existing one keeps working, but you can have several).

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
