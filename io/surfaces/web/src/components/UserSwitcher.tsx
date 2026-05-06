import { useEffect, useState } from 'react'
import { buildApiUrl } from '../api/orchestrator'
import { getActiveUserId, setActiveUserId } from '../lib/active-user'
import './UserSwitcher.css'

interface UserSummary {
  id: string
  email: string
}

/**
 * Demo-mode "Acting as: [user]" switcher. Selecting a different user writes
 * the choice to localStorage and reloads the page so all state — SSE streams,
 * cached conversations, in-flight requests — resets cleanly under the new
 * identity. Not authentication; see plans/multitenancy.md Appendix A.
 */
function UserSwitcher() {
  const [users, setUsers] = useState<UserSummary[]>([])
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

  return (
    <select
      className="user-switcher"
      value={current}
      onChange={onChange}
      title="Acting as (demo only — not authentication)"
      aria-label="Active user"
    >
      {options.map(u => (
        <option key={u.id} value={u.id}>
          {u.email}
        </option>
      ))}
    </select>
  )
}

export default UserSwitcher
