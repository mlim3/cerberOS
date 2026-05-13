import { describe, expect, test } from 'bun:test'
import { buildUserTaskWirePayload } from './wire'

describe('buildUserTaskWirePayload', () => {
  test('includes the user role and default fields', () => {
    const payload = buildUserTaskWirePayload({
      task_id: 'task-1',
      user_id: 'user-1',
      user_role: 'root',
      content: 'hello',
    })

    expect(payload.user_role).toBe('root')
    expect(payload.timeout_seconds).toBe(1800)
    expect(payload.payload).toEqual({ raw_input: 'hello' })
    expect(payload.required_skill_domains).toEqual([])
  })

  test('preserves an explicit conversation id and callback topic', () => {
    const payload = buildUserTaskWirePayload({
      task_id: 'task-2',
      user_id: 'user-2',
      content: 'hello',
      conversation_id: 'conv-1',
      callback_topic: 'topic-1',
    })

    expect(payload.conversation_id).toBe('conv-1')
    expect(payload.callback_topic).toBe('topic-1')
  })
})
