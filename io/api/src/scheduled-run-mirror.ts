/**
 * Persist orchestrator callbacks (task_result / error_response) to Memory when
 * no POST /api/chat session is streaming — typical for scheduled (cron) runs.
 */

import {
  appendLogEntry,
  createConversation,
  getTask,
  listConversations,
} from '@cerberos/io-core/memory-client'

const SCHEDULED_RUNS_CONVERSATION_TITLE = 'Scheduled task runs'

export function mirrorMemoryConfigured(): boolean {
  return !!(process.env.MEMORY_API_BASE ?? '').trim()
}

async function resolveConversationId(params: {
  userId: string
  taskId: string
  payloadConversationId?: string
}): Promise<string | null> {
  const task = await getTask(params.taskId, params.userId)
  if (task?.conversationId) {
    return task.conversationId
  }
  if (params.payloadConversationId?.trim()) {
    return params.payloadConversationId.trim()
  }
  const list = await listConversations(params.userId, { limit: 250 })
  const existing = list.find(c => c.title === SCHEDULED_RUNS_CONVERSATION_TITLE)
  if (existing) {
    return existing.conversationId
  }
  const created = await createConversation({
    userId: params.userId,
    title: SCHEDULED_RUNS_CONVERSATION_TITLE,
  })
  return created?.conversationId ?? null
}

export async function persistOrchestratorOutcomeToMemory(params: {
  userId: string
  taskId: string
  payloadConversationId?: string
  traceId?: string
  contentLines: string[]
}): Promise<void> {
  const conversationId = await resolveConversationId({
    userId: params.userId,
    taskId: params.taskId,
    payloadConversationId: params.payloadConversationId,
  })
  if (!conversationId) {
    return
  }
  await appendLogEntry({
    conversationId,
    userId: params.userId,
    role: 'assistant',
    content: params.contentLines.join('\n'),
    taskId: params.taskId,
    traceId: params.traceId?.trim() || undefined,
  })
}
