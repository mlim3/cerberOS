/**
 * CLI Surface Adapter
 *
 * Implements the SurfaceAdapter interface for command-line environments.
 * Supports text input/output only.
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

/** CLI-specific configuration */
export interface CLISurfaceConfig {
  surfaceId?: string
  prompt?: string
  historyFile?: string
  apiUrl?: string
}

/**
 * CLI Surface Adapter
 *
 * A minimal surface implementation for command-line environments.
 * Supports text input/output only.
 */
export class CLISurfaceAdapter implements SurfaceAdapter {
  readonly surfaceId: string
  readonly surfaceName: string
  readonly surfaceType: string = 'cli'
  readonly capabilities: SurfaceCapabilities
  private apiUrl: string
  private tasks: Task[] = []

  constructor(config?: CLISurfaceConfig) {
    this.surfaceId = config?.surfaceId ?? `cli-${Date.now()}`
    this.surfaceName = 'Command Line Interface'
    this.apiUrl = config?.apiUrl ?? 'http://localhost:3000'
    this.capabilities = {
      ...BASE_CAPABILITIES,
    }
  }

  async initialize(): Promise<void> {
    console.log(`[CLISurfaceAdapter] Initialized: ${this.surfaceId}`)
    console.log(`[CLISurfaceAdapter] API URL: ${this.apiUrl}`)
  }

  receiveInput(input: ProcessedInput): void {
    console.log(`[CLI] Input: ${input.content}`)
  }

  showTaskStatus(update: StatusUpdate): void {
    console.log(`[CLI] Task ${update.taskId}: ${update.status} - ${update.lastUpdate}`)
  }

  deliverResponse(response: AgentResponse): void {
    if (response.streamComplete) {
      console.log(`[CLI] Response complete: ${response.content.substring(0, 100)}...`)
    } else {
      process.stdout.write(response.content)
    }
  }

  notify(notification: Notification): void {
    const prefix = notification.priority === 'urgent' ? '!!' :
                   notification.priority === 'important' ? '!' : 'i'
    console.log(`[CLI] ${prefix} ${notification.title}: ${notification.body}`)
  }

  getTasks(): Task[] {
    return this.tasks
  }

  getConversationHistory(_taskId: string): ChatMessage[] {
    return []
  }

  async shutdown(): Promise<void> {
    console.log(`[CLISurfaceAdapter] Shutdown: ${this.surfaceId}`)
  }

  // CLI-specific methods
  async sendMessage(taskId: string, content: string): Promise<void> {
    try {
      const response = await fetch(`${this.apiUrl}/api/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ taskId, content }),
      })

      if (!response.body) {
        throw new Error('No response body')
      }

      const reader = response.body.getReader()
      const decoder = new TextDecoder()

      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        process.stdout.write(decoder.decode(value))
      }
      console.log() // newline after stream
    } catch (error) {
      console.error(`[CLI] Error sending message:`, error)
    }
  }

  async fetchTasks(): Promise<void> {
    try {
      const response = await fetch(`${this.apiUrl}/api/tasks`)
      const data = await response.json() as { tasks: Task[] }
      this.tasks = data.tasks
      console.log(`[CLI] Fetched ${this.tasks.length} tasks`)
    } catch (error) {
      console.error(`[CLI] Error fetching tasks:`, error)
    }
  }
}

/**
 * Create a CLI surface adapter.
 */
export function createCLISurface(config?: CLISurfaceConfig): CLISurfaceAdapter {
  return new CLISurfaceAdapter(config)
}

// CLI entry point
async function main() {
  const adapter = createCLISurface({ apiUrl: process.env.API_URL ?? 'http://localhost:3000' })
  await adapter.initialize()

  console.log('\n=== cerberOS CLI Surface ===\n')
  console.log('Type your message and press Enter to send.')
  console.log('Type :tasks to see tasks, :quit to exit.\n')

  const readline = await import('readline')
  const reader = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  })

  const taskId = `cli-task-${Date.now()}`
  console.log(`Task ID: ${taskId}\n`)

  const ask = () => {
    reader.question('> ', async (input: string) => {
      if (input === ':quit') {
        await adapter.shutdown()
        reader.close()
        process.exit(0)
      } else if (input === ':tasks') {
        await adapter.fetchTasks()
        const tasks = adapter.getTasks()
        console.log('\nTasks:')
        tasks.forEach(t => console.log(`  - ${t.id}: ${t.title} (${t.status})`))
        console.log()
      } else if (input.trim()) {
        await adapter.sendMessage(taskId, input)
      }
      ask()
    })
  }

  ask()
}

main().catch(console.error)
