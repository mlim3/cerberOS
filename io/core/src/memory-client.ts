/**
 * Memory service client for IO component logging.
 *
 * Interface mirrors what the real Memory HTTP service would provide.
 * Current implementation uses in-memory storage as a fallback when
 * MEMORY_API_BASE is not configured. When Memory is connected, only
 * this file needs to change — the interface stays the same.
 */

const MEMORY_API_BASE = process.env.MEMORY_API_BASE ?? ''
const MEMORY_API_KEY = process.env.MEMORY_API_KEY ?? ''

// ─── In-memory storage (demo / no Memory service) ───────────────────────────────────

const DEMO_MODE = !MEMORY_API_BASE

interface DemoLogEntry {
  messageId: string
  sessionId: string
  userId: string
  role: 'user' | 'assistant'
  content: string
  taskId?: string
  createdAt: string
}

const demoLogs: DemoLogEntry[] = []

/** Maps taskId → sessionId (in-memory for now; persists for the server process lifetime) */
const taskSessionMap = new Map<string, string>()

// ─── Public types ───────────────────────────────────────────────────────────────────

export interface MemoryLogEntry {
  messageId: string
  sessionId: string
  userId: string
  role: 'user' | 'assistant'
  content: string
  taskId?: string
  createdAt: string
}

export interface AppendLogParams {
  sessionId: string
  userId: string
  role: 'user' | 'assistant'
  content: string
  taskId?: string
  idempotencyKey?: string
  /** W3C trace_id (32 hex) — same as HTTP / NATS user_task; keeps Memory rows aligned with IO + orchestrator */
  traceId?: string
}

// ─── Helpers ───────────────────────────────────────────────────────────────────────

export function getOrCreateSessionId(taskId: string, _userId: string): string {
  let sessionId = taskSessionMap.get(taskId)
  if (!sessionId) {
    sessionId = crypto.randomUUID()
    taskSessionMap.set(taskId, sessionId)
  }
  return sessionId
}

// ─── Append ────────────────────────────────────────────────────────────────────────

/**
 * Append a log entry. When MEMORY_API_BASE is set, proxies to the Memory HTTP service.
 * Otherwise, stores in the in-memory demo array.
 */
export async function appendLogEntry(params: AppendLogParams): Promise<MemoryLogEntry | null> {
  if (DEMO_MODE) {
    const entry: DemoLogEntry = {
      messageId: crypto.randomUUID(),
      sessionId: params.sessionId,
      userId: params.userId,
      role: params.role,
      content: params.content,
      taskId: params.taskId,
      createdAt: new Date().toISOString(),
    }
    demoLogs.push(entry)
    return entry
  }

  const body: Record<string, unknown> = {
    userId: params.userId,
    role: params.role,
    content: params.content,
  }
  if (params.taskId) body['taskId'] = params.taskId
  if (params.idempotencyKey) body['idempotencyKey'] = params.idempotencyKey

  try {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      'X-API-KEY': MEMORY_API_KEY,
    }
    if (params.traceId) {
      headers['X-Trace-ID'] = params.traceId
    }

    const res = await fetch(`${MEMORY_API_BASE}/api/v1/chat/${params.sessionId}/messages`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
    })

    if (!res.ok) {
      console.error('[MemoryClient] Failed to append log:', res.status, await res.text())
      return null
    }

    const json = await res.json() as { data: { message: MemoryLogEntry } }
    return json.data.message
  } catch (err) {
    console.error('[MemoryClient] Network error appending log:', err)
    return null
  }
}

// ─── Retrieve ─────────────────────────────────────────────────────────────────────

/**
 * Retrieve log entries for a session, optionally filtered by taskId.
 * When MEMORY_API_BASE is set, fetches from the Memory HTTP service.
 * Otherwise, queries the in-memory demo array.
 */
export async function getSessionLogs(
  sessionId: string,
  options?: { taskId?: string; limit?: number; traceId?: string }
): Promise<MemoryLogEntry[]> {
  if (DEMO_MODE) {
    let entries = demoLogs.filter(m => m.sessionId === sessionId)
    if (options?.taskId) {
      entries = entries.filter(m => m.taskId === options.taskId)
    }
    if (options?.limit) {
      entries = entries.slice(-options.limit)
    }
    return entries
  }

  try {
    const url = new URL(`${MEMORY_API_BASE}/api/v1/chat/${sessionId}/messages`)
    if (options?.limit) url.searchParams.set('limit', String(options.limit))

    const headers: Record<string, string> = {
      'X-API-KEY': MEMORY_API_KEY,
    }
    if (options?.traceId) {
      headers['X-Trace-ID'] = options.traceId
    }

    const res = await fetch(url.toString(), {
      headers,
    })

    if (!res.ok) {
      console.error('[MemoryClient] Failed to fetch logs:', res.status)
      return []
    }

    const json = await res.json() as { data: { messages: MemoryLogEntry[] } }
    const messages = json.data?.messages ?? []

    if (options?.taskId) {
      return messages.filter(m => m.taskId === options.taskId)
    }
    return messages
  } catch (err) {
    console.error('[MemoryClient] Network error fetching logs:', err)
    return []
  }
}
