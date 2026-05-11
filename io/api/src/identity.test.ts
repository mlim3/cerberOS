import { describe, expect, test } from 'bun:test'
import { activeUserId, assertNoUserIdOverride, userIdRequired } from './identity'

const VALID_UUID = '11111111-1111-1111-1111-111111111111'
const OTHER_UUID = '22222222-2222-2222-2222-222222222222'

function fakeCtx(headers: Record<string, string | undefined>, query: Record<string, string | undefined> = {}) {
  return {
    req: {
      header: (name: string) => headers[name] ?? headers[name.toLowerCase()],
      query: (name: string) => query[name],
    },
    json: (obj: unknown, status: number) => {
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

describe('assertNoUserIdOverride — MT-2 hard reject body/query userId mismatch', () => {
  test('body without userId → null (pass-through)', () => {
    const ctx = fakeCtx({})
    expect(assertNoUserIdOverride(ctx, { title: 'hi' }, VALID_UUID)).toBeNull()
  })

  test('null body → null (pass-through, used by DELETE/GET writes)', () => {
    const ctx = fakeCtx({})
    expect(assertNoUserIdOverride(ctx, null, VALID_UUID)).toBeNull()
  })

  test('body.userId matches active user → null (silent accept, back-compat)', () => {
    const ctx = fakeCtx({})
    expect(assertNoUserIdOverride(ctx, { userId: VALID_UUID }, VALID_UUID)).toBeNull()
  })

  test('body.userId matches active user (case-insensitive) → null', () => {
    const ctx = fakeCtx({})
    expect(assertNoUserIdOverride(ctx, { userId: VALID_UUID.toUpperCase() }, VALID_UUID)).toBeNull()
  })

  test('body.userId differs from active user → 403', async () => {
    const ctx = fakeCtx({})
    const res = assertNoUserIdOverride(ctx, { userId: OTHER_UUID }, VALID_UUID)
    expect(res).not.toBeNull()
    expect(res!.status).toBe(403)
    const errBody = await res!.json() as { error: string }
    expect(errBody.error).toContain('X-Active-User')
  })

  test('attacker scenario: body.userId=<victim> + X-Active-User=<attacker> → 403', async () => {
    const attacker = VALID_UUID
    const victim = OTHER_UUID
    const ctx = fakeCtx({ 'X-Active-User': attacker })
    expect(activeUserId(ctx)).toBe(attacker)
    const res = assertNoUserIdOverride(ctx, { userId: victim }, attacker)
    expect(res).not.toBeNull()
    expect(res!.status).toBe(403)
  })

  test('query string userId mismatch → 403', () => {
    const ctx = fakeCtx({}, { userId: OTHER_UUID })
    const res = assertNoUserIdOverride(ctx, {}, VALID_UUID)
    expect(res).not.toBeNull()
    expect(res!.status).toBe(403)
  })

  test('query string userId matches active user → null', () => {
    const ctx = fakeCtx({}, { userId: VALID_UUID })
    expect(assertNoUserIdOverride(ctx, {}, VALID_UUID)).toBeNull()
  })

  test('non-object body (string/number) → null (nothing to override)', () => {
    const ctx = fakeCtx({})
    expect(assertNoUserIdOverride(ctx, 'oops', VALID_UUID)).toBeNull()
    expect(assertNoUserIdOverride(ctx, 42, VALID_UUID)).toBeNull()
  })

  test('non-string body.userId is ignored (no false 403)', () => {
    const ctx = fakeCtx({})
    expect(assertNoUserIdOverride(ctx, { userId: 12345 }, VALID_UUID)).toBeNull()
    expect(assertNoUserIdOverride(ctx, { userId: null }, VALID_UUID)).toBeNull()
  })

  test('whitespace-padded matching userId → null', () => {
    const ctx = fakeCtx({})
    expect(assertNoUserIdOverride(ctx, { userId: `  ${VALID_UUID}  ` }, VALID_UUID)).toBeNull()
  })
})
