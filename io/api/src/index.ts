import './load-env'
import { Hono } from 'hono';
import { cors } from 'hono/cors';
import { serveStatic } from 'hono/bun';
import {
  parseOrchestratorStreamEvent,
  type StatusUpdate,
  type LogEntry,
  type SendMessageRequest,
  type OrchestratorStreamEvent,
  type CredentialRequest,
} from '@cerberos/io-core';
import {
  appendLogEntry,
  createConversation,
  createTask,
  getTask,
  getConversationLogs,
  listConversations,
  deleteConversation,
  renameConversation,
  type MemoryLogEntry,
} from '@cerberos/io-core/memory-client'
import { transcribe, warmupTranscription } from './transcription/runner'
import {
  createNatsClient,
  callbackTopicForTask,
  IO_RESULTS_TOPIC_PREFIX,
  SUBJECT_IO_RESULTS,
} from './nats/client'
import { traceMiddleware } from './trace-context'
import { ioLog, logFromContext } from './logger'
import { startHeartbeatEmitter } from './heartbeat'
import { mirrorMemoryConfigured, persistOrchestratorOutcomeToMemory } from './scheduled-run-mirror'
import { messageLooksLikeUserCronScheduling } from './scheduling-language'

// =============================================================================
// Planner input enrichment
// =============================================================================

/**
 * Build the `raw_input` string that goes into the orchestrator's planner prompt.
 *
 * When a task is a follow-up (e.g. the user continues a COMPLETED task), the
 * current message alone is often ambiguous — "Is it nice living there?" after
 * "What's Texas?" — and the planner LLM returns a conversational clarification
 * instead of a valid execution-plan JSON object, which the orchestrator then
 * rejects with "planner result is not a valid execution plan JSON object".
 *
 * The web UI already sends `conversationHistory` (prior turns + the current
 * user message as the last entry). We prepend a "Conversation so far" block
 * when there is meaningful prior context, so the planner sees what the user
 * is actually referring to. For first-turn messages we keep `raw_input`
 * identical to `content` to avoid changing the planner prompt for existing
 * flows.
 *
 * Length is bounded to protect the planner context window: we keep only the
 * last MAX_TURNS entries prior to the current message, and truncate any
 * individual message to MAX_CHARS_PER_MSG characters.
 */
const MAX_TURNS = 10
const MAX_CHARS_PER_MSG = 1500
function buildRawInputWithHistory(
  content: string,
  conversationHistory?: Array<{ role: 'user' | 'assistant'; content: string }>,
): string {
  if (!conversationHistory || conversationHistory.length === 0) {
    return content
  }
  // The web UI appends the current message to history before calling; drop it
  // for the prior-context block so we don't duplicate the "Current message"
  // line. Fall back to the full history if the last entry doesn't match.
  const last = conversationHistory[conversationHistory.length - 1]
  const prior = (last?.role === 'user' && last.content === content)
    ? conversationHistory.slice(0, -1)
    : conversationHistory
  if (prior.length === 0) {
    return content
  }
  const trimmed = prior.slice(-MAX_TURNS).map(m => {
    const c = m.content.length > MAX_CHARS_PER_MSG
      ? m.content.slice(0, MAX_CHARS_PER_MSG) + ' …[truncated]'
      : m.content
    return `${m.role}: ${c}`
  })
  return [
    'Conversation so far:',
    ...trimmed,
    '',
    'Current message:',
    content,
  ].join('\n')
}

// =============================================================================
// Configuration
// =============================================================================

const DEMO_MODE = process.env.DEMO_MODE === 'true'

// =============================================================================
// NATS client (stub)
// =============================================================================

const natsClient = createNatsClient({
  url: process.env.NATS_URL ?? '',
  credsPath: process.env.NATS_CREDS_PATH,
})
ioLog(
  'info',
  'transport',
  natsClient ? 'using NATS' : 'using HTTP bridge (POST /api/orchestrator/stream-events)',
)

startHeartbeatEmitter(natsClient)

// =============================================================================
// Pending chat responses — POST /api/chat registers here, orchestrator delivers
// =============================================================================

type ChatResponseCallback = {
  push: (content: string) => void
  complete: () => void
  error: (msg: string) => void
}
const pendingChatResponses = new Map<string, ChatResponseCallback>()

const DEFAULT_UI_USER_ID = process.env.IO_DEFAULT_USER_ID ?? '00000000-0000-0000-0000-000000000001'

/** Map key for pending chat — UUID string case must match (e.g. uuidgen vs NATS subject). */
function chatPendingKey(taskId: string): string {
  return taskId.trim().toLowerCase()
}

function requestUserId(c: { req: { header: (name: string) => string | undefined; query: (name: string) => string | undefined } }): string {
  const fromHeader = c.req.header('X-User-Id')
  const fromQuery = c.req.query('userId')
  return fromHeader || fromQuery || DEFAULT_UI_USER_ID
}

function deliverChatResponse(taskId: string, content: string, done: boolean): boolean {
  const key = chatPendingKey(taskId)
  const cb = pendingChatResponses.get(key)
  if (!cb) return false
  cb.push(content)
  if (done) {
    cb.complete()
    pendingChatResponses.delete(key)
  }
  return true
}

function parseEnvelopePayload(env: Record<string, unknown>): Record<string, unknown> {
  const p = env.payload
  if (p == null) return {}
  if (typeof p === 'string') {
    try {
      return JSON.parse(p) as Record<string, unknown>
    } catch {
      return {}
    }
  }
  if (typeof p === 'object') return p as Record<string, unknown>
  return {}
}

/**
 * Extract a clean human-readable string from the orchestrator's task_result payload.
 * The result may be: a plain string, a JSON array of subtask results, or an object.
 */
