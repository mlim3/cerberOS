import { useState, useEffect, useRef } from 'react'
import type { Task } from '@cerberos/io-core'
import type { UISettings } from './SettingsPanel'
import SidebarLogo from './SidebarLogo'
import './TaskSidebar.css'

interface TaskSidebarProps {
  tasks: Task[]
  selectedTaskId: string | null
  onSelectTask: (id: string) => void
  settings: UISettings
  taskHeartbeats: Record<string, number>
  onCreateTask: () => void
  onDeleteTask: (id: string) => void
  onRenameTask: (id: string, title: string) => void
}

function TaskSidebar({ tasks, selectedTaskId, onSelectTask, settings, taskHeartbeats, onCreateTask, onDeleteTask, onRenameTask }: TaskSidebarProps) {
  const [searchQuery, setSearchQuery] = useState('')
  const [tick, setTick] = useState(() => Date.now())
  const [openMenuId, setOpenMenuId] = useState<string | null>(null)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null)
  const renameInputRef = useRef<HTMLInputElement>(null)

  const searchLower = searchQuery.trim().toLowerCase()
  const filteredTasks = searchLower
    ? tasks.filter(t => t.title.toLowerCase().includes(searchLower))
    : tasks

  const hasUrgentTasks = tasks.some(t => t.status === 'awaiting_feedback')

  const sortedTasks = [...filteredTasks].sort((a, b) => {
    if (settings.highlightAwaitingFeedback) {
      const priority = { awaiting_feedback: 0, working: 1, completed: 2 }
      const pA = priority[a.status] ?? 1
      const pB = priority[b.status] ?? 1
      if (pA !== pB) return pA - pB
    }
    return 0
  })

  useEffect(() => {
    const id = setInterval(() => setTick(Date.now()), 300)
    return () => clearInterval(id)
  }, [])

  // Close dropdown on outside click
  useEffect(() => {
    if (!openMenuId) return
    const handler = (e: MouseEvent) => {
      const target = e.target as Element
      if (!target.closest('.task-menu-container')) setOpenMenuId(null)
    }
    window.addEventListener('mousedown', handler)
    return () => window.removeEventListener('mousedown', handler)
  }, [openMenuId])

  // Focus rename input when it appears
  useEffect(() => {
    if (renamingId) renameInputRef.current?.focus()
  }, [renamingId])

  const startRename = (id: string, currentTitle: string) => {
    setOpenMenuId(null)
    setRenamingId(id)
    setRenameValue(currentTitle)
  }

  const commitRename = () => {
    if (renamingId && renameValue.trim()) {
      onRenameTask(renamingId, renameValue.trim())
    }
    setRenamingId(null)
    setRenameValue('')
  }

  const cancelRename = () => {
    setRenamingId(null)
    setRenameValue('')
  }

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
        <div className="sidebar-controls-row">
          <button
            type="button"
            className="new-task-button"
            onClick={onCreateTask}
          >
            Create New Task
          </button>
        </div>
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
        {sortedTasks.length === 0 && (
          <div className="task-list-empty">
            <span className="empty-icon">📋</span>
            <span className="empty-text">No tasks to display</span>
          </div>
        )}
        {sortedTasks.map(task => (
          <div
            key={task.id}
            className={`task-item ${selectedTaskId === task.id ? 'selected' : ''} ${getStatusClass(task.status)} task-menu-container`}
          >
            {renamingId === task.id ? (
              <input
                ref={renameInputRef}
                className="task-rename-input"
                value={renameValue}
                onChange={e => setRenameValue(e.target.value)}
                onKeyDown={e => {
                  if (e.key === 'Enter') commitRename()
                  else if (e.key === 'Escape') cancelRename()
                }}
                onBlur={commitRename}
              />
            ) : (
              <>
                <button
                  className="task-item-btn"
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
                          {Math.max(0, (tick - (taskHeartbeats[task.id] ?? tick)) / 1000).toFixed(1)}s
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
                </button>
                <button
                  className="task-menu-trigger"
                  title="Task options"
                  onClick={e => {
                    e.stopPropagation()
                    setOpenMenuId(openMenuId === task.id ? null : task.id)
                  }}
                >
                  ···
                </button>
                {openMenuId === task.id && (
                  <div className="task-dropdown">
                    <button
                      className="task-dropdown-item"
                      onClick={() => startRename(task.id, task.title)}
                    >
                      Rename
                    </button>
                    <button
                      className="task-dropdown-item danger"
                      onClick={() => {
                        setOpenMenuId(null)
                        setConfirmDeleteId(task.id)
                      }}
                    >
                      Delete
                    </button>
                  </div>
                )}
              </>
            )}
          </div>
        ))}
      </div>
      <SidebarLogo />
      {confirmDeleteId && (() => {
        const task = tasks.find(t => t.id === confirmDeleteId)
        return (
          <div className="confirm-overlay" role="dialog" aria-modal="true">
            <div className="confirm-modal">
              <p className="confirm-message">
                Delete <strong>"{task?.title ?? 'this task'}"</strong>?
              </p>
              <p className="confirm-subtext">This cannot be undone.</p>
              <div className="confirm-actions">
                <button
                  className="confirm-cancel"
                  onClick={() => setConfirmDeleteId(null)}
                  autoFocus
                >
                  Cancel
                </button>
                <button
                  className="confirm-delete"
                  onClick={() => {
                    onDeleteTask(confirmDeleteId)
                    setConfirmDeleteId(null)
                  }}
                >
                  Delete
                </button>
              </div>
            </div>
          </div>
        )
      })()}
    </aside>
  )
}

export default TaskSidebar
