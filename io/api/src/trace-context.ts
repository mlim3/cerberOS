/**
 * W3C Trace Context (traceparent) — trace_id is the 32-char hex segment.
 * CloudEvents `traceid` and HTTP logs use the same value.
 */

import type { MiddlewareHandler } from 'hono'

const VERSION = '00'
const FLAGS = '01'

function randomHex(byteLength: number): string {
  const buf = new Uint8Array(byteLength)
  crypto.getRandomValues(buf)
  return Array.from(buf, (b) => b.toString(16).padStart(2, '0')).join('')
}

export type ParsedTraceparent = {
  traceId: string
  parentId: string
  flags: string
}

/**
 * Parse traceparent header. Returns null if missing or invalid.
 */
export function parseTraceparent(header: string | undefined): ParsedTraceparent | null {
  if (!header?.trim()) return null
  const parts = header.trim().split('-')
  if (parts.length !== 4 || parts[0] !== VERSION) return null
  const [, traceId, parentId, flags] = parts
  if (
    !/^[0-9a-f]{32}$/i.test(traceId) ||
    !/^[0-9a-f]{16}$/i.test(parentId) ||
    !/^[0-9a-f]{2}$/i.test(flags)
  ) {
    return null
  }
  return {
    traceId: traceId.toLowerCase(),
    parentId: parentId.toLowerCase(),
    flags: flags.toLowerCase(),
  }
}

/**
 * New root trace (W3C trace_id = 32 lowercase hex chars).
 */
export function createTraceparent(): { traceparent: string; traceId: string } {
  const traceId = randomHex(16)
  const parentId = randomHex(8)
  const traceparent = `${VERSION}-${traceId}-${parentId}-${FLAGS}`
  return { traceparent, traceId }
}

/**
 * Resolve trace for this request: valid incoming traceparent keeps the same
 * trace_id but uses a new parent_id (this hop's span). Invalid/missing → new root trace.
 */
export function resolveTraceparent(incoming: string | undefined): {
  traceparent: string
  traceId: string
} {
  const parsed = parseTraceparent(incoming)
  if (parsed) {
    const newParentId = randomHex(8)
    return {
      traceparent: `${VERSION}-${parsed.traceId}-${newParentId}-${parsed.flags}`,
      traceId: parsed.traceId,
    }
  }
  return createTraceparent()
}

/** Resolve W3C trace context per request; echo traceparent on the response. */
export const traceMiddleware: MiddlewareHandler = async (c, next) => {
  const { traceparent, traceId } = resolveTraceparent(c.req.header('traceparent'))
  c.set('traceId', traceId)
  c.set('traceparent', traceparent)
  await next()
  c.header('traceparent', traceparent)
}
