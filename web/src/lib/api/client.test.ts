import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, notifyUnauthorized, onUnauthorized, runQuery, subscribeSession } from './client'
import { ApiError } from './types'
import { mockFetch } from '@/test/mockApi'
import {
  accountBody, dashboardBody, err, expiredAccountBody, previewBody, routineBody, runBody, sessionBody, settingsBody,
} from '@/test/fixtures'

afterEach(async () => {
  // Clear module-level CSRF/session state between tests.
  mockFetch({ 'POST /api/auth/logout': { status: 204 } })
  await api.logout()
})

async function signIn() {
  const m = mockFetch({ 'POST /api/auth/login': { body: sessionBody } })
  await api.login('admin', 'pw')
  return m
}

describe('session and CSRF', () => {
  it('adopts the CSRF token from login and sends it on writes only', async () => {
    await signIn()
    const m = mockFetch({ 'GET /api/dashboard': { body: dashboardBody }, 'POST /api/auth/logout': { status: 204 } })
    await api.dashboard()
    await api.logout()
    expect(m.calls[0]!.headers['X-CSRF-Token']).toBeUndefined()
    expect(m.calls[1]!.headers['X-CSRF-Token']).toBe('csrf-token-1')
    expect(m.fn.mock.calls[0]![1]).toMatchObject({ credentials: 'same-origin' })
    expect(m.calls[0]!.path).toBe('/api/dashboard')
  })

  it('exposes session timing and notifies subscribers; logout clears both', async () => {
    const seen: (string | undefined)[] = []
    const off = subscribeSession(() => seen.push(api.sessionInfo()?.expiresAt))
    await signIn()
    expect(api.sessionInfo()).toEqual({
      username: 'admin', expiresAt: '2099-01-01T00:00:00Z', idleTimeoutSeconds: 1800, renewalThresholdSeconds: 300,
    })
    mockFetch({ 'POST /api/auth/logout': err(500, 'internal', 'x') })
    await expect(api.logout()).rejects.toBeInstanceOf(ApiError)
    expect(api.sessionInfo()).toBeNull() // cleared even though the call failed
    off()
    expect(seen).toEqual(['2099-01-01T00:00:00Z', undefined])
  })

  it('session() returns null on 401 without announcing an expiry, and rethrows other failures', async () => {
    const heard = vi.fn()
    const off = onUnauthorized(heard)
    mockFetch({ 'GET /api/auth/session': err(401, 'unauthenticated', 'authentication required') })
    await expect(api.session()).resolves.toBeNull()
    mockFetch({ 'GET /api/auth/session': err(500, 'internal', 'boom') })
    await expect(api.session()).rejects.toMatchObject({ status: 500 })
    mockFetch({ 'GET /api/auth/session': { body: sessionBody } })
    await expect(api.session()).resolves.toEqual({ username: 'admin' })
    expect(heard).not.toHaveBeenCalled()
    off()
  })

  it('announces 401s from protected calls but not from login', async () => {
    const heard = vi.fn()
    const off = onUnauthorized(heard)
    mockFetch({ 'GET /api/dashboard': err(401, 'session_expired', 'expired'), 'POST /api/auth/login': err(401, 'invalid_credentials', 'nope') })
    await api.dashboard().catch(() => undefined)
    expect(heard).toHaveBeenCalledTimes(1)
    await expect(api.login('a', 'b')).rejects.toMatchObject({ code: 'invalid_credentials', message: 'nope' })
    expect(heard).toHaveBeenCalledTimes(1)
    off()
    notifyUnauthorized() // no listeners left: harmless
  })

  it('renewal refreshes the stored session', async () => {
    await signIn()
    const m = mockFetch({
      'POST /api/auth/session/renew': { body: { renewed: true, session: { ...sessionBody, expires_at: '2099-06-01T00:00:00Z', csrf_token: 'rotated' } } },
      'POST /api/routines': { body: routineBody },
    })
    await api.renewSession()
    expect(api.sessionInfo()?.expiresAt).toBe('2099-06-01T00:00:00Z')
    await api.createRoutine({ accountId: 'a', name: 'n', gmailQuery: 'q', action: { type: 'archive' }, schedule: { type: 'manual' }, enabled: false, traceability: true, maxMessagesPerRun: 5 })
    expect(m.last('POST', '/api/routines')!.headers['X-CSRF-Token']).toBe('rotated')
  })
})

