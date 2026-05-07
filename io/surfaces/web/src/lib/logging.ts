/**
 * Demo logging: all user and Orchestrator/CerberOS responses are sent here.
 *
 * Currently this module is **not wired into the ActivityLog UI** — the web surface
 * uses App.tsx's local logEntries state for the ActivityLog. This module exists
 * to provide a future path for log persistence through the IO API server.
 *
 * When the Memory service is connected (via MEMORY_API_BASE), log entries can be
 * persisted server-side. When not configured, falls back to in-memory storage.
 *
 * NOTE: The LogEntry type here differs from ActivityLog's LogEntry — this module
 * tracks raw messages (user/orchestrator), while ActivityLog tracks enriched
 * activity events (heartbeat, user_message, agent_response, status_change).
 */

export type LogRole = 'user' | 'orchestrator'

export interface LogEntry {
  taskId: string
  role: LogRole
  content: string
  at: string
}

/** In-memory fallback log (used when IO API is not available). */
const memoryLog: LogEntry[] = []
if (typeof window !== 'undefined') {
  ;(window as unknown as { __demoLogs?: LogEntry[] }).__demoLogs = memoryLog
}

function isoNow(): string {
  return new Date().toISOString()
}

/**
 * Send user response to log. The IO API server already persists chat messages
 * via the /api/chat endpoint (orchestrator.ts). This module stores a local copy
 * for ActivityLog display. Falls back to in-memory storage.
 */
export function logUserResponse(taskId: string, content: string): void {
  const entry: LogEntry = { taskId, role: 'user', content, at: isoNow() }
  memoryLog.push(entry)
}

/**
 * Send Orchestrator/CerberOS response to log. Same local-storage pattern.
 */
export function logOrchestratorResponse(taskId: string, content: string): void {
  const entry: LogEntry = { taskId, role: 'orchestrator', content, at: isoNow() }
  memoryLog.push(entry)
}

/**
 * Retrieve log entries for a task. Fetches from IO API when available,
 * falls back to the in-memory array.
 */
export async function getMemoryLog(taskId: string): Promise<LogEntry[]> {
  // Try IO API first
  try {
    const base = (import.meta.env.VITE_IO_API_BASE as string | undefined) ?? ''
    const url = base ? `${base.replace(/\/$/, '')}/api/logs/${encodeURIComponent(taskId)}` : `/api/logs/${encodeURIComponent(taskId)}`
    const res = await fetch(url)
    if (res.ok) {
      const json = await res.json() as { logs: LogEntry[] }
      if (json.logs?.length > 0) return json.logs
    }
  } catch {
    /* fall through to in-memory */
  }

  // Fall back to in-memory
  return memoryLog.filter(m => m.taskId === taskId)
}
