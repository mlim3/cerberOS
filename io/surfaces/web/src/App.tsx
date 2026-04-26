import { useState, useCallback, useEffect, useRef } from 'react'
import type {
  Task,
  ChatMessage,
  CredentialRequest,
  CredentialRequestStatus,
  OrchestratorStreamEvent,
  PlanPreview,
  PlanDecisionStatus,
} from '@cerberos/io-core'
import TaskSidebar from './components/TaskSidebar'
import ChatWindow from './components/ChatWindow'
import CredentialModal from './components/CredentialModal'
import PlanPreviewCard from './components/PlanPreviewCard'
import SettingsButton from './components/SettingsButton'
import SettingsPanel, { defaultUISettings } from './components/SettingsPanel'
import type { UISettings } from './components/SettingsPanel'
import ActivityLog from './components/ActivityLog'
import type { LogEntry } from './components/ActivityLog'
import {
  streamOrchestratorReply,
  submitCredential,
  submitPlanDecision,
  subscribeOrchestratorTaskStream,
  orchestratorSseEnabled,
  formatExpectedNextInput,
  buildApiUrl,
} from './api/orchestrator'
import { createWebSurface, type SurfaceAdapter } from './surface'
import './App.css'

const DEMO_MODE = import.meta.env.VITE_DEMO_MODE === 'true'
const UI_USER_ID = (import.meta.env.VITE_IO_USER_ID as string | undefined) ?? '00000000-0000-0000-0000-000000000001'

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

// Re-export for backward compatibility with components importing from App
export type { Task, ChatMessage } from '@cerberos/io-core'

/** @deprecated Use ChatMessage from @cerberos/io-core */
export type Message = ChatMessage

