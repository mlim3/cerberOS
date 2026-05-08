import './ProgressIndicator.css'

interface ProgressIndicatorProps {
  isActive: boolean
  statusText?: string
}

export default function ProgressIndicator({ isActive, statusText }: ProgressIndicatorProps) {
  if (!isActive) return null

  return (
    <div className="progress-indicator">
      <span className="progress-dot"></span>
      <span className="progress-text">{statusText || 'Thinking...'}</span>
    </div>
  )
}
