/**
 * W3C Trace Context (traceparent) — trace_id is the 32-char hex segment.
 * CloudEvents `traceid` and HTTP logs use the same value.
 *
 * Also exports lightweight OTLP HTTP spans to Tempo so traces are queryable
 * in Grafana Explore → Tempo without pulling in the full OpenTelemetry SDK.
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
 * Use incoming traceparent if valid; otherwise create a new trace.
 */
export function resolveTraceparent(incoming: string | undefined): {
  traceparent: string
  traceId: string
  spanId: string
  parentSpanId: string | undefined
} {
  const parsed = parseTraceparent(incoming)
  if (parsed) {
    const spanId = randomHex(8)
    return {
      traceparent: `${VERSION}-${parsed.traceId}-${spanId}-${parsed.flags}`,
      traceId: parsed.traceId,
      spanId,
      parentSpanId: parsed.parentId,
    }
  }
  const traceId = randomHex(16)
  const spanId = randomHex(8)
  return {
    traceparent: `${VERSION}-${traceId}-${spanId}-${FLAGS}`,
    traceId,
    spanId,
    parentSpanId: undefined,
  }
}

// ── OTLP HTTP span export (fire-and-forget to Tempo) ──────────────────────────

const OTLP_ENDPOINT = process.env.OTEL_EXPORTER_OTLP_ENDPOINT ?? ''
const SERVICE_NAME = process.env.OTEL_SERVICE_NAME ?? 'io-api'

interface OtlpSpan {
  traceId: string
  spanId: string
  parentSpanId?: string
  name: string
  kind: number
  startTimeUnixNano: string
  endTimeUnixNano: string
  attributes: { key: string; value: { stringValue?: string; intValue?: string } }[]
  status: { code: number }
}

const spanBuffer: OtlpSpan[] = []
let flushTimer: ReturnType<typeof setTimeout> | null = null
const FLUSH_INTERVAL_MS = 2_000
const MAX_BUFFER = 64
let otlpReady = false

if (OTLP_ENDPOINT) {
  console.log(`[trace] OTLP exporter enabled → ${OTLP_ENDPOINT}/v1/traces`)
}

function scheduleFlush() {
  if (flushTimer) return
  flushTimer = setTimeout(flushSpans, FLUSH_INTERVAL_MS)
}

function flushSpans() {
  flushTimer = null
  if (!OTLP_ENDPOINT || spanBuffer.length === 0) return
  const spans = spanBuffer.splice(0, spanBuffer.length)
  const body = {
    resourceSpans: [{
      resource: {
        attributes: [
          { key: 'service.name', value: { stringValue: SERVICE_NAME } },
        ],
      },
      scopeSpans: [{
        scope: { name: 'io-api-trace', version: '1.0.0' },
        spans: spans.map(s => ({
          traceId: s.traceId,
          spanId: s.spanId,
          ...(s.parentSpanId ? { parentSpanId: s.parentSpanId } : {}),
          name: s.name,
          kind: s.kind,
          startTimeUnixNano: s.startTimeUnixNano,
          endTimeUnixNano: s.endTimeUnixNano,
          attributes: s.attributes,
          status: s.status,
        })),
      }],
    }],
  }
  const url = `${OTLP_ENDPOINT}/v1/traces`
  fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }).then(res => {
    if (!otlpReady && res.ok) {
      otlpReady = true
      console.log(`[trace] OTLP export OK (${spans.length} spans → Tempo)`)
    }
    if (!res.ok) {
      res.text().then(t => console.error(`[trace] OTLP export failed: ${res.status} ${t}`))
    }
  }).catch(err => {
    console.error(`[trace] OTLP export error: ${err}`)
  })
}

function emitSpan(span: OtlpSpan) {
  if (!OTLP_ENDPOINT) return
  spanBuffer.push(span)
  if (spanBuffer.length >= MAX_BUFFER) {
    if (flushTimer) { clearTimeout(flushTimer); flushTimer = null }
    flushSpans()
  } else {
    scheduleFlush()
  }
}

function strAttr(key: string, value: string) {
  return { key, value: { stringValue: value } }
}

function hrNano(): string {
  return `${BigInt(Date.now()) * 1_000_000n}`
}

/** Resolve W3C trace context per request; echo traceparent on response; emit OTLP span. */
export const traceMiddleware: MiddlewareHandler = async (c, next) => {
  const { traceparent, traceId, spanId, parentSpanId } = resolveTraceparent(
    c.req.header('traceparent'),
  )
  c.set('traceId', traceId)
  c.set('traceparent', traceparent)

  const startNano = hrNano()
  await next()
  const endNano = hrNano()

  c.header('traceparent', traceparent)

  emitSpan({
    traceId,
    spanId,
    parentSpanId,
    name: `${c.req.method} ${c.req.path}`,
    kind: 2, // SPAN_KIND_SERVER
    startTimeUnixNano: startNano,
    endTimeUnixNano: endNano,
    attributes: [
      strAttr('http.method', c.req.method),
      strAttr('http.url', c.req.url),
      strAttr('http.status_code', String(c.res.status)),
      strAttr('component', 'io-api'),
    ],
    status: { code: c.res.status >= 400 ? 2 : 1 },
  })
}