function extractHumanResult(result: unknown): string {
  if (!result) return 'Task completed.'
  if (typeof result === 'string') {
    try {
      const parsed = JSON.parse(result)
      return extractHumanResult(parsed)
    } catch {
      return result
    }
  }
  if (Array.isArray(result)) {
    const parts = result
      .map((item: Record<string, unknown>) => {
        const r = item.result ?? item.output ?? item.content
        return typeof r === 'string' ? r : r ? JSON.stringify(r) : null
      })
      .filter(Boolean) as string[]
    return parts.length > 0 ? parts.join('\n\n') : 'Task completed.'
  }
  if (typeof result === 'object') {
    const obj = result as Record<string, unknown>
    const text = obj.result ?? obj.output ?? obj.content ?? obj.answer
    if (typeof text === 'string') return text
    if (text) return extractHumanResult(text)
    return JSON.stringify(result)
  }
  return String(result)
}

/**
 * Client task id for `pendingChatResponses` — usually `aegis.io.results.<taskId>` subject suffix.
 * For some payloads (e.g. task_accepted) only orchestrator_task_ref is inside payload; user task id
 * is only reliable from the subject, so we prefer subject when it matches.
 */
function clientTaskIdFromIOResults(subject: string, payload: Record<string, unknown>): string {
  if (subject.startsWith(IO_RESULTS_TOPIC_PREFIX)) {
    return chatPendingKey(subject.slice(IO_RESULTS_TOPIC_PREFIX.length))
  }
  const raw = (payload.task_id as string) || (payload.orchestrator_task_ref as string) || ''
  return raw ? chatPendingKey(raw) : ''
}

/** error_response carries user TaskID in payload — prefer it over subject (subject can differ by broker). */
function pendingKeyFromErrorPayload(payload: Record<string, unknown>, subject: string): string {
  const tid = payload.task_id
  if (tid !== undefined && tid !== null && String(tid).length > 0) {
    return chatPendingKey(String(tid))
  }
  return clientTaskIdFromIOResults(subject, payload)
}

// Subscribe to NATS callback topics for orchestrator responses
if (natsClient) {
  natsClient.subscribe(SUBJECT_IO_RESULTS, (msg: unknown) => {
    const raw = msg as { envelope?: Record<string, unknown>; subject?: string } | undefined
    if (!raw) return
    const envelope = (raw.envelope ?? raw) as Record<string, unknown>
    const subject = typeof raw.subject === 'string' ? raw.subject : ''
    const envelopeTrace =
      envelope && typeof envelope.trace_id === 'string' ? envelope.trace_id.trim() : undefined
    const payload = parseEnvelopePayload(envelope)
    const msgType = envelope.message_type as string | undefined
    const taskId = clientTaskIdFromIOResults(subject, payload)

    if (msgType === 'task_accepted') {
      if (taskId) {
        deliverChatResponse(taskId, 'Task accepted — the orchestrator is working on it.\n', false)
        ioLog('info', 'nats', 'task_accepted', { task_id: taskId })
      }
    } else if (msgType === 'task_result') {
      const result = payload.result as unknown
      const content = extractHumanResult(result)
      if (taskId) {
        const streamed = deliverChatResponse(taskId, content, true)
        ioLog('info', 'nats', 'task_result', { task_id: taskId })
        if (!streamed && !DEMO_MODE && mirrorMemoryConfigured()) {
          const uid =
            typeof payload.user_id === 'string' && payload.user_id.trim()
              ? payload.user_id.trim()
              : DEFAULT_UI_USER_ID
          const cid =
            typeof payload.conversation_id === 'string' && payload.conversation_id.trim()
              ? payload.conversation_id.trim()
              : undefined
          const headline =
            payload.success === false
              ? `Task finished with errors: \`${taskId}\``
              : `Task completed: \`${taskId}\``
          const rawInput =
            typeof payload.raw_input === 'string' && payload.raw_input.trim()
              ? payload.raw_input.trim()
              : ''
          void persistOrchestratorOutcomeToMemory({
            userId: uid,
            taskId,
            payloadConversationId: cid,
            traceId: envelopeTrace,
            contentLines: rawInput
              ? [`Scheduled task: ${rawInput}`, '', headline, '', content]
              : [headline, '', content],
          })
        }
      }
    } else if (msgType === 'error_response') {
      const userMsg = (payload.user_message ?? 'An error occurred.') as string
      const errKey = pendingKeyFromErrorPayload(payload, subject)
      if (errKey) {
        const cb = pendingChatResponses.get(errKey)
        if (cb) {
          cb.error(userMsg)
          pendingChatResponses.delete(errKey)
          ioLog('warn', 'nats', 'error_response', { task_id: errKey, detail: userMsg })
        } else {
          ioLog('warn', 'nats', 'error_response_no_pending_chat', {
            task_id: errKey,
            subject,
            detail: userMsg,
            hint: 'client disconnected before callback or task id key mismatch',
          })
          if (!DEMO_MODE && mirrorMemoryConfigured()) {
            const uid =
              typeof payload.user_id === 'string' && payload.user_id.trim()
                ? payload.user_id.trim()
                : DEFAULT_UI_USER_ID
            const cid =
              typeof payload.conversation_id === 'string' && payload.conversation_id.trim()
                ? payload.conversation_id.trim()
                : undefined
            void persistOrchestratorOutcomeToMemory({
              userId: uid,
              taskId: errKey,
              payloadConversationId: cid,
              traceId: envelopeTrace,
              contentLines: [`Task failed: \`${errKey}\``, '', userMsg],
            })
          }
        }
      }
    }
  })
}

// =============================================================================
// In-memory task state (separate from log persistence)
// =============================================================================

const tasks = new Map<string, StatusUpdate>();

/** Maps MemoryLogEntry (user|assistant) back to LogEntry (user|orchestrator). */
function memoryToLogEntry(m: MemoryLogEntry): LogEntry {
  return {
    taskId: m.taskId ?? '',
    role: m.role === 'assistant' ? 'orchestrator' : 'user',
    content: m.content,
    at: m.createdAt,
  }
}

