import { useState, useEffect, useRef } from 'react'
import type { Task } from '../App'
import './TaskSidebar.css'

interface TaskSidebarProps {
  tasks: Task[]
  selectedTaskId: string
  onSelectTask: (id: string) => void
}

function TaskSidebar({ tasks, selectedTaskId, onSelectTask }: TaskSidebarProps) {
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

  const sortedTasks = [...filteredTasks].sort((a, b) => {
    const priority = { awaiting_feedback: 0, working: 1, completed: 2 }
    return priority[a.status] - priority[b.status]
  })

  useEffect(() => {
    const id = setInterval(() => {
      setSecondsSinceHeartbeat((Date.now() - lastHeartbeatRef.current) / 1000)
    }, 500)
    return () => clearInterval(id)
  }, [])

  return (
    <aside className="sidebar">
      <div className="sidebar-header">
        <h2>Tasks</h2>
        <span className="task-count">{filteredTasks.length}</span>
      </div>
      <div className="sidebar-toggle">
        <label className="toggle-label">
          <input
            type="checkbox"
            checked={showFinishedOnly}
            onChange={() => setShowFinishedOnly(!showFinishedOnly)}
            className="toggle-input"
          />
          <span className="toggle-text">Show finished only</span>
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
        {sortedTasks.map(task => (
          <button
            key={task.id}
            className={`task-item ${selectedTaskId === task.id ? 'selected' : ''}`}
            onClick={() => onSelectTask(task.id)}
          >
            <div className="task-status">
              {task.status === 'awaiting_feedback' && (
                <span className="status-icon warning" title="Awaiting feedback">⚠️</span>
              )}
              {task.status === 'working' && (
                <span className="status-icon heartbeat" title="Seconds since last heartbeat">
                  {secondsSinceHeartbeat.toFixed(1)}s
                </span>
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
