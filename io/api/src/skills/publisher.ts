/**
 * Skill publisher — IO writes skill_cache records to memory via NATS.
 *
 * The agents-component reads `skill_cache` entries at startup (and on the new
 * `aegis.agents.skill.reload` trigger) to populate the live M4 skill tree.
 * IO can therefore extend the runtime skill set without spawning an agent
 * process: build a SkillNode, wrap it in a MemoryWrite envelope, publish on
 * `aegis.orchestrator.state.write`, then nudge aegis-agents to reload.
 *
 * For the FP-Stefan demo this is a permissive, non-LLM derivation: we sketch a
 * SkillNode from the description / repo README and rely on the orchestrator's
 * existing planner to actually wire the skill into a generated plan when a
 * user invokes it. A full LLM-backed extraction can replace `synthesizeNode`
 * later without changing the publish/reload plumbing.
 */

import type { IONatsClient } from '../nats/client'
import { ioLog } from '../logger'

const SUBJECT_STATE_WRITE = 'aegis.orchestrator.state.write'
const SUBJECT_SKILL_RELOAD = 'aegis.agents.skill.reload'

export type SkillScope = 'me' | 'all'

export interface SkillNode {
  name: string
  level: 'command'
  label?: string
  description?: string
  origin: 'synthesized'
  synthesized_at: string
  recipe?: string
  spec?: {
    parameters?: Record<string, { type: string; required: boolean; description: string }>
  }
}

export interface PublishSkillOptions {
  domain: string
  node: SkillNode
  ownerUserId: string
  scope: SkillScope
}

interface MemoryWriteEnvelope {
  agent_id: string
  session_id: string
  data_type: string
  ttl_hint_seconds: number
  payload: SkillNode
  tags: Record<string, string>
  request_id?: string
  require_ack?: boolean
}

interface OutboundEnvelope {
  message_id: string
  message_type: string
  source_component: string
  correlation_id?: string
  trace_id?: string
  timestamp: string
  schema_version: string
  payload: MemoryWriteEnvelope
}

function uuid(): string {
  return crypto.randomUUID()
}

/**
 * Publish a synthesized SkillNode to memory and trigger aegis-agents to reload.
 * Returns true if both publishes succeeded; false otherwise. Failures are
 * logged but never thrown — this is a best-effort path.
 */
export function publishSkill(
  natsClient: IONatsClient | null,
  opts: PublishSkillOptions,
): boolean {
  if (!natsClient?.connected) {
    ioLog('warn', 'skills', 'NATS disconnected; skill publish skipped', {
      skill: opts.node.name,
      domain: opts.domain,
    })
    return false
  }

  const tags: Record<string, string> = {
    domain: opts.domain,
    origin: 'synthesized',
    skill_name: opts.node.name,
    scope: opts.scope === 'all' ? 'global' : 'user',
  }
  if (opts.ownerUserId) {
    tags.owner_user_id = opts.ownerUserId
  }

  // session_id and agent_id are required by validateMemoryWrite on the
  // memory-side. We use synthetic IDs that mark this as IO-published.
  const requestId = uuid()
  const memoryWrite: MemoryWriteEnvelope = {
    agent_id: `io-skill-publisher`,
    session_id: `io-skill-publisher-${requestId}`,
    data_type: 'skill_cache',
    ttl_hint_seconds: 0,
    payload: opts.node,
    tags,
    request_id: requestId,
    require_ack: false,
  }

  const envelope: OutboundEnvelope = {
    message_id: uuid(),
    message_type: 'state.write',
    source_component: 'io',
    correlation_id: requestId,
    timestamp: new Date().toISOString(),
    schema_version: '1.0',
    payload: memoryWrite,
  }

  natsClient.publishRaw(SUBJECT_STATE_WRITE, envelope)

  // Reload trigger: aegis-agents subscribes to this subject and re-runs
  // LoadSynthesizedSkills, which picks up the record we just wrote. Best
  // effort — the skill is durably in memory either way; reload only avoids
  // requiring a manual restart.
  natsClient.publishRaw(SUBJECT_SKILL_RELOAD, {
    triggered_by: 'io',
    skill_name: opts.node.name,
    domain: opts.domain,
    scope: opts.scope,
  })

  ioLog('info', 'skills', 'skill published to memory + reload signaled', {
    skill: opts.node.name,
    domain: opts.domain,
    scope: opts.scope,
  })
  return true
}

