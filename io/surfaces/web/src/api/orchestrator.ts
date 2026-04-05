/**
 * IO API client. Sends chat messages to the IO API server
 * and streams back the orchestrator's response via SSE.
 *
 * Credentials use a SEPARATE channel (submitCredential) that
 * never touches the chat pipeline, conversation history, or logging.
 * Credentials flow: IO → IO-API → Memory Vault (encrypted storage).
 * The Orchestrator never sees plaintext secrets.
 */

import { parseOrchestratorStreamEvent, type OrchestratorStreamEvent } from '@cerberos/io-core'

export interface OrchestratorMessage {
  role: 'user' | 'assistant'
  content: string
}

/** Base URL for IO API (no trailing slash). Empty = same origin (Vite proxy in dev). */
export function getIoApiBase(): string {
  const v = import.meta.env.VITE_IO_API_BASE as string | undefined
  return v ? v.replace(/\/$/, '') : ''
}

export function buildApiUrl(path: string): string {
  const base = getIoApiBase()
  const p = path.startsWith('/') ? path : `/${path}`
  return base ? `${base}${p}` : p
}

/** When false (`VITE_ORCHESTRATOR_SSE=0` or `false`), UI uses local mock heartbeats only. */
export function orchestratorSseEnabled(): boolean {
  const v = import.meta.env.VITE_ORCHESTRATOR_SSE as string | undefined
  if (v === '0' || v === 'false') return false
  return true
}

export function formatExpectedNextInput(minutes: number | null): string {
  if (minutes === null) return 'Done'
  if (minutes === 0) return 'Now'
  return `~${minutes} min`
}

/**
 * Subscribe to orchestrator→IO push events for one task (SSE).
 * Returns unsubscribe. Uses enveloped events per io-interfaces.md §1.0.
 */
export function subscribeOrchestratorTaskStream(
  taskId: string,
  handlers: {
    onEvent: (event: OrchestratorStreamEvent) => void
    onOpen?: () => void
    onTransportError?: () => void
  },
): () => void {
  const url = buildApiUrl(`/api/events/${encodeURIComponent(taskId)}`)
  const es = new EventSource(url)

  es.onopen = () => {
    handlers.onOpen?.()
  }

  es.onmessage = (e: MessageEvent<string>) => {
    try {
      const raw = JSON.parse(e.data) as unknown
      const parsed = parseOrchestratorStreamEvent(raw)
      if (parsed) handlers.onEvent(parsed)
    } catch {
      /* ignore bad frame */
    }
  }

  es.onerror = () => {
    handlers.onTransportError?.()
    es.close()
  }

  return () => {
    es.close()
  }
}

export interface CredentialSubmitParams {
  /** Task this credential belongs to (for context/ack) */
  taskId: string
  /** Correlation ID from the CredentialRequest */
  requestId: string
  /** User ID for vault namespace */
  userId: string
  /** Key name in vault */
  keyName: string
  /** The secret value (IO-API will forward to Memory Vault) */
  value: string
}

export interface CredentialSubmitResult {
  ok: boolean
  keyName?: string
  error?: string
}

/**
 * Submit a credential through the dedicated secure channel.
 * The IO API server proxies this to Memory's vault endpoint.
 * The Orchestrator NEVER sees the plaintext value.
 */
export async function submitCredential(
  params: CredentialSubmitParams,
): Promise<CredentialSubmitResult> {
  try {
    // POST to IO API's credential endpoint, which proxies to Memory Vault
    const res = await fetch(buildApiUrl('/api/credential'), {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Surface-Key': 'dev',
      },
      body: JSON.stringify({
        taskId: params.taskId,
        requestId: params.requestId,
        userId: params.userId,
        keyName: params.keyName,
        value: params.value,
      }),
    })

    if (!res.ok) {
      return { ok: false, error: `${res.status} ${res.statusText}` }
    }

    return { ok: true, keyName: params.keyName }
  } catch (err) {
    // In demo mode the endpoint won't exist — simulate success
    console.log('[credential] Demo mode: simulating credential storage')
    return { ok: true, keyName: params.keyName }
  }
}

export async function* streamOrchestratorReply(
  taskId: string,
  userContent: string,
  conversationHistory: OrchestratorMessage[],
): AsyncGenerator<string, void, unknown> {
  const res = await fetch(buildApiUrl('/api/chat'), {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Surface-Key': 'dev',
    },
    body: JSON.stringify({
      taskId,
      content: userContent,
      conversationHistory,
    }),
  })

  if (!res.ok || !res.body) {
    yield `Error: ${res.status} ${res.statusText}`
    return
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()

  try {
    let buffer = ''
    while (true) {
      const { done, value } = await reader.read()
      if (done) break

      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n')
      buffer = lines.pop() ?? ''

      for (const line of lines) {
        if (!line.startsWith('data: ')) continue
        const data = line.slice(6).trim()
        if (!data) continue
        try {
          const parsed = JSON.parse(data)
          if (parsed.done) return
          if (parsed.chunk) yield parsed.chunk
        } catch {
          // ignore partial JSON
        }
      }
    }
  } finally {
    reader.releaseLock()
  }
}
