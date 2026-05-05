import { useEffect, useRef } from 'react'
import type { SkillActivity } from '@cerberos/io-core'
import './SkillActivityToast.css'

// How long each toast is visible before fading out (ms).
const TOAST_DURATION_MS = 4000

// Domain icons shown next to the skill name.
const DOMAIN_ICONS: Record<string, string> = {
  web: '🌐',
  logs: '📋',
  storage: '🗄️',
  data: '📊',
  comms: '📡',
}

function domainIcon(domain: string): string {
  return DOMAIN_ICONS[domain] ?? '⚙️'
}

/** Human-readable label for a command name, e.g. "web.search" → "Web Search". */
function commandLabel(command: string): string {
  const parts = command.split('.')
  return parts
    .map(p => p.charAt(0).toUpperCase() + p.slice(1))
    .join(' ')
}

export interface SkillToastItem {
  id: string
  activity: SkillActivity
  /** Epoch ms when this toast was created — drives the auto-dismiss timer. */
  createdAt: number
}

interface SkillActivityToastProps {
  toasts: SkillToastItem[]
  onDismiss: (id: string) => void
}

/**
 * SkillActivityToast renders a stack of transient notification chips at the
 * bottom-right of the viewport. Each chip auto-dismisses after TOAST_DURATION_MS.
 *
 * Parent is responsible for managing the `toasts` list. Toasts are added in
 * App.tsx when a `skill_activity` SSE event arrives from the orchestrator.
 */
function SkillActivityToast({ toasts, onDismiss }: SkillActivityToastProps) {
  // Per-toast dismiss timers. We use a ref map so timers survive re-renders
  // without stale-closure issues.
  const timers = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map())

  useEffect(() => {
    for (const toast of toasts) {
      if (timers.current.has(toast.id)) continue
      const remaining = TOAST_DURATION_MS - (Date.now() - toast.createdAt)
      const delay = Math.max(0, remaining)
      const t = setTimeout(() => {
        onDismiss(toast.id)
        timers.current.delete(toast.id)
      }, delay)
      timers.current.set(toast.id, t)
    }
  }, [toasts, onDismiss])

  // Clean up timers on unmount.
  useEffect(() => {
    return () => {
      for (const t of timers.current.values()) clearTimeout(t)
    }
  }, [])

  if (toasts.length === 0) return null

  return (
    <div className="skill-toast-stack" aria-live="polite" aria-label="Skill activity notifications">
      {toasts.map(({ id, activity }) => (
        <div
          key={id}
          className={`skill-toast skill-toast--${activity.domain}`}
          role="status"
        >
          <span className="skill-toast-icon" aria-hidden="true">
            {domainIcon(activity.domain)}
          </span>
          <div className="skill-toast-body">
            <span className="skill-toast-label">{commandLabel(activity.command)}</span>
            {activity.synthesized && activity.outcome === 'synthesized' && (
              <span className="skill-toast-badge skill-toast-badge--new" title="New skill synthesized from this session">
                new skill
              </span>
            )}
            {activity.synthesized && activity.outcome !== 'synthesized' && (
              <span className="skill-toast-badge skill-toast-badge--learned" title="Learned skill from a prior session">
                learned
              </span>
            )}
            {activity.vaultDelegated && (
              <span className="skill-toast-badge skill-toast-badge--vault" title="Credentialed operation">
                secured
              </span>
            )}
            {activity.elapsedMs > 0 && (
              <span className="skill-toast-elapsed">
                {activity.elapsedMs >= 1000
                  ? `${(activity.elapsedMs / 1000).toFixed(1)}s`
                  : `${activity.elapsedMs}ms`}
              </span>
            )}
          </div>
          <button
            className="skill-toast-dismiss"
            onClick={() => onDismiss(id)}
            aria-label="Dismiss notification"
          >
            ×
          </button>
        </div>
      ))}
    </div>
  )
}

export default SkillActivityToast
