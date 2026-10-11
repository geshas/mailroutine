import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from '@tanstack/react-router'
import { getRouter } from './router'
import './styles.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <RouterProvider router={getRouter()} />
  </StrictMode>,
)

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof getRouter>
  }
}
