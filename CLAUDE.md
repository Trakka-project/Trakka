# CLAUDE.md

This is the compact entry point for Claude Code (claude.ai/code) working in this repository. Detailed, topic-specific instructions live in `.claude/*.md` (loaded on demand per the table below) and in `docs/*.md`; this file only holds what's true everywhere and an index of where to look next. **Don't duplicate detail from `.claude/*.md` or `docs/*.md` back into this file** — extend the relevant sub-file instead.

## What this is

Trakka is an ultra-lightweight Go backend (target: <20MB RAM at runtime) for shopping lists and to-do lists, exposed as a JSON REST API and served alongside static PWA assets. It is designed to run identically under Docker and Podman rootless, and is held to explicit security rules (parameterized SQL only, strict URL scheme validation, hardened HTTP response headers) — see "Non-negotiable security rules" below, and [.claude/backend.md](.claude/backend.md#security-rules) for the full rationale, before touching handlers or the frontend.

## Commands

```bash
go build -o trakka ./cmd/server    # compile
go vet ./...                       # static checks
gofmt -l .                         # list any non-gofmt-formatted files (should be empty)
go mod tidy                        # sync go.mod/go.sum after dependency changes

# Run locally (no container needed — pure Go, no CGO)
PORT=8080 DB_PATH=./trakka.db STATIC_DIR=./static TEMPLATES_DIR=./templates go run ./cmd/server
```

```bash
docker compose up -d --build              # Trakka only
docker compose --profile calendar up -d   # Trakka + Radicale (CalDAV sync)
podman-compose up -d                      # same compose.yml works unchanged
```

```bash
make build-apk-capacitor   # optional Android APK (Capacitor), built in a container (docs/MOBILE_BUILD.md)
```

`go test ./...` applies to whatever packages currently have tests (no custom test runner or build tag scheme). See [.claude/ci-security.md](.claude/ci-security.md) for the full local-check recipe (linters, `govulncheck`, `gosec`, `gitleaks`, Trivy) that mirrors CI.

## Where to look

| Working on... | Read |
|---|---|
| Package layout/import boundaries, `internal/config`, DB driver/connection pool/migration engine, Go & dependency version pinning, `cmd/server` (healthcheck/shutdown/logging), Dockerfile/`compose.yml` | [.claude/architecture.md](.claude/architecture.md) |
| `internal/handlers`, `internal/db`, `internal/auth`, `internal/scraper`, `internal/webpush`, `internal/backup` — API/RBAC/sharing/pinning/recurring-items/price-lookup/push-notification/encrypted-WebDAV-backup design — and the **full** non-negotiable security rules | [.claude/backend.md](.claude/backend.md) |
| `static/js/*.js`, `static/sw.js`, `static/css/*.css`, `templates/login.html` — PWA/offline mechanism, i18n, theming, mobile layout rules | [.claude/frontend-pwa.md](.claude/frontend-pwa.md) |
| `.github/workflows/ci.yml`, `.golangci.yml`, gosec/gitleaks/Trivy findings & exemptions, `.github/` templates | [.claude/ci-security.md](.claude/ci-security.md) |
| "What's built, what's verified, what's left", session handoff, the copy-paste prompt for a new session | [.claude/status.md](.claude/status.md) |
| REST endpoint reference | [docs/API.md](docs/API.md) |
| DB schema & migrations | [docs/DATABASE.md](docs/DATABASE.md) |
| Offline/service-worker mechanism (deep dive) | [docs/PWA.md](docs/PWA.md) |
| Production deployment | [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) |
| Local dev setup, pre-commit hooks | [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) |
| Past security audit | [docs/AUDIT.md](docs/AUDIT.md) |
| End-user PWA install steps | [docs/INSTALLATION.md](docs/INSTALLATION.md) |
| `android/`, root `Makefile` — the Android app (Capacitor shell connecting to any Trakka server: connect screen, `TrakkaApp` native plugin, QR scanner, containerized build, signing key) | [docs/MOBILE_BUILD.md](docs/MOBILE_BUILD.md) |

## Non-negotiable security rules

Standing constraints for this codebase — full text, rationale, and code pointers for every rule in [.claude/backend.md#security-rules](.claude/backend.md#security-rules):

- **SQL**: parameterized (`?`) placeholders only, confined to `internal/db` — never concatenate or `fmt.Sprintf` a user-supplied value into a query.
- **URLs**: any user-supplied URL must pass `internal/validate.URL` (absolute `http://`/`https://`, non-empty host) — blocks `javascript:`/`data:` schemes.
- **Passwords & sessions**: `bcrypt` only, never logged in plaintext. Session tokens are `crypto/rand`, stored **hashed** (SHA-256); cookie is `HttpOnly`, `SameSite=Lax`.
- **CSRF**: every `/api/v1/...` write checks `Origin`/`Sec-Fetch-Site`; `/auth/login` and `/auth/register` additionally require a double-submit `csrf_token`.
- **SSRF**: any outbound request to a user-supplied URL (the scraper, Web Push, the WebDAV backup client) must go through a dial guard (`safeDialContext`/`dialGuard`: resolve host → verify public IP → dial that literal IP). Never call `http.Get`/`http.Client.Do` directly on untrusted input.
- **CSP / frontend**: strict `Content-Security-Policy` (two narrow, documented exceptions for the Tailwind Play CDN); never `innerHTML` with interpolated data; don't add a new inline `<script>`/`<style>` without revisiting the CSP.
- **Responses**: JSON encoder keeps `SetEscapeHTML(true)`; every `/api/v1/...` JSON response carries `Cache-Control: no-store`.
- **OIDC**: verify the RS256 signature before ever trusting claims; hard-reject any other `alg` first (alg-confusion defense).

## Current status

The backend (REST v1 API, auth/RBAC, background price scraping, sharing, recurring items, Web Push, a versioned SQLite migration engine), the PWA frontend, offline support, Docker/Compose, and `docs/*.md` are all written and — per the last recorded check — build/vet/gofmt/test clean. The project is deep into iterative feature work; a long tail of frontend-only features were built without browser-automation tooling available and are explicitly marked unverified pending a real browser/device pass. **Full session-by-session history, exactly what's verified vs. not, and the copy-paste "resume this project" prompt live in [.claude/status.md](.claude/status.md)** — read it before assuming something is or isn't done.
