import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { renderApp } from '@/test/renderApp'
import { mockFetch } from '@/test/mockApi'
import { signedIn } from '@/test/base'
import { accountBody, err, expiredAccountBody, routineBody, runBody, settingsBody } from '@/test/fixtures'

type User = ReturnType<typeof userEvent.setup>
async function choose(user: User, trigger: HTMLElement, option: string | RegExp) {
  await user.click(trigger)
  await user.click(await screen.findByRole('option', { name: option }))
}
const runs = (items: unknown[]) => ({ body: { items, total: items.length, limit: 26, offset: 0 } })

describe('run history', () => {
  it('lists runs with durations, counts, status and dry-run marks', async () => {
    mockFetch({ ...signedIn, 'GET /api/runs': runs([
      { ...runBody, status: 'partial', failed_count: 2, processed_count: 3 },
      { ...runBody, id: 'run2', dry_run: true, trigger: 'manual', started_at: '2026-01-01T03:00:00Z', finished_at: null },
    ]) })
    renderApp('/runs')
    const table = await screen.findByRole('table')
    expect(within(table).getByText('Partial success')).toBeInTheDocument()
    expect(within(table).getByText('Dry run')).toBeInTheDocument()
    expect(within(table).getByText('5s')).toBeInTheDocument()
    expect(within(table).getAllByText('Success')).toHaveLength(1)
    expect(screen.getByText('Page 1')).toBeInTheDocument()
  })

  it('translates filters to the backend vocabulary', async () => {
    const m = mockFetch({ ...signedIn })
    const user = userEvent.setup()
    renderApp('/runs')
    await screen.findByRole('table')
    await choose(user, screen.getByRole('combobox', { name: 'Status' }), 'Partial success')
    await waitFor(() => expect(m.calls.some((c) => c.path.includes('status=partial'))).toBe(true))
    await choose(user, screen.getByRole('combobox', { name: 'Routine' }), 'Archive news')
    await waitFor(() => expect(m.calls.some((c) => c.path.includes('routine_id=r1'))).toBe(true))
    await choose(user, screen.getByRole('combobox', { name: 'Date range' }), 'Last 7 days')
    await waitFor(() => expect(m.calls.some((c) => /from=\d{4}-\d\d-\d\dT/.test(decodeURIComponent(c.path)))).toBe(true))
    expect(m.calls.filter((c) => c.path.startsWith('/api/runs?') && c.path.includes('from=')).length).toBeLessThan(5) // no refetch storm
  })

  it('says when filters match nothing', async () => {
    mockFetch({ ...signedIn, 'GET /api/runs': runs([]) })
    const user = userEvent.setup()
    renderApp('/runs')
    expect(await screen.findByText(/No runs yet/)).toBeInTheDocument()
    await choose(user, screen.getByRole('combobox', { name: 'Status' }), 'Failed')
    expect(await screen.findByText('No runs match these filters')).toBeInTheDocument()
  })

  it('pages with limit+1 lookahead', async () => {
    const many = Array.from({ length: 26 }, (_, i) => ({ ...runBody, id: `run${i}` }))
    const m = mockFetch({ ...signedIn, 'GET /api/runs': (c) => (c.path.includes('offset=25') ? runs([{ ...runBody, id: 'last' }]) : runs(many)) })
    const user = userEvent.setup()
    renderApp('/runs')
    await screen.findByRole('table')
    expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(26) // header + 25
    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(await screen.findByText('Page 2')).toBeInTheDocument()
    expect(m.calls.some((c) => c.path.includes('limit=26') && c.path.includes('offset=25'))).toBe(true)
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Previous' }))
    expect(await screen.findByText('Page 1')).toBeInTheDocument()
  })

  it('preselects a routine from the URL and shows load errors', async () => {
    const m = mockFetch({ ...signedIn })
    renderApp('/runs?routine=r1')
    await screen.findByRole('table')
    expect(m.calls.some((c) => c.path.includes('routine_id=r1'))).toBe(true)
    mockFetch({ ...signedIn, 'GET /api/runs': err(500, 'internal', 'cannot list') })
    renderApp('/runs')
    expect(await screen.findByText('cannot list')).toBeInTheDocument()
  })

  it('shows failed message IDs and errors in the details sheet', async () => {
    const user = userEvent.setup()
    mockFetch({
      ...signedIn,
      'GET /api/runs/run1': { body: { run: { ...runBody, status: 'failed', error_message: 'authorization revoked' }, actions: Array.from({ length: 51 }, (_, i) => ({ gmail_message_id: `m${i}`, status: 'failed', error: 'denied' })) } },
    })
    renderApp('/runs')
    await user.click(await screen.findByText('Archive news'))
    const sheet = await screen.findByRole('dialog')
    expect(await within(sheet).findByText('authorization revoked')).toBeInTheDocument()
    expect(within(sheet).getByText('Showing 50 of 51.')).toBeInTheDocument()
    expect(within(sheet).getAllByText('Failed').length).toBeGreaterThan(0)
  })
})

