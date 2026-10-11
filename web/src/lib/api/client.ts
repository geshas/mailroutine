import type { z } from 'zod'
import { ZodError } from 'zod'
import {
  accountsSchema, connectSchema, dashboardSchema, errorSchema, labelsSchema, previewSchema, renewSchema,
  routineSchema, routinesSchema, runDetailsSchema, runSchema, runsSchema, sessionSchema, settingsSchema,
  statusToWire, toRoutineBody, traceLabelsSchema,
} from './wire'
import { ApiError } from './types'
import type {
  Dashboard, GmailAccount, GmailLabel, PreviewResult, Routine, RoutineInput, RunDetails, RunFilters,
  RunSummary, Session, SessionInfo, Settings, TraceLabelsReport,
} from './types'

/** Every screen talks to the backend only through this interface. */
export interface MailRoutineApi {
  login(username: string, password: string): Promise<Session>
  logout(): Promise<void>
  session(): Promise<Session | null>
  /** Timing facts from the most recent session/login/renew response, if any. */
  sessionInfo(): SessionInfo | null
  renewSession(): Promise<void>
  listAccounts(): Promise<GmailAccount[]>
  connectGoogleUrl(): Promise<{ url: string }>
  deleteAccount(id: string): Promise<void>
  listLabels(accountId: string, signal?: AbortSignal): Promise<GmailLabel[]>
  /** Which MailRoutine/* labels exist in the account's Gmail. */
  getTraceLabels(accountId: string): Promise<TraceLabelsReport>
  /** Creates the MailRoutine/* labels that are missing (never duplicates existing ones). */
  createTraceLabels(accountId: string): Promise<TraceLabelsReport>
  dashboard(): Promise<Dashboard>
  listRoutines(): Promise<Routine[]>
  getRoutine(id: string): Promise<Routine>
  createRoutine(input: RoutineInput, opts?: ConfirmOpts): Promise<Routine>
  updateRoutine(id: string, input: RoutineInput, opts?: ConfirmOpts): Promise<Routine>
  deleteRoutine(id: string): Promise<void>
  setRoutineEnabled(id: string, enabled: boolean, opts?: ConfirmOpts): Promise<Routine>
  /** Starts a run in the background and returns the new (running) execution. */
  runRoutine(id: string, opts?: ConfirmOpts & { dryRun?: boolean }): Promise<RunSummary>
  /** Polls until the run is no longer running. */
  waitForRun(runId: string, signal?: AbortSignal): Promise<RunSummary>
  /** Preview a saved routine's stored query (read-only). */
  previewRoutine(id: string): Promise<PreviewResult>
  /** Preview an unsaved query (read-only). */
  previewDraft(input: { accountId: string; gmailQuery: string }): Promise<PreviewResult>
  listRuns(filters?: RunFilters, signal?: AbortSignal): Promise<RunSummary[]>
  getRun(id: string): Promise<RunDetails>
  getSettings(): Promise<Settings>
  updateSettings(s: Partial<Pick<Settings, 'timezone'>>): Promise<Settings>
}

/** Destructive actions (Trash) must be explicitly confirmed by the person using the UI. */
export interface ConfirmOpts {
  confirmDestructive?: boolean
}

/* ---------- Session-expiry notification ---------- */

type Listener = () => void
const unauthorizedListeners = new Set<Listener>()
/** Subscribe to "session expired / not authenticated" (HTTP 401 on a protected call). */
export function onUnauthorized(fn: Listener) {
  unauthorizedListeners.add(fn)
  return () => {
    unauthorizedListeners.delete(fn)
  }
}
export function notifyUnauthorized() {
  unauthorizedListeners.forEach((fn) => fn())
}

/* ---------- CSRF & session state ---------- */

// The backend returns a per-session CSRF token in the login/session/renew response bodies.
// It is echoed back in X-CSRF-Token on every state-changing request and kept only in memory.
let csrfToken: string | null = null
let currentSession: SessionInfo | null = null

const sessionListeners = new Set<Listener>()
/** For useSyncExternalStore: re-render when the session deadline moves. */
export function subscribeSession(fn: Listener) {
  sessionListeners.add(fn)
  return () => {
    sessionListeners.delete(fn)
  }
}
function setSession(info: SessionInfo | null) {
  currentSession = info
  sessionListeners.forEach((fn) => fn())
}

function adopt(s: { info: SessionInfo; csrfToken: string }): Session {
  csrfToken = s.csrfToken
  setSession(s.info)
  return { username: s.info.username }
}

const SAFE = new Set(['GET', 'HEAD', 'OPTIONS'])
// Calls that must not trigger the global "session expired" redirect.
const NO_401_REDIRECT = new Set(['/auth/login', '/auth/session'])
const NETWORK_MESSAGE = "Can't reach the MailRoutine server. Check that it is running."