type SsePush = (bytes: Uint8Array) => void;
const sseClients = new Map<string, Set<SsePush>>();

const text = new TextEncoder();

function subscribeSse(taskId: string, push: SsePush): () => void {
  let set = sseClients.get(taskId);
  if (!set) {
    set = new Set();
    sseClients.set(taskId, set);
  }
  set.add(push);
  return () => {
    set!.delete(push);
    if (set!.size === 0) sseClients.delete(taskId);
  };
}

function broadcastStreamEvent(taskId: string, event: OrchestratorStreamEvent): void {
  const line = `data: ${JSON.stringify(event)}\n\n`;
  const bytes = text.encode(line);
  const set = sseClients.get(taskId);
  if (!set) return;
  for (const push of set) {
    try {
      push(bytes);
    } catch {
      /* client disconnected */
    }
  }
}

function broadcastStatus(taskId: string, status: StatusUpdate): void {
  broadcastStreamEvent(taskId, { type: 'status', payload: status });
}

function persistAndBroadcastStatus(status: StatusUpdate): void {
  tasks.set(status.taskId, status);
  broadcastStatus(status.taskId, status);
}

// =============================================================================
// Demo mode only: mock response generator and demo credential
// =============================================================================

/** Demo credential for local dev when Orchestrator is not wired (task 13 in mock UI). */
const DEMO_TASK_13_CREDENTIAL: CredentialRequest = {
  taskId: '13',
  requestId: 'cred-13-dbpwd',
  userId: '00000000-0000-0000-0000-000000000001',
  keyName: 'prod_db_admin_password',
  label: 'Production database admin password',
  description: 'Required to execute the migration on the production cluster.',
}

// Mock response generator for demo (only active in DEMO_MODE)
async function* generateMockResponse(content: string): AsyncGenerator<string> {
  const responses = [
    "I'm analyzing your request",
    'Processing the information',
    'Looking up relevant data',
    'Formulating a response',
  ];

  for (const response of responses) {
    yield response + '...\n\n';
    await new Promise(r => setTimeout(r, 500));
  }

  yield `Based on your message "${content}", here's what I found:\n\n`;
  await new Promise(r => setTimeout(r, 300));

  yield '• This is a demo response from the IO API server\n';
  yield '• The streaming is working correctly\n';
  yield '• Your message was logged to memory\n';
  yield '\nFeel free to ask more questions!';
}

type AppEnv = {
  Variables: {
    traceId: string
    traceparent: string
    taskId: string
    conversationId: string
  }
}

const app = new Hono<AppEnv>();

// =============================================================================
// Middleware
// =============================================================================

app.use('/*', cors())

app.use('/*', async (c, next) => {
  const path = c.req.path
  if (path === '/health' || path === '/api/health') return next()
  return traceMiddleware(c, next)
})

// =============================================================================
// Health checks (never pass through traceMiddleware — probes must not touch OTLP)
// =============================================================================

app.get('/health', (c) => {
  ioLog('info', 'http', 'GET /health')
  return c.json({ status: 'ok', timestamp: new Date().toISOString() })
})

app.get('/api/health', (c) => {
  ioLog('info', 'http', 'GET /api/health')
  return c.json({ status: 'ok', timestamp: new Date().toISOString() })
})

// =============================================================================
// Status endpoint
// =============================================================================

app.get('/api/status', (c) => {
  logFromContext(c, 'info', 'http', 'GET /api/status')
  const stt = (process.env.STT_PROVIDER ?? 'local').toLowerCase()
  return c.json({
    io_api: 'ok',
    demo_mode: DEMO_MODE,
    orchestrator: natsClient?.connected ? 'connected' : 'disconnected',
    memory: (process.env.MEMORY_API_BASE ?? '').trim() ? 'configured' : 'disconnected',
    nats: natsClient ? 'configured' : 'disconnected',
    web_dashboard: process.env.NODE_ENV === 'production' ? 'serving' : 'dev',
    voice_enabled: stt !== 'disabled' && stt !== 'off' && stt !== 'none',
  })
});

// =============================================================================
// Conversation / Task endpoints
// =============================================================================

// Get all tasks
app.get('/api/tasks', (c) => {
  logFromContext(c, 'info', 'http', 'GET /api/tasks')
  const taskList = Array.from(tasks.values());
  return c.json({ tasks: taskList });
});

// Get status for a specific task
app.get('/api/tasks/:taskId', (c) => {
  const taskId = c.req.param('taskId');
  c.set('taskId', taskId)
  logFromContext(c, 'info', 'http', 'GET /api/tasks/:taskId')
  const task = tasks.get(taskId);
  if (!task) {
    return c.json({ error: 'Task not found' }, 404);
  }
  return c.json(task);
});

app.post('/api/conversations', async (c) => {
  const { title, userId } = await c.req.json()
  const effectiveUserId = typeof userId === 'string' && userId ? userId : requestUserId(c)
  const conversation = await createConversation({
    userId: effectiveUserId,
    title: typeof title === 'string' ? title : undefined,
  })
  if (!conversation) {
    return c.json({ error: 'Failed to create conversation' }, 502)
  }
  return c.json({
    conversationId: conversation.conversationId,
    title: conversation.title,
    status: 'created',
  })
})

