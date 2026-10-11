# Security policy

## Reporting a vulnerability

Please **do not** open a public issue. Use GitHub's private vulnerability reporting (Security tab → *Report a vulnerability*) on <https://github.com/geshas/mailroutine>. Include affected version, reproduction steps and impact. Expect an acknowledgement within a few days.

## Supported versions

Security fixes are released for the latest minor version. Please update to the newest release before reporting.

## Design summary

- Admin password: Argon2id hash supplied through the environment, never stored in the database or logs.
- Gmail refresh tokens: AES-256-GCM encrypted at rest with `ENCRYPTION_KEY`; never sent to the browser or logged.
- Sessions: server-side, token hash only in SQLite, HttpOnly + SameSite=Lax cookie (+Secure over HTTPS), sliding idle timeout, revoked on logout and on credential change.
- CSRF token and Origin validation for state-changing requests; OAuth `state` and PKCE; login rate limiting; strict Content-Security-Policy.
- Database and log files are created owner-only; password hashing concurrency is bounded; unauthenticated endpoints reveal nothing beyond liveness.
- CI runs govulncheck, gosec and npm audit; Dependabot proposes dependency updates weekly.
- Least-privilege Gmail scope (`gmail.modify`); no permanent deletion; message bodies are never fetched or stored.

## Operator guidance

Serve over HTTPS when reachable beyond localhost, keep `.env` and `ENCRYPTION_KEY` out of version control and backups-with-data, restrict access to the `/data` volume, and keep the image updated.
