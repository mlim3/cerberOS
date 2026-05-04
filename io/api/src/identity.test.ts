import { describe, expect, test } from 'bun:test'
import { activeUserId, userIdRequired } from './identity'

const VALID_UUID = '11111111-1111-1111-1111-111111111111'

function fakeCtx(headers: Record<string, string | undefined>) {
  return {
    req: {
      header: (name: string) => headers[name] ?? headers[name.toLowerCase()],
    },
    json: (obj: unknown, status: 400) => {
      return new Response(JSON.stringify(obj), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
    },
  }
}

describe('activeUserId — X-Active-User is the only identity source', () => {
  test('missing header → null', () => {
    expect(activeUserId(fakeCtx({}))).toBeNull()
  })

  test('empty header → null', () => {
    expect(activeUserId(fakeCtx({ 'X-Active-User': '' }))).toBeNull()
  })

  test('whitespace-only header → null', () => {
    expect(activeUserId(fakeCtx({ 'X-Active-User': '   ' }))).toBeNull()
  })

  test('non-UUID value → null', () => {
    expect(activeUserId(fakeCtx({ 'X-Active-User': 'not-a-uuid' }))).toBeNull()
  })

  test('SQL-ish injection attempt → null', () => {
    expect(activeUserId(fakeCtx({ 'X-Active-User': "1' OR 1=1 --" }))).toBeNull()
  })

  test('valid UUID → lowercase string', () => {
    expect(activeUserId(fakeCtx({ 'X-Active-User': VALID_UUID }))).toBe(VALID_UUID)
  })

  test('valid uppercase UUID → normalized to lowercase', () => {
    expect(activeUserId(fakeCtx({ 'X-Active-User': VALID_UUID.toUpperCase() }))).toBe(VALID_UUID)
  })

  test('valid UUID with surrounding whitespace → trimmed', () => {
    expect(activeUserId(fakeCtx({ 'X-Active-User': `  ${VALID_UUID}  ` }))).toBe(VALID_UUID)
  })

  test('legacy X-User-Id header is NOT honored', () => {
    expect(activeUserId(fakeCtx({ 'X-User-Id': VALID_UUID }))).toBeNull()
  })
})

describe('userIdRequired — 400 response shape', () => {
  test('returns status 400', async () => {
    const ctx = fakeCtx({})
    const res = userIdRequired(ctx)
    expect(res.status).toBe(400)
  })

  test('error body mentions X-Active-User', async () => {
    const ctx = fakeCtx({})
    const res = userIdRequired(ctx)
    const body = await res.json() as { error: string }
    expect(body.error).toContain('X-Active-User')
  })
})
