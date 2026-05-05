import { describe, expect, test } from 'bun:test'
import { parseRhythmReply } from './parseRhythm'

describe('parseRhythmReply', () => {
  test('every 1 minute until wall time — embedded sentence', () => {
    const nl =
      'reply in this conversation/chat saying "I am waiting" every 1 minute until 9:30 PM local time'
    const r = parseRhythmReply(nl, 'America/Chicago')
    expect(r.ok).toBe(true)
    if (!r.ok) return
    expect(r.scheduleKind).toBe('interval')
    expect(r.intervalSeconds).toBe(60)
  })

  test('every minute (no digit), strip until clause', () => {
    const z = parseRhythmReply('remind me every minute until noon', 'UTC')
    expect(z.ok).toBe(true)
    if (!z.ok) return
    expect(z.intervalSeconds).toBe(60)
  })

  test('narrowed standalone line keeps minute interval', () => {
    const r = parseRhythmReply('every 2 minutes until 9pm', 'UTC')
    expect(r.ok).toBe(true)
    if (!r.ok) return
    expect(r.intervalSeconds).toBe(120)
  })
})
