import { useCallback, useEffect, useState } from 'react'
import { buildApiUrl } from '../api/orchestrator'
import './UserCronSection.css'

/** Memory/IO JSON envelope: `error` is `{ code, message, details? }`, not a string. */
function schedulingApiErrorMessage(body: unknown, httpStatus: number): string {
  if (body && typeof body === 'object' && 'error' in body) {
    const err = (body as { error?: unknown }).error
    if (typeof err === 'string') return err
    if (err && typeof err === 'object' && 'message' in err) {
      const m = (err as { message?: unknown }).message
      if (typeof m === 'string' && m.trim()) return m
    }
  }
  return `HTTP ${httpStatus}`
}

type UserCronListItem = {
  id: string
  name: string
  scheduleKind: string
  intervalSeconds: number
  timeZone: string
  cronExpression: string
  nextRunAt: string
  payload: { userId?: string; rawInput?: string; conversationId?: string }
}

export default function UserCronSection({ userId }: { userId: string }) {
  const [name, setName] = useState('')
  const [rawInput, setRawInput] = useState('')
  const [scheduleKind, setScheduleKind] = useState<'interval' | 'cron'>('interval')
  const [intervalSeconds, setIntervalSeconds] = useState(86400)
  const [cronExpression, setCronExpression] = useState('0 7 * * *')
  const [timeZone, setTimeZone] = useState(
    typeof Intl !== 'undefined' && Intl.DateTimeFormat
      ? Intl.DateTimeFormat().resolvedOptions().timeZone
      : 'UTC',
  )
  const [nextLocal, setNextLocal] = useState(() => {
    const d = new Date()
    d.setMinutes(d.getMinutes() + 2)
    const pad = (n: number) => n.toString().padStart(2, '0')
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
  })
  const [jobs, setJobs] = useState<UserCronListItem[]>([])
  const [loadError, setLoadError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoadError(null)
    try {
      const res = await fetch(
        buildApiUrl(`/api/user-crons?userId=${encodeURIComponent(userId)}`),
        { headers: { 'X-Surface-Key': 'dev' } },
      )
      const j = (await res.json()) as { ok?: boolean; data?: { jobs?: UserCronListItem[] }; error?: { message?: string } }
      if (!res.ok) {
        setLoadError(schedulingApiErrorMessage(j, res.status))
        return
      }
      setJobs(j.data?.jobs ?? [])
    } catch (e) {
      setLoadError(String(e))
    }
  }, [userId])

  useEffect(() => {
    void load()
  }, [load])

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault()
    setSaveError(null)
    setLoading(true)
    try {
      const nextRunAt = new Date(nextLocal).toISOString()
      const res = await fetch(buildApiUrl('/api/user-crons'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Surface-Key': 'dev' },
        body: JSON.stringify({
          name: name.trim(),
          userId,
          rawInput: rawInput.trim(),
          scheduleKind,
          intervalSeconds: scheduleKind === 'interval' ? intervalSeconds : 0,
          cronExpression: scheduleKind === 'cron' ? cronExpression.trim() : '',
          timeZone: scheduleKind === 'cron' ? timeZone : 'UTC',
          nextRunAt,
        }),
      })
      const j = (await res.json()) as { ok?: boolean; error?: { message?: string } }
      if (!res.ok) {
        setSaveError(schedulingApiErrorMessage(j, res.status))
        return
      }
      setName('')
      setRawInput('')
      await load()
    } catch (e) {
      setSaveError(String(e))
    } finally {
      setLoading(false)
    }
  }

  async function handleDelete(id: string) {
    setSaveError(null)
    const res = await fetch(
      buildApiUrl(
        `/api/user-crons/${encodeURIComponent(id)}?userId=${encodeURIComponent(userId)}`,
      ),
      { method: 'DELETE', headers: { 'X-Surface-Key': 'dev' } },
    )
    if (!res.ok) {
      const t = await res.text()
      setSaveError(t || `HTTP ${res.status}`)
      return
    }
    await load()
  }

  return (
    <div className="user-cron-section">
      <h3 className="user-cron-title">Scheduled tasks</h3>
      <p className="user-cron-hint">
        Run any natural-language task on a repeating interval (seconds) or a cron schedule (5-field, e.g. daily 7:00
        in your timezone). The orchestrator receives it like a normal chat task.
      </p>
      <form className="user-cron-form" onSubmit={handleCreate}>
        <label>
          Name
          <input
            value={name}
            onChange={e => setName(e.target.value)}
            placeholder="Morning check-in"
            required
            maxLength={200}
          />
        </label>
        <label>
          Task
          <textarea
            value={rawInput}
            onChange={e => setRawInput(e.target.value)}
            placeholder="What the agent should do each time…"
            required
            rows={3}
          />
        </label>
        <div className="user-cron-schedule">
          <label>
            <input
              type="radio"
              name="sk"
              checked={scheduleKind === 'interval'}
              onChange={() => setScheduleKind('interval')}
            />{' '}
            Every N seconds
          </label>
          <label>
            <input
              type="radio"
              name="sk"
              checked={scheduleKind === 'cron'}
              onChange={() => setScheduleKind('cron')}
            />{' '}
            Cron
          </label>
        </div>
        {scheduleKind === 'interval' ? (
          <label>
            Interval (seconds)
            <input
              type="number"
              min={60}
              step={1}
              value={intervalSeconds}
              onChange={e => setIntervalSeconds(Number(e.target.value))}
            />
          </label>
        ) : (
          <>
            <label>
              Cron (minute hour day month weekday)
              <input
                value={cronExpression}
                onChange={e => setCronExpression(e.target.value)}
                placeholder="0 7 * * *"
                spellCheck={false}
              />
            </label>
            <label>
              Time zone
              <input
                value={timeZone}
                onChange={e => setTimeZone(e.target.value)}
                placeholder="America/New_York"
                spellCheck={false}
              />
            </label>
          </>
        )}
        <label>
          First run (local)
          <input
            type="datetime-local"
            value={nextLocal}
            onChange={e => setNextLocal(e.target.value)}
            required
          />
        </label>
        {saveError && <p className="user-cron-error">{saveError}</p>}
        <button type="submit" className="user-cron-submit" disabled={loading}>
          {loading ? 'Saving…' : 'Add schedule'}
        </button>
      </form>
      {loadError && <p className="user-cron-error">{loadError}</p>}
      {jobs.length > 0 && (
        <ul className="user-cron-list">
          {jobs.map(j => (
            <li key={j.id} className="user-cron-row">
              <div>
                <strong>{j.name}</strong>
                <span className="user-cron-meta">
                  {j.scheduleKind === 'cron'
                    ? `cron ${j.cronExpression} (${j.timeZone})`
                    : `every ${j.intervalSeconds}s`}{' '}
                  — next {new Date(j.nextRunAt).toLocaleString()}
                </span>
              </div>
              <button type="button" onClick={() => void handleDelete(j.id)}>
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
