# Contributing to Trakka

Thanks for your interest in contributing. Trakka is an ultra-lightweight Go
backend + PWA frontend, and it's held to a fairly strict set of conventions
and security rules so it stays that way — please read this file, and
[CLAUDE.md](CLAUDE.md), before opening a pull request.

## Before you start

[CLAUDE.md](CLAUDE.md) is the single source of truth for this project's
architecture, conventions, and — most importantly — its **security rules**
(parameterized SQL only, strict URL scheme validation, SSRF guarding on
outbound scraper requests, CSP/security headers, password/session handling).
Skim it first. Anything below repeats or summarizes what's there; CLAUDE.md
wins if the two ever disagree.

## Requirements

- Go 1.27.2+ (see `go.mod`'s `go` directive — it must stay in sync with the
  `modernc.org/sqlite` version and the Dockerfile's builder image tag, see
  CLAUDE.md)
- No CGO, no Docker/Podman required for local development — the backend is
  pure Go and runs with a plain `go run`/`go build`
- Docker or Podman only if you're testing the container build/deployment path

## Local setup

```bash
go build -o trakka ./cmd/server
PORT=8080 DB_PATH=./trakka.db STATIC_DIR=./static TEMPLATES_DIR=./templates go run ./cmd/server
```

The frontend is vanilla JS served straight from `static/` — no build step, no
`npm install`. Edit and refresh.

## Before opening a pull request

Run these and make sure they're all clean:

```bash
go build -o trakka ./cmd/server
go vet ./...
gofmt -l .        # must print nothing
go test ./...     # only some packages have tests today; that's expected
```

If you touched anything under `static/` or `templates/`, exercise the change
in a real browser — there is no frontend test suite, so a passing `go build`
says nothing about the UI. Pay particular attention to anything involving the
service worker (`static/sw.js`) or the offline queue (`static/js/db.js`); see
[docs/PWA.md](docs/PWA.md) before touching either.

## Conventions

Trakka follows an established, fairly opinionated set of conventions — see
the "Project conventions" section of [CLAUDE.md](CLAUDE.md) for the full
list. The highlights:

- **Go stdlib wherever possible.** No web framework/router, no ORM. The only
  non-stdlib, non-`golang.org/x/...` dependency is `modernc.org/sqlite`
  (pure-Go, no CGO — this is load-bearing, don't swap it out). Don't add a
  new third-party dependency without a strong reason.
- **Package boundaries are enforced.** `internal/handlers` never imports
  `database/sql` directly — it only calls methods on `*db.DB` and checks
  `db.ErrNotFound`. If a handler needs a new query, add a method to
  `internal/db`, don't reach around it.
- **All SQL is parameterized**, confined to `internal/db`. Never build a
  query by concatenating or `fmt.Sprintf`-ing a value into it.
- **Frontend has no dependencies and no build step**: vanilla ES6+,
  `document.createElement`/`textContent` only — never `innerHTML` with
  interpolated data. Any URL rendered as `<a href>` must be re-validated
  client-side (`http`/`https` only), mirroring `internal/validate.URL`.
- **UI copy is in French**, code/comments/docs are in English.
- **Comments are rare.** Write code that doesn't need them; when you do add
  one, explain a non-obvious *why*, never restate the *what*.
- **Don't scope-creep a change.** A bug fix doesn't need a refactor riding
  along with it; don't add abstractions, flags, or fallback paths for cases
  that can't currently happen.

## Schema changes

Trakka uses a versioned migration engine (`internal/db/migrate.go`), tracked
via SQLite's `PRAGMA user_version` — not a single idempotent `schema.sql`.
Add a new schema change as a new file at
`internal/db/migrations/NNNN_description.sql` (embedded automatically); it
runs at most once per database, ever, so it needs no `IF NOT EXISTS` guards.
**Never edit an already-shipped migration file in place** — a database that
already applied it recorded that in `user_version` and will never run it
again, so the edit would only reach databases that haven't gotten there yet.
See [docs/DATABASE.md](docs/DATABASE.md#evolving-the-schema) for the full
mechanism, including the one case (widening a `CHECK` constraint) that has
to be Go code instead of a `.sql` file.

## Security

Trakka is held to explicit security rules — see
[CLAUDE.md](CLAUDE.md#security-rules) in full before touching handlers,
auth, the scraper, or the frontend. In short: parameterized SQL only,
`internal/validate.URL` on any user-supplied URL, no `SetEscapeHTML(false)`,
no `innerHTML` with interpolated data, preserve the SSRF dial guard
(`internal/scraper.safeDialContext`) for any new outbound-fetch feature, and
don't loosen the CSP or drop `SameSite=Strict` on the session cookie without
an equivalent replacement. If you find a security issue, please report it
privately rather than opening a public issue — see below.

## Commit messages

Write clear, imperative-mood commit messages that explain *why* a change was
made, not just what changed (`git diff` already shows the what). Keep
unrelated changes in separate commits.

## Pull requests

1. Fork the repo and create a branch off `main`.
2. Make your change, following the conventions above.
3. Run the checks in "Before opening a pull request".
4. Open a PR with a description of the change and, if relevant, how you
   tested it (a passing `go vet`/`gofmt` is not the same as having exercised
   the feature — say explicitly what you did or didn't verify, the same way
   [CLAUDE.md's handoff notes](CLAUDE.md#project-status-and-session-handoff-last-updated-2026-08-26-automatic-price-lookup-feature)
   do).
5. Be responsive to review feedback — small, focused PRs are much easier to
   review than large ones.

## Documentation

If your change affects behavior described in `docs/*.md` or `CLAUDE.md`,
update those in the same PR. Docs here explain the *why*, not just the what,
and stay cross-linked rather than duplicated — see the "Documentation"
section of CLAUDE.md.

## License

By contributing, you agree that your contributions will be licensed under
the same license as the project — see [LICENSE](LICENSE) (GPLv3).
