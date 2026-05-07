/**
 * Service-level heartbeat emitter for the IO API.
 *
 * Publishes a raw JSON Beat on `aegis.heartbeat.service.io` every
 * HEARTBEAT_INTERVAL_MS (default 10s). The orchestrator's heartbeat
 * sweeper subscribes to `aegis.heartbeat.service.*` and marks IO
 * stale if no beat arrives within the stale window (~45s).
 *
 * The payload intentionally skips envelope wrapping so the sweeper
 * (written in Go) does not need to understand IO-side framing.
 *
 * See docs/heartbeat.md for the full system design.
 */

import os from 'os'
import type { IONatsClient } from './nats/client'
import { ioLog } from './logger'

const SUBJECT_PREFIX = 'aegis.heartbeat.service.'
const SERVICE_NAME = 'io'
const DEFAULT_INTERVAL_MS = 10_000

interface Beat {
  service: string
  instance_id: string
  status: 'ok' | 'degraded'
  timestamp: string
  pid: number
  hostname: string
  uptime_s: number
}

/**
 * Start the periodic heartbeat emitter. Returns a stop function that
 * clears the interval; intended for tests or graceful shutdown.
 */
export function startHeartbeatEmitter(
  client: IONatsClient | null,
  intervalMs: number = DEFAULT_INTERVAL_MS,
): () => void {
  if (!client) {
    ioLog('info', 'heartbeat', 'disabled: no NATS client available')
    return () => {}
  }

  const hostname = os.hostname() || 'unknown'
  const pid = process.pid
  const instanceId = `${SERVICE_NAME}-${hostname}-${pid}`
  const subject = SUBJECT_PREFIX + SERVICE_NAME
  const startedAtMs = Date.now()

  const emit = () => {
    const beat: Beat = {
      service: SERVICE_NAME,
      instance_id: instanceId,
      status: 'ok',
      timestamp: new Date().toISOString(),
      pid,
      hostname,
      uptime_s: Math.floor((Date.now() - startedAtMs) / 1000),
    }
    try {
      client.publishRaw(subject, beat)
    } catch (err) {
      ioLog('warn', 'heartbeat', 'publish failed', { err: String(err) })
    }
  }

  emit()
  const handle = setInterval(emit, intervalMs)

  ioLog('info', 'heartbeat', 'emitter started', {
    subject,
    interval_ms: intervalMs,
    instance_id: instanceId,
  })

  return () => clearInterval(handle)
}
