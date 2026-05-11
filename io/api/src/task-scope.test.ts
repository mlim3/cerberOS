import { afterEach, describe, expect, test } from 'bun:test'
import type { StatusUpdate } from '@cerberos/io-core'
import {
  __resetTaskScopeForTests,
  broadcastStatus,
  deliverChatResponse,
  dropPendingChatCallback,
  getTaskStatus,
  listTasksForUser,
  ownerOfTask,
  persistAndBroadcastStatus,
  recordTaskOwnership,
  registerPendingChatCallback,
  scopedKey,
  setTaskStatus,
  subscribeSse,
} from './task-scope'

const ALICE = '11111111-1111-1111-1111-111111111111'
const BOB = '22222222-2222-2222-2222-222222222222'

function status(taskId: string, lastUpdate = 'working'): StatusUpdate {
  return {
    taskId,
    status: 'working',
    lastUpdate,
    expectedNextInputMinutes: null,
    timestamp: 1700000000000,
  }
}

afterEach(() => {
  __resetTaskScopeForTests()
})

describe('scopedKey — composite (userId, taskId) format', () => {
  test('lowercases both halves', () => {
    expect(scopedKey(ALICE.toUpperCase(), 'TASK-13')).toBe(`${ALICE}:task-13`)
  })

  test('trims surrounding whitespace', () => {
    expect(scopedKey(`  ${ALICE}  `, '  t-1  ')).toBe(`${ALICE}:t-1`)
  })

  test('different users with same taskId produce different keys', () => {
    expect(scopedKey(ALICE, 'shared-id')).not.toBe(scopedKey(BOB, 'shared-id'))
  })
})

describe('taskOwnership reverse index', () => {
  test('recordTaskOwnership / ownerOfTask round-trip', () => {
    recordTaskOwnership(ALICE, 'task-13')
    expect(ownerOfTask('task-13')).toBe(ALICE)
  })

  test('ownerOfTask is case-insensitive on the taskId', () => {
    recordTaskOwnership(ALICE, 'Task-13')
    expect(ownerOfTask('TASK-13')).toBe(ALICE)
  })

  test('unknown task returns undefined (not a fallback owner)', () => {
    expect(ownerOfTask('never-registered')).toBeUndefined()
  })

  test('re-registering with a different user overwrites — last writer wins', () => {
    recordTaskOwnership(ALICE, 'task-x')
    recordTaskOwnership(BOB, 'task-x')
    expect(ownerOfTask('task-x')).toBe(BOB)
  })
})

describe('tasks map — per-user isolation', () => {
  test('setTaskStatus then getTaskStatus round-trips for the owner only', () => {
    setTaskStatus(ALICE, 't-1', status('t-1'))
    expect(getTaskStatus(ALICE, 't-1')).toBeDefined()
    expect(getTaskStatus(BOB, 't-1')).toBeUndefined()
  })

  test('two users with identical taskIds do NOT collide', () => {
    setTaskStatus(ALICE, 'same-id', status('same-id', 'alice-update'))
    setTaskStatus(BOB, 'same-id', status('same-id', 'bob-update'))
    expect(getTaskStatus(ALICE, 'same-id')?.lastUpdate).toBe('alice-update')
    expect(getTaskStatus(BOB, 'same-id')?.lastUpdate).toBe('bob-update')
  })

  test('listTasksForUser returns only that user\'s tasks', () => {
    setTaskStatus(ALICE, 'a-1', status('a-1'))
    setTaskStatus(ALICE, 'a-2', status('a-2'))
    setTaskStatus(BOB, 'b-1', status('b-1'))
    const aliceList = listTasksForUser(ALICE)
    expect(aliceList).toHaveLength(2)
    expect(aliceList.every((t) => t.taskId.startsWith('a-'))).toBe(true)
  })

  test('listTasksForUser does NOT leak across users sharing taskId prefix', () => {
    setTaskStatus(ALICE, 'shared', status('shared'))
    setTaskStatus(BOB, 'shared', status('shared', 'bob-version'))
    const bobList = listTasksForUser(BOB)
    expect(bobList).toHaveLength(1)
    expect(bobList[0].lastUpdate).toBe('bob-version')
  })
})

