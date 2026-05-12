import { useEffect, useState } from 'react'
import { buildApiUrl } from '../api/orchestrator'
import { getActiveUserId } from '../lib/active-user'
import './SettingsPanel.css'

export interface UISettings {
  demoMode: boolean
  showStreamingProgress: boolean
  highlightAwaitingFeedback: boolean
  showHeartbeatSeconds: boolean
  fontSizeScale: 'normal' | 'large'
  highContrast: boolean
  showActivityLog: boolean
  showSkillToasts: boolean
}

export const defaultUISettings: UISettings = {
  demoMode: true,
  showStreamingProgress: true,
  highlightAwaitingFeedback: true,
  showHeartbeatSeconds: true,
  fontSizeScale: 'normal',
  highContrast: false,
  showActivityLog: false,
  showSkillToasts: true,
}

interface SettingsPanelProps {
  settings: UISettings
  onSettingsChange: (settings: UISettings) => void
  onClose: () => void
}

function SettingsPanel({ settings, onSettingsChange, onClose }: SettingsPanelProps) {
  const userId = getActiveUserId()
  const updateSetting = <K extends keyof UISettings>(key: K, value: UISettings[K]) => {
    onSettingsChange({ ...settings, [key]: value })
  }

  const [googleOAuth, setGoogleOAuth] = useState<{ configured: boolean; email: string | null }>({ configured: false, email: null })
  const [googleOAuthMsg, setGoogleOAuthMsg] = useState<{ kind: 'idle' | 'pending' | 'ok' | 'error'; text?: string }>({ kind: 'idle' })

  useEffect(() => {
    if (!userId) return
    let cancelled = false
    fetch(buildApiUrl('/api/admin/google-oauth/status'), {
      headers: { 'X-Active-User': userId },
    })
      .then((r) => (r.ok ? r.json() : { configured: false, email: null }))
      .then((data: { configured?: boolean; email?: string | null }) => {
        if (!cancelled) setGoogleOAuth({ configured: !!data.configured, email: data.email ?? null })
      })
      .catch(() => { if (!cancelled) setGoogleOAuth({ configured: false, email: null }) })
    return () => { cancelled = true }
  }, [userId])

  function handleConnectGoogle(): void {
    const connectUrl = userId
      ? buildApiUrl(`/api/admin/google-oauth/connect?uid=${encodeURIComponent(userId)}`)
      : buildApiUrl('/api/admin/google-oauth/connect')
    const popup = window.open(
      connectUrl,
      'google-oauth',
      'width=600,height=700,left=200,top=100',
    )
    if (!popup) return
    const timer = setInterval(() => {
      if (popup.closed) {
        clearInterval(timer)
        // Re-fetch status after the popup closes so the UI reflects the new state.
        if (!userId) return
        fetch(buildApiUrl('/api/admin/google-oauth/status'), { headers: { 'X-Active-User': userId } })
          .then((r) => (r.ok ? r.json() : { configured: false, email: null }))
          .then((data: { configured?: boolean; email?: string | null }) => {
            setGoogleOAuth({ configured: !!data.configured, email: data.email ?? null })
          })
          .catch(() => {})
      }
    }, 500)
  }

  async function handleDisconnectGoogle(): Promise<void> {
    if (!userId) return
    setGoogleOAuthMsg({ kind: 'pending', text: 'Disconnecting…' })
    try {
      const res = await fetch(buildApiUrl('/api/admin/google-oauth'), {
        method: 'DELETE',
        headers: { 'X-Active-User': userId },
      })
      if (!res.ok) {
        const d = await res.json().catch(() => ({})) as { error?: string }
        setGoogleOAuthMsg({ kind: 'error', text: d.error ?? `failed (${res.status})` })
        return
      }
      setGoogleOAuth({ configured: false, email: null })
      setGoogleOAuthMsg({ kind: 'ok', text: 'Disconnected.' })
    } catch {
      setGoogleOAuthMsg({ kind: 'error', text: 'Network error.' })
    }
  }

  return (
    <div className="settings-panel-overlay" onClick={onClose}>
      <div className="settings-panel-container" onClick={e => e.stopPropagation()}>
        <div className="settings-panel-header">
          <h2>Settings</h2>
          <button className="settings-close-btn" onClick={onClose} aria-label="Close settings">
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <line x1="18" y1="6" x2="6" y2="18" />
              <line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>

        <div className="settings-sections">
          <section className="settings-section">
            <h3 className="settings-section-title">Demo Behavior</h3>
            <div className="settings-group">
              <label className="settings-toggle">
                <span className="toggle-info">
                  <span className="toggle-label">Demo mode: scripted conversations</span>
                  <span className="toggle-description">Show suggestion chips for quick demo responses</span>
                </span>
                <input
                  type="checkbox"
                  checked={settings.demoMode}
                  onChange={e => updateSetting('demoMode', e.target.checked)}
                />
                <span className="toggle-switch"></span>
              </label>

              <label className="settings-toggle">
                <span className="toggle-info">
                  <span className="toggle-label">Show streaming progress</span>
                  <span className="toggle-description">Display visual indicator while streaming responses</span>
                </span>
                <input
                  type="checkbox"
                  checked={settings.showStreamingProgress}
                  onChange={e => updateSetting('showStreamingProgress', e.target.checked)}
                />
                <span className="toggle-switch"></span>
              </label>
            </div>
          </section>

          <section className="settings-section">
            <h3 className="settings-section-title">Task List / Heartbeat</h3>
            <div className="settings-group">
              <label className="settings-toggle">
                <span className="toggle-info">
                  <span className="toggle-label">Highlight awaiting feedback at top</span>
                  <span className="toggle-description">Sort tasks requiring feedback first</span>
                </span>
                <input
                  type="checkbox"
                  checked={settings.highlightAwaitingFeedback}
                  onChange={e => updateSetting('highlightAwaitingFeedback', e.target.checked)}
                />
                <span className="toggle-switch"></span>
              </label>

              <label className="settings-toggle">
                <span className="toggle-info">
                  <span className="toggle-label">Show heartbeat timer</span>
                  <span className="toggle-description">Display seconds since last heartbeat for working tasks</span>
                </span>
                <input
                  type="checkbox"
                  checked={settings.showHeartbeatSeconds}
                  onChange={e => updateSetting('showHeartbeatSeconds', e.target.checked)}
                />
                <span className="toggle-switch"></span>
              </label>

              <label className="settings-toggle">
                <span className="toggle-info">
                  <span className="toggle-label">Show activity log panel</span>
                  <span className="toggle-description">Display real-time event log and heartbeats</span>
                </span>
                <input
                  type="checkbox"
                  checked={settings.showActivityLog}
                  onChange={e => updateSetting('showActivityLog', e.target.checked)}
                />
                <span className="toggle-switch"></span>
              </label>

              <label className="settings-toggle">
                <span className="toggle-info">
                  <span className="toggle-label">Skill activity notifications</span>
                  <span className="toggle-description">Show toast notifications for web searches, log queries, and other notable skill invocations</span>
                </span>
                <input
                  type="checkbox"
                  checked={settings.showSkillToasts}
                  onChange={e => updateSetting('showSkillToasts', e.target.checked)}
                />
                <span className="toggle-switch"></span>
              </label>
            </div>
          </section>

          <section className="settings-section">
            <h3 className="settings-section-title">Appearance</h3>
            <div className="settings-group">
              <div className="settings-select-row">
                <span className="select-label">Font size</span>
                <select
                  value={settings.fontSizeScale}
                  onChange={e => updateSetting('fontSizeScale', e.target.value as 'normal' | 'large')}
                  className="settings-select"
                >
                  <option value="normal">Normal</option>
                  <option value="large">Large</option>
                </select>
              </div>

              <label className="settings-toggle">
                <span className="toggle-info">
                  <span className="toggle-label">High contrast mode</span>
                  <span className="toggle-description">Increase text and border contrast</span>
                </span>
                <input
                  type="checkbox"
                  checked={settings.highContrast}
                  onChange={e => updateSetting('highContrast', e.target.checked)}
                />
                <span className="toggle-switch"></span>
              </label>
            </div>
          </section>

          <section className="settings-section">
            <h3 className="settings-section-title">Google Workspace</h3>
            <p className="settings-muted-paragraph">
              Connect your Google account to enable <strong>gmail_search</strong>,{' '}
              <strong>gmail_get_message</strong>, and <strong>calendar_list_events</strong>.
            </p>
            {googleOAuth.configured ? (
              <div className="settings-group">
                <p className="settings-muted-paragraph">
                  Connected as <strong>{googleOAuth.email}</strong>
                </p>
                <button className="settings-disconnect-btn" onClick={handleDisconnectGoogle}>
                  Disconnect Google account
                </button>
              </div>
            ) : (
              <div className="settings-group">
                <button className="settings-connect-btn" onClick={handleConnectGoogle}>
                  Connect Google account
                </button>
              </div>
            )}
            {googleOAuthMsg.kind === 'pending' && <p className="settings-muted-paragraph">{googleOAuthMsg.text}</p>}
            {googleOAuthMsg.kind === 'ok' && <p className="settings-status-ok">{googleOAuthMsg.text}</p>}
            {googleOAuthMsg.kind === 'error' && <p className="settings-status-error">{googleOAuthMsg.text}</p>}
          </section>

          <section className="settings-section">
            <h3 className="settings-section-title">Recurring tasks</h3>
            <p className="settings-muted-paragraph">
              Repeating runs are configured entirely in chat. From the sidebar <strong>Recurring</strong> tab, choose{' '}
              <strong>Create recurring task</strong> for a guided thread—or in <strong>any</strong> task chat, wording
              like &quot;every morning&quot;, &quot;daily&quot;, or &quot;every hour&quot; switches that send into scheduler
              setup (rhythm → first run → saved user cron).
            </p>
          </section>
        </div>

        <div className="settings-footer">
          <span className="settings-hint">Settings are saved automatically</span>
        </div>
      </div>
    </div>
  )
}

export default SettingsPanel