// Create a new task
app.post('/api/tasks', async (c) => {
  const body = await c.req.json()
  const userId = typeof body.userId === 'string' && body.userId ? body.userId : requestUserId(c)
  const conversationId = typeof body.conversationId === 'string' && body.conversationId ? body.conversationId : undefined
  const title = typeof body.title === 'string' ? body.title : undefined
  const inputSummary = typeof body.inputSummary === 'string' ? body.inputSummary : undefined
  const status = typeof body.status === 'string' ? body.status : 'awaiting_feedback'
  const createdTask = await createTask({
    userId,
    conversationId,
    title,
    traceId: c.get('traceId'),
    inputSummary,
    status,
  })
  if (!createdTask) {
    return c.json({ error: 'Failed to create task' }, 502)
  }

  const taskId = createdTask.taskId
  c.set('taskId', taskId)
  c.set('conversationId', createdTask.conversationId)

  logFromContext(c, 'info', 'http', 'POST /api/tasks', {
    user_id: userId,
  })

  const statusUpdate: StatusUpdate = {
    taskId,
    status: status === 'working' || status === 'completed' ? status : 'awaiting_feedback',
    lastUpdate: 'Task created — awaiting orchestrator',
    expectedNextInputMinutes: null,
    timestamp: Date.now(),
  }
  tasks.set(taskId, statusUpdate)
  broadcastStatus(taskId, statusUpdate)

  return c.json({ taskId, conversationId: createdTask.conversationId, status: 'created' })
});

// =============================================================================
// Orchestrator HTTP bridge
// =============================================================================

/**
 * Orchestrator (or gateway) pushes frames onto the same channel the web UI consumes via SSE.
 * Body: { type: 'status', payload: StatusUpdate } | { type: 'credential_request', payload: CredentialRequest }
 */
app.post('/api/orchestrator/stream-events', async (c) => {
  let raw: unknown;
  try {
    raw = await c.req.json();
  } catch {
    return c.json({ error: 'invalid json' }, 400);
  }
  const event = parseOrchestratorStreamEvent(raw);
  if (!event) {
    return c.json({ error: 'invalid orchestrator stream event' }, 400);
  }
  const taskId = event.payload.taskId;
  if (taskId) c.set('taskId', taskId)
  logFromContext(c, 'info', 'orchestrator-proxy', 'POST /api/orchestrator/stream-events', {
    event_type: event.type,
  })
  if (event.type === 'status') {
    tasks.set(taskId, event.payload);
  } else if (event.type === 'chat_response') {
    const { content, done } = event.payload;
    deliverChatResponse(taskId, content, done);
  }
  broadcastStreamEvent(taskId, event);
  return c.json({ ok: true });
});

// =============================================================================
// Plan approval: IO → Orchestrator
// =============================================================================

/**
 * The web dashboard POSTs the user's approve/reject decision here.
 * Body: { taskId, orchestratorTaskRef, approved, reason? }
 * The decision is forwarded to the orchestrator over NATS on
 * `aegis.orchestrator.plan.decision`.
 */
app.post('/api/orchestrator/plan-decision', async (c) => {
  type PlanDecisionRequest = {
    taskId?: string
    orchestratorTaskRef?: string
    approved?: boolean
    reason?: string
  }
  let body: PlanDecisionRequest
  try {
    body = (await c.req.json()) as PlanDecisionRequest
  } catch {
    return c.json({ error: 'invalid json' }, 400)
  }
  const taskId = typeof body.taskId === 'string' ? body.taskId : ''
  const orchestratorTaskRef =
    typeof body.orchestratorTaskRef === 'string' ? body.orchestratorTaskRef : ''
  const approved = body.approved === true
  const reason = typeof body.reason === 'string' ? body.reason : undefined

  if (!taskId || !orchestratorTaskRef) {
    return c.json({ error: 'taskId and orchestratorTaskRef are required' }, 400)
  }

  c.set('taskId', taskId)
  logFromContext(c, 'info', 'orchestrator-proxy', 'POST /api/orchestrator/plan-decision', {
    orchestrator_task_ref: orchestratorTaskRef,
    approved,
  })

  if (!natsClient?.connected) {
    return c.json(
      { error: 'orchestrator not connected — cannot deliver decision' },
      503,
    )
  }

  try {
    await natsClient.publishPlanDecision({
      task_id: taskId,
      orchestrator_task_ref: orchestratorTaskRef,
      approved,
      reason,
      trace_id: c.get('traceId'),
    })
  } catch (err) {
    logFromContext(c, 'error', 'nats', 'failed to publish plan_decision', {
      err: String(err),
    })
    return c.json({ error: 'failed to publish decision' }, 502)
  }

  return c.json({ ok: true })
})

// =============================================================================
// Chat streaming
// =============================================================================

/** When recurrence language would hit NATS unnecessarily, steer to user crons. */
const SCHEDULING_AUTOMATION_CHAT_REPLY =
  [
    'That kind of repeating message belongs in **Scheduled tasks**, not in the planner chat loop.',
    '',
    '- Open **Create recurring task** (or **Settings → Scheduling**), set the rhythm (e.g. every minute), and put your text — e.g. **I am waiting** — in what runs each time.',
    '- Stopping automatically at a wall time like **9:15 PM** is **not modeled yet**. Delete or pause that scheduled task when you are finished; we could add end times later.',
    '',
    '(This chat endpoint does not run timers or impersonate cron.)',
  ].join('\n')

