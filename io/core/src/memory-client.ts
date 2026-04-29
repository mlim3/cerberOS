/**
 * Memory service client for IO component logging and conversation/task metadata.
 */

const MEMORY_API_BASE = process.env.MEMORY_API_BASE ?? ''
const MEMORY_API_KEY = process.env.MEMORY_API_KEY ?? ''

const DEMO_MODE = !MEMORY_API_BASE

type CoreLogLevel = 'debug' | 'info' | 'warn' | 'error'

function memoryClientLog(level: CoreLogLevel, msg: string, fields: Record<string, unknown> = {}): void {
  const rec = {
    time: new Date().toISOString(),
    level: level.toUpperCase(),
    component: 'io',
    module: 'memory-client',
    msg,
    ...fields,
  }
  const line = JSON.stringify(rec)
  if (level === 'error') console.error(line)
  else console.log(line)
}

interface DemoLogEntry {
  messageId: string
  conversationId: string
  userId: string
  role: 'user' | 'assistant'
  content: string
  taskId?: string
  createdAt: string
}

export interface MemoryConversation {
  conversationId: string
  userId: string
  title: string
  createdAt: string
  updatedAt: string
  lastMessagePreview?: string
  messageCount: number
  latestTaskId?: string
  latestTaskStatus?: string
}

export interface MemoryTask {
  taskId: string
  conversationId: string
  userId: string
  orchestratorTaskRef?: string
  traceId?: string
  status: string
  inputSummary?: string
  createdAt: string
  updatedAt: string
  completedAt?: string
}

export interface MemoryLogEntry {
  messageId: string
  conversationId: string
  userId: string
  role: 'user' | 'assistant'
  content: string
  taskId?: string
  createdAt: string
}

export interface CreateConversationParams {
  userId: string
  conversationId?: string
  title?: string
}

export interface CreateTaskParams {
  userId: string
  conversationId?: string
  taskId?: string
  title?: string
  orchestratorTaskRef?: string
  traceId?: string
  status?: string
  inputSummary?: string
}

export interface AppendLogParams {
  conversationId: string
  userId: string
  role: 'user' | 'assistant'
  content: string
  taskId?: string
  idempotencyKey?: string
  /** W3C trace_id (32 hex) — same as HTTP / NATS user_task; keeps Memory rows aligned with IO + orchestrator */
  traceId?: string
}

const demoLogs: DemoLogEntry[] = []
const demoConversations = new Map<string, MemoryConversation>()
const demoTasks = new Map<string, MemoryTask>()

function authHeaders(): HeadersInit {
  return {
    'Content-Type': 'application/json',
    'X-Internal-API-Key': MEMORY_API_KEY,
  }
}

function ensureDemoConversation(params: CreateConversationParams): MemoryConversation {
  const conversationId = params.conversationId ?? crypto.randomUUID()
  const now = new Date().toISOString()
  const existing = demoConversations.get(conversationId)
  if (existing) {
    return existing
  }
  const conversation: MemoryConversation = {
    conversationId,
    userId: params.userId,
    title: params.title?.trim() || 'New Conversation',
    createdAt: now,
    updatedAt: now,
    lastMessagePreview: '',
    messageCount: 0,
  }
  demoConversations.set(conversationId, conversation)
  return conversation
}

function touchDemoConversation(conversationId: string, update: Partial<MemoryConversation>): void {
  const existing = demoConversations.get(conversationId)
  if (!existing) return
  demoConversations.set(conversationId, { ...existing, ...update, updatedAt: update.updatedAt ?? existing.updatedAt })
}

export async function createConversation(params: CreateConversationParams): Promise<MemoryConversation | null> {
  if (DEMO_MODE) {
    return ensureDemoConversation(params)
  }

  try {
    const res = await fetch(`${MEMORY_API_BASE}/api/v1/conversations`, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify(params),
    })
    if (!res.ok) {
      const text = await res.text()
      memoryClientLog('error', 'create conversation failed', { status: res.status, response_bytes: text.length })
      return null
    }
    const json = await res.json() as { data: { conversation: MemoryConversation } }
    return json.data?.conversation ?? null
  } catch (err) {
    memoryClientLog('error', 'create conversation network error', { error: String(err) })
    return null
  }
}

