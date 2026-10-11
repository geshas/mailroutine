// Automated accessibility checks (axe-core) on every screen, plus design-token contrast.
// jsdom cannot compute rendered colours, so contrast is verified from the CSS tokens below.
import { readFileSync } from 'node:fs'
import path from 'node:path'
import axe from 'axe-core'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { renderApp } from './renderApp'
import { mockFetch } from './mockApi'
import { signedIn } from './base'
import { err, expiredAccountBody, accountBody, runBody } from './fixtures'

async function violations(container: HTMLElement) {
  const result = await axe.run(container, {
    rules: { 'color-contrast': { enabled: false }, region: { enabled: false } }, // contrast: token test; region: fragment under test
  })
  return result.violations.map((v) => `${v.id}: ${v.help} -> ${v.nodes.map((n) => n.target.join(' ')).join(' | ')}`)
}

describe('axe: no accessibility violations', () => {
  it('login', async () => {
    mockFetch({ 'GET /api/auth/session': err(401, 'unauthenticated', 'x') })
    const { container } = renderApp('/login')
    await screen.findByLabelText('Username')
    expect(await violations(container)).toEqual([])
  })

  it.each([
    ['dashboard', '/dashboard', 'Total runs'],
    ['routines', '/routines', 'Archive news'],
    ['new routine', '/routines/new', 'Routine name'],
    ['edit routine', '/routines/r1', 'Delete routine'],
    ['run history', '/runs', 'Archive news'],
    ['settings', '/settings', 'Connect Gmail'],
  ])('%s', async (_name, route, ready) => {
    mockFetch({ ...signedIn, 'GET /api/accounts': { body: { items: [accountBody, expiredAccountBody] } } })
    const { container } = renderApp(route)
    await screen.findAllByText(ready)
    expect(await violations(container)).toEqual([])
  })

  it('empty dashboard and error state', async () => {
    mockFetch({ ...signedIn, 'GET /api/dashboard': err(500, 'internal', 'down') })
    const { container } = renderApp('/dashboard')
    await screen.findByText('down')
    expect(await violations(container)).toEqual([])
  })
})

describe('keyboard and screen-reader behaviour', () => {
  it('lets keyboard users open a run from the table (rows are not mouse-only)', async () => {
    mockFetch({ ...signedIn, 'GET /api/runs': { body: { items: [runBody], total: 1, limit: 26, offset: 0 } } })
    const user = userEvent.setup()
    renderApp('/runs')
    const open = await screen.findByRole('button', { name: /Archive news, view run details/ })
    open.focus()
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('offers a skip link that moves focus to the page content', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/dashboard')
    const skip = await screen.findByRole('link', { name: 'Skip to content' })
    await user.click(skip)
    expect(document.activeElement).toBe(document.getElementById('main'))
  })

  it('moves focus to the content after navigating', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/dashboard')
    await user.click(await screen.findByRole('link', { name: 'Routines' }))
    await screen.findByText('Archive news')
    expect(document.activeElement).toBe(document.getElementById('main'))
  })

  it('focuses the first invalid field and announces its error', async () => {
    mockFetch(signedIn)
    const user = userEvent.setup()
    renderApp('/routines/new')
    await user.click(await screen.findByRole('button', { name: 'Save Routine' }))
    const name = screen.getByLabelText('Routine name')
    expect(name).toHaveFocus()
    expect(name).toHaveAttribute('aria-invalid', 'true')
    const error = await screen.findByText('Give the routine a name.')
    expect(error).toHaveAttribute('role', 'alert')
    expect(name.getAttribute('aria-describedby')).toBe(error.id)
  })

  it('announces login errors', async () => {
    mockFetch({ 'GET /api/auth/session': err(401, 'unauthenticated', 'x'), 'POST /api/auth/login': err(401, 'invalid_credentials', 'invalid username or password') })
    const user = userEvent.setup()
    renderApp('/login')
    await user.type(await screen.findByLabelText('Username'), 'a')
    await user.type(screen.getByLabelText('Password'), 'b')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('invalid username or password')
    expect(screen.getByLabelText('Password')).toHaveAttribute('aria-describedby', alert.id)
  })

  it('keeps icon-only action buttons named at every width', async () => {
    mockFetch(signedIn)
    renderApp('/routines')
    const preview = await screen.findByRole('button', { name: /Preview/ })
    expect(preview.querySelector('span.sr-only')).not.toBeNull() // visible text is hidden on small screens, name stays
    expect(screen.getByRole('button', { name: 'More actions' })).toBeInTheDocument()
  })

  it('exposes the theme choice as a labelled, stateful group', async () => {
    mockFetch(signedIn)
    renderApp('/settings')
    const group = await screen.findByRole('group', { name: 'Theme' })
    const buttons = Array.from(group.querySelectorAll('button'))
    expect(buttons.map((b) => b.getAttribute('aria-pressed'))).toEqual(['true', 'false', 'false']) // system by default
  })

  it('marks loading regions as busy', async () => {
    mockFetch({ ...signedIn, 'GET /api/dashboard': () => new Promise(() => {}) as never })
    renderApp('/dashboard')
    expect(await screen.findByRole('status', { name: 'Loading dashboard' })).toHaveAttribute('aria-busy', 'true')
  })
})

