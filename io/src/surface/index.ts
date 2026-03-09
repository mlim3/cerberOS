/**
 * Surface Adapter Module
 *
 * Exports all surface-related types and utilities.
 *
 * Core concepts:
 * - SurfaceAdapter: Interface that all surfaces implement
 * - SurfaceFactory: Registry-driven factory for creating surfaces
 * - Built-in adapters: WebSurfaceAdapter, CLISurfaceAdapter
 *
 * Usage:
 *   import { createSurface, registerSurface } from './surface'
 *
 *   const surface = await createSurface({ type: 'web' })
 */

export * from './SurfaceAdapter'
export * from './WebSurfaceAdapter'
export * from './CLISurfaceAdapter'
export * from './SurfaceFactory'
