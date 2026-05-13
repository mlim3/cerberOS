import { useState, useRef } from 'react'
import type { CredentialRequest, CredentialRequestStatus } from '@cerberos/io-core'
import { IconCheckCircle, IconLock } from './icons/InlineUiIcons'
import './CredentialRequestCard.css'

interface CredentialRequestCardProps {
  request: CredentialRequest
  status: CredentialRequestStatus
  onProvide: () => void
  /** When provided, renders an inline input instead of opening a modal. */
  onSubmitInline?: (requestId: string, value: string) => void | Promise<void>
}

function CredentialRequestCard({ request, status, onProvide, onSubmitInline }: CredentialRequestCardProps) {
  const [value, setValue] = useState('')
  const [reveal, setReveal] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  if (status === 'submitted') {
    return (
      <div className="cred-card cred-card-done">
        <div className="cred-card-icon cred-card-icon--svg" aria-hidden>
          <IconCheckCircle size={26} />
        </div>
        <div className="cred-card-body">
          <span className="cred-card-title">Credential provided securely</span>
          <span className="cred-card-subtitle">{request.label}</span>
        </div>
      </div>
    )
  }

  if (onSubmitInline) {
    const handleSubmit = (e: React.FormEvent) => {
      e.preventDefault()
      if (!value || status === 'submitting') return
      onSubmitInline(request.requestId, value)
    }

    return (
      <div className="cred-card cred-card-inline">
        <div className="cred-card-icon">{'\u{1F512}'}</div>
        <div className="cred-card-body cred-card-body-expanded">
          <span className="cred-card-title">{request.label}</span>
          {request.description && (
            <span className="cred-card-subtitle">{request.description}</span>
          )}
          <form onSubmit={handleSubmit} className="cred-card-form">
            <div className="cred-card-input-row">
              <input
                ref={inputRef}
                type={reveal ? 'text' : 'password'}
                value={value}
                onChange={e => setValue(e.target.value)}
                placeholder="Enter credential…"
                className="cred-card-input"
                autoComplete="off"
                spellCheck={false}
                disabled={status === 'submitting'}
              />
              <button
                type="button"
                className="cred-card-eye-btn"
                onMouseDown={() => setReveal(true)}
                onMouseUp={() => setReveal(false)}
                onMouseLeave={() => setReveal(false)}
                tabIndex={-1}
              >
                {reveal ? '\u{1F441}' : '\u{1F441}\u{200D}\u{1F5E8}'}
              </button>
            </div>
            <button
              type="submit"
              className="cred-card-btn"
              disabled={!value || status === 'submitting'}
            >
              {status === 'submitting' ? 'Submitting…' : '\u{1F512} Submit securely'}
            </button>
          </form>
        </div>
      </div>
    )
  }

  return (
    <div className="cred-card cred-card-pending">
      <div className="cred-card-icon cred-card-icon--svg" aria-hidden>
        <IconLock size={26} />
      </div>
      <div className="cred-card-body">
        <span className="cred-card-title">{request.label}</span>
        {request.description && (
          <span className="cred-card-subtitle">{request.description}</span>
        )}
      </div>
      <button
        className="cred-card-btn"
        onClick={onProvide}
        disabled={status === 'submitting'}
      >
        {status === 'submitting' ? 'Submitting…' : 'Provide credential'}
      </button>
    </div>
  )
}

export default CredentialRequestCard
