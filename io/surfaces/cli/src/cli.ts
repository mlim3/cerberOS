/**
 * cerberOS CLI Surface Entry Point
 *
 * Usage:
 *   bun run src/cli.ts           # development
 *   cerberos                     # installed via npm link / bin
 *
 * Environment:
 *   CLI_API_BASE        IO API server URL (default: http://localhost:3001)
 *   CLI_USER_ID         User UUID for vault namespace (default: dev UUID)
 *   CLI_SURFACE_ID      Surface identifier (default: auto-generated)
 *   SESSION_FILE        Path to session JSON (default: ~/.cerberos/cli-session.json)
 */

import { CerberOSREPL } from './repl'
import { config } from './config'

async function main() {
  const repl = new CerberOSREPL(config)
  await repl.start()
}

main().catch(err => {
  console.error(JSON.stringify({
    time: new Date().toISOString(),
    level: 'ERROR',
    service: 'io-cli',
    component: 'cli',
    msg: 'fatal error',
    error: String(err),
    exit_code: 1,
  }))
  process.exit(1)
})
