import { QueryClient } from '@tanstack/react-query'
import { RouterProvider, createMemoryHistory, createRouter } from '@tanstack/react-router'
import { render } from '@testing-library/react'
import { routeTree } from '@/routeTree.gen'

/** Mounts the real route tree (guards, loaders, providers) at `path` with in-memory history. */
export function renderApp(path = '/') {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0 } } })
  const router = createRouter({ routeTree, context: { queryClient }, history: createMemoryHistory({ initialEntries: [path] }), defaultPreloadStaleTime: 0 })
  const utils = render(<RouterProvider router={router} />)
  return { ...utils, router, queryClient }
}
