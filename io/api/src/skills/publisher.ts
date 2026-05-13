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
  reload?: boolean
}

export interface ImportedSkillFile {
  path: string
  node: SkillNode
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

  if (opts.reload !== false) {
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
  }

  ioLog('info', 'skills', opts.reload === false ? 'skill published to memory' : 'skill published to memory + reload signaled', {
    skill: opts.node.name,
    domain: opts.domain,
    scope: opts.scope,
  })
  return true
}

export function publishSkillReload(
  natsClient: IONatsClient | null,
  domain: string,
  skillName: string,
  scope: SkillScope,
): boolean {
  if (!natsClient?.connected) {
    return false
  }
  natsClient.publishRaw(SUBJECT_SKILL_RELOAD, {
    triggered_by: 'io',
    skill_name: skillName,
    domain,
    scope,
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
  const imported = await importSkillsFromRepo(repo)
  if (imported.skills.length > 0) {
    return imported.skills[0].node
  }

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

export interface ImportSkillsFromRepoResult {
  repoSlug: string
  ref: string
  skills: ImportedSkillFile[]
  fallbackUsed: boolean
}

export async function importSkillsFromRepo(repo: string): Promise<ImportSkillsFromRepoResult> {
  const norm = normalizeRepoPath(repo)
  const repoSlug = norm.replace(/^github\.com\//, '')
  const { ref, candidatePaths } = await discoverSkillCandidates(repoSlug)

  if (candidatePaths.length === 0) {
    const node = await buildFallbackSkillNode(repoSlug)
    return {
      repoSlug,
      ref,
      skills: [{ path: 'README.md', node }],
      fallbackUsed: true,
    }
  }

  const imported: ImportedSkillFile[] = []
  for (const path of candidatePaths) {
    const content = await fetchSkillMarkdown(repoSlug, ref, path)
    if (!content) continue
    const parsed = parseSkillDocument(path, content)
    if (!looksLikeSkillDocument(path, parsed)) continue
    const node = buildSkillNodeFromMarkdown(repoSlug, path, parsed)
    imported.push({ path, node })
  }

  if (imported.length === 0) {
    const node = await buildFallbackSkillNode(repoSlug)
    return {
      repoSlug,
      ref,
      skills: [{ path: 'README.md', node }],
      fallbackUsed: true,
    }
  }

  return { repoSlug, ref, skills: imported, fallbackUsed: false }
}

async function buildFallbackSkillNode(repoSlug: string): Promise<SkillNode> {
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

async function discoverSkillCandidates(repoSlug: string): Promise<{ ref: string; candidatePaths: string[] }> {
  const ref = await getDefaultBranch(repoSlug)
  const tree = await fetchRepoTree(repoSlug, ref)
  if (!tree) {
    return { ref, candidatePaths: [] }
  }

  const candidatePaths = tree
    .filter((entry) => entry.type === 'blob' && isPotentialSkillFile(entry.path))
    .map((entry) => entry.path)

  return { ref, candidatePaths }
}

async function getDefaultBranch(repoSlug: string): Promise<string> {
  try {
    const url = `https://api.github.com/repos/${repoSlug}`
    const res = await fetch(url, {
      headers: {
        Accept: 'application/vnd.github+json',
        'User-Agent': 'cerberos-io',
      },
    })
    if (!res.ok) return 'main'
    const body = await res.json() as { default_branch?: string }
    return body.default_branch || 'main'
  } catch {
    return 'main'
  }
}

async function fetchRepoTree(repoSlug: string, ref: string): Promise<Array<{ path: string; type: string }> | null> {
  try {
    const url = `https://api.github.com/repos/${repoSlug}/git/trees/${encodeURIComponent(ref)}?recursive=1`
    const res = await fetch(url, {
      headers: {
        Accept: 'application/vnd.github+json',
        'User-Agent': 'cerberos-io',
      },
    })
    if (!res.ok) return null
    const body = await res.json() as { tree?: Array<{ path?: string; type?: string }> }
    return (body.tree ?? [])
      .filter((entry): entry is { path: string; type: string } => typeof entry.path === 'string' && typeof entry.type === 'string')
  } catch {
    return null
  }
}

async function fetchSkillMarkdown(repoSlug: string, ref: string, path: string): Promise<string | null> {
  try {
    const url = `https://raw.githubusercontent.com/${repoSlug}/${encodeURIComponent(ref)}/${path}`
    const res = await fetch(url, {
      headers: { 'User-Agent': 'cerberos-io' },
    })
    if (!res.ok) return null
    return await res.text()
  } catch {
    return null
  }
}

function isPotentialSkillFile(path: string): boolean {
  const lower = path.toLowerCase()
  const base = lower.split('/').pop() ?? lower
  if (/(^|\/)skills?(\/|$)/.test(lower)) return true
  if (/\.(md|markdown|mdx|txt|yaml|yml)$/i.test(base)) return true
  return /skill|guide|playbook|workflow|instructions?/.test(base)
}

function looksLikeSkillDocument(path: string, parsed: { frontmatter: Record<string, string>; body: string }): boolean {
  const fm = parsed.frontmatter
  const body = parsed.body.toLowerCase()
  let score = 0

  if (fm.name) score += 2
  if (fm.description) score += 2
  if (fm.when_to_use) score += 2
  if (fm.version) score += 1
  if (fm.languages) score += 1
  if (/when to use|overview|steps|usage|instructions/.test(body)) score += 1
  if (/^#\s|^##\s/m.test(parsed.body)) score += 1
  if (/(^|\/)skills?(\/|$)/.test(path.toLowerCase())) score += 1

  if (score >= 3) return true
  return Boolean(fm.name && fm.description)
}

function buildSkillNodeFromMarkdown(repoSlug: string, path: string, parsed: { frontmatter: Record<string, string>; body: string }): SkillNode {
  const frontName = normalizeSkillName(parsed.frontmatter.name || '')
  const fallbackName = normalizeSkillName(path.replace(/^.*\//, '').replace(/\.md$/i, '').replace(/[^a-z0-9_-]/gi, '_'))
  const name = frontName || fallbackName || deriveSnakeCaseName(path)
  const labelSource = parsed.frontmatter.display_name || parsed.frontmatter.name || path.replace(/^.*\//, '').replace(/\.md$/i, '')
  const label = humanizeLabel(labelSource || name)
  const descSource = parsed.frontmatter.description || parsed.frontmatter.when_to_use || summarizeMarkdown(parsed.body)
  const description = limitDescription(descSource || `Imported from ${repoSlug}/${path}`)
  const recipe = parsed.body.trim() || `Read and apply the guidance from ${repoSlug}/${path}.`

  return {
    name,
    level: 'command',
    label,
    description,
    origin: 'synthesized',
    synthesized_at: new Date().toISOString(),
    recipe,
    spec: { parameters: {} },
  }
}

function parseSkillDocument(path: string, content: string): { frontmatter: Record<string, string>; body: string } {
  const source = content.replace(/^\uFEFF/, '').replace(/\r\n/g, '\n')
  const lower = path.toLowerCase()
  if (/\.(yaml|yml)$/i.test(lower)) {
    return {
      frontmatter: parseFrontmatterLines(source.split('\n')),
      body: '',
    }
  }
  if (!source.startsWith('---\n')) {
    return { frontmatter: {}, body: source.trim() }
  }

  const lines = source.split('\n')
  const frontmatterLines: string[] = []
  let idx = 1
  for (; idx < lines.length; idx++) {
    if (lines[idx].trim() === '---') {
      idx++
      break
    }
    frontmatterLines.push(lines[idx] ?? '')
  }

  const frontmatter = parseFrontmatterLines(frontmatterLines)
  return {
    frontmatter,
    body: lines.slice(idx).join('\n').trim(),
  }
}

function parseFrontmatterLines(lines: string[]): Record<string, string> {
  const out: Record<string, string> = {}
  let currentKey = ''
  let currentMode: 'scalar' | 'block' = 'scalar'
  let blockMarker: '|' | '>' | '' = ''

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? ''
    const match = line.match(/^([A-Za-z0-9_-]+):(?:\s*(.*))?$/)
    if (match) {
      currentKey = match[1]!
      const rawValue = (match[2] ?? '').trim()
      if (rawValue === '|' || rawValue === '>') {
        currentMode = 'block'
        blockMarker = rawValue
        out[currentKey] = ''
        continue
      }
      currentMode = 'scalar'
      blockMarker = ''
      out[currentKey] = unquoteYamlScalar(rawValue)
      continue
    }

    if (currentMode === 'block' && currentKey) {
      const trimmed = line.replace(/^\s{2}/, '')
      if (out[currentKey]) {
        out[currentKey] += blockMarker === '>' ? ` ${trimmed.trim()}` : `\n${trimmed}`
      } else {
        out[currentKey] = trimmed.trim()
      }
    }
  }

  for (const [key, value] of Object.entries(out)) {
    out[key] = value.trim()
  }
  return out
}

function unquoteYamlScalar(value: string): string {
  const trimmed = value.trim()
  if ((trimmed.startsWith('"') && trimmed.endsWith('"')) || (trimmed.startsWith("'") && trimmed.endsWith("'"))) {
    return trimmed.slice(1, -1)
  }
  return trimmed
}

function summarizeMarkdown(body: string): string {
  const text = body
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .slice(0, 5)
    .join(' ')
  return text.slice(0, 280)
}

function limitDescription(text: string): string {
  const trimmed = text.trim()
  if (trimmed.length <= 300) return trimmed
  return `${trimmed.slice(0, 297)}...`
}

function humanizeLabel(text: string): string {
  const base = text.trim().replace(/[_-]+/g, ' ')
  if (!base) return 'Imported Skill'
  return base
    .split(/\s+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ')
}

function normalizeSkillName(text: string): string {
  const cleaned = text
    .toLowerCase()
    .replace(/[^a-z0-9\s_-]/g, ' ')
    .trim()
    .split(/[\s-]+/)
    .filter(Boolean)
    .slice(0, 5)
    .join('_')
  return cleaned.slice(0, 64)
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
