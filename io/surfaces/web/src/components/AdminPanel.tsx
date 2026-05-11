import { useEffect, useState } from 'react'
import { buildApiUrl } from '../api/orchestrator'
import { getActiveUserId } from '../lib/active-user'
import './AdminPanel.css'

/**
 * Admin panel for root/manager users. Surfaces:
 *  - LLM API key configuration (Anthropic / OpenAI)
 *  - User creation (email + role)
 *  - User listing with role badges
 *  - Skill installation (GitHub URL + scope)
 *  - "Install Superpowers for all users" one-click
 *
 * All actions hit IO API endpoints that role-check via `requireRole`. The
 * panel is rendered as an overlay alongside SettingsPanel.
 */

type Role = 'root' | 'manager' | 'user'

interface UserListing {
  id: string
  email: string
  role?: Role
}

interface AdminPanelProps {
  onClose: () => void
}

function AdminPanel({ onClose }: AdminPanelProps): React.ReactElement {
  const userId = getActiveUserId()
  const [users, setUsers] = useState<UserListing[]>([])
  const [refreshKey, setRefreshKey] = useState(0)

  // LLM key state
  const [llmProvider, setLlmProvider] = useState<'anthropic' | 'openai'>('anthropic')
  const [llmKey, setLlmKey] = useState('')
  const [llmStatus, setLlmStatus] = useState<{ kind: 'idle' | 'ok' | 'error'; msg?: string }>({ kind: 'idle' })

  // Gmail demo account state (App Password — no OAuth)
  const [gmailEmail, setGmailEmail] = useState('')
  const [gmailAppPassword, setGmailAppPassword] = useState('')
  // calendar_ical_url is the per-calendar "Secret address in iCal format"
  // (Calendar Settings → "Settings for my calendars" → calendar name →
  // "Integrate calendar"). It's itself a bearer secret and unlocks the
  // calendar_list_upcoming skill without OAuth.
  const [gmailCalendarICalURL, setGmailCalendarICalURL] = useState('')
  const [gmailConfigured, setGmailConfigured] = useState<{
    email: string | null
    calendarICalURLConfigured: boolean
  }>({ email: null, calendarICalURLConfigured: false })
  const [gmailStatus, setGmailStatus] = useState<{ kind: 'idle' | 'ok' | 'pending' | 'error'; msg?: string }>({ kind: 'idle' })

  // New user state
  const [newUserEmail, setNewUserEmail] = useState('')
  const [newUserRole, setNewUserRole] = useState<Role>('user')
  const [newUserStatus, setNewUserStatus] = useState<{ kind: 'idle' | 'ok' | 'error'; msg?: string }>({ kind: 'idle' })

  // Skill import state
  const [skillRepo, setSkillRepo] = useState('')
  const [skillScopeAll, setSkillScopeAll] = useState(false)
  const [skillStatus, setSkillStatus] = useState<{ kind: 'idle' | 'ok' | 'pending' | 'error'; msg?: string }>({ kind: 'idle' })

  // Skill creation state
  const [skillDesc, setSkillDesc] = useState('')
  const [skillCreateStatus, setSkillCreateStatus] = useState<{ kind: 'idle' | 'ok' | 'pending' | 'error'; msg?: string }>({ kind: 'idle' })

  useEffect(() => {
    let cancelled = false
    fetch(buildApiUrl('/api/users'))
      .then((r) => (r.ok ? r.json() : { users: [] }))
      .then((data: { users?: UserListing[] }) => {
        if (!cancelled) setUsers(data.users ?? [])
      })
      .catch(() => {
        if (!cancelled) setUsers([])
      })
    return () => {
      cancelled = true
    }
  }, [refreshKey])

  useEffect(() => {
    let cancelled = false
    fetch(buildApiUrl('/api/admin/gmail-credentials'), {
      headers: { 'X-Active-User': userId },
    })
      .then((r) => (r.ok ? r.json() : { configured: false, email: null, calendar_ical_url_configured: false }))
      .then((data: { configured?: boolean; email?: string | null; calendar_ical_url_configured?: boolean }) => {
        if (!cancelled) {
          setGmailConfigured({
            email: data.email ?? null,
            calendarICalURLConfigured: !!data.calendar_ical_url_configured,
          })
        }
      })
      .catch(() => {
        if (!cancelled) setGmailConfigured({ email: null, calendarICalURLConfigured: false })
      })
    return () => {
      cancelled = true
    }
  }, [userId, refreshKey])

  async function handleSetLlmKey(e: React.FormEvent): Promise<void> {
    e.preventDefault()
    if (!llmKey.trim()) {
      setLlmStatus({ kind: 'error', msg: 'key is required' })
      return
    }
    setLlmStatus({ kind: 'idle' })
    try {
      const res = await fetch(buildApiUrl('/api/admin/llm-keys'), {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Active-User': userId,
        },
        body: JSON.stringify({ provider: llmProvider, key: llmKey }),
      })
      const data = await res.json().catch(() => ({})) as { error?: string; requires_restart?: boolean }
      if (!res.ok) {
        setLlmStatus({ kind: 'error', msg: data.error ?? `failed (${res.status})` })
        return
      }
      setLlmKey('')
      setLlmStatus({
        kind: 'ok',
        msg: data.requires_restart
          ? 'Saved. Restart aegis-agents for the new key to take effect.'
          : 'Saved.',
      })
    } catch (err) {
      setLlmStatus({ kind: 'error', msg: err instanceof Error ? err.message : String(err) })
    }
  }

  async function handleSaveGmail(e: React.FormEvent): Promise<void> {
    e.preventDefault()
    const cleaned = gmailAppPassword.replace(/\s+/g, '')
    if (!gmailEmail.includes('@')) {
      setGmailStatus({ kind: 'error', msg: 'enter a valid email address' })
      return
    }
    if (cleaned.length !== 16) {
      setGmailStatus({
        kind: 'error',
        msg: 'app password must be 16 characters (Google App Password format)',
      })
      return
    }
    const trimmedICal = gmailCalendarICalURL.trim()
    if (trimmedICal && !/^https?:\/\//i.test(trimmedICal)) {
      setGmailStatus({ kind: 'error', msg: 'calendar iCal URL must start with http(s)://' })
      return
    }
    setGmailStatus({ kind: 'pending', msg: 'Saving…' })
    try {
      const res = await fetch(buildApiUrl('/api/admin/gmail-credentials'), {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Active-User': userId,
        },
        body: JSON.stringify({
          email: gmailEmail.trim(),
          app_password: cleaned,
          ...(trimmedICal ? { calendar_ical_url: trimmedICal } : {}),
        }),
      })
      const data = await res.json().catch(() => ({})) as {
        error?: string
        email?: string
        calendar_ical_url_configured?: boolean
      }
      if (!res.ok) {
        setGmailStatus({ kind: 'error', msg: data.error ?? `failed (${res.status})` })
        return
      }
      setGmailAppPassword('')
      setGmailCalendarICalURL('')
      setGmailConfigured({
        email: data.email ?? gmailEmail.trim(),
        calendarICalURLConfigured: !!data.calendar_ical_url_configured || !!trimmedICal,
      })
      setGmailStatus({
        kind: 'ok',
        msg: trimmedICal
          ? 'Saved. Agents can call gmail_send, calendar_create_event, and calendar_list_upcoming now (no restart needed).'
          : 'Saved. Agents can call gmail_send / calendar_create_event now (no restart needed). Paste a calendar iCal URL to enable read access.',
      })
    } catch (err) {
      setGmailStatus({ kind: 'error', msg: err instanceof Error ? err.message : String(err) })
    }
  }

  async function handleCreateUser(e: React.FormEvent): Promise<void> {
    e.preventDefault()
    setNewUserStatus({ kind: 'idle' })
    try {
      const res = await fetch(buildApiUrl('/api/users'), {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Active-User': userId,
        },
        body: JSON.stringify({ email: newUserEmail.trim(), role: newUserRole }),
      })
      const data = await res.json().catch(() => ({})) as { error?: string; user?: UserListing }
      if (!res.ok) {
        setNewUserStatus({ kind: 'error', msg: data.error ?? `failed (${res.status})` })
        return
      }
      setNewUserStatus({ kind: 'ok', msg: `created ${data.user?.email}` })
      setNewUserEmail('')
      setNewUserRole('user')
      setRefreshKey((k) => k + 1)
    } catch (err) {
      setNewUserStatus({ kind: 'error', msg: err instanceof Error ? err.message : String(err) })
    }
  }

  async function handleImportSkill(e: React.FormEvent): Promise<void> {
    e.preventDefault()
    if (!skillRepo.trim()) return
    setSkillStatus({ kind: 'pending', msg: 'Importing…' })
    try {
      const res = await fetch(buildApiUrl('/api/skills/import-github'), {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Active-User': userId,
        },
        body: JSON.stringify({ repo: skillRepo.trim(), scope: skillScopeAll ? 'all' : 'me' }),
      })
      const data = await res.json().catch(() => ({})) as { error?: string; skill?: { name: string }; skills?: Array<{ name: string }> }
      if (!res.ok) {
        setSkillStatus({ kind: 'error', msg: data.error ?? `failed (${res.status})` })
        return
      }
      const names = data.skills?.map((s) => s.name).join(', ') ?? data.skill?.name ?? '(unnamed)'
      setSkillStatus({ kind: 'ok', msg: `imported: ${names}` })
      setSkillRepo('')
    } catch (err) {
      setSkillStatus({ kind: 'error', msg: err instanceof Error ? err.message : String(err) })
    }
  }

  async function handleCreateSkill(e: React.FormEvent): Promise<void> {
    e.preventDefault()
    if (!skillDesc.trim()) return
    setSkillCreateStatus({ kind: 'pending', msg: 'Creating…' })
    try {
      const res = await fetch(buildApiUrl('/api/skills/create'), {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Active-User': userId,
        },
        body: JSON.stringify({ description: skillDesc.trim() }),
      })
      const data = await res.json().catch(() => ({})) as { error?: string; skill?: { name: string } }
      if (!res.ok) {
        setSkillCreateStatus({ kind: 'error', msg: data.error ?? `failed (${res.status})` })
        return
      }
      setSkillCreateStatus({ kind: 'ok', msg: `created skill: ${data.skill?.name ?? '(unnamed)'}` })
      setSkillDesc('')
    } catch (err) {
      setSkillCreateStatus({ kind: 'error', msg: err instanceof Error ? err.message : String(err) })
    }
  }

  async function handleInstallSuperpowers(): Promise<void> {
    setSkillStatus({ kind: 'pending', msg: 'Installing Superpowers for all users…' })
    try {
      const res = await fetch(buildApiUrl('/api/skills/import-github'), {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Active-User': userId,
        },
        body: JSON.stringify({ repo: 'github.com/obra/superpowers', scope: 'all' }),
      })
      const data = await res.json().catch(() => ({})) as { error?: string; skills?: Array<{ name: string }> }
      if (!res.ok) {
        setSkillStatus({ kind: 'error', msg: data.error ?? `failed (${res.status})` })
        return
      }
      const count = data.skills?.length ?? 0
      setSkillStatus({ kind: 'ok', msg: `Superpowers installed (${count} skills, all users)` })
    } catch (err) {
      setSkillStatus({ kind: 'error', msg: err instanceof Error ? err.message : String(err) })
    }
  }

  return (
    <div className="admin-panel-overlay" onClick={onClose}>
      <div className="admin-panel-container" onClick={(e) => e.stopPropagation()}>
        <div className="admin-panel-header">
          <h2>Admin</h2>
          <button className="admin-close-btn" onClick={onClose} aria-label="Close admin">
            ×
          </button>
        </div>

        <div className="admin-sections">
          <section className="admin-section">
            <h3>LLM Provider Key</h3>
            <p className="admin-help">
              Stored in OpenBao via the vault engine. After saving, restart
              <code> aegis-agents</code> to pick up the new key.
            </p>
            <form onSubmit={handleSetLlmKey} className="admin-form">
              <div className="admin-row">
                <label>Provider</label>
                <select
                  value={llmProvider}
                  onChange={(e) => setLlmProvider(e.target.value as 'anthropic' | 'openai')}
                >
                  <option value="anthropic">Anthropic</option>
                  <option value="openai">OpenAI</option>
                </select>
              </div>
              <div className="admin-row">
                <label>Key</label>
                <input
                  type="password"
                  placeholder="sk-..."
                  value={llmKey}
                  onChange={(e) => setLlmKey(e.target.value)}
                  autoComplete="off"
                />
              </div>
              <button type="submit" className="admin-submit">Save key</button>
              {llmStatus.kind === 'ok' && <div className="admin-status-ok">{llmStatus.msg}</div>}
              {llmStatus.kind === 'error' && <div className="admin-status-error">{llmStatus.msg}</div>}
            </form>
          </section>

          <section className="admin-section">
            <h3>Gmail Demo Account</h3>
            <p className="admin-help">
              Powers <code>gmail_send</code>, <code>calendar_create_event</code>, and (when an
              iCal URL is supplied) <code>calendar_list_upcoming</code> — all via SMTP / HTTPS,
              no OAuth, no Google Cloud Console project. Generate a 16-character App Password
              at{' '}
              <a
                href="https://myaccount.google.com/apppasswords"
                target="_blank"
                rel="noopener noreferrer"
              >
                myaccount.google.com/apppasswords
              </a>{' '}
              (requires 2-Step Verification on the Google account). For read access to the
              calendar, copy the "Secret address in iCal format" from{' '}
              <a
                href="https://calendar.google.com/calendar/u/0/r/settings"
                target="_blank"
                rel="noopener noreferrer"
              >
                Calendar settings → Settings for my calendars → [calendar] → Integrate calendar
              </a>
              .
            </p>
            {gmailConfigured.email && (
              <div className="admin-status-ok admin-gmail-current">
                Currently configured: <code>{gmailConfigured.email}</code>
                {gmailConfigured.calendarICalURLConfigured && (
                  <span> · calendar read enabled</span>
                )}
              </div>
            )}
            <form onSubmit={handleSaveGmail} className="admin-form">
              <div className="admin-row">
                <label>Gmail address</label>
                <input
                  type="email"
                  placeholder="cerberos-demo@gmail.com"
                  value={gmailEmail}
                  onChange={(e) => setGmailEmail(e.target.value)}
                  autoComplete="off"
                />
              </div>
              <div className="admin-row">
                <label>App password</label>
                <input
                  type="password"
                  placeholder="xxxx xxxx xxxx xxxx (spaces ok)"
                  value={gmailAppPassword}
                  onChange={(e) => setGmailAppPassword(e.target.value)}
                  autoComplete="off"
                />
              </div>
              <div className="admin-row">
                <label>Calendar iCal URL (optional)</label>
                <input
                  type="password"
                  placeholder="https://calendar.google.com/calendar/ical/.../basic.ics"
                  value={gmailCalendarICalURL}
                  onChange={(e) => setGmailCalendarICalURL(e.target.value)}
                  autoComplete="off"
                />
              </div>
              <button type="submit" className="admin-submit">Save Gmail account</button>
              {gmailStatus.kind === 'pending' && <div className="admin-status-pending">{gmailStatus.msg}</div>}
              {gmailStatus.kind === 'ok' && <div className="admin-status-ok">{gmailStatus.msg}</div>}
              {gmailStatus.kind === 'error' && <div className="admin-status-error">{gmailStatus.msg}</div>}
            </form>
          </section>

          <section className="admin-section">
            <h3>Users</h3>
            <ul className="admin-user-list">
              {users.map((u) => (
                <li key={u.id}>
                  <span className={`admin-role-badge admin-role-${u.role ?? 'user'}`}>
                    {u.role ?? 'user'}
                  </span>
                  <span className="admin-user-email">{u.email}</span>
                </li>
              ))}
            </ul>
            <form onSubmit={handleCreateUser} className="admin-form">
              <div className="admin-row">
                <label>Email</label>
                <input
                  type="email"
                  placeholder="alice@example.com"
                  value={newUserEmail}
                  onChange={(e) => setNewUserEmail(e.target.value)}
                  required
                />
              </div>
              <div className="admin-row">
                <label>Role</label>
                <select
                  value={newUserRole}
                  onChange={(e) => setNewUserRole(e.target.value as Role)}
                >
                  <option value="user">user</option>
                  <option value="manager">manager</option>
                </select>
              </div>
              <button type="submit" className="admin-submit">Create user</button>
              {newUserStatus.kind === 'ok' && <div className="admin-status-ok">{newUserStatus.msg}</div>}
              {newUserStatus.kind === 'error' && <div className="admin-status-error">{newUserStatus.msg}</div>}
            </form>
          </section>

          <section className="admin-section">
            <h3>Skills</h3>
            <form onSubmit={handleImportSkill} className="admin-form">
              <div className="admin-row">
                <label>GitHub repo</label>
                <input
                  type="text"
                  placeholder="github.com/user/repo"
                  value={skillRepo}
                  onChange={(e) => setSkillRepo(e.target.value)}
                />
              </div>
              <label className="admin-checkbox">
                <input
                  type="checkbox"
                  checked={skillScopeAll}
                  onChange={(e) => setSkillScopeAll(e.target.checked)}
                />
                <span>Make available to all users</span>
              </label>
              <div className="admin-button-row">
                <button type="submit" className="admin-submit">Import skill</button>
                <button type="button" className="admin-secondary" onClick={handleInstallSuperpowers}>
                  Install Superpowers (all users)
                </button>
              </div>
              {skillStatus.kind === 'pending' && <div className="admin-status-pending">{skillStatus.msg}</div>}
              {skillStatus.kind === 'ok' && <div className="admin-status-ok">{skillStatus.msg}</div>}
              {skillStatus.kind === 'error' && <div className="admin-status-error">{skillStatus.msg}</div>}
            </form>

            <form onSubmit={handleCreateSkill} className="admin-form admin-form-create-skill">
              <div className="admin-row">
                <label>Create from description</label>
                <input
                  type="text"
                  placeholder="e.g. emails my team every Monday"
                  value={skillDesc}
                  onChange={(e) => setSkillDesc(e.target.value)}
                />
              </div>
              <button type="submit" className="admin-submit">Create skill</button>
              {skillCreateStatus.kind === 'pending' && <div className="admin-status-pending">{skillCreateStatus.msg}</div>}
              {skillCreateStatus.kind === 'ok' && <div className="admin-status-ok">{skillCreateStatus.msg}</div>}
              {skillCreateStatus.kind === 'error' && <div className="admin-status-error">{skillCreateStatus.msg}</div>}
            </form>
          </section>
        </div>
      </div>
    </div>
  )
}

export default AdminPanel
