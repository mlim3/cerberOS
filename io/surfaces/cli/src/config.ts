/**
 * CLI configuration from environment variables.
 * All values have sensible defaults for local development.
 */

import { join } from 'path'
import { homedir } from 'os'

export interface CLIConfig {
  apiBase: string
  orchestratorUrl: string
  natsUrl?: string
  sessionFile: string
  surfaceId: string
  userId: string
  color: boolean
  showSpinners: boolean
  pollIntervalMs: number
}

const DEFAULT_API_BASE = process.env.CLI_API_BASE ?? 'http://localhost:3001'
const DEFAULT_SESSION_FILE = join(homedir(), '.cerberos', 'cli-session.json')

function loadConfig(): CLIConfig {
  return {
    apiBase: process.env.CLI_API_BASE ?? DEFAULT_API_BASE,
    orchestratorUrl: process.env.ORCHESTRATOR_URL ?? 'http://localhost:9000',
    natsUrl: process.env.NATS_URL,
    sessionFile: process.env.SESSION_FILE ?? DEFAULT_SESSION_FILE,
    surfaceId: process.env.CLI_SURFACE_ID ?? `cli-${os.hostname()}-${Date.now()}`,
    userId: process.env.CLI_USER_ID ?? '00000000-0000-0000-0000-000000000001',
    color: process.env.CLI_NO_COLOR !== '1' && !process.env.NO_COLOR,
    showSpinners: process.env.CLI_NO_SPINNER !== '1',
    pollIntervalMs: parseInt(process.env.CLI_POLL_INTERVAL ?? '2000'),
  }
}

// We need os at runtime for the hostname fallback
import { hostname as osHostname } from 'os'

// Re-parse with proper hostname access
function loadConfigFinal(): CLIConfig {
  return {
    apiBase: process.env.CLI_API_BASE ?? DEFAULT_API_BASE,
    orchestratorUrl: process.env.ORCHESTRATOR_URL ?? 'http://localhost:9000',
    natsUrl: process.env.NATS_URL,
    sessionFile: process.env.SESSION_FILE ?? DEFAULT_SESSION_FILE,
    surfaceId: process.env.CLI_SURFACE_ID ?? `cli-${osHostname()}-${Date.now()}`,
    userId: process.env.CLI_USER_ID ?? '00000000-0000-0000-0000-000000000001',
    color: process.env.CLI_NO_COLOR !== '1' && !process.env.NO_COLOR,
    showSpinners: process.env.CLI_NO_SPINNER !== '1',
    pollIntervalMs: parseInt(process.env.CLI_POLL_INTERVAL ?? '2000'),
  }
}

export const config = loadConfigFinal()
