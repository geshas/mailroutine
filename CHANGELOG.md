# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/) and the project uses [Semantic Versioning](https://semver.org/).

## [0.1.3] - 2026-10-20

Maintenance release: dependency updates and clearer personal Google OAuth setup.

### Added
- A "Buy me a coffee" sidebar button and README link to the maintainer's PayPal.Me page.

### Changed
- Updated the Google API client and frontend dependencies, including React, Radix UI, TanStack and Vitest.
- Updated SHA-pinned checkout, Go setup and Docker build/metadata GitHub Actions.
- Clarified production OAuth publishing, the personal-use verification exception and reconnecting accounts previously authorized in Testing mode.

## [0.1.2] - 2026-10-11

Makes MailRoutine deployable from Portainer and other UIs that have no shell.

### Added
- First-run **setup mode** for containers without a shell (Portainer, NAS UIs): when required configuration is missing, the container serves a stateless page that generates `SESSION_SECRET`, `ENCRYPTION_KEY` and the admin password hash to paste into the stack's environment, instead of exiting. Disable with `MAILROUTINE_SETUP=off`.
- `ADMIN_PASSWORD_HASH_B64`: the admin hash base64-encoded, immune to `$` mangling by compose/Portainer/`.env` parsers.
- `docker-compose.portainer.yml`: a stack file that deploys with empty variables, using a named data volume.
- README section *Portainer / no shell*, SECURITY.md notes on setup mode, and a troubleshooting row for the setup page.

### Changed
- The "missing configuration" error now mentions `ADMIN_PASSWORD_HASH_B64` and the setup page.
- An unconfigured container reports `/health` as 503 `setup_required` (unhealthy) until it is configured.

## [0.1.1] - 2026-10-11

Maintenance release: refreshed the build and release toolchain. No application changes.

### Changed
- Container base image updated from Alpine 3.22 to 3.24 (picks up upstream security patches).
- GitHub Actions updated to current, SHA-pinned releases: `setup-node` 7.1.0, `upload-artifact` 7.0.2, `docker/login-action` 4.6.0, `docker/setup-buildx-action` 4.4.1, `docker/setup-qemu-action` 4.4.0.
- Dependabot no longer proposes TypeScript major upgrades (TypeScript 7 does not build the project yet) or non-LTS Node.js majors.

## [0.1.0] - 2026-10-11

First public release.

### Added
- Self-hosted service that runs recurring Gmail maintenance: archive, move to Trash, mark read/unread, add/remove label.
- Native Gmail search queries with preview, dry run, per-run safety limit and explicit confirmation for Trash.
- Schedules: manual, hourly, daily, weekly, monthly, with timezone and DST handling.
- Multiple Gmail accounts through your own Google Cloud OAuth client (`gmail.modify` scope only).
- Single administrator login with Argon2id password hash, server-side sliding sessions, CSRF protection and login throttling.
- Refresh tokens encrypted at rest with AES-256-GCM.
- Web UI (React, TanStack Router/Query, shadcn/ui): dashboard, routines, query builder with inbox-only and date-window options, run history, settings, light/dark theme.
- Single static binary with embedded UI, SQLite storage with versioned migrations and online backup, Docker image (amd64/arm64), structured logging with optional rotated file.
- OAuth scope verification: an account is only stored if Google granted `gmail.modify`; a partial grant shows an error with a one-click reconnect, and a later “insufficient scopes” response from Gmail flags the account as needing reconnection.
- Traceability: routines (on by default) add `MailRoutine/<Action>` labels to the emails they handle; Settings → MailRoutine labels creates any that are missing, without ever duplicating existing ones.
- Reusable Go packages (`pkg/models`, `pkg/schedule`, `pkg/engine`, `pkg/gmail`).

### Security
- Strict Content-Security-Policy, `X-Content-Type-Options`, `Referrer-Policy`, `Permissions-Policy`, cross-origin isolation headers.
- OAuth `state` + PKCE; session cookie `HttpOnly`, `SameSite=Lax`, `Secure` over HTTPS.
- Database and log files created owner-only (`0600`); unauthenticated `/health` does not reveal the version.
- Password hashing concurrency is bounded so login floods cannot exhaust memory.
- CI runs `govulncheck`, `gosec`, and `npm audit`.
