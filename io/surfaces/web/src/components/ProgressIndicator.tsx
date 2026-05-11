import './ProgressIndicator.css'

interface ProgressIndicatorProps {
  isActive: boolean
  statusText?: string
  /** e.g. `progress-indicator--thinking` while streaming, `progress-indicator--working` otherwise. */
  className?: string
}

export default function ProgressIndicator({ isActive, statusText, className }: ProgressIndicatorProps) {
  if (!isActive) return null

  return (
    <div className={['progress-indicator', className].filter(Boolean).join(' ')}>
      <span className="progress-dot"></span>
      <span className="progress-text">{statusText || 'Thinking...'}</span>
    </div>
  )
}
