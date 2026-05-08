/**
 * Structured JSON logs for centralized log systems (one JSON object per line).
 *
 * Canonical schema (see docs/logging.md):
 *   { time, level, msg, component, module, ... }
 *
 * `component` is always the constant "io" since this logger lives inside the
 * io component. The second argument to ioLog/logFromContext is the *module*
 * — the sub-unit within io (e.g. "http", "nats", "transcription").
 */

import type { Context } from 'hono'

export type LogLevel = 'debug' | 'info' | 'warn' | 'error'
export type LogLevelInput = LogLevel | 'warning' | 'fatal' | 'critical'

const COMPONENT = 'io'

const levelRank: Record<LogLevel, number> = {
  debug: 0,
  info: 1,
  warn: 2,
  error: 3,
}

function normalizeLogLevel(raw: string | undefined): LogLevel {
  const v = (raw ?? 'info').toLowerCase()
  if (v === 'debug' || v === 'info' || v === 'warn' || v === 'error') {
    return v
  }
  if (v === 'warning') return 'warn'
  if (v === 'fatal' || v === 'critical') return 'error'
  return 'info'
}

const LOG_LEVEL = normalizeLogLevel(process.env.LOG_LEVEL)

function minLevel(): number {
  return levelRank[LOG_LEVEL]
}

function shouldEmit(level: LogLevel): boolean {
  return levelRank[level] >= minLevel()
}

export type LogFields = Record<string, unknown>

/**
 * Truncate user-supplied text into a debug-safe preview suitable for short
 * metadata fields (titles, reasons, vault progress messages, error codes,
 * voice transcripts).
 *
 * Caps at `maxWords` words AND `maxChars` characters (whichever is hit first)
 * and appends `…` when truncation occurred. Whitespace is collapsed and
 * trimmed so multi-line input renders as a single readable string.
 *
 * For long *conversation* content (user chat messages, agent replies), prefer
 * `previewHeadTail` instead — it keeps the start AND the end so the line
 * remains recognisable when the message is many paragraphs long. See
 * docs/logging.md for the full policy.
 */
export function previewWords(s: string, maxWords = 20, maxChars = 140): string {
  if (!s) return ''
  const flat = s.replace(/\s+/g, ' ').trim()
  if (!flat) return ''
  const words = flat.split(' ')
  let truncated = false
  let out = flat
  if (words.length > maxWords) {
    out = words.slice(0, maxWords).join(' ')
    truncated = true
  }
  if (out.length > maxChars) {
    out = out.slice(0, maxChars).trimEnd()
    truncated = true
  }
  return truncated ? `${out}…` : out
}

/**
 * Build a debug-safe head+tail preview suitable for long conversation
 * messages — typically `content_preview` (user → agent) and `result_preview`
 * (agent → user).
 *
 * Format: `<first headWords words> [..N chars..] <last tailWords words>`,
 * where `N` is the number of characters omitted from the middle. When the
 * string is short enough that head+tail would already cover the whole thing,
 * the original (whitespace-collapsed) value is returned unchanged.
 *
 * The motivation is debugger UX: a user might paste a long document and put
 * the actual question on the last line; a beginning-only preview hides it.
 * Head+tail makes the same message recognisable across IO, orchestrator, and
 * agent logs.
 */
export function previewHeadTail(s: string, headWords = 15, tailWords = 10): string {
  if (!s) return ''
  const flat = s.replace(/\s+/g, ' ').trim()
  if (!flat) return ''
  const words = flat.split(' ')
  if (words.length <= headWords + tailWords) {
    return flat
  }
  const head = words.slice(0, headWords).join(' ')
  const tail = words.slice(words.length - tailWords).join(' ')
  const omitted = flat.length - head.length - tail.length
  if (omitted <= 0) return flat
  return `${head} [..${omitted} chars..] ${tail}`
}

/**
 * Emit one structured log line (no request context).
 *
 * @param level   canonical level or alias (warning|fatal|critical)
 * @param module  sub-unit name within the io component (e.g. "http", "nats")
 * @param msg     short event name or sentence
 * @param fields  additional safe metadata
 */
export function ioLog(
  level: LogLevelInput,
  module: string,
  msg: string,
  fields: LogFields = {},
): void {
  const normalizedLevel = normalizeLogLevel(level)
  if (!shouldEmit(normalizedLevel)) return
  const rec: Record<string, unknown> = {
    time: new Date().toISOString(),
    level: normalizedLevel.toUpperCase(),
    component: COMPONENT,
    module,
    msg,
    ...fields,
  }
  const line = JSON.stringify(rec)
  if (normalizedLevel === 'error') console.error(line)
  else console.log(line)
}

/**
 * Structured log with trace_id, task_id, and conversation_id pulled from Hono
 * context. trace middleware sets traceId; route handlers set taskId and
 * conversationId via c.set(...) at the top of each handler. Explicit fields
 * passed in `fields` win over context values, so a route can override on a
 * single line.
 */
export function logFromContext(
  c: Context,
  level: LogLevelInput,
  module: string,
  msg: string,
  fields: LogFields = {},
): void {
  const traceId = c.get('traceId') as string | undefined
  const taskId = c.get('taskId') as string | undefined
  const conversationId = c.get('conversationId') as string | undefined
  const merged: LogFields = { ...fields }
  if (taskId !== undefined && merged.task_id === undefined) merged.task_id = taskId
  if (conversationId !== undefined && merged.conversation_id === undefined) merged.conversation_id = conversationId
  logFromContextWithTrace(traceId, level, module, msg, merged)
}

export function logFromContextWithTrace(
  traceId: string | undefined,
  level: LogLevelInput,
  module: string,
  msg: string,
  fields: LogFields = {},
): void {
  const merged = traceId ? { ...fields, trace_id: traceId } : { ...fields }
  ioLog(level, module, msg, merged)
}
