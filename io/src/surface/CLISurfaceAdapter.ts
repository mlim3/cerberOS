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
} from './SurfaceAdapter'
import { BASE_CAPABILITIES } from './SurfaceAdapter'
import type { Task, Message } from '../App'

/** CLI-specific configuration */
export interface CLISurfaceConfig {
  surfaceId?: string
  prompt?: string
  historyFile?: string
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
  readonly capabilities: SurfaceCapabilities = {
    ...BASE_CAPABILITIES,
    image: true,
    voice: true,
  }

  constructor(config?: CLISurfaceConfig) {
    this.surfaceId = config?.surfaceId ?? `cli-${Date.now()}`
    this.surfaceName = 'Command Line Interface'
  }

  async initialize(): Promise<void> {
    console.log(`[CLISurfaceAdapter] Initialized: ${this.surfaceId}`)
  }

  receiveInput(input: ProcessedInput): void {
    console.log(`[CLI] Input: ${input.content}`)
  }

  showTaskStatus(update: StatusUpdate): void {
    console.log(`[CLI] Task ${update.taskId}: ${update.status} - ${update.lastUpdate}`)
  }

  deliverResponse(response: AgentResponse): void {
    console.log(`[CLI] Response: ${response.content}`)
  }

  notify(notification: Notification): void {
    console.log(`[CLI] ${notification.priority.toUpperCase()}: ${notification.title} - ${notification.body}`)
  }

  getTasks(): Task[] {
    return []
  }

  getConversationHistory(_taskId: string): Message[] {
    return []
  }

  async shutdown(): Promise<void> {
    console.log(`[CLISurfaceAdapter] Shutdown: ${this.surfaceId}`)
  }
}

/**
 * Create a CLI surface adapter.
 */
export function createCLISurface(config?: CLISurfaceConfig): CLISurfaceAdapter {
  return new CLISurfaceAdapter(config)
}
