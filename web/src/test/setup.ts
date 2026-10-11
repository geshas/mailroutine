import '@testing-library/jest-dom/vitest'
import { cleanup, configure } from '@testing-library/react'
import { afterEach } from 'vitest'

// jsdom renders large Radix lists slowly, especially with coverage and parallel workers.
configure({ asyncUtilTimeout: 8000 })

afterEach(() => {
  cleanup()
  sessionStorage.clear()
  localStorage.clear()
  document.documentElement.classList.remove('dark')
})

Object.defineProperty(window, 'scrollTo', { writable: true, value: () => {} })

Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }),
})

// Radix primitives rely on browser APIs jsdom does not implement.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
vi.stubGlobal('ResizeObserver', ResizeObserverStub)
Element.prototype.scrollIntoView = () => {}
Element.prototype.hasPointerCapture = () => false
Element.prototype.setPointerCapture = () => {}
Element.prototype.releasePointerCapture = () => {}
