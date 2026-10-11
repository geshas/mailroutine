import { QueryClient } from '@tanstack/react-query'
import { createRouter } from '@tanstack/react-router'
import { ApiError } from './lib/api/types'
import { routeTree } from './routeTree.gen'

export const getRouter = () => {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 10_000,
        refetchIntervalInBackground: false,
        // Never retry auth or validation failures; retry transient ones once.
        retry: (count, error) => !(error instanceof ApiError && error.status >= 400 && error.status < 500) && count < 1,
      },
    },
  })
  return createRouter({ routeTree, context: { queryClient }, scrollRestoration: true, defaultPreloadStaleTime: 0 })
}
