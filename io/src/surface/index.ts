/**
 * Surface Adapter Module
 *
 * Exports all surface-related types and utilities.
 *
 * Core concepts:
 * - SurfaceAdapter: Interface that all surfaces implement
 * - SurfaceFactory: Creates surface instances
 * - WebSurfaceAdapter: The React web dashboard implementation
 *
 * Usage:
 *   import { SurfaceFactory, getSurfaceAdapter } from './surface'
 *
 *   // Create a new surface
 *   const surface = await SurfaceFactory.create({ type: 'web' })
 *
 *   // Get the existing web surface
 *   const webSurface = getSurfaceAdapter()
 */

// Re-export everything
export * from './SurfaceAdapter'
export * from './WebSurfaceAdapter'
export * from './SurfaceFactory'
