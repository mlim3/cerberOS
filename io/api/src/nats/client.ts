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
import { ioLog } from '../logger'

// ── NATS subjects (aligned with agents-component/internal/comms/subjects.go) ──

/** IO publishes user tasks here for the orchestrator to pick up. */
export const SUBJECT_TASK_INBOUND = 'aegis.orchestrator.tasks.inbound'

/** IO subscribes to this wildcard for orchestrator responses on per-task callback topics. */
export const SUBJECT_IO_RESULTS = 'aegis.io.results.>'

/** Orchestrator publishes task completion/results here. */
export const SUBJECT_TASK_RESULT = 'aegis.orchestrator.task.result'

/** Orchestrator publishes agent status transitions here. */
export const SUBJECT_AGENT_STATUS = 'aegis.orchestrator.agent.status'

/** Orchestrator publishes credential requests here. */
export const SUBJECT_CREDENTIAL_REQUEST = 'aegis.orchestrator.credential.request'

/** Orchestrator publishes errors here. */
export const SUBJECT_ERROR = 'aegis.orchestrator.error'

/** IO publishes the user's approve/reject decision for a proposed plan here. */
export const SUBJECT_PLAN_DECISION = 'aegis.orchestrator.plan.decision'

export interface PlanDecisionPayload {
  orchestrator_task_ref: string
  task_id: string
  approved: boolean
  reason?: string
  /** W3C trace_id (32 hex) — forwarded on the wire envelope for orchestrator logs */
  trace_id?: string
}

export interface NatsConfig {
  url: string
  credsPath?: string
}

export interface UserTaskPayload {
  task_id: string
  user_id: string
  content: string
  priority?: number
  timeout_seconds?: number
  payload?: { raw_input: string }
  callback_topic?: string
  required_skill_domains?: string[]
  user_context_id?: string
  /** W3C trace_id (32 hex) — forwarded on the wire envelope for orchestrator logs */
  trace_id?: string
}

export interface IONatsClient {
  connected: boolean
  publishUserTask(task: UserTaskPayload): Promise<void>
  publishPlanDecision(decision: PlanDecisionPayload): Promise<void>
  subscribe(channel: string, handler: (msg: unknown) => void): () => void
  close(): void
}

/** Outbound envelope — mirrors agents-component wire format. */
interface OutboundEnvelope {
  message_id: string
  message_type: string
  source_component: string
  correlation_id: string
  trace_id?: string
  timestamp: string
  schema_version: string
  payload: unknown
}

function buildEnvelope(
  messageType: string,
  correlationId: string,
  payload: unknown,
  traceId?: string,
): OutboundEnvelope {
  const env: OutboundEnvelope = {
    message_id: crypto.randomUUID(),
    message_type: messageType,
    source_component: 'io',
    correlation_id: correlationId,
    timestamp: new Date().toISOString(),
    schema_version: '1.0',
    payload,
  }
  if (traceId) env.trace_id = traceId
  return env
}

/** Prefix for per-task IO callback subjects (`aegis.io.results.<client_task_id>`). */
export const IO_RESULTS_TOPIC_PREFIX = 'aegis.io.results.'

