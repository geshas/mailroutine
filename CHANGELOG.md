# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/) and the project uses [Semantic Versioning](https://semver.org/).

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
