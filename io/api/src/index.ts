import { Hono } from 'hono';
import { cors } from 'hono/cors';
import type { StatusUpdate, LogEntry, SendMessageRequest } from '@cerberos/io-core';

// In-memory storage (would be replaced with proper persistence in production)
const tasks = new Map<string, StatusUpdate>();
const logs: LogEntry[] = [];

// Mock response generator for demo
async function* generateMockResponse(content: string): AsyncGenerator<string> {
  const responses = [
    "I'm analyzing your request",
    "Processing the information",
    "Looking up relevant data",
    "Formulating a response",
  ];

  for (const response of responses) {
    yield response + "...\n\n";
    await new Promise(r => setTimeout(r, 500));
  }

  yield `Based on your message "${content}", here's what I found:\n\n`;
  await new Promise(r => setTimeout(r, 300));

  yield "• This is a demo response from the IO API server\n";
  yield "• The streaming is working correctly\n";
  yield "• Your message was logged to memory\n";
  yield "\nFeel free to ask more questions!";
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

// Send a message (returns streaming response)
app.post('/api/chat', async (c) => {
  const body = await c.req.json() as SendMessageRequest;
  const { taskId, content, conversationHistory } = body;

  // Log user message
  const userLogEntry: LogEntry = {
    taskId,
    role: 'user',
    content,
    at: new Date().toISOString(),
  };
  logs.push(userLogEntry);

  // Update task status to working
  tasks.set(taskId, {
    taskId,
    status: 'working',
    lastUpdate: 'Generating response...',
    expectedNextInputMinutes: 1,
    timestamp: Date.now(),
  });

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
      logs.push({
        taskId,
        role: 'orchestrator',
        content: accumulated,
        at: new Date().toISOString(),
      });

      // Update task status
      tasks.set(taskId, {
        taskId,
        status: 'awaiting_feedback',
        lastUpdate: 'Response complete',
        expectedNextInputMinutes: 0,
        timestamp: Date.now(),
      });
    },
  });

  return new Response(stream, {
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      'Connection': 'keep-alive',
      'X-Content-Type-Options': 'nosniff',
    },
  });
});

// Get logs for a task
app.get('/api/logs/:taskId', (c) => {
  const taskId = c.req.param('taskId');
  const taskLogs = logs.filter(log => log.taskId === taskId);
  return c.json({ logs: taskLogs });
});

// Get all logs
app.get('/api/logs', (c) => {
  return c.json({ logs });
});

// SSE endpoint for status updates
app.get('/api/events/:taskId', (c) => {
  const taskId = c.req.param('taskId');

  const stream = new ReadableStream({
    start(controller) {
      const encoder = new TextEncoder();

      // Send initial status
      const status = tasks.get(taskId) || {
        taskId,
        status: 'awaiting_feedback' as const,
        lastUpdate: 'Waiting for input',
        expectedNextInputMinutes: null,
        timestamp: Date.now(),
      };

      controller.enqueue(encoder.encode(`data: ${JSON.stringify(status)}\n\n`));

      // Simulate periodic updates
      const interval = setInterval(() => {
        const current = tasks.get(taskId);
        if (current?.status === 'working') {
          controller.enqueue(encoder.encode(`data: ${JSON.stringify(current)}\n\n`));
        }
      }, 3000);

      c.req.raw.signal.addEventListener('abort', () => {
        clearInterval(interval);
        controller.close();
      });
    },
  });

  return new Response(stream, {
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      'Connection': 'keep-alive',
    },
  });
});

export default {
  port: 3001,
  fetch: app.fetch,
};
