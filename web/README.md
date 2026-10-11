# MailRoutine web UI

React 19 + TypeScript single-page app, built with Vite and embedded into the Go binary (`web/dist` → `go:embed`).
The design comes from the [mailroutine-oss-dash](https://github.com/geshas/mailroutine-oss-dash) reference
(shadcn/ui + Tailwind v4 + TanStack Router/Query), converted from a server-rendered template into a client-only SPA
and wired to the real backend. There is **no mock API**: every number on screen comes from the server.

```bash
npm ci
npm run dev        # http://localhost:5173, proxies /api to the Go server on :8080
npm test           # unit + component tests
npm run build      # type-checks, then writes web/dist
```

## Where things live

| Path | Purpose |
|---|---|
| `src/lib/api/client.ts` | The only code that calls `fetch`. CSRF header, 401 handling, error envelope, polling for runs |
| `src/lib/api/wire.ts` | zod schemas for the Go JSON (snake_case) and the translation into UI types. Responses that do not match are rejected |
| `src/lib/api/types.ts` | UI domain types |
| `src/lib/session-activity.ts` | Sliding-session keep-alive: renews only after real input, never on idle tabs or polling |
| `src/routes/` | File-based TanStack routes (`_shell` = signed-in layout with the session guard) |
| `src/components/mr/` | App components; `components/ui/` are the shadcn primitives actually used |
| `src/test/contract.test.ts` | Checks the schemas against JSON recorded from the real server (`make contract`) |

Security notes: no tokens in web storage (CSRF token lives in memory), no inline scripts (the theme bootstrap is
`public/theme-init.js`), fonts are bundled (`@fontsource-variable`) so nothing is loaded from third-party hosts,
Trash always requires an explicit confirmation that is also enforced by the API (`confirm_destructive`).
