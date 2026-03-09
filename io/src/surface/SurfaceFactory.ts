/**
 * Surface Factory
 *
 * Registry-driven factory for creating SurfaceAdapter instances.
 * The factory itself contains NO surface implementations — those live
 * in their own files and register themselves at import time.
 *
 * To add a new surface from an external module:
 *
 *   // 1. Augment the type map (optional, for strong typing)
 *   declare module '@cerberOS/io/surface/SurfaceFactory' {
 *     interface SurfaceTypeMap {
 *       telegram: { botToken: string }
 *     }
 *   }
 *
 *   // 2. Register the creator
 *   registerSurface('telegram', (config) => new TelegramSurfaceAdapter(config.config))
 *
 * That's it — createSurface({ type: 'telegram', config: { botToken: '...' } })
 * is now strongly typed and works at runtime.
 */

import type { SurfaceAdapter, SurfaceCapabilities } from './SurfaceAdapter'

// Import built-in implementations (they self-register via initializeBuiltins)
import { createWebSurface, type WebSurfaceConfig } from './WebSurfaceAdapter'
import { CLISurfaceAdapter, type CLISurfaceConfig } from './CLISurfaceAdapter'

// ============================================================================
// Extensible Type Map
// ============================================================================

/**
 * Surface type map — open for extension via TypeScript module augmentation.
 *
 * Built-in entries are seeded here. External surfaces extend this interface
 * from their own modules, and the factory picks up the types automatically.
 */
export interface SurfaceTypeMap {
  web: WebSurfaceConfig | undefined
  cli: CLISurfaceConfig | undefined
}

/** Known surface type keys (extends as SurfaceTypeMap is augmented) */
export type SurfaceType = keyof SurfaceTypeMap & string

/** Configuration for creating a surface */
export type SurfaceConfig<TType extends string = SurfaceType> = {
  /** Type of surface to create */
  type: TType
  /** Unique identifier for this surface instance */
  surfaceId?: string
  /** Surface-specific configuration */
  config?: TType extends SurfaceType ? SurfaceTypeMap[TType] : unknown
}

// ============================================================================
// Registry
// ============================================================================

/** A function that creates a SurfaceAdapter from config */
type SurfaceCreator<TType extends string = string> = (
  config: SurfaceConfig<TType>
) => SurfaceAdapter | Promise<SurfaceAdapter>

/** Runtime registry mapping surface type keys to creator functions */
const surfaceRegistry = new Map<string, SurfaceCreator<string>>()

/** Name of the default surface type used by getDefaultSurface() */
let defaultSurfaceType = 'web'

// ============================================================================
// Public API
// ============================================================================

/** Register a surface type with its creator function */
export function registerSurface<TType extends SurfaceType>(
  type: TType,
  creator: SurfaceCreator<TType>
): void
export function registerSurface(type: string, creator: SurfaceCreator<string>): void
export function registerSurface(type: string, creator: SurfaceCreator<string>): void {
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
 * Set which surface type getDefaultSurface() returns.
 * Defaults to 'web' if never called.
 */
export function setDefaultSurfaceType(type: string): void {
  defaultSurfaceType = type.toLowerCase()
}

/**
 * Create a surface adapter based on configuration.
 *
 * @param config - Configuration specifying the type and options for the surface
 * @returns A SurfaceAdapter instance
 * @throws Error if the surface type is not registered
 */
export async function createSurface<TType extends SurfaceType>(
  config: SurfaceConfig<TType>
): Promise<SurfaceAdapter>
export async function createSurface(config: SurfaceConfig<string>): Promise<SurfaceAdapter>
export async function createSurface(config: SurfaceConfig<string>): Promise<SurfaceAdapter> {
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
 * Get the default surface for a given context.
 * Routes through the registry — the default type is configurable via setDefaultSurfaceType().
 */
export async function getDefaultSurface(): Promise<SurfaceAdapter> {
  return createSurface({ type: defaultSurfaceType })
}

// ============================================================================
// Capability Utilities
// ============================================================================

/** Check if a surface supports a specific capability */
export function surfaceSupports(surface: SurfaceAdapter, capability: keyof SurfaceCapabilities): boolean {
  return surface.capabilities[capability] === true
}

/** Get the best surface from a list that supports a required capability */
export function findSurfaceWithCapability(
  surfaces: SurfaceAdapter[],
  capability: keyof SurfaceCapabilities
): SurfaceAdapter | undefined {
  return surfaces.find(s => surfaceSupports(s, capability))
}

/** Merge capabilities from multiple surfaces */
export function mergeCapabilities(surfaces: SurfaceAdapter[]): SurfaceCapabilities {
  if (surfaces.length === 0) {
    return { text: false, voice: false, image: false, video: false, richCards: false, pushNotifications: false, voiceCalls: false }
  }

  // Start from a copy of the first surface's capabilities (all false)
  const merged = { ...surfaces[0].capabilities }
  for (const key of Object.keys(merged) as (keyof SurfaceCapabilities)[]) {
    merged[key] = false
  }

  // OR together: if any surface has a capability, the merged set has it
  for (const surface of surfaces) {
    for (const key of Object.keys(surface.capabilities) as (keyof SurfaceCapabilities)[]) {
      if (surface.capabilities[key]) {
        merged[key] = true
      }
    }
  }

  return merged
}

// ============================================================================
// Built-in Registration
// ============================================================================

function initializeBuiltins(): void {
  registerSurface('web', (config) => createWebSurface(config.config as WebSurfaceConfig | undefined))
  registerSurface('cli', (config) => new CLISurfaceAdapter(config.config as CLISurfaceConfig | undefined))

  console.log('[SurfaceFactory] Initialized with surfaces:', getRegisteredSurfaces().join(', '))
}

// Auto-initialize built-ins on import
initializeBuiltins()
