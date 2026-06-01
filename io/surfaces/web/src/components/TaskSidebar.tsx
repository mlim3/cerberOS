import { useState, useEffect, useRef } from 'react'
import type { Task } from '@cerberos/io-core'
import type { UserCronJob } from '../api/userCrons'
import type { UISettings } from './SettingsPanel'
import SidebarLogo from './SidebarLogo'
import {
  SidebarEmptyRecurringIcon,
  SidebarEmptyTasksIcon,
  TaskRowCompletedIcon,
} from './icons/SidebarEmptyIcons'
import './TaskSidebar.css'
import './UserCronSection.css'

export type SidebarPrimaryTab = 'tasks' | 'recurring'

interface TaskSidebarProps {
  sidebarTab: SidebarPrimaryTab
  onSidebarTabChange: (tab: SidebarPrimaryTab) => void
  tasks: Task[]
  selectedTaskId: string | null
  onSelectTask: (id: string) => void
  settings: UISettings
  taskHeartbeats: Record<string, number>
  onCreateTask: () => void
  onDeleteTask: (id: string) => void
  onRenameTask: (id: string, title: string) => void
  userCronJobs: UserCronJob[]
  userCronLoadError: string | null
  userCronListLoading: boolean
  onReloadUserCronJobs: () => void | Promise<void>
  onDeleteUserCronJob: (id: string) => Promise<{ ok: true } | { ok: false; error: string }>
  onSelectRecurringJob: (job: UserCronJob) => void
  highlightRecurringJobId: string | null
  conversationUnreadIds: Record<string, true>
}

