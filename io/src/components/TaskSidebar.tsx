import { useState, useEffect, useRef } from 'react'
import type { Task } from '../App'
import type { UISettings } from './SettingsPanel'
import './TaskSidebar.css'

interface TaskSidebarProps {
  tasks: Task[]
  selectedTaskId: string
  onSelectTask: (id: string) => void
  settings: UISettings
}

function parseETA(eta: string): number {
  if (eta === 'Now' || eta === 'Done') return eta === 'Now' ? 0 : Infinity
  const match = eta.match(/~?(\d+)\s*min/)
  if (match) return parseInt(match[1], 10)
  return 999
}

function TaskSidebar({ tasks, selectedTaskId, onSelectTask, settings }: TaskSidebarProps) {
  const [showFinishedOnly, setShowFinishedOnly] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [secondsSinceHeartbeat, setSecondsSinceHeartbeat] = useState(0)
  const lastHeartbeatRef = useRef(Date.now())

  const filteredByToggle = showFinishedOnly
    ? tasks.filter(t => t.status === 'completed')
    : tasks.filter(t => t.status !== 'completed')

  const searchLower = searchQuery.trim().toLowerCase()
  const filteredTasks = searchLower
    ? filteredByToggle.filter(t => t.title.toLowerCase().includes(searchLower))
    : filteredByToggle

  const hasUrgentTasks = tasks.some(t => t.status === 'awaiting_feedback')

  const sortedTasks = [...filteredTasks].sort((a, b) => {
    if (settings.highlightAwaitingFeedback) {
      const priority = { awaiting_feedback: 0, working: 1, completed: 2 }
      const pA = priority[a.status] ?? 1
      const pB = priority[b.status] ?? 1
      if (pA !== pB) return pA - pB
    }
    return parseETA(a.expectedNextInput) - parseETA(b.expectedNextInput)
  })

  useEffect(() => {
    const id = setInterval(() => {
      setSecondsSinceHeartbeat((Date.now() - lastHeartbeatRef.current) / 1000)
    }, 500)
    return () => clearInterval(id)
  }, [])

  const getStatusClass = (status: string) => {
    if (status === 'awaiting_feedback') return 'awaiting'
    if (status === 'working') return 'working'
    return 'completed'
  }

  return (
    <aside className="sidebar">
      <div className={`sidebar-header ${hasUrgentTasks ? 'has-urgent' : ''}`}>
        <h2>Agent Tasks</h2>
        <span className="task-count">{filteredTasks.length}</span>
      </div>
      <div className="sidebar-controls">
        <label className="toggle-label">
          <input
            type="checkbox"
            checked={showFinishedOnly}
            onChange={() => setShowFinishedOnly(!showFinishedOnly)}
            className="toggle-input"
          />
          <span className="toggle-text">Finished only</span>
        </label>
      </div>
      <div className="sidebar-search">
        <input
          type="search"
          value={searchQuery}
          onChange={e => setSearchQuery(e.target.value)}
          placeholder="Search tasks…"
          className="search-input"
          aria-label="Search tasks by title"
        />
      </div>
      <div className="task-list">
        {sortedTasks.length > 0 && (
          <div className="task-list-header">
            <span className="task-list-header-status">Status</span>
            <span className="task-list-header-title">Task</span>
            <span className="task-list-header-next">Next input</span>
          </div>
        )}
        {sortedTasks.length === 0 && (
          <div className="task-list-empty">
            <span className="empty-icon">📋</span>
            <span className="empty-text">No tasks to display</span>
          </div>
        )}
        {sortedTasks.map(task => (
          <button
            key={task.id}
            className={`task-item ${selectedTaskId === task.id ? 'selected' : ''} ${getStatusClass(task.status)}`}
            onClick={() => onSelectTask(task.id)}
          >
            <div className="task-status">
              {task.status === 'awaiting_feedback' && (
                <span className="status-icon urgent-dot" title="Awaiting feedback">
                  <span className="pulse-dot urgent"></span>
                </span>
              )}
              {task.status === 'working' && (
                settings.showHeartbeatSeconds ? (
                  <span className="status-icon heartbeat" title="Seconds since last heartbeat">
                    {secondsSinceHeartbeat.toFixed(1)}s
                  </span>
                ) : (
                  <span className="status-icon working-dot" title="Working">
                    <span className="pulse-dot"></span>
                  </span>
                )
              )}
              {task.status === 'completed' && (
                <span className="status-icon completed" title="Completed">✓</span>
              )}
            </div>
            <div className="task-info">
              <span className="task-title">{task.title}</span>
              <span className="task-update">{task.lastUpdate}</span>
            </div>
            <div className="task-eta">
              <span className={task.status === 'awaiting_feedback' ? 'urgent' : ''}>
                {task.expectedNextInput}
              </span>
            </div>
          </button>
        ))}
      </div>
    </aside>
  )
}

export default TaskSidebar
