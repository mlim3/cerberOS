import { describe, expect, test } from 'bun:test'
import { parseTraceparent, resolveTraceparent } from './trace-context'

describe('resolveTraceparent', () => {
  test('incoming valid header keeps trace_id but uses new parent span id', () => {
    const incoming =
      '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'
    const resolved = resolveTraceparent(incoming)
    expect(resolved.traceId).toBe('4bf92f3577b34da6a3ce929d0e0e4736')

    const roundTrip = parseTraceparent(resolved.traceparent)
    expect(roundTrip).not.toBeNull()
    expect(roundTrip!.traceId).toBe(resolved.traceId)
    expect(roundTrip!.parentId).not.toBe('00f067aa0ba902b7')
  })

  test('missing header creates new trace', () => {
    const a = resolveTraceparent(undefined)
    const b = parseTraceparent(a.traceparent)
    expect(b).not.toBeNull()
    expect(b!.traceId).toBe(a.traceId)
  })
})