// Send a message (returns streaming response)
app.post('/api/chat', async (c) => {
  const body = (await c.req.json()) as SendMessageRequest;
  const { taskId, userId, content, conversationHistory, conversationId, required_skill_domains } = body as SendMessageRequest & { userId?: string; required_skill_domains?: string[] };
  const effectiveUserId = userId || requestUserId(c)
  const traceId = c.get('traceId') as string | undefined;
  if (taskId) c.set('taskId', taskId)
  if (conversationId) c.set('conversationId', conversationId)
  logFromContext(c, 'info', 'http', 'POST /api/chat', {
    user_id: effectiveUserId,
    content_len: content.length,
    history_len: conversationHistory?.length,
  })

  if (!conversationId) {
    return c.json({ error: 'conversationId is required' }, 400)
  }

  const scheduleAutomationIntent = messageLooksLikeUserCronScheduling(content ?? '')

  // Log user message via memory client — fire-and-forget so the SSE stream opens immediately.
  appendLogEntry({
    conversationId,
    userId: effectiveUserId,
    role: 'user',
    content,
    taskId,
    traceId,
  }).catch(() => { /* best-effort */ })

  const workingStatus: StatusUpdate = {
    taskId,
    status: 'working',
    lastUpdate: 'Generating response...',
    expectedNextInputMinutes: 1,
    timestamp: Date.now(),
  };
  if (!scheduleAutomationIntent) {
    persistAndBroadcastStatus(workingStatus)
  }

  const encoder = new TextEncoder();

  // When NATS is connected, forward the message and wait for the real orchestrator response.
  // When not connected (and not demo), show a clear fallback.
  // Demo mode always uses the local mock generator.
  const useRealOrchestrator = !DEMO_MODE && natsClient?.connected

  /** True only after JetStream publish succeeds — drives SSE branch (avoids stale `connected` vs stream start race). */
  let awaitingOrchestratorChat = false
  let userTaskPublishError: string | null = null

  // Publish user message to orchestrator via NATS
  if (useRealOrchestrator && !scheduleAutomationIntent) {
    try {
      // When conversationId is set the agent fetches prior turns natively from
      // its ConversationSnapshot; skip text injection to avoid double-context.
      const rawInput = conversationId
        ? content
        : buildRawInputWithHistory(content, conversationHistory)
      const isFollowUp = !conversationId && rawInput !== content
      await natsClient!.publishUserTask({
        task_id: taskId,
        user_id: effectiveUserId,
        content,
        payload: { raw_input: rawInput },
        callback_topic: callbackTopicForTask(taskId),
        trace_id: traceId,
        conversation_id: conversationId,
        required_skill_domains,
      })
      awaitingOrchestratorChat = true
      logFromContext(c, 'info', 'nats', 'published user_task', {
        is_follow_up: isFollowUp,
        history_turns: conversationHistory?.length ?? 0,
        raw_input_len: rawInput.length,
      })
    } catch (err) {
      userTaskPublishError = String(err)
      logFromContext(c, 'error', 'nats', 'failed to publish user_task', { err: userTaskPublishError })
    }
  }

  let cleanupChatStream: (() => void) | undefined

  const stream = new ReadableStream({
    start(controller) {
      const finishCronSteerAssistant = async (body: string) => {
        controller.enqueue(encoder.encode(`data: ${JSON.stringify({ chunk: body })}\n\n`))
        controller.enqueue(encoder.encode(`data: ${JSON.stringify({ done: true })}\n\n`))
        controller.close()
        await appendLogEntry({
          conversationId,
          userId: effectiveUserId,
          role: 'assistant',
          content: body,
          taskId,
          traceId,
        })
        const doneStatus: StatusUpdate = {
          taskId,
          status: 'awaiting_feedback',
          lastUpdate: 'Response complete',
          expectedNextInputMinutes: 0,
          timestamp: Date.now(),
        }
        persistAndBroadcastStatus(doneStatus)
      }

      if (scheduleAutomationIntent) {
        void finishCronSteerAssistant(SCHEDULING_AUTOMATION_CHAT_REPLY)
        return
      }

      // Not connected, not demo → clear fallback
      if (!DEMO_MODE && !natsClient?.connected) {
        const fallbackMsg = '[IO] Orchestrator is not connected. The message was logged but cannot be processed.\n\n' +
             'To connect the orchestrator, configure NATS_URL or start the orchestrator service.\n'
        controller.enqueue(encoder.encode(`data: ${JSON.stringify({ chunk: fallbackMsg })}\n\n`));
        controller.enqueue(encoder.encode(`data: ${JSON.stringify({ done: true })}\n\n`));
        controller.close();
        appendLogEntry({ conversationId, userId: effectiveUserId, role: 'assistant', content: fallbackMsg, taskId, traceId })
        return;
      }

      if (userTaskPublishError) {
        const errMsg =
          `[IO] Could not publish task to NATS: ${userTaskPublishError}\n\n` +
          'Check NATS_URL and that the broker is reachable from the IO container.\n'
        controller.enqueue(encoder.encode(`data: ${JSON.stringify({ chunk: errMsg })}\n\n`))
        controller.enqueue(encoder.encode(`data: ${JSON.stringify({ done: true })}\n\n`))
        controller.close()
        appendLogEntry({ conversationId, userId: effectiveUserId, role: 'assistant', content: errMsg, taskId, traceId })
        return
      }

      if (awaitingOrchestratorChat) {
        // Wait for the orchestrator to deliver a response via NATS callback or HTTP bridge
        const TIMEOUT_MS = 120_000
        // Seed with a local ack so the user sees feedback within ~100ms instead of
        // waiting the full planner round-trip for the orchestrator's task_accepted.
        let accumulated = 'Task received — sending to orchestrator...\n'
        let streamDone = false

        const safeEnqueue = (line: string) => {
          if (streamDone) return
          try {
            controller.enqueue(encoder.encode(line))
          } catch {
            streamDone = true
          }
        }
        const safeClose = () => {
          if (streamDone) return
          streamDone = true
          try {
            controller.close()
          } catch {
            /* already closed (e.g. client disconnected) */
          }
        }

        const pendingKey = chatPendingKey(taskId)
        let keepalive: ReturnType<typeof setInterval> | undefined
        const stopKeepalive = () => {
          if (keepalive !== undefined) {
            clearInterval(keepalive)
            keepalive = undefined
          }
        }

        const timeout = setTimeout(() => {
          stopKeepalive()
          pendingChatResponses.delete(pendingKey)
          if (streamDone) return
          const msg = accumulated
            ? accumulated
            : '[IO] The orchestrator did not respond in time. Your message was delivered — the result may arrive via the status panel.\n'
          if (!accumulated) {
            safeEnqueue(`data: ${JSON.stringify({ chunk: msg })}\n\n`)
          }
          safeEnqueue(`data: ${JSON.stringify({ done: true })}\n\n`)
          safeClose()
          appendLogEntry({ conversationId, userId: effectiveUserId, role: 'assistant', content: msg, taskId, traceId })
        }, TIMEOUT_MS)

        cleanupChatStream = () => {
          stopKeepalive()
          clearTimeout(timeout)
          pendingChatResponses.delete(pendingKey)
          streamDone = true
        }

        pendingChatResponses.set(pendingKey, {
          push(chunk: string) {
            if (streamDone) return
            accumulated += chunk
            safeEnqueue(`data: ${JSON.stringify({ chunk: accumulated })}\n\n`)
          },
          complete() {
            stopKeepalive()
            clearTimeout(timeout)
            if (streamDone) return
            safeEnqueue(`data: ${JSON.stringify({ done: true })}\n\n`)
            safeClose()
            appendLogEntry({ conversationId, userId: effectiveUserId, role: 'assistant', content: accumulated, taskId, traceId })
            const doneStatus: StatusUpdate = {
              taskId,
              status: 'awaiting_feedback',
              lastUpdate: 'Response complete',
              expectedNextInputMinutes: 0,
              timestamp: Date.now(),
            }
            persistAndBroadcastStatus(doneStatus)
          },
          error(msg: string) {
            stopKeepalive()
            clearTimeout(timeout)
            if (streamDone) return
            const errContent = accumulated + `\n\nError: ${msg}`
            safeEnqueue(`data: ${JSON.stringify({ chunk: errContent })}\n\n`)
            safeEnqueue(`data: ${JSON.stringify({ done: true })}\n\n`)
            safeClose()
            appendLogEntry({ conversationId, userId: effectiveUserId, role: 'assistant', content: errContent, taskId, traceId })
          },
        })
        // Flush the local ack immediately so the user sees feedback before the orchestrator round-trip.
        safeEnqueue(`data: ${JSON.stringify({ chunk: accumulated })}\n\n`)
        ioLog('info', 'http', 'task_received_local_ack', { task_id: taskId })
        // SSE comment line primes buffered proxies/bun runtime so the next real chunk isn't held back.
        safeEnqueue(': io-waiting\n\n')
        // Idle long-poll streams can be closed by proxies or stacks with no bytes for ~30s; keep TCP/SSE alive until orchestrator responds.
        // Bun default idleTimeout is 10s; keepalives must fire more often or the TCP is reset mid-SSE.
        keepalive = setInterval(() => {
          if (streamDone) {
            stopKeepalive()
            return
          }
          safeEnqueue(': keepalive\n\n')
        }, 8_000)
        ioLog('info', 'http', 'chat_stream_pending', { task_id: taskId, pending_key: pendingKey })
        return
      }

      // Demo mode: use mock generator
      ;(async () => {
        let accumulated = '';
        for await (const chunk of generateMockResponse(content)) {
          accumulated += chunk;
          controller.enqueue(encoder.encode(`data: ${JSON.stringify({ chunk: accumulated })}\n\n`));
        }
        controller.enqueue(encoder.encode(`data: ${JSON.stringify({ done: true })}\n\n`));
        controller.close();
        await appendLogEntry({ conversationId, userId: effectiveUserId, role: 'assistant', content: accumulated, taskId, traceId })
        const doneStatus: StatusUpdate = {
          taskId, status: 'awaiting_feedback', lastUpdate: 'Response complete',
          expectedNextInputMinutes: 0, timestamp: Date.now(),
        }
        persistAndBroadcastStatus(doneStatus)
      })()
    },
    cancel() {
      cleanupChatStream?.()
    },
  });

  return new Response(stream, {
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
      'X-Content-Type-Options': 'nosniff',
      ...(scheduleAutomationIntent ? { 'X-IO-Chat-Scheduling-Steer': '1' } : {}),
    },
  });
});

