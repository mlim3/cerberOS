/**
 * Parse free-text replies in the recurring-task chat wizard into interval or cron schedules.
 */

export type ParsedRhythm =
  | { ok: true; scheduleKind: 'interval'; intervalSeconds: number }
  | {
      ok: true
      scheduleKind: 'cron'
      cronExpression: string
      timeZone: string
    }

export type ParseRhythmFail = { ok: false; reason: string }

const CRON5 =
  /^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)(?:\s+(\S.*?))?$/

function normalizedTz(s?: string): string {
  const t = (s ?? '').trim()
  if (t && !/^\d+$/.test(t)) return t.replace(/\s+$/, '')
  if (typeof Intl !== 'undefined' && Intl.DateTimeFormat) {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  }
  return 'UTC'
}

function parseFlexibleInt(tok: string): number | null {
  const n = parseInt(tok.replace(/,/g, ''), 10)
  return Number.isFinite(n) ? n : null
}

/** Trailing clauses like “until 9:30 pm local” are end-time hints, not part of rhythm for parsing. */
function stripUntilTrailingClause(s: string): string {
  return s.replace(/\buntil\b[\s\S]*$/i, '').trim()
}

/** When the rhythm is buried in a sentence, find “every/each … unit” and turn it into an interval. */
function tryLooseEmbeddedEveryInterval(full: string): ParsedRhythm | ParseRhythmFail | null {
  const work = stripUntilTrailingClause(full.trim())
  if (!work) return null
  const lower = work.toLowerCase()

  if (
    /\b(?:every|each)\s+(?:a|the|one|1)\s+minutes?\b/i.test(lower) ||
    /\b(?:every|each)\s+minute\b/i.test(lower)
  ) {
    return { ok: true, scheduleKind: 'interval', intervalSeconds: 60 }
  }

  const m =
    /\b(?:every|each)\s+(\d+)\s*(seconds?|secs?|minutes?|mins?|hours?|hrs?|days?)\b/i.exec(
      work,
    )
  if (!m) return null
  const n = parseFlexibleInt(m[1]) ?? 0
  if (n < 1) return { ok: false, reason: 'bad_interval' }
  const unit = (m[2] ?? 'minutes').toLowerCase()
  let seconds = n
  if (unit.startsWith('m')) seconds = n * 60
  else if (unit.startsWith('h')) seconds = n * 3600
  else if (unit.startsWith('d')) seconds = n * 86400
  else seconds = n
  if (seconds < 60) return { ok: false, reason: 'interval_below_minute' }
  return { ok: true, scheduleKind: 'interval', intervalSeconds: seconds }
}

/** e.g. "daily at 9am", "every day at 14:30" */
function tryDailyWallClock(lower: string, defaultTz: string): ParsedRhythm | null {
  const m =
    /\b(?:every\s+day|daily)\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\b/.exec(lower)
  if (!m) return null
  let hour = parseFlexibleInt(m[1]) ?? 0
  const minute = parseFlexibleInt(m[2] ?? '0') ?? 0
  const mer = m[3]
  if (mer === 'pm' && hour < 12) hour += 12
  if (mer === 'am' && hour === 12) hour = 0
  if (hour < 0 || hour > 23 || minute < 0 || minute > 59) return null
  return {
    ok: true,
    scheduleKind: 'cron',
    cronExpression: `${minute} ${hour} * * *`,
    timeZone: defaultTz,
  }
}

