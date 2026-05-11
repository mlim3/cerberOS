import { useEffect, useState } from 'react'
import { buildApiUrl } from '../api/orchestrator'
import { getActiveUserId, setActiveUserId } from '../lib/active-user'
import './UserSwitcher.css'

interface UserSummary {
  id: string
  email: string
  role?: 'root' | 'manager' | 'user'
}

/**
 * Demo-mode "Acting as: [user]" switcher. Selecting a different user writes
 * the choice to localStorage and reloads the page so all state — SSE streams,
 * cached conversations, in-flight requests — resets cleanly under the new
 * identity. Not authentication; see plans/multitenancy.md Appendix A.
 */
function UserSwitcher() {
  const [users, setUsers] = useState<UserSummary[]>([])
  const [reloadTick, setReloadTick] = useState(0)
  const current = getActiveUserId()

  useEffect(() => {
    let cancelled = false
    fetch(buildApiUrl('/api/users'))
      .then(r => (r.ok ? r.json() : { users: [] }))
      .then((data: { users?: UserSummary[] }) => {
        if (!cancelled) setUsers(data.users ?? [])
      })
      .catch(() => {
        if (!cancelled) setUsers([])
      })
    return () => { cancelled = true }
  }, [reloadTick])

  // Listen for cross-component "users changed" events dispatched by
  // AdminPanel after a successful Create user. This avoids requiring a
  // full page reload to pick up the new entry in the switcher dropdown.
  useEffect(() => {
    function onUsersChanged() { setReloadTick((t) => t + 1) }
    window.addEventListener('cerberos:users-changed', onUsersChanged)
    return () => window.removeEventListener('cerberos:users-changed', onUsersChanged)
  }, [])

  function onChange(e: React.ChangeEvent<HTMLSelectElement>): void {
    const next = e.target.value
    if (next && next !== current) {
      setActiveUserId(next)
      window.location.reload()
    }
  }

  // If the roster fails to load, render a stub showing the current uuid so the
  // switcher is still visibly present (and debuggable) rather than silently absent.
  const options = users.length > 0
    ? users
    : [{ id: current, email: `${current.slice(0, 8)}…` }]

  function roleBadge(role?: 'root' | 'manager' | 'user'): string {
    if (role === 'root') return ' (root)'
    if (role === 'manager') return ' (mgr)'
    return ''
  }

  return (
    <div className="user-switcher-control" title="Acting as (demo only — not authentication)">
      <span className="user-switcher-label">Acting as</span>
      <select
        className="user-switcher"
        value={current}
        onChange={onChange}
        aria-label="Active user"
      >
        {options.map(u => (
          <option key={u.id} value={u.id}>
            {u.email}{roleBadge((u as UserSummary).role)}
          </option>
        ))}
      </select>
    </div>
  )
}

export default UserSwitcher
