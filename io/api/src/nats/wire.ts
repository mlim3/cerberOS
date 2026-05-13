export const IO_RESULTS_TOPIC_PREFIX = 'aegis.io.results.'

/** Build a callback topic for a given task. */
export function callbackTopicForTask(taskId: string): string {
  return `${IO_RESULTS_TOPIC_PREFIX}${taskId}`
}

export interface UserTaskPayload {
  task_id: string
  user_id: string
  user_role?: 'root' | 'manager' | 'user'
  content: string
  priority?: number
  timeout_seconds?: number
  payload?: { raw_input: string }
  callback_topic?: string
  required_skill_domains?: string[]
  user_context_id?: string
  /** Stable ID linking follow-up messages in the same conversation. When set,
   *  the orchestrator threads it to agents so they can fetch prior turns from
   *  their ConversationSnapshot rather than relying on the raw_input history block. */
  conversation_id?: string
  /** W3C trace_id (32 hex) — forwarded on the wire envelope for orchestrator logs */
  trace_id?: string
}

export function buildUserTaskWirePayload(task: UserTaskPayload) {
  return {
    task_id: task.task_id,
    user_id: task.user_id,
    user_role: task.user_role,
    required_skill_domains: task.required_skill_domains ?? [],
    priority: task.priority ?? 5,
    // Runaway backstop for a whole task. Individual phases are already
    // bounded by tighter timers in the orchestrator:
    //   - DecompositionTimeoutSeconds (30s) — planner LLM call
    //   - PlanApprovalTimeoutSeconds (300s) — user review of the plan
    //   - per-subtask timeout_seconds in the plan itself (~30s each)
    // The old 120s default was shorter than the approval window alone,
    // so a task sitting in AWAITING_APPROVAL would be killed before the
    // user could click "Approve". 30 minutes gives realistic headroom
    // for approval + multi-subtask execution without sacrificing the
    // backstop semantics.
    timeout_seconds: task.timeout_seconds ?? 1800,
    payload: task.payload ?? { raw_input: task.content },
    callback_topic: task.callback_topic ?? callbackTopicForTask(task.task_id),
    user_context_id: task.user_context_id,
    ...(task.conversation_id ? { conversation_id: task.conversation_id } : {}),
  }
}
