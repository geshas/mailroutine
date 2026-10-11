import { useEffect, useSyncExternalStore } from 'react'
import { api, notifyUnauthorized, subscribeSession } from './api/client'

const ACTIVITY_EVENTS = ['pointerdown', 'keydown', 'wheel', 'touchstart'] as const
const MIN_THROTTLE_MS = 15_000
/** Used until the server has told us its own renewal threshold. */
export const RENEW_THROTTLE_MS = 5 * 60_000

/**
 * Pure throttle decision so it can be tested:
 * renew only if the user interacted after the last renewal and the throttle window has passed.
 */
export function shouldRenew(lastActivity: number, lastRenew: number, now: number, throttleMs = RENEW_THROTTLE_MS) {
  return lastActivity > lastRenew && now - lastRenew >= throttleMs
}

/** The server's own renewal threshold is the natural throttle; renewing sooner is a no-op there. */
export function throttleFor(renewalThresholdSeconds: number | undefined) {
  return renewalThresholdSeconds === undefined ? RENEW_THROTTLE_MS : Math.max(MIN_THROTTLE_MS, renewalThresholdSeconds * 1000)
}

/** Re-checks the session without extending it; a definitive "no session" signs the user out. */
export async function verifySession() {
  const s = await api.session().catch(() => 'unreachable' as const)
  if (s === null) notifyUnauthorized()
}

/**
 * Sliding-session keep-alive driven by genuine user interaction.
 * An open-but-idle tab or background polling never renews the session, and when the
 * inactivity deadline passes the user is returned to the login page.
 */
export function useSessionActivity() {
  const expiresAt = useSyncExternalStore(subscribeSession, () => api.sessionInfo()?.expiresAt)
  useEffect(() => {
    let lastRenew = Date.now()
    let inFlight = false
    const onActivity = () => {
      if (document.visibilityState !== 'visible' || inFlight) return
      const now = Date.now()
      if (!shouldRenew(now, lastRenew, now, throttleFor(api.sessionInfo()?.renewalThresholdSeconds))) return
      inFlight = true
      lastRenew = now
      // A 401 here triggers the global session-expired handler in the API client.
      api.renewSession().catch(() => {}).finally(() => (inFlight = false))
    }
    ACTIVITY_EVENTS.forEach((e) => window.addEventListener(e, onActivity, { passive: true }))
    const onVisible = () => document.visibilityState === 'visible' && void verifySession()
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      ACTIVITY_EVENTS.forEach((e) => window.removeEventListener(e, onActivity))
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [])

  // Re-armed whenever a renewal moves the deadline.
  useEffect(() => {
    if (!expiresAt) return
    const delay = Math.max(1000, new Date(expiresAt).getTime() - Date.now() + 1500)
    const t = window.setTimeout(() => void verifySession(), delay)
    return () => window.clearTimeout(t)
  }, [expiresAt])
}
