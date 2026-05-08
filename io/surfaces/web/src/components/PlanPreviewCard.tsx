import { useId, useState } from 'react'
import type {
  PlanPreview,
  PlanPreviewSubtask,
  PlanDecisionStatus,
} from '@cerberos/io-core'
import './PlanPreviewCard.css'

export interface PlanPreviewCardProps {
  preview: PlanPreview
  status: PlanDecisionStatus
  error?: string
  onApprove: () => void
  onReject: (reason?: string) => void
}

/**
 * Inline card rendered in the task detail pane when the orchestrator is
 * waiting on the user's approval of a multi-step plan. Surfaces the subtask
 * graph (action, dependencies, required skill domains) and exposes an
 * Approve / Reject control.
 *
 * The card is intentionally lightweight — it does not own any state beyond
 * the handlers passed in from App. Parent App coordinates the POST to the
 * IO API (`submitPlanDecision`) and updates the `status` field so the
 * card can disable itself while the decision is in flight.
 */
export default function PlanPreviewCard(props: PlanPreviewCardProps) {
  const { preview, status, error, onApprove, onReject } = props
  const disabled =
    status === 'submitting' || status === 'approved' || status === 'rejected'
  const [expanded, setExpanded] = useState(false)
  const subtasksListId = useId()

  return (
    <section
      className={`plan-preview-card${expanded ? ' plan-preview-card--expanded' : ' plan-preview-card--collapsed'}`}
      aria-label="Plan awaiting approval"
    >
      <header className="plan-preview-header">
        <button
          type="button"
          className="plan-preview-toggle"
          aria-expanded={expanded}
          aria-controls={subtasksListId}
          onClick={() => setExpanded(e => !e)}
        >
          <span className="plan-preview-chevron" aria-hidden />
          <span className="plan-preview-toggle-text">
            <span className="plan-preview-title">Plan awaiting your approval</span>
            <span className="plan-preview-subtitle">
              {preview.subtasks.length} step{preview.subtasks.length === 1 ? '' : 's'}
              {preview.expiresInSeconds > 0 && (
                <>
                  {' • '}expires in ~{Math.max(1, Math.round(preview.expiresInSeconds / 60))} min
                </>
              )}
            </span>
          </span>
        </button>
        <span className={`plan-preview-status plan-preview-status-${status}`}>
          {status === 'pending' && 'Awaiting decision'}
          {status === 'submitting' && 'Submitting…'}
          {status === 'approved' && 'Approved'}
          {status === 'rejected' && 'Rejected'}
          {status === 'error' && 'Error'}
        </span>
      </header>

      {expanded && (
        <ol id={subtasksListId} className="plan-preview-subtasks">
          {preview.subtasks.map((s: PlanPreviewSubtask, i: number) => (
            <li key={s.subtaskId} className="plan-preview-subtask">
              <div className="plan-preview-subtask-head">
                <span className="plan-preview-subtask-idx">{i + 1}</span>
                <span className="plan-preview-subtask-action">{s.action || s.subtaskId}</span>
                {s.domains.length > 0 && (
                  <span className="plan-preview-subtask-domains">
                    {s.domains.join(', ')}
                  </span>
                )}
              </div>
              {s.instructions && (
                <p className="plan-preview-subtask-instructions">{s.instructions}</p>
              )}
              {s.dependsOn.length > 0 && (
                <p className="plan-preview-subtask-deps">
                  depends on: {s.dependsOn.join(', ')}
                </p>
              )}
            </li>
          ))}
        </ol>
      )}

      {error && <p className="plan-preview-error">{error}</p>}

      <footer className="plan-preview-actions">
        <button
          type="button"
          className="plan-preview-approve"
          disabled={disabled}
          onClick={onApprove}
        >
          Approve and run
        </button>
        <button
          type="button"
          className="plan-preview-reject"
          disabled={disabled}
          onClick={() => onReject(undefined)}
        >
          Reject
        </button>
      </footer>
    </section>
  )
}
