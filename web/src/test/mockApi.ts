import { vi } from 'vitest'

export interface Call {
  method: string
  path: string
  body: unknown
  headers: Record<string, string>
}

type Reply = { status?: number; body?: unknown; raw?: string; contentType?: string }
type Handler = Reply | ((call: Call) => Reply)

/**
 * Installs a fetch stub keyed by "METHOD /api/path" (query string ignored).
 * Unmatched requests fail the test loudly so no screen can silently depend on invented data.
 */
export function mockFetch(routes: Record<string, Handler>) {
  const calls: Call[] = []
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    const method = init?.method ?? 'GET'
    const call: Call = {
      method,
      path,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
      headers: (init?.headers as Record<string, string>) ?? {},
    }
    calls.push(call)
    const key = `${method} ${path.split('?')[0]}`
    const route = routes[key]
    if (!route) throw new Error(`unmocked request: ${key}`)
    const reply = typeof route === 'function' ? route(call) : route
    const status = reply.status ?? 200
    const noBody = status === 204
    return new Response(noBody ? null : (reply.raw ?? JSON.stringify(reply.body ?? {})), {
      status,
      headers: { 'Content-Type': reply.contentType ?? 'application/json' },
    })
  })
  vi.stubGlobal('fetch', fn)
  return {
    calls,
    fn,
    count: (method: string, path: string) => calls.filter((c) => c.method === method && c.path.split('?')[0] === path).length,
    last: (method: string, path: string) => calls.filter((c) => c.method === method && c.path.split('?')[0] === path).at(-1),
  }
}