// =============================================================================
// Log retrieval
// =============================================================================

// Get logs for a task
app.get('/api/logs/:taskId', async (c) => {
  const taskId = c.req.param('taskId');
  const traceId = c.get('traceId') as string | undefined;
  const userId = requestUserId(c)
  c.set('taskId', taskId)
  logFromContext(c, 'info', 'http', 'GET /api/logs/:taskId', { task_id: taskId })
  const task = await getTask(taskId, userId)
  if (!task) {
    return c.json({ logs: [] })
  }
  c.set('conversationId', task.conversationId)
  const memoryLogs = await getConversationLogs(task.conversationId, { userId, traceId })
  const logs = memoryLogs.map(memoryToLogEntry)
  return c.json({ logs });
});

app.get('/api/conversations', async (c) => {
  const userId = requestUserId(c)
  logFromContext(c, 'info', 'http', 'GET /api/conversations', { user_id: userId })
  const conversations = await listConversations(userId)
  return c.json({ conversations })
})

app.delete('/api/conversations/:conversationId', async (c) => {
  const conversationId = c.req.param('conversationId')
  const userId = requestUserId(c)
  logFromContext(c, 'info', 'http', 'DELETE /api/conversations/:conversationId', { user_id: userId })
  await deleteConversation(conversationId, userId)
  return c.json({ ok: true })
})

app.patch('/api/conversations/:conversationId', async (c) => {
  const conversationId = c.req.param('conversationId')
  const userId = requestUserId(c)
  const body = await c.req.json() as { title?: string }
  logFromContext(c, 'info', 'http', 'PATCH /api/conversations/:conversationId', { user_id: userId })
  if (body.title) {
    await renameConversation(conversationId, userId, body.title)
  }
  return c.json({ ok: true })
})

