/**
 * Single source of caller identity for all IO API routes.
 *
 * Demo-mode honor system: trusts the `X-Active-User` header set by the UI's
 * user switcher. No fallbacks to body / query / default — handlers MUST return
 * `userIdRequired(c)` (400) when `activeUserId` returns null. Real auth (MT-1)
 * replaces this with verified token claims.
 *
 * Role gating (FP-Stefan): admin endpoints like POST /api/users and the LLM
 * key configuration UI use `requireRole(c, 'manager' | 'root')`. Roles are
 * looked up from memory (identity_schema.users.role) on every call — small,
 * synchronous, and acceptable for the single-tenant demo. NOTE: this is the
 * same honor-system identity model as `activeUserId`; a malicious caller can
 * still set X-Active-User to any UUID. Real auth replaces this entirely.
 */

import { listUsersWithMeta } from '@cerberos/io-core/memory-client'

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

export type UserRole = 'root' | 'manager' | 'user'

export function activeUserId(c: { req: { header: (name: string) => string | undefined } }): string | null {
  const v = c.req.header('X-Active-User')?.trim().toLowerCase()
  if (!v || !UUID_RE.test(v)) return null
  return v
}

export function userIdRequired(c: { json: (obj: unknown, status: 400) => Response }): Response {
  return c.json({ error: 'X-Active-User header is required (UUID)' }, 400)
}

/**
 * MT-2: defense-in-depth against userId-override on write endpoints.
 *
 * Returns a 403 Response when the request carries a `userId` in the JSON body
 * or query string that does not match `activeId` (the X-Active-User reader's
 * result). A matching value is silently accepted for backward compatibility
 * with older clients that still echo their own id back.
 *
 * Handlers MUST short-circuit on a non-null return:
 *   const denied = assertNoUserIdOverride(c, body, userId)
 *   if (denied) return denied
 *
 * The silent-drop behavior left by MT-D2 (#193) is safe but unobservable;
 * this turns "your override was ignored" into a hard, auditable boundary.
 */
export function assertNoUserIdOverride(
  c: {
    req: { query: (name: string) => string | undefined }
    json: (obj: unknown, status: 403) => Response
  },
  body: unknown,
  activeId: string,
): Response | null {
  const active = activeId.toLowerCase()
  const fromBody =
    body && typeof body === 'object' && 'userId' in body && typeof (body as { userId?: unknown }).userId === 'string'
      ? ((body as { userId: string }).userId).trim().toLowerCase()
      : null
  const fromQuery = c.req.query('userId')?.trim().toLowerCase() || null
  for (const candidate of [fromBody, fromQuery]) {
    if (candidate && candidate !== active) {
      return c.json(
        { error: 'userId override does not match X-Active-User' },
        403,
      )
    }
  }
  return null
}

const ROLE_RANK: Record<UserRole, number> = { user: 0, manager: 1, root: 2 }

export async function getActiveRole(userId: string): Promise<UserRole | null> {
  const { users } = await listUsersWithMeta()
  const u = users.find((x) => x.id === userId)
  return (u?.role as UserRole | undefined) ?? null
}

/**
 * Resolve the active user, fetch their role, and require it to be at least
 * `minRole`. Returns `{ ok: true, userId, role }` when allowed, otherwise
 * a Response that the handler should return directly.
 */
export async function requireRole(
  c: {
    req: { header: (name: string) => string | undefined }
    json: (obj: unknown, status: number) => Response
  },
  minRole: UserRole,
): Promise<{ ok: true; userId: string; role: UserRole } | Response> {
  const userId = activeUserId(c)
  if (!userId) return userIdRequired(c as Parameters<typeof userIdRequired>[0])
  const role = await getActiveRole(userId)
  if (!role) {
    return c.json({ error: 'unknown user' }, 401)
  }
  if (ROLE_RANK[role] < ROLE_RANK[minRole]) {
    return c.json({ error: `forbidden: requires role >= ${minRole}` }, 403)
  }
  return { ok: true, userId, role }
}
