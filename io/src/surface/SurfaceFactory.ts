/**
 * Surface Factory
 *
 * Factory pattern implementation for creating SurfaceAdapter instances.
 * Enables the IO layer to support multiple surfaces (web, telegram, whatsapp, etc.)
 * without hardcoding surface creation logic.
 *
 * Usage:
 *   const surface = SurfaceFactory.create({ type: 'web' })
 *   const telegram = SurfaceFactory.create({ type: 'telegram', token: '...' })
 */

import type { SurfaceAdapter, SurfaceCapabilities } from './SurfaceAdapter'
import { BASE_CAPABILITIES } from './SurfaceAdapter'

// Import existing implementations
import { WebSurfaceAdapter, createWebSurface, type WebSurfaceConfig } from './WebSurfaceAdapter'

/** Configuration for creating a surface */
export interface SurfaceConfig {
  /** Type of surface to create */
  type: 'web' | 'telegram' | 'whatsapp' | 'cli' | 'wearable'
  /** Unique identifier for this surface instance */
  surfaceId?: string
  /** Surface-specific configuration */
  config?: WebSurfaceConfig | TelegramSurfaceConfig | WhatsAppSurfaceConfig | CLISurfaceConfig | WearableSurfaceConfig
}

/** Telegram-specific configuration */
export interface TelegramSurfaceConfig {
  botToken: string
  allowedUsers?: string[]
}

/** WhatsApp-specific configuration */
export interface WhatsAppSurfaceConfig {
  phoneNumberId: string
  accessToken: string
  verifyToken?: string
}

/** CLI-specific configuration */
export interface CLISurfaceConfig {
  surfaceId?: string
  prompt?: string
  historyFile?: string
}

/** Wearable-specific configuration */
export interface WearableSurfaceConfig {
  deviceType: 'watch' | 'ring' | 'glasses'
  connectionType: 'bluetooth' | 'wifi'
}

/** Registry of surface creators */
type SurfaceCreator = (config: SurfaceConfig) => SurfaceAdapter | Promise<SurfaceAdapter>

/** Registry mapping surface types to creators */
const surfaceRegistry: Map<string, SurfaceCreator> = new Map()

/** Register a surface type with its creator function */
export function registerSurface(type: string, creator: SurfaceCreator): void {
  surfaceRegistry.set(type.toLowerCase(), creator)
  console.log(`[SurfaceFactory] Registered surface type: ${type}`)
}

/** Check if a surface type is registered */
export function isSurfaceRegistered(type: string): boolean {
  return surfaceRegistry.has(type.toLowerCase())
}

/** Get list of registered surface types */
export function getRegisteredSurfaces(): string[] {
  return Array.from(surfaceRegistry.keys())
}

/**
 * Create a surface adapter based on configuration.
 *
 * @param config - Configuration specifying the type and options for the surface
 * @returns A SurfaceAdapter instance
 * @throws Error if the surface type is not registered
 */
export async function createSurface(config: SurfaceConfig): Promise<SurfaceAdapter> {
  const type = config.type.toLowerCase()
  const creator = surfaceRegistry.get(type)

  if (!creator) {
    const available = getRegisteredSurfaces().join(', ')
    throw new Error(
      `Unknown surface type: "${type}". Available surfaces: ${available || 'none'}`
    )
  }

  console.log(`[SurfaceFactory] Creating surface: ${type}`)
  return creator(config)
}

/**
 * Create the default web surface adapter.
 * This is a convenience function for the common case.
 */
export function createWebSurfaceAdapter(config?: WebSurfaceConfig): SurfaceAdapter {
  return new WebSurfaceAdapter(config)
}

/**
 * Get the default surface for a given context.
 * This helps when you need a surface but don't care which one.
 */
export function getDefaultSurface(): SurfaceAdapter {
  // Default to web surface
  return createWebSurfaceAdapter()
}

// ============================================================================
// Built-in Surface Implementations
// ============================================================================

/**
 * Web Surface Creator
 *
 * Creates a web surface adapter for the React dashboard.
 * This is registered by default.
 */
function createWebSurfaceCreator(_config: SurfaceConfig): SurfaceAdapter {
  const webConfig = _config.config as WebSurfaceConfig | undefined
  return createWebSurface(webConfig)
}

/**
 * CLI Surface Creator
 *
 * Creates a command-line interface surface adapter.
 * Useful for server-side interactions or testing.
 */
function createCLISurfaceCreator(_config: SurfaceConfig): SurfaceAdapter {
  const cliConfig = _config.config as CLISurfaceConfig | undefined

  return new CLISurfaceAdapter(cliConfig)
}

/**
 * CLI Surface Adapter
 *
 * A minimal surface implementation for command-line environments.
 * Supports text input/output only.
 */
