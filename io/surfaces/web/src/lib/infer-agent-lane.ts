import type { TranscriptLane } from '@cerberos/io-core'

/**
 * Best-effort lane for agent bubbles when the backend does not set {@link ChatMessage.lane}.
 * Conservative so normal assistant replies stay in the assistant lane.
 */
export function inferAgentLane(content: string): Exclude<TranscriptLane, 'system'> {
  const head = content.trimStart().slice(0, 360)
  if (/^#{1,3}\s*sub[\s_-]?agent\b/im.test(head)) return 'sub_agent'
  if (/^sub[\s_-]?agent\s*[:\-–]/im.test(head)) return 'sub_agent'
  if (/^\*\*\s*sub[\s_-]?agent\b/im.test(head)) return 'sub_agent'
  if (/^from (the )?subtask\b/im.test(head)) return 'sub_agent'
  if (/^delegated (to|from)\b/im.test(head)) return 'sub_agent'
  if (/^\[tool[/\\]/i.test(head)) return 'sub_agent'
  // Attribution line like [research-agent] or [worker-1]: — but not markdown links `[text](url)`
  if (/^\[[^\]\n]+\]\s*[:\-–]/.test(head) && !/^\[[^\]]+\]\([^)]*\)/.test(head)) return 'sub_agent'
  return 'assistant'
}
