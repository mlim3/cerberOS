/**
 * Populate process.env from ancestor .env files before other modules read env
 * (Docker/K8s normally inject vars; this helps local `bun run` from io/ or io/api/).
 */

import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

function applyEnvFile(filePath: string) {
  const raw = readFileSync(filePath, 'utf8')
  for (const line of raw.split(/\r?\n/)) {
    const s = line.trim()
    if (!s || s.startsWith('#')) continue
    const eq = s.indexOf('=')
    if (eq <= 0) continue
    const key = s.slice(0, eq).trim()
    let val = s.slice(eq + 1).trim()
    if (
      (val.startsWith('"') && val.endsWith('"')) ||
      (val.startsWith("'") && val.endsWith("'"))
    ) {
      val = val.slice(1, -1)
    }
    const cur = process.env[key]
    if (cur === undefined || cur === '') {
      process.env[key] = val
    }
  }
}

function loadAncestorDotenv() {
  const paths: string[] = []
  let dir = dirname(fileURLToPath(import.meta.url))
  for (let i = 0; i < 10; i++) {
    const p = join(dir, '.env')
    if (existsSync(p)) paths.push(p)
    const parent = dirname(dir)
    if (parent === dir) break
    dir = parent
  }
  paths.reverse()
  for (const p of paths) {
    applyEnvFile(p)
  }
}

/** Memory service uses INTERNAL_VAULT_API_KEY; docker-compose duplicates it as MEMORY_API_KEY. Normalize so both exist when either is set. */
function mirrorMemoryCredentials() {
  const mem = (process.env.MEMORY_API_KEY ?? '').trim()
  const vault = (process.env.INTERNAL_VAULT_API_KEY ?? '').trim()
  if (vault && !mem) process.env.MEMORY_API_KEY = vault
  if (mem && !vault) process.env.INTERNAL_VAULT_API_KEY = mem
}

loadAncestorDotenv()
mirrorMemoryCredentials()