app.get('/api/conversations/:conversationId/logs', async (c) => {
  const conversationId = c.req.param('conversationId')
  const userId = requestUserId(c)
  c.set('conversationId', conversationId)
  logFromContext(c, 'info', 'http', 'GET /api/conversations/:conversationId/logs', {
    user_id: userId,
  })
  const memoryLogs = await getConversationLogs(conversationId, { userId })
  return c.json({ logs: memoryLogs.map(memoryToLogEntry) })
})

// User-scheduled crons (proxy to memory-api; server holds MEMORY_API_KEY)
const memoryBffBase = () => (process.env.MEMORY_API_BASE ?? '').trim().replace(/\/$/, '')
const memoryBffKey = () =>
  (process.env.MEMORY_API_KEY ?? process.env.INTERNAL_VAULT_API_KEY ?? '').trim()

function jsonFromMemoryProxy(text: string, status: number) {
  try {
    return { data: JSON.parse(text) as unknown, err: null as null }
  } catch {
    return { data: null, err: text as string }
  }
}

app.post('/api/user-crons', async (c) => {
  const base = memoryBffBase()
  const key = memoryBffKey()
  if (!base || !key) {
    return c.json(
      {
        error:
          'User crons need MEMORY_API_BASE and either MEMORY_API_KEY or INTERNAL_VAULT_API_KEY on the IO server',
      },
      503,
    )
  }
  const body = (await c.req.json()) as {
    name?: string
    userId?: string
    rawInput?: string
    scheduleKind?: 'interval' | 'cron'
    intervalSeconds?: number
    cronExpression?: string
    timeZone?: string
    nextRunAt?: string
    conversationId?: string
  }
  const userId = typeof body.userId === 'string' && body.userId ? body.userId : requestUserId(c)
  if (!body.name?.trim() || !body.rawInput?.trim() || !body.nextRunAt || !body.scheduleKind) {
    return c.json({ error: 'name, rawInput, nextRunAt, and scheduleKind are required' }, 400)
  }
  logFromContext(c, 'info', 'http', 'POST /api/user-crons', { user_id: userId, name: body.name })
  const res = await fetch(`${base}/api/v1/scheduled_jobs`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Internal-API-Key': key },
    body: JSON.stringify({
      jobType: 'user_cron',
      targetKind: 'user',
      targetService: 'orchestrator',
      status: 'active',
      name: body.name.trim(),
      scheduleKind: body.scheduleKind,
      intervalSeconds: body.intervalSeconds ?? 0,
      cronExpression: body.cronExpression ?? '',
      timeZone: body.timeZone?.trim() || 'UTC',
      nextRunAt: body.nextRunAt,
      userId,
      payload: { userId, rawInput: body.rawInput.trim(), conversationId: body.conversationId },
    }),
  })
  const text = await res.text()
  const { data, err } = jsonFromMemoryProxy(text, res.status)
  if (err !== null) {
    return c.text(err, res.status)
  }
  const conversationId = body.conversationId?.trim()
  if (res.ok && conversationId) {
    const scheduleText =
      body.scheduleKind === 'cron'
        ? `cron ${body.cronExpression ?? ''} (${body.timeZone?.trim() || 'UTC'})`
        : `every ${body.intervalSeconds ?? 0}s`
    await appendLogEntry({
      conversationId,
      userId,
      role: 'user',
      content: `Scheduled task "${body.name.trim()}" (${scheduleText}):\n\n${body.rawInput.trim()}`,
      taskId: conversationId,
    }).catch(error => {
      ioLog('warn', 'memory', 'failed to append scheduled task note', {
        conversation_id: conversationId,
        error: String(error),
      })
    })
  }
  return c.json(data, res.status)
})

app.get('/api/user-crons', async (c) => {
  const base = memoryBffBase()
  const key = memoryBffKey()
  if (!base || !key) {
    return c.json(
      {
        error:
          'User crons need MEMORY_API_BASE and either MEMORY_API_KEY or INTERNAL_VAULT_API_KEY on the IO server',
      },
      503,
    )
  }
  const userId = c.req.query('userId') || requestUserId(c)
  const res = await fetch(
    `${base}/api/v1/user_crons?userId=${encodeURIComponent(userId)}`,
    { headers: { 'X-Internal-API-Key': key } },
  )
  const text = await res.text()
  const { data, err } = jsonFromMemoryProxy(text, res.status)
  if (err !== null) {
    return c.text(err, res.status)
  }
  return c.json(data, res.status)
})

app.delete('/api/user-crons/:jobId', async (c) => {
  const base = memoryBffBase()
  const key = memoryBffKey()
  if (!base || !key) {
    return c.json(
      {
        error:
          'User crons need MEMORY_API_BASE and either MEMORY_API_KEY or INTERNAL_VAULT_API_KEY on the IO server',
      },
      503,
    )
  }
  const jobId = c.req.param('jobId')
  const userId = c.req.query('userId') || requestUserId(c)
  const res = await fetch(
    `${base}/api/v1/scheduled_jobs/${encodeURIComponent(jobId)}?userId=${encodeURIComponent(userId)}`,
    { method: 'DELETE', headers: { 'X-Internal-API-Key': key } },
  )
  const text = await res.text()
  const { data, err } = jsonFromMemoryProxy(text, res.status)
  if (err !== null) {
    return c.text(err, res.status)
  }
  return c.json(data, res.status)
})

// Get all logs — session-scoped queries via /api/logs/:taskId are the intended API
app.get('/api/logs', (c) => {
  logFromContext(c, 'info', 'http', 'GET /api/logs')
  return c.json({ logs: [] });
});

// =============================================================================
// Credential submission
// =============================================================================

