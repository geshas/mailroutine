import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ThemeProvider, useTheme } from './theme'

function Probe() {
  const { theme, setTheme } = useTheme()
  return (
    <div>
      <span data-testid="t">{theme}</span>
      {(['light', 'dark', 'system'] as const).map((t) => <button key={t} onClick={() => setTheme(t)}>{t}</button>)}
    </div>
  )
}

describe('theme', () => {
  it('defaults to system and follows the OS preference', async () => {
    const listeners: (() => void)[] = []
    let dark = false
    vi.stubGlobal('matchMedia', () => ({ get matches() { return dark }, addEventListener: (_: string, f: () => void) => listeners.push(f), removeEventListener: () => {} }))
    render(<ThemeProvider><Probe /></ThemeProvider>)
    expect(screen.getByTestId('t')).toHaveTextContent('system')
    expect(document.documentElement).not.toHaveClass('dark')
    dark = true
    act(() => listeners.forEach((f) => f()))
    expect(document.documentElement).toHaveClass('dark')
  })

  it('persists explicit choices and clears the stored value for system', async () => {
    const user = userEvent.setup()
    render(<ThemeProvider><Probe /></ThemeProvider>)
    await user.click(screen.getByText('dark'))
    expect(document.documentElement).toHaveClass('dark')
    expect(localStorage.getItem('mailroutine.theme')).toBe('dark')
    await user.click(screen.getByText('light'))
    expect(document.documentElement).not.toHaveClass('dark')
    expect(localStorage.getItem('mailroutine.theme')).toBe('light')
    await user.click(screen.getByText('system'))
    expect(localStorage.getItem('mailroutine.theme')).toBeNull()
  })

  it('restores the saved theme and survives unavailable storage', async () => {
    localStorage.setItem('mailroutine.theme', 'dark')
    render(<ThemeProvider><Probe /></ThemeProvider>)
    expect(screen.getByTestId('t')).toHaveTextContent('dark')
    const get = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    const set = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    const user = userEvent.setup()
    render(<ThemeProvider><Probe /></ThemeProvider>)
    await user.click(screen.getAllByText('light')[1]!)
    get.mockRestore(); set.mockRestore()
  })
})
