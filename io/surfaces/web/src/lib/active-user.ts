/**
 * Demo-mode active-user resolution. Read at module init time and on every
 * dropdown switch (which triggers `window.location.reload()`), so consumers
 * can treat the result as a stable constant within a single page lifetime.
 *
 * Resolution order:
 *   1. localStorage["cerberos-active-user"]  (set by UserSwitcher)
 *   2. VITE_IO_USER_ID env var                (build-time override)
 *   3. hardcoded default UUID                 (matches identity_schema seed)
 *
 * NOT auth — anyone can edit localStorage. Real auth (MT-1) replaces this.
 */

const STORAGE_KEY = 'cerberos-active-user'
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

function isValidUuid(v: string | null | undefined): v is string {
  return typeof v === 'string' && UUID_RE.test(v)
}

export function getActiveUserId(): string {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (isValidUuid(stored)) return stored.toLowerCase()
  } catch {
    // SSR / privacy mode — fall through to env default
  }
  const fromEnv = import.meta.env.VITE_IO_USER_ID as string | undefined
  if (isValidUuid(fromEnv)) return fromEnv.toLowerCase()
  return '00000000-0000-0000-0000-000000000001'
}

export function setActiveUserId(userId: string): void {
  if (!isValidUuid(userId)) return
  try {
    localStorage.setItem(STORAGE_KEY, userId.toLowerCase())
  } catch {
    // ignore storage errors
  }
}