async function http<T = void>(
  method: string,
  path: string,
  opts: { body?: unknown; signal?: AbortSignal | undefined; schema?: z.ZodType<T, unknown>; accept?: string } = {},
): Promise<T> {
  const headers: Record<string, string> = { Accept: opts.accept ?? 'application/json' }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  if (!SAFE.has(method) && csrfToken) headers['X-CSRF-Token'] = csrfToken

  let res: Response
  try {
    res = await fetch(`/api${path}`, {
      method,
      credentials: 'same-origin',
      headers,
      ...(opts.body !== undefined ? { body: JSON.stringify(opts.body) } : {}),
      ...(opts.signal ? { signal: opts.signal } : {}),
    })
  } catch (e) {
    if ((e as Error).name === 'AbortError') throw e
    throw new ApiError(NETWORK_MESSAGE, 0, 'network')
  }

  const isJson = (res.headers.get('content-type') ?? '').includes('application/json')
  // A non-JSON error means we hit something other than the MailRoutine backend (e.g. a proxy).
  if (!res.ok && !isJson) throw new ApiError(res.status === 401 ? 'Not signed in' : NETWORK_MESSAGE, res.status === 401 ? 401 : 0, 'network')

  if (!res.ok) {
    const parsed = errorSchema.safeParse(await res.json().catch(() => null))
    const err = parsed.success
      ? new ApiError(parsed.data.error.message, res.status, parsed.data.error.code, parsed.data.error.fields ?? {})
      : new ApiError(res.statusText || `Request failed (${res.status})`, res.status)
    if (res.status === 401 && !NO_401_REDIRECT.has(path)) notifyUnauthorized()
    throw err
  }
  if (res.status === 204 || !opts.schema) return undefined as never
  try {
    return opts.schema.parse(await res.json())
  } catch (e) {
    if (e instanceof ZodError) {
      throw new ApiError('The server sent an unexpected response. Is the web UI the same version as the server?', 502, 'bad_response')
    }
    throw e
  }
}

const enc = encodeURIComponent

export function runQuery(f: RunFilters = {}): string {
  const q = new URLSearchParams()
  if (f.routineId) q.set('routine_id', f.routineId)
  if (f.status) q.set('status', statusToWire(f.status))
  if (f.from) q.set('from', f.from)
  if (f.to) q.set('to', f.to)
  if (f.limit !== undefined) q.set('limit', String(f.limit))
  if (f.offset !== undefined) q.set('offset', String(f.offset))
  const s = q.toString()
  return s ? `?${s}` : ''
}

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const t = setTimeout(resolve, ms)
    signal?.addEventListener('abort', () => (clearTimeout(t), reject(new DOMException('Aborted', 'AbortError'))), { once: true })
  })

export const restApi: MailRoutineApi = {
  login: async (username, password) => adopt(await http('POST', '/auth/login', { body: { username, password }, schema: sessionSchema })),
  logout: async () => {
    try {
      await http('POST', '/auth/logout')
    } finally {
      csrfToken = null
      setSession(null)
    }
  },
  session: async () => {
    try {
      return adopt(await http('GET', '/auth/session', { schema: sessionSchema }))
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return null
      throw e
    }
  },
  sessionInfo: () => currentSession,
  renewSession: async () => {
    const r = await http('POST', '/auth/session/renew', { schema: renewSchema })
    adopt(r.session)
  },
  listAccounts: () => http('GET', '/accounts', { schema: accountsSchema }),
  connectGoogleUrl: () => http('GET', '/accounts/google/connect', { schema: connectSchema }),
  deleteAccount: (id) => http('DELETE', `/accounts/${enc(id)}`),
  listLabels: (accountId, signal) => http('GET', `/accounts/${enc(accountId)}/labels`, { schema: labelsSchema, signal }),
  getTraceLabels: (accountId) => http('GET', `/accounts/${enc(accountId)}/labels/mailroutine`, { schema: traceLabelsSchema }),
  createTraceLabels: (accountId) => http('POST', `/accounts/${enc(accountId)}/labels/mailroutine`, { body: {}, schema: traceLabelsSchema }),
  dashboard: () => http('GET', '/dashboard', { schema: dashboardSchema }),
  listRoutines: () => http('GET', '/routines', { schema: routinesSchema }),
  getRoutine: (id) => http('GET', `/routines/${enc(id)}`, { schema: routineSchema }),
  createRoutine: (input, opts) => http('POST', '/routines', { body: toRoutineBody(input, opts), schema: routineSchema }),
  updateRoutine: (id, input, opts) => http('PUT', `/routines/${enc(id)}`, { body: toRoutineBody(input, opts), schema: routineSchema }),
  deleteRoutine: (id) => http('DELETE', `/routines/${enc(id)}`),
  setRoutineEnabled: (id, enabled, opts) =>
    http('POST', `/routines/${enc(id)}/${enabled ? 'enable' : 'disable'}`, {
      body: { confirm_destructive: opts?.confirmDestructive ?? false },
      schema: routineSchema,
    }),
  runRoutine: (id, opts) =>
    http('POST', `/routines/${enc(id)}/run`, {
      body: { dry_run: opts?.dryRun ?? false, confirm_destructive: opts?.confirmDestructive ?? false },
      schema: runSchema,
    }),
  waitForRun: async (runId, signal) => {
    for (let delay = 800; ; delay = Math.min(delay * 1.5, 5000)) {
      const run = await restApi.getRun(runId)
      if (run.status !== 'running') return run
      await sleep(delay, signal)
    }
  },
  previewRoutine: (id) => http('POST', `/routines/${enc(id)}/preview`, { body: {}, schema: previewSchema }),
  previewDraft: ({ accountId, gmailQuery }) =>
    http('POST', '/routines/preview', { body: { account_id: accountId, gmail_query: gmailQuery }, schema: previewSchema }),
  listRuns: (f, signal) => http('GET', `/runs${runQuery(f)}`, { schema: runsSchema, signal }),
  getRun: (id) => http('GET', `/runs/${enc(id)}`, { schema: runDetailsSchema }),
  getSettings: () => http('GET', '/settings', { schema: settingsSchema }),
  updateSettings: (s) => http('PUT', '/settings', { body: s, schema: settingsSchema }),
}

/** The one backend client. There is intentionally no mock: the UI only ever shows real server data. */
export const api: MailRoutineApi = restApi
