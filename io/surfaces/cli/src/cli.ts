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
import { dispatchAdmin, isAdminCommand } from './admin'

async function main() {
  // One-shot admin commands run and exit; the interactive REPL is the
  // default when no positional arg is given (preserves existing UX).
  const argv = process.argv.slice(2)
  if (argv.length > 0 && isAdminCommand(argv)) {
    const code = await dispatchAdmin(argv)
    process.exit(code)
  }
  const repl = new CerberOSREPL(config)
  await repl.start()
}

main().catch(err => {
  console.error(JSON.stringify({
    time: new Date().toISOString(),
    level: 'ERROR',
    component: 'io',
    module: 'cli',
    msg: 'fatal error',
    error: String(err),
    exit_code: 1,
  }))
  process.exit(1)
})
