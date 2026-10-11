import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { renderApp } from '@/test/renderApp'
import { mockFetch } from '@/test/mockApi'
import { signedIn } from '@/test/base'
import { err, expiredAccountBody, sessionBody, accountBody } from '@/test/fixtures'
import { notifyUnauthorized } from '@/lib/api/client'

const signedOut = { 'GET /api/auth/session': err(401, 'unauthenticated', 'authentication required') }

describe('entry routing', () => {
  it('sends signed-out visitors from / to the login page', async () => {
    mockFetch(signedOut)
    const { router } = renderApp('/')
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/login')
    expect(document.title).toBe('Sign in — MailRoutine')
  })

  it('sends signed-in users from / to the dashboard', async () => {
    mockFetch(signedIn)
    const { router } = renderApp('/')
    expect(await screen.findByText('Total runs')).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/dashboard')
  })

  it('protects every app page', async () => {
    for (const path of ['/dashboard', '/routines', '/routines/new', '/routines/r1', '/runs', '/settings']) {
      mockFetch(signedOut)
      const { router, unmount } = renderApp(path)
      await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
      unmount()
    }
  })

  it('treats an unreachable server as signed out and explains it on sign-in', async () => {
    mockFetch({ ...signedOut, 'POST /api/auth/login': { status: 502, raw: 'bad gateway', contentType: 'text/plain' } })
    const user = userEvent.setup()
    renderApp('/dashboard')
    await user.type(await screen.findByLabelText('Username'), 'a')
    await user.type(screen.getByLabelText('Password'), 'b')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText(/Can't reach the MailRoutine server/)).toBeInTheDocument()
  })

  it('renders a 404 page for unknown paths', async () => {
    mockFetch(signedIn)
    renderApp('/definitely/not/here')
    expect(await screen.findByText('Page not found')).toBeInTheDocument()
  })
})

describe('login page', () => {
  it('signs in with the typed credentials and lands on the dashboard', async () => {
    let authed = false
    const m = mockFetch({
      ...signedIn,
      'GET /api/auth/session': () => (authed ? { body: sessionBody } : err(401, 'unauthenticated', 'authentication required')),
      'POST /api/auth/login': () => ((authed = true), { body: sessionBody }),
    })
    const user = userEvent.setup()
    const { router } = renderApp('/login')
    await user.type(await screen.findByLabelText('Username'), 'admin')
    await user.type(screen.getByLabelText('Password'), 's3cret')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/dashboard'))
    expect(m.last('POST', '/api/auth/login')!.body).toEqual({ username: 'admin', password: 's3cret' })
    expect(localStorage.length).toBe(0) // no credentials or tokens in web storage
  })

  it('shows the server error and re-enables the form', async () => {
    mockFetch({ ...signedOut, 'POST /api/auth/login': err(401, 'invalid_credentials', 'invalid username or password') })
    const user = userEvent.setup()
    renderApp('/login')
    await user.type(await screen.findByLabelText('Username'), 'admin')
    await user.type(screen.getByLabelText('Password'), 'wrong')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText('invalid username or password')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled()
  })

  it('reports throttling', async () => {
    mockFetch({ ...signedOut, 'POST /api/auth/login': err(429, 'too_many_attempts', 'too many failed sign-in attempts; try again in 60 seconds') })
    const user = userEvent.setup()
    renderApp('/login')
    await user.type(await screen.findByLabelText('Username'), 'admin')
    await user.type(screen.getByLabelText('Password'), 'x')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText(/try again in 60 seconds/)).toBeInTheDocument()
  })

  it('offers no registration or social login', async () => {
    mockFetch(signedOut)
    renderApp('/login')
    await screen.findByLabelText('Username')
    expect(screen.queryByText(/sign up|register|google/i)).toBeNull()
  })
})

describe('app shell', () => {
  it('navigates with the sidebar and logs out', async () => {
    const m = mockFetch({ ...signedIn, 'POST /api/auth/logout': { status: 204 } })
    const user = userEvent.setup()
    const { router } = renderApp('/dashboard')
    await screen.findByText('Total runs')
    for (const [name, path] of [['Routines', '/routines'], ['Run history', '/runs'], ['Settings', '/settings'], ['Dashboard', '/dashboard']] as const) {
      await user.click(screen.getByRole('link', { name }))
      await waitFor(() => expect(router.state.location.pathname).toBe(path))
    }
    expect(screen.getByText('admin')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Log out' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
    expect(m.count('POST', '/api/auth/logout')).toBe(1)
  })

  it('returns to login with a notice when the session expires mid-use', async () => {
    mockFetch(signedIn)
    const { router } = renderApp('/dashboard')
    await screen.findByText('Total runs')
    notifyUnauthorized()
    await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
    expect(await screen.findByText('Your session has expired')).toBeInTheDocument()
  })

  it('warns when a Gmail connection needs attention', async () => {
    mockFetch({ ...signedIn, 'GET /api/accounts': { body: { items: [accountBody, expiredAccountBody] } } })
    renderApp('/dashboard')
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Gmail connection needs attention')
    expect(alert.querySelector('a')).toHaveAttribute('href', '/settings')
  })

  it('switches theme from the sidebar', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/dashboard')
    await screen.findByText('Total runs')
    await user.click(screen.getByRole('radio', { name: 'Dark theme' }))
    expect(document.documentElement).toHaveClass('dark')
    expect(localStorage.getItem('mailroutine.theme')).toBe('dark')
  })
})
