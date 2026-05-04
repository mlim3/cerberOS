import { describe, expect, test, beforeEach } from 'bun:test'
import { getActiveUserId, setActiveUserId } from './active-user'

const STORAGE_KEY = 'cerberos-active-user'
const ALICE = '11111111-1111-1111-1111-111111111111'
const BOB = '22222222-2222-2222-2222-222222222222'
const DEFAULT = '00000000-0000-0000-0000-000000000001'

// Polyfill localStorage for the bun test runtime.
class MemStore {
  store = new Map<string, string>()
  getItem(k: string): string | null { return this.store.get(k) ?? null }
  setItem(k: string, v: string): void { this.store.set(k, v) }
  removeItem(k: string): void { this.store.delete(k) }
  clear(): void { this.store.clear() }
}
;(globalThis as unknown as { localStorage: MemStore }).localStorage = new MemStore()
;(globalThis as unknown as { import: { meta: { env: Record<string, string> } } }).import = {
  meta: { env: {} },
}

beforeEach(() => {
  ;(globalThis as unknown as { localStorage: MemStore }).localStorage.clear()
})

describe('active-user', () => {
  test('falls back to default when localStorage empty and no env var', () => {
    expect(getActiveUserId()).toBe(DEFAULT)
  })

  test('returns localStorage value when valid', () => {
    setActiveUserId(ALICE)
    expect(getActiveUserId()).toBe(ALICE)
  })

  test('switching writes new uuid to localStorage', () => {
    setActiveUserId(ALICE)
    setActiveUserId(BOB)
    expect(getActiveUserId()).toBe(BOB)
  })

  test('setActiveUserId rejects non-uuid input', () => {
    setActiveUserId(ALICE)
    setActiveUserId('not-a-uuid')
    expect(getActiveUserId()).toBe(ALICE)
  })

  test('uppercase uuid in localStorage is normalized to lowercase', () => {
    ;(globalThis as unknown as { localStorage: MemStore }).localStorage.setItem(STORAGE_KEY, ALICE.toUpperCase())
    expect(getActiveUserId()).toBe(ALICE)
  })

  test('garbage value in localStorage falls back to default', () => {
    ;(globalThis as unknown as { localStorage: MemStore }).localStorage.setItem(STORAGE_KEY, 'malicious-junk')
    expect(getActiveUserId()).toBe(DEFAULT)
  })
})
