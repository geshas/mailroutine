import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { renderApp } from '@/test/renderApp'
import { mockFetch } from '@/test/mockApi'
import { signedIn } from '@/test/base'
import { accountBody, err, previewBody, routineBody } from '@/test/fixtures'

type User = ReturnType<typeof userEvent.setup>

async function choose(user: User, trigger: HTMLElement, option: string | RegExp) {
  await user.click(trigger)
  await user.click(await screen.findByRole('option', { name: option }))
}

const created = { status: 201, body: { ...routineBody, id: 'r9' } }

describe('new routine', () => {
  it('starts blank: no invented query, and the limit comes from the server settings', async () => {
    mockFetch({ ...signedIn, 'GET /api/settings': { body: { ...(signedIn['GET /api/settings'].body), default_max_messages_per_run: 250 } } })
    renderApp('/routines/new')
    expect(await screen.findByRole('heading', { name: 'New Routine' })).toBeInTheDocument()
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('')
    expect(screen.getByLabelText('Routine name')).toHaveValue('')
    await waitFor(() => expect(screen.getByLabelText('Maximum emails per run')).toHaveValue(250))
    expect(document.title).toBe('New routine — MailRoutine')
  })

  it('lets the person clear and retype the limit without the default snapping back', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    const max = await screen.findByLabelText('Maximum emails per run')
    await waitFor(() => expect(max).toHaveValue(500))
    await user.clear(max)
    expect(max).toHaveValue(null)
    await user.type(max, '42')
    expect(max).toHaveValue(42)
  })

  it('builds a query from the simple builder and sends exactly that to Gmail', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines': created })
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Routine name'), 'Weekly cleanup')
    await user.type(screen.getByLabelText('Label'), 'Newsletters')
    await user.type(screen.getByLabelText('Older than'), '14')
    await user.type(screen.getByLabelText(/From/), 'amazon.com')
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('label:Newsletters from:amazon.com older_than:14d')

    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    await waitFor(() => expect(m.count('POST', '/api/routines')).toBe(1))
    expect(m.last('POST', '/api/routines')!.body).toEqual({
      account_id: 'acc1', name: 'Weekly cleanup', description: '', gmail_query: 'label:Newsletters from:amazon.com older_than:14d',
      action_type: 'archive', action_config: {}, schedule_type: 'daily', schedule_config: { time: '03:00' },
      max_messages_per_run: 500, enabled: false, traceability: true, confirm_destructive: false,
    })
    expect(await screen.findByText('Routine saved (disabled)')).toBeInTheDocument()
  })

  it('supports advanced queries, and refuses to show syntax the builder cannot represent', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.click(await screen.findByRole('radio', { name: 'Advanced query' }))
    const query = screen.getByLabelText('Gmail query')
    await user.type(query, 'has:attachment larger:5M')
    await user.click(screen.getByRole('radio', { name: 'Simple builder' }))
    expect(await screen.findByText(/simple builder can't show/)).toBeInTheDocument()
    expect(screen.getByLabelText('Gmail query')).toHaveValue('has:attachment larger:5M') // stays in advanced mode
  })

  it('round-trips a query the builder understands', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.click(await screen.findByRole('radio', { name: 'Advanced query' }))
    await user.type(screen.getByLabelText('Gmail query'), 'label:News older_than:3m is:unread -is:starred')
    await user.click(screen.getByRole('radio', { name: 'Simple builder' }))
    await waitFor(() => expect(screen.getByLabelText('Label')).toHaveValue('News'))
    expect(screen.getByLabelText('Older than')).toHaveValue(3)
  })

  it('previews the query on screen via the unsaved-preview endpoint', async () => {
    const m = mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Label'), 'News')
    await user.click(screen.getAllByRole('button', { name: 'Preview matches' })[0]!)
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('News <n@example.com>')).toBeInTheDocument()
    expect(m.last('POST', '/api/routines/preview')!.body).toEqual({ account_id: 'acc1', gmail_query: 'label:News' })
    expect(within(dialog).getByText('Read-only')).toBeInTheDocument()
    // The preview dialog can save straight from the results.
    await user.click(within(dialog).getAllByRole('button', { name: 'Close' })[0]!)
  })

  it('reports preview failures', async () => {
    mockFetch({ ...signedIn, 'POST /api/routines/preview': err(400, 'invalid_query', 'Gmail rejected this search query') })
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Label'), 'News')
    await user.click(screen.getAllByRole('button', { name: 'Preview matches' })[0]!)
    expect(await screen.findByText('Gmail rejected this search query')).toBeInTheDocument()
  })

  it('validates before talking to the server', async () => {
    const m = mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.click(await screen.findByRole('button', { name: 'Save Routine' }))
    expect(await screen.findByText('Give the routine a name.')).toBeInTheDocument()
    expect(screen.getByText(/doesn't match every email/)).toBeInTheDocument()
    expect(m.count('POST', '/api/routines')).toBe(0)

    await user.type(screen.getByLabelText('Routine name'), 'x')
    await user.type(screen.getByLabelText('Label'), 'a')
    await user.click(screen.getByRole('radio', { name: 'Add label' }))
    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    expect(await screen.findByText('Choose a label.')).toBeInTheDocument()
    await user.type(screen.getByLabelText('Label', { selector: '#action-label' }), 'Done')
    const max = screen.getByLabelText('Maximum emails per run')
    await user.clear(max)
    await user.type(max, '99999')
    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    expect(await screen.findByText('Enter a number between 1 and 10,000.')).toBeInTheDocument()
    expect(m.count('POST', '/api/routines')).toBe(0)
  })

  it('maps server-side field errors back onto the form', async () => {
    mockFetch({ ...signedIn, 'POST /api/routines': err(400, 'validation_failed', 'the routine is not valid', { name: 'must be at most 120 characters', gmail_query: 'bad', max_messages_per_run: 'too big', 'action_config.label': 'cannot modify', account_id: 'unknown' }) })
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Routine name'), 'x')
    await user.type(screen.getByLabelText('Label'), 'a')
    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    expect(await screen.findByText('must be at most 120 characters')).toBeInTheDocument()
    expect(screen.getByText('bad')).toBeInTheDocument()
    expect(screen.getByText('too big')).toBeInTheDocument()
    expect(screen.getByText('Couldn\'t save routine')).toBeInTheDocument()
  })

  it('configures every schedule type', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines': created })
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Routine name'), 'S')
    await user.type(screen.getByLabelText('Label'), 'a')

    await user.click(screen.getByRole('radio', { name: 'Hourly' }))
    const minute = screen.getByLabelText('At minute')
    await user.clear(minute)
    await user.type(minute, '15')
    expect(screen.queryByLabelText('At')).toBeNull()

    await user.click(screen.getByRole('radio', { name: 'Weekly' }))
    await choose(user, screen.getByRole('combobox', { name: 'Day of week' }), 'Friday')

    await user.click(screen.getByRole('radio', { name: 'Monthly' }))
    expect(screen.getByText(/last day/)).toBeInTheDocument()
    await choose(user, screen.getByRole('combobox', { name: 'Day of month' }), '31')

    await user.click(screen.getByRole('radio', { name: 'Manual only' }))
    expect(screen.getByText('Runs only when you press Run now.')).toBeInTheDocument()

    await user.click(screen.getByRole('radio', { name: 'Monthly' }))
    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    await waitFor(() => expect(m.count('POST', '/api/routines')).toBe(1))
    expect(m.last('POST', '/api/routines')!.body).toMatchObject({ schedule_type: 'monthly', schedule_config: { day_of_month: 31, weekday: 5, minute: 15, time: '03:00' } })
  })

  it('save & enable shows what will happen and then saves enabled', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines': created })
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Routine name'), 'Go live')
    await user.type(screen.getByLabelText('Label'), 'News')
    await user.click(screen.getByRole('button', { name: 'Save & Enable' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText(/Archive/)).toBeInTheDocument()
    expect(within(dialog).getByText(/Daily at 03:00/)).toBeInTheDocument()
    expect(within(dialog).getByText('label:News')).toBeInTheDocument()
    expect(m.count('POST', '/api/routines')).toBe(0)
    await user.click(within(dialog).getByRole('button', { name: 'Save & Enable' }))
    await waitFor(() => expect(m.count('POST', '/api/routines')).toBe(1))
    expect(m.last('POST', '/api/routines')!.body).toMatchObject({ enabled: true, confirm_destructive: false })
    expect(await screen.findByText('Routine created and enabled')).toBeInTheDocument()
  })

  it('sends the destructive confirmation only for an enabled Trash routine', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines': created })
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Routine name'), 'Bin')
    await user.type(screen.getByLabelText('Label'), 'News')
    await user.click(screen.getByRole('radio', { name: 'Move to Trash' }))
    expect(screen.getByText(/does not permanently delete/)).toBeInTheDocument()
    // Saving disabled needs no confirmation.
    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    await waitFor(() => expect(m.count('POST', '/api/routines')).toBe(1))
    expect(m.last('POST', '/api/routines')!.body).toMatchObject({ enabled: false, confirm_destructive: false })
  })

  it('without a Gmail account it guides the person to Settings and blocks saving', async () => {
    mockFetch({ ...signedIn, 'GET /api/accounts': { body: { items: [] } } })
    renderApp('/routines/new')
    expect(await screen.findByText(/Connect a Gmail account in Settings/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save Routine' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Save & Enable' })).toBeDisabled()
  })

  it('offers real Gmail labels as suggestions (user labels only)', async () => {
    mockFetch(signedIn)
    renderApp('/routines/new')
    await screen.findByLabelText('Label')
    await waitFor(() => expect(document.querySelectorAll('#label-suggestions option')).toHaveLength(1))
    expect(document.querySelector('#label-suggestions option')).toHaveAttribute('value', 'Newsletters')
  })

  it('switches accounts', async () => {
    const m = mockFetch({
      ...signedIn,
      'GET /api/accounts': { body: { items: [accountBody, { ...accountBody, id: 'acc3', email: 'two@gmail.com' }] } },
      'GET /api/accounts/acc3/labels': { body: { items: [] } },
      'POST /api/routines': created,
    })
    const user = userEvent.setup()
    renderApp('/routines/new')
    await screen.findByLabelText('Routine name')
    await choose(user, await screen.findByRole('combobox', { name: 'Gmail account' }), 'two@gmail.com')
    await waitFor(() => expect(m.count('GET', '/api/accounts/acc3/labels')).toBe(1))
  })
})

