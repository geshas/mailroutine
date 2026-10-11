// Domain types used by the UI. The REST wire format (snake_case, see wire.ts)
// is translated into these at the API boundary and nowhere else.

export type ActionType = 'archive' | 'trash' | 'mark_read' | 'mark_unread' | 'add_label' | 'remove_label'

export type ScheduleType = 'manual' | 'hourly' | 'daily' | 'weekly' | 'monthly'

export type RunStatus = 'success' | 'warning' | 'failed' | 'running' | 'cancelled'

export type ThemePreference = 'system' | 'light' | 'dark'

export interface Session {
  username: string
}

/** Timing facts the server reports about the current login session. */
export interface SessionInfo extends Session {
  expiresAt: string
  idleTimeoutSeconds: number
  renewalThresholdSeconds: number
}

export interface GmailAccount {
  id: string
  email: string
  status: 'connected' | 'expired'
  connectedAt: string
}

export interface Routine {
  id: string
  accountId: string
  name: string
  description?: string | undefined
  gmailQuery: string
  action: { type: ActionType; label?: string | undefined }
  schedule: {
    type: ScheduleType
    time?: string | undefined
    dayOfWeek?: number | undefined
    dayOfMonth?: number | undefined
    minute?: number | undefined
  }
  enabled: boolean
  /** Adds a MailRoutine/<Action> label to every email the routine handles. */
  traceability: boolean
  /** True while a run of this routine is in progress. */
  running: boolean
  maxMessagesPerRun: number
  lastRunAt?: string | undefined
  lastRunHandled?: number | undefined
  nextRunAt?: string | undefined
  createdAt: string
  updatedAt: string
}

export type RoutineInput = Omit<
  Routine,
  'id' | 'createdAt' | 'updatedAt' | 'lastRunAt' | 'nextRunAt' | 'lastRunHandled' | 'running'
>

export interface RunSummary {
  id: string
  routineId: string
  routineName: string
  startedAt: string
  finishedAt?: string | undefined
  matched: number
  handled: number
  failed: number
  status: RunStatus
  dryRun: boolean
  trigger: 'schedule' | 'manual'
}

export interface RunFailure {
  messageId: string
  error: string
}

export interface RunDetails extends RunSummary {
  error?: string | undefined
  /** Messages Gmail refused to modify (Gmail message IDs, never content). */
  failures: RunFailure[]
}

export interface Dashboard {
  totalRuns: number
  lastRun?: RunSummary | undefined
  nextRun?: { routineId: string; routineName: string; at: string } | undefined
  emailsHandled: number
  enabledRoutines: number
  connectedAccounts: number
  recentRuns: RunSummary[]
}

export interface PreviewMessage {
  id: string
  sender: string
  subject: string
  date: string
}

export interface PreviewResult {
  total: number
  /** True when `total` is Gmail's approximation rather than an exact count. */
  isEstimate: boolean
  /** True when more messages match than the samples shown. */
  hasMore: boolean
  samples: PreviewMessage[]
}

export interface Settings {
  timezone: string
  version: string
  license: string
  repositoryUrl: string
  documentationUrl: string
  googleRedirectUrl: string
  defaultMaxMessagesPerRun: number
}

export interface RunFilters {
  routineId?: string | undefined
  status?: RunStatus | undefined
  from?: string | undefined
  to?: string | undefined
  limit?: number | undefined
  offset?: number | undefined
}

export interface GmailLabel {
  id: string
  name: string
  type?: 'system' | 'user' | undefined
}

export interface TraceLabel {
  name: string
  /** Absent for the parent "MailRoutine" label. */
  action?: ActionType | undefined
  exists: boolean
}

export interface TraceLabelsReport {
  items: TraceLabel[]
  /** Labels created by the request that produced this report (empty for a status check). */
  created: string[]
}

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    /** Machine-readable code from the backend, e.g. "validation_failed", "account_needs_reauth". */
    public code?: string,
    /** Per-field validation messages keyed by the backend's field names. */
    public fields: Record<string, string> = {},
  ) {
    super(message)
    this.name = 'ApiError'
  }
}
