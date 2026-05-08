import { useState } from 'react'
import { buildApiUrl } from '../api/orchestrator'
import { setActiveUserId } from '../lib/active-user'
import './FirstRunSetup.css'

/**
 * One-screen "Create your account" flow shown when no root user exists yet.
 *
 * Promotes the seed `dev-default@example.com` row by reusing its UUID with
 * a fresh email + role='root', so existing per-user rows (chat, vault) stay
 * coherent even though the email changes. After success the new identity is
 * written to localStorage and the page reloads.
 */
const SEED_USER_ID = '00000000-0000-0000-0000-000000000001'

interface FirstRunSetupProps {
  onComplete: () => void
}

function FirstRunSetup({ onComplete }: FirstRunSetupProps): React.ReactElement {
  const [email, setEmail] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(e: React.FormEvent): Promise<void> {
    e.preventDefault()
    const trimmed = email.trim().toLowerCase()
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(trimmed)) {
      setError('Enter a valid email address.')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const res = await fetch(buildApiUrl('/api/users'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          email: trimmed,
          role: 'root',
          id: SEED_USER_ID,
        }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({})) as { error?: string }
        throw new Error(data.error ?? `signup failed (${res.status})`)
      }
      const json = await res.json() as { user?: { id: string; email: string; role?: string } }
      const newId = json.user?.id ?? SEED_USER_ID
      setActiveUserId(newId)
      onComplete()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSubmitting(false)
    }
  }

  return (
    <div className="first-run-overlay">
      <div className="first-run-card">
        <h1 className="first-run-title">Welcome to cerberOS</h1>
        <p className="first-run-subtitle">
          No root user exists yet. Create the first account — it becomes the
          system root and can manage everyone else.
        </p>
        <form onSubmit={handleSubmit} className="first-run-form">
          <label className="first-run-label" htmlFor="first-run-email">
            Email
          </label>
          <input
            id="first-run-email"
            className="first-run-input"
            type="email"
            placeholder="you@example.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoFocus
            required
            disabled={submitting}
          />
          {error && <div className="first-run-error">{error}</div>}
          <button
            type="submit"
            className="first-run-submit"
            disabled={submitting || !email.trim()}
          >
            {submitting ? 'Creating…' : 'Create root account'}
          </button>
        </form>
        <p className="first-run-fineprint">
          Demo mode: this is honor-system identity, not authentication. The
          account name shows up in the user switcher; everyone using this
          installation can pick it.
        </p>
      </div>
    </div>
  )
}

export default FirstRunSetup
