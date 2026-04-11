import { describe, expect, test } from 'bun:test'
import { createTraceparent, parseTraceparent, resolveTraceparent } from './trace-context'

describe('trace-context', () => {
  test('parseTraceparent accepts W3C sample', () => {
    const h = '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'
    const p = parseTraceparent(h)
    expect(p).not.toBeNull()
    expect(p!.traceId).toBe('4bf92f3577b34da6a3ce929d0e0e4736')
    expect(p!.parentId).toBe('00f067aa0ba902b7')
    expect(p!.flags).toBe('01')
  })

  test('createTraceparent returns version 00 and 4 hyphen parts', () => {
    const { traceparent, traceId } = createTraceparent()
    expect(traceId).toHaveLength(32)
    const parts = traceparent.split('-')
    expect(parts[0]).toBe('00')
    expect(parts).toHaveLength(4)
    expect(parts[2]).toHaveLength(16)
  })

  test('resolveTraceparent on valid incoming keeps trace_id, issues new span id', () => {
    const incoming = '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'
    const r = resolveTraceparent(incoming)
    expect(r.traceId).toBe('4bf92f3577b34da6a3ce929d0e0e4736')
    const out = parseTraceparent(r.traceparent)
    expect(out).not.toBeNull()
    expect(out!.traceId).toBe(r.traceId)
    expect(out!.flags).toBe('01')
    // This hop must not reuse the incoming parent id as the new span id.
    expect(out!.parentId).not.toBe('00f067aa0ba902b7')
    expect(out!.parentId).toHaveLength(16)
  })

  test('resolveTraceparent on missing header creates root trace', () => {
    const r = resolveTraceparent(undefined)
    expect(r.traceId).toHaveLength(32)
    expect(parseTraceparent(r.traceparent)).not.toBeNull()
  })
})
