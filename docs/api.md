# REST API

The machine-readable description is in [openapi.yaml](openapi.yaml). Summary:

**Conventions.** JSON in/out, UTC RFC 3339 timestamps. Errors: `{"error": {"code": "...", "message": "...", "fields": {"name": "..."}}}`. Authentication is the `mailroutine_session` cookie (HttpOnly). Every non-GET request must send `X-CSRF-Token` (value from `POST /api/auth/login` or `GET /api/auth/session`) and, if the browser sends an `Origin`, it must equal `APP_URL`. Unknown JSON fields are rejected.

**Session semantics.** `GET` requests validate but never extend a session. Non-GET requests and `POST /api/auth/session/renew` count as user activity and slide the expiry (at most once per `SESSION_RENEWAL_THRESHOLD`). Expired sessions return `401` with code `session_expired`.

| Method & path | Purpose |
|---|---|
| `POST /api/auth/login` | `{username,password}` → session info + `csrf_token`; sets cookie. `429` + `Retry-After` when throttled |
| `POST /api/auth/logout` | Destroys the session (204) |
| `GET /api/auth/session` | Current session info (401 if none) |
| `POST /api/auth/session/renew` | `{renewed, session}` |
| `GET /api/dashboard` | `total_runs, last_run, next_run {routine_id, routine_name, at}, emails_handled, enabled_routines, connected_accounts, recent_runs`. Empty installations return zeros and nulls, never placeholders. Dry runs are not counted |
| `GET /api/accounts` | Connected accounts (never includes tokens) |
| `GET /api/accounts/google/connect` | Redirects to Google; with `Accept: application/json` returns `{"url": ...}` instead (state/PKCE cookies are set either way) |
| `GET /api/accounts/google/callback` | OAuth redirect target → `/settings?connected=1` or `?oauth_error=…` |
| `DELETE /api/accounts/{id}` | Revoke token, delete account + its routines/history |
| `GET /api/accounts/{id}/labels` | Gmail labels |
| `GET /api/accounts/{id}/labels/mailroutine` | Which traceability labels exist in the mailbox: `MailRoutine` plus `MailRoutine/Archive`, `/Trash`, `/Read`, `/Unread`, `/AddLabel`, `/RemoveLabel` (`items[{name, action, exists}]`) |
| `POST /api/accounts/{id}/labels/mailroutine` | Creates only the missing ones (case-insensitive match, parents first) and returns the same report plus `created`; safe to repeat |
| `GET /api/routines`, `POST /api/routines` | List / create. Routines include `running` and `last_run_handled` (messages handled by the latest finished real run; `null` if it never ran) |
| `GET/PUT/DELETE /api/routines/{id}` | Read / replace / delete |
| `POST /api/routines/{id}/enable` · `/disable` | Toggle (enable of a Trash routine needs `{"confirm_destructive":true}`) |
| `POST /api/routines/{id}/run` | `{dry_run, confirm_destructive}` → `202` + execution; `409` if already running |
| `POST /api/routines/{id}/preview` · `POST /api/routines/preview` | Metadata sample + count (`count_is_estimate` flags Gmail's estimate) |
| `GET /api/runs?routine_id&status&from&to&limit&offset` | Paged history `{items,total,limit,offset}`; `from` (inclusive) / `to` (exclusive) are RFC 3339 and filter on start time |
| `GET /api/runs/{id}` | `{run, actions}` (failures first) |
| `GET/PUT /api/settings` | Timezone and read-only app info. Changing the timezone re-targets every routine's schedule and recomputes its next run |
| `GET /health` · `GET /ready` | Liveness / DB readiness (unauthenticated) |

**Routine body**: `account_id, name, description, gmail_query, action_type (archive|trash|mark_read|mark_unread|add_label|remove_label), action_config{label}, schedule_type (manual|hourly|daily|weekly|monthly), schedule_config{timezone, minute, time "HH:MM", weekday 0=Sun, day_of_month}, max_messages_per_run (1–10000), enabled, traceability, confirm_destructive`.

**Traceability** (default `true` for new routines; omitted on update = unchanged): after an action succeeds on a message, `MailRoutine/<Action>` is added to it. Messages the action failed on are not labelled. If labelling itself fails the run still succeeds and the run's message says how many emails could not be labelled. Dry runs never add labels.
