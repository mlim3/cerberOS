import { useState, useRef, useEffect } from 'react'
import type { CredentialRequest } from '@cerberos/io-core'
import './CredentialModal.css'

interface CredentialModalProps {
  request: CredentialRequest
  onSubmit: (requestId: string, credential: string) => void | Promise<void>
  onCancel: () => void
  isSubmitting?: boolean
}

function CredentialModal({ request, onSubmit, onCancel, isSubmitting }: CredentialModalProps) {
  const [value, setValue] = useState('')
  const [reveal, setReveal] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  useEffect(() => {
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', handleKey)
    return () => window.removeEventListener('keydown', handleKey)
  }, [onCancel])

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!value || isSubmitting) return
    onSubmit(request.requestId, value)
  }

  return (
    <div className="credential-overlay" onClick={onCancel}>
      <div className="credential-modal" onClick={e => e.stopPropagation()}>
        <div className="credential-modal-header">
          <span className="credential-lock-icon">{'\u{1F512}'}</span>
          <h2>Secure Credential Entry</h2>
        </div>

        <p className="credential-modal-label">{request.label}</p>
        {request.description && (
          <p className="credential-modal-desc">{request.description}</p>
        )}

        <div className="credential-modal-notice">
          This credential is transmitted through a dedicated secure channel.
          It will <strong>not</strong> appear in chat history, logs, or conversation context.
        </div>

        <form onSubmit={handleSubmit}>
          <div className="credential-input-row">
            <input
              ref={inputRef}
              type={reveal ? 'text' : 'password'}
              value={value}
              onChange={e => setValue(e.target.value)}
              placeholder="Enter credential…"
              className="credential-input"
              autoComplete="off"
              spellCheck={false}
            />
            <button
              type="button"
              className="credential-eye-btn"
              onMouseDown={() => setReveal(true)}
              onMouseUp={() => setReveal(false)}
              onMouseLeave={() => setReveal(false)}
              title="Hold to reveal"
            >
              {reveal ? '\u{1F441}' : '\u{1F441}\u{200D}\u{1F5E8}'}
            </button>
          </div>

          <div className="credential-modal-actions">
            <button
              type="button"
              className="credential-cancel-btn"
              onClick={onCancel}
              disabled={isSubmitting}
            >
              Cancel
            </button>
            <button
              type="submit"
              className="credential-submit-btn"
              disabled={!value || isSubmitting}
            >
              {isSubmitting ? 'Submitting…' : '\u{1F512} Submit securely'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

export default CredentialModal
