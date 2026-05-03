import './SettingsPanel.css'

export interface UISettings {
  demoMode: boolean
  showStreamingProgress: boolean
  highlightAwaitingFeedback: boolean
  showHeartbeatSeconds: boolean
  fontSizeScale: 'normal' | 'large'
  highContrast: boolean
  showActivityLog: boolean
}

export const defaultUISettings: UISettings = {
  demoMode: true,
  showStreamingProgress: true,
  highlightAwaitingFeedback: true,
  showHeartbeatSeconds: true,
  fontSizeScale: 'normal',
  highContrast: false,
  showActivityLog: false,
}

interface SettingsPanelProps {
  settings: UISettings
  onSettingsChange: (settings: UISettings) => void
  onClose: () => void
}

function SettingsPanel({ settings, onSettingsChange, onClose }: SettingsPanelProps) {
  const updateSetting = <K extends keyof UISettings>(key: K, value: UISettings[K]) => {
    onSettingsChange({ ...settings, [key]: value })
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
