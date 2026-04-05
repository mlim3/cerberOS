import { Hono } from 'hono';
import { cors } from 'hono/cors';
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
  getSessionLogs,
  getOrCreateSessionId,
  type MemoryLogEntry,
} from '@cerberos/io-core/memory-client'
import { transcribe, warmupTranscription } from './transcription/runner'

// In-memory task state (separate from log persistence)
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

/** Demo credential for local dev when Orchestrator is not wired (task 13 in mock UI). */
const DEMO_TASK_13_CREDENTIAL: CredentialRequest = {
  taskId: '13',
  requestId: 'cred-13-dbpwd',
  userId: '00000000-0000-0000-0000-000000000001',
  keyName: 'prod_db_admin_password',
  label: 'Production database admin password',
  description: 'Required to execute the migration on the production cluster.',
};

// Mock response generator for demo
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

const app = new Hono();

// Middleware
app.use('/*', cors());

// Health check
app.get('/health', (c) => c.json({ status: 'ok', timestamp: new Date().toISOString() }));
app.get('/api/health', (c) => c.json({ status: 'ok', timestamp: new Date().toISOString() }));

// Get all tasks status
app.get('/api/tasks', (c) => {
  const taskList = Array.from(tasks.values());
  return c.json({ tasks: taskList });
});

// Get status for a specific task
app.get('/api/tasks/:taskId', (c) => {
  const taskId = c.req.param('taskId');
  const task = tasks.get(taskId);
  if (!task) {
    return c.json({ error: 'Task not found' }, 404);
  }
  return c.json(task);
});

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
  if (event.type === 'status') {
    tasks.set(taskId, event.payload);
  }
  broadcastStreamEvent(taskId, event);
  return c.json({ ok: true });
});

// Send a message (returns streaming response)
app.post('/api/chat', async (c) => {
  const body = (await c.req.json()) as SendMessageRequest;
  const { taskId, content, conversationHistory } = body;

  // Log user message via memory client (uses in-memory when MEMORY_API_BASE is unset)
  const sessionId = getOrCreateSessionId(taskId, '00000000-0000-0000-0000-000000000001')
  await appendLogEntry({
    sessionId,
    userId: '00000000-0000-0000-0000-000000000001',
    role: 'user',
    content,
    taskId,
  })

  const workingStatus: StatusUpdate = {
    taskId,
    status: 'working',
    lastUpdate: 'Generating response...',
    expectedNextInputMinutes: 1,
    timestamp: Date.now(),
  };
  persistAndBroadcastStatus(workingStatus);

  // Return SSE streaming response
  const encoder = new TextEncoder();
  const stream = new ReadableStream({
    async start(controller) {
      let accumulated = '';
      for await (const chunk of generateMockResponse(content)) {
        accumulated += chunk;
        controller.enqueue(encoder.encode(`data: ${JSON.stringify({ chunk: accumulated })}\n\n`));
      }
      controller.enqueue(encoder.encode(`data: ${JSON.stringify({ done: true })}\n\n`));
      controller.close();

      // Log assistant response after streaming completes
      await appendLogEntry({
        sessionId,
        userId: '00000000-0000-0000-0000-000000000001',
        role: 'assistant',
        content: accumulated,
        taskId,
      })

      const doneStatus: StatusUpdate = {
        taskId,
        status: 'awaiting_feedback',
        lastUpdate: 'Response complete',
        expectedNextInputMinutes: 0,
        timestamp: Date.now(),
      };
      persistAndBroadcastStatus(doneStatus);
    },
  });

  return new Response(stream, {
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
      'X-Content-Type-Options': 'nosniff',
    },
  });
});

// Get logs for a task
app.get('/api/logs/:taskId', async (c) => {
  const taskId = c.req.param('taskId');
  const sessionId = getOrCreateSessionId(taskId, '00000000-0000-0000-0000-000000000001')
  const memoryLogs = await getSessionLogs(sessionId, { taskId })
  const logs = memoryLogs.map(memoryToLogEntry)
  return c.json({ logs });
});

// Get all logs — session-scoped queries via /api/logs/:taskId are the intended API
app.get('/api/logs', (c) => {
  return c.json({ logs: [] });
});

// Credential submission endpoint (proxies to Memory Vault)
// NEVER logs or exposes the credential value in responses or logs
app.post('/api/credential', async (c) => {
  const body = (await c.req.json()) as {
    taskId: string;
    requestId: string;
    userId: string;
    keyName: string;
    value: string;
  };
  const { taskId, requestId, userId, keyName, value } = body;

  const memoryVaultUrl = process.env.MEMORY_VAULT_URL || 'http://localhost:8080/api/v1/vault';
  const memoryApiKey = process.env.MEMORY_VAULT_API_KEY || '';

  try {
    const vaultRes = await fetch(`${memoryVaultUrl}/${userId}/secrets`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-API-KEY': memoryApiKey,
        'X-Trace-ID': c.req.header('X-Trace-ID') || crypto.randomUUID(),
      },
      body: JSON.stringify({
        key_name: keyName,
        value: value,
      }),
    });

    if (!vaultRes.ok) {
      const errText = await vaultRes.text();
      return c.json(
        {
          taskId,
          requestId,
          keyName,
          status: 'error',
          error: `Vault returned ${vaultRes.status}: ${errText}`,
        },
        500,
      );
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
  } catch {
    console.log('[credential] Memory service unavailable, simulating credential storage');
    return c.json(
      {
        taskId,
        requestId,
        keyName,
        status: 'stored',
      },
      201,
    );
  }
});

// SSE: orchestrator → IO push stream for one task (enveloped events per io-interfaces.md §1.0)
app.get('/api/events/:taskId', (c) => {
  const taskId = c.req.param('taskId');

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

      // Local dev: push a sample credential request for the demo task
      if (taskId === '13') {
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

/** POST /api/voice/transcribe — transcribe audio using faster-whisper */
app.post('/api/voice/transcribe', async (c) => {
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
    console.error('[Voice] Transcription failed:', err)
    return c.json({ error: String(err) }, 500)
  }
})

// Warm up faster-whisper before accepting traffic
warmupTranscription()

export default {
  port: 3001,
  fetch: app.fetch,
};
