# Development

## Requirements

- Go 1.27.0+ (matches the `go 1.27.0` directive in [go.mod](../go.mod) and the `golang:1.27.0-alpine` build stage in the [Dockerfile](../Dockerfile))
- No CGO toolchain needed — `modernc.org/sqlite` is pure Go

## Running locally

```bash
go build -o trakka ./cmd/server
DB_PATH=./trakka.db STATIC_DIR=./static TEMPLATES_DIR=./templates PORT=8080 ./trakka
```

or without a build step:

```bash
DB_PATH=./trakka.db STATIC_DIR=./static TEMPLATES_DIR=./templates go run ./cmd/server
```

The database file is created (and its schema applied) automatically on first run — no separate migration step.

## Common commands

```bash
go build -o trakka ./cmd/server   # compile
go vet ./...                      # static analysis
gofmt -l .                        # list files needing formatting (should print nothing)
gofmt -w .                        # fix formatting
go mod tidy                       # sync go.mod/go.sum after adding/removing imports
```

There is currently no automated test suite (`go test ./...` finds nothing to run).

## Preventing committed secrets

CI (`.github/workflows/ci.yml`'s `security-scan` job) runs [gitleaks](https://github.com/gitleaks/gitleaks) against the full commit history on every push/PR and fails the build if it finds anything that looks like a credential — see [CLAUDE.md](../CLAUDE.md#cicd--sécurité) for the full pipeline design. That's the authoritative gate; nothing gets merged without passing it.

For faster local feedback before you even push, this repo ships an optional pre-commit hook (`.githooks/pre-commit`) that runs `gitleaks protect --staged` against just the staged diff. It isn't enabled by default — git hooks in `.git/hooks` aren't tracked by git, so `.githooks/` is a tracked stand-in you opt into once per clone:

```bash
go install github.com/zricethezav/gitleaks/v8@v8.28.0   # match GITLEAKS_VERSION in ci.yml
git config core.hooksPath .githooks
```

If gitleaks isn't installed, the hook prints a reminder and lets the commit through rather than blocking it — CI still catches anything it would have caught. Make sure `$(go env GOPATH)/bin` is on your `PATH` (or the hook falls back to checking that path directly), since that's where `go install` puts the binary.

## Manual smoke test

A quick end-to-end check against a running instance. Every `/api/v1/...` call now requires a session, so register/log in first and reuse a cookie jar:

```bash
curl http://localhost:8080/healthz

curl -c cookies.txt -X POST http://localhost:8080/auth/register \
  -d "email=dev@example.com&password=devpassword&password_confirm=devpassword&display_name=Dev"

HOUSE=$(curl -s -b cookies.txt http://localhost:8080/api/v1/houses)
HOUSE_ID=$(echo "$HOUSE" | grep -o '"id":[0-9]*' | head -1 | grep -o '[0-9]*')

LIST=$(curl -s -b cookies.txt -X POST http://localhost:8080/api/v1/lists -d "{\"name\":\"Courses\",\"type\":\"shopping\",\"house_id\":$HOUSE_ID}")
LIST_ID=$(echo "$LIST" | grep -o '"id":[0-9]*' | head -1 | grep -o '[0-9]*')

ITEM=$(curl -s -b cookies.txt -X POST http://localhost:8080/api/v1/items \
  -d "{\"list_id\":$LIST_ID,\"title\":\"Lait\",\"quantity\":2}")
ITEM_ID=$(echo "$ITEM" | grep -o '"id":[0-9]*' | head -1 | grep -o '[0-9]*')

curl -b cookies.txt -X PATCH http://localhost:8080/api/v1/items/$ITEM_ID -d '{"done":true}'
curl -b cookies.txt http://localhost:8080/api/v1/lists/$LIST_ID

# should be rejected (non-http(s) scheme):
curl -b cookies.txt -i -X PATCH http://localhost:8080/api/v1/items/$ITEM_ID -d '{"url":"javascript:alert(1)"}'

# should be rejected (no session):
curl -i http://localhost:8080/api/v1/houses
```

## Project layout

```
cmd/server/main.go     entry point: config, wiring, graceful shutdown, -healthcheck probe
internal/config/       environment-variable configuration (PORT, DB_PATH, STATIC_DIR, TEMPLATES_DIR, OIDC_*, session settings)
internal/models/       House/List/Item/User/Session/HouseMember structs shared by db and handlers
internal/db/           all database/sql usage: connection setup, schema, parameterized queries
internal/auth/         non-HTTP auth logic: bcrypt, session tokens, hand-rolled OIDC client + id_token verification
internal/handlers/     HTTP routing, request validation, security headers, JSON responses, session middleware, /auth/... handlers
internal/validate/     reusable input validators (currently: strict http(s) URL validation)
internal/logbuffer/    in-memory ring-buffer slog.Handler backing the admin console's "Logs" panel (GET /api/v1/admin/logs)
static/                PWA frontend served at "/": index.html, js/app.js, js/list_view.js, js/db.js, sw.js, css/tokens.css, css/base.css, manifest.json, icons/
templates/             login.html, rendered via html/template with server-injected per-request data (OIDC-enabled flag, mode, error message)
Dockerfile             multi-stage build (golang:1.27.0-alpine -> alpine:latest)
compose.yml            trakka + optional radicale services, shared bridge network
docs/                  this documentation
```

Package boundaries are deliberate: `internal/handlers` never imports `database/sql` directly — it only calls methods on `*db.DB` and checks for `db.ErrNotFound`. When adding a feature, keep new SQL in `internal/db` and new HTTP/validation logic in `internal/handlers`/`internal/validate`.

## Building the container image

```bash
docker build -t trakka:latest .
```

If you change the Go version or bump `modernc.org/sqlite`, keep three things in sync: the `FROM golang:X-alpine` line in the [Dockerfile](../Dockerfile), the `go` directive in [go.mod](../go.mod), and the actual minimum Go version required by whichever `modernc.org/sqlite` release you pin (check with `go mod download -json modernc.org/sqlite@vX.Y.Z` and inspect its `GoMod` file) — these three can drift independently and the failure mode (Docker build breaking on a Go toolchain mismatch) is easy to miss locally if your local Go toolchain is newer than the Dockerfile's. See [CLAUDE.md](../CLAUDE.md) for the specific version this project is currently pinned to and why.

## Extending the API

New endpoints follow the existing pattern:
1. If the endpoint needs new data access, add a method to `internal/db/lists.go` or `internal/db/items.go` (or a new file) using a parameterized query, returning `db.ErrNotFound` when nothing matches.
2. Register the route in `(*handlers.Application).Routes()` (in `internal/handlers/app.go`) using the `"METHOD /path"` mux pattern.
3. Write the handler in `internal/handlers`; use the `decodeJSON`/`writeJSON`/`writeError`/`pathID` helpers in `internal/handlers/json.go` rather than reimplementing them. Validate any URL field through `internal/validate.URL`.
4. If the change touches the schema, add a new file at `internal/db/migrations/NNNN_description.sql` (see [docs/DATABASE.md](DATABASE.md#evolving-the-schema)) — no existence guards needed (versioning already ensures it runs once), and don't edit an already-shipped migration file in place.

Full endpoint behavior is documented in [docs/API.md](API.md); the security rules every change must uphold (parameterized SQL, URL scheme validation, security headers, safe DOM rendering) are listed in [CLAUDE.md](../CLAUDE.md#security-rules).

## Frontend

`static/js/app.js`, `static/js/list_view.js` and `static/js/db.js` are vanilla ES6+ (`async`/`await`, no build step, no dependencies), loaded as classic `<script>` tags sharing one global scope (not ES modules) — `list_view.js` (the "Google Tasks"-style list detail view) freely references `state`/`els`/`apiRequest` etc. defined in `app.js`. Every `fetch` call goes through the `apiRequest()` helper, which wraps network failures and non-2xx responses in a single `Error` shown to the user via the on-page error banner, and redirects to `/auth/login` on a `401` (the single choke point that auth-gates the whole SPA, including mid-session expiry) — when adding a new action, reuse `apiRequest()` rather than calling `fetch` directly. All dynamic content is rendered with `document.createElement`/`textContent`, never `innerHTML`, to avoid DOM-based XSS from list/item titles.

Trakka is also an offline-first PWA (`static/sw.js`, `static/js/db.js`) — see [docs/PWA.md](PWA.md) before changing anything related to the service worker, the manifest, or the IndexedDB sync queue; it documents a genuinely non-obvious ordering bug that testing (not code review) caught, and how to test changes to those two files without a browser.