describe('query builder: age and date window', () => {
  it('treats 0 as "any age" and never falls back to a made-up value', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    const older = await screen.findByLabelText('Older than')
    await user.type(older, '0')
    const query = screen.getByLabelText('Generated Gmail query')
    expect(query).toHaveValue('')
    expect(query).not.toHaveAttribute('placeholder', expect.stringContaining('14'))
    expect(older).toHaveValue(0)
    await user.clear(older)
    await user.type(older, '30')
    expect(query).toHaveValue('older_than:30d')
  })

  it('builds an after/before window, combinable with other conditions', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Label'), 'Receipts')
    await user.type(screen.getByLabelText('After'), '2026-01-01')
    await user.type(screen.getByLabelText('Before'), '2026-03-01')
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('label:Receipts after:2026/01/01 before:2026/03/01')
    await user.click(screen.getByRole('button', { name: 'Clear' }))
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('label:Receipts')
  })

  it('rejects a window that can match nothing before saving', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines': created })
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Routine name'), 'x')
    await user.type(screen.getByLabelText('After'), '2026-05-01')
    await user.type(screen.getByLabelText('Before'), '2026-04-01')
    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    expect(await screen.findByText(/“After” must be earlier than “Before”/)).toBeInTheDocument()
    expect(m.count('POST', '/api/routines')).toBe(0)
  })

  it('round-trips an existing window query between advanced and simple modes', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.click(await screen.findByRole('radio', { name: 'Advanced query' }))
    await user.type(screen.getByLabelText('Gmail query'), 'after:2026/01/01 before:2026-02-01')
    await user.click(screen.getByRole('radio', { name: 'Simple builder' }))
    await waitFor(() => expect(screen.getByLabelText('After')).toHaveValue('2026-01-01'))
    expect(screen.getByLabelText('Before')).toHaveValue('2026-02-01')
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('after:2026/01/01 before:2026/02/01')
  })

  it('opens an old draft saved before date windows existed', async () => {
    sessionStorage.setItem('mailroutine.draft.new', JSON.stringify({
      name: 'old draft', accountId: 'acc1', advanced: false, builder: { label: 'News', olderThan: '', olderUnit: 'd', from: '', read: 'any', starred: 'any' },
      rawQuery: '', schedule: { type: 'daily', time: '03:00' }, action: { type: 'archive' }, maxPerRun: null, enabled: false,
    }))
    mockFetch(signedIn)
    renderApp('/routines/new')
    expect(await screen.findByLabelText('After')).toHaveValue('')
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('label:News')
  })
})

