/**
 * Web Surface Adapter
 *
 * Implements the SurfaceAdapter interface for the React web dashboard.
 * This adapter wraps the existing React components and callbacks,
 * exposing them through the standard SurfaceAdapter interface.
 *
 * The adapter does NOT modify the existing React components.
 * It simply provides a unified interface that can be used
 * by the orchestrator or other agents.
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

/** Configuration for creating a web surface adapter */
export interface WebSurfaceConfig {
  /** Unique identifier for this surface instance */
  surfaceId?: string
  /** Optional: DOM element to mount to (defaults to document.body) */
  mountElement?: HTMLElement
  /** Optional: callback when surface receives input */
  onInput?: (input: ProcessedInput) => void
}

/**
 * Web Surface Adapter
 *
 * Wraps the existing React web dashboard with the SurfaceAdapter interface.
 * This maintains backward compatibility - the existing App.tsx and components
 * continue to work exactly as before.
 */
export class WebSurfaceAdapter implements SurfaceAdapter {
  readonly surfaceId: string
  readonly surfaceName: string
  readonly surfaceType: string
  readonly capabilities: SurfaceCapabilities

  private tasks: Task[] = []
  private taskCallbacks: {
    onSelectTask?: (id: string) => void
    onSendMessage?: (taskId: string, content: string) => void | Promise<void>
  } = {}
  private statusCallbacks: Set<(update: StatusUpdate) => void> = new Set()
  private responseCallbacks: Set<(response: AgentResponse) => void> = new Set()

  constructor(config: WebSurfaceConfig = {}) {
    this.surfaceId = config.surfaceId ?? `web-${Date.now()}`
    this.surfaceName = 'Web Dashboard'
    this.surfaceType = 'web'
    this.capabilities = {
      text: true,
      voice: true,
      image: true,
      video: true,
      richCards: true,
      pushNotifications: false,
      voiceCalls: false,
    }
  }

  /** Register task-related callbacks from the React app */
  registerTaskCallbacks(callbacks: {
    onSelectTask?: (id: string) => void
    onSendMessage?: (taskId: string, content: string) => void | Promise<void>
  }): void {
    this.taskCallbacks = { ...this.taskCallbacks, ...callbacks }
  }

  /** Update the task list (called by React app when tasks change) */
  updateTasks(tasks: Task[]): void {
    this.tasks = tasks
  }

  /** Register for status updates */
  onStatusUpdate(callback: (update: StatusUpdate) => void): () => void {
    this.statusCallbacks.add(callback)
    return () => this.statusCallbacks.delete(callback)
  }

  /** Register for agent responses */
  onResponse(callback: (response: AgentResponse) => void): () => void {
    this.responseCallbacks.add(callback)
    return () => this.responseCallbacks.delete(callback)
  }

  async initialize(): Promise<void> {
    console.log(`[WebSurfaceAdapter] Initialized: ${this.surfaceId}`)
    // The React app handles its own initialization
    // This is a no-op for the adapter
  }

  receiveInput(input: ProcessedInput): void {
    console.log(`[WebSurfaceAdapter] Received input:`, input)

    if (input.type === 'text' && input.content.trim()) {
      const taskId = input.taskId ?? this.tasks[0]?.id

      if (taskId && this.taskCallbacks.onSendMessage) {
        this.taskCallbacks.onSendMessage(taskId, input.content.trim())
      } else {
        console.warn('[WebSurfaceAdapter] No task selected and no default available')
      }
    }
  }

  showTaskStatus(update: StatusUpdate): void {
    console.log(`[WebSurfaceAdapter] Status update:`, update)

    // Notify all registered status callbacks
    this.statusCallbacks.forEach(cb => {
      try {
        cb(update)
      } catch (e) {
        console.error('[WebSurfaceAdapter] Status callback error:', e)
      }
    })
  }

  deliverResponse(response: AgentResponse): void {
    console.log(`[WebSurfaceAdapter] Response:`, response)

    // Notify all registered response callbacks
    this.responseCallbacks.forEach(cb => {
      try {
        cb(response)
      } catch (e) {
        console.error('[WebSurfaceAdapter] Response callback error:', e)
      }
    })
  }

  notify(notification: Notification): void {
    console.log(`[WebSurfaceAdapter] Notification:`, notification)

    // For web, we could use the browser Notification API
    // For now, just log it
    if (notification.priority === 'urgent' && 'Notification' in window) {
      if (Notification.permission === 'granted') {
        new Notification(notification.title, { body: notification.body })
      } else if (Notification.permission !== 'denied') {
        Notification.requestPermission().then(permission => {
          if (permission === 'granted') {
            new Notification(notification.title, { body: notification.body })
          }
        })
      }
    }
  }

  getTasks(): Task[] {
    return [...this.tasks]
  }

  getConversationHistory(taskId: string): ChatMessage[] {
    const task = this.tasks.find(t => t.id === taskId)
    return task?.messages ?? []
  }

  async shutdown(): Promise<void> {
    console.log(`[WebSurfaceAdapter] Shutting down: ${this.surfaceId}`)
    this.statusCallbacks.clear()
    this.responseCallbacks.clear()
    this.taskCallbacks = {}
  }
}

// Singleton instance - the React app uses this
let webSurfaceAdapter: WebSurfaceAdapter | null = null

/**
 * Get or create the singleton WebSurfaceAdapter instance.
 * The React app calls this to register its callbacks.
 */
export function getWebSurface(): WebSurfaceAdapter {
  if (!webSurfaceAdapter) {
    webSurfaceAdapter = new WebSurfaceAdapter()
  }
  return webSurfaceAdapter
}

/**
 * Initialize the web surface adapter.
 * Called by the React app during startup.
 */
export function createWebSurface(config?: WebSurfaceConfig): WebSurfaceAdapter {
  webSurfaceAdapter = new WebSurfaceAdapter(config)
  return webSurfaceAdapter
}

/**
 * Export the adapter for use by orchestrator or other agents.
 * This is the main entry point - external code uses this to get
 * a reference to the web surface through the SurfaceAdapter interface.
 */
export function getSurfaceAdapter(): SurfaceAdapter {
  if (!webSurfaceAdapter) {
    console.warn('[WebSurfaceAdapter] Not initialized - creating default instance')
    webSurfaceAdapter = createWebSurface()
  }
  return webSurfaceAdapter
}
