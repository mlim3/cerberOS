import { buildApiUrl } from './orchestrator'

export type UserCronJob = {
  id: string
  name: string
  scheduleKind: string
  intervalSeconds: number
  timeZone: string
  cronExpression: string
  nextRunAt: string
  payload: { userId?: string; rawInput?: string; conversationId?: string }
}

/** Memory/IO JSON envelope: `error` is `{ code, message, details? }`, not a string. */
export function schedulingApiErrorMessage(body: unknown, httpStatus: number): string {
  if (body && typeof body === 'object' && 'error' in body) {
    const err = (body as { error?: unknown }).error
    if (typeof err === 'string') return err
    if (err && typeof err === 'object' && 'message' in err) {
      const m = (err as { message?: unknown }).message
      if (typeof m === 'string' && m.trim()) return m
    }
  }
  return `HTTP ${httpStatus}`
}

export async function fetchUserCronJobs(userId: string): Promise<{ ok: true; jobs: UserCronJob[] } | { ok: false; error: string }> {
  try {
    const res = await fetch(
      buildApiUrl(`/api/user-crons?userId=${encodeURIComponent(userId)}`),
      { headers: { 'X-Surface-Key': 'dev' } },
    )
    const j = (await res.json()) as { ok?: boolean; data?: { jobs?: UserCronJob[] }; error?: { message?: string } }
    if (!res.ok) {
      return { ok: false, error: schedulingApiErrorMessage(j, res.status) }
    }
    return { ok: true, jobs: j.data?.jobs ?? [] }
  } catch (e) {
    return { ok: false, error: String(e) }
  }
}

export type CreateUserCronBody = {
  name: string
  userId: string
  rawInput: string
  conversationId: string
  scheduleKind: 'interval' | 'cron'
  intervalSeconds: number
  cronExpression: string
  timeZone: string
  nextRunAt: string
}

export async function createUserCronJob(body: CreateUserCronBody): Promise<{ ok: true } | { ok: false; error: string }> {
  try {
    const res = await fetch(buildApiUrl('/api/user-crons'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Surface-Key': 'dev' },
      body: JSON.stringify(body),
    })
    const j = (await res.json()) as { ok?: boolean; error?: { message?: string } }
    if (!res.ok) {
      return { ok: false, error: schedulingApiErrorMessage(j, res.status) }
    }
    return { ok: true }
  } catch (e) {
    return { ok: false, error: String(e) }
  }
}

export async function deleteUserCronJob(userId: string, jobId: string): Promise<{ ok: true } | { ok: false; error: string }> {
  try {
    const res = await fetch(
      buildApiUrl(`/api/user-crons/${encodeURIComponent(jobId)}?userId=${encodeURIComponent(userId)}`),
      { method: 'DELETE', headers: { 'X-Surface-Key': 'dev' } },
    )
    if (!res.ok) {
      const t = await res.text()
      return { ok: false, error: t || `HTTP ${res.status}` }
    }
    return { ok: true }
  } catch (e) {
    return { ok: false, error: String(e) }
  }
}