describe('inbox-only searches', () => {
  it('adds in:inbox from the builder switch', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Label'), 'News')
    await user.click(screen.getByRole('switch', { name: /Only emails in the Inbox/ }))
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('in:inbox label:News')
    await user.click(screen.getByRole('switch', { name: /Only emails in the Inbox/ }))
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('label:News')
  })

  it('nudges archive routines that would also match already-archived mail, and fixes it in one click', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines': created })
    const user = userEvent.setup()
    renderApp('/routines/new')
    expect(screen.queryByText(/already-archived matches/)).toBeNull() // nothing to warn about yet
    await user.type(await screen.findByLabelText('Routine name'), 'Tidy')
    await user.type(screen.getByLabelText('Label'), 'News')
    expect(await screen.findByText(/already-archived matches are counted but not touched/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Only search the Inbox' }))
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('in:inbox label:News')
    expect(screen.queryByText(/already-archived matches/)).toBeNull()
    expect(screen.getByRole('switch', { name: /Only emails in the Inbox/ })).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    await waitFor(() => expect(m.last('POST', '/api/routines')!.body).toMatchObject({ gmail_query: 'in:inbox label:News' }))
  })

  it('does not nudge for other actions, and handles advanced queries', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.click(await screen.findByRole('radio', { name: 'Advanced query' }))
    await user.type(screen.getByLabelText('Gmail query'), 'from:a.com')
    await user.click(await screen.findByRole('button', { name: 'Only search the Inbox' }))
    expect(screen.getByLabelText('Gmail query')).toHaveValue('in:inbox from:a.com')
    await user.clear(screen.getByLabelText('Gmail query'))
    await user.type(screen.getByLabelText('Gmail query'), 'from:a.com')
    await user.click(screen.getByRole('radio', { name: 'Mark as read' }))
    expect(screen.queryByText(/already-archived matches/)).toBeNull()
  })

  it('reads in:inbox back into the simple builder', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.click(await screen.findByRole('radio', { name: 'Advanced query' }))
    await user.type(screen.getByLabelText('Gmail query'), 'in:inbox older_than:2d')
    await user.click(screen.getByRole('radio', { name: 'Simple builder' }))
    await waitFor(() => expect(screen.getByRole('switch', { name: /Only emails in the Inbox/ })).toBeChecked())
    expect(screen.getByLabelText('Older than')).toHaveValue(2)
  })
})

