/**
 * IO API client. Sends chat messages to the IO API server
 * and streams back the orchestrator's response via SSE.
 *
 * Credentials use a SEPARATE channel (submitCredential) that
 * never touches the chat pipeline, conversation history, or logging.
 */

export interface OrchestratorMessage {
  role: 'user' | 'assistant'
  content: string
}

/**
 * Submit a credential through the dedicated secure channel.
 * This intentionally does NOT flow through the chat/logging pipeline.
 */
export async function submitCredential(
  taskId: string,
  requestId: string,
  credential: string,
): Promise<{ ok: boolean; error?: string }> {
  try {
    const res = await fetch('/api/credential', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Surface-Key': 'dev',
      },
      body: JSON.stringify({ taskId, requestId, credential }),
    })

    if (!res.ok) {
      return { ok: false, error: `${res.status} ${res.statusText}` }
    }
    return { ok: true }
  } catch (err) {
    // In demo mode the endpoint won't exist — simulate success
    console.log('[credential] Demo mode: simulating credential acceptance')
    return { ok: true }
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