describe('error handling', () => {
  it('parses the backend error envelope, including field messages', async () => {
    mockFetch({ 'POST /api/routines': err(400, 'validation_failed', 'the routine is not valid', { name: 'is required' }) })
    const e = (await api.createRoutine({ accountId: 'a', name: '', gmailQuery: '', action: { type: 'archive' }, schedule: { type: 'manual' }, enabled: false, traceability: true, maxMessagesPerRun: 1 }).catch((x) => x)) as ApiError
    expect(e).toBeInstanceOf(ApiError)
    expect(e).toMatchObject({ status: 400, code: 'validation_failed', message: 'the routine is not valid', fields: { name: 'is required' } })
  })

  it('falls back to the status text for unexpected JSON errors', async () => {
    mockFetch({ 'GET /api/dashboard': { status: 418, body: { whatever: true } } })
    await expect(api.dashboard()).rejects.toMatchObject({ status: 418 })
  })

  it('treats non-JSON failures as an unreachable server', async () => {
    mockFetch({ 'GET /api/dashboard': { status: 502, raw: '<html>bad gateway</html>', contentType: 'text/html' } })
    await expect(api.dashboard()).rejects.toMatchObject({ code: 'network', status: 0 })
    mockFetch({ 'GET /api/dashboard': { status: 401, raw: 'nope', contentType: 'text/plain' } })
    await expect(api.dashboard()).rejects.toMatchObject({ status: 401 })
  })

  it('reports network failures but lets aborts through', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('offline') }))
    await expect(api.dashboard()).rejects.toMatchObject({ code: 'network' })
    vi.stubGlobal('fetch', vi.fn(async () => { throw new DOMException('aborted', 'AbortError') }))
    await expect(api.dashboard()).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('rejects responses that do not match the contract instead of leaking undefined into the UI', async () => {
    mockFetch({ 'GET /api/dashboard': { body: { total_runs: 'many' } } })
    await expect(api.dashboard()).rejects.toMatchObject({ code: 'bad_response', status: 502 })
  })
})

