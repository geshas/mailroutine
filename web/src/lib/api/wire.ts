// Runtime-validated wire format of the Go backend and its translation to UI types.
// Anything the server sends that does not match is rejected here instead of leaking
// `undefined` into components.
import { z } from 'zod'
import type {
  Dashboard,
  GmailAccount,
  GmailLabel,
  PreviewResult,
  Routine,
  RoutineInput,
  RunDetails,
  RunStatus,
  RunSummary,
  SessionInfo,
  Settings,
  TraceLabelsReport,
} from './types'

const iso = z.string()
const optional = <T extends z.ZodType>(t: T) => t.nullish().transform((v) => v ?? undefined)

/* ---------- session ---------- */

const sessionWire = z.object({
  username: z.string(),
  expires_at: iso,
  idle_timeout_seconds: z.number(),
  renewal_threshold_seconds: z.number(),
  csrf_token: z.string(),
})
export const sessionSchema = sessionWire.transform((s) => ({
  info: {
    username: s.username,
    expiresAt: s.expires_at,
    idleTimeoutSeconds: s.idle_timeout_seconds,
    renewalThresholdSeconds: s.renewal_threshold_seconds,
  } satisfies SessionInfo,
  csrfToken: s.csrf_token,
}))
export const renewSchema = z.object({ renewed: z.boolean(), session: sessionSchema })

/* ---------- accounts / labels ---------- */

const accountWire = z.object({
  id: z.string(),
  email: z.string(),
  connection_status: z.enum(['connected', 'needs_reauth']),
  created_at: iso,
})
export const accountsSchema = z
  .object({ items: z.array(accountWire) })
  .transform(({ items }): GmailAccount[] =>
    items.map((a) => ({
      id: a.id,
      email: a.email,
      status: a.connection_status === 'connected' ? 'connected' : 'expired',
      connectedAt: a.created_at,
    })),
  )
// The SPA navigates to this URL, so only accept an https URL (never javascript:, data:, or plain http).
export const connectSchema = z.object({
  url: z.string().refine((u) => {
    try {
      return new URL(u).protocol === 'https:'
    } catch {
      return false
    }
  }, 'must be an https URL'),
})

export const labelsSchema = z
  .object({ items: z.array(z.object({ id: z.string(), name: z.string(), type: z.enum(['system', 'user']).optional() })) })
  .transform(({ items }): GmailLabel[] => items.map((l) => ({ id: l.id, name: l.name, ...(l.type ? { type: l.type } : {}) })))

/* ---------- routines ---------- */

const actionType = z.enum(['archive', 'trash', 'mark_read', 'mark_unread', 'add_label', 'remove_label'])
const scheduleType = z.enum(['manual', 'hourly', 'daily', 'weekly', 'monthly'])

const routineWire = z.object({
  id: z.string(),
  account_id: z.string(),
  name: z.string(),
  description: z.string().optional(),
  gmail_query: z.string(),
  action_type: actionType,
  action_config: z.object({ label: z.string().optional() }),
  schedule_type: scheduleType,
  schedule_config: z.object({
    timezone: z.string().optional(),
    minute: z.number().optional(),
    time: z.string().optional(),
    weekday: z.number().optional(),
    day_of_month: z.number().optional(),
  }),
  enabled: z.boolean(),
  traceability: z.boolean(),
  running: z.boolean(),
  max_messages_per_run: z.number(),
  last_run_at: optional(iso),
  last_run_handled: optional(z.number()),
  next_run_at: optional(iso),
  created_at: iso,
  updated_at: iso,
})

function toRoutine(r: z.infer<typeof routineWire>): Routine {
  const c = r.schedule_config
  return {
    id: r.id,
    accountId: r.account_id,
    name: r.name,
    description: r.description || undefined,
    gmailQuery: r.gmail_query,
    action: { type: r.action_type, label: r.action_config.label || undefined },
    schedule: { type: r.schedule_type, time: c.time, dayOfWeek: c.weekday, dayOfMonth: c.day_of_month, minute: c.minute },
    enabled: r.enabled,
    traceability: r.traceability,
    running: r.running,
    maxMessagesPerRun: r.max_messages_per_run,
    lastRunAt: r.last_run_at,
    lastRunHandled: r.last_run_handled,
    nextRunAt: r.next_run_at,
    createdAt: r.created_at,
    updatedAt: r.updated_at,
  }
}
export const routineSchema = routineWire.transform(toRoutine)
export const routinesSchema = z.object({ items: z.array(routineWire) }).transform(({ items }) => items.map(toRoutine))

/** UI routine input → request body. The timezone is deliberately omitted: the server applies the global one. */
export function toRoutineBody(i: RoutineInput, opts: { confirmDestructive?: boolean } = {}) {
  const s = i.schedule
  return {
    account_id: i.accountId,
    name: i.name,
    description: i.description ?? '',
    gmail_query: i.gmailQuery,
    action_type: i.action.type,
    action_config: i.action.label ? { label: i.action.label } : {},
    schedule_type: s.type,
    schedule_config: {
      ...(s.time !== undefined ? { time: s.time } : {}),
      ...(s.dayOfWeek !== undefined ? { weekday: s.dayOfWeek } : {}),
      ...(s.dayOfMonth !== undefined ? { day_of_month: s.dayOfMonth } : {}),
      ...(s.minute !== undefined ? { minute: s.minute } : {}),
    },
    max_messages_per_run: i.maxMessagesPerRun,
    enabled: i.enabled,
    traceability: i.traceability,
    confirm_destructive: opts.confirmDestructive ?? false,
  }
}