/** Build a callback topic for a given task. */
export function callbackTopicForTask(taskId: string): string {
  return `${IO_RESULTS_TOPIC_PREFIX}${taskId}`
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

  type PendingSub = { channel: string; handler: (msg: unknown) => void; unsub?: () => void }
  const pendingSubscriptions: PendingSub[] = []

  function attachSubscription(channel: string, handler: (msg: unknown) => void): () => void {
    if (!nc) return () => {}
    const sub = nc.subscribe(channel)
    subscriptions.push(sub)
    ;(async () => {
      for await (const msg of sub) {
        try {
          const text = new TextDecoder().decode(msg.data)
          const parsed = JSON.parse(text) as Record<string, unknown>
          handler({ envelope: parsed, subject: msg.subject })
        } catch {
          // skip unparseable messages
        }
      }
    })()
    return () => {
      sub.unsubscribe()
    }
  }

  function flushPendingSubscriptions() {
    if (!nc) return
    const queued = pendingSubscriptions.splice(0, pendingSubscriptions.length)
    for (const entry of queued) {
      entry.unsub = attachSubscription(entry.channel, entry.handler)
      ioLog('info', 'nats', 'Subscribed (deferred)', { channel: entry.channel })
    }
  }

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
        ioLog('info', 'nats', 'Created AEGIS_ORCHESTRATOR stream', {})
      }

      isConnected = true
      ioLog('info', 'nats', 'Connected to NATS', { url: config.url })
      flushPendingSubscriptions()

      // Monitor connection status
      ;(async () => {
        if (!nc) return
        for await (const s of nc.status()) {
          switch (s.type) {
            case 'disconnect':
              isConnected = false
              ioLog('info', 'nats', 'NATS disconnected', {})
              break
            case 'reconnect':
              isConnected = true
              ioLog('info', 'nats', 'NATS reconnected', {})
              break
          }
        }
      })()
    } catch (err) {
      ioLog('error', 'nats', 'Connection failed', { err: String(err) })
      isConnected = false
    }
  })()

  const client: IONatsClient = {
    get connected() {
      return isConnected
    },

    async publishUserTask(task: UserTaskPayload) {
      if (!js) throw new Error('NATS JetStream not connected')
      const natsPayload = {
        task_id: task.task_id,
        user_id: task.user_id,
        required_skill_domains: task.required_skill_domains ?? ['general'],
        priority: task.priority ?? 5,
        // Runaway backstop for a whole task. Individual phases are already
        // bounded by tighter timers in the orchestrator:
        //   - DecompositionTimeoutSeconds (30s) — planner LLM call
        //   - PlanApprovalTimeoutSeconds (300s) — user review of the plan
        //   - per-subtask timeout_seconds in the plan itself (~30s each)
        // The old 120s default was shorter than the approval window alone,
        // so a task sitting in AWAITING_APPROVAL would be killed before the
        // user could click "Approve". 30 minutes gives realistic headroom
        // for approval + multi-subtask execution without sacrificing the
        // backstop semantics.
        timeout_seconds: task.timeout_seconds ?? 1800,
        payload: task.payload ?? { raw_input: task.content },
        callback_topic: task.callback_topic ?? callbackTopicForTask(task.task_id),
        user_context_id: task.user_context_id,
      }
      const envelope = buildEnvelope('user_task', task.task_id, natsPayload, task.trace_id)
      const data = new TextEncoder().encode(JSON.stringify(envelope))
      await js.publish(SUBJECT_TASK_INBOUND, data)
    },

    async publishPlanDecision(decision: PlanDecisionPayload) {
      if (!js) throw new Error('NATS JetStream not connected')
      const payload = {
        orchestrator_task_ref: decision.orchestrator_task_ref,
        task_id: decision.task_id,
        approved: decision.approved,
        reason: decision.reason ?? '',
      }
      // correlation_id = orchestrator_task_ref so orchestrator can fall back
      // to envelope.correlation_id if the payload is stripped somewhere.
      const envelope = buildEnvelope(
        'plan_decision',
        decision.orchestrator_task_ref,
        payload,
        decision.trace_id,
      )
      const data = new TextEncoder().encode(JSON.stringify(envelope))
      await js.publish(SUBJECT_PLAN_DECISION, data)
    },

    subscribe(channel: string, handler: (msg: unknown) => void): () => void {
      if (!nc) {
        const entry: PendingSub = { channel, handler }
        pendingSubscriptions.push(entry)
        ioLog('info', 'nats', 'Subscription queued until connect', { channel })
        return () => {
          if (entry.unsub) {
            entry.unsub()
          } else {
            const ix = pendingSubscriptions.indexOf(entry)
            if (ix >= 0) pendingSubscriptions.splice(ix, 1)
          }
        }
      }
      return attachSubscription(channel, handler)
    },

    close() {
      pendingSubscriptions.length = 0
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
