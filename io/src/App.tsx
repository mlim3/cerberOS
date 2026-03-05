import { useState, useCallback, useEffect, useRef } from 'react'
import TaskSidebar from './components/TaskSidebar'
import ChatWindow from './components/ChatWindow'
import SettingsButton from './components/SettingsButton'
import SettingsPanel, { defaultUISettings } from './components/SettingsPanel'
import type { UISettings } from './components/SettingsPanel'
import ActivityLog from './components/ActivityLog'
import type { LogEntry } from './components/ActivityLog'
import { streamOrchestratorReply } from './api/orchestrator'
import './App.css'

const SETTINGS_STORAGE_KEY = 'cerberos-io-settings'

function loadSettings(): UISettings {
  try {
    const stored = localStorage.getItem(SETTINGS_STORAGE_KEY)
    if (stored) {
      return { ...defaultUISettings, ...JSON.parse(stored) }
    }
  } catch {
    // ignore parse errors
  }
  return defaultUISettings
}

function saveSettings(settings: UISettings): void {
  try {
    localStorage.setItem(SETTINGS_STORAGE_KEY, JSON.stringify(settings))
  } catch {
    // ignore storage errors
  }
}

export interface Task {
  id: string
  title: string
  status: 'awaiting_feedback' | 'working' | 'completed'
  lastUpdate: string
  expectedNextInput: string
  messages: Message[]
}

export interface Message {
  id: string
  role: 'user' | 'agent'
  content: string
  timestamp: string
}

function nextId(): string {
  return Math.random().toString(36).slice(2, 11)
}

