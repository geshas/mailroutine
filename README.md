# MailRoutine

**Scheduled automation for Gmail.** MailRoutine is a self-hosted service that runs recurring maintenance on your Gmail accounts in the background, for example *"every day at 03:00, archive everything matching `label:Newsletters older_than:7d`"*.

It is **not an email client**. You keep using Gmail on the web and on your phone; MailRoutine just tidies up on a schedule.

## Features

- Gmail OAuth, with **multiple Gmail accounts** per installation
- Native Gmail search queries (no custom query language) with **preview** and **dry run**
- Actions: archive, move to Trash, mark read/unread, add/remove label (no permanent delete)
- Schedules: manual, hourly, daily, weekly, monthly, timezone and DST aware
- Safety: per-run message cap, explicit confirmation for Trash, no overlapping runs, instant disable
- Traceability labels (`MailRoutine/Archive`, `/Trash`, `/Read`, …) so you can always find what MailRoutine touched; one click in Settings creates the missing ones
- Run history, dashboard statistics, light/dark theme
- Single administrator login, persistent sliding sessions, CSRF protection
- One Go binary + embedded web UI, SQLite, one Docker container, nothing else to run

<!-- Screenshots: add docs/img/dashboard.png and docs/img/routine.png and link them here. -->

## Requirements

- Docker with Compose (or Go 1.27+ and Node 24+ to build from source)
- A Google account able to create a Google Cloud project (free) – see [docs/oauth-setup.md](docs/oauth-setup.md)
- A URL Google accepts as an OAuth redirect: `http://localhost`, or an HTTPS domain

## Quick start

```bash
git clone https://github.com/geshas/mailroutine.git && cd mailroutine
cp .env.example .env

# 1. Administrator password hash (prompts twice, input hidden)
docker compose run --rm --entrypoint /app/mailroutine mailroutine hash-password
#    paste the printed ADMIN_PASSWORD_HASH='...' line into .env

# 2. Secrets (run twice: once for SESSION_SECRET, once for ENCRYPTION_KEY)
docker run --rm --entrypoint /app/mailroutine ghcr.io/geshas/mailroutine:latest generate-secret
#    or: openssl rand -base64 32

# 3. Google OAuth credentials -> GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET in .env
#    (step-by-step guide: docs/oauth-setup.md)

docker compose up -d
```

Open <http://localhost:8080>, sign in as `admin`, and connect Gmail under **Settings**.

> `hash-password` works before any other configuration exists. If you cloned the repo and have not published an image yet, build first: `docker compose build` (uncomment `build:` in `docker-compose.yml`) or use the *build from source* path below.

### Portainer / no shell

If you manage containers through Portainer (or a NAS UI) you cannot run `mailroutine hash-password` first. You don't have to:

1. Create a **Stack** from [`docker-compose.portainer.yml`](docker-compose.portainer.yml) and deploy it **with the variables empty**, except `APP_URL` (the address you will open, e.g. `http://192.168.1.20:8080`).
2. The container starts in **setup mode** and serves a page at that address. Choose the administrator password there; the page generates `ADMIN_PASSWORD_HASH_B64`, `SESSION_SECRET` and `ENCRYPTION_KEY` and shows them in a block to copy.
3. In Portainer open the stack → **Environment variables**, paste those values, add `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` ([guide](docs/oauth-setup.md)), then **Update the stack**. The setup page is replaced by the real app.
4. **Back up `ENCRYPTION_KEY`** somewhere safe: without it the stored Gmail connections cannot be decrypted.

The setup page is stateless: it applies nothing and stores nothing, so it cannot be used to take over an installation, but anyone who can open it can see the freshly generated values on it. Use it on a trusted network or over HTTPS, and do not reuse a password from elsewhere. Set `MAILROUTINE_SETUP=off` if you prefer the container to exit on bad configuration instead.

`ADMIN_PASSWORD_HASH_B64` is the same Argon2id hash as `ADMIN_PASSWORD_HASH`, base64-encoded so it contains no `$` characters that compose, Portainer or `.env` parsers could mangle. Either variable works; the plain one wins if both are set.

### Build from source with Docker

```bash
docker build -t mailroutine .
# then set `image: mailroutine` (or enable `build:`) in docker-compose.yml
docker compose up -d
```

### Run without Docker

```bash
make install      # Go modules + web dependencies (Go 1.27+, Node 24+)
make setup        # .env with fresh secrets; asks for the admin password
# add GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET to .env
make run          # builds the UI + binary, serves everything on http://localhost:8080
```

## Google OAuth setup

MailRoutine uses **your own** Google Cloud OAuth client, so no third party ever sees your mail. Follow [docs/oauth-setup.md](docs/oauth-setup.md). Important points:

- Required scope: `https://www.googleapis.com/auth/gmail.modify`
- Redirect URI to register: `<APP_URL>/api/accounts/google/callback` (shown in Settings)
- Apps left in **Testing** status get refresh tokens that **expire after 7 days**. For long-running use, publish the app (see the guide).

