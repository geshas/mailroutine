// Validates the UI's schemas against JSON recorded from the real Go server:
//   CONTRACT_DIR=$PWD/web/.contract go test ./internal/http -run TestWriteContractFixtures
//   cd web && CONTRACT_DIR=$PWD/.contract npm test
// Skipped when no recording is available (a plain `npm test`); CI runs both halves.
import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'
import {
  accountsSchema, dashboardSchema, errorSchema, labelsSchema, previewSchema, renewSchema, routineSchema, routinesSchema,
  runDetailsSchema, runSchema, runsSchema, sessionSchema, settingsSchema, traceLabelsSchema,
} from '@/lib/api/wire'
import { TRACE_LABELS } from '@/lib/format'

const dir = process.env['CONTRACT_DIR']
const load = (name: string): unknown => JSON.parse(readFileSync(path.join(dir!, `${name}.json`), 'utf8'))

describe.skipIf(!dir || !existsSync(dir))('real server responses match the UI schemas', () => {
  it('session endpoints', () => {
    const s = sessionSchema.parse(load('login'))
    expect(s.info.username).toBe('admin')
    expect(s.csrfToken.length).toBeGreaterThan(20)
    expect(s.info.renewalThresholdSeconds).toBeGreaterThan(0)
    sessionSchema.parse(load('session'))
    expect(renewSchema.parse(load('renew')).renewed).toBe(false)
  })

  it('accounts, labels, settings', () => {
    const accounts = accountsSchema.parse(load('accounts'))
    expect(accounts).toHaveLength(1)
    expect(accounts[0]!.status).toBe('connected')
    expect(labelsSchema.parse(load('labels')).some((l) => l.name === 'Newsletters')).toBe(true)
    const settings = settingsSchema.parse(load('settings'))
    expect(settings.license).toBe('Apache-2.0')
    expect(settings.repositoryUrl).toMatch(/^https:\/\//)
    expect(settingsSchema.parse(load('put_settings')).timezone).toBe('Europe/Paris')
  })

  it('MailRoutine labels: names match the UI constants and creation is reported', () => {
    const before = traceLabelsSchema.parse(load('trace_labels'))
    expect(before.items[0]).toEqual({ name: 'MailRoutine', exists: false })
    expect(before.created).toEqual([])
    const byAction = Object.fromEntries(before.items.filter((i) => i.action).map((i) => [i.action, i.name]))
    expect(byAction).toEqual(TRACE_LABELS) // UI copy of the label names cannot drift from the server
    const after = traceLabelsSchema.parse(load('trace_labels_created'))
    expect(after.items.every((i) => i.exists)).toBe(true)
    expect(after.created).toHaveLength(7)
  })

  it('routines carry real last-run data after a run', () => {
    const fresh = routineSchema.parse(load('routine_new'))
    expect(fresh.lastRunHandled).toBeUndefined()
    expect(fresh.traceability).toBe(true) // on by default
    expect(fresh.nextRunAt).toBeDefined()
    const [r] = routinesSchema.parse(load('routines'))
    expect(r!.lastRunHandled).toBe(3)
    expect(r!.lastRunAt).toBeDefined()
    expect(routineSchema.parse(load('routine')).schedule).toMatchObject({ type: 'daily', time: '03:00' })
  })

  it('runs and run details', () => {
    const run = runSchema.parse(load('run_started'))
    expect(run.status).toBe('running')
    expect(run.routineName).toBe('Archive news')
    const [first] = runsSchema.parse(load('runs'))
    expect(first).toMatchObject({ status: 'success', matched: 3, handled: 3, failed: 0, dryRun: false })
    const details = runDetailsSchema.parse(load('run'))
    expect(details.failures).toEqual([{ messageId: 'm-failed', error: 'gmail error 404' }])
  })

  it('dashboard: zeros when empty, real figures after a run', () => {
    expect(dashboardSchema.parse(load('dashboard_empty'))).toEqual({
      totalRuns: 0, lastRun: undefined, nextRun: undefined, emailsHandled: 0, enabledRoutines: 0, connectedAccounts: 1, recentRuns: [],
    })
    const d = dashboardSchema.parse(load('dashboard'))
    expect(d).toMatchObject({ totalRuns: 1, emailsHandled: 3, enabledRoutines: 1 })
    expect(d.nextRun).toMatchObject({ routineName: 'Archive news' })
    expect(d.lastRun?.id).toBe(d.recentRuns[0]!.id)
  })

  it('preview and errors', () => {
    const p = previewSchema.parse(load('preview'))
    expect(p.samples[0]).toMatchObject({ sender: 'sender@example.com', subject: 'Subj m1' })
    expect(p.isEstimate).toBe(false)
    const e = errorSchema.parse(load('error_validation'))
    expect(e.error.code).toBe('validation_failed')
    expect(Object.keys(e.error.fields ?? {}).length).toBeGreaterThan(0)
  })
})
