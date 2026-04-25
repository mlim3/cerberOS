/**
 * Structured JSON logs for centralized log systems (one JSON object per line).
 */

import type { Context } from 'hono'

export type LogLevel = 'debug' | 'info' | 'warn' | 'error'
export type LogLevelInput = LogLevel | 'warning' | 'fatal' | 'critical'

const SERVICE = 'io-api'

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
 * Emit one structured log line (no request context).
 */
export function ioLog(
  level: LogLevelInput,
  component: string,
  msg: string,
  fields: LogFields = {},
): void {
  const normalizedLevel = normalizeLogLevel(level)
  if (!shouldEmit(normalizedLevel)) return
  const rec: Record<string, unknown> = {
    time: new Date().toISOString(),
    level: normalizedLevel.toUpperCase(),
    service: SERVICE,
    component,
    msg,
    ...fields,
  }
  const line = JSON.stringify(rec)
  if (normalizedLevel === 'error') console.error(line)
  else console.log(line)
}

/**
 * Structured log with trace_id from Hono context (set by trace middleware).
 */
export function logFromContext(
  c: Context,
  level: LogLevelInput,
  component: string,
  msg: string,
  fields: LogFields = {},
): void {
  const traceId = c.get('traceId') as string | undefined
  const taskId = c.get('taskId') as string | undefined
  logFromContextWithTrace(traceId, level, component, msg, taskId ? { task_id: taskId, ...fields } : fields)
}

export function logFromContextWithTrace(
  traceId: string | undefined,
  level: LogLevelInput,
  component: string,
  msg: string,
  fields: LogFields = {},
): void {
  const merged = traceId ? { ...fields, trace_id: traceId } : { ...fields }
  ioLog(level, component, msg, merged)
}