describe('SSE subscribers — per-user isolation', () => {
  test('broadcast to user A does not reach user B subscribing to the same taskId', () => {
    const aReceived: Uint8Array[] = []
    const bReceived: Uint8Array[] = []
    const unA = subscribeSse(ALICE, 'shared', (b) => { aReceived.push(b) })
    const unB = subscribeSse(BOB, 'shared', (b) => { bReceived.push(b) })

    broadcastStatus(ALICE, 'shared', status('shared'))

    expect(aReceived.length).toBe(1)
    expect(bReceived.length).toBe(0)
    unA()
    unB()
  })

  test('unsubscribe cleans up the per-(userId, taskId) slot', () => {
    const seen: Uint8Array[] = []
    const unsubscribe = subscribeSse(ALICE, 't-1', (b) => { seen.push(b) })
    unsubscribe()
    broadcastStatus(ALICE, 't-1', status('t-1'))
    expect(seen.length).toBe(0)
  })
})

describe('pendingChatResponses — per-user isolation', () => {
  test('deliverChatResponse(A, taskId) only fires the callback registered under A', () => {
    let aCalls = 0
    let bCalls = 0
    registerPendingChatCallback(ALICE, 'shared', {
      push: () => { aCalls += 1 },
      complete: () => {},
      error: () => {},
    })
    registerPendingChatCallback(BOB, 'shared', {
      push: () => { bCalls += 1 },
      complete: () => {},
      error: () => {},
    })

    deliverChatResponse(ALICE, 'shared', 'hi', false)

    expect(aCalls).toBe(1)
    expect(bCalls).toBe(0)
  })

  test('done=true removes the callback so subsequent deliveries are dropped', () => {
    let calls = 0
    let completed = false
    registerPendingChatCallback(ALICE, 't-1', {
      push: () => { calls += 1 },
      complete: () => { completed = true },
      error: () => {},
    })

    expect(deliverChatResponse(ALICE, 't-1', 'final', true)).toBe(true)
    expect(completed).toBe(true)
    expect(deliverChatResponse(ALICE, 't-1', 'late', false)).toBe(false)
    expect(calls).toBe(1)
  })

  test('dropPendingChatCallback evicts under the composite key, not by taskId', () => {
    registerPendingChatCallback(ALICE, 'shared', {
      push: () => {}, complete: () => {}, error: () => {},
    })
    registerPendingChatCallback(BOB, 'shared', {
      push: () => {}, complete: () => {}, error: () => {},
    })

    dropPendingChatCallback(ALICE, 'shared')

    expect(deliverChatResponse(ALICE, 'shared', 'x', false)).toBe(false)
    expect(deliverChatResponse(BOB, 'shared', 'x', false)).toBe(true)
  })
})

describe('persistAndBroadcastStatus — writes and broadcasts under the owner\'s slot', () => {
  test('after persist, only owner\'s getTaskStatus and SSE subscriber see the update', () => {
    const aReceived: Uint8Array[] = []
    const bReceived: Uint8Array[] = []
    subscribeSse(ALICE, 'cross-user', (b) => { aReceived.push(b) })
    subscribeSse(BOB, 'cross-user', (b) => { bReceived.push(b) })

    persistAndBroadcastStatus(ALICE, status('cross-user'))

    expect(getTaskStatus(ALICE, 'cross-user')).toBeDefined()
    expect(getTaskStatus(BOB, 'cross-user')).toBeUndefined()
    expect(aReceived.length).toBe(1)
    expect(bReceived.length).toBe(0)
  })
})

describe('MT-3 attacker scenario: guess-the-taskId across users', () => {
  test('Bob registers task-13; Alice cannot peek at it via getTaskStatus', () => {
    recordTaskOwnership(BOB, 'task-13')
    setTaskStatus(BOB, 'task-13', status('task-13', 'bob private'))
    // Alice has the same taskId in her head (e.g. saw it in logs) and asks IO
    expect(getTaskStatus(ALICE, 'task-13')).toBeUndefined()
    // Reverse index still tells the server who owns it (for orchestrator routing)
    // but Alice can't claim it just by setting X-Active-User to herself.
    expect(ownerOfTask('task-13')).toBe(BOB)
  })

  test('Bob has a parked chat stream; Alice cannot intercept the orchestrator reply', () => {
    const bobChunks: string[] = []
    recordTaskOwnership(BOB, 'task-13')
    registerPendingChatCallback(BOB, 'task-13', {
      push: (c) => { bobChunks.push(c) },
      complete: () => {},
      error: () => {},
    })

    // Simulated attacker: Alice tries to drain Bob's slot using her own userId
    expect(deliverChatResponse(ALICE, 'task-13', 'malicious', false)).toBe(false)
    expect(bobChunks.length).toBe(0)

    // Owner-resolved delivery (what the orchestrator inbound path actually does) works
    const owner = ownerOfTask('task-13')!
    expect(deliverChatResponse(owner, 'task-13', 'real reply', false)).toBe(true)
    expect(bobChunks).toEqual(['real reply'])
  })
})
