/**
 * NATS client for cerberOS IO API.
 *
 * Connects to NATS JetStream and:
 *   - Publishes UserTask envelopes on aegis.orchestrator.task.inbound
 *   - Subscribes to orchestrator response channels for task results,
 *     agent status, credential requests, and errors
 *
 * Envelope format mirrors the agents-component wire protocol
 * (agents-component/internal/comms/comms.go outboundEnvelope).
 */

import { connect, type NatsConnection, type JetStreamClient, type Subscription } from 'nats'

// ── NATS subjects (aligned with agents-component/internal/comms/subjects.go) ──

/** IO publishes user tasks here for the orchestrator to pick up. */
export const SUBJECT_TASK_INBOUND = 'aegis.orchestrator.task.inbound'

/** Orchestrator publishes task completion/results here. */
export const SUBJECT_TASK_RESULT = 'aegis.orchestrator.task.result'

/** Orchestrator publishes agent status transitions here. */
export const SUBJECT_AGENT_STATUS = 'aegis.orchestrator.agent.status'

/** Orchestrator publishes credential requests here. */
export const SUBJECT_CREDENTIAL_REQUEST = 'aegis.orchestrator.credential.request'

/** Orchestrator publishes errors here. */
export const SUBJECT_ERROR = 'aegis.orchestrator.error'

export interface NatsConfig {
  url: string
  credsPath?: string
}

export interface IONatsClient {
  connected: boolean
  publishUserTask(envelope: unknown): Promise<void>
  subscribe(channel: string, handler: (msg: unknown) => void): () => void
  close(): void
}

/** Outbound envelope — mirrors agents-component wire format. */
interface OutboundEnvelope {
  message_id: string
  message_type: string
  source_component: string
  correlation_id: string
  timestamp: string
  schema_version: string
  payload: unknown
}

function buildEnvelope(messageType: string, correlationId: string, payload: unknown): OutboundEnvelope {
  return {
    message_id: crypto.randomUUID(),
    message_type: messageType,
    source_component: 'io',
    correlation_id: correlationId,
    timestamp: new Date().toISOString(),
    schema_version: '1.0',
    payload,
  }
}

/**
 * Returns null when NATS_URL is not configured (falls back to HTTP bridge).
 */
export function createNatsClient(config: NatsConfig): IONatsClient | null {
  if (!config.url) {
    return null
  }

  let nc: NatsConnection | null = null
  let js: JetStreamClient | null = null
  const subscriptions: Subscription[] = []
  let isConnected = false

  // Connect asynchronously — the client exposes `connected` as a live flag.
  ;(async () => {
    try {
      nc = await connect({
        servers: config.url,
        ...(config.credsPath ? { authenticator: undefined } : {}),
        name: 'cerberos-io',
        reconnect: true,
        maxReconnectAttempts: -1,
        reconnectTimeWait: 500,
      })
      js = nc.jetstream()

      // Ensure the AEGIS_ORCHESTRATOR stream exists (idempotent — safe if the
      // orchestrator already created it). This mirrors agents-component
      // comms.go EnsureStreams() so IO can start in any order.
      const jsm = await nc.jetstreamManager()
      try {
        await jsm.streams.info('AEGIS_ORCHESTRATOR')
      } catch {
        await jsm.streams.add({
          name: 'AEGIS_ORCHESTRATOR',
          subjects: ['aegis.orchestrator.>'],
          storage: 'file',
          retention: 'limits',
        })
        console.log('[IO:NATS] Created AEGIS_ORCHESTRATOR stream')
      }

      isConnected = true
      console.log(`[IO:NATS] Connected to ${config.url}`)

      // Monitor connection status
      ;(async () => {
        if (!nc) return
        for await (const s of nc.status()) {
          switch (s.type) {
            case 'disconnect':
              isConnected = false
              console.log('[IO:NATS] Disconnected')
              break
            case 'reconnect':
              isConnected = true
              console.log('[IO:NATS] Reconnected')
              break
          }
        }
      })()
    } catch (err) {
      console.error('[IO:NATS] Connection failed:', err)
      isConnected = false
    }
  })()

  const client: IONatsClient = {
    get connected() {
      return isConnected
    },

    async publishUserTask(payload: unknown) {
      if (!js) throw new Error('NATS JetStream not connected')
      const taskPayload = payload as { taskId?: string; task_id?: string }
      const correlationId = taskPayload.taskId ?? taskPayload.task_id ?? ''
      const envelope = buildEnvelope('task.inbound', correlationId, payload)
      const data = new TextEncoder().encode(JSON.stringify(envelope))
      await js.publish(SUBJECT_TASK_INBOUND, data)
    },

    subscribe(channel: string, handler: (msg: unknown) => void): () => void {
      if (!nc) {
        console.warn('[IO:NATS] Cannot subscribe — not connected')
        return () => {}
      }
      const sub = nc.subscribe(channel)
      subscriptions.push(sub)

      // Consume messages in the background
      ;(async () => {
        for await (const msg of sub) {
          try {
            const text = new TextDecoder().decode(msg.data)
            const parsed = JSON.parse(text)
            // Unwrap envelope if present
            handler(parsed.payload ?? parsed)
          } catch {
            // skip unparseable messages
          }
        }
      })()

      return () => {
        sub.unsubscribe()
      }
    },

    close() {
      for (const sub of subscriptions) {
        sub.unsubscribe()
      }
      subscriptions.length = 0
      nc?.close()
      isConnected = false
    },
  }

  return client
}