describe('edit routine', () => {
  const existing = { ...routineBody, enabled: true, description: 'keep me', action_type: 'add_label', action_config: { label: 'Done' }, schedule_type: 'weekly', schedule_config: { timezone: 'UTC', time: '06:30', weekday: 3 }, last_run_at: '2026-01-03T03:00:00Z', next_run_at: '2099-01-08T06:30:00Z' }

  it('loads the stored routine and saves changes with PUT, preserving the description', async () => {
    const m = mockFetch({ ...signedIn, 'GET /api/routines/r1': { body: existing }, 'PUT /api/routines/r1': { body: existing } })
    const user = userEvent.setup()
    renderApp('/routines/r1')
    expect(await screen.findByRole('heading', { name: 'Archive news' })).toBeInTheDocument()
    expect(screen.getByLabelText('Routine name')).toHaveValue('Archive news')
    expect(screen.getByLabelText('At')).toHaveValue('06:30')
    expect(screen.getByLabelText('Label', { selector: '#action-label' })).toHaveValue('Done')
    expect(screen.getByText(/Last run/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save & Enable' })).toBeNull() // already enabled

    const name = screen.getByLabelText('Routine name')
    await user.clear(name)
    await user.type(name, 'Renamed')
    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    await waitFor(() => expect(m.count('PUT', '/api/routines/r1')).toBe(1))
    expect(m.last('PUT', '/api/routines/r1')!.body).toMatchObject({ name: 'Renamed', description: 'keep me', enabled: true, action_type: 'add_label', action_config: { label: 'Done' }, schedule_config: { weekday: 3, time: '06:30' } })
  })

  it('previews the edited query, not the stored one', async () => {
    const m = mockFetch({ ...signedIn, 'GET /api/routines/r1': { body: { ...existing, gmail_query: 'label:old' } } })
    const user = userEvent.setup()
    renderApp('/routines/r1')
    const label = await screen.findByLabelText('Label', { selector: '#label' })
    await user.clear(label)
    await user.type(label, 'fresh')
    await user.click(screen.getAllByRole('button', { name: 'Preview matches' })[0]!)
    await screen.findByRole('dialog')
    expect(m.last('POST', '/api/routines/preview')!.body).toEqual({ account_id: 'acc1', gmail_query: 'label:fresh' })
    expect(m.count('POST', '/api/routines/r1/preview')).toBe(0)
  })

  it('asks for confirmation when an enabled Trash routine is saved', async () => {
    const trash = { ...existing, action_type: 'trash', action_config: {} }
    const m = mockFetch({ ...signedIn, 'GET /api/routines/r1': { body: trash }, 'PUT /api/routines/r1': { body: trash } })
    const user = userEvent.setup()
    renderApp('/routines/r1')
    await user.click(await screen.findByRole('button', { name: 'Save Routine' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(m.count('PUT', '/api/routines/r1')).toBe(0)
    expect(within(dialog).getByText(/not permanent deletion/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Save routine' }))
    await waitFor(() => expect(m.last('PUT', '/api/routines/r1')?.body).toMatchObject({ enabled: true, confirm_destructive: true }))
  })

  it('deletes from the danger zone after confirmation', async () => {
    const m = mockFetch({ ...signedIn, 'DELETE /api/routines/r1': { status: 204 } })
    const user = userEvent.setup()
    const { router } = renderApp('/routines/r1')
    await user.click(await screen.findByRole('button', { name: 'Delete routine' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete routine' }))
    await waitFor(() => expect(m.count('DELETE', '/api/routines/r1')).toBe(1))
    await waitFor(() => expect(router.state.location.pathname).toBe('/routines'))
  })

  it('shows a load error for a missing routine', async () => {
    mockFetch({ ...signedIn, 'GET /api/routines/r1': err(404, 'not_found', 'routine not found') })
    renderApp('/routines/r1')
    expect(await screen.findByText('routine not found')).toBeInTheDocument()
  })

  it('keeps unsaved edits when the session expires, and restores them after signing back in', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    const first = renderApp('/routines/new')
    await user.type(await screen.findByLabelText('Routine name'), 'Half typed')
    const { notifyUnauthorized } = await import('@/lib/api/client')
    notifyUnauthorized()
    const saved = sessionStorage.getItem('mailroutine.draft.new')
    expect(saved).toContain('Half typed')
    first.unmount()

    mockFetch(signedIn)
    renderApp('/routines/new')
    expect(await screen.findByDisplayValue('Half typed')).toBeInTheDocument()
    expect(await screen.findByText('Restored your unsaved changes')).toBeInTheDocument()
    expect(sessionStorage.getItem('mailroutine.draft.new')).toBeNull()
  })

  it('ignores a corrupt draft', async () => {
    sessionStorage.setItem('mailroutine.draft.new', '{broken')
    mockFetch(signedIn)
    renderApp('/routines/new')
    expect(await screen.findByLabelText('Routine name')).toHaveValue('')
  })
})

void previewBody

describe('traceability option', () => {
  it('is on by default, names the label for the chosen action, and is sent to the API', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines': created })
    const user = userEvent.setup()
    renderApp('/routines/new')
    const box = await screen.findByRole('checkbox', { name: 'Traceability' })
    expect(box).toBeChecked()
    expect(screen.getByText('MailRoutine/Archive')).toBeInTheDocument()
    await user.click(screen.getByRole('radio', { name: 'Move to Trash' }))
    expect(screen.getByText('MailRoutine/Trash')).toBeInTheDocument()
    await user.click(screen.getByRole('radio', { name: 'Mark as unread' }))
    expect(screen.getByText('MailRoutine/Unread')).toBeInTheDocument()
    await user.type(screen.getByLabelText('Routine name'), 'T')
    await user.type(screen.getByLabelText('Label'), 'a')
    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    await waitFor(() => expect(m.last('POST', '/api/routines')!.body).toMatchObject({ traceability: true, action_type: 'mark_unread' }))
  })

  it('can be switched off', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines': created })
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.click(await screen.findByRole('checkbox', { name: 'Traceability' }))
    expect(screen.getByRole('checkbox', { name: 'Traceability' })).not.toBeChecked()
    await user.type(screen.getByLabelText('Routine name'), 'T')
    await user.type(screen.getByLabelText('Label'), 'a')
    await user.click(screen.getByRole('button', { name: 'Save Routine' }))
    await waitFor(() => expect(m.last('POST', '/api/routines')!.body).toMatchObject({ traceability: false }))
  })

  it('shows the stored value when editing, and old drafts default to on', async () => {
    mockFetch({ ...signedIn, 'GET /api/routines/r1': { body: { ...routineBody, traceability: false } } })
    const a = renderApp('/routines/r1')
    expect(await screen.findByRole('checkbox', { name: 'Traceability' })).not.toBeChecked()
    a.unmount()
    sessionStorage.setItem('mailroutine.draft.new', JSON.stringify({
      name: 'd', accountId: 'acc1', advanced: true, builder: {}, rawQuery: 'x', schedule: { type: 'daily', time: '03:00' }, action: { type: 'archive' }, maxPerRun: null, enabled: false,
    }))
    mockFetch(signedIn)
    renderApp('/routines/new')
    expect(await screen.findByRole('checkbox', { name: 'Traceability' })).toBeChecked()
  })
})

describe('older-than field can always return to "any age"', () => {
  it('lets the spinner reach 0, and 0 / blank mean no filter', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    const older = await screen.findByLabelText('Older than')
    expect(older).toHaveAttribute('min', '0')
    await user.type(older, '1')
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('older_than:1d')
    // jsdom has no stepper UI; the browser spinner stops at `min`, so min=0 is what lets 1 -> 0 work.
    await user.clear(older)
    await user.type(older, '0')
    expect(older).toHaveValue(0)
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('')
    expect(screen.getByText('0 or empty means any age.')).toBeInTheDocument()
  })

  it('has a Clear button that empties the field, and never accepts negatives or decimals', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    const older = await screen.findByLabelText('Older than')
    expect(screen.queryByRole('button', { name: 'Clear older than' })).toBeNull()
    await user.type(older, '7')
    await user.click(screen.getByRole('button', { name: 'Clear older than' }))
    expect(older).toHaveValue(null)
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('')
    await user.type(older, '-5')
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('') // "-" is clamped to 0
    await user.clear(older)
    await user.type(older, '2.9')
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('older_than:2d')
  })

  it('date window fields can each be cleared back to empty', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    const after = await screen.findByLabelText('After')
    await user.type(after, '2026-01-05')
    await user.type(screen.getByLabelText('Before'), '2026-02-01')
    await user.clear(after) // clearing one bound keeps the other
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('before:2026/02/01')
    await user.click(screen.getByRole('button', { name: 'Clear' }))
    expect(screen.getByLabelText('Generated Gmail query')).toHaveValue('')
  })
})
