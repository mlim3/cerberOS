/**
 * Demo logging: all user and Orchestrator/CerberOS responses are sent here.
 * Logs are not persisted; this is the single place in code where they are "sent".
 */

export type LogRole = 'user' | 'orchestrator'

export interface LogEntry {
  taskId: string
  role: LogRole
  content: string
  at: string
}

/** In-memory log (not stored to disk). Visible in devtools when inspecting window.__demoLogs */
const memoryLog: LogEntry[] = []
if (typeof window !== 'undefined') {
  ;(window as unknown as { __demoLogs?: LogEntry[] }).__demoLogs = memoryLog
}

function isoNow(): string {
  return new Date().toISOString()
}

/** Send user response to log. Call this whenever the user sends a message to the orchestrator. */
export function logUserResponse(taskId: string, content: string): void {
  const entry: LogEntry = { taskId, role: 'user', content, at: isoNow() }
  memoryLog.push(entry)
  console.log('[LOG user]', taskId, content)
}

/** Send Orchestrator/CerberOS response to log. Call this whenever the orchestrator replies. */
export function logOrchestratorResponse(taskId: string, content: string): void {
  const entry: LogEntry = { taskId, role: 'orchestrator', content, at: isoNow() }
  memoryLog.push(entry)
  console.log('[LOG orchestrator]', taskId, content)
}

export function getMemoryLog(): readonly LogEntry[] {
  return memoryLog
}
