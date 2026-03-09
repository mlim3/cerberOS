/**
 * Surface Adapter Interface
 *
 * Defines the contract for all IO surface implementations.
 * Any surface registers itself with the SurfaceFactory and implements this interface.
 *
 * This is the core abstraction that enables the Surface Factory pattern,
 * allowing the IO layer to support an open-ended set of surfaces
 * while maintaining a consistent API for the orchestrator.
 */

import type { Task, Message } from '../App'

/** Capabilities that a surface may support */
export interface SurfaceCapabilities {
  /** Can receive and display text */
  text: boolean
  /** Can record and send voice input */
  voice: boolean
  /** Can capture and send images */
  image: boolean
  /** Can capture and send video */
  video: boolean
  /** Can display rich UI cards (not just plain text) */
  richCards: boolean
  /** Can send push notifications to the user */
  pushNotifications: boolean
  /** Can make phone calls for urgent alerts */
  voiceCalls: boolean
}

/** Input types that a surface can receive */
export type InputType = 'text' | 'voice' | 'image' | 'video'

/** Processed input from the user */
export interface ProcessedInput {
  type: InputType
  content: string // For text: the message. For media: a path or base64
  taskId?: string // Optional context - if not provided, surface may prompt or create new
  metadata?: Record<string, unknown>
}

/** Status update from the orchestrator for a task */
export interface StatusUpdate {
  taskId: string
  status: 'awaiting_feedback' | 'working' | 'completed'
  lastUpdate: string
  expectedNextInputMinutes: number | null
  timestamp?: string // ISO 8601
}

/** Agent response to deliver to the user */
export interface AgentResponse {
  taskId: string
  content: string // May be plain text or rich card JSON
  isRichCard?: boolean
  streamComplete?: boolean
}

/** Notification to deliver to the user */
export interface Notification {
  id: string
  priority: 'urgent' | 'important' | 'normal'
  title: string
  body: string
  taskId?: string
  actionUrl?: string
}

/** The Surface Adapter interface - all surfaces implement this */
export interface SurfaceAdapter {
  /** Unique identifier for this surface instance */
  readonly surfaceId: string
  /** Human-readable name for this surface */
  readonly surfaceName: string
  /** Type of surface (matches the key used to register with SurfaceFactory) */
  readonly surfaceType: string
  /** Capabilities of this surface */
  readonly capabilities: SurfaceCapabilities

  /**
   * Initialize the surface adapter.
   * Called once when the surface is created.
   */
  initialize(): Promise<void>

  /**
   * Receive processed input from the user.
   * The surface has already converted raw input (typing, voice, etc.)
   * into a ProcessedInput that can be forwarded to the orchestrator.
   */
  receiveInput(input: ProcessedInput): void

  /**
   * Display a task status update to the user.
   * Called when the orchestrator pushes status changes.
   */
  showTaskStatus(update: StatusUpdate): void

  /**
   * Deliver an agent response to the user.
   * May be streamed (streamComplete=false) or complete (streamComplete=true).
   */
  deliverResponse(response: AgentResponse): void

  /**
   * Send a notification to the user.
   * Surface handles routing based on priority and capabilities.
   */
  notify(notification: Notification): void

  /**
   * Get all tasks visible to this surface.
   * Returns current task list for surfaces that display tasks.
   */
  getTasks(): Task[]

  /**
   * Get conversation history for a specific task.
   */
  getConversationHistory(taskId: string): Message[]

  /**
   * Shutdown the surface adapter.
   * Called when the surface is being destroyed.
   */
  shutdown(): Promise<void>
}

/** Default capabilities for a basic text-only surface (e.g., CLI) */
export const BASE_CAPABILITIES: SurfaceCapabilities = {
  text: true,
  voice: false,
  image: false,
  video: false,
  richCards: false,
  pushNotifications: false,
  voiceCalls: false,
}

/** Capabilities for a full-featured web dashboard */
export const WEB_DASHBOARD_CAPABILITIES: SurfaceCapabilities = {
  text: true,
  voice: true,     // Future: voice input via Web Speech API
  image: true,     // Future: file upload
  video: true,     // Future: video upload
  richCards: true, // React UI can render rich cards
  pushNotifications: false, // Could implement with Service Workers
  voiceCalls: false,
}

/** Capabilities preset for messaging-style surfaces (push notifications, media, no rich cards) */
export const MESSAGING_CAPABILITIES: SurfaceCapabilities = {
  text: true,
  voice: true,
  image: true,
  video: true,
  richCards: false,
  pushNotifications: true,
  voiceCalls: false,
}
