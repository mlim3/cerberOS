/**
 * Demo orchestrator API. Calls are logged; for the demo some use an LLM API.
 * Streaming: responses are yielded as chunks instead of one full string.
 */

import { logUserResponse, logOrchestratorResponse } from '../lib/logging'

export interface OrchestratorMessage {
  role: 'user' | 'assistant'
  content: string
}

/**
 * Send user message to orchestrator and stream back the reply.
 * User message is logged here; orchestrator reply is logged when stream completes.
 */
export async function* streamOrchestratorReply(
  taskId: string,
  userContent: string,
  _conversationHistory: OrchestratorMessage[],
  options?: { apiKey?: string; useRealApi?: boolean }
): AsyncGenerator<string, void, unknown> {
  // Log user response (visible in code: this is where user input is sent to logging)
  logUserResponse(taskId, userContent)

  const useRealApi = options?.useRealApi && options?.apiKey
  if (useRealApi) {
    yield* streamFromAnthropic(taskId, userContent, _conversationHistory, options.apiKey!)
    return
  }

  // Mock stream for demo: simulate CerberOS/orchestrator reply
  const fullReply = await getMockOrchestratorReply(userContent)
  const words = fullReply.split(/(\s+)/)
  let accumulated = ''
  for (const w of words) {
    accumulated += w
    yield accumulated
    await new Promise((r) => setTimeout(r, 30))
  }

  // Log orchestrator response (visible in code: this is where orchestrator output is sent to logging)
  logOrchestratorResponse(taskId, fullReply)
}

async function getMockOrchestratorReply(userMessage: string): Promise<string> {
  // Simulate a sensible demo reply without calling a real API
  const lower = userMessage.toLowerCase()
  if (lower.includes('auth') || lower.includes('oauth')) {
    return "Understood. I'll proceed with OAuth 2.0 and the selected providers. You can review the implementation in the next step."
  }
  if (lower.includes('approve') || lower.includes('yes') || lower.includes('ok')) {
    return "Acknowledged. Proceeding with the plan. I'll signal when the next user input is needed."
  }
  if (lower.includes('test') || lower.includes('endpoint')) {
    return "I'll continue running the endpoint tests and will pause for your review when the suite is complete."
  }
  return `CerberOS: I've noted your input («${userMessage.slice(0, 50)}${userMessage.length > 50 ? '…' : ''}»). For the demo, orchestrator replies are streamed and logged. Next step is ready for your review when you are.`
}

/** Optional: stream from Anthropic API when apiKey is set and useRealApi is true. */
async function* streamFromAnthropic(
  taskId: string,
  userContent: string,
  history: OrchestratorMessage[],
  apiKey: string
): AsyncGenerator<string, void, unknown> {
  const messages = [
    ...history.map((m) => ({ role: m.role as 'user' | 'assistant', content: m.content })),
    { role: 'user' as const, content: userContent },
  ]
  const body = {
    model: 'claude-3-5-sonnet-20241022',
    max_tokens: 1024,
    messages,
    stream: true,
  }
  const res = await fetch('https://api.anthropic.com/v1/messages', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'x-api-key': apiKey,
      'anthropic-version': '2023-06-01',
    },
    body: JSON.stringify(body),
  })
  if (!res.ok || !res.body) {
    const fallback = `Request failed: ${res.status}. Using mock reply for demo.`
    logOrchestratorResponse(taskId, fallback)
    yield fallback
    return
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let full = ''
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      const chunk = decoder.decode(value, { stream: true })
      const lines = chunk.split('\n').filter((l) => l.startsWith('data: '))
      for (const line of lines) {
        const data = line.slice(6)
        if (data === '[DONE]') continue
        try {
          const parsed = JSON.parse(data)
          const delta = parsed.delta?.text ?? parsed.content?.[0]?.text ?? ''
          if (delta) {
            full += delta
            yield full
          }
        } catch {
          // ignore parse errors for partial chunks
        }
      }
    }
  } finally {
    reader.releaseLock()
  }
  logOrchestratorResponse(taskId, full)
}
