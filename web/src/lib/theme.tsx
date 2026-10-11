import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import type { ThemePreference } from './api/types'

const KEY = 'mailroutine.theme'

function read(): ThemePreference {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'light' || v === 'dark') return v
  } catch {
    /* storage unavailable */
  }
  return 'system'
}

const Ctx = createContext<{ theme: ThemePreference; setTheme: (t: ThemePreference) => void }>({
  theme: 'system',
  setTheme: () => {},
})

function applyTheme(t: ThemePreference) {
  const dark = t === 'dark' || (t === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.classList.toggle('dark', dark)
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<ThemePreference>(read)

  useEffect(() => {
    applyTheme(theme)
    if (theme !== 'system') return
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const onChange = () => applyTheme('system')
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [theme])

  const setTheme = (t: ThemePreference) => {
    try {
      if (t === 'system') localStorage.removeItem(KEY)
      else localStorage.setItem(KEY, t)
    } catch {
      /* the choice still applies for this session */
    }
    setThemeState(t)
  }
  return <Ctx.Provider value={{ theme, setTheme }}>{children}</Ctx.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export const useTheme = () => useContext(Ctx)