/* ---------- MailRoutine labels ---------- */

export const traceLabelsSchema = z
  .object({
    items: z.array(z.object({ name: z.string(), action: actionType.optional(), exists: z.boolean() })),
    created: z.array(z.string()),
  })
  .transform(
    (r): TraceLabelsReport => ({
      items: r.items.map((i) => ({ name: i.name, ...(i.action ? { action: i.action } : {}), exists: i.exists })),
      created: r.created,
    }),
  )

/* ---------- runs ---------- */

const runStatusWire = z.enum(['running', 'success', 'partial', 'failed', 'cancelled'])
const statusFromWire = (s: z.infer<typeof runStatusWire>): RunStatus => (s === 'partial' ? 'warning' : s)
export const statusToWire = (s: RunStatus) => (s === 'warning' ? 'partial' : s)

const runWire = z.object({
  id: z.string(),
  routine_id: z.string(),
  routine_name: z.string().optional(),
  started_at: iso,
  finished_at: optional(iso),
  status: runStatusWire,
  matched_count: z.number(),
  processed_count: z.number(),
  failed_count: z.number(),
  error_message: z.string().optional(),
  dry_run: z.boolean(),
  trigger: z.enum(['schedule', 'manual']),
})

function toRun(r: z.infer<typeof runWire>): RunSummary {
  return {
    id: r.id,
    routineId: r.routine_id,
    routineName: r.routine_name || '(deleted routine)',
    startedAt: r.started_at,
    finishedAt: r.finished_at,
    matched: r.matched_count,
    handled: r.processed_count,
    failed: r.failed_count,
    status: statusFromWire(r.status),
    dryRun: r.dry_run,
    trigger: r.trigger,
  }
}
export const runSchema = runWire.transform(toRun)
export const runsSchema = z.object({ items: z.array(runWire) }).transform(({ items }) => items.map(toRun))

export const runDetailsSchema = z
  .object({
    run: runWire,
    actions: z.array(z.object({ gmail_message_id: z.string(), status: z.string(), error: z.string().optional() })),
  })
  .transform(({ run, actions }): RunDetails => ({
    ...toRun(run),
    error: run.error_message || undefined,
    failures: actions.filter((a) => a.status === 'failed').map((a) => ({ messageId: a.gmail_message_id, error: a.error ?? '' })),
  }))

/* ---------- dashboard / preview / settings ---------- */

export const dashboardSchema = z
  .object({
    total_runs: z.number(),
    last_run: optional(runWire),
    next_run: optional(z.object({ routine_id: z.string(), routine_name: z.string(), at: iso })),
    emails_handled: z.number(),
    enabled_routines: z.number(),
    connected_accounts: z.number(),
    recent_runs: z.array(runWire),
  })
  .transform(
    (d): Dashboard => ({
      totalRuns: d.total_runs,
      lastRun: d.last_run ? toRun(d.last_run) : undefined,
      nextRun: d.next_run ? { routineId: d.next_run.routine_id, routineName: d.next_run.routine_name, at: d.next_run.at } : undefined,
      emailsHandled: d.emails_handled,
      enabledRoutines: d.enabled_routines,
      connectedAccounts: d.connected_accounts,
      recentRuns: d.recent_runs.map(toRun),
    }),
  )

export const previewSchema = z
  .object({
    count: z.number(),
    count_is_estimate: z.boolean(),
    has_more: z.boolean(),
    messages: z.array(z.object({ id: z.string(), from: z.string(), subject: z.string(), date: z.string() })),
  })
  .transform(
    (p): PreviewResult => ({
      total: p.count,
      isEstimate: p.count_is_estimate,
      hasMore: p.has_more,
      samples: p.messages.map((m) => ({ id: m.id, sender: m.from, subject: m.subject, date: m.date })),
    }),
  )

export const settingsSchema = z
  .object({
    timezone: z.string(),
    version: z.string(),
    license: z.string(),
    repository_url: z.string(),
    documentation_url: z.string(),
    google_redirect_url: z.string(),
    default_max_messages_per_run: z.number(),
  })
  .transform(
    (s): Settings => ({
      timezone: s.timezone,
      version: s.version,
      license: s.license,
      repositoryUrl: s.repository_url,
      documentationUrl: s.documentation_url,
      googleRedirectUrl: s.google_redirect_url,
      defaultMaxMessagesPerRun: s.default_max_messages_per_run,
    }),
  )

export const errorSchema = z.object({
  error: z.object({ code: z.string(), message: z.string(), fields: z.record(z.string(), z.string()).optional() }),
})
