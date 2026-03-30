import type { CredentialRequest, CredentialRequestStatus } from '@cerberos/io-core'
import './CredentialRequestCard.css'

interface CredentialRequestCardProps {
  request: CredentialRequest
  status: CredentialRequestStatus
  onProvide: () => void
}

function CredentialRequestCard({ request, status, onProvide }: CredentialRequestCardProps) {
  if (status === 'submitted') {
    return (
      <div className="cred-card cred-card-done">
        <div className="cred-card-icon">{'\u{2705}'}</div>
        <div className="cred-card-body">
          <span className="cred-card-title">Credential provided securely</span>
          <span className="cred-card-subtitle">{request.label}</span>
        </div>
      </div>
    )
  }

  return (
    <div className="cred-card cred-card-pending">
      <div className="cred-card-icon">{'\u{1F512}'}</div>
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
