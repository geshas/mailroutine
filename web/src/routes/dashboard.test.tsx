import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { renderApp } from '@/test/renderApp'
import { mockFetch } from '@/test/mockApi'
import { signedIn } from '@/test/base'
import { dashboardBody, err, runBody } from '@/test/fixtures'

const empty = { total_runs: 0, last_run: null, next_run: null, emails_handled: 0, enabled_routines: 0, connected_accounts: 1, recent_runs: [] }

describe('dashboard', () => {
  it('prompts to connect Gmail when no account exists and there are no routines', async () => {
    mockFetch({ ...signedIn, 'GET /api/dashboard': { body: { ...empty, connected_accounts: 0 } }, 'GET /api/accounts': { body: { items: [] } } })
    renderApp('/dashboard')
    expect(await screen.findByText('No Gmail account is connected yet.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Connect Gmail' })).toHaveAttribute('href', '/settings')
  })

  it('flags an account that needs attention even without routines', async () => {
    mockFetch({ ...signedIn, 'GET /api/dashboard': { body: empty }, 'GET /api/accounts': { body: { items: [{ id: 'a', provider: 'gmail', email: 'old@gmail.com', connection_status: 'needs_reauth', created_at: '2026-01-01T00:00:00Z' }] } } })
    renderApp('/dashboard')
    expect(await screen.findByText('old@gmail.com')).toBeInTheDocument()
    expect(screen.getAllByText('Needs attention').length).toBeGreaterThan(0)
  })

  it('shows exactly what the server reports', async () => {
    mockFetch(signedIn)
    renderApp('/dashboard')
    expect(await screen.findByText('Total runs')).toBeInTheDocument()
    const totals = screen.getByText('Total runs').closest('span')!
    expect(within(totals).getByText('7')).toBeInTheDocument()
    expect(within(screen.getByText('Emails handled').closest('span')!).getByText('42')).toBeInTheDocument()
    expect(screen.getByText(/Active — 2 routines maintaining 1 Gmail account/)).toBeInTheDocument()
    expect(screen.getAllByText('Archive news').length).toBeGreaterThanOrEqual(2) // last/next caption + table
    expect(screen.getByText('Last run')).toBeInTheDocument()
    expect(screen.getByText('Next run')).toBeInTheDocument()
  })

  it('invents nothing when nothing has happened yet', async () => {
    mockFetch({ ...signedIn, 'GET /api/dashboard': { body: empty } })
    renderApp('/dashboard')
    expect(await screen.findByText('No routines yet.')).toBeInTheDocument()
    expect(screen.queryByText('Total runs')).toBeNull()
    // A connected Gmail account is still reflected before any routine exists.
    expect(screen.getByText('me@gmail.com')).toBeInTheDocument()
    expect(screen.getByText('Connected')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Connected Gmail accounts' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Create your first routine/ })).toHaveAttribute('href', '/routines/new')
  })

  it('distinguishes paused from empty and shows the "nothing scheduled" caption', async () => {
    mockFetch({ ...signedIn, 'GET /api/dashboard': { body: { ...empty, total_runs: 1, emails_handled: 3, recent_runs: [runBody], last_run: runBody } } })
    renderApp('/dashboard')
    expect(await screen.findByText('Paused — no routines are enabled.')).toBeInTheDocument()
    expect(screen.getByText('Nothing scheduled')).toBeInTheDocument()
  })

  it('handles an empty run list with routines enabled', async () => {
    mockFetch({ ...signedIn, 'GET /api/dashboard': { body: { ...dashboardBody, recent_runs: [], last_run: null, next_run: null, total_runs: 0, emails_handled: 0 } } })
    renderApp('/dashboard')
    expect(await screen.findByText(/No runs yet/)).toBeInTheDocument()
    expect(screen.getAllByText('0').length).toBeGreaterThanOrEqual(2)
  })

  it('opens run details from a recent run', async () => {
    mockFetch({ ...signedIn, 'GET /api/runs/run1': { body: { run: { ...runBody, status: 'partial', failed_count: 1, error_message: '1 message failed' }, actions: [{ gmail_message_id: 'abc', status: 'failed', error: 'quota' }] } } })
    const user = userEvent.setup()
    renderApp('/dashboard')
    await user.click(within(await screen.findByRole('table')).getByText('Archive news'))
    const sheet = await screen.findByRole('dialog')
    expect(await within(sheet).findByText('1 message failed')).toBeInTheDocument()
    expect(within(sheet).getByText('abc')).toBeInTheDocument()
    expect(within(sheet).getByText(/quota/)).toBeInTheDocument()
    expect(within(sheet).getByText('Scheduled run')).toBeInTheDocument()
  })

  it('offers a retry when loading fails', async () => {
    let fail = true
    mockFetch({ ...signedIn, 'GET /api/dashboard': () => (fail ? err(500, 'internal', 'database is down') : { body: dashboardBody }) })
    const user = userEvent.setup()
    renderApp('/dashboard')
    expect(await screen.findByText('database is down')).toBeInTheDocument()
    fail = false
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Total runs')).toBeInTheDocument()
  })

  it('polls with GET requests only', async () => {
    const m = mockFetch(signedIn)
    renderApp('/dashboard')
    await screen.findByText('Total runs')
    expect(m.calls.filter((c) => c.method !== 'GET')).toEqual([])
  })
})