/**
 * Build a SkillNode from a free-text description. The name is derived from
 * the first few alphanumeric tokens; description is truncated to fit the
 * agents-component Tool Contract limit (300 chars). For the FP-Stefan demo
 * this is intentionally heuristic — an LLM-driven extractor can replace
 * this without changing callers.
 */
export function nodeFromDescription(description: string): SkillNode {
  const trimmed = description.trim()
  const name = deriveSnakeCaseName(trimmed)
  const label = trimmed.length > 60 ? `${trimmed.slice(0, 57)}...` : trimmed
  const desc = ensureNegativeGuidance(trimmed)
  return {
    name,
    level: 'command',
    label,
    description: desc.length > 300 ? desc.slice(0, 297) + '...' : desc,
    origin: 'synthesized',
    synthesized_at: new Date().toISOString(),
    recipe: `1. Interpret the user's request in the context of: ${trimmed}\n2. Use available domain tools to satisfy the request.\n3. Return the result.`,
    spec: { parameters: {} },
  }
}

/**
 * Build a SkillNode from a GitHub repo (URL or "github.com/user/repo" form).
 * Derives a sensible skill name from the repo path and uses the README (if
 * fetchable) as the description body. Network failures are tolerated — we
 * fall back to a name-only stub so the user still gets a registered skill.
 */
export async function nodeFromRepo(repo: string): Promise<SkillNode> {
  const norm = normalizeRepoPath(repo)
  const repoSlug = norm.replace(/^github\.com\//, '')
  const name = deriveSnakeCaseName(repoSlug.replace(/\//g, '_'))

  let readmeBody = ''
  try {
    const url = `https://api.github.com/repos/${repoSlug}/readme`
    const res = await fetch(url, {
      headers: { Accept: 'application/vnd.github.raw' },
    })
    if (res.ok) {
      readmeBody = await res.text()
    }
  } catch {
    // Network errors are fine — we degrade to a stub skill node.
  }

  const summary = readmeBody
    ? readmeBody.split('\n').filter((l) => l.trim()).slice(0, 5).join(' ').slice(0, 240)
    : `Imported from ${repoSlug}`
  const desc = ensureNegativeGuidance(`From ${repoSlug}: ${summary}`)

  return {
    name,
    level: 'command',
    label: repoSlug,
    description: desc.length > 300 ? desc.slice(0, 297) + '...' : desc,
    origin: 'synthesized',
    synthesized_at: new Date().toISOString(),
    recipe: `1. Recognize that the user wants to invoke the imported "${repoSlug}" skill.\n2. Use available domain tools (web, github, vault) to fulfil the request consistent with that skill's intent.\n3. Return the result.`,
    spec: { parameters: {} },
  }
}

function deriveSnakeCaseName(text: string): string {
  const cleaned = text
    .toLowerCase()
    .replace(/[^a-z0-9\s_-]/g, ' ')
    .trim()
    .split(/[\s-]+/)
    .filter(Boolean)
    .slice(0, 5)
    .join('_')
  const truncated = (cleaned || 'imported_skill').slice(0, 60)
  return truncated || 'imported_skill'
}

function ensureNegativeGuidance(desc: string): string {
  if (/do not use when/i.test(desc) || /not for/i.test(desc)) return desc
  return `${desc}. Do not use when the user has not explicitly requested this skill.`
}

function normalizeRepoPath(repo: string): string {
  let r = repo.trim()
  r = r.replace(/^https?:\/\//, '')
  r = r.replace(/\.git$/, '')
  r = r.replace(/\/$/, '')
  if (!r.startsWith('github.com/')) {
    if (r.includes('/') && !r.includes('.')) r = `github.com/${r}`
  }
  return r
}
