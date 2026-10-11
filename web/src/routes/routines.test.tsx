import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { renderApp } from '@/test/renderApp'
import { mockFetch } from '@/test/mockApi'
import { signedIn } from '@/test/base'
import { err, previewBody, routineBody, runBody } from '@/test/fixtures'

const trash = { ...routineBody, action_type: 'trash', name: 'Trash old' }
const withTrash = { ...signedIn, 'GET /api/routines': { body: { items: [trash] } } }

async function openMenu(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'More actions' }))
}

describe('routines list', () => {
  it('lists real routines with schedule, action and last/next run', async () => {
    mockFetch({ ...signedIn, 'GET /api/routines': { body: { items: [{ ...routineBody, enabled: true, last_run_at: '2026-01-03T03:00:00Z', last_run_handled: 12, next_run_at: '2099-01-04T03:00:00Z' }] } } })
    renderApp('/routines')
    expect(await screen.findByText('Archive news')).toBeInTheDocument()
    expect(screen.getByText('me@gmail.com')).toBeInTheDocument()
    expect(screen.getByText('label:news older_than:7d')).toBeInTheDocument()
    expect(screen.getByText(/Daily at 03:00/)).toBeInTheDocument()
    expect(screen.getByText('12 emails')).toBeInTheDocument()
    expect(screen.getByText('Enabled')).toBeInTheDocument()
  })

  it('shows a dash instead of an invented figure for a routine that never ran', async () => {
    mockFetch(signedIn)
    renderApp('/routines')
    await screen.findByText('Archive news')
    expect(screen.getByText('Paused')).toBeInTheDocument()
    const handled = screen.getByText('Handled').nextElementSibling!
    expect(handled).toHaveTextContent('—')
  })

  it('shows the empty state and load errors', async () => {
    mockFetch({ ...signedIn, 'GET /api/routines': { body: { items: [] } } })
    const first = renderApp('/routines')
    expect(await screen.findByText('No routines yet.')).toBeInTheDocument()
    first.unmount()
    mockFetch({ ...signedIn, 'GET /api/routines': err(500, 'internal', 'db down') })
    renderApp('/routines')
    expect(await screen.findByText('db down')).toBeInTheDocument()
  })

  it('enables and disables without extra prompts for non-destructive actions', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines/r1/enable': { body: { ...routineBody, enabled: true } }, 'POST /api/routines/r1/disable': { body: routineBody } })
    const user = userEvent.setup()
    renderApp('/routines')
    await user.click(await screen.findByRole('switch', { name: 'Enable Archive news' }))
    await waitFor(() => expect(m.count('POST', '/api/routines/r1/enable')).toBe(1))
    expect(m.last('POST', '/api/routines/r1/enable')!.body).toEqual({ confirm_destructive: false })
    expect(await screen.findByText('Archive news enabled')).toBeInTheDocument()
  })

  it('requires explicit confirmation before a Trash routine is enabled', async () => {
    const m = mockFetch({ ...withTrash, 'POST /api/routines/r1/enable': { body: { ...trash, enabled: true } } })
    const user = userEvent.setup()
    renderApp('/routines')
    await user.click(await screen.findByRole('switch', { name: 'Enable Trash old' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(m.count('POST', '/api/routines/r1/enable')).toBe(0)
    expect(within(dialog).getByText(/never deletes permanently/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(m.count('POST', '/api/routines/r1/enable')).toBe(0)
    await user.click(screen.getByRole('switch', { name: 'Enable Trash old' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Enable routine' }))
    await waitFor(() => expect(m.last('POST', '/api/routines/r1/enable')?.body).toEqual({ confirm_destructive: true }))
  })

  it('reports toggle failures from the server', async () => {
    mockFetch({ ...signedIn, 'POST /api/routines/r1/enable': err(409, 'account_needs_reauth', 'this Gmail account must be reconnected in Settings') })
    const user = userEvent.setup()
    renderApp('/routines')
    await user.click(await screen.findByRole('switch', { name: 'Enable Archive news' }))
    expect(await screen.findByText('this Gmail account must be reconnected in Settings')).toBeInTheDocument()
  })

  it('previews matches read-only', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines/r1/preview': { body: { ...previewBody, count: 1200, count_is_estimate: true, has_more: true } } })
    const user = userEvent.setup()
    renderApp('/routines')
    await user.click(await screen.findByRole('button', { name: /Preview/ }))
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText(/≈ 1,200/)).toBeInTheDocument()
    expect(within(dialog).getByText(/Gmail's estimate/)).toBeInTheDocument()
    expect(within(dialog).getByText('News <n@example.com>')).toBeInTheDocument()
    expect(within(dialog).getByText('(no subject)')).toBeInTheDocument()
    expect(within(dialog).getByText(/nothing in Gmail is changed/)).toBeInTheDocument()
    expect(m.calls.filter((c) => c.method !== 'GET' && !c.path.endsWith('/preview'))).toEqual([])
  })

  it('shows an exact count and handles preview errors', async () => {
    mockFetch({ ...signedIn, 'POST /api/routines/r1/preview': { body: { ...previewBody, count: 1, messages: [previewBody.messages[0]] } } })
    const user = userEvent.setup()
    const first = renderApp('/routines')
    await user.click(await screen.findByRole('button', { name: /Preview/ }))
    expect(await screen.findByText(/email currently matches this routine/)).toBeInTheDocument()
    first.unmount()

    mockFetch({ ...signedIn, 'POST /api/routines/r1/preview': err(400, 'invalid_query', 'Gmail rejected this search query') })
    renderApp('/routines')
    await user.click(await screen.findByRole('button', { name: /Preview/ }))
    expect(await screen.findByText('Gmail rejected this search query')).toBeInTheDocument()
  })

  it('runs now: starts in the background, waits, and reports the real outcome', async () => {
    let polls = 0
    const m = mockFetch({
      ...signedIn,
      'POST /api/routines/r1/run': { status: 202, body: { ...runBody, id: 'run9', status: 'running', processed_count: 0, matched_count: 0, finished_at: null } },
      'GET /api/runs/run9': () => ({ body: { run: { ...runBody, id: 'run9', status: ++polls < 2 ? 'running' : 'success', matched_count: 9, processed_count: 8, failed_count: 0 }, actions: [] } }),
    })
    const user = userEvent.setup()
    renderApp('/routines')
    await user.click(await screen.findByRole('button', { name: /Run now/ }))
    expect(await screen.findByText('Archive news started')).toBeInTheDocument()
    expect(await screen.findByText('Archive news finished', {}, { timeout: 5000 })).toBeInTheDocument()
    expect(screen.getByText('8 of 9 emails handled.')).toBeInTheDocument()
    expect(m.last('POST', '/api/routines/r1/run')!.body).toEqual({ dry_run: false, confirm_destructive: false })
  })

  it('dry run never confirms destructive actions and says nothing changed', async () => {
    const m = mockFetch({
      ...withTrash,
      'POST /api/routines/r1/run': { status: 202, body: { ...runBody, id: 'run8', status: 'running', dry_run: true } },
      'GET /api/runs/run8': { body: { run: { ...runBody, id: 'run8', dry_run: true, matched_count: 4, processed_count: 0 }, actions: [] } },
    })
    const user = userEvent.setup()
    renderApp('/routines')
    await openMenu(user)
    await user.click(await screen.findByRole('menuitem', { name: /Dry run/ }))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(await screen.findByText(/would be handled \(dry run, nothing changed\)/)).toBeInTheDocument()
    expect(m.last('POST', '/api/routines/r1/run')!.body).toEqual({ dry_run: true, confirm_destructive: false })
  })

  it('confirms before a real Trash run', async () => {
    const m = mockFetch({
      ...withTrash,
      'POST /api/routines/r1/run': { status: 202, body: { ...runBody, id: 'run7', status: 'running' } },
      'GET /api/runs/run7': { body: { run: { ...runBody, id: 'run7', status: 'partial', matched_count: 5, processed_count: 4, failed_count: 1 }, actions: [] } },
    })
    const user = userEvent.setup()
    renderApp('/routines')
    await user.click(await screen.findByRole('button', { name: /Run now/ }))
    const dialog = await screen.findByRole('alertdialog')
    expect(m.count('POST', '/api/routines/r1/run')).toBe(0)
    await user.click(within(dialog).getByRole('button', { name: 'Move to Trash' }))
    expect(await screen.findByText('Trash old finished with errors')).toBeInTheDocument()
    expect(m.last('POST', '/api/routines/r1/run')!.body).toEqual({ dry_run: false, confirm_destructive: true })
  })

  it('reports failed and cancelled runs and refused starts', async () => {
    const start = { status: 202, body: { ...runBody, id: 'runF', status: 'running' } }
    mockFetch({ ...signedIn, 'POST /api/routines/r1/run': start, 'GET /api/runs/runF': { body: { run: { ...runBody, id: 'runF', status: 'failed' }, actions: [] } } })
    const user = userEvent.setup()
    const a = renderApp('/routines')
    await user.click(await screen.findByRole('button', { name: /Run now/ }))
    expect(await screen.findByText('Archive news failed')).toBeInTheDocument()
    a.unmount()

    mockFetch({ ...signedIn, 'POST /api/routines/r1/run': start, 'GET /api/runs/runF': { body: { run: { ...runBody, id: 'runF', status: 'cancelled' }, actions: [] } } })
    const b = renderApp('/routines')
    await user.click(await screen.findByRole('button', { name: /Run now/ }))
    expect(await screen.findByText('Archive news was stopped')).toBeInTheDocument()
    b.unmount()

    mockFetch({ ...signedIn, 'POST /api/routines/r1/run': err(409, 'routine_running', 'this routine is already running') })
    renderApp('/routines')
    await user.click(await screen.findByRole('button', { name: /Run now/ }))
    expect(await screen.findByText('this routine is already running')).toBeInTheDocument()
  })

  it('disables Run now while the server says the routine is running', async () => {
    mockFetch({ ...signedIn, 'GET /api/routines': { body: { items: [{ ...routineBody, running: true }] } } })
    renderApp('/routines')
    expect(await screen.findByRole('button', { name: /Run now/ })).toBeDisabled()
  })

  it('duplicates as a disabled copy', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/routines': { status: 201, body: { ...routineBody, id: 'r2', name: 'Archive news (copy)' } } })
    const user = userEvent.setup()
    renderApp('/routines')
    await openMenu(user)
    await user.click(await screen.findByRole('menuitem', { name: /Duplicate/ }))
    expect(await screen.findByText('Routine duplicated')).toBeInTheDocument()
    expect(m.last('POST', '/api/routines')!.body).toMatchObject({ name: 'Archive news (copy)', enabled: false, gmail_query: 'label:news older_than:7d', confirm_destructive: false })
  })

  it('reports duplicate failures', async () => {
    mockFetch({ ...signedIn, 'POST /api/routines': err(400, 'validation_failed', 'the routine is not valid') })
    const user = userEvent.setup()
    renderApp('/routines')
    await openMenu(user)
    await user.click(await screen.findByRole('menuitem', { name: /Duplicate/ }))
    expect(await screen.findByText('the routine is not valid')).toBeInTheDocument()
  })

  it('deletes only after confirmation', async () => {
    const m = mockFetch({ ...signedIn, 'DELETE /api/routines/r1': { status: 204 } })
    const user = userEvent.setup()
    renderApp('/routines')
    await openMenu(user)
    await user.click(await screen.findByRole('menuitem', { name: /Delete/ }))
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText(/run history will be removed/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(m.count('DELETE', '/api/routines/r1')).toBe(0)
    await openMenu(user)
    await user.click(await screen.findByRole('menuitem', { name: /Delete/ }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete routine' }))
    await waitFor(() => expect(m.count('DELETE', '/api/routines/r1')).toBe(1))
    expect(await screen.findByText('Deleted “Archive news”')).toBeInTheDocument()
  })

  it('reports delete failures and edit navigation', async () => {
    mockFetch({ ...signedIn, 'DELETE /api/routines/r1': err(409, 'routine_running', 'this routine is running') })
    const user = userEvent.setup()
    const { router } = renderApp('/routines')
    await openMenu(user)
    await user.click(await screen.findByRole('menuitem', { name: /Delete/ }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete routine' }))
    expect(await screen.findByText('this routine is running')).toBeInTheDocument()
    await openMenu(user)
    await user.click(await screen.findByRole('menuitem', { name: /Edit/ }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/routines/r1'))
  })
})
