import { describe, expect, test } from 'bun:test'
import { messageLooksLikeUserCronScheduling } from './scheduling-language'

describe('messageLooksLikeUserCronScheduling', () => {
  test('reply here every minute until 9:30 wall clock', () => {
    const msg =
      'reply in this conversation/chat saying "I am waiting" every 1 minute until 9:30 PM local time'
    expect(messageLooksLikeUserCronScheduling(msg)).toBe(true)
  })

  test('reply here every minute until 9:15 wall clock', () => {
    const msg =
      'reply in this conversation/chat saying "I am waiting" every 1 minute until 9:15 PM local time'
    expect(messageLooksLikeUserCronScheduling(msg)).toBe(true)
  })

  test('every minute without digit', () => {
    expect(messageLooksLikeUserCronScheduling('ping here every minute with status')).toBe(true)
    expect(messageLooksLikeUserCronScheduling('every the minute say hi')).toBe(true)
  })

  test('plain question should not trigger', () => {
    expect(messageLooksLikeUserCronScheduling('What is cron in Linux?')).toBe(false)
  })
})
