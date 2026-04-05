/**
 * NATS client stub for cerberOS IO API.
 *
 * Topic naming follows the agents component convention (subjects.go).
 * All subjects live under the AEGIS_ORCHESTRATOR JetStream stream.
 *
 * TODO: Replace with actual NATS.js implementation.
 *   - Connect to NATS_URL with optional creds (NATS_CREDS_PATH)
 *   - Publish UserTask envelopes on publishUserTask()
 *   - Subscribe to orchestrator response channels
 *   - Wire into /api/chat to forward messages to the orchestrator
 *   - Wire into /api/events/:taskId to consume orchestrator push events
 */

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

/**
 * Returns null when NATS_URL is not configured (falls back to HTTP bridge).
 */
export function createNatsClient(config: NatsConfig): IONatsClient | null {
  if (!config.url) {
    return null
  }

  // TODO: instantiate NATS connection here.
  // Example outline:
  //   const nc = await connect({ servers: config.url, userCreds: config.credsPath })
  //   Subscribe to: SUBJECT_TASK_RESULT, SUBJECT_AGENT_STATUS,
  //                 SUBJECT_CREDENTIAL_REQUEST, SUBJECT_ERROR
  //   Publish to:   SUBJECT_TASK_INBOUND
  //   return { connected: true, publishUserTask, subscribe, close }
  return null
}
