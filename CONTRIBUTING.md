# Contributing

Thanks for helping! MailRoutine is Apache-2.0; by submitting a contribution you agree it is licensed under the same terms.

## Ground rules

- Keep the self-hosted app simple and fully functional. No billing, tenancy, feature limits or license checks belong in this repository.
- `pkg/*` must stay free of SQLite, HTTP, auth, environment and Docker dependencies (other Go modules import them).
- Never log or persist secrets, tokens or message bodies.
- Tests must not need a real Gmail account – use the fakes in the existing tests.

## Workflow

```bash
gofmt -l .            # must print nothing
go vet ./... && go test -race ./...
cd web && npm ci && npm run lint && npm run typecheck && npm run build
```

Open an issue first for larger changes. Keep pull requests focused and add tests for behaviour changes. Security issues: see [SECURITY.md](SECURITY.md), not public issues.
