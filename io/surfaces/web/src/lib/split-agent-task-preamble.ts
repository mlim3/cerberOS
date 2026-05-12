/**
 * Separates leading orchestrator status clauses (lines starting with "Task")
 * from the final assistant answer so the UI can style status text differently.
 *
 * Matching is case-insensitive so models or APIs that emit "task received" still split.
 */

function peelLeadingTaskSegment(remaining: string): { head: string; tail: string } {
  const t = remaining.trimStart()
  if (!/^task\b/i.test(t)) {
    return { head: '', tail: remaining }
  }

  const betweenTasks = t.match(/^(task\b[\s\S]*?)\s+(?=task\b)/i)
  if (betweenTasks) {
    return {
      head: betweenTasks[1]!.trim(),
      tail: t.slice(betweenTasks[0]!.length).trim(),
    }
  }

  const bodyAfterPeriod = t.match(/^(task\b[\s\S]*?\.)\s+((?!task\b)[\s\S]*)$/is)
  if (bodyAfterPeriod) {
    return {
      head: bodyAfterPeriod[1]!.trim(),
      tail: bodyAfterPeriod[2]!.trim(),
    }
  }

  // `...` or Unicode ellipsis (…)
  const bodyAfterEllipsis = t.match(/^(task\b[\s\S]*?(?:\.\.\.|…))\s+((?!task\b)[\s\S]*)$/is)
  if (bodyAfterEllipsis) {
    return {
      head: bodyAfterEllipsis[1]!.trim(),
      tail: bodyAfterEllipsis[2]!.trim(),
    }
  }

  return { head: t.trim(), tail: '' }
}

/** True when {@link splitAgentTaskStatusPreamble} finds orchestrator status lines to show above the answer. */
export function hasAgentTaskStatusPreamble(raw: string): boolean {
  return splitAgentTaskStatusPreamble(raw).preamble.trim().length > 0
}

export function splitAgentTaskStatusPreamble(raw: string): { preamble: string; body: string } {
  const text = raw.trim()
  if (!text || !/^task\b/i.test(text)) {
    return { preamble: '', body: raw }
  }

  const nlLines = text.split(/\r?\n/)
  if (nlLines.length > 1) {
    const preambleLines: string[] = []
    let i = 0
    while (i < nlLines.length && /^\s*task\b/i.test(nlLines[i]!)) {
      preambleLines.push(nlLines[i]!.trimEnd())
      i++
    }
    if (preambleLines.length > 0) {
      return {
        preamble: preambleLines.join('\n'),
        body: nlLines.slice(i).join('\n').trimStart(),
      }
    }
  }

  const statusChunks: string[] = []
  let remaining = text

  while (remaining && /^task\b/i.test(remaining.trimStart())) {
    const { head, tail } = peelLeadingTaskSegment(remaining)
    if (!head) {
      break
    }
    statusChunks.push(head)
    if (!tail) {
      remaining = ''
      break
    }
    remaining = tail
  }

  if (statusChunks.length === 0) {
    return { preamble: '', body: raw }
  }

  return {
    preamble: statusChunks.join('\n'),
    body: remaining.trim(),
  }
}
