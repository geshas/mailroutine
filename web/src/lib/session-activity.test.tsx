import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, onUnauthorized } from './api/client'
import { RENEW_THROTTLE_MS, shouldRenew, throttleFor, useSessionActivity, verifySession } from './session-activity'
import { mockFetch } from '@/test/mockApi'
import { err, sessionBody } from '@/test/fixtures'

describe('renewal decision', () => {
  it('does not renew when idle (no activity since last renewal)', () => {
    expect(shouldRenew(1000, 2000, 2000 + RENEW_THROTTLE_MS * 10)).toBe(false)
  })
  it('throttles renewal within the window even with activity', () => {
    expect(shouldRenew(5000, 1000, 1000 + RENEW_THROTTLE_MS - 1)).toBe(false)
  })
  it('renews after activity once the window has passed', () => {
    const now = 1000 + RENEW_THROTTLE_MS
    expect(shouldRenew(now, 1000, now)).toBe(true)
  })
  it('follows the server-reported threshold, with a floor', () => {
    expect(throttleFor(undefined)).toBe(RENEW_THROTTLE_MS)
    expect(throttleFor(600)).toBe(600_000)
    expect(throttleFor(1)).toBe(15_000)
    expect(shouldRenew(100_000, 0, 100_000, 60_000)).toBe(true)
  })
})

describe('verifySession', () => {
  it('signs the user out only when the server says there is no session', async () => {
    const heard = vi.fn()
    const off = onUnauthorized(heard)
    mockFetch({ 'GET /api/auth/session': err(401, 'session_expired', 'x') })
    await verifySession()
    expect(heard).toHaveBeenCalledTimes(1)
    mockFetch({ 'GET /api/auth/session': { body: sessionBody } })
    await verifySession()
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('offline') }))
    await verifySession() // a network blip must not log anyone out
    expect(heard).toHaveBeenCalledTimes(1)
    off()
  })
})

describe('useSessionActivity', () => {
  beforeEach(() => { vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] }) })
  afterEach(() => { vi.useRealTimers() })

  const signIn = async () => {
    mockFetch({ 'POST /api/auth/login': { body: { ...sessionBody, renewal_threshold_seconds: 60, expires_at: new Date(Date.now() + 1_800_000).toISOString() } } })
    await api.login('admin', 'pw')
  }

  it('renews only after real interaction and only once per window', async () => {
    await signIn()
    const m = mockFetch({
      'POST /api/auth/session/renew': { body: { renewed: true, session: { ...sessionBody, renewal_threshold_seconds: 60, expires_at: new Date(Date.now() + 3_600_000).toISOString() } } },
    })
    renderHook(() => useSessionActivity())
    // Time passing and background work alone never renew the session.
    await act(async () => { vi.advanceTimersByTime(10 * 60_000) })
    expect(m.count('POST', '/api/auth/session/renew')).toBe(0)

    await act(async () => { window.dispatchEvent(new Event('pointerdown')) })
    expect(m.count('POST', '/api/auth/session/renew')).toBe(1)
    await act(async () => { window.dispatchEvent(new Event('keydown')); window.dispatchEvent(new Event('wheel')) })
    expect(m.count('POST', '/api/auth/session/renew')).toBe(1) // throttled

    await act(async () => { vi.advanceTimersByTime(61_000) })
    await act(async () => { window.dispatchEvent(new Event('touchstart')) })
    expect(m.count('POST', '/api/auth/session/renew')).toBe(2)
  })

  it('ignores activity in a hidden tab', async () => {
    await signIn()
    const m = mockFetch({ 'POST /api/auth/session/renew': { body: { renewed: false, session: sessionBody } } })
    renderHook(() => useSessionActivity())
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    await act(async () => { vi.advanceTimersByTime(10 * 60_000); window.dispatchEvent(new Event('pointerdown')) })
    expect(m.count('POST', '/api/auth/session/renew')).toBe(0)
  })

  it('checks the session at the inactivity deadline and when the tab becomes visible', async () => {
    await signIn()
    const heard = vi.fn()
    const off = onUnauthorized(heard)
    const m = mockFetch({ 'GET /api/auth/session': err(401, 'session_expired', 'x') })
    renderHook(() => useSessionActivity())
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(m.count('GET', '/api/auth/session')).toBe(1)
    expect(heard).toHaveBeenCalledTimes(1)
    await act(async () => { vi.advanceTimersByTime(1_900_000) }) // past the 30 minute deadline
    expect(m.count('GET', '/api/auth/session')).toBe(2)
    off()
  })

  it('stops listening on unmount', async () => {
    await signIn()
    const m = mockFetch({ 'POST /api/auth/session/renew': { body: { renewed: true, session: sessionBody } } })
    const { unmount } = renderHook(() => useSessionActivity())
    unmount()
    await act(async () => { vi.advanceTimersByTime(10 * 60_000); window.dispatchEvent(new Event('pointerdown')) })
    expect(m.count('POST', '/api/auth/session/renew')).toBe(0)
  })
})
