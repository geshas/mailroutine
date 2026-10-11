# Architecture

```
cmd/mailroutine      CLI + server wiring (serve, hash-password, generate-secret, backup, healthcheck)
pkg/models           Domain types and validation        ┐
pkg/schedule         Next-run calculation               │ reusable, import-safe:
pkg/engine           Execution engine + interfaces      │ no SQLite / HTTP / auth /
pkg/gmail            Gmail API MailProvider             ┘ env / Docker dependencies
internal/config      Environment parsing and validation
internal/security    Argon2id, AES-256-GCM, random/HMAC helpers
internal/logging     slog setup, file rotation, redaction
internal/sqlite      SQLite storage, migrations, leases, sessions, backup
internal/auth        Admin credentials, sliding sessions, login limiter
internal/oauth       Google OAuth (code flow + PKCE), revoke
internal/accounts    Account lifecycle, encrypted tokens → Gmail client
internal/runner      Lease, execution records, engine driving, schedule update
internal/scheduler   Polling loop for due routines
internal/http        REST API, CSRF, static UI
migrations/          Versioned SQL schema (embedded)
web/                 React + TypeScript SPA (TanStack Router/Query, shadcn/ui); built into web/dist and embedded
```

## Reusing the core

`pkg/engine` depends only on the `MailProvider` and `Recorder` interfaces. A different host (for example a multi-tenant service with PostgreSQL) implements `Recorder` for its storage, builds a `MailProvider` per mailbox (`pkg/gmail.New` takes any authorized `*http.Client`), and calls `Engine.Run` / `Engine.Preview`. `pkg/schedule.Next` computes run times from a `models.ScheduleConfig`.

## Execution flow

1. Scheduler tick (default 60s): fail stale runs, query enabled routines with `next_run_at <= now` and no live lease.
2. `Runner.Start` takes a free concurrency slot, atomically claims the routine (`UPDATE … WHERE claim_token IS NULL OR claimed_at < stale`), inserts a `running` execution, then runs in a goroutine. Manual runs use the same path, so manual and scheduled runs can never overlap (HTTP 409).
3. Engine collects up to `max_messages_per_run` IDs page by page (≤500 per page; memory is bounded by the cap), then applies the action in batches (default 100). A failed batch is retried per message, so one bad message cannot block others.
4. After every batch, progress is persisted and the lease heartbeat refreshed. A disabled routine stops at the next batch boundary.
5. The execution is finalised (`success`, `partial`, `failed`, `cancelled`); the lease is released; `next_run_at` is recomputed **from now** for scheduled runs.

### Reliability notes

- Retries: transient errors (429, quota 403, 5xx, network) use bounded exponential backoff with jitter (default 5 attempts, ≤30s, honours `Retry-After`). Permanent errors (invalid query, 4xx) are not retried. Revoked authorization aborts the run and marks the account *needs reconnect*.
- Delivery is **at-least-once**: a crash after Gmail confirmed a batch but before it was recorded can leave that batch modified yet unrecorded. All actions are idempotent, so re-running is safe. Exactly-once cannot be guaranteed.
- A message is only counted as handled after Gmail confirms the batch.
- Leases: heartbeat every minute; a lease without heartbeat for 10 minutes is stale and may be taken over. At startup (single process) all leftover leases are cleared and `running` executions marked failed.
- Gmail's `batchModify` is used for modifications (≤1000 IDs/call). Searches use the Gmail API default of excluding Spam and Trash (`includeSpamTrash=false`).

## Scheduling semantics

- Hourly: at the configured minute of every hour (absolute elapsed time).
- Daily/weekly/monthly: wall-clock time in the routine's IANA timezone; results are stored as UTC.
- **DST spring-forward** (time doesn't exist): runs at the first instant after the gap. **Fall-back** (time occurs twice): runs once, at the first occurrence.
- **Monthly on a day that doesn't exist** (e.g. 31st in April, 30th/31st in February): runs on the last day of that month.
- **Downtime**: a routine that was due runs once on startup; missed slots are never replayed, and the next run is computed from the finish time.
- Interrupted runs (shutdown) keep their `next_run_at`, so they are retried after restart.

## Data & security model

- SQLite (pure-Go driver, WAL, foreign keys, 5s busy timeout, immediate write transactions). Timestamps are UTC unix milliseconds. Migrations are embedded and versioned in `schema_migrations`.
- Stored: encrypted refresh tokens, routines, execution summaries, per-message Gmail IDs + outcome, sessions (token hash only), settings. **Never stored:** message bodies or subjects, admin password hash, plaintext tokens, encryption key.
- Refresh tokens: AES-256-GCM, `v1:` + base64(nonce‖ciphertext); key from `ENCRYPTION_KEY`.
- Sessions: 256-bit random token; DB stores `HMAC(SESSION_SECRET, token)`. A credential fingerprint (`HMAC(secret, username‖hash)`) is stored per session; sessions with a different fingerprint are rejected and purged at startup.
- CSRF: per-session token (`HMAC(secret, session id)`) in `X-CSRF-Token` for all non-GET requests, plus Origin check against `APP_URL`; cookies are `SameSite=Lax`.

## Backups

`VACUUM INTO` produces a consistent snapshot without stopping the app (`mailroutine backup <file>`).

## Web UI

The UI (see `web/README.md`) is a client-side SPA served by the same Go process, so it is same-origin with the API and
needs no CORS. All backend access goes through `web/src/lib/api/client.ts`; responses are validated with zod
(`wire.ts`) and translated from the API's snake_case into UI types. `make contract` records real server responses and
the UI test-suite checks its schemas against them, so a backend change that breaks the UI fails CI.
Content-Security-Policy: `script-src 'self'` (no inline scripts), `style-src 'self' 'unsafe-inline'` (Radix and sonner inject styles at runtime).