export function parseRhythmReply(raw: string, defaultTimeZone?: string): ParsedRhythm | ParseRhythmFail {
  const t = raw.trim()
  const loweredAll = t.toLowerCase()
  /** Drop “until …” before line-anchored patterns so replies like “every 1 minute until 9 pm” parse. */
  const narrowedIntervalLine = stripUntilTrailingClause(t)
  const lower = narrowedIntervalLine.toLowerCase()
  const defaultTz = normalizedTz(defaultTimeZone)

  if (!t) return { ok: false, reason: 'empty' }

  const daily = tryDailyWallClock(loweredAll, defaultTz)
  if (daily) return daily

  // Interval: "every 5 minutes", "every 3600 seconds", "15 min"
  const every =
    /^(?:every\s+)?(\d+)\s*(s|sec|secs|second|seconds|m|min|mins|minute|minutes|h|hrs|hr|hours|hour|d|days|day)?$/i.exec(
      narrowedIntervalLine.trim(),
    )
  if (every) {
    const n = parseFlexibleInt(every[1]) ?? 0
    if (n < 1) return { ok: false, reason: 'bad_interval' }
    const unit = (every[2] ?? 's').toLowerCase()
    let seconds = n
    if (unit.startsWith('m')) seconds = n * 60
    else if (unit.startsWith('h')) seconds = n * 3600
    else if (unit.startsWith('d')) seconds = n * 86400
    else seconds = n // seconds
    if (seconds < 60) return { ok: false, reason: 'interval_below_minute' }
    return { ok: true, scheduleKind: 'interval', intervalSeconds: seconds }
  }

  // Presets
  if (/^(hourly|every\s+hour)$/.test(lower)) {
    return { ok: true, scheduleKind: 'interval', intervalSeconds: 3600 }
  }
  if (/^(daily|every\s+day|once\s+a\s+day)$/.test(lower)) {
    return { ok: true, scheduleKind: 'interval', intervalSeconds: 86400 }
  }
  if (/^(weekly|every\s+week)$/.test(lower)) {
    return { ok: true, scheduleKind: 'interval', intervalSeconds: 7 * 86400 }
  }

  const embedded = tryLooseEmbeddedEveryInterval(t)
  if (embedded !== null) {
    return embedded
  }

  const cronMatch = CRON5.exec(t.replace(/\u2013|\u2014/g, '-').trim())
  if (cronMatch) {
    const [, minute, hour, dom, month, dow, tzRest] = cronMatch
    // Avoid prose matching as cron (CRON5 is very loose otherwise).
    if (/[\d*]/.test(minute)) {
      const expr = `${minute} ${hour} ${dom} ${month} ${dow}`.trim()
      const tz = tzRest?.trim() ? tzRest.trim() : defaultTz
      return {
        ok: true,
        scheduleKind: 'cron',
        cronExpression: expr,
        timeZone: tz,
      }
    }
  }

  return { ok: false, reason: 'unrecognized' }
}

/**
 * Parses first-run time from user reply. Prefer ISO-ish local: YYYY-MM-DD HH:MM
 */
export function parseFirstRunAt(raw: string): { ok: true; iso: string } | { ok: false; reason: string } {
  const t = raw.trim()
  if (!t) return { ok: false, reason: 'empty' }

  // datetime-local pasted: 2026-05-03T14:30
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/.test(t)) {
    const d = new Date(t)
    if (!Number.isNaN(d.getTime())) return { ok: true, iso: d.toISOString() }
  }

  // YYYY-MM-DD HH:MM
  const m = /^(\d{4})-(\d{2})-(\d{2})\s+(\d{1,2}):(\d{2})(?::(\d{2}))?$/.exec(t)
  if (m) {
    const y = Number(m[1])
    const mo = Number(m[2]) - 1
    const da = Number(m[3])
    let h = Number(m[4])
    const mi = Number(m[5])
    const se = Number(m[6] ?? '0')
    const d = new Date(y, mo, da, h, mi, se, 0)
    if (
      Number.isNaN(d.getTime()) ||
      d.getFullYear() !== y ||
      d.getMonth() !== mo ||
      d.getDate() !== da
    ) {
      return { ok: false, reason: 'invalid_date' }
    }
    return { ok: true, iso: d.toISOString() }
  }

  const d2 = new Date(t)
  if (!Number.isNaN(d2.getTime())) return { ok: true, iso: d2.toISOString() }

  return { ok: false, reason: 'unrecognized' }
}
