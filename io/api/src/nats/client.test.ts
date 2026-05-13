import { describe, expect, test } from 'bun:test'
import { buildUserTaskNatsPayload } from './client'

describe('buildUserTaskNatsPayload', () => {
  test('defaults user_context_id to user_id when absent', () => {
    const payload = buildUserTaskNatsPayload({
      task_id: 'task-1',
      user_id: 'user-123',
      content: 'hello',
    })

    expect(payload.user_context_id).toBe('user-123')
    expect(payload.user_id).toBe('user-123')
    expect(payload.required_skill_domains).toEqual([])
    expect(payload.timeout_seconds).toBe(1800)
  })

  test('preserves an explicit non-empty user_context_id', () => {
    const payload = buildUserTaskNatsPayload({
      task_id: 'task-2',
      user_id: 'user-123',
      content: 'hello',
      user_context_id: 'ctx-456',
      required_skill_domains: ['web'],
      timeout_seconds: 90,
    })

    expect(payload.user_context_id).toBe('ctx-456')
    expect(payload.required_skill_domains).toEqual(['web'])
    expect(payload.timeout_seconds).toBe(90)
  })

  test('falls back to user_id when user_context_id is blank', () => {
    const payload = buildUserTaskNatsPayload({
      task_id: 'task-3',
      user_id: 'user-123',
      content: 'hello',
      user_context_id: '   ',
    })

    expect(payload.user_context_id).toBe('user-123')
  })
})