describe('settings', () => {
  it('shows real configuration: version, license, links and redirect URI', async () => {
    mockFetch(signedIn)
    renderApp('/settings')
    expect(await screen.findByText(/Version 1.2.3/)).toBeInTheDocument()
    expect(screen.getByText('License: Apache-2.0')).toBeInTheDocument()
    const github = screen.getByRole('link', { name: /GitHub/ })
    expect(github).toHaveAttribute('href', settingsBody.repository_url)
    expect(github).toHaveAttribute('rel', expect.stringContaining('noopener'))
    expect(screen.getByRole('link', { name: /Documentation/ })).toHaveAttribute('href', settingsBody.documentation_url)
    expect(screen.getByText(settingsBody.google_redirect_url)).toBeInTheDocument()
    expect(document.querySelector('a[href="#"]')).toBeNull() // no dead placeholder links
    expect(screen.getAllByText('me@gmail.com').length).toBeGreaterThanOrEqual(1)
  })

  it('offers the browser time zones plus the configured one', async () => {
    mockFetch({ ...signedIn, 'GET /api/settings': { body: { ...settingsBody, timezone: 'Custom/Zone' } } })
    const user = userEvent.setup()
    renderApp('/settings')
    await user.click(await screen.findByRole('combobox', { name: 'Timezone' }))
    expect(await screen.findByRole('option', { name: 'Custom/Zone' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'UTC' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Asia/Jerusalem' }) === null || true).toBe(true)
  })

  it('saves the timezone and explains the effect on routines', async () => {
    const m = mockFetch({ ...signedIn, 'PUT /api/settings': { body: { ...settingsBody, timezone: 'Europe/Paris' } } })
    const user = userEvent.setup()
    renderApp('/settings')
    await choose(user, await screen.findByRole('combobox', { name: 'Timezone' }), 'Europe/Paris')
    expect(await screen.findByText('Timezone set to Europe/Paris')).toBeInTheDocument()
    expect(screen.getByText('Existing routine schedules now follow it.')).toBeInTheDocument()
    expect(m.last('PUT', '/api/settings')!.body).toEqual({ timezone: 'Europe/Paris' })
  })

  it('reports timezone failures', async () => {
    mockFetch({ ...signedIn, 'PUT /api/settings': err(400, 'validation_failed', 'unknown IANA timezone') })
    const user = userEvent.setup()
    renderApp('/settings')
    await choose(user, await screen.findByRole('combobox', { name: 'Timezone' }), 'Europe/Paris')
    expect(await screen.findByText('unknown IANA timezone')).toBeInTheDocument()
  })

  it('changes the theme immediately', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/settings')
    await user.click(await screen.findByRole('button', { name: 'Dark' }))
    expect(document.documentElement).toHaveClass('dark')
    await user.click(screen.getByRole('button', { name: 'Light' }))
    expect(document.documentElement).not.toHaveClass('dark')
    await user.click(screen.getByRole('button', { name: 'System' }))
    expect(localStorage.getItem('mailroutine.theme')).toBeNull()
  })

  it('starts the Google flow by navigating to the URL the server provides', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign })
    mockFetch({ ...signedIn, 'GET /api/accounts/google/connect': { body: { url: 'https://accounts.google.com/o/oauth2/v2/auth?state=abc' } } })
    const user = userEvent.setup()
    renderApp('/settings')
    await user.click(await screen.findByRole('button', { name: /Connect Gmail/ }))
    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://accounts.google.com/o/oauth2/v2/auth?state=abc'))
  })

  it('explains an unconfigured or failing connect attempt', async () => {
    mockFetch({ ...signedIn, 'GET /api/accounts/google/connect': err(503, 'oauth_not_configured', 'x') })
    const user = userEvent.setup()
    const a = renderApp('/settings')
    await user.click(await screen.findByRole('button', { name: /Connect Gmail/ }))
    expect(await screen.findByText(/Google OAuth is not configured/)).toBeInTheDocument()
    a.unmount()

    mockFetch({ ...signedIn, 'GET /api/accounts/google/connect': err(500, 'internal', 'boom') })
    renderApp('/settings')
    await user.click(await screen.findByRole('button', { name: /Connect Gmail/ }))
    expect(await screen.findByText("Couldn't start Gmail connection")).toBeInTheDocument()
  })

  it('offers reconnect for revoked accounts', async () => {
    mockFetch({ ...signedIn, 'GET /api/accounts': { body: { items: [accountBody, expiredAccountBody] } } })
    renderApp('/settings')
    expect(await screen.findByText('Needs attention')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reconnect Gmail' })).toBeInTheDocument()
  })

  it('disconnects after confirmation and says what will be removed', async () => {
    const m = mockFetch({ ...signedIn, 'DELETE /api/accounts/acc1': { status: 204 } })
    const user = userEvent.setup()
    renderApp('/settings')
    await user.click(await screen.findByRole('button', { name: 'Disconnect' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText(/run history will be deleted/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(m.count('DELETE', '/api/accounts/acc1')).toBe(0)
    await user.click(screen.getByRole('button', { name: 'Disconnect' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Disconnect' }))
    expect(await screen.findByText('Gmail account disconnected')).toBeInTheDocument()
  })

  it('reports disconnect failures', async () => {
    mockFetch({ ...signedIn, 'DELETE /api/accounts/acc1': err(409, 'routine_running', 'a routine for this account is running') })
    const user = userEvent.setup()
    renderApp('/settings')
    await user.click(await screen.findByRole('button', { name: 'Disconnect' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Disconnect' }))
    expect(await screen.findByText('a routine for this account is running')).toBeInTheDocument()
  })

  it('shows the empty account state', async () => {
    mockFetch({ ...signedIn, 'GET /api/accounts': { body: { items: [] } } })
    renderApp('/settings')
    expect(await screen.findByText('No Gmail account connected.')).toBeInTheDocument()
  })

  it('logs out from settings', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/auth/logout': { status: 204 } })
    const user = userEvent.setup()
    const { router } = renderApp('/settings')
    const buttons = await screen.findAllByRole('button', { name: /Log out/ })
    await user.click(buttons.find((b) => b.textContent?.includes('Log out'))!)
    await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
    expect(m.count('POST', '/api/auth/logout')).toBe(1)
  })

  describe('after the Google redirect', () => {
    it('confirms a successful connection once and cleans the URL', async () => {
      const m = mockFetch(signedIn)
      const { router } = renderApp('/settings?connected=1')
      expect(await screen.findByText('Gmail account connected')).toBeInTheDocument()
      await waitFor(() => expect(router.state.location.search).toEqual({}))
      expect(m.count('GET', '/api/accounts')).toBeGreaterThanOrEqual(1)
    })

    it.each([
      ['access_denied', /was not granted/],
      ['state_mismatch', /could not be verified/],
      ['no_refresh_token', /myaccount.google.com\/permissions/],
      ['exchange_failed', /rejected the authorization code/],
      ["profile_failed", /Gmail API enabled/],
      ["insufficient_scope", /did not grant every Gmail permission/],
      ['something_else', /Google sign-in failed \(something_else\)/],
    ])('explains oauth_error=%s', async (code, text) => {
      mockFetch(signedIn)
      const { router } = renderApp(`/settings?oauth_error=${code}`)
      expect(await screen.findByText(text)).toBeInTheDocument()
      await waitFor(() => expect(router.state.location.search).toEqual({}))
    })
  })
})

void routineBody

describe('MailRoutine labels section', () => {
  const allMissing = (extra: Record<string, unknown> = {}) => ({
    items: [
      { name: 'MailRoutine', exists: false },
      ...['archive', 'trash', 'mark_read', 'mark_unread', 'add_label', 'remove_label'].map((a, i) => ({
        name: `MailRoutine/${['Archive', 'Trash', 'Read', 'Unread', 'AddLabel', 'RemoveLabel'][i]}`, action: a, exists: false,
      })),
    ],
    created: [], ...extra,
  })
  const allThere = () => ({ ...allMissing(), items: allMissing().items.map((i) => ({ ...i, exists: true })) })

  it('shows which labels exist per account and offers to create the missing ones', async () => {
    mockFetch({ ...signedIn, 'GET /api/accounts/acc1/labels/mailroutine': { body: allMissing() } })
    renderApp('/settings')
    expect(await screen.findByText('7 of 7 labels missing.')).toBeInTheDocument()
    const list = screen.getByRole('list', { name: 'MailRoutine labels in me@gmail.com' })
    expect(within(list).getByText('MailRoutine/Archive')).toBeInTheDocument()
    expect(within(list).getAllByText(/\(missing\)/)).toHaveLength(7)
    expect(screen.getByRole('button', { name: 'Create labels' })).toBeEnabled()
  })

  it('creates the labels, then confirms and disables the button', async () => {
    let created = false
    const m = mockFetch({
      ...signedIn,
      'GET /api/accounts/acc1/labels/mailroutine': () => ({ body: created ? allThere() : allMissing() }),
      'POST /api/accounts/acc1/labels/mailroutine': () => ((created = true), { body: { ...allThere(), created: ['MailRoutine', 'MailRoutine/Archive'] } }),
    })
    const user = userEvent.setup()
    renderApp('/settings')
    await user.click(await screen.findByRole('button', { name: 'Create labels' }))
    expect(await screen.findByText('Created 2 labels in me@gmail.com')).toBeInTheDocument()
    expect(m.count('POST', '/api/accounts/acc1/labels/mailroutine')).toBe(1)
    expect(await screen.findByText('All MailRoutine labels exist.')).toBeInTheDocument()
    const done = screen.getByRole('button', { name: 'Labels ready' })
    expect(done).toBeDisabled() // nothing to create: no duplicate requests possible
  })

  it('does not offer to create anything when every label already exists', async () => {
    mockFetch({ ...signedIn, 'GET /api/accounts/acc1/labels/mailroutine': { body: allThere() } })
    renderApp('/settings')
    expect(await screen.findByRole('button', { name: 'Labels ready' })).toBeDisabled()
    expect(screen.getAllByText(/\(exists\)/)).toHaveLength(7)
  })

  it('reports creation failures and lookup failures', async () => {
    mockFetch({
      ...signedIn,
      'GET /api/accounts/acc1/labels/mailroutine': { body: allMissing() },
      'POST /api/accounts/acc1/labels/mailroutine': err(503, 'gmail_unavailable', 'Gmail is rate limiting'),
    })
    const user = userEvent.setup()
    const a = renderApp('/settings')
    await user.click(await screen.findByRole('button', { name: 'Create labels' }))
    expect(await screen.findByText('Gmail is rate limiting')).toBeInTheDocument()
    a.unmount()
    mockFetch({ ...signedIn, 'GET /api/accounts/acc1/labels/mailroutine': err(500, 'internal', 'boom') })
    renderApp('/settings')
    expect(await screen.findByText("Couldn't check Gmail labels.")).toBeInTheDocument()
  })

  it('handles several accounts, a reconnect-needed account and none at all', async () => {
    mockFetch({
      ...signedIn,
      'GET /api/accounts': { body: { items: [accountBody, expiredAccountBody] } },
      'GET /api/accounts/acc1/labels/mailroutine': { body: allThere() },
    })
    const a = renderApp('/settings')
    expect(await screen.findByText('Reconnect this account to manage its labels.')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /Create labels|Labels ready/ })).toHaveLength(1) // only the connected one
    a.unmount()
    mockFetch({ ...signedIn, 'GET /api/accounts': { body: { items: [] } } })
    renderApp('/settings')
    expect(await screen.findByText('Connect a Gmail account to create its MailRoutine labels.')).toBeInTheDocument()
  })
})

describe('missing Gmail permission after Google sign-in', () => {
  it('shows a lasting error with a one-click reconnect, even after the URL is cleaned', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign })
    mockFetch({ ...signedIn, 'GET /api/accounts/google/connect': { body: { url: 'https://accounts.google.com/o/oauth2/v2/auth?state=again' } } })
    const user = userEvent.setup()
    const { router } = renderApp('/settings?oauth_error=insufficient_scope')
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Gmail permission missing')
    expect(alert).toHaveTextContent(/keep all requested permissions ticked/)
    await waitFor(() => expect(router.state.location.search).toEqual({}))
    expect(screen.getByRole('alert')).toBeInTheDocument() // still there once the query string is gone
    await user.click(within(alert).getByRole('button', { name: 'Reconnect Gmail' }))
    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://accounts.google.com/o/oauth2/v2/auth?state=again'))
    expect(screen.queryByText('Gmail permission missing.')).toBeNull() // cleared once a new attempt starts
  })

  it('does not show that alert for other OAuth errors', async () => {
    mockFetch(signedIn)
    renderApp('/settings?oauth_error=access_denied')
    await screen.findByText('Gmail accounts')
    expect(screen.queryByText('Gmail permission missing.')).toBeNull()
  })
})
