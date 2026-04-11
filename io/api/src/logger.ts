/**
 * Structured JSON logs for centralized log systems (one JSON object per line).
 */

import type { Context } from 'hono'

export type LogLevel = 'debug' | 'info' | 'warn' | 'error'

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
  level: LogLevel,
  component: string,
  msg: string,
  fields: LogFields = {},
): void {
  if (!shouldEmit(level)) return
  const rec: Record<string, unknown> = {
    ts: new Date().toISOString(),
    level,
    service: SERVICE,
    component,
    msg,
    ...fields,
  }
  const line = JSON.stringify(rec)
  if (level === 'error') console.error(line)
  else console.log(line)
}

/**
 * Structured log with trace_id from Hono context (set by trace middleware).
 */
export function logFromContext(
  c: Context,
  level: LogLevel,
  component: string,
  msg: string,
  fields: LogFields = {},
): void {
  const traceId = c.get('traceId') as string | undefined
  logFromContextWithTrace(traceId, level, component, msg, fields)
}

export function logFromContextWithTrace(
  traceId: string | undefined,
  level: LogLevel,
  component: string,
  msg: string,
  fields: LogFields = {},
): void {
  const merged = traceId ? { ...fields, trace_id: traceId } : { ...fields }
  ioLog(level, component, msg, merged)
}