## Environment configuration

Copy `.env.example`; every variable is documented there. Required: `ADMIN_PASSWORD_HASH`, `SESSION_SECRET`, `ENCRYPTION_KEY`, `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`. MailRoutine refuses to start with a clear message listing every missing or invalid value.

| Variable | Default | Purpose |
|---|---|---|
| `APP_URL` | `http://localhost:8080` | Public URL; decides cookie `Secure` flag, OAuth redirect |
| `HTTP_ADDR` / `HTTP_PORT` | `0.0.0.0` / `8080` | Listen address |
| `TZ` | `UTC` | Default timezone |
| `TRUSTED_PROXIES` | empty | Proxies whose `X-Forwarded-For` is trusted |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD_HASH` | `admin` / – | Administrator (Argon2id hash) |
| `SESSION_IDLE_TIMEOUT` | `30m` | Logout after this much inactivity |
| `SESSION_RENEWAL_THRESHOLD` | `5m` | Minimum gap between session extensions |
| `SESSION_ABSOLUTE_TIMEOUT` | `0` | Hard cap on session age; `0` disables |
| `SESSION_SECRET` | – | ≥32 chars; keys session hashing and CSRF |
| `GOOGLE_CLIENT_ID/SECRET`, `GOOGLE_REDIRECT_URL` | – | Your OAuth client |
| `ENCRYPTION_KEY` | – | base64 of 32 bytes; encrypts stored refresh tokens |
| `DATABASE_PATH` | `/data/mailroutine.db` | SQLite file |
| `SCHEDULER_INTERVAL` | `60s` | How often due routines are looked up |
| `DEFAULT_MAX_MESSAGES_PER_RUN` | `500` | Default per-run cap (max 10000) |
| `MAX_CONCURRENT_ROUTINES` | `2` | Parallel runs |
| `GMAIL_BATCH_SIZE` / `GMAIL_REQUEST_TIMEOUT` | `100` / `30s` | Gmail call tuning |
| `LOG_*` | see below | Logging |

Generate secrets: `mailroutine generate-secret` (or `openssl rand -base64 32`).

**Docker Compose note:** the password hash contains `$`. Keep it in single quotes in `.env`, exactly as `hash-password` prints it.

## First login, connect Gmail, create a routine

1. Sign in with `ADMIN_USERNAME` and the password you hashed.
2. **Settings → Connect a Gmail account**, approve the permissions.
3. **Routines → New routine**: pick the account, write a Gmail query (or use the query builder), choose an action and a schedule.
4. Click **Preview matches** to see sender/subject/date of what matches now. Nothing is changed by previews.
5. Optionally **Run now → Dry run** from the routines list to record what *would* happen.
6. Switch the routine to **Enabled**. It runs automatically at its next scheduled time.

New routines start **disabled** so you can preview first. Moving mail to Trash asks for explicit confirmation.

## Managing routines & history

Enable/disable with the toggle (a disabled routine stops immediately, even mid-run), edit, preview, run now, or delete. **History** lists every run with matched / handled / failed counts; expand a row to see failed message IDs and errors. Dry runs are marked and never counted as handled mail. The **Dashboard** shows totals, the next scheduled run and recent executions. Per-message history is kept for 180 days.

Schedule semantics (details in [docs/architecture.md](docs/architecture.md)): times are in the routine's timezone; a monthly routine set to the 31st runs on the last day of shorter months; a time skipped by a DST jump runs right after the jump; after downtime a routine runs **once**, never replaying missed slots.

## Logging

Logs always go to **stdout** (`docker logs mailroutine`). Optionally also write a rotated file:

```env
LOG_FILE_PATH=/data/logs/mailroutine.log
LOG_MAX_SIZE_MB=10   LOG_MAX_BACKUPS=5   LOG_MAX_AGE_DAYS=30   LOG_COMPRESS=true
LOG_LEVEL=info       LOG_FORMAT=json   # or text
```

Both destinations receive identical events. Passwords, hashes, tokens, session cookies and authorization headers are never logged (a redaction layer also scrubs known token shapes). Gmail subjects are not logged. Docker's own stdout log rotation is separate from this file rotation – configure it on the container (`logging: driver: json-file, options: max-size/max-file`) if you want to cap it.

## Sessions and authentication

Authentication is local and single-user; the password hash lives only in your environment, never in the database. Sessions are server-side rows in SQLite (only a keyed hash of the token is stored), delivered in an `HttpOnly`, `SameSite=Lax` cookie (`Secure` when `APP_URL` is https).

Sessions **slide**: you stay signed in while you use the app and are signed out after `SESSION_IDLE_TIMEOUT` of inactivity. Only genuine interaction extends a session (clicks/keys in the UI, and any change you make). An open tab, dashboard polling, the scheduler and health checks do not. Sessions survive restarts. Changing `ADMIN_PASSWORD_HASH`, `ADMIN_USERNAME` or `SESSION_SECRET` and restarting signs everyone out.

To change the password: run `hash-password` again, replace `ADMIN_PASSWORD_HASH`, and `docker compose up -d`.

## Security

See [SECURITY.md](SECURITY.md). Highlights: Argon2id password hash, AES-256-GCM encrypted refresh tokens, CSRF tokens + Origin checks, OAuth state + PKCE, login throttling, strict CSP, no tokens ever sent to the browser, least-privilege Gmail scope. Run MailRoutine behind HTTPS if it is reachable beyond localhost.

### HTTPS reverse proxy (Caddy)

```caddy
mail.example.com {
    reverse_proxy mailroutine:8080
}
```

Set `APP_URL=https://mail.example.com`, register `https://mail.example.com/api/accounts/google/callback` in Google Cloud, and set `TRUSTED_PROXIES` to the proxy's address/network so login throttling sees real client IPs. Caddy handles certificates automatically.

