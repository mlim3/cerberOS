/**
 * Heuristic: user wants a timetable / repeating run (not NLP-perfect).
 * Mirrors io/api/src/scheduling-language.ts.
 */

const STRONG_REPEAT =
  /\b(?:every|each)\s+\d+\s*(?:minute|minutes|mins?|hours?|hrs?|days?|seconds?)s?\b/i

const CIRCA_DIURNAL =
  /\b(?:every|each)\s+(?:morning|evenings?|night|nights|midnight|noon)s?\b/i

const SIMPLE_FREQ =
  /\b(?:daily|weekly|hourly|monthly|bi-?weekly|every\s+other\s+day)\b/i

const UNTIL_WALL_CLOCK =
  /\buntil\s+(?:approximately\s+)?\d{1,2}(?::\d{2})?\s*(?:am|pm)?(?:\s+local(?:\s+time)?)?\b/i

const REPLY_HERE_REPEATING =
  /\b(?:reply|say|respond|answer|message|post|ping|tell\s+me)\b[\s\S]{0,260}\bevery\b/i

function everyMinuteWithoutDigit(lower: string): boolean {
  return (
    /\b(?:every|each)\s+(?:a|the|one|1)\s+minutes?\b/i.test(lower) ||
    /\b(?:every|each)\s+minute\b/i.test(lower)
  )
}

/** Strip trailing timetable-ish phrase for cron `rawInput` (best-effort). */
export function extractPromptBodyForCron(text: string): string {
  const t = text.trim()
  let cut = t

  const tryCut = (re: RegExp) => {
    const m = t.match(re)
    if (!m || m.index === undefined || m.index < 10) return
    const slice = t.slice(0, m.index).trim()
    if (slice.length >= 8) cut = slice
  }

  tryCut(STRONG_REPEAT)
  tryCut(CIRCA_DIURNAL)
  tryCut(/\b(?:daily|weekly|hourly|monthly|bi-?weekly|every\s+other\s+day)\b.*$/i)
  tryCut(/\b(?:cron|scheduled|schedule)\b.*$/i)

  tryCut(/\buntil\s+(?:approximately\s+)?\d{1,2}(?::\d{2})?\s*(?:am|pm)?(?:\s+local(?:\s+time)?)?\b[\s\S]*$/i)
  tryCut(/\b(?:every|each)\s+(?:a|the|one|1)\s+minutes?\b[\s\S]*$/i)
  tryCut(/\b(?:every|each)\s+minute\b[\s\S]*$/i)

  const out = cut.trim()
  return out.length >= 6 ? out : t
}

export function looksLikeRepeatingSchedulingIntent(text: string): boolean {
  const t = text.trim()
  if (t.length < 10) return false
  const lower = t.toLowerCase()

  const em = everyMinuteWithoutDigit(lower)

  if (
    SIMPLE_FREQ.test(lower) ||
    STRONG_REPEAT.test(t) ||
    em ||
    CIRCA_DIURNAL.test(lower) ||
    (UNTIL_WALL_CLOCK.test(t) && /\b(?:every|each)\b/i.test(lower))
  ) {
    return true
  }

  if (REPLY_HERE_REPEATING.test(t)) {
    return true
  }

  if (
    /\b(?:cron|scheduled)\b/i.test(lower) &&
    /\b(?:every|daily|hourly|weekly|monthly)\b/i.test(lower)
  ) {
    return true
  }
  if (
    /\b(?:remind\s+me|ping\s+me|notify\s+me)\b/i.test(lower) &&
    /\b(?:every|daily|hourly|weekly)\b/i.test(lower)
  ) {
    return true
  }

  const dayAndTime =
    /\b(?:at|@)\s*(?:\d{1,2}(?::\d{2})?\s*(?:am|pm)?).*\b(?:daily|weekly|every|each)\b/i.test(t) ||
    /\b(?:every|each)\s+(?:monday|tuesday|wednesday|thursday|friday|saturday|sunday)s?\s+(?:morning|evening|\d)/i.test(
      lower,
    )

  return Boolean(dayAndTime)
}
