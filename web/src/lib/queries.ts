import { queryOptions } from '@tanstack/react-query'
import { api } from './api/client'
import type { RunFilters } from './api/types'

/** Dashboard and routine lists poll with GET only; polling never extends the login session. */
export const POLL_MS = 30_000

export const q = {
  dashboard: () => queryOptions({ queryKey: ['dashboard'], queryFn: () => api.dashboard(), refetchInterval: POLL_MS }),
  routines: () => queryOptions({ queryKey: ['routines'], queryFn: () => api.listRoutines(), refetchInterval: POLL_MS }),
  routine: (id: string) => queryOptions({ queryKey: ['routines', id], queryFn: () => api.getRoutine(id) }),
  traceLabels: (accountId: string) => queryOptions({ queryKey: ['trace-labels', accountId], queryFn: () => api.getTraceLabels(accountId) }),
  accounts: () => queryOptions({ queryKey: ['accounts'], queryFn: () => api.listAccounts() }),
  runs: (f: RunFilters = {}) =>
    queryOptions({ queryKey: ['runs', f], queryFn: ({ signal }) => api.listRuns(f, signal), refetchInterval: POLL_MS }),
  run: (id: string) => queryOptions({ queryKey: ['run', id], queryFn: () => api.getRun(id) }),
  settings: () => queryOptions({ queryKey: ['settings'], queryFn: () => api.getSettings() }),
}
