/**
 * Orchestrator client for CLI surface.
 * Handles chat streaming and task management.
 */

export interface ChatStreamResult {
  content: string
  taskId: string
}

/**
 * Send a message to the orchestrator and stream the response.
 * Uses the IO API's /api/chat endpoint which proxies to the orchestrator.
 */
export async function* streamChat(
  apiBase: string,
  taskId: string,
  content: string,
  conversationHistory?: Array<{ role: 'user' | 'assistant'; content: string }>,
): AsyncGenerator<string, void, unknown> {
  const res = await fetch(`${apiBase}/api/chat`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Surface-Key': 'cli',
    },
    body: JSON.stringify({ taskId, content, conversationHistory }),
  })

  if (!res.ok || !res.body) {
    throw new Error(`Chat failed: ${res.status} ${res.statusText}`)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  try {
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

/**
 * Fetch all tasks from the IO API.
 */
export async function fetchTasks(apiBase: string) {
  const res = await fetch(`${apiBase}/api/tasks`, {
    headers: { 'X-Surface-Key': 'cli' },
  })
  if (!res.ok) throw new Error(`Failed to fetch tasks: ${res.status}`)
  const json = await res.json() as { tasks: unknown[] }
  return json.tasks
}

/**
 * Fetch logs for a specific task from the IO API.
 */
export async function fetchTaskLogs(apiBase: string, taskId: string) {
  const res = await fetch(`${apiBase}/api/logs/${encodeURIComponent(taskId)}`, {
    headers: { 'X-Surface-Key': 'cli' },
  })
  if (!res.ok) throw new Error(`Failed to fetch logs: ${res.status}`)
  const json = await res.json() as { logs: unknown[] }
  return json.logs
}
