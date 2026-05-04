import { useState } from 'react'
import type { ToolCallInfo } from '@cerberos/io-core'
import './ToolCallBlock.css'

const SENSITIVE_KEYS = /password|secret|token|credential|key|api_key/i

function scrubValue(key: string, value: unknown): unknown {
  if (SENSITIVE_KEYS.test(key)) return '[REDACTED]'
  if (typeof value === 'object' && value !== null) {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([k, v]) => [k, scrubValue(k, v)])
    )
  }
  return value
}

function formatInputs(inputs: Record<string, unknown>): string {
  const scrubbed = Object.fromEntries(
    Object.entries(inputs).map(([k, v]) => [k, scrubValue(k, v)])
  )
  return Object.entries(scrubbed)
    .map(([k, v]) => `${k}: ${JSON.stringify(v)}`)
    .join(', ')
}

function formatDuration(ms?: number): string {
  if (!ms) return ''
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${ms}ms`
}

export default function ToolCallBlock({ tool }: { tool: ToolCallInfo }) {
  const [expanded, setExpanded] = useState(false)

  return (
    <div
      className={`tool-call-block ${expanded ? 'expanded' : ''} ${tool.status}`}
      onClick={() => setExpanded(!expanded)}
      role="button"
      aria-expanded={expanded}
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          setExpanded(!expanded)
        }
      }}
    >
      <div className="tool-call-header">
        <span className="tool-call-arrow">{expanded ? '\u25BC' : '\u25B6'}</span>
        <span className="tool-call-name">{tool.toolName}</span>
        {tool.status === 'completed' && <span className="tool-call-success">{'\u2713'}</span>}
        {tool.status === 'error' && <span className="tool-call-error">{'\u2717'}</span>}
        {tool.durationMs != null && (
          <span className="tool-call-duration">{'\u00B7'} {formatDuration(tool.durationMs)}</span>
        )}
      </div>
      {expanded && (
        <div className="tool-call-detail">
          <div className="tool-call-section">
            <span className="tool-call-label">INPUT</span>
            <span className="tool-call-value">{formatInputs(tool.inputs)}</span>
          </div>
          {tool.output && (
            <div className="tool-call-section">
              <span className="tool-call-label">OUTPUT</span>
              <span className={`tool-call-value ${tool.status}`}>{tool.output}</span>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