function TaskSidebar({
  sidebarTab,
  onSidebarTabChange,
  tasks,
  selectedTaskId,
  onSelectTask,
  settings,
  taskHeartbeats,
  onCreateTask,
  onDeleteTask,
  onRenameTask,
  userCronJobs,
  userCronLoadError,
  userCronListLoading,
  onReloadUserCronJobs,
  onDeleteUserCronJob,
  onSelectRecurringJob,
  highlightRecurringJobId,
  conversationUnreadIds,
}: TaskSidebarProps) {
  const [searchQuery, setSearchQuery] = useState('')
  const [tick, setTick] = useState(() => Date.now())
  const [openMenuId, setOpenMenuId] = useState<string | null>(null)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null)
  const [cronActionError, setCronActionError] = useState<string | null>(null)
  const [copiedCidTaskId, setCopiedCidTaskId] = useState<string | null>(null)
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

  const describeScheduleKind = (job: UserCronJob) =>
    job.scheduleKind === 'cron' ? 'Cron' : `Every ${job.intervalSeconds}s`

  // copyConversationId puts the full conversation_id on the clipboard so the
  // user can paste it into Loki/Grafana queries. We deliberately surface the
  // copy via a dropdown action (and a hover-revealed short prefix) rather
  // than always-on text to keep the sidebar density low while still making
  // the ID one click away for log correlation during the demo.
  const copyConversationId = async (taskId: string, cid: string) => {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(cid)
      } else {
        const ta = document.createElement('textarea')
        ta.value = cid
        ta.style.position = 'fixed'
        ta.style.opacity = '0'
        document.body.appendChild(ta)
        ta.select()
        document.execCommand('copy')
        document.body.removeChild(ta)
      }
      setCopiedCidTaskId(taskId)
      window.setTimeout(() => setCopiedCidTaskId((curr) => (curr === taskId ? null : curr)), 1500)
    } catch {
      // Clipboard rejected (insecure context, permissions, etc.) — fall back
      // to a window.prompt so the user can still copy the value manually.
      window.prompt('Copy conversation ID:', cid)
    }
  }

  return (
    <aside className="sidebar">
      <SidebarLogo />

      <div className={`sidebar-header ${hasUrgentTasks ? 'has-urgent' : ''}`}>
        <h2>{sidebarTab === 'tasks' ? 'Agent tasks' : 'Recurring tasks'}</h2>
        <span className="task-count">{sidebarTab === 'tasks' ? filteredTasks.length : userCronJobs.length}</span>
      </div>

      <div className="sidebar-tabs" role="tablist" aria-label="Sidebar primary view">
        <button
          type="button"
          role="tab"
          aria-selected={sidebarTab === 'tasks'}
          className={`sidebar-tab ${sidebarTab === 'tasks' ? 'sidebar-tab-active' : ''}`}
          onClick={() => onSidebarTabChange('tasks')}
        >
          Tasks
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={sidebarTab === 'recurring'}
          className={`sidebar-tab ${sidebarTab === 'recurring' ? 'sidebar-tab-active' : ''}`}
          onClick={() => onSidebarTabChange('recurring')}
        >
          Recurring
        </button>
      </div>

      {sidebarTab === 'tasks' && (
        <>
          <div className="sidebar-controls">
            <div className="sidebar-controls-row sidebar-controls-buttons">
              <button type="button" className="new-task-button new-task-button-primary" onClick={onCreateTask}>
                Create new task
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
                <SidebarEmptyTasksIcon className="empty-icon empty-icon--svg" />
                <span className="empty-text">No tasks to display</span>
              </div>
            )}
            {sortedTasks.map(task => (
              <div
                key={task.id}
                className={`task-item ${selectedTaskId === task.id ? 'selected' : ''} ${getStatusClass(task.status)} task-menu-container${
                  conversationUnreadIds[task.id] ? ' task-item-unread-thread' : ''
                }`}
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
                    <button type="button" className="task-item-btn" onClick={() => onSelectTask(task.id)}>
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
                          <span className="status-icon completed" title="Completed">
                            <TaskRowCompletedIcon />
                          </span>
                        )}
                      </div>
                      <div className="task-info">
                        {conversationUnreadIds[task.id] && (
                          <span className="thread-unread-badge" title="New scheduled turn in thread">
                            New
                          </span>
                        )}
                        <span className="task-title">{task.title}</span>
                        <span className="task-update">{task.lastUpdate}</span>
                        {/* task.id IS the conversation_id once the first
                            message has been sent — they're unified by the
                            backend (see App.tsx newCid swap). Surfacing it
                            here makes Loki log correlation one click away. */}
                        <span
                          className="task-cid"
                          title={`Conversation ID: ${task.id} — open the task menu to copy`}
                        >
                          cid: {task.id.slice(0, 8)}…
                        </span>
                      </div>
                    </button>
                    <button
                      type="button"
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
                        <button type="button" className="task-dropdown-item" onClick={() => startRename(task.id, task.title)}>
                          Rename
                        </button>
                        <button
                          type="button"
                          className="task-dropdown-item"
                          onClick={() => {
                            setOpenMenuId(null)
                            void copyConversationId(task.id, task.id)
                          }}
                          title={task.id}
                        >
                          {copiedCidTaskId === task.id ? 'Copied!' : 'Copy CID to clipboard'}
                        </button>
                        <button
                          type="button"
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
        </>
      )}

      {sidebarTab === 'recurring' && (
        <>
          <div className="sidebar-controls">
            <div className="sidebar-controls-row sidebar-controls-buttons">
              <button
                type="button"
                className="new-task-button new-task-button-secondary"
                disabled={userCronListLoading}
                onClick={() => {
                  setCronActionError(null)
                  void onReloadUserCronJobs()
                }}
              >
                {userCronListLoading ? 'Refreshing…' : 'Refresh'}
              </button>
            </div>
            <p className="sidebar-recurring-explainer">
              Ask the agent to create a recurring task in any chat. Selecting a listed job jumps to its conversation when available.
            </p>
          </div>
          {userCronLoadError && <p className="sidebar-recurring-error">{userCronLoadError}</p>}
          {cronActionError && <p className="sidebar-recurring-error">{cronActionError}</p>}
          <div className="task-list">
            {userCronJobs.length === 0 ? (
              <div className="task-list-empty">
                <SidebarEmptyRecurringIcon className="empty-icon empty-icon--svg" />
                <span className="empty-text">No recurring tasks yet.</span>
                <span className="sidebar-recurring-cta-link">Ask the agent to schedule a recurring task.</span>
              </div>
            ) : (
              <ul className="user-cron-list sidebar-recurring-list">
                {userCronJobs.map(j => (
                  <li
                    key={j.id}
                    className={`user-cron-card sidebar-recurring-card ${
                      highlightRecurringJobId === j.id ? 'sidebar-recurring-card-active' : ''
                    }`}
                    onClick={() => onSelectRecurringJob(j)}
                  >
                    <div className="user-cron-card-body">
                      <div className="user-cron-card-title-row">
                        <strong className="user-cron-card-name">{j.name}</strong>
                        <span className={`user-cron-kind user-cron-kind--${j.scheduleKind}`}>
                          {describeScheduleKind(j)}
                        </span>
                      </div>
                      {j.payload?.rawInput ? (
                        <p className="user-cron-card-prompt">{j.payload.rawInput}</p>
                      ) : (
                        <p className="user-cron-card-prompt user-cron-card-prompt--empty">No prompt stored</p>
                      )}
                      <div className="user-cron-card-meta">
                        {j.scheduleKind === 'cron' ? (
                          <code className="user-cron-expr">
                            {j.cronExpression} · {j.timeZone || 'UTC'}
                          </code>
                        ) : (
                          <span>{describeScheduleKind(j)}</span>
                        )}
                        <span className="user-cron-next">Next · {new Date(j.nextRunAt).toLocaleString()}</span>
                      </div>
                    </div>
                    <button
                      type="button"
                      className="user-cron-remove"
                      onClick={e => {
                        e.stopPropagation()
                        void (async () => {
                          setCronActionError(null)
                          const r = await onDeleteUserCronJob(j.id)
                          if (!r.ok) setCronActionError(r.error)
                        })()
                      }}
                    >
                      Remove
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </>
      )}

      {confirmDeleteId && (() => {
        const task = tasks.find(t => t.id === confirmDeleteId)
        return (
          <div className="confirm-overlay" role="dialog" aria-modal="true">
            <div className="confirm-modal">
              <p className="confirm-message">
                Delete <strong>&quot;{task?.title ?? 'this task'}&quot;</strong>?
              </p>
              <p className="confirm-subtext">This cannot be undone.</p>
              <div className="confirm-actions">
                <button type="button" className="confirm-cancel" onClick={() => setConfirmDeleteId(null)} autoFocus>
                  Cancel
                </button>
                <button
                  type="button"
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