## Backups

The database holds accounts (encrypted tokens), routines and history.

```bash
# consistent snapshot, safe while running:
docker compose exec mailroutine /app/mailroutine backup /data/backups/mailroutine-$(date +%F).db
```

**Back up `ENCRYPTION_KEY` separately.** A database backup without the key is useless for Gmail access: if the key is lost you must reconnect each account (routines and history are unaffected). Never store the key next to the backup.

Restore: stop the container, replace `data/mailroutine.db` with the backup (delete any `-wal`/`-shm` files beside it), start again with the **same** `ENCRYPTION_KEY`.

## Upgrading

```bash
docker compose pull && docker compose up -d
```

Database migrations are applied automatically at startup. Take a backup first. Pin a version (`ghcr.io/geshas/mailroutine:0.1.0`) for controlled upgrades.

## Troubleshooting

| Symptom | Fix |
|---|---|
| Container shows a setup page instead of the app | Required variables are missing: follow the page, or see *Portainer / no shell* above |
| Container exits immediately | You set `MAILROUTINE_SETUP=off`; `docker logs mailroutine` lists each invalid variable |
| `ADMIN_PASSWORD_HASH ... not a valid Argon2id` | Quote the hash with single quotes in `.env` |
| `redirect_uri_mismatch` | Redirect URI in Google Cloud must equal the one in Settings exactly |
| Gmail account shows *needs reconnect* | Token revoked/expired (7-day Testing limit) → Settings → Reconnect |
| Sign-in says *request rejected: open MailRoutine at its configured address* | Browse to exactly `APP_URL` (same scheme, host and port; `localhost` ≠ `127.0.0.1`) |
| Signed out unexpectedly | Idle timeout, or hash/secret changed. Check `SESSION_IDLE_TIMEOUT` |
| Can't write `/data` | Use a named volume or ensure the host dir is writable; the entrypoint chowns it |

More in [docs/oauth-setup.md](docs/oauth-setup.md#troubleshooting).

## Development

```bash
make setup     # .env with fresh secrets + admin password hash (then add GOOGLE_CLIENT_ID/SECRET)
make install   # go modules + npm ci
make dev       # Go API :8080 + Vite UI :5173 with hot reload -> open http://localhost:5173
make help      # everything else: run, build, test, cover, backup, docker-*
```

In `make dev` the browser talks to Vite, so `APP_URL` defaults to `http://localhost:5173`; register `http://localhost:5173/api/accounts/google/callback` as the Google redirect URI for development.

```bash
make test                     # Go (vet + race) and web (lint, types, unit tests); no Gmail account needed
make cover                    # coverage: Go merged profile must stay >= 95%, web thresholds in vitest.config.ts
cd web && npm ci && npm run dev   # UI on :5173, proxies /api to :8080 (see web/README.md)
go run ./cmd/mailroutine      # backend on :8080 (reads .env)
cd web && npm run build       # embed the UI: output goes to web/dist
```

## Architecture

Reusable packages (`pkg/models`, `pkg/schedule`, `pkg/engine`, `pkg/gmail`) are free of SQLite, HTTP, auth and environment dependencies and can be imported by other Go programs. The self-hosted app lives under `internal/`. See [docs/architecture.md](docs/architecture.md).

## API

JSON REST API under `/api`, cookie-authenticated with CSRF header. See [docs/api.md](docs/api.md) and [docs/openapi.yaml](docs/openapi.yaml).

## Contributing

Issues and pull requests are welcome – see [CONTRIBUTING.md](CONTRIBUTING.md). Report vulnerabilities privately per [SECURITY.md](SECURITY.md).

## License

Apache License 2.0 – see [LICENSE](LICENSE). Copyright © MailRoutine contributors. There are no feature limits or license checks. Contributions are accepted under the same license.

## Releasing

Tag `v0.1.0` and push the tag (`git tag v0.1.0 && git push origin v0.1.0`). The release workflow builds `linux/amd64` and `linux/arm64` images and publishes `ghcr.io/geshas/mailroutine:0.1.0`, `:0.1`, and `:latest` (stable tags only). After the first release, set the package visibility to *public* in GitHub → Packages.
