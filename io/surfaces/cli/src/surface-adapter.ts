/**
 * CLI Surface Adapter — SurfaceAdapter interface implementation.
 * Extracted from the original index.ts for library use.
 */

import type {
  SurfaceAdapter,
  SurfaceCapabilities,
  ProcessedInput,
  StatusUpdate,
  AgentResponse,
  Notification,
  Task,
  ChatMessage,
} from '@cerberos/io-core'
import { BASE_CAPABILITIES } from '@cerberos/io-core'
import { streamChat, fetchTasks } from './io/orchestrator-client'
import { submitCredential } from './io/memory-client'
import { SessionStore } from './session/store'

function cliLog(level: 'info' | 'warn' | 'error', msg: string, fields: Record<string, unknown> = {}): void {
  const line = JSON.stringify({
    time: new Date().toISOString(),
    level: level.toUpperCase(),
    component: 'io',
    module: 'cli-surface-adapter',
    msg,
    ...fields,
  })
  if (level === 'error') console.error(line)
  else console.log(line)
}

export interface CLISurfaceConfig {
  surfaceId?: string
  apiUrl?: string
  sessionFile?: string
  userId?: string
}

export class CLISurfaceAdapter implements SurfaceAdapter {
  readonly surfaceId: string
  readonly surfaceName = 'cerberOS CLI'
  readonly surfaceType = 'cli'
  readonly capabilities: SurfaceCapabilities

  private apiUrl: string
  private userId: string
  private store: SessionStore

  constructor(config: CLISurfaceConfig = {}) {
    this.surfaceId = config.surfaceId ?? `cli-${Date.now()}`
    this.apiUrl = config.apiUrl ?? process.env.CLI_API_BASE ?? 'http://localhost:3001'
    this.userId = config.userId ?? '00000000-0000-0000-0000-000000000001'
    this.store = new SessionStore(config.sessionFile ?? `${process.env.HOME}/.cerberos/cli-session.json`)
    this.capabilities = { ...BASE_CAPABILITIES }
  }

  async initialize(): Promise<void> {
    await this.store.load()
    cliLog('info', 'initialized', { surface_id: this.surfaceId, api_url: this.apiUrl })
  }

  receiveInput(input: ProcessedInput): void {
    if (input.type !== 'text') {
      cliLog('warn', 'non-text input not supported', { input_type: input.type })
      return
    }
    // In library mode, this is a fire-and-forget send
    this.sendMessage(input.taskId ?? this.store.getActiveTaskId() ?? 'unknown', input.content)
      .catch(err => cliLog('error', 'send message failed', { task_id: input.taskId, error: String(err) }))
  }

  showTaskStatus(update: StatusUpdate): void {
    this.store.updateTaskStatus(update)
  }

  async deliverResponse(response: AgentResponse): Promise<void> {
    if (!response.streamComplete) {
      process.stdout.write(response.content)
    } else {
      console.log(response.content)
    }
  }

  notify(notification: Notification): void {
    const prefix = notification.priority === 'urgent' ? '!!' :
                   notification.priority === 'important' ? '!' : 'i'
    console.log(`[CLI] ${prefix} ${notification.title}: ${notification.body}`)
  }

  getTasks(): Task[] {
    return this.store.getTasks()
  }

  getConversationHistory(taskId: string): ChatMessage[] {
    return this.store.getConversationHistory(taskId).map((m, i) => ({
      id: String(i),
      role: m.role === 'user' ? 'user' : 'agent',
      content: m.content,
      timestamp: '',
    }))
  }

  async shutdown(): Promise<void> {
    cliLog('info', 'shutdown', { surface_id: this.surfaceId })
  }

  async sendMessage(taskId: string, content: string): Promise<string> {
    const history = this.store.getConversationHistory(taskId)
    let full = ''
    for await (const chunk of streamChat(this.apiUrl, taskId, content, history)) {
      process.stdout.write(chunk)
      full += chunk
    }
    console.log()
    this.store.addMessage(taskId, 'user', content)
    this.store.addMessage(taskId, 'assistant', full)
    return full
  }

  async submitCredential(
    requestId: string,
    keyName: string,
    taskId: string,
    value: string,
  ): Promise<boolean> {
    const result = await submitCredential(this.apiUrl, {
      taskId,
      requestId,
      userId: this.userId,
      keyName,
      value,
    })
    return result.ok
  }
}

export function createCLISurface(config?: CLISurfaceConfig): CLISurfaceAdapter {
  return new CLISurfaceAdapter(config)
}