class CLISurfaceAdapter implements SurfaceAdapter {
  readonly surfaceId: string
  readonly surfaceName: string
  readonly surfaceType: string = 'cli'
  readonly capabilities: SurfaceCapabilities = { ...BASE_CAPABILITIES }

  constructor(config?: CLISurfaceConfig) {
    this.surfaceId = config?.surfaceId ?? `cli-${Date.now()}`
    this.surfaceName = 'Command Line Interface'
  }

  async initialize(): Promise<void> {
    console.log(`[CLISurfaceAdapter] Initialized: ${this.surfaceId}`)
  }

  receiveInput(input: { type: 'text'; content: string; taskId?: string }): void {
    console.log(`[CLI] Input: ${input.content}`)
  }

  showTaskStatus(update: { taskId: string; status: string; lastUpdate: string }): void {
    console.log(`[CLI] Task ${update.taskId}: ${update.status} - ${update.lastUpdate}`)
  }

  deliverResponse(response: { taskId: string; content: string }): void {
    console.log(`[CLI] Response: ${response.content}`)
  }

  notify(notification: { title: string; body: string; priority: string }): void {
    console.log(`[CLI] ${notification.priority.toUpperCase()}: ${notification.title} - ${notification.body}`)
  }

  getTasks(): any[] {
    return [] // CLI doesn't display tasks
  }

  getConversationHistory(_taskId: string): any[] {
    return []
  }

  async shutdown(): Promise<void> {
    console.log(`[CLISurfaceAdapter] Shutdown: ${this.surfaceId}`)
  }
}

/**
 * Placeholder creators for future surfaces.
 * These demonstrate how to add new surface types.
 *
 * To implement Telegram or WhatsApp surfaces, you would:
 * 1. Create a TelegramSurfaceAdapter class
 * 2. Register it with SurfaceFactory.registerSurface('telegram', createTelegramSurface)
 */

// function createTelegramSurfaceCreator(config: SurfaceConfig): SurfaceAdapter {
//   const telegramConfig = config.config as TelegramSurfaceConfig
//   // TODO: Implement TelegramSurfaceAdapter
//   throw new Error('Telegram surface not yet implemented')
// }

// function createWhatsAppSurfaceCreator(config: SurfaceConfig): SurfaceAdapter {
//   const whatsappConfig = config.config as WhatsAppSurfaceConfig
//   // TODO: Implement WhatsAppSurfaceAdapter
//   throw new Error('WhatsApp surface not yet implemented')
// }

// function createWearableSurfaceCreator(config: SurfaceConfig): SurfaceAdapter {
//   const wearableConfig = config.config as WearableSurfaceConfig
//   // TODO: Implement WearableSurfaceAdapter
//   throw new Error('Wearable surface not yet implemented')
// }

// ============================================================================
// Factory Initialization
// ============================================================================

/**
 * Initialize the factory with built-in surface types.
 * Called automatically when this module is imported.
 */
function initializeFactory(): void {
  // Register built-in surfaces
  registerSurface('web', createWebSurfaceCreator)
  registerSurface('cli', createCLISurfaceCreator)

  // TODO: Uncomment when implementations are ready
  // registerSurface('telegram', createTelegramSurfaceCreator)
  // registerSurface('whatsapp', createWhatsAppSurfaceCreator)
  // registerSurface('wearable', createWearableSurfaceCreator)

  console.log('[SurfaceFactory] Initialized with surfaces:', getRegisteredSurfaces().join(', '))
}

// Auto-initialize
initializeFactory()

// ============================================================================
// Surface Capabilities Utilities
// ============================================================================

/**
 * Check if a surface supports a specific capability
 */
export function surfaceSupports(surface: SurfaceAdapter, capability: keyof SurfaceCapabilities): boolean {
  return surface.capabilities[capability] === true
}

/**
 * Get the best surface from a list that supports a required capability
 */
export function findSurfaceWithCapability(
  surfaces: SurfaceAdapter[],
  capability: keyof SurfaceCapabilities
): SurfaceAdapter | undefined {
  return surfaces.find(s => surfaceSupports(s, capability))
}

/**
 * Merge capabilities from multiple surfaces.
 * Useful when a user has multiple active surfaces.
 */
export function mergeCapabilities(surfaces: SurfaceAdapter[]): SurfaceCapabilities {
  const merged: SurfaceCapabilities = {
    text: false,
    voice: false,
    image: false,
    video: false,
    richCards: false,
    pushNotifications: false,
    voiceCalls: false,
  }

  for (const surface of surfaces) {
    if (surface.capabilities.text) merged.text = true
    if (surface.capabilities.voice) merged.voice = true
    if (surface.capabilities.image) merged.image = true
    if (surface.capabilities.video) merged.video = true
    if (surface.capabilities.richCards) merged.richCards = true
    if (surface.capabilities.pushNotifications) merged.pushNotifications = true
    if (surface.capabilities.voiceCalls) merged.voiceCalls = true
  }

  return merged
}
