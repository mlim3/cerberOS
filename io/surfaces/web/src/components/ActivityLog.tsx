import { useEffect, useRef } from 'react'
import './ActivityLog.css'

export interface LogEntry {
  id: string
  timestamp: string
  type: 'heartbeat' | 'user_message' | 'agent_response' | 'status_change'
  taskId: string
  taskTitle: string
  message: string
}

interface ActivityLogProps {
  entries: LogEntry[]
  onClose: () => void
}

function ActivityLog({ entries, onClose }: ActivityLogProps) {
  const logEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [entries])

  const getTypeIcon = (type: LogEntry['type']) => {
    switch (type) {
      case 'heartbeat':
        return '💓'
      case 'user_message':
        return '👤'
      case 'agent_response':
        return '🤖'
      case 'status_change':
        return '🔔'
      default:
        return '📝'
    }
  }

  const getTypeClass = (type: LogEntry['type']) => {
    switch (type) {
      case 'heartbeat':
        return 'heartbeat'
      case 'user_message':
        return 'user'
      case 'agent_response':
        return 'agent'
      case 'status_change':
        return 'status'
      default:
        return ''
    }
  }

  return (
    <div className="activity-log">
      <div className="activity-log-header">
        <h3>Activity Log</h3>
        <button className="activity-log-close" onClick={onClose} aria-label="Close activity log">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <line x1="18" y1="6" x2="6" y2="18" />
            <line x1="6" y1="6" x2="18" y2="18" />
          </svg>
        </button>
      </div>
      <div className="activity-log-content">
        {entries.length === 0 && (
          <div className="activity-log-empty">
            <span>No activity yet</span>
          </div>
        )}
        {entries.map(entry => (
          <div key={entry.id} className={`log-entry ${getTypeClass(entry.type)}`}>
            <span className="log-timestamp">[{entry.timestamp}]</span>
            <span className="log-icon">{getTypeIcon(entry.type)}</span>
            <span className="log-task">{entry.taskTitle}:</span>
            <span className="log-message">{entry.message}</span>
          </div>
        ))}
        <div ref={logEndRef} />
      </div>
    </div>
  )
}

export default ActivityLog
