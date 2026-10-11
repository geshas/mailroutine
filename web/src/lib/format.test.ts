import { describe, expect, it, vi } from 'vitest'
import {
  ACTION_LABELS, emptyQueryBuilder, queryTargetsInbox, windowIsEmpty, DAYS, SCHEDULE_LABELS, buildGmailQuery, describeAction, describeSchedule, duration, fullDate, num, relativeDay,
} from './format'

const base = { inboxOnly: false, label: '', olderThan: '', olderUnit: 'd' as const, from: '', after: '', before: '', read: 'any' as const, starred: 'any' as const }

describe('buildGmailQuery', () => {
  it('builds label + older_than', () => {
    expect(buildGmailQuery({ ...base, label: 'Newsletters', olderThan: '14' })).toBe('label:Newsletters older_than:14d')
  })
  it('adds read and not-starred filters', () => {
    expect(buildGmailQuery({ ...base, from: 'amazon.com', read: 'unread', starred: 'not_starred' })).toBe('from:amazon.com is:unread -is:starred')
  })
  it('handles every option and ignores blanks', () => {
    expect(buildGmailQuery(base)).toBe('')
    expect(buildGmailQuery({ ...base, label: 'My Receipts', olderThan: '2', olderUnit: 'y', read: 'read', starred: 'starred' }))
      .toBe('label:My-Receipts older_than:2y is:read is:starred')
    expect(buildGmailQuery({ ...base, olderThan: '0' })).toBe('') // 0 means "any age", never a default
  })
  it('builds a date window with Gmail\'s YYYY/MM/DD syntax', () => {
    expect(buildGmailQuery({ ...base, after: '2026-01-05' })).toBe('after:2026/01/05')
    expect(buildGmailQuery({ ...base, before: '2026-02-01' })).toBe('before:2026/02/01')
    expect(buildGmailQuery({ ...base, label: 'News', after: '2026-01-01', before: '2026-02-01', olderThan: '1', olderUnit: 'm' }))
      .toBe('label:News older_than:1m after:2026/01/01 before:2026/02/01')
  })
  it('can restrict a search to the Inbox', () => {
    expect(buildGmailQuery({ ...base, inboxOnly: true })).toBe('in:inbox')
    expect(buildGmailQuery({ ...base, inboxOnly: true, label: 'News', olderThan: '7' })).toBe('in:inbox label:News older_than:7d')
    expect(queryTargetsInbox('in:inbox older_than:7d')).toBe(true)
    expect(queryTargetsInbox('label:INBOX')).toBe(true)
    expect(queryTargetsInbox('label:inbox-old')).toBe(false)
    expect(queryTargetsInbox('from:a.com')).toBe(false)
  })
  it('detects windows that can match nothing', () => {
    expect(windowIsEmpty({ after: '2026-02-01', before: '2026-01-01' })).toBe(true)
    expect(windowIsEmpty({ after: '2026-01-01', before: '2026-01-01' })).toBe(true)
    expect(windowIsEmpty({ after: '2026-01-01', before: '2026-01-02' })).toBe(false)
    expect(windowIsEmpty({ after: '', before: '2026-01-02' })).toBe(false)
    expect(emptyQueryBuilder.after).toBe('')
  })
})

describe('descriptions', () => {
  it('describes schedules', () => {
    expect(describeSchedule({ type: 'manual' })).toBe('Manual only')
    expect(describeSchedule({ type: 'hourly', minute: 5 })).toBe('Hourly at :05')
    expect(describeSchedule({ type: 'hourly' })).toBe('Hourly at :00')
    expect(describeSchedule({ type: 'daily', time: '03:00' })).toBe('Daily at 03:00')
    expect(describeSchedule({ type: 'daily' })).toBe('Daily at 00:00')
    expect(describeSchedule({ type: 'weekly', time: '09:30', dayOfWeek: 1 })).toBe('Weekly on Monday at 09:30')
    expect(describeSchedule({ type: 'monthly', time: '01:00', dayOfMonth: 31 })).toBe('Monthly on day 31 at 01:00')
    expect(DAYS).toHaveLength(7)
    expect(Object.keys(SCHEDULE_LABELS)).toHaveLength(5)
  })
  it('describes actions', () => {
    expect(describeAction({ type: 'archive' })).toBe('Archive')
    expect(describeAction({ type: 'add_label', label: 'Done' })).toBe('Add label “Done”')
    expect(Object.keys(ACTION_LABELS)).toHaveLength(6)
  })
})

describe('number and date helpers', () => {
  it('formats numbers with grouping', () => {
    expect(num(0)).toBe('0')
    expect(num(1234567)).toBe('1,234,567')
  })
  it('computes durations', () => {
    expect(duration('2026-01-01T00:00:00Z', undefined)).toBe('—')
    expect(duration('2026-01-01T00:00:00Z', '2026-01-01T00:00:42Z')).toBe('42s')
    expect(duration('2026-01-01T00:00:00Z', '2026-01-01T00:02:05Z')).toBe('2m 5s')
    expect(duration('2026-01-01T00:00:10Z', '2026-01-01T00:00:00Z')).toBe('0s')
  })
  it('renders relative days around "now"', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 5, 15, 12, 0))
    const at = (d: number, h = 9) => new Date(2026, 5, 15 + d, h, 5).toISOString()
    expect(relativeDay(undefined)).toBe('—')
    expect(relativeDay(at(0))).toMatch(/^Today, 09:05/)
    expect(relativeDay(at(-1))).toMatch(/^Yesterday/)
    expect(relativeDay(at(1))).toMatch(/^Tomorrow/)
    expect(relativeDay(at(5))).not.toMatch(/Today|Tomorrow|Yesterday/)
    vi.useRealTimers()
  })
  it('renders full dates', () => {
    expect(fullDate(undefined)).toBe('—')
    expect(fullDate('2026-03-01T10:00:00Z')).toMatch(/2026/)
  })
})