describe('wire format translation', () => {
  it('maps accounts, flagging revoked ones as expired', async () => {
    mockFetch({ 'GET /api/accounts': { body: { items: [accountBody, expiredAccountBody] } } })
    expect(await api.listAccounts()).toEqual([
      { id: 'acc1', email: 'me@gmail.com', status: 'connected', connectedAt: '2026-01-01T00:00:00Z' },
      { id: 'acc2', email: 'old@gmail.com', status: 'expired', connectedAt: '2026-01-01T00:00:00Z' },
    ])
  })

  it('maps routines, including last-run data and omitted optionals', async () => {
    const withRun = { ...routineBody, enabled: true, last_run_at: '2026-01-03T03:00:00Z', last_run_handled: 12, next_run_at: '2026-01-04T03:00:00Z',
      action_type: 'add_label', action_config: { label: 'Done' }, schedule_type: 'weekly', schedule_config: { timezone: 'UTC', time: '06:00', weekday: 2 } }
    mockFetch({ 'GET /api/routines': { body: { items: [routineBody, withRun] } } })
    const [plain, full] = await api.listRoutines()
    expect(plain).toMatchObject({ id: 'r1', accountId: 'acc1', gmailQuery: 'label:news older_than:7d', lastRunAt: undefined, lastRunHandled: undefined, nextRunAt: undefined, description: undefined, running: false })
    expect(full).toMatchObject({
      action: { type: 'add_label', label: 'Done' }, schedule: { type: 'weekly', time: '06:00', dayOfWeek: 2 },
      lastRunHandled: 12, lastRunAt: '2026-01-03T03:00:00Z', nextRunAt: '2026-01-04T03:00:00Z', enabled: true,
    })
  })

  it('builds request bodies without inventing a timezone and with an explicit confirmation flag', async () => {
    const m = mockFetch({ 'POST /api/routines': { body: routineBody }, 'PUT /api/routines/r1': { body: routineBody } })
    const input = {
      accountId: 'acc1', name: 'N', description: 'D', gmailQuery: 'q', action: { type: 'add_label' as const, label: 'L' },
      schedule: { type: 'monthly' as const, time: '01:00', dayOfMonth: 31 }, enabled: true, traceability: false, maxMessagesPerRun: 50,
    }
    await api.createRoutine(input)
    expect(m.last('POST', '/api/routines')!.body).toEqual({
      account_id: 'acc1', name: 'N', description: 'D', gmail_query: 'q', action_type: 'add_label', action_config: { label: 'L' },
      schedule_type: 'monthly', schedule_config: { time: '01:00', day_of_month: 31 }, max_messages_per_run: 50, enabled: true, traceability: false, confirm_destructive: false,
    })
    await api.updateRoutine('r1', { ...input, action: { type: 'trash' }, schedule: { type: 'hourly', minute: 0 } }, { confirmDestructive: true })
    expect(m.last('PUT', '/api/routines/r1')!.body).toMatchObject({
      action_config: {}, schedule_config: { minute: 0 }, confirm_destructive: true,
    })
  })

  it('enable/disable/run/preview/delete hit the right endpoints with the right bodies', async () => {
    const m = mockFetch({
      'POST /api/routines/r1/enable': { body: routineBody }, 'POST /api/routines/r1/disable': { body: routineBody },
      'POST /api/routines/r1/run': { status: 202, body: { ...runBody, status: 'running' } },
      'POST /api/routines/r1/preview': { body: previewBody }, 'POST /api/routines/preview': { body: previewBody },
      'DELETE /api/routines/r1': { status: 204 }, 'DELETE /api/accounts/acc%201': { status: 204 },
      'GET /api/routines/r1': { body: routineBody },
    })
    await api.setRoutineEnabled('r1', true, { confirmDestructive: true })
    await api.setRoutineEnabled('r1', false)
    const started = await api.runRoutine('r1', { dryRun: true })
    await api.previewRoutine('r1')
    await api.previewDraft({ accountId: 'acc1', gmailQuery: 'is:unread' })
    await api.deleteRoutine('r1')
    await api.deleteAccount('acc 1')
    await api.getRoutine('r1')
    expect(m.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      'POST /api/routines/r1/enable', 'POST /api/routines/r1/disable', 'POST /api/routines/r1/run', 'POST /api/routines/r1/preview',
      'POST /api/routines/preview', 'DELETE /api/routines/r1', 'DELETE /api/accounts/acc%201', 'GET /api/routines/r1',
    ])
    expect(m.calls[0]!.body).toEqual({ confirm_destructive: true })
    expect(m.calls[2]!.body).toEqual({ dry_run: true, confirm_destructive: false })
    expect(m.calls[4]!.body).toEqual({ account_id: 'acc1', gmail_query: 'is:unread' })
    expect(started.status).toBe('running')
  })

  it('maps runs: partial → warning, cancelled stays, missing routine name is labelled', async () => {
    mockFetch({
      'GET /api/runs': { body: { items: [{ ...runBody, status: 'partial', failed_count: 1 }, { ...runBody, id: 'x', status: 'cancelled', routine_name: undefined, dry_run: true, finished_at: null }], total: 2, limit: 25, offset: 0 } },
    })
    const runs = await api.listRuns({ limit: 25 })
    expect(runs[0]).toMatchObject({ status: 'warning', handled: 5, matched: 5, failed: 1, dryRun: false, trigger: 'schedule' })
    expect(runs[1]).toMatchObject({ status: 'cancelled', routineName: '(deleted routine)', dryRun: true, finishedAt: undefined })
  })

  it('maps run details with failures only', async () => {
    mockFetch({
      'GET /api/runs/run1': {
        body: { run: { ...runBody, error_message: 'one failed' }, actions: [
          { gmail_message_id: 'a', status: 'success' }, { gmail_message_id: 'b', status: 'failed', error: 'quota' }, { gmail_message_id: 'c', status: 'failed' },
        ] },
      },
    })
    const d = await api.getRun('run1')
    expect(d.error).toBe('one failed')
    expect(d.failures).toEqual([{ messageId: 'b', error: 'quota' }, { messageId: 'c', error: '' }])
  })

  it('maps the dashboard without fabricating anything for an empty installation', async () => {
    mockFetch({ 'GET /api/dashboard': { body: { total_runs: 0, last_run: null, next_run: null, emails_handled: 0, enabled_routines: 0, connected_accounts: 0, recent_runs: [] } } })
    expect(await api.dashboard()).toEqual({ totalRuns: 0, lastRun: undefined, nextRun: undefined, emailsHandled: 0, enabledRoutines: 0, connectedAccounts: 0, recentRuns: [] })
    mockFetch({ 'GET /api/dashboard': { body: dashboardBody } })
    const d = await api.dashboard()
    expect(d).toMatchObject({ totalRuns: 7, emailsHandled: 42, nextRun: { routineId: 'r1', routineName: 'Archive news' }, lastRun: { id: 'run1' } })
  })

  it('maps previews and flags estimates', async () => {
    mockFetch({ 'POST /api/routines/preview': { body: { ...previewBody, count: 1200, count_is_estimate: true, has_more: true } } })
    const p = await api.previewDraft({ accountId: 'a', gmailQuery: 'q' })
    expect(p).toMatchObject({ total: 1200, isEstimate: true, hasMore: true })
    expect(p.samples[0]).toEqual({ id: 'm1', sender: 'News <n@example.com>', subject: 'Hello', date: '2026-01-01T00:00:00Z' })
  })

  it('maps settings and labels; saves only the timezone', async () => {
    const m = mockFetch({
      'GET /api/settings': { body: settingsBody }, 'PUT /api/settings': { body: { ...settingsBody, timezone: 'Asia/Tokyo' } },
      'GET /api/accounts/acc1/labels': { body: { items: [{ id: 'L', name: 'News', type: 'user' }, { id: 'INBOX', name: 'INBOX' }] } },
    })
    expect(await api.getSettings()).toEqual({
      timezone: 'UTC', version: '1.2.3', license: 'Apache-2.0', repositoryUrl: 'https://github.com/geshas/mailroutine',
      documentationUrl: 'https://github.com/geshas/mailroutine#readme', googleRedirectUrl: 'http://localhost:8080/api/accounts/google/callback',
      defaultMaxMessagesPerRun: 500,
    })
    expect((await api.updateSettings({ timezone: 'Asia/Tokyo' })).timezone).toBe('Asia/Tokyo')
    expect(m.last('PUT', '/api/settings')!.body).toEqual({ timezone: 'Asia/Tokyo' })
    expect(await api.listLabels('acc1')).toEqual([{ id: 'L', name: 'News', type: 'user' }, { id: 'INBOX', name: 'INBOX' }])
  })

  it('asks for the Google URL as JSON so the SPA can navigate itself', async () => {
    const m = mockFetch({ 'GET /api/accounts/google/connect': { body: { url: 'https://accounts.google.com/o/oauth2/auth?state=s' } } })
    expect(await api.connectGoogleUrl()).toEqual({ url: 'https://accounts.google.com/o/oauth2/auth?state=s' })
    expect(m.calls[0]!.headers['Accept']).toBe('application/json')
  })

  it('builds run filter queries using backend names and values', () => {
    expect(runQuery()).toBe('')
    expect(runQuery({ routineId: 'r1', status: 'warning', from: '2026-01-01T00:00:00Z', to: '2026-02-01T00:00:00Z', limit: 26, offset: 25 }))
      .toBe('?routine_id=r1&status=partial&from=2026-01-01T00%3A00%3A00Z&to=2026-02-01T00%3A00%3A00Z&limit=26&offset=25')
    expect(runQuery({ status: 'cancelled', offset: 0 })).toBe('?status=cancelled&offset=0')
  })
})

