# CI/CD, security scanning & GitHub governance

> Part of Trakka's modular instructions — see the root [CLAUDE.md](../CLAUDE.md) for the orientation index. Read **this** file when touching `.github/workflows/ci.yml`, `.golangci.yml`, any `#nosec`/`// gitleaks:allow` annotation, or anything under `.github/`. For the actual security *rules* the pipeline enforces (SQL, CSRF, SSRF, CSP, ...) see [backend.md#security-rules](backend.md#security-rules).

## CI/CD & Sécurité

`.github/workflows/ci.yml` is Trakka's GitHub Actions pipeline. It runs on every push to `main`, every `v*.*.*` tag, every pull request targeting `main`, every published GitHub Release, and on-demand via `workflow_dispatch` (with `image_tag`/`push_to_ghcr` inputs for manual/nightly-style builds). Superseded runs on the same branch/PR are cancelled automatically (`concurrency`).

### Pipeline shape

Four jobs, the last depending on the first three (`needs: [lint, test, security-scan]`):

1. **`lint`** — `gofmt -l .`, `go vet ./...`, `go mod verify`, then `golangci-lint run` (via `golangci-lint-action`, version pinned through the `GOLANGCI_LINT_VERSION` workflow env var). `go mod verify` confirms every module in the build matches the checksums recorded in `go.sum`, so a tampered or corrupted dependency can never silently compile in.
2. **`test`** — `go test -v -race -cover ./...`. The race detector needs `CGO_ENABLED=1` (it links a small C runtime) — this is set for this one step only and has no bearing on the shipped binary, which the `Dockerfile` still always builds with `CGO_ENABLED=0` (see [architecture.md](architecture.md)'s "Go version pinning is intentional and fragile"; `modernc.org/sqlite` is pure Go either way, so enabling cgo here changes nothing about which SQLite driver gets used).
3. **`security-scan`** — `govulncheck ./...` (native to the Go ecosystem, maintained by the Go team; flags only vulnerabilities your code actually reaches, not every CVE sitting in an unused transitive dependency), `gosec` (Go SAST, SARIF output), a Trivy filesystem/dependency scan (`go.sum` CVEs, SARIF output), and `gitleaks detect` (secret scanning, SARIF output) — checked out with `fetch-depth: 0` specifically for this job (every other job uses the default shallow clone), since gitleaks scans the *full commit history*, not just the tree at HEAD: a credential removed in a later commit is still compromised for good, so only ever scanning the current checkout would miss exactly the case this tool exists for. All three SARIF files upload to the repo's **Security → Code scanning** tab (`github/codeql-action/upload-sarif`, tagged with a `category` — `gosec` / `trivy-fs` / `gitleaks` — so the tools' findings don't overwrite each other there) and this job needs `security-events: write` to do that. `gitleaks` is installed via `go install github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}` (an exact pinned version, same convention as `govulncheck`/`gosec` below) rather than a marketplace Action, and run with `--redact` so a matched secret's actual value never appears in the job log or the uploaded SARIF. A local, opt-in pre-commit hook (`.githooks/pre-commit`, enabled via `git config core.hooksPath .githooks`) runs `gitleaks protect --staged` for faster feedback before a push — see [docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md#preventing-committed-secrets) — but CI is what actually gates a merge; the hook is a convenience, not the enforcement point, and silently no-ops (rather than blocking) if gitleaks isn't installed locally.
4. **`build-scan-push`** — builds the Docker image (linux/amd64 only, loaded locally) purely to run a Trivy **image** scan (SARIF, `category: trivy-image`) before anything is pushed anywhere; only if that passes does it build the real multi-arch (linux/amd64+arm64) image and push it to `ghcr.io` — and only when the run isn't a pull request (a PR gets the build+scan but never the push; see the `policy` step's logic) — then attests build provenance for the pushed image via `actions/attest-build-provenance` (a GitHub-native SLSA attestation, keyless via OIDC — hence this job's `id-token: write`/`attestations: write` — verifiable later with `gh attestation verify`). Tags follow `docker/metadata-action`: a sanitized branch-name tag and a `sha-<short>` commit tag on *every* push (including feature branches, for traceability), the tag's own semver on a `v*.*.*` push, `latest` *only* on that same `v*.*.*` tag push (`enable=${{ startsWith(github.ref, 'refs/tags/v') }}` — deliberately not `{{is_default_branch}}`, so `latest` no longer moves on an ordinary push/merge to `main`), plus the manual `image_tag` input on a `workflow_dispatch` run.

Both Trivy scans and gosec are hard gates (`exit-code: "1"` on any unfixed `CRITICAL`/`HIGH` finding) — they still upload whatever SARIF they produced first (`if: always()` on the upload step), so a failing run still shows its findings in the Security tab rather than only in the job log. All four SARIF-upload steps additionally check `hashFiles('<report>.sarif') != ''` before uploading and carry `continue-on-error: true`, so a scan tool that crashes before writing a report (e.g. Trivy failing to pull its vulnerability DB) fails cleanly instead of cascading into a second, unrelated-looking "Path does not exist" error in the next step — see the "Fix: gosec findings... plus SARIF-upload robustness" entry in [status.md](status.md) for the incident that prompted this.

### Permissions model

The workflow denies everything by default (`permissions: read-all` at the top level) and each job then declares only what it actually needs, narrowing below even that default:

| Job | Permissions | Why |
|---|---|---|
| `lint` | `contents: read` | just needs to check out the code |
| `test` | `contents: read` | same |
| `security-scan` | `contents: read`, `security-events: write`, `actions: read` | the second grant is for uploading SARIF; the third is required by `upload-sarif` itself to look up the current workflow run (only actually enforced on a private repo, but harmless to grant regardless) |
| `build-scan-push` | `contents: read`, `packages: write`, `security-events: write`, `actions: read`, `id-token: write`, `attestations: write` | pushing to GHCR, uploading the image-scan SARIF, minting the provenance attestation, and (same as `security-scan`) letting `upload-sarif` read the current workflow run each need their own scope |

No job gets `write` access it doesn't use — in particular, only `build-scan-push` can ever push anything, and only on a non-PR event.

**Gotcha discovered the hard way**: `github/codeql-action/upload-sarif` calls the GitHub REST API to look up the current workflow run (used to correlate the SARIF upload with it) — this needs `actions: read`, which is *not* covered by `security-events: write`. On this originally-private repo, omitting it made every `upload-sarif` step fail with `Error: Resource not accessible by integration - .../actions/workflow-runs#get-a-workflow-run`, and because the *next* step in `security-scan` (`Trivy filesystem scan`) had no `if: always()`, that failure skipped it too — so the following `Upload Trivy filesystem SARIF` step (which *does* have `if: always()`) then failed a second, different way: `Error: Path does not exist: trivy-fs-results.sarif`, because the scan that would have produced it never ran. Both symptoms had the single root cause above. Any job that calls `upload-sarif` needs `actions: read` alongside `security-events: write`, even though GitHub's own quickstart examples often show only the latter.

### The golden rule: pin every action to a commit SHA

Every `uses:` in this workflow is pinned to a full 40-character commit SHA, never a floating tag (`@v4`) or branch — a tag can be force-moved to point at different code (the exact "GitHub Action supply-chain poisoning" class of attack this defends against), a SHA cannot. **Any new action added to this workflow, or a version bump of an existing one, must follow the same rule**: resolve the release tag to its commit SHA and pin that, with a `# vX.Y.Z` trailing comment so a human can still tell which release it is at a glance:

```yaml
uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2
```

To find the SHA for a tag: `git ls-remote --tags https://github.com/<owner>/<repo>.git <tag>` locally, or `gh api repos/<owner>/<repo>/tags` (the *tags* endpoint, not `git/refs/tags`, which can return the wrong SHA for an annotated tag — the tags endpoint always returns the dereferenced commit; this actually happened while pinning this workflow: `golangci-lint-action`, `trivy-action`, and `codeql-action`'s tags are annotated, and `git/refs/tags` returned each tag object's own SHA rather than the commit it points to). The GitHub web UI's release page also shows the tag, which you can resolve the same way. Never trust a SHA pasted from anywhere you can't independently re-derive.

### Running the same checks locally

```bash
gofmt -l .                                                  # must print nothing
go vet ./...
go mod verify
go build -o trakka ./cmd/server

golangci-lint run ./...                                     # config: .golangci.yml
CGO_ENABLED=1 go test -v -race -cover ./...                 # -race needs a C toolchain (gcc/clang); CI enables cgo for this step only

go install golang.org/x/vuln/cmd/govulncheck@v1.7.0         # match GOVULNCHECK_VERSION in ci.yml
govulncheck ./...

go install github.com/securego/gosec/v2/cmd/gosec@v2.29.0   # match GOSEC_VERSION in ci.yml
gosec ./...

go install github.com/zricethezav/gitleaks/v8@v8.28.0       # match GITLEAKS_VERSION in ci.yml
gitleaks detect --source . --redact                         # secret scan (full git history)

# Trivy (needs the trivy CLI installed separately, e.g. via your package
# manager or https://aquasecurity.github.io/trivy/):
trivy fs --severity CRITICAL,HIGH --ignore-unfixed .
docker build -t trakka:scan . && trivy image --severity CRITICAL,HIGH --ignore-unfixed trakka:scan
```

`.golangci.yml` configures `errcheck` to exclude the `Close`/`Rollback` deferred-cleanup pattern already used throughout `internal/db` (`defer rows.Close()`, `defer tx.Rollback()`, etc.) — checking the return value of a cleanup call made *after* the operation that actually mattered (the query, the write, the commit) already returned and was checked is a near-universal, low-risk Go idiom that most real-world `errcheck` configs exclude the same way; without it, turning on `golangci-lint` here would fail on pre-existing, harmless findings unrelated to whatever a given PR actually changed. A handful of `#nosec G101`/`#nosec G124` comments in `internal/settings/settings.go`, `internal/handlers/auth.go`, and `internal/auth/service.go` document specific gosec false positives the same way (a settings-table key name and a map of UI strings that only *look* like credentials to a naive string-matcher; and four `http.Cookie` literals gosec's G124 check can't recognize as secure because their `Secure` field is a variable — `s.CookieSecure`/`app.Auth.CookieSecure`, wired from `SESSION_COOKIE_SECURE` — rather than a literal `true`). Three more, added when Web Push notifications first turned on a clean `gosec ./...` gate: `#nosec G115` in `internal/webpush/encrypt.go` (`byte(len(asPub))` — `asPub` is always exactly 65 bytes, an internally-generated `ecdh.P256()` ephemeral public key, never user-controlled, so this can never overflow a byte); `#nosec G201` in `internal/db/push_subscriptions.go` (`ListPushSubscriptionsForUsers`'s `fmt.Sprintf`-built `IN (...)` clause — only the placeholder count is interpolated, every id is still bound as a parameter, the same "variable-length IN clause" idiom the function's own doc comment already explains); and `#nosec G118` in `internal/handlers/push.go`'s `sendToUsers` (the per-subscription cleanup delete deliberately uses its own `subscriptionCleanupTimeout`-bounded `context.Background()`, not the batch's own `ctx`, since that may already be at or past `pushSendTimeout`'s deadline by the time an individual delivery goroutine gets there). The encrypted-backups feature added a few more, each justified inline: `#nosec G101` on `internal/backup/service.go`'s `settingWebDAVPassword` (a settings-table key name — the value itself is sealed with the backup key), and `#nosec G304` on the file opens in `internal/backup/service.go` (paths built by the package itself inside its staging directory, or created via `os.CreateTemp` there) and in `cmd/server/main.go`'s `runDecryptBackup` (paths the operator passes on their own command line). Two gosec findings from that feature were fixed rather than silenced — G703 (a handler deleting a path returned from a function fed by the request body: staged uploads became an opaque `*backup.StagedUpload` handle) and G118 (a background goroutine on `context.Background()`: now `context.WithoutCancel(ctx)` + a timeout). Don't add a new blanket exclusion without the same kind of one-line justification these carry; a real finding should get fixed, not silenced. See the "Fix: gosec findings from the Web Push notifications feature..." entry in [status.md](status.md) for how those three were diagnosed and fixed, and the "Fix: gitleaks blocked the Web Push notifications commit..." entry for a worked example of triaging a gitleaks false positive (`// gitleaks:allow` with a one-line justification) versus a real secret.

## Contribution & GitHub templates

`.github/` carries the standard GitHub community/governance files, in English — like the rest of code, comments, and technical documentation per the "Documentation" convention in [architecture.md](architecture.md) (only end-user-visible UI strings are French):

- **`.github/PULL_REQUEST_TEMPLATE.md`** — auto-filled into every new PR's description. Structured as: a free-form summary, a checkbox "type de changement" (Fix/Feature/Refactoring/Docs/CI-CD), a linked-issue field (`Fixes #123`), and a validation checklist (`go build`/`go vet`/`gofmt`/`go test`/`golangci-lint` all clean, `CLAUDE.md` updated if the PR changes a convention/security rule/architecture it documents, PWA/offline behavior manually checked if `static/js/*.js` or `static/sw.js` changed). **When opening or reviewing a PR, actually work through this checklist rather than leaving it as inert boilerplate** — it mirrors exactly what `lint`/`test`/`security-scan` already enforce in CI (see above), so a PR that fails it will also fail CI.
- **`.github/ISSUE_TEMPLATE/`** — GitHub Issue Forms (YAML, not the older Markdown template format): `bug_report.yml` (description, repro steps, expected behavior, Go/JS logs, environment, browser) and `feature_request.yml` (problem, proposed solution, alternatives considered, a multi-select for which area it touches — lists/budget/sharing/PWA/auth/other — plus a checkbox nudging proposals to stay consistent with the project's "ultra-lightweight, stdlib-first" goal). `config.yml` disables blank issues and points contributors at `CLAUDE.md` and `CONTRIBUTING.md` before they file a ticket.
- **`.github/CONTRIBUTING.md`** — the short contributor workflow (fork → branch → respect project conventions → local checks → PR against `main` using the template above → CI must pass) that links back to the root `CLAUDE.md` for anything substantive rather than duplicating it.
- **`.github/CODE_OF_CONDUCT.md`** — the standard Contributor Covenant v2.1, with the enforcement contact set to the project's maintainer email.

None of this changes any build/runtime behavior — it's pure repository governance, kept in `.github/` alongside `workflows/ci.yml` per GitHub's own convention for where these files are discovered.

**Status**: the whole pipeline above has never actually run on GitHub's runners yet — every Go-side tool it calls was verified locally, and the YAML's structure was validated separately, but the Docker build, both Trivy scans, the SARIF uploads, and the build-provenance attestation are unverified beyond local reasoning. See "What's left" in [status.md](status.md).
