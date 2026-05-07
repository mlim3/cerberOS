/**
 * Surface Adapter Types
 *
 * Re-exports shared types from @cerberos/io-core.
 * All surface adapter types are defined in the core package.
 */

export type {
  SurfaceAdapter,
  SurfaceCapabilities,
  InputType,
  ProcessedInput,
  StatusUpdate,
  AgentResponse,
  Notification,
  Task,
  ChatMessage,
} from '@cerberos/io-core'

export { BASE_CAPABILITIES, WEB_DASHBOARD_CAPABILITIES } from '@cerberos/io-core'