describe('waitForRun', () => {
  it('polls until the run finishes', async () => {
    let n = 0
    mockFetch({
      'GET /api/runs/run1': () => ({ body: { run: { ...runBody, status: ++n < 3 ? 'running' : 'success' }, actions: [] } }),
    })
    vi.useFakeTimers()
    const p = api.waitForRun('run1')
    await vi.advanceTimersByTimeAsync(10_000)
    expect((await p).status).toBe('success')
    expect(n).toBe(3)
    vi.useRealTimers()
  })

  it('stops when aborted', async () => {
    mockFetch({ 'GET /api/runs/run1': { body: { run: { ...runBody, status: 'running' }, actions: [] } } })
    const c = new AbortController()
    const p = api.waitForRun('run1', c.signal)
    const assertion = expect(p).rejects.toMatchObject({ name: 'AbortError' })
    await new Promise((r) => setTimeout(r, 20))
    c.abort()
    await assertion
  })
})

describe('unsafe redirect targets are refused', () => {
  it.each(['javascript:alert(1)', 'http://accounts.google.com/auth', 'data:text/html,hi', '//evil.example', 'not a url'])('%s', async (url) => {
    mockFetch({ 'GET /api/accounts/google/connect': { body: { url } } })
    await expect(api.connectGoogleUrl()).rejects.toMatchObject({ code: 'bad_response' })
  })
})