export async function createTask(params: CreateTaskParams): Promise<MemoryTask | null> {
  if (DEMO_MODE) {
    const conversation = ensureDemoConversation({
      userId: params.userId,
      conversationId: params.conversationId,
      title: params.title,
    })
    const taskId = params.taskId ?? crypto.randomUUID()
    const now = new Date().toISOString()
    const task: MemoryTask = {
      taskId,
      conversationId: conversation.conversationId,
      userId: params.userId,
      orchestratorTaskRef: params.orchestratorTaskRef,
      traceId: params.traceId,
      status: params.status ?? 'awaiting_feedback',
      inputSummary: params.inputSummary,
      createdAt: now,
      updatedAt: now,
    }
    demoTasks.set(taskId, task)
    touchDemoConversation(conversation.conversationId, {
      latestTaskId: taskId,
      latestTaskStatus: task.status,
      updatedAt: now,
    })
    return task
  }

  try {
    const res = await fetch(`${MEMORY_API_BASE}/api/v1/tasks`, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify(params),
    })
    if (!res.ok) {
      const text = await res.text()
      memoryClientLog('error', 'create task failed', { task_id: params.taskId, status: res.status, response_bytes: text.length })
      return null
    }
    const json = await res.json() as { data: { task: MemoryTask } }
    return json.data?.task ?? null
  } catch (err) {
    memoryClientLog('error', 'create task network error', { task_id: params.taskId, error: String(err) })
    return null
  }
}

export async function getTask(taskId: string, userId: string): Promise<MemoryTask | null> {
  if (DEMO_MODE) {
    const task = demoTasks.get(taskId)
    if (!task || task.userId !== userId) return null
    return task
  }

  try {
    const url = new URL(`${MEMORY_API_BASE}/api/v1/tasks/${taskId}`)
    url.searchParams.set('userId', userId)
    const res = await fetch(url.toString(), {
      headers: { 'X-Internal-API-Key': MEMORY_API_KEY },
    })
    if (!res.ok) {
      memoryClientLog('error', 'fetch task failed', { task_id: taskId, status: res.status })
      return null
    }
    const json = await res.json() as { data: { task: MemoryTask } }
    return json.data?.task ?? null
  } catch (err) {
    memoryClientLog('error', 'fetch task network error', { task_id: taskId, error: String(err) })
    return null
  }
}

export async function appendLogEntry(params: AppendLogParams): Promise<MemoryLogEntry | null> {
  if (DEMO_MODE) {
    const conversation = ensureDemoConversation({
      userId: params.userId,
      conversationId: params.conversationId,
    })
    const entry: DemoLogEntry = {
      messageId: crypto.randomUUID(),
      conversationId: conversation.conversationId,
      userId: params.userId,
      role: params.role,
      content: params.content,
      taskId: params.taskId,
      createdAt: new Date().toISOString(),
    }
    demoLogs.push(entry)
    touchDemoConversation(conversation.conversationId, {
      lastMessagePreview: params.content,
      messageCount: (conversation.messageCount ?? 0) + 1,
      updatedAt: entry.createdAt,
    })
    if (params.taskId) {
      const task = demoTasks.get(params.taskId)
      if (task) {
        demoTasks.set(params.taskId, {
          ...task,
          updatedAt: entry.createdAt,
        })
        touchDemoConversation(conversation.conversationId, {
          latestTaskId: params.taskId,
          latestTaskStatus: task.status,
          updatedAt: entry.createdAt,
        })
      }
    }
    return entry
  }

  const body: Record<string, unknown> = {
    userId: params.userId,
    role: params.role,
    content: params.content,
  }
  if (params.idempotencyKey) body['idempotencyKey'] = params.idempotencyKey

  try {
    const headers: Record<string, string> = {
      ...authHeaders() as Record<string, string>,
    }
    if (params.traceId) {
      headers['X-Trace-ID'] = params.traceId
    }

    const res = await fetch(`${MEMORY_API_BASE}/api/v1/chat/${params.conversationId}/messages`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
    })

    if (!res.ok) {
      const text = await res.text()
      memoryClientLog('error', 'append log failed', { task_id: params.taskId, status: res.status, response_bytes: text.length })
      return null
    }

    const json = await res.json() as { data: { message: MemoryLogEntry } }
    const entry = json.data?.message ?? null
    if (entry) {
      entry.taskId = params.taskId
    }
    return entry
  } catch (err) {
    memoryClientLog('error', 'append log network error', { task_id: params.taskId, error: String(err) })
    return null
  }
}