describe('colour contrast of the design tokens (WCAG AA)', () => {
  const css = readFileSync(path.resolve(import.meta.dirname, '../styles.css'), 'utf8')
  const tokens = (selector: string) => {
    const body = css.match(new RegExp(`${selector}\\s*\\{([\\s\\S]*?)\\n\\}`))![1]!
    return Object.fromEntries([...body.matchAll(/--([a-z0-9-]+):\s*(oklch\([^;]*\));/g)].map((m) => [m[1]!, m[2]!]))
  }
  const toLinear = (s: string, under?: number[]) => {
    const m = s.match(/oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+)%)?\)/)!
    const [L, C, h] = [Number(m[1]), Number(m[2]), Number(m[3])]
    const alpha = m[4] ? Number(m[4]) / 100 : 1
    const a = C * Math.cos((h * Math.PI) / 180)
    const b = C * Math.sin((h * Math.PI) / 180)
    const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3
    const mm = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3
    const ss = (L - 0.0894841775 * a - 1.291485548 * b) ** 3
    const rgb = [4.0767416621 * l - 3.3077115913 * mm + 0.2309699292 * ss, -1.2684380046 * l + 2.6097574011 * mm - 0.3413193965 * ss, -0.0041960863 * l - 0.7034186147 * mm + 1.707614701 * ss].map((x) => Math.max(0, Math.min(1, x)))
    return alpha < 1 && under ? rgb.map((c, i) => alpha * c + (1 - alpha) * under[i]!) : rgb
  }
  const lum = (c: number[]) => 0.2126 * c[0]! + 0.7152 * c[1]! + 0.0722 * c[2]!
  const ratio = (fg: number[], bg: number[]) => (Math.max(lum(fg), lum(bg)) + 0.05) / (Math.min(lum(fg), lum(bg)) + 0.05)

  const textPairs: [string, string][] = [
    ['foreground', 'background'], ['foreground', 'card'], ['muted-foreground', 'background'], ['muted-foreground', 'card'], ['muted-foreground', 'muted'],
    ['primary-foreground', 'primary'], ['destructive-foreground', 'destructive'], ['success', 'success-soft'], ['warning', 'warning-soft'],
    ['destructive', 'danger-soft'], ['info', 'info-soft'], ['sidebar-foreground', 'sidebar'], ['muted-foreground', 'sidebar'],
    ['accent-foreground', 'accent'], ['destructive', 'background'], ['primary', 'background'],
  ]
  const controlPairs: [string, string][] = [['input', 'background'], ['input', 'card'], ['ring', 'background']] // 3:1 for UI components

  for (const [mode, selector] of [['light', ':root'], ['dark', '\\.dark']] as const) {
    const t = tokens(selector)
    const under = toLinear(t['background']!)
    const check = (pairs: [string, string][], min: number) =>
      pairs.map(([f, b]) => {
        const bg = toLinear(t[b]!, under)
        return { pair: `${f} on ${b}`, ratio: ratio(toLinear(t[f]!, bg), bg), min }
      })
    it(`${mode}: text pairs reach 4.5:1`, () => {
      for (const r of check(textPairs, 4.5)) expect(r.ratio, `${mode} ${r.pair}`).toBeGreaterThanOrEqual(r.min)
    })
    it(`${mode}: form-control borders and focus ring reach 3:1`, () => {
      for (const r of check(controlPairs, 3)) expect(r.ratio, `${mode} ${r.pair}`).toBeGreaterThanOrEqual(r.min)
    })
  }
})
