import { useMemo, useState } from 'react'
import { buildApiUrl } from '../api/orchestrator'
import { schedulingApiErrorMessage } from '../api/userCrons'
import './UserCronSection.css'
import './RecurringScheduleForm.css'

const INTERVAL_PRESETS: Array<{ label: string; seconds: number }> = [
  { label: '1 min', seconds: 60 },
  { label: '5 min', seconds: 300 },
  { label: '15 min', seconds: 900 },
  { label: '1 hour', seconds: 3600 },
  { label: '24 hours', seconds: 86400 },
]

const CRON_PRESETS: Array<{ label: string; expression: string }> = [
  { label: 'Daily 7:00', expression: '0 7 * * *' },
  { label: 'Every hour', expression: '0 * * * *' },
  { label: 'Weekdays 9:00', expression: '0 9 * * 1-5' },
]

function defaultNameFromRaw(raw: string): string {
  const line = raw.trim().split('\n')[0] ?? ''
  if (!line.length) return 'Recurring task'
  return line.length > 80 ? `${line.slice(0, 77)}…` : line
}

function describeInterval(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 60) return ''
  const m = seconds / 60
  const h = seconds / 3600
  const d = seconds / 86400
  if (seconds % 86400 === 0 && d >= 1) return d === 1 ? 'Runs about every day' : `Runs about every ${d} days`
  if (seconds % 3600 === 0 && h >= 1) return h === 1 ? 'Runs about every hour' : `Runs about every ${h} hours`
  if (seconds % 60 === 0 && m >= 1) return m === 1 ? 'Runs about every minute' : `Runs about every ${m} minutes`
  return `Every ${seconds} seconds`
}

export default function RecurringScheduleForm({
  userId,
  conversationId,
  rawInput,
  onSaved,
  onCancel,
}: {
  userId: string
  conversationId: string
  rawInput: string
  onSaved: (detail: { name: string; rawInput: string; scheduleLabel: string; conversationId: string }) => void
  onCancel: () => void
}) {
  const [name, setName] = useState(() => defaultNameFromRaw(rawInput))
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
  const [saveError, setSaveError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const intervalSummary = useMemo(() => describeInterval(intervalSeconds), [intervalSeconds])

  const nextRunPreview = useMemo(() => {
    try {
      const d = new Date(nextLocal)
      if (Number.isNaN(d.getTime())) return null
      return d.toLocaleString(undefined, {
        weekday: 'short',
        month: 'short',
        day: 'numeric',
        hour: 'numeric',
        minute: '2-digit',
      })
    } catch {
      return null
    }
  }, [nextLocal])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSaveError(null)
    setLoading(true)
    try {
      const nextRunAt = new Date(nextLocal).toISOString()
      const scheduleLabel =
        scheduleKind === 'cron'
          ? `cron ${cronExpression.trim()} (${timeZone})`
          : `every ${intervalSeconds}s`
      const res = await fetch(buildApiUrl('/api/user-crons'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Surface-Key': 'dev' },
        body: JSON.stringify({
          name: name.trim(),
          userId,
          rawInput: rawInput.trim(),
          conversationId,
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
      onSaved({
        name: name.trim(),
        rawInput: rawInput.trim(),
        scheduleLabel,
        conversationId,
      })
    } catch (err) {
      setSaveError(String(err))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="recurring-schedule-form">
      <div className="recurring-schedule-form-header">
        <h3 className="recurring-schedule-form-title">When should this run?</h3>
        <p className="recurring-schedule-form-sub">
          Choose an interval or a cron rule, then the first run time (your device’s local time).
        </p>
      </div>
      <form className="user-cron-form recurring-schedule-inner" onSubmit={handleSubmit}>
        <label className="user-cron-label">
          Short name
          <input
            value={name}
            onChange={e => setName(e.target.value)}
            required
            maxLength={200}
            autoComplete="off"
          />
        </label>

        <fieldset className="user-cron-fieldset">
          <legend className="user-cron-legend">How often</legend>
          <div className="user-cron-segment" role="group" aria-label="Schedule type">
            <button
              type="button"
              className={
                scheduleKind === 'interval'
                  ? 'user-cron-segment-btn user-cron-segment-btn--active'
                  : 'user-cron-segment-btn'
              }
              onClick={() => setScheduleKind('interval')}
              aria-pressed={scheduleKind === 'interval'}
            >
              Interval
            </button>
            <button
              type="button"
              className={
                scheduleKind === 'cron' ? 'user-cron-segment-btn user-cron-segment-btn--active' : 'user-cron-segment-btn'
              }
              onClick={() => setScheduleKind('cron')}
              aria-pressed={scheduleKind === 'cron'}
            >
              Cron
            </button>
          </div>

          {scheduleKind === 'interval' ? (
            <>
              <div className="user-cron-presets" aria-label="Interval presets">
                {INTERVAL_PRESETS.map(p => (
                  <button key={p.seconds} type="button" className="user-cron-chip" onClick={() => setIntervalSeconds(p.seconds)}>
                    {p.label}
                  </button>
                ))}
              </div>
              <label className="user-cron-label">
                Seconds between runs
                <input
                  type="number"
                  min={60}
                  step={1}
                  value={intervalSeconds}
                  onChange={e => setIntervalSeconds(Number(e.target.value))}
                  inputMode="numeric"
                />
              </label>
              {intervalSummary && <p className="user-cron-subhint">{intervalSummary}</p>}
            </>
          ) : (
            <>
              <div className="user-cron-presets" aria-label="Cron presets">
                {CRON_PRESETS.map(p => (
                  <button
                    key={p.expression}
                    type="button"
                    className="user-cron-chip"
                    onClick={() => setCronExpression(p.expression)}
                  >
                    {p.label}
                  </button>
                ))}
              </div>
              <label className="user-cron-label">
                Cron expression
                <input
                  value={cronExpression}
                  onChange={e => setCronExpression(e.target.value)}
                  placeholder="minute hour day month weekday"
                  spellCheck={false}
                />
              </label>
              <label className="user-cron-label">
                Time zone (IANA)
                <input value={timeZone} onChange={e => setTimeZone(e.target.value)} spellCheck={false} />
              </label>
            </>
          )}
        </fieldset>

        <fieldset className="user-cron-fieldset">
          <legend className="user-cron-legend">First run</legend>
          <label className="user-cron-label">
            Starts at (local)
            <input type="datetime-local" value={nextLocal} onChange={e => setNextLocal(e.target.value)} required />
          </label>
          {nextRunPreview && (
            <p className="user-cron-subhint">
              Roughly <strong>{nextRunPreview}</strong> on this device.
            </p>
          )}
        </fieldset>

        {saveError && <p className="user-cron-error">{saveError}</p>}

        <div className="recurring-schedule-actions">
          <button type="button" className="recurring-schedule-cancel" onClick={onCancel} disabled={loading}>
            Cancel
          </button>
          <button type="submit" className="user-cron-submit recurring-schedule-submit" disabled={loading}>
            {loading ? 'Saving…' : 'Save recurring task'}
          </button>
        </div>
      </form>
    </div>
  )
}
