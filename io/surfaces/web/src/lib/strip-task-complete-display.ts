/**
 * Remove synthetic `task_complete({...})` tool-call text from assistant markdown source.
 * IO API unwraps these for new NATS results; this covers older Memory logs and streaming quirks.
 */
export function stripTaskCompleteDisplayNoise(text: string): string {
  const t = text.trim()
  const only = t.match(/^task_complete\s*\(\s*(\{[\s\S]*\})\s*\)\s*$/i)
  if (only) {
    try {
      const obj = JSON.parse(only[1]!) as { result?: unknown }
      if (typeof obj.result === 'string' && obj.result.trim()) return obj.result.trim()
    } catch {
      /* fall through */
    }
  }
  const lines = text.split(/\r?\n/)
  const kept = lines.filter(line => !/^\s*task_complete\s*\(/i.test(line))
  return kept.join('\n')
}