function timeLabel(): string {
  const d = new Date()
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

/** Random 2000–4000 ms in 100 ms increments (mock heartbeat interval). */
function randomHeartbeatMs(): number {
  return Math.floor(Math.random() * 21) * 100 + 2000
}

const mockTasks: Task[] = [
  {
    id: '1',
    title: 'Implement authentication flow',
    status: 'awaiting_feedback',
    lastUpdate: 'Awaiting user approval for OAuth provider selection',
    expectedNextInput: 'Now',
    messages: [
      { id: '1a', role: 'user', content: 'Set up authentication for the app', timestamp: '10:30 AM' },
      { id: '1b', role: 'agent', content: 'I\'ve analyzed the requirements. For authentication, I recommend implementing OAuth 2.0 with support for Google and GitHub providers. Should I proceed with this approach, or would you prefer a different authentication method?', timestamp: '10:31 AM' },
    ],
  },
  {
    id: '2',
    title: 'Refactor database schema',
    status: 'awaiting_feedback',
    lastUpdate: 'Migration script ready for review',
    expectedNextInput: 'Now',
    messages: [
      { id: '2a', role: 'user', content: 'Optimize the user table for better query performance', timestamp: '9:15 AM' },
      { id: '2b', role: 'agent', content: 'I\'ve created a migration script that adds indexes to the user table and normalizes the address fields. The migration is ready for your review before execution.', timestamp: '9:45 AM' },
    ],
  },
  {
    id: '3',
    title: 'Build dashboard components',
    status: 'working',
    lastUpdate: 'Creating chart components...',
    expectedNextInput: '~5 min',
    messages: [
      { id: '3a', role: 'user', content: 'Create a dashboard with user metrics and activity charts', timestamp: '11:00 AM' },
      { id: '3b', role: 'agent', content: 'Working on the dashboard components. Currently implementing the activity chart using recharts library...', timestamp: '11:02 AM' },
    ],
  },
  {
    id: '4',
    title: 'API endpoint testing',
    status: 'working',
    lastUpdate: 'Running integration tests...',
    expectedNextInput: '~12 min',
    messages: [
      { id: '4a', role: 'user', content: 'Write comprehensive tests for all REST endpoints', timestamp: '8:00 AM' },
      { id: '4b', role: 'agent', content: 'I\'m systematically testing each endpoint. Currently on the user management endpoints. 15 of 32 tests completed.', timestamp: '8:30 AM' },
    ],
  },
  {
    id: '5',
    title: 'Documentation update',
    status: 'completed',
    lastUpdate: 'Documentation published',
    expectedNextInput: 'Done',
    messages: [
      { id: '5a', role: 'user', content: 'Update the API documentation with new endpoints', timestamp: 'Yesterday' },
      { id: '5b', role: 'agent', content: 'Documentation has been updated with all new endpoints, including request/response examples and authentication requirements. The docs are now published.', timestamp: 'Yesterday' },
    ],
  },
  {
    id: '6',
    title: 'Cache layer implementation',
    status: 'completed',
    lastUpdate: 'Redis cache wired and tested',
    expectedNextInput: 'Done',
    messages: [
      { id: '6a', role: 'user', content: 'Add a cache layer for read-heavy endpoints', timestamp: 'Yesterday' },
      { id: '6b', role: 'agent', content: 'Redis cache is in place with TTLs and invalidation on write. Load tests show a 3x improvement on the hot paths.', timestamp: 'Yesterday' },
    ],
  },
  {
    id: '7',
    title: 'Error handling and retry logic',
    status: 'completed',
    lastUpdate: 'Retry policies applied to external calls',
    expectedNextInput: 'Done',
    messages: [
      { id: '7a', role: 'user', content: 'Improve error handling for third-party API calls', timestamp: '2 days ago' },
      { id: '7b', role: 'agent', content: 'Exponential backoff and circuit breaker are configured. Errors are logged with context for debugging.', timestamp: '2 days ago' },
    ],
  },
  {
    id: '8',
    title: 'Logging and monitoring setup',
    status: 'completed',
    lastUpdate: 'Structured logs and dashboards live',
    expectedNextInput: 'Done',
    messages: [
      { id: '8a', role: 'user', content: 'Set up structured logging and a basic monitoring dashboard', timestamp: '2 days ago' },
      { id: '8b', role: 'agent', content: 'Structured JSON logs are shipping to the aggregator. Dashboard includes latency p99 and error rate.', timestamp: '2 days ago' },
    ],
  },
  {
    id: '9',
    title: 'CI pipeline configuration',
    status: 'completed',
    lastUpdate: 'Pipeline green on main',
    expectedNextInput: 'Done',
    messages: [
      { id: '9a', role: 'user', content: 'Configure CI to run tests and lint on every PR', timestamp: '3 days ago' },
      { id: '9b', role: 'agent', content: 'GitHub Actions workflow runs unit and integration tests, plus ESLint. Status checks are required for merge.', timestamp: '3 days ago' },
    ],
  },
  {
    id: '10',
    title: 'Deploy script and rollback',
    status: 'completed',
    lastUpdate: 'Deploy and rollback documented',
    expectedNextInput: 'Done',
    messages: [
      { id: '10a', role: 'user', content: 'Add a deploy script with one-command rollback', timestamp: '3 days ago' },
      { id: '10b', role: 'agent', content: 'Deploy script uses blue-green; rollback restores the previous release. Runbook is in the wiki.', timestamp: '3 days ago' },
    ],
  },
  {
    id: '11',
    title: 'User onboarding flow',
    status: 'completed',
    lastUpdate: 'Onboarding steps and emails done',
    expectedNextInput: 'Done',
    messages: [
      { id: '11a', role: 'user', content: 'Design the first-time user onboarding flow', timestamp: '4 days ago' },
      { id: '11b', role: 'agent', content: 'Welcome email, checklist, and first-run tutorial are implemented. Analytics events added for funnel tracking.', timestamp: '4 days ago' },
    ],
  },
  {
    id: '12',
    title: 'Security audit and dependency update',
    status: 'completed',
    lastUpdate: 'Vulnerabilities addressed',
    expectedNextInput: 'Done',
    messages: [
      { id: '12a', role: 'user', content: 'Run a security audit and update vulnerable dependencies', timestamp: 'Last week' },
      { id: '12b', role: 'agent', content: 'npm audit and Snyk run completed. Critical and high issues are fixed; lockfile updated.', timestamp: 'Last week' },
    ],
  },
]

function App() {
  const [tasks, setTasks] = useState<Task[]>(mockTasks)
  const [selectedTaskId, setSelectedTaskId] = useState<string>(mockTasks[0].id)
  const [showSettings, setShowSettings] = useState(false)
  const [streamingForTaskId, setStreamingForTaskId] = useState<string | null>(null)
  const [streamingContent, setStreamingContent] = useState('')
  const [uiSettings, setUISettings] = useState<UISettings>(loadSettings)
  const [logEntries, setLogEntries] = useState<LogEntry[]>([])
  const heartbeatIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const [taskHeartbeats, setTaskHeartbeats] = useState<Record<string, number>>({})
  const taskNextHeartbeatAtRef = useRef<Record<string, number>>({})

  const selectedTask = tasks.find(t => t.id === selectedTaskId)

  const addLogEntry = useCallback((entry: Omit<LogEntry, 'id' | 'timestamp'>) => {
    const newEntry: LogEntry = {
      ...entry,
      id: nextId(),
      timestamp: timeLabel(),
    }
    setLogEntries(prev => [...prev.slice(-99), newEntry])
  }, [])

  useEffect(() => {
    saveSettings(uiSettings)
  }, [uiSettings])

  useEffect(() => {
    document.documentElement.setAttribute('data-font-scale', uiSettings.fontSizeScale)
    document.documentElement.setAttribute('data-high-contrast', uiSettings.highContrast ? 'true' : 'false')
  }, [uiSettings.fontSizeScale, uiSettings.highContrast])

  useEffect(() => {
    const id = setInterval(() => {
      const now = Date.now()
      const working = tasks.filter(t => t.status === 'working')
      let updated = false
      const nextUpdates: Record<string, number> = {}
      for (const task of working) {
        const nextAt = taskNextHeartbeatAtRef.current[task.id]
        if (nextAt === undefined || nextAt === 0 || now >= nextAt) {
          const next = now + randomHeartbeatMs()
          taskNextHeartbeatAtRef.current[task.id] = next
          nextUpdates[task.id] = now
          updated = true
        }
      }
      if (updated) {
        setTaskHeartbeats(prev => ({ ...prev, ...nextUpdates }))
      }
    }, 100)
    return () => clearInterval(id)
  }, [tasks])

  useEffect(() => {
    if (!uiSettings.showActivityLog) {
      if (heartbeatIntervalRef.current) {
        clearInterval(heartbeatIntervalRef.current)
        heartbeatIntervalRef.current = null
      }
      return
    }

    heartbeatIntervalRef.current = setInterval(() => {
      const workingTasks = tasks.filter(t => t.status === 'working')
      if (workingTasks.length > 0) {
        const task = workingTasks[Math.floor(Math.random() * workingTasks.length)]
        addLogEntry({
          type: 'heartbeat',
          taskId: task.id,
          taskTitle: task.title.slice(0, 20),
          message: `${task.lastUpdate} Next input in ${task.expectedNextInput}.`,
        })
      }
    }, 3000)

    return () => {
      if (heartbeatIntervalRef.current) {
        clearInterval(heartbeatIntervalRef.current)
      }
    }
  }, [uiSettings.showActivityLog, tasks, addLogEntry])

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'k') {
        e.preventDefault()
        const input = document.querySelector('.chat-input') as HTMLInputElement | null
        input?.focus()
      }

      if (e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
        e.preventDefault()
        const currentIndex = tasks.findIndex(t => t.id === selectedTaskId)
        if (currentIndex === -1) return
        const newIndex = e.key === 'ArrowUp'
          ? Math.max(0, currentIndex - 1)
          : Math.min(tasks.length - 1, currentIndex + 1)
        if (newIndex !== currentIndex) {
          setSelectedTaskId(tasks[newIndex].id)
        }
      }

      if (e.key === 'Escape' && showSettings) {
        setShowSettings(false)
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [tasks, selectedTaskId, showSettings])

  const onSendMessage = useCallback(
    async (taskId: string, userContent: string) => {
      const task = tasks.find(t => t.id === taskId)
      if (!task) return
      const userMsg: Message = {
        id: nextId(),
        role: 'user',
        content: userContent,
        timestamp: timeLabel(),
      }
      setTasks(prev =>
        prev.map(t =>
          t.id === taskId ? { ...t, messages: [...t.messages, userMsg] } : t
        )
      )

      if (uiSettings.showActivityLog) {
        addLogEntry({
          type: 'user_message',
          taskId,
          taskTitle: task.title.slice(0, 20),
          message: `User: "${userContent.slice(0, 50)}${userContent.length > 50 ? '…' : ''}"`,
        })
      }

      setStreamingForTaskId(taskId)
      setStreamingContent('')
      const history = task.messages.map(m => ({
        role: m.role as 'user' | 'assistant',
        content: m.content,
      }))
      let full = ''
      try {
        for await (const chunk of streamOrchestratorReply(
          taskId,
          userContent,
          [...history, { role: 'user', content: userContent }]
        )) {
          full = chunk
          setStreamingContent(chunk)
        }
      } finally {
        setStreamingForTaskId(null)
        setStreamingContent('')
        if (full) {
          const agentMsg: Message = {
            id: nextId(),
            role: 'agent',
            content: full,
            timestamp: timeLabel(),
          }
          setTasks(prev =>
            prev.map(t =>
              t.id === taskId ? { ...t, messages: [...t.messages, agentMsg] } : t
            )
          )

          if (uiSettings.showActivityLog) {
            addLogEntry({
              type: 'agent_response',
              taskId,
              taskTitle: task.title.slice(0, 20),
              message: `Agent replied (${full.length} chars)`,
            })
          }
        }
      }
    },
    [tasks, uiSettings.showActivityLog, addLogEntry]
  )

  return (
    <div className="app">
      <TaskSidebar
        tasks={tasks}
        selectedTaskId={selectedTaskId}
        onSelectTask={setSelectedTaskId}
        settings={uiSettings}
        taskHeartbeats={taskHeartbeats}
      />
      <main className="main-content">
        <header className="header">
          <div className="header-task-info">
            <h1 className="header-title">
              {selectedTask?.title || 'Select a task'}
            </h1>
            {selectedTask && (
              <div className="header-meta">
                <span className={`header-status-pill ${selectedTask.status}`}>
                  {selectedTask.status === 'working' && (
                    <span className="header-heartbeat-dot"></span>
                  )}
                  {selectedTask.status === 'awaiting_feedback' && 'Awaiting feedback'}
                  {selectedTask.status === 'working' && 'In progress'}
                  {selectedTask.status === 'completed' && 'Completed'}
                </span>
                <span className="header-eta">
                  ETA: {selectedTask.expectedNextInput}
                </span>
              </div>
            )}
            {selectedTask && selectedTask.status !== 'completed' && (
              <p className="header-last-update">{selectedTask.lastUpdate}</p>
            )}
          </div>
          <SettingsButton
            isOpen={showSettings}
            onToggle={() => setShowSettings(!showSettings)}
          />
        </header>
        {selectedTask ? (
          <ChatWindow
            task={selectedTask}
            onSendMessage={onSendMessage}
            isStreaming={streamingForTaskId === selectedTask.id}
            streamingContent={streamingForTaskId === selectedTask.id ? streamingContent : ''}
            settings={uiSettings}
          />
        ) : (
          <div className="empty-state">
            <div className="empty-state-icon">🤖</div>
            <h2 className="empty-state-title">Select a task to begin</h2>
            <p className="empty-state-text">Choose a task from the sidebar to view its conversation and provide feedback to the agent.</p>
          </div>
        )}
        {showSettings && (
          <SettingsPanel
            settings={uiSettings}
            onSettingsChange={setUISettings}
            onClose={() => setShowSettings(false)}
          />
        )}
      </main>
      {uiSettings.showActivityLog && (
        <ActivityLog
          entries={logEntries}
          onClose={() => setUISettings(prev => ({ ...prev, showActivityLog: false }))}
        />
      )}
    </div>
  )
}

export default App
