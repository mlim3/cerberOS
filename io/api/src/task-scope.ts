/**
 * MT-3: per-task in-memory state, scoped by (userId, taskId).
 *
 * The IO API server holds short-term state for each in-flight task in three
 * Maps: latest status (`tasks`), parked chat-stream callbacks
 * (`pendingChatResponses`), and SSE subscribers (`sseClients`). Pre-MT-3 all
 * three were keyed by `taskId` alone, which let two users collide on a short
 * id and let an attacker tap another user's stream by guessing the id.
 *
 * This module owns those maps, exposes only the composite-key APIs, and is
 * side-effect-free so tests can import it without spinning up NATS or the
 * HTTP listener.
 *
 * Orchestrator inbound paths (NATS callback, /api/orchestrator/stream-events)
 * carry only `taskId`, so we also keep a `taskOwnership` reverse index
 * populated when a task is registered. Unknown-owner events are dropped at
 * the call site — the caller logs and skips broadcast rather than guessing.
 */

import type { OrchestratorStreamEvent, StatusUpdate } from '@cerberos/io-core'

export type SsePush = (bytes: Uint8Array) => void

export type ChatResponseCallback = {
  push: (content: string) => void
  complete: () => void
  error: (msg: string) => void
}

/** Canonical taskId form — uppercase UUIDs and stray whitespace must collapse. */
export function chatPendingKey(taskId: string): string {
  return taskId.trim().toLowerCase()
}

/** Composite key for per-task state. Format: `<userId>:<taskId>` (both lowercased). */
export function scopedKey(userId: string, taskId: string): string {
  return `${userId.trim().toLowerCase()}:${chatPendingKey(taskId)}`
}

const taskOwnership = new Map<string, string>()
const tasks = new Map<string, StatusUpdate>()
const sseClients = new Map<string, Set<SsePush>>()
const pendingChatResponses = new Map<string, ChatResponseCallback>()

const text = new TextEncoder()

export function recordTaskOwnership(userId: string, taskId: string): void {
  taskOwnership.set(chatPendingKey(taskId), userId.trim().toLowerCase())
}

export function ownerOfTask(taskId: string): string | undefined {
  return taskOwnership.get(chatPendingKey(taskId))
}

export function getTaskStatus(userId: string, taskId: string): StatusUpdate | undefined {
  return tasks.get(scopedKey(userId, taskId))
}

export function setTaskStatus(userId: string, taskId: string, status: StatusUpdate): void {
  tasks.set(scopedKey(userId, taskId), status)
}

export function listTasksForUser(userId: string): StatusUpdate[] {
  const prefix = `${userId.trim().toLowerCase()}:`
  const out: StatusUpdate[] = []
  for (const [key, value] of tasks) {
    if (key.startsWith(prefix)) out.push(value)
  }
  return out
}

export function registerPendingChatCallback(
  userId: string,
  taskId: string,
  cb: ChatResponseCallback,
): void {
  pendingChatResponses.set(scopedKey(userId, taskId), cb)
}

export function dropPendingChatCallback(userId: string, taskId: string): void {
  pendingChatResponses.delete(scopedKey(userId, taskId))
}

export function deliverChatResponse(
  userId: string,
  taskId: string,
  content: string,
  done: boolean,
): boolean {
  const key = scopedKey(userId, taskId)
  const cb = pendingChatResponses.get(key)
  if (!cb) return false
  cb.push(content)
  if (done) {
    cb.complete()
    pendingChatResponses.delete(key)
  }
  return true
}

export function failPendingChatCallback(userId: string, taskId: string, msg: string): boolean {
  const key = scopedKey(userId, taskId)
  const cb = pendingChatResponses.get(key)
  if (!cb) return false
  cb.error(msg)
  pendingChatResponses.delete(key)
  return true
}

export function subscribeSse(userId: string, taskId: string, push: SsePush): () => void {
  const key = scopedKey(userId, taskId)
  let set = sseClients.get(key)
  if (!set) {
    set = new Set()
    sseClients.set(key, set)
  }
  set.add(push)
  return () => {
    set!.delete(push)
    if (set!.size === 0) sseClients.delete(key)
  }
}

export function broadcastStreamEvent(
  userId: string,
  taskId: string,
  event: OrchestratorStreamEvent,
): void {
  const set = sseClients.get(scopedKey(userId, taskId))
  if (!set) return
  const bytes = text.encode(`data: ${JSON.stringify(event)}\n\n`)
  for (const push of set) {
    try {
      push(bytes)
    } catch {
      /* client disconnected */
    }
  }
}

export function broadcastStatus(userId: string, taskId: string, status: StatusUpdate): void {
  broadcastStreamEvent(userId, taskId, { type: 'status', payload: status })
}

export function persistAndBroadcastStatus(userId: string, status: StatusUpdate): void {
  setTaskStatus(userId, status.taskId, status)
  broadcastStatus(userId, status.taskId, status)
}

/**
 * Test-only hook. Wipes every map. Not exported through any production path;
 * `index.ts` never calls it. Keeps test isolation cheap.
 */
export function __resetTaskScopeForTests(): void {
  taskOwnership.clear()
  tasks.clear()
  sseClients.clear()
  pendingChatResponses.clear()
}