export async function getConversationLogs(
  conversationId: string,
  options?: { userId?: string; taskId?: string; limit?: number; traceId?: string }
): Promise<MemoryLogEntry[]> {
  if (DEMO_MODE) {
    let entries = demoLogs.filter(m => m.conversationId === conversationId)
    if (options?.userId) {
      entries = entries.filter(m => m.userId === options.userId)
    }
    if (options?.taskId) {
      entries = entries.filter(m => m.taskId === options.taskId)
    }
    if (options?.limit) {
      entries = entries.slice(-options.limit)
    }
    return entries
  }

  try {
    const url = new URL(`${MEMORY_API_BASE}/api/v1/chat/${conversationId}/messages`)
    if (options?.userId) url.searchParams.set('userId', options.userId)
    if (options?.limit) url.searchParams.set('limit', String(options.limit))

    const headers: Record<string, string> = {
      'X-Internal-API-Key': MEMORY_API_KEY,
    }
    if (options?.traceId) {
      headers['X-Trace-ID'] = options.traceId
    }

    const res = await fetch(url.toString(), {
      headers,
    })

    if (!res.ok) {
      memoryClientLog('error', 'fetch logs failed', { task_id: options?.taskId, status: res.status })
      return []
    }

    const json = await res.json() as { data: { messages: MemoryLogEntry[] } }
    const messages = json.data?.messages ?? []

    if (options?.taskId) {
      return messages.filter(m => m.taskId === options.taskId)
    }
    return messages
  } catch (err) {
    memoryClientLog('error', 'fetch logs network error', { task_id: options?.taskId, error: String(err) })
    return []
  }
}

export async function deleteConversation(conversationId: string, userId: string): Promise<boolean> {
  if (DEMO_MODE) {
    demoConversations.delete(conversationId)
    return true
  }

  try {
    const url = new URL(`${MEMORY_API_BASE}/api/v1/conversations/${conversationId}`)
    url.searchParams.set('userId', userId)
    const res = await fetch(url.toString(), {
      method: 'DELETE',
      headers: { 'X-Internal-API-Key': MEMORY_API_KEY },
    })
    return res.ok
  } catch (err) {
    memoryClientLog('error', 'delete conversation network error', { conversation_id: conversationId, error: String(err) })
    return false
  }
}

export async function renameConversation(conversationId: string, userId: string, title: string): Promise<boolean> {
  if (DEMO_MODE) {
    touchDemoConversation(conversationId, { title })
    return true
  }

  try {
    const res = await fetch(`${MEMORY_API_BASE}/api/v1/conversations/${conversationId}`, {
      method: 'PATCH',
      headers: authHeaders(),
      body: JSON.stringify({ userId, title }),
    })
    return res.ok
  } catch (err) {
    memoryClientLog('error', 'rename conversation network error', { conversation_id: conversationId, error: String(err) })
    return false
  }
}

export async function listConversations(
  userId: string,
  options?: { limit?: number }
): Promise<MemoryConversation[]> {
  if (DEMO_MODE) {
    const conversations = Array.from(demoConversations.values())
      .filter(c => c.userId === userId)
      .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
    if (options?.limit) {
      return conversations.slice(0, options.limit)
    }
    return conversations
  }

  try {
    const url = new URL(`${MEMORY_API_BASE}/api/v1/conversations`)
    url.searchParams.set('userId', userId)
    if (options?.limit) url.searchParams.set('limit', String(options.limit))
    const res = await fetch(url.toString(), {
      headers: {
        'X-Internal-API-Key': MEMORY_API_KEY,
      },
    })
    if (!res.ok) {
      memoryClientLog('error', 'list conversations failed', { status: res.status })
      return []
    }
    const json = await res.json() as { data: { conversations: MemoryConversation[] } }
    return json.data?.conversations ?? []
  } catch (err) {
    memoryClientLog('error', 'list conversations network error', { error: String(err) })
    return []
  }
}
