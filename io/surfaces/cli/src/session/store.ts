/**
 * Session store — persists active tasks and conversation history to a JSON file.
 * Allows the CLI to resume sessions after restart.
 */

import { readFile, writeFile, mkdir } from 'fs/promises'
import { dirname } from 'path'
import type { Task, StatusUpdate } from '@cerberos/io-core'

function sessionLog(msg: string, fields: Record<string, unknown> = {}): void {
  console.error(JSON.stringify({
    time: new Date().toISOString(),
    level: 'ERROR',
    component: 'io',
    module: 'cli-session-store',
    msg,
    ...fields,
  }))
}

export interface StoredSession {
  version: number
  surfaceId: string
  tasks: Task[]
  conversationHistory: Record<string, Array<{ role: 'user' | 'assistant'; content: string }>>
  lastActiveTaskId?: string
  updatedAt: string
}

export class SessionStore {
  private filePath: string
  private tasks: Task[] = []
  private conversationHistory: Map<string, Array<{ role: 'user' | 'assistant'; content: string }>> = new Map()
  private lastActiveTaskId?: string

  constructor(filePath: string) {
    this.filePath = filePath
  }

  async load(): Promise<void> {
    try {
      const data = await readFile(this.filePath, 'utf-8')
      const parsed = JSON.parse(data) as StoredSession
      this.tasks = parsed.tasks ?? []
      this.conversationHistory = new Map(Object.entries(parsed.conversationHistory ?? {}))
      this.lastActiveTaskId = parsed.lastActiveTaskId
    } catch {
      // No saved session — start fresh
    }
  }

  async save(): Promise<void> {
    const dir = dirname(this.filePath)
    await mkdir(dir, { recursive: true })

    const data: StoredSession = {
      version: 1,
      surfaceId: 'cli',
      tasks: this.tasks,
      conversationHistory: Object.fromEntries(this.conversationHistory),
      lastActiveTaskId: this.lastActiveTaskId,
      updatedAt: new Date().toISOString(),
    }

    await writeFile(this.filePath, JSON.stringify(data, null, 2), 'utf-8')
  }

  getTasks(): Task[] {
    return [...this.tasks]
  }

  getTask(taskId: string): Task | undefined {
    return this.tasks.find(t => t.id === taskId)
  }

  upsertTask(task: Partial<Task> & { id: string }): void {
    const idx = this.tasks.findIndex(t => t.id === task.id)
    if (idx >= 0) {
      this.tasks[idx] = { ...this.tasks[idx], ...task }
    } else {
      this.tasks.unshift(task as Task)
    }
    this.save().catch(err => sessionLog('session save failed', { task_id: task.id, error: String(err) })) // fire-and-forget
  }

  updateTaskStatus(update: StatusUpdate): void {
    const task = this.tasks.find(t => t.id === update.taskId)
    if (!task) return

    task.status = update.status
    task.lastUpdate = update.lastUpdate
    task.expectedNextInput = formatExpectedNextInput(update.expectedNextInputMinutes)
    this.save().catch(err => sessionLog('session save failed', { task_id: update.taskId, error: String(err) }))
  }

  addMessage(taskId: string, role: 'user' | 'assistant', content: string): void {
    const history = this.conversationHistory.get(taskId) ?? []
    history.push({ role, content })
    this.conversationHistory.set(taskId, history)
    this.save().catch(err => sessionLog('session save failed', { task_id: taskId, error: String(err) }))
  }

  getConversationHistory(taskId: string): Array<{ role: 'user' | 'assistant'; content: string }> {
    return this.conversationHistory.get(taskId) ?? []
  }

  setActiveTask(taskId: string): void {
    this.lastActiveTaskId = taskId
    this.save().catch(err => sessionLog('session save failed', { task_id: taskId, error: String(err) }))
  }

  getActiveTaskId(): string | undefined {
    return this.lastActiveTaskId ?? this.tasks[0]?.id
  }
}

function formatExpectedNextInput(minutes: number | null): string {
  if (minutes === null) return 'Done'
  if (minutes === 0) return 'Now'
  return `~${minutes} min`
}