type UITask = Task & {
  currentTaskId?: string
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

/** When SSE is unavailable, keep the midterm demo credential flow for task 13. */
const FALLBACK_TASK_13_CREDENTIAL: CredentialRequest = {
  taskId: '13',
  requestId: 'cred-13-dbpwd',
  userId: '00000000-0000-0000-0000-000000000001',
  keyName: 'prod_db_admin_password',
  label: 'Production database admin password',
  description: 'Required to execute the migration on the production cluster.',
}

const mockTasks: UITask[] = [
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
    id: '13',
    title: 'Deploy to production DB',
    status: 'awaiting_feedback',
    lastUpdate: 'Need database credentials to proceed',
    expectedNextInput: 'Now',
    messages: [
      { id: '13a', role: 'user', content: 'Deploy the new schema to the production database', timestamp: '10:45 AM' },
      { id: '13b', role: 'agent', content: 'I\'ve prepared the migration script and validated it against a staging snapshot. To run it on the production cluster I\'ll need the database admin password. A secure credential prompt will appear below — the password will be transmitted through an isolated channel and will not be logged or stored in this conversation.', timestamp: '10:46 AM' },
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

type ConversationSummary = {
  conversationId: string
  title: string
  updatedAt: string
  lastMessagePreview?: string
  latestTaskId?: string
  latestTaskStatus?: string
}

function taskFromConversation(c: ConversationSummary): UITask {
  return {
    id: c.conversationId,
    currentTaskId: c.latestTaskId,
    title: c.title || 'New Conversation',
    status: c.latestTaskStatus === 'working' || c.latestTaskStatus === 'awaiting_feedback'
      ? c.latestTaskStatus
      : 'completed',
    lastUpdate: c.lastMessagePreview || 'Open the conversation to continue.',
    expectedNextInput: 'Done',
    messages: [],
  }
}

function deriveConversationTitle(task: Pick<UITask, 'title' | 'messages'>, userContent: string): string {
  if (task.title === 'New Task' && task.messages.length === 0) {
    return userContent.length > 60 ? `${userContent.slice(0, 57)}…` : userContent
  }
  return task.title
}

function App() {
  const [tasks, setTasks] = useState<UITask[]>(DEMO_MODE ? mockTasks : [])
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(
    DEMO_MODE ? mockTasks[0].id : null
  )
  const [showSettings, setShowSettings] = useState(false)
  const [streamingForTaskId, setStreamingForTaskId] = useState<string | null>(null)
  const [streamingContent, setStreamingContent] = useState('')
  const [uiSettings, setUISettings] = useState<UISettings>(loadSettings)
  const [logEntries, setLogEntries] = useState<LogEntry[]>([])
  const heartbeatIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const [taskHeartbeats, setTaskHeartbeats] = useState<Record<string, number>>({})
  const taskNextHeartbeatAtRef = useRef<Record<string, number>>({})
  /** When false, semantic heartbeats come from Orchestrator SSE; when true, local mock timers (API down or env). */
  const [useMockHeartbeat, setUseMockHeartbeat] = useState(() => !orchestratorSseEnabled())

  // Credential state — completely separate from the chat pipeline (populated via SSE / orchestrator push)
  const [credentialRequests, setCredentialRequests] = useState<
    Record<string, { request: CredentialRequest; status: CredentialRequestStatus }>
  >({})
  const [showCredentialModal, setShowCredentialModal] = useState(false)
  const [activeCredentialTaskId, setActiveCredentialTaskId] = useState<string | null>(null)

  // Plan-preview state — populated by orchestrator 'plan_preview' SSE events.
  // Key is the user-facing task_id (not the orchestrator_task_ref).
  const [planPreviews, setPlanPreviews] = useState<
    Record<string, { preview: PlanPreview; status: PlanDecisionStatus; error?: string }>
  >({})

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
    if (DEMO_MODE) return
    let cancelled = false

    ;(async () => {
      try {
        const res = await fetch(buildApiUrl(`/api/conversations?userId=${encodeURIComponent(UI_USER_ID)}`), {
          headers: { 'X-User-Id': UI_USER_ID },
        })
        if (!res.ok) return
        const json = await res.json() as { conversations?: ConversationSummary[] }
        const conversations = json.conversations ?? []
        if (cancelled) return
        const loadedTasks = conversations.map(taskFromConversation)
        setTasks(prev => {
          const existing = new Map(prev.map(task => [task.id, task]))
          for (const task of loadedTasks) {
            if (!existing.has(task.id)) {
              existing.set(task.id, task)
            }
          }
          return Array.from(existing.values()).sort((a, b) => a.id.localeCompare(b.id)).reverse()
        })
      } catch {
        // ignore bootstrap failures; the app can still create new tasks
      }
    })()

    return () => {
      cancelled = true
    }
  }, [DEMO_MODE])

  useEffect(() => {
    if (DEMO_MODE) return
    if (!selectedTaskId) return
    const task = tasks.find(t => t.id === selectedTaskId)
    if (!task || task.messages.length > 0) return
    if (!task.id) return

    let cancelled = false
    ;(async () => {
      try {
        const res = await fetch(
          buildApiUrl(`/api/conversations/${encodeURIComponent(task.id)}/logs?userId=${encodeURIComponent(UI_USER_ID)}`),
          { headers: { 'X-User-Id': UI_USER_ID } },
        )
        if (!res.ok) return
        const json = await res.json() as { logs?: Array<{ role: 'user' | 'orchestrator'; content: string; at: string }> }
        if (cancelled) return
        const messages: ChatMessage[] = (json.logs ?? []).map((log, index) => ({
          id: `${selectedTaskId}-${index}`,
          role: log.role === 'user' ? 'user' : 'agent',
          content: log.content,
          timestamp: new Date(log.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
        }))
        setTasks(prev =>
          prev.map(t => (t.id === selectedTaskId ? { ...t, messages } : t))
        )
      } catch {
        // ignore load failures for now
      }
    })()

    return () => {
      cancelled = true
    }
  }, [DEMO_MODE, selectedTaskId])

  // Orchestrator → IO push stream (SSE) for status + credential_request
  useEffect(() => {
    if (!orchestratorSseEnabled()) return

    let cancelled = false

    const onEvent = (ev: OrchestratorStreamEvent) => {
      if (cancelled) return
      if (ev.type === 'status') {
        const p = ev.payload
        setTasks(prev =>
          prev.map(t =>
            t.currentTaskId === p.taskId
              ? {
                  ...t,
                  status: p.status,
                  lastUpdate: p.lastUpdate,
                  expectedNextInput: formatExpectedNextInput(p.expectedNextInputMinutes),
                }
              : t,
          ),
        )
        setTaskHeartbeats(prev => ({ ...prev, [p.taskId]: Date.now() }))
      } else if (ev.type === 'credential_request') {
        setCredentialRequests(prev => {
          const ex = prev[ev.payload.taskId]
          if (
            ex?.status === 'submitted' &&
            ex.request.requestId === ev.payload.requestId
          ) {
            return prev
          }
          return {
            ...prev,
            [ev.payload.taskId]: { request: ev.payload, status: 'pending' },
          }
        })
      } else if (ev.type === 'plan_preview') {
        const p = ev.payload
        setPlanPreviews(prev => ({
          ...prev,
          [p.taskId]: { preview: p, status: 'pending' },
        }))
      }
    }

    const activeTaskId = tasks.find(t => t.id === selectedTaskId)?.currentTaskId
    if (!activeTaskId) return
    const unsub = subscribeOrchestratorTaskStream(activeTaskId, {
      onOpen: () => {
        if (!cancelled) setUseMockHeartbeat(false)
      },
      onEvent,
      onTransportError: () => {
        if (!cancelled) setUseMockHeartbeat(true)
      },
    })

    return () => {
      cancelled = true
      unsub()
    }
  }, [selectedTaskId])

  // Offline / API-down: still show credential demo for task 13 (matches IO API demo push)
  useEffect(() => {
    if (!DEMO_MODE) return
    if (!useMockHeartbeat) return
    if (selectedTaskId !== '13') return
    setCredentialRequests(prev => {
      if (prev['13']) return prev
      return {
        ...prev,
        '13': { request: FALLBACK_TASK_13_CREDENTIAL, status: 'pending' },
      }
    })
  }, [DEMO_MODE, useMockHeartbeat, selectedTaskId])

  // Refs to hold callbacks for SurfaceAdapter integration
  const tasksRef = useRef(tasks)
  const uiSettingsRef = useRef(uiSettings)
  const addLogEntryRef = useRef(addLogEntry)

  // Keep refs updated
  tasksRef.current = tasks
  uiSettingsRef.current = uiSettings

  // This needs to be after addLogEntry is defined
  useEffect(() => {
    addLogEntryRef.current = addLogEntry
  }, [addLogEntry])

  // Initialize SurfaceAdapter for orchestrator integration
  // This does NOT change existing React behavior - it's an optional bridge
  useEffect(() => {
    const surface = createWebSurface({ surfaceId: 'cerberos-web-dashboard' })

    // Create wrapper that has access to current state
    const handleSendMessageWrapper = async (conversationId: string, userContent: string) => {
      const currentTasks = tasksRef.current
      const task = currentTasks.find(t => t.id === conversationId)
      if (!task) return
      const conversationTitle = deriveConversationTitle(task, userContent)
      let activeTaskId = task.currentTaskId
      try {
        const res = await fetch(buildApiUrl('/api/tasks'), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-User-Id': UI_USER_ID },
          body: JSON.stringify({
            userId: UI_USER_ID,
            conversationId,
            title: conversationTitle,
            inputSummary: userContent,
          }),
        })
        if (res.ok) {
          const data = await res.json() as { taskId?: string }
          if (data.taskId) {
            activeTaskId = data.taskId
            setTasks(prev =>
              prev.map(t => (t.id === conversationId ? { ...t, currentTaskId: activeTaskId } : t)),
            )
          }
        }
      } catch {
        // keep prior task id in demo/offline mode
      }
      const userMsg: ChatMessage = {
        id: nextId(),
        role: 'user',
        content: userContent,
        timestamp: timeLabel(),
      }
      setTasks(prev =>
        prev.map(t =>
          t.id === conversationId
            ? { ...t, title: conversationTitle, messages: [...t.messages, userMsg] }
            : t
        )
      )

      const settings = uiSettingsRef.current
      if (settings.showActivityLog) {
        addLogEntryRef.current?.({
          type: 'user_message',
          taskId: activeTaskId ?? conversationId,
          taskTitle: task.title.slice(0, 20),
          message: `User: "${userContent.slice(0, 50)}${userContent.length > 50 ? '…' : ''}"`,
        })
      }

      setStreamingForTaskId(conversationId)
      setStreamingContent('')
      const history = task.messages.map(m => ({
        role: m.role as 'user' | 'assistant',
        content: m.content,
      }))
      let full = ''
      try {
        if (!activeTaskId) return
        for await (const chunk of streamOrchestratorReply(
          activeTaskId,
          conversationId,
          UI_USER_ID,
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
          const agentMsg: ChatMessage = {
            id: nextId(),
            role: 'agent',
            content: full,
            timestamp: timeLabel(),
          }
          setTasks(prev =>
            prev.map(t =>
              t.id === conversationId ? { ...t, messages: [...t.messages, agentMsg] } : t
            )
          )

          if (settings.showActivityLog) {
            addLogEntryRef.current?.({
              type: 'agent_response',
              taskId: activeTaskId ?? conversationId,
              taskTitle: task.title.slice(0, 20),
              message: `Agent replied (${full.length} chars)`,
            })
          }
        }
      }
    }

    // Register React callbacks with the adapter
    // The adapter can now be used by external orchestrator code
    surface.registerTaskCallbacks({
      onSelectTask: setSelectedTaskId,
      onSendMessage: handleSendMessageWrapper,
    })

    // Also expose the tasks for the adapter to read
    surface.updateTasks(tasks)

    // Keep tasks in sync
    const unsubscribe = surface.onStatusUpdate((update) => {
      // When status updates come in, we could update tasks here
      void update
    })

    // Expose the adapter globally for orchestrator integration
    // @ts-expect-error - Adding to window for external access
    window.__cerberosSurface = surface

    return () => {
      unsubscribe()
      surface.shutdown()
      // @ts-expect-error
      delete window.__cerberosSurface
    }
  }, []) // Empty deps - only runs once on mount

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

    if (!useMockHeartbeat) {
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
  }, [uiSettings.showActivityLog, tasks, addLogEntry, useMockHeartbeat])

  const handleCreateTask = async () => {
    let newConversationId: string

    try {
      const res = await fetch(buildApiUrl('/api/conversations'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: 'New Conversation', userId: UI_USER_ID }),
      })
      const data = await res.json()
      newConversationId = data.conversationId
    } catch {
      // API unreachable — fall back to local ID
      newConversationId = nextId()
    }

    const newTask: Task = {
      id: newConversationId,
      title: 'New Task',
      status: 'awaiting_feedback',
      lastUpdate: 'Describe what you want the agent to work on.',
      expectedNextInput: 'Now',
      messages: [],
    }

    setTasks(prev => [newTask, ...prev])
    setSelectedTaskId(newConversationId)

    if (uiSettings.showActivityLog) {
      addLogEntry({
        type: 'status_change',
        taskId: newConversationId,
        taskTitle: newTask.title.slice(0, 20),
        message: 'New task created. Awaiting your description.',
      })
    }
  }

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

  // Keep surface adapter synced with tasks
  useEffect(() => {
    // @ts-expect-error - window property for external access
    const surface = window.__cerberosSurface as SurfaceAdapter | undefined
    if (surface && 'updateTasks' in surface) {
      // @ts-expect-error - WebSurfaceAdapter has updateTasks
      surface.updateTasks(tasks)
    }
  }, [tasks])

  const onSendMessage = useCallback(
    async (conversationId: string, userContent: string) => {
      const task = tasks.find(t => t.id === conversationId)
      if (!task) return
      const conversationTitle = deriveConversationTitle(task, userContent)
      let activeTaskId = task.currentTaskId
      try {
        const res = await fetch(buildApiUrl('/api/tasks'), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-User-Id': UI_USER_ID },
          body: JSON.stringify({
            userId: UI_USER_ID,
            conversationId,
            title: conversationTitle,
            inputSummary: userContent,
          }),
        })
        if (res.ok) {
          const data = await res.json() as { taskId?: string }
          if (data.taskId) {
            activeTaskId = data.taskId
            setTasks(prev =>
              prev.map(t => (t.id === conversationId ? { ...t, currentTaskId: activeTaskId } : t)),
            )
          }
        }
      } catch {
        // keep prior task id in demo/offline mode
      }
      const userMsg: ChatMessage = {
        id: nextId(),
        role: 'user',
        content: userContent,
        timestamp: timeLabel(),
      }
      setTasks(prev =>
        prev.map(t =>
          t.id === conversationId
            ? { ...t, title: conversationTitle, messages: [...t.messages, userMsg] }
            : t
        )
      )

      if (uiSettings.showActivityLog) {
        addLogEntry({
          type: 'user_message',
          taskId: activeTaskId ?? conversationId,
          taskTitle: task.title.slice(0, 20),
          message: `User: "${userContent.slice(0, 50)}${userContent.length > 50 ? '…' : ''}"`,
        })
      }

      setStreamingForTaskId(conversationId)
      setStreamingContent('')
      const history = task.messages.map(m => ({
        role: m.role as 'user' | 'assistant',
        content: m.content,
      }))
      let full = ''
      try {
        if (!activeTaskId) return
        for await (const chunk of streamOrchestratorReply(
          activeTaskId,
          conversationId,
          UI_USER_ID,
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
          const agentMsg: ChatMessage = {
            id: nextId(),
            role: 'agent',
            content: full,
            timestamp: timeLabel(),
          }
          setTasks(prev =>
            prev.map(t =>
              t.id === conversationId ? { ...t, messages: [...t.messages, agentMsg] } : t
            )
          )

          if (uiSettings.showActivityLog) {
            addLogEntry({
              type: 'agent_response',
              taskId: activeTaskId ?? conversationId,
              taskTitle: task.title.slice(0, 20),
              message: `Agent replied (${full.length} chars)`,
            })
          }
        }
      }
    },
    [tasks, uiSettings.showActivityLog, addLogEntry]
  )

  // ── Credential handlers (completely separate from chat) ──

  const selectedTaskCredential = selectedTask
    ? selectedTask.currentTaskId
      ? credentialRequests[selectedTask.currentTaskId] ?? null
      : null
    : null

  const handleOpenCredentialModal = useCallback(() => {
    if (!selectedTask?.currentTaskId) return
    setActiveCredentialTaskId(selectedTask.currentTaskId)
    setShowCredentialModal(true)
  }, [selectedTask])

  const handleCredentialSubmit = useCallback(
    async (requestId: string, _credential: string) => {
      const taskId = activeCredentialTaskId
      if (!taskId) return

      const credRequest = credentialRequests[taskId]?.request
      if (!credRequest) return

      setCredentialRequests(prev => ({
        ...prev,
        [taskId]: { ...prev[taskId], status: 'submitting' },
      }))

      const result = await submitCredential({
        taskId,
        requestId,
        userId: credRequest.userId,
        keyName: credRequest.keyName,
        value: _credential,
      })

      if (result.ok) {
        setCredentialRequests(prev => ({
          ...prev,
          [taskId]: { ...prev[taskId], status: 'submitted' },
        }))

        // Add a system-level message (no content leaked)
        const sysMsg: ChatMessage = {
          id: nextId(),
          role: 'user',
          content: 'Credential provided securely via isolated channel',
          timestamp: timeLabel(),
          isRedacted: true,
        }
        setTasks(prev =>
          prev.map(t =>
            t.currentTaskId === taskId ? { ...t, messages: [...t.messages, sysMsg] } : t
          )
        )

        if (uiSettings.showActivityLog) {
          addLogEntry({
            type: 'status_change',
            taskId,
            taskTitle: tasks.find(t => t.currentTaskId === taskId)?.title.slice(0, 20) ?? '',
            message: 'Credential submitted through secure channel (content not logged)',
          })
        }

        // Simulate the orchestrator acknowledging receipt after a short delay
        setTimeout(() => {
          const ackMsg: ChatMessage = {
            id: nextId(),
            role: 'agent',
            content: 'Credentials received securely. Running the migration now — I\'ll update you when it completes.',
            timestamp: timeLabel(),
          }
          setTasks(prev =>
            prev.map(t =>
              t.currentTaskId === taskId
                ? {
                    ...t,
                    status: 'working',
                    lastUpdate: 'Running production migration…',
                    expectedNextInput: '~3 min',
                    messages: [...t.messages, ackMsg],
                  }
                : t
            )
          )
        }, 800)
      } else {
        setCredentialRequests(prev => ({
          ...prev,
          [taskId]: { ...prev[taskId], status: 'error' },
        }))
      }

      setShowCredentialModal(false)
      setActiveCredentialTaskId(null)
    },
    [activeCredentialTaskId, credentialRequests, tasks, uiSettings.showActivityLog, addLogEntry]
  )

  const handleCredentialCancel = useCallback(() => {
    setShowCredentialModal(false)
    setActiveCredentialTaskId(null)
  }, [])

  const handlePlanDecision = useCallback(
    async (taskId: string, approved: boolean, reason?: string) => {
      const entry = planPreviews[taskId]
      if (!entry) return
      setPlanPreviews(prev => ({
        ...prev,
        [taskId]: { ...prev[taskId], status: 'submitting', error: undefined },
      }))
      const result = await submitPlanDecision({
        taskId,
        orchestratorTaskRef: entry.preview.orchestratorTaskRef,
        approved,
        reason,
      })
      setPlanPreviews(prev => ({
        ...prev,
        [taskId]: {
          ...prev[taskId],
          status: result.ok ? (approved ? 'approved' : 'rejected') : 'error',
          error: result.ok ? undefined : result.error,
        },
      }))
    },
    [planPreviews],
  )

  const selectedPlanPreview =
    selectedTask?.currentTaskId && planPreviews[selectedTask.currentTaskId]
      ? planPreviews[selectedTask.currentTaskId]
      : null

  return (
    <div className="app">
      <TaskSidebar
        tasks={tasks}
        selectedTaskId={selectedTaskId}
        onSelectTask={setSelectedTaskId}
        settings={uiSettings}
        taskHeartbeats={taskHeartbeats}
        onCreateTask={handleCreateTask}
      />
      <main className="main-content">
        <header className="header">
          <div className="header-task-info">
            <h1 className="header-title">
              {selectedTask?.title ?? ''}
            </h1>
            {selectedTask && (
              <div className="header-meta">
                <span className={`header-status-dot ${selectedTask.status}`}></span>
                <span className="header-status-text">
                  {selectedTask.status === 'awaiting_feedback' && 'Awaiting feedback'}
                  {selectedTask.status === 'working' && 'Working'}
                  {selectedTask.status === 'completed' && 'Completed'}
                </span>
              </div>
            )}
          </div>
          <SettingsButton
            isOpen={showSettings}
            onToggle={() => setShowSettings(!showSettings)}
          />
        </header>
        {selectedTask ? (
          <>
            {selectedPlanPreview && (
              <PlanPreviewCard
                preview={selectedPlanPreview.preview}
                status={selectedPlanPreview.status}
                error={selectedPlanPreview.error}
                onApprove={() => selectedTask.currentTaskId && handlePlanDecision(selectedTask.currentTaskId, true)}
                onReject={reason => selectedTask.currentTaskId && handlePlanDecision(selectedTask.currentTaskId, false, reason)}
              />
            )}
            <ChatWindow
              task={selectedTask}
              onSendMessage={onSendMessage}
              isStreaming={streamingForTaskId === selectedTask.id}
              streamingContent={streamingForTaskId === selectedTask.id ? streamingContent : ''}
              settings={uiSettings}
              credentialRequest={selectedTaskCredential?.request}
              credentialStatus={selectedTaskCredential?.status}
              onProvideCredential={handleOpenCredentialModal}
            />
          </>
        ) : (
          <div className="empty-state">
            <pre className="empty-state-ascii">{`
  ██████╗███████╗██████╗ ██████╗ ███████╗██████╗  ██████╗ ███████╗
 ██╔════╝██╔════╝██╔══██╗██╔══██╗██╔════╝██╔══██╗██╔═══██╗██╔════╝
 ██║     █████╗  ██████╔╝██████╔╝█████╗  ██████╔╝██║   ██║███████╗
 ██║     ██╔══╝  ██╔══██╗██╔══██╗██╔══╝  ██╔══██╗██║   ██║╚════██║
 ╚██████╗███████╗██║  ██║██████╔╝███████╗██║  ██║╚██████╔╝███████║
  ╚═════╝╚══════╝╚═╝  ╚═╝╚═════╝ ╚══════╝╚═╝  ╚═╝ ╚═════╝ ╚══════╝`}</pre>
            <h2 className="empty-state-title">Create a new task to begin</h2>
            <p className="empty-state-text">Press "Create New Task" in the sidebar to start working with the agent.</p>
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
      {showCredentialModal && activeCredentialTaskId && credentialRequests[activeCredentialTaskId] && (
        <CredentialModal
          request={credentialRequests[activeCredentialTaskId].request}
          onSubmit={handleCredentialSubmit}
          onCancel={handleCredentialCancel}
          isSubmitting={credentialRequests[activeCredentialTaskId].status === 'submitting'}
        />
      )}
    </div>
  )
}

export default App
