import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { Link, Outlet, createRootRouteWithContext, useRouter, type ErrorComponentProps } from '@tanstack/react-router'
import { Toaster } from '@/components/ui/sonner'
import { Button } from '@/components/ui/button'
import { ThemeProvider, useTheme } from '@/lib/theme'

function NotFoundComponent() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="max-w-md text-center">
        <h1 className="text-6xl font-semibold text-foreground">404</h1>
        <h2 className="mt-4 text-lg font-medium text-foreground">Page not found</h2>
        <p className="mt-2 text-sm text-muted-foreground">The page you're looking for doesn't exist or has been moved.</p>
        <Button asChild className="mt-6">
          <Link to="/">Go home</Link>
        </Button>
      </div>
    </div>
  )
}

function ErrorComponent({ error, reset }: ErrorComponentProps) {
  const router = useRouter()
  console.error(error)
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="max-w-md text-center">
        <h1 className="text-xl font-semibold tracking-tight text-foreground">This page didn't load</h1>
        <p className="mt-2 text-sm text-muted-foreground">Something went wrong. You can try again or head back home.</p>
        <div className="mt-6 flex flex-wrap justify-center gap-2">
          <Button
            onClick={() => {
              void router.invalidate()
              reset()
            }}
          >
            Try again
          </Button>
          <Button variant="outline" asChild>
            <a href="/">Go home</a>
          </Button>
        </div>
      </div>
    </div>
  )
}

function ThemedToaster() {
  const { theme } = useTheme()
  return <Toaster position="bottom-right" theme={theme} />
}

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: RootComponent,
  notFoundComponent: NotFoundComponent,
  errorComponent: ErrorComponent,
})

function RootComponent() {
  const { queryClient } = Route.useRouteContext()
  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <Outlet />
        <ThemedToaster />
      </ThemeProvider>
    </QueryClientProvider>
  )
}
