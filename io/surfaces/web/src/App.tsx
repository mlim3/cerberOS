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
import TaskSidebar, { type SidebarPrimaryTab } from './components/TaskSidebar'
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
import {
  createUserCronJob,
  deleteUserCronJob,
  fetchUserCronJobs,
  type UserCronJob,
} from './api/userCrons'
import { createWebSurface, type SurfaceAdapter } from './surface'
import { parseFirstRunAt, parseRhythmReply } from './recurring/parseRhythm'
import { extractPromptBodyForCron, looksLikeRepeatingSchedulingIntent } from './recurring/detectIntent'
import './App.css'

const DEMO_MODE = import.meta.env.VITE_DEMO_MODE === 'true'
const UI_USER_ID = (import.meta.env.VITE_IO_USER_ID as string | undefined) ?? '00000000-0000-0000-0000-000000000001'

/** Mirrors `scheduled-run-mirror` bucket title — omit from sidebar (not user-authored threads). */
const MEMORY_ORCHESTRATOR_FALLBACK_CONVERSATION_TITLE = 'Scheduled task runs'

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

/** Matches Memory-created conversation/task ids (UUID); offline-only tasks use short nextId() strings. */
function isLikelyPersistedConversationId(id: string): boolean {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(
    id.trim(),
  )
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

type RecurringSetupState =
  | null
  | { conversationId: string; step: 'prompt' }
  | { conversationId: string; step: 'rhythm'; rawInput: string }
  | {
      conversationId: string
      step: 'first_run'
      rawInput: string
      scheduleKind: 'interval' | 'cron'
      intervalSeconds: number
      cronExpression: string
      timeZone: string
    }

const RECURRING_CHAT_INTRO =
  '**Recurring task setup**\n\nDescribe what you want the agent to do **each time this schedule runs**, then reply here with **how often** and **when it should first run**. We\'ll prompt you step by step.\n\nSend **your task** as your **next message**.'

const RECURRING_AFTER_PROMPT_AGENT =
  '**How often should this repeat?**\n\nYou can reply in plain words or with cron:\n• `every hour`, `every 15 minutes`, `every 900 seconds` (minimum **60** seconds)\n• `daily at 9am` or `daily at 6:30pm` *(uses your device time zone)*\n• Cron: `0 7 * * * America/Los_Angeles` *(minute hour day month weekday — optional TZ at end)*'

const RECURRING_FIRST_RUN_AGENT =
  '**When should the first run happen?**\n\nSend local time as **`2026-05-03 14:30`** or datetime-local style **`2026-05-03T14:30`**. It must be a few minutes or more in the future so the scheduler can pick it up.'

function rhythmRetryMessage(reason: string): string {
  if (reason === 'interval_below_minute') {
    return '**Intervals must be at least 60 seconds.** Try `every hour`, `every 120 seconds`, or a cron line like `0 * * * * UTC`.'
  }
  if (reason === 'bad_interval') {
    return '**That number wasn’t usable as an interval.** Try `every 5 minutes`, `daily at 9am`, or cron `15 * * * * UTC`.'
  }
  return '**I didn’t catch that rhythm.** Try `hourly`, `daily at 7pm`, `every 300 seconds`, or a full cron like `15 9 * * * America/New_York`.'
}

function firstRunRetryMessage(): string {
  return '**I couldn’t read that time.** Use `YYYY-MM-DD HH:MM` (24h) or paste a clear datetime; pick a moment at least a few minutes from now.'
}

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

function fingerprintLogs(logs: Array<{ at?: string }>): string {
  if (logs.length === 0) return '0:'
  const lastAt = logs[logs.length - 1]?.at ?? ''
  return `${logs.length}:${lastAt}`
}

function defaultCronName(raw: string): string {
  const line = raw.trim().split('\n')[0] ?? ''
  if (!line.length) return 'Recurring task'
  return line.length > 80 ? `${line.slice(0, 77)}…` : line
}

function scheduleLabelParts(
  kind: 'interval' | 'cron',
  intervalSeconds: number,
  cronExpr: string,
  timeZone: string,
): string {
  return kind === 'cron' ? `cron ${cronExpr.trim()} (${timeZone})` : `every ${intervalSeconds}s`
}

function deriveConversationTitle(task: Pick<UITask, 'title' | 'messages'>, userContent: string): string {
  const isFreshTitle =
    (task.title === 'New Task' || task.title === 'New recurring task') && task.messages.length <= 1
  if (isFreshTitle) {
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
  const [sidebarPrimaryTab, setSidebarPrimaryTab] = useState<SidebarPrimaryTab>('tasks')
  const [userCronJobs, setUserCronJobs] = useState<UserCronJob[]>([])
  const [userCronListError, setUserCronListError] = useState<string | null>(null)
  const [userCronListLoading, setUserCronListLoading] = useState(false)
  const [recurringSetup, setRecurringSetup] = useState<RecurringSetupState>(null)
  const [cronContextJob, setCronContextJob] = useState<UserCronJob | null>(null)
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

  /** Fingerprint seen when conversation was focused / opened — detects new mirrored cron turns */
  const readLogFingerprintsRef = useRef<Record<string, string>>({})
  const [cronThreadUnreadIds, setCronThreadUnreadIds] = useState<Record<string, true>>({})
  /** Last row from server transcript while viewing this chat — for “new pulse” styling */
  const [liveLogPulseKeyByTask, setLiveLogPulseKeyByTask] = useState<Record<string, string>>({})

  const selectedTaskIdRef = useRef(selectedTaskId)
  selectedTaskIdRef.current = selectedTaskId

  const consumeRecurringUserTurnRef = useRef<(cid: string, text: string) => Promise<boolean>>(async () => false)

  const tryEmbeddedSchedulingIntentRef = useRef(
    (_cid: string, _text: string, _task: UITask): boolean => false,
  )

  const createConversationBusyRef = useRef(false)

  const selectedConversationSidebarPreview =
    DEMO_MODE || !selectedTaskId ? '' : tasks.find(t => t.id === selectedTaskId)?.lastUpdate ?? ''

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

  const refetchConversations = useCallback(async () => {
    if (DEMO_MODE) return
    try {
      const res = await fetch(buildApiUrl(`/api/conversations?userId=${encodeURIComponent(UI_USER_ID)}`), {
        headers: { 'X-User-Id': UI_USER_ID },
      })
      if (!res.ok) return
      const json = await res.json() as { conversations?: ConversationSummary[] }
      const conversations = (json.conversations ?? []).filter(c => {
        const ttl = typeof c.title === 'string' ? c.title.trim() : ''
        return ttl !== MEMORY_ORCHESTRATOR_FALLBACK_CONVERSATION_TITLE
      })
      const loadedTasks = conversations.map(taskFromConversation)
      setTasks(prev => {
        const serverIds = new Set(loadedTasks.map(t => t.id))
        /** Server list wins: drop persisted convs that vanished (e.g. after DELETE). */
        const mergedFromApi = loadedTasks.map(task => {
          const prior = prev.find(p => p.id === task.id)
          if (!prior) return task
          return {
            ...prior,
            title: task.title || prior.title,
            status: task.status,
            lastUpdate: task.lastUpdate,
            expectedNextInput: task.expectedNextInput,
            currentTaskId: task.currentTaskId ?? prior.currentTaskId,
          }
        })
        /** Keep optimistic tasks when conversation POST failed — ids are not UUID-shaped. */
        const keptLocalOnly = prev.filter(
          t =>
            !serverIds.has(t.id) &&
            !isLikelyPersistedConversationId(t.id),
        )
        const byId = new Map<string, UITask>()
        for (const t of mergedFromApi) byId.set(t.id, t)
        for (const t of keptLocalOnly) byId.set(t.id, t)
        const merged = Array.from(byId.values()).filter(t => {
          const ttl = (t.title ?? '').trim()
          return ttl !== MEMORY_ORCHESTRATOR_FALLBACK_CONVERSATION_TITLE
        })
        return merged.sort((a, b) => a.id.localeCompare(b.id)).reverse()
      })
    } catch {
      // ignore bootstrap failures; the app can still create new tasks
    }
  }, [DEMO_MODE])

  useEffect(() => {
    if (DEMO_MODE) return
    void refetchConversations()
  }, [DEMO_MODE, refetchConversations])

  useEffect(() => {
    if (DEMO_MODE) return
    const id = setInterval(() => {
      if (document.visibilityState === 'visible') {
        void refetchConversations()
      }
    }, 20_000)
    const onVis = () => {
      if (document.visibilityState === 'visible') void refetchConversations()
    }
    document.addEventListener('visibilitychange', onVis)
    return () => {
      clearInterval(id)
      document.removeEventListener('visibilitychange', onVis)
    }
  }, [DEMO_MODE, refetchConversations])

  const reloadUserCrons = useCallback(async () => {
    setUserCronListLoading(true)
    setUserCronListError(null)
    const result = await fetchUserCronJobs(UI_USER_ID)
    if (result.ok) {
      setUserCronJobs(result.jobs)
    } else {
      setUserCronListError(result.error)
    }
    setUserCronListLoading(false)
  }, [])

  useEffect(() => {
    void reloadUserCrons()
  }, [reloadUserCrons])

  useEffect(() => {
    if (DEMO_MODE) return

    let cancelled = false

    async function fingerprintFor(cid: string): Promise<string | null> {
      try {
        const res = await fetch(
          buildApiUrl(`/api/conversations/${encodeURIComponent(cid)}/logs?userId=${encodeURIComponent(UI_USER_ID)}`),
          { headers: { 'X-User-Id': UI_USER_ID } },
        )
        if (!res.ok) return null
        const json = await res.json() as { logs?: Array<{ at: string }> }
        return fingerprintLogs(json.logs ?? [])
      } catch {
        return null
      }
    }

    const tick = async () => {
      if (cancelled || document.visibilityState !== 'visible') return
      const ids = [
        ...new Set(
          userCronJobs
            .map(j => j.payload?.conversationId)
            .filter((x): x is string => typeof x === 'string' && x.trim().length > 0),
        ),
      ]
      if (ids.length === 0) return
      const seenOpen = selectedTaskIdRef.current
      for (const cid of ids) {
        if (cancelled) break
        if (cid === seenOpen) continue
        const fp = await fingerprintFor(cid)
        if (fp === null || cancelled) continue
        const seen = readLogFingerprintsRef.current[cid]
        if (seen === undefined) {
          readLogFingerprintsRef.current[cid] = fp
          continue
        }
        if (seen !== fp) {
          setCronThreadUnreadIds(prev => ({ ...prev, [cid]: true }))
        }
      }
    }

    const iv = window.setInterval(tick, 22_000)
    void tick()
    return () => {
      cancelled = true
      window.clearInterval(iv)
    }
  }, [userCronJobs, DEMO_MODE])

  const handleSidebarDeleteUserCronJob = useCallback(
    async (jobId: string) => {
      const r = await deleteUserCronJob(UI_USER_ID, jobId)
      if (r.ok) {
        setCronContextJob(prev => (prev?.id === jobId ? null : prev))
        await reloadUserCrons()
      }
      return r
    },
    [reloadUserCrons],
  )

  const handleCloseSettings = useCallback(() => {
    setShowSettings(false)
  }, [])

  const toggleSettings = useCallback(() => {
    setShowSettings(prev => !prev)
  }, [])

  useEffect(() => {
    if (DEMO_MODE) return
    if (!selectedTaskId) return
    const task = tasks.find(t => t.id === selectedTaskId)
    if (!task?.id) return
    if (streamingForTaskId === selectedTaskId) return

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
        const rawLogs = json.logs ?? []
        const fpNew = fingerprintLogs(rawLogs)
        const priorFp = readLogFingerprintsRef.current[selectedTaskId]
        const messages: ChatMessage[] = rawLogs.map((log, index) => {
          const isAgent = log.role !== 'user'
          const scheduledRun = isAgent && log.content.startsWith('Scheduled task:')
          return {
            id: `${selectedTaskId}-log-${index}-${encodeURIComponent(log.at)}`,
            role: isAgent ? 'agent' : 'user',
            content: log.content,
            timestamp: new Date(log.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
            scheduledRun,
          }
        })
        setTasks(prev =>
          prev.map(t => {
            if (t.id !== selectedTaskId) return t
            if (messages.length === 0 && t.messages.length > 0) return t
            return { ...t, messages }
          }),
        )
        if (cancelled || selectedTaskIdRef.current !== selectedTaskId) return
        readLogFingerprintsRef.current[selectedTaskId] = fpNew
        setCronThreadUnreadIds(prev => {
          if (!prev[selectedTaskId]) return prev
          const next = { ...prev }
          delete next[selectedTaskId]
          return next
        })
        const last = messages[messages.length - 1]
        if (
          priorFp !== undefined &&
          priorFp !== fpNew &&
          last?.scheduledRun &&
          last.role === 'agent'
        ) {
          setLiveLogPulseKeyByTask(prev => ({ ...prev, [selectedTaskId]: last.id }))
          window.setTimeout(() => {
            setLiveLogPulseKeyByTask(prev => {
              const copy = { ...prev }
              if (copy[selectedTaskId] === last.id) delete copy[selectedTaskId]
              return copy
            })
          }, 7000)
        }
      } catch {
        // ignore load failures for now
      }
    })()

    return () => {
      cancelled = true
    }
  }, [DEMO_MODE, selectedTaskId, selectedConversationSidebarPreview, streamingForTaskId])

  // Orchestrator → IO push stream (SSE) for status + credential_request
  const activeOrchestratorTaskId = tasks.find(t => t.id === selectedTaskId)?.currentTaskId
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

    if (!activeOrchestratorTaskId) return
    const unsub = subscribeOrchestratorTaskStream(activeOrchestratorTaskId, {
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
  }, [selectedTaskId, activeOrchestratorTaskId])

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
  const recurringSetupRef = useRef(recurringSetup)
  recurringSetupRef.current = recurringSetup

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

      if (await consumeRecurringUserTurnRef.current(conversationId, userContent)) return

      const trimmedSend = userContent.trim()
      if (tryEmbeddedSchedulingIntentRef.current(conversationId, trimmedSend, task)) return

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
      onSelectTask: id => {
        setShowSettings(false)
        setCronContextJob(null)
        setCronThreadUnreadIds(prev => {
          if (!prev[id]) return prev
          const next = { ...prev }
          delete next[id]
          return next
        })
        setSelectedTaskId(id)
      },
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

  const handleDeleteTask = useCallback((id: string) => {
    setTasks(prev => prev.filter(t => t.id !== id))
    setRecurringSetup(prev => (prev?.conversationId === id ? null : prev))
    setCronContextJob(prev => {
      if (!prev?.payload?.conversationId) return prev
      return prev.payload.conversationId === id ? null : prev
    })
    if (selectedTaskId === id) {
      setSelectedTaskId(null)
    }
    if (!DEMO_MODE) {
      void fetch(buildApiUrl(`/api/conversations/${encodeURIComponent(id)}`), {
        method: 'DELETE',
        headers: { 'X-User-Id': UI_USER_ID },
      })
        .then(() => {
          void refetchConversations()
        })
        .catch(() => {
          /* best-effort server delete — refetch clears stale sidebar if DELETE actually succeeded elsewhere */
          void refetchConversations()
        })
    }
  }, [selectedTaskId, DEMO_MODE, refetchConversations])

  const handleRenameTask = useCallback((id: string, title: string) => {
    setTasks(prev => prev.map(t => t.id === id ? { ...t, title } : t))
    if (!DEMO_MODE) {
      fetch(buildApiUrl(`/api/conversations/${encodeURIComponent(id)}`), {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json', 'X-User-Id': UI_USER_ID },
        body: JSON.stringify({ title }),
      }).catch(() => {/* best-effort */})
    }
  }, [DEMO_MODE])

  const handleCreateTask = async () => {
    if (createConversationBusyRef.current) return
    createConversationBusyRef.current = true
    try {
      setShowSettings(false)
      setSidebarPrimaryTab('tasks')
      setRecurringSetup(null)
      setCronContextJob(null)
      let newConversationId: string

      try {
        const res = await fetch(buildApiUrl('/api/conversations'), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ title: 'New Conversation', userId: UI_USER_ID }),
        })
        let parsed: unknown
        try {
          parsed = await res.json()
        } catch {
          parsed = null
        }
        const cid =
          parsed && typeof parsed === 'object' && 'conversationId' in parsed
            ? (parsed as { conversationId?: unknown }).conversationId
            : undefined
        newConversationId =
          res.ok && typeof cid === 'string' && cid.length > 0 ? cid : nextId()
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
    } finally {
      createConversationBusyRef.current = false
    }
  }

  const handleCreateRecurringTask = async () => {
    if (createConversationBusyRef.current) return
    createConversationBusyRef.current = true
    try {
      setShowSettings(false)
      setSidebarPrimaryTab('tasks')
      setCronContextJob(null)
      setRecurringSetup(null)
      let newConversationId: string

      try {
        const res = await fetch(buildApiUrl('/api/conversations'), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ title: 'New recurring task', userId: UI_USER_ID }),
        })
        let parsed: unknown
        try {
          parsed = await res.json()
        } catch {
          parsed = null
        }
        const cid =
          parsed && typeof parsed === 'object' && 'conversationId' in parsed
            ? (parsed as { conversationId?: unknown }).conversationId
            : undefined
        newConversationId =
          res.ok && typeof cid === 'string' && cid.length > 0 ? cid : nextId()
      } catch {
        newConversationId = nextId()
      }

      const introMsg: ChatMessage = {
        id: nextId(),
        role: 'agent',
        content: RECURRING_CHAT_INTRO,
        timestamp: timeLabel(),
      }

      const newTask: UITask = {
        id: newConversationId,
        title: 'New recurring task',
        status: 'awaiting_feedback',
        lastUpdate: 'Describe what should run on the schedule',
        expectedNextInput: 'Your prompt',
        messages: [introMsg],
      }

      setRecurringSetup({ conversationId: newConversationId, step: 'prompt' })
      setTasks(prev => [newTask, ...prev])
      setSelectedTaskId(newConversationId)

      queueMicrotask(() => {
        document.querySelector<HTMLInputElement>('.chat-input')?.focus()
      })

      if (uiSettings.showActivityLog) {
        addLogEntry({
          type: 'status_change',
          taskId: newConversationId,
          taskTitle: newTask.title.slice(0, 20),
          message: 'Recurring task setup — describe the repeating work in chat, then rhythm and first-run time.',
        })
      }
    } finally {
      createConversationBusyRef.current = false
    }
  }

  const finishRecurringFromChat = useCallback(
    (detail: { name: string; rawInput: string; scheduleLabel: string; conversationId: string }) => {
      setRecurringSetup(null)
      const content = `Scheduled task "${detail.name}" (${detail.scheduleLabel}):\n\n${detail.rawInput}`
      const userMsg: ChatMessage = {
        id: nextId(),
        role: 'user',
        content,
        timestamp: timeLabel(),
      }
      const agentMsg: ChatMessage = {
        id: nextId(),
        role: 'agent',
        content: `**Recurring task saved.** It will run **${detail.scheduleLabel}**. Continue chatting anytime.`,
        timestamp: timeLabel(),
      }
      setTasks(prev =>
        prev.map(t =>
          t.id === detail.conversationId
            ? {
                ...t,
                messages: [...t.messages, userMsg, agentMsg],
                lastUpdate: content,
                status: 'awaiting_feedback',
                expectedNextInput: 'Now',
              }
            : t,
        ),
      )
      void refetchConversations()
      void reloadUserCrons()
    },
    [refetchConversations, reloadUserCrons],
  )

  const consumeRecurringUserTurn = useCallback(
    async (conversationId: string, userContent: string): Promise<boolean> => {
      const trimmed = userContent.trim()
      if (!trimmed) return false
      const rs = recurringSetupRef.current
      if (!rs || rs.conversationId !== conversationId) return false

      const currentTasks = tasksRef.current
      const task = currentTasks.find(t => t.id === conversationId)
      if (!task) return false

      const defaultTz =
        typeof Intl !== 'undefined' && Intl.DateTimeFormat
          ? Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
          : 'UTC'

      if (rs.step === 'prompt') {
        const rawStored = extractPromptBodyForCron(trimmed).trim()
        const conversationTitle = deriveConversationTitle(task, rawStored.length >= 14 ? rawStored : trimmed)
        const userMsg: ChatMessage = { id: nextId(), role: 'user', content: trimmed, timestamp: timeLabel() }
        const parsedFromLine = parseRhythmReply(trimmed, defaultTz)

        if (parsedFromLine.ok) {
          const cadenceHint =
            parsedFromLine.scheduleKind === 'interval'
              ? scheduleLabelParts('interval', parsedFromLine.intervalSeconds, '', '')
              : scheduleLabelParts(
                  'cron',
                  0,
                  parsedFromLine.cronExpression,
                  parsedFromLine.timeZone,
                )
          const agentMsg: ChatMessage = {
            id: nextId(),
            role: 'agent',
            content:
              `**Repeating cadence detected** (${cadenceHint}). Anything like **until 9:30 pm** does **not** auto-stop the job yet—pause or delete it when you are done.\n\n` +
              RECURRING_FIRST_RUN_AGENT,
            timestamp: timeLabel(),
          }
          const nextSetup =
            parsedFromLine.scheduleKind === 'interval'
              ? {
                  conversationId,
                  step: 'first_run' as const,
                  rawInput: rawStored,
                  scheduleKind: 'interval' as const,
                  intervalSeconds: parsedFromLine.intervalSeconds,
                  cronExpression: '',
                  timeZone: 'UTC',
                }
              : {
                  conversationId,
                  step: 'first_run' as const,
                  rawInput: rawStored,
                  scheduleKind: 'cron' as const,
                  intervalSeconds: 0,
                  cronExpression: parsedFromLine.cronExpression,
                  timeZone: parsedFromLine.timeZone,
                }
          setRecurringSetup(nextSetup)
          setTasks(prev =>
            prev.map(t =>
              t.id === conversationId
                ? {
                    ...t,
                    title: conversationTitle,
                    messages: [...t.messages, userMsg, agentMsg],
                    lastUpdate: 'First run time',
                    expectedNextInput: 'Date & time',
                  }
                : t,
            ),
          )
          return true
        }

        const agentMsg: ChatMessage = {
          id: nextId(),
          role: 'agent',
          content: RECURRING_AFTER_PROMPT_AGENT,
          timestamp: timeLabel(),
        }
        setTasks(prev =>
          prev.map(t =>
            t.id === conversationId
              ? {
                  ...t,
                  title: conversationTitle,
                  messages: [...t.messages, userMsg, agentMsg],
                  lastUpdate: 'Pick how often',
                  expectedNextInput: 'Rhythm',
                }
              : t,
          ),
        )
        setRecurringSetup({ conversationId, step: 'rhythm', rawInput: rawStored })
        return true
      }

      if (rs.step === 'rhythm') {
        const parsed = parseRhythmReply(trimmed, defaultTz)
        const userMsg: ChatMessage = { id: nextId(), role: 'user', content: trimmed, timestamp: timeLabel() }
        if (!parsed.ok) {
          const agentMsg: ChatMessage = {
            id: nextId(),
            role: 'agent',
            content: rhythmRetryMessage(parsed.reason),
            timestamp: timeLabel(),
          }
          setTasks(prev =>
            prev.map(t =>
              t.id === conversationId ? { ...t, messages: [...t.messages, userMsg, agentMsg] } : t,
            ),
          )
          return true
        }
        const agentMsg: ChatMessage = {
          id: nextId(),
          role: 'agent',
          content: RECURRING_FIRST_RUN_AGENT,
          timestamp: timeLabel(),
        }
        const nextSetup =
          parsed.scheduleKind === 'interval'
            ? {
                conversationId,
                step: 'first_run' as const,
                rawInput: rs.rawInput,
                scheduleKind: 'interval' as const,
                intervalSeconds: parsed.intervalSeconds,
                cronExpression: '',
                timeZone: 'UTC',
              }
            : {
                conversationId,
                step: 'first_run' as const,
                rawInput: rs.rawInput,
                scheduleKind: 'cron' as const,
                intervalSeconds: 0,
                cronExpression: parsed.cronExpression,
                timeZone: parsed.timeZone,
              }
        setRecurringSetup(nextSetup)
        setTasks(prev =>
          prev.map(t =>
            t.id === conversationId
              ? {
                  ...t,
                  messages: [...t.messages, userMsg, agentMsg],
                  lastUpdate: 'First run time',
                  expectedNextInput: 'Date & time',
                }
              : t,
          ),
        )
        return true
      }

      if (rs.step === 'first_run') {
        const parsed = parseFirstRunAt(trimmed)
        const userMsg: ChatMessage = { id: nextId(), role: 'user', content: trimmed, timestamp: timeLabel() }
        if (!parsed.ok) {
          const agentMsg: ChatMessage = {
            id: nextId(),
            role: 'agent',
            content: firstRunRetryMessage(),
            timestamp: timeLabel(),
          }
          setTasks(prev =>
            prev.map(t =>
              t.id === conversationId ? { ...t, messages: [...t.messages, userMsg, agentMsg] } : t,
            ),
          )
          return true
        }

        const name = defaultCronName(rs.rawInput)
        const scheduleLabel = scheduleLabelParts(
          rs.scheduleKind,
          rs.intervalSeconds,
          rs.cronExpression,
          rs.timeZone,
        )

        const result = await createUserCronJob({
          name,
          userId: UI_USER_ID,
          rawInput: rs.rawInput,
          conversationId: rs.conversationId,
          scheduleKind: rs.scheduleKind,
          intervalSeconds: rs.scheduleKind === 'interval' ? rs.intervalSeconds : 0,
          cronExpression: rs.scheduleKind === 'cron' ? rs.cronExpression : '',
          timeZone: rs.scheduleKind === 'cron' ? rs.timeZone : 'UTC',
          nextRunAt: parsed.iso,
        })

        if (!result.ok) {
          const agentMsg: ChatMessage = {
            id: nextId(),
            role: 'agent',
            content: `**Couldn't save schedule:** ${result.error}\n\n${RECURRING_FIRST_RUN_AGENT}`,
            timestamp: timeLabel(),
          }
          setTasks(prev =>
            prev.map(t =>
              t.id === conversationId ? { ...t, messages: [...t.messages, userMsg, agentMsg] } : t,
            ),
          )
          return true
        }

        setTasks(prev =>
          prev.map(t =>
            t.id === conversationId ? { ...t, messages: [...t.messages, userMsg] } : t,
          ),
        )
        finishRecurringFromChat({
          name,
          rawInput: rs.rawInput,
          scheduleLabel,
          conversationId: rs.conversationId,
        })
        return true
      }

      return false
    },
    [finishRecurringFromChat],
  )

  consumeRecurringUserTurnRef.current = consumeRecurringUserTurn

  const tryEmbeddedSchedulingIntent = useCallback(
    (conversationId: string, userContent: string, task: UITask): boolean => {
      const trimmed = userContent.trim()
      if (!trimmed) return false
      if (!looksLikeRepeatingSchedulingIntent(trimmed)) return false

      const rawInput = extractPromptBodyForCron(trimmed).trim()
      if (rawInput.length < 4) return false

      const titleBase = rawInput.length >= 10 ? rawInput : trimmed
      const conversationTitle = deriveConversationTitle(task, titleBase)

      const defaultTz =
        typeof Intl !== 'undefined' && Intl.DateTimeFormat
          ? Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
          : 'UTC'

      /** If rhythm is readable from the same line (“every minute until …”), skip straight to first-run step. */
      const parsedRhythm = parseRhythmReply(trimmed, defaultTz)

      const userMsg: ChatMessage = { id: nextId(), role: 'user', content: trimmed, timestamp: timeLabel() }

      setShowSettings(false)
      setSidebarPrimaryTab('tasks')
      setCronContextJob(null)

      if (parsedRhythm.ok) {
        const nextSetup =
          parsedRhythm.scheduleKind === 'interval'
            ? {
                conversationId,
                step: 'first_run' as const,
                rawInput,
                scheduleKind: 'interval' as const,
                intervalSeconds: parsedRhythm.intervalSeconds,
                cronExpression: '',
                timeZone: 'UTC',
              }
            : {
                conversationId,
                step: 'first_run' as const,
                rawInput,
                scheduleKind: 'cron' as const,
                intervalSeconds: 0,
                cronExpression: parsedRhythm.cronExpression,
                timeZone: parsedRhythm.timeZone,
              }
        const cadenceHint =
          parsedRhythm.scheduleKind === 'interval'
            ? scheduleLabelParts('interval', parsedRhythm.intervalSeconds, '', '')
            : scheduleLabelParts('cron', 0, parsedRhythm.cronExpression, parsedRhythm.timeZone)
        const agentMsg: ChatMessage = {
          id: nextId(),
          role: 'agent',
          content:
            `**Repeating cadence detected** (${cadenceHint}). Anything like **until 9:30 pm** does **not** auto-stop the job yet—pause or delete it when you are done.\n\n` +
            RECURRING_FIRST_RUN_AGENT,
          timestamp: timeLabel(),
        }
        setRecurringSetup(nextSetup)
        setTasks(prev =>
          prev.map(t =>
            t.id === conversationId
              ? {
                  ...t,
                  title: conversationTitle,
                  messages: [...t.messages, userMsg, agentMsg],
                  lastUpdate: 'First run time',
                  expectedNextInput: 'Date & time',
                }
              : t,
          ),
        )
        return true
      }

      const agentMsg: ChatMessage = {
        id: nextId(),
        role: 'agent',
        content:
          '**Sounds like you want this on a repeat.** We’ll configure **how often** and **when it first runs**, then save it as your **user cron** (same thread).\n\n' +
          RECURRING_AFTER_PROMPT_AGENT,
        timestamp: timeLabel(),
      }

      setTasks(prev =>
        prev.map(t =>
          t.id === conversationId
            ? {
                ...t,
                title: conversationTitle,
                messages: [...t.messages, userMsg, agentMsg],
                lastUpdate: 'Schedule: how often?',
                expectedNextInput: 'Rhythm',
              }
            : t,
        ),
      )
      setRecurringSetup({ conversationId, step: 'rhythm', rawInput })
      return true
    },
    [],
  )

  tryEmbeddedSchedulingIntentRef.current = tryEmbeddedSchedulingIntent

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
          const id = tasks[newIndex].id
          setShowSettings(false)
          setCronThreadUnreadIds(prev => {
            if (!prev[id]) return prev
            const next = { ...prev }
            delete next[id]
            return next
          })
          setSelectedTaskId(id)
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

      if (await consumeRecurringUserTurnRef.current(conversationId, userContent)) return

      const trimmedSend = userContent.trim()
      if (tryEmbeddedSchedulingIntentRef.current(conversationId, trimmedSend, task)) return

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

  const cronStripVisible =
    Boolean(cronContextJob &&
      selectedTask &&
      cronContextJob.payload?.conversationId === selectedTask.id)

  const handleSelectTaskFromSidebar = useCallback((id: string) => {
    setShowSettings(false)
    setCronContextJob(null)
    setCronThreadUnreadIds(prev => {
      if (!prev[id]) return prev
      const next = { ...prev }
      delete next[id]
      return next
    })
    setSelectedTaskId(id)
  }, [])

  const handleSelectRecurringJob = useCallback((job: UserCronJob) => {
    setShowSettings(false)
    setCronContextJob(job)
    const cid = job.payload?.conversationId
    if (!cid) {
      setRecurringSetup(null)
      setSelectedTaskId(null)
      return
    }
    setCronThreadUnreadIds(prev => {
      if (!prev[cid]) return prev
      const next = { ...prev }
      delete next[cid]
      return next
    })
    setRecurringSetup(prev => (prev && prev.conversationId !== cid ? null : prev))
    setSelectedTaskId(cid)
  }, [])

  return (
    <div className="app">
      <TaskSidebar
        sidebarTab={sidebarPrimaryTab}
        onSidebarTabChange={setSidebarPrimaryTab}
        tasks={tasks}
        selectedTaskId={selectedTaskId}
        onSelectTask={handleSelectTaskFromSidebar}
        settings={uiSettings}
        taskHeartbeats={taskHeartbeats}
        onCreateTask={handleCreateTask}
        onDeleteTask={handleDeleteTask}
        onRenameTask={handleRenameTask}
        userCronJobs={userCronJobs}
        userCronLoadError={userCronListError}
        userCronListLoading={userCronListLoading}
        onReloadUserCronJobs={reloadUserCrons}
        onDeleteUserCronJob={handleSidebarDeleteUserCronJob}
        onStartRecurringChatFlow={handleCreateRecurringTask}
        onSelectRecurringJob={handleSelectRecurringJob}
        highlightRecurringJobId={cronContextJob?.id ?? null}
        conversationUnreadIds={cronThreadUnreadIds}
      />
      <main className="main-content">
        <header className="header">
          <div className="header-task-info">
            <div className="header-task-primary">
              <h1 className="header-title">
                {selectedTask?.title ??
                  (cronContextJob?.name ?? '')}
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
            {cronStripVisible && cronContextJob && (
              <div className="header-recurring-strip">
                <strong>Scheduled</strong> · next run {new Date(cronContextJob.nextRunAt).toLocaleString()} ·{' '}
                {cronContextJob.scheduleKind === 'cron' ? (
                  <code>
                    {cronContextJob.cronExpression} ({cronContextJob.timeZone || 'UTC'})
                  </code>
                ) : (
                  <span>every {cronContextJob.intervalSeconds}s</span>
                )}
              </div>
            )}
          </div>
          <SettingsButton
            isOpen={showSettings}
            onToggle={toggleSettings}
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
              pulseMessageKey={liveLogPulseKeyByTask[selectedTask.id] ?? undefined}
              inputPlaceholder={
                recurringSetup?.conversationId === selectedTask.id
                  ? recurringSetup.step === 'prompt'
                    ? 'What should the agent run on each schedule?'
                    : recurringSetup.step === 'rhythm'
                      ? 'e.g. every hour · daily at 9am · 0 * * * * UTC'
                      : 'First run · e.g. 2026-05-03 15:45'
                  : undefined
              }
            />
          </>
        ) : cronContextJob ? (
          <div className="empty-state cron-orphan-panel">
            <h2 className="empty-state-title">{cronContextJob.name}</h2>
            <p className="empty-state-text">This repeating task is not tied to an open conversation.</p>
            <p className="cron-orphan-schedule">
              Next run {new Date(cronContextJob.nextRunAt).toLocaleString()}
              {cronContextJob.scheduleKind === 'cron'
                ? ` · ${cronContextJob.cronExpression} (${cronContextJob.timeZone || 'UTC'})`
                : ` · every ${cronContextJob.intervalSeconds}s`}
            </p>
            <button type="button" className="cron-orphan-close" onClick={() => setCronContextJob(null)}>
              Close
            </button>
          </div>
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
            onClose={handleCloseSettings}
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
