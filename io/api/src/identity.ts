/**
 * Single source of caller identity for all IO API routes.
 *
 * Demo-mode honor system: trusts the `X-Active-User` header set by the UI's
 * user switcher. No fallbacks to body / query / default — handlers MUST return
 * `userIdRequired(c)` (400) when `activeUserId` returns null. Real auth (MT-1)
 * replaces this with verified token claims.
 */

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

export function activeUserId(c: { req: { header: (name: string) => string | undefined } }): string | null {
  const v = c.req.header('X-Active-User')?.trim().toLowerCase()
  if (!v || !UUID_RE.test(v)) return null
  return v
}

export function userIdRequired(c: { json: (obj: unknown, status: 400) => Response }): Response {
  return c.json({ error: 'X-Active-User header is required (UUID)' }, 400)
}