// Credential submission endpoint (proxies to Vault Engine, then notifies agent via NATS)
// NEVER logs or exposes the credential value in responses or logs
app.post('/api/credential', async (c) => {
  const body = (await c.req.json()) as {
    taskId: string;
    requestId: string;
    userId: string;
    keyName: string;
    value: string;
  };
  const { taskId, requestId, userId, keyName } = body;
  c.set('taskId', taskId)
  logFromContext(c, 'info', 'http', 'POST /api/credential', {
    request_id: requestId,
    user_id: userId,
    key_name: keyName,
  })

  const vaultEngineUrl = (process.env.VAULT_ENGINE_URL || 'http://vault:8000').replace(/\/$/, '');

  try {
    const vaultRes = await fetch(`${vaultEngineUrl}/credentials/put`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'traceparent': c.get('traceparent'),
        'X-Trace-ID': c.get('traceId'),
      },
      body: JSON.stringify({
        user_id: userId,
        credential_type: keyName,
        value: body.value, // pass through without logging
      }),
    });

    if (!vaultRes.ok) {
      logFromContext(c, 'error', 'credential', 'vault engine rejected credential put', {
        key_name: keyName,
        status: String(vaultRes.status),
      })
      return c.json(
        {
          taskId,
          requestId,
          keyName,
          status: 'error',
          error: `Vault engine returned ${vaultRes.status}`,
        },
        500,
      );
    }

    // Notify the waiting agent that the credential is now available.
    // The agent's Execute() is blocked on aegis.agents.credential.response
    // for this requestId — publishing here unblocks the retry.
    if (natsClient?.connected) {
      natsClient.publishRaw('aegis.agents.credential.response', {
        request_id: requestId,
        status: 'granted',
        permission_token: `io-stored-${userId}-${keyName}`,
        expires_at: new Date(Date.now() + 3600_000).toISOString(),
      })
    }

    return c.json(
      {
        taskId,
        requestId,
        keyName,
        status: 'stored',
      },
      201,
    );
  } catch (err) {
    logFromContext(c, 'warn', 'credential', 'vault engine unavailable; credential not stored', {
      key_name: keyName,
      error: String(err),
    })
    return c.json(
      {
        taskId,
        requestId,
        keyName,
        status: 'error',
        error: 'vault engine unavailable',
      },
      502,
    );
  }
});

// =============================================================================
// SSE: orchestrator → IO push stream for one task
// =============================================================================

app.get('/api/events/:taskId', (c) => {
  const taskId = c.req.param('taskId');
  c.set('taskId', taskId)
  logFromContext(c, 'info', 'http', 'GET /api/events/:taskId')

  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      const push = (bytes: Uint8Array) => {
        try {
          controller.enqueue(bytes);
        } catch {
          /* stream closed */
        }
      };

      const unsubscribe = subscribeSse(taskId, push);

      // Only push initial status when the BFF has state (e.g. after /api/chat). Otherwise the
      // web UI keeps its local mock task list without being overwritten by a synthetic default.
      const stored = tasks.get(taskId);
      if (stored) {
        push(text.encode(`data: ${JSON.stringify({ type: 'status', payload: stored })}\n\n`));
      }

      // Demo mode: push a sample credential request for the demo task
      if (DEMO_MODE && taskId === '13') {
        setTimeout(() => {
          broadcastStreamEvent('13', {
            type: 'credential_request',
            payload: DEMO_TASK_13_CREDENTIAL,
          });
        }, 900);
      }

      const interval = setInterval(() => {
        const current = tasks.get(taskId);
        if (current?.status === 'working') {
          broadcastStatus(taskId, { ...current, timestamp: Date.now() });
        }
      }, 2800);

      c.req.raw.signal.addEventListener('abort', () => {
        clearInterval(interval);
        unsubscribe();
        try {
          controller.close();
        } catch {
          /* */
        }
      });
    },
  });

  return new Response(stream, {
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
    },
  });
});

// =============================================================================
// Voice / transcription
// =============================================================================

/** POST /api/voice/transcribe — transcribe audio using faster-whisper */
app.post('/api/voice/transcribe', async (c) => {
  logFromContext(c, 'info', 'http', 'POST /api/voice/transcribe')
  const body = await c.req.json<{
    audioData: string
    format: string
    language?: string
  }>()

  try {
    const result = await transcribe({
      audioData: body.audioData,
      language: body.language,
    })
    return c.json(result)
  } catch (err) {
    logFromContext(c, 'error', 'voice', 'transcription failed', { err: String(err) })
    return c.json({ error: String(err) }, 500)
  }
})

// =============================================================================
// Production: serve static web dashboard files
// =============================================================================

if (process.env.NODE_ENV === 'production') {
  app.use('/*', serveStatic({ root: '/app/web-dist/' }))
  app.use('/*', serveStatic({ path: '/app/web-dist/index.html' })) // SPA routing fallback
}

// =============================================================================
// Warm up faster-whisper before accepting traffic
// =============================================================================

warmupTranscription()

// Bun default idle timeout is ~10s; SSE needs keepalives. Disabling globally (0) risks idle
// connection buildup — use a bounded timeout; override with BUN_IDLE_TIMEOUT_SECONDS (e.g. 300 for slow streams).
const configuredIdleTimeoutSeconds = Number(process.env.BUN_IDLE_TIMEOUT_SECONDS)
const serverIdleTimeoutSeconds =
  Number.isFinite(configuredIdleTimeoutSeconds) && configuredIdleTimeoutSeconds > 0
    ? configuredIdleTimeoutSeconds
    : 120

// Kubelet probes use the pod IP; must listen on all interfaces (not loopback-only).
const listenHost = (process.env.IO_LISTEN_HOST ?? process.env.HOST ?? '0.0.0.0').trim() || '0.0.0.0'

// Use Bun.serve() — the default-export object shape does not reliably accept `hostname`
// across Bun versions and can crash at startup in containers.
Bun.serve({
  port: 3001,
  hostname: listenHost,
  idleTimeout: serverIdleTimeoutSeconds,
  fetch: app.fetch,
})

ioLog('info', 'http', `IO API listening`, { host: listenHost, port: 3001 })
