import type { ToolCallInfo } from '@cerberos/io-core'
import './ToolCallChip.css'

function formatDuration(ms?: number): string {
  if (!ms) return ''
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${ms}ms`
}

export default function ToolCallChip({ tool }: { tool: ToolCallInfo }) {
  const duration = tool.durationMs != null ? formatDuration(tool.durationMs) : ''

  return (
    <span className={`tool-call-chip tool-call-chip--${tool.status}`} title={tool.toolName}>
      <span className="tool-call-chip__glyph" aria-hidden="true">
        {tool.status === 'running' && <span className="tool-call-chip__spinner" />}
        {tool.status === 'completed' && '✓'}
        {tool.status === 'error' && '✗'}
      </span>
      <span className="tool-call-chip__name">{tool.toolName}</span>
      {duration && (
        <span className="tool-call-chip__duration">{duration}</span>
      )}
    </span>
  )
}
