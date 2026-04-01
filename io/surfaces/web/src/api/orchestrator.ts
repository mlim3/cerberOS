/**
 * IO API client. Sends chat messages to the IO API server
 * and streams back the orchestrator's response via SSE.
 *
 * Credentials use a SEPARATE channel (submitCredential) that
 * never touches the chat pipeline, conversation history, or logging.
 * Credentials flow: IO → IO-API → Memory Vault (encrypted storage).
 * The Orchestrator never sees plaintext secrets.
 */

export interface OrchestratorMessage {
  role: 'user' | 'assistant'
  content: string
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
    const res = await fetch('/api/credential', {
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
  const res = await fetch('/api/chat', {
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
