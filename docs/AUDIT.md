# Application Security Audit — Trakka

| | |
|---|---|
| **Scope** | Go backend (`cmd/`, `internal/`), HTML templates (`templates/`), PWA frontend (`static/js/`, `static/sw.js`), SQL layer (`internal/db/`), session management, dependencies (`go.mod`) |
| **Date** | 2026-08-30 |
| **Branch** | `feature/security_audit` |
| **Method** | Exhaustive manual code review + a working proof of concept for the authorization findings + tooling (`go vet`, `golangci-lint`, `gosec`, `govulncheck`) + dynamic validation against a running instance |
| **Overall rating before remediation** | **6.8 / 10 — B** |
| **Overall rating after remediation** | **9.4 / 10 — A** |

---

## 1. Executive summary

Trakka starts from a security baseline well above average for a self-hosted project of its size. The fundamentals are not merely present but **correct in the details**: fully parameterized queries confined to `internal/db`, bcrypt password hashing, session tokens stored **hashed** (SHA-256) and never in cleartext, a dummy bcrypt comparison that defeats timing-based user enumeration, OIDC verification that rejects any `alg` other than RS256 *before* any key lookup, an SSRF guard that dials the validated IP literal itself (closing the DNS-rebinding window), and a frontend that builds its DOM exclusively through `textContent`/`createElement`. Neither the SAST tooling nor the dependency scanner reported anything before this audit began.

The audit nevertheless identified **16 findings**: **2 High**, **8 Medium**, and **6 Low**. No critical vulnerability was found.

The most serious finding (**H-01**) is a **privilege escalation confirmed by an executable proof of concept**: a user holding nothing more than a write-level share on a list could re-tag that list into a Space they own, then share that Space — completely bypassing the project's own explicit rule that "access granted through a share can never itself be used to extend further access". The second (**H-02**) is structural: no rate limiting existed on `/auth/login`, leaving bcrypt's cost as the only brake on password spraying against an exposed instance.

The recurring theme among the Medium findings is **incomplete defense in depth** rather than absent controls: CSRF protection rested entirely on a single mechanism (`SameSite=Lax`) that by construction does not protect login forms; the SSRF blocklist covered the obvious ranges but let CGNAT, `0.0.0.0/8` and NAT64/6to4 encapsulation through; the OIDC client accepted a cleartext-HTTP issuer and RSA keys of any size; and the frontend's IndexedDB mirror survived logout, exposing one user's data to the next on a shared browser.

**Every High, Medium and Low finding has been remediated.** The two items originally recorded as accepted risks — the account-enumeration oracle (L-06) and the third-party Tailwind CDN (BP-01) — were subsequently fixed as well, and are documented in §4 alongside the rest.

### What was already correct (and must not regress)

These properties were verified and should be preserved:

- **SQL injection** — none. Every query uses `?` placeholders and lives in `internal/db`. The only two places that assemble SQL dynamically (`ListListsForUser`, `ListPriceAlertsByHouse`) concatenate literal fragments only; every value remains a bound parameter.
- **Sessions** — a 32-byte `crypto/rand` token, stored as a SHA-256 hash, so a database leak alone hands out no live sessions. The cookie is `HttpOnly` + `Secure` (configurable) + `SameSite=Lax`, and expiry is enforced in SQL.
- **XSS** — `html/template` (contextual escaping) for the single server-rendered page; `textContent`/`createElement` everywhere else. All 20 `innerHTML` uses inject static SVG constants with no interpolation whatsoever. `SetEscapeHTML(true)` is left in place on the JSON encoder.
- **Authorization** — every mutating route goes through `authorizeHouseAccess` / `authorizeHouseOwner` / `authorizeListAccess` / `authorizeItemAccess` / `authorizeSpaceOwner` / `authorizeAdmin`. A consistent "don't distinguish nonexistent from unauthorized" convention (404 rather than 403) applies to personal resources.
- **SSRF** — `safeDialContext` resolves the host and then dials **the validated IP literal**, which closes the TOCTOU window a "resolve, check, then dial the hostname" approach would leave open for DNS rebinding.
- **Go error handling** — all HTTP response bodies are closed (5 of 5), every detached goroutine has a cancellable context and a buffered channel (no leaks), and `errors.Is` is used consistently with the package sentinels.

---

## 2. Findings

| ID | Severity | Category | Finding | File | Status |
|---|---|---|---|---|---|
| **H-01** | 🔴 **High** | Authorization / IDOR | Privilege escalation: a write-share recipient can re-tag a list into their own Space, then share that Space to grant third parties access | `internal/handlers/lists.go` | ✅ **Fixed** |
| **H-02** | 🔴 **High** | Authentication | No rate limiting on `/auth/login` or `/auth/register`: unlimited brute force / password spraying | `internal/handlers/auth.go` | ✅ **Fixed** |
| **M-01** | 🟠 Medium | CSRF | No anti-CSRF token on the login/registration forms → login CSRF (`SameSite` protects nothing when there is no pre-existing session) | `templates/login.html`, `internal/handlers/auth.go` | ✅ **Fixed** |
| **M-02** | 🟠 Medium | CSRF | The JSON API relied solely on `SameSite=Lax` — no `Origin` / `Sec-Fetch-Site` check as a second layer | `internal/handlers/app.go` | ✅ **Fixed** |
| **M-03** | 🟠 Medium | Authentication | Closing registration (`registration_open=false`) was not enforced for OIDC auto-provisioning: an access control that did not control access | `internal/auth/oidc_login.go` | ✅ **Fixed** |
| **M-04** | 🟠 Medium | SSRF | Incomplete IP blocklist: CGNAT `100.64.0.0/10`, `0.0.0.0/8`, `240.0.0.0/4`, TEST-NET, NAT64 `64:ff9b::/96`, 6to4 `2002::/16` all uncovered | `internal/scraper/scraper.go` | ✅ **Fixed** |
| **M-05** | 🟠 Medium | OIDC / Transport | Cleartext `http://` OIDC issuer accepted (interceptable JWKS → forged `id_token`); RSA keys of arbitrary size accepted; IdP response bodies unbounded | `internal/auth/oidc.go` | ✅ **Fixed** |
| **M-06** | 🟠 Medium | Data exposure | The IndexedDB mirror and offline write queue survived logout: one user's data shown to the next on a shared browser | `static/js/app.js`, `static/js/db.js` | ✅ **Fixed** |
| **M-07** | 🟠 Medium | Input validation | No length bound on any text field (name, title, email, icon) or on `quantity`: ~1 MiB persistable per field | `internal/handlers/*.go` | ✅ **Fixed** |
| **M-08** | 🟠 Medium | DoS / resources | Unbounded concurrent scrapes: outbound request amplification triggerable by any authenticated user | `internal/handlers/scrape.go` | ✅ **Fixed** |
| **L-01** | 🟡 Low | Information disclosure | Directory listing enabled on `http.FileServer` (`/js/`, `/css/`, `/icons/`, `/locales/`) | `internal/handlers/app.go` | ✅ **Fixed** |
| **L-02** | 🟡 Low | HTTP headers | HSTS, `Permissions-Policy`, COOP and CORP all missing | `internal/handlers/middleware.go` | ✅ **Fixed** |
| **L-03** | 🟡 Low | Sessions | Expired sessions were never pruned: unbounded table growth | `internal/db/sessions.go` | ✅ **Fixed** |
| **L-04** | 🟡 Low | Authentication | A password over 72 bytes (bcrypt's limit) produced an opaque `bad_request` instead of a specific message | `internal/auth/password.go` | ✅ **Fixed** |
| **L-05** | 🟡 Low | Robustness | Multi-document JSON bodies (`{}{}`) silently accepted; 500 instead of 404 for a missing list | `internal/handlers/json.go`, `rbac.go` | ✅ **Fixed** |
| **L-06** | 🟡 Low | Information disclosure | Account enumeration through the invite/share endpoints | `internal/handlers/house_members.go`, `shares.go` | ✅ **Fixed** |
| **BP-01** | 🔵 Best practice | Supply chain | Third-party `cdn.tailwindcss.com` script executing in the application's own origin, unpinnable by SRI | `static/index.html`, `templates/login.html` | ✅ **Fixed** |

---

## 3. Detail by category

### 3.1 Injection & database — ✅ no vulnerability

**Assessment.** All data access goes through `database/sql` with `?` placeholders, exclusively from `internal/db`. The architectural boundary holds: `internal/handlers` never imports `database/sql`.

Two places build a query dynamically, and both were inspected specifically:

```go
// internal/db/lists.go:62-75 — the concatenated fragments are literals,
// never values; those stay bound through args...
conditions = append(conditions, `lists.type = ?`)
args = append(args, typeFilter)
...
query += ` WHERE ` + strings.Join(conditions, ` AND `)
rows, err := d.conn.QueryContext(ctx, query, args...)
```

The same shape appears in `internal/db/price_alerts.go:86-95`. Both `typeFilter` and `status` are additionally validated against an allowlist (`models.ValidListTypes`, `models.ValidPriceAlertStatuses`) *before* reaching the db layer — two independent protections.

The three `fmt.Sprintf` calls in `internal/db/migrate.go` build `PRAGMA user_version = %d` from an internal `int` (SQLite accepts no bound parameter on a PRAGMA); no user data passes through them.

**Also verified:** `PRAGMA foreign_keys(ON)` set in the DSN, consistent `ON DELETE CASCADE` chains, the deliberate single-connection pool, and `email TEXT NOT NULL UNIQUE COLLATE NOCASE` (so user lookup is case-insensitive with no bypass).

**Recommendation: none.** Keep the rule that every new query uses `?` and lives in `internal/db`.

---

### 3.2 Authentication & sessions

#### 🔴 H-02 — No rate limiting on authentication

**Description.** `handleLoginSubmit` accepted an unlimited number of attempts. On an internet-facing instance the only brake on password spraying was bcrypt's CPU cost (~60 ms per attempt), still allowing thousands of attempts per minute in parallel. Combined with a minimal password policy (8 characters, no composition requirement), the account-compromise risk was real.

**Remediation.** A new `internal/handlers/ratelimit.go` implements an in-memory fixed-window counter with no external dependency (consistent with the project's stdlib-first convention), using **two** independent buckets:

- **per client IP** — 30 attempts / 15 min — blunts spraying many accounts from one host;
- **per submitted email** — 8 attempts / 15 min — blunts guessing one account from many hosts, **and is the bucket that actually does the work behind a reverse proxy**, where every request shares the proxy's address.

`X-Forwarded-For` is deliberately **ignored**: Trakka has no configuration describing which proxies to trust, and honoring an unvalidated header would let a single host defeat the IP bucket entirely by varying one request header. That reasoning is recorded in the code.

A **successful** authentication resets the email bucket (`rl.reset`), so a user who mistypes and then corrects never locks themselves out.

```go
// internal/handlers/auth.go:126 — both buckets are always recorded, never
// short-circuited, so an attacker cannot keep one cold by tripping the other.
func (app *Application) allowAuthAttempt(r *http.Request, email string) bool {
	ipOK := app.authIPLimiter().allow(clientIP(r), authRateMaxPerIP)
	emailOK := true
	if email != "" {
		emailOK = app.authEmailLimiter().allow(email, authRateMaxPerEmail)
	}
	...
}
```

**Dynamic verification** (running instance): the first seven attempts return `error=invalid_credentials`; the eighth and onward return `error=rate_limited`.

#### 🟠 M-03 — Closing registration did not apply to OIDC

**Description.** `handleRegisterSubmit` checked `RegistrationOpen`, but `handleOIDCCallback` → `LoginOrProvisionOIDCUser` created a local account for any unknown OIDC identity **without ever consulting that setting**. An administrator who closed registration through the admin panel believed the instance was closed while anyone holding an account at the configured IdP could still obtain one. An access control that does not enforce what it advertises is a vulnerability, not merely an inconsistency.

**Remediation.** `LoginOrProvisionOIDCUser` now takes `allowProvisioning bool` and returns a new `auth.ErrRegistrationClosed` sentinel. **Existing OIDC accounts continue to sign in normally** — only *creating* an account is blocked. The IdP-supplied `email` and `name` claims are also length-bounded on the way through, so a hostile or misconfigured provider cannot persist an arbitrary string.

#### 🟠 M-05 — OIDC client hardening

Three distinct weaknesses in `internal/auth/oidc.go`:

1. **Cleartext HTTP issuer accepted.** Neither `NewOIDCClient` nor the admin panel checked the scheme. Over `http://`, anyone on the network path can substitute the JWKS with keys they hold and **forge a valid `id_token` for any account on the instance** — a total authentication compromise.
2. **RSA key size unchecked.** `RS256` says nothing about the modulus; a JWKS advertising a 512-bit key was accepted and honored.
3. **Unbounded response bodies.** All three reads (discovery document, token response, JWKS) decoded a body of unlimited size.

**Remediation:**

- `validateIssuerURL` requires `https`, with an explicit `OIDC_ALLOW_INSECURE_ISSUER=true` escape hatch for an IdP reachable only on a private container network (a real self-hosted case: Authelia or Keycloak on the same Docker network). Secure by default, without stranding anyone.
- The same check applies to **all three endpoints the discovery document names** (`authorization_endpoint`, `token_endpoint`, `jwks_uri`), so a document served over HTTPS cannot redirect the token exchange or the JWKS fetch onto cleartext HTTP.
- `minRSAKeyBits = 2048` (the NIST SP 800-57 / RFC 7518 §3.3 floor); weaker keys, and keys whose exponent falls outside a sane range, are skipped.
- `io.LimitReader(resp.Body, 1 MiB)` on all three reads.

**Dynamic verification:** `PATCH /api/v1/admin/settings` with `"oidc_issuer":"http://idp.internal"` → `400` carrying the escape-hatch message; with `https://…` it clears the scheme check and fails later on network discovery, as expected.

#### 🟡 L-03 — Expired session cleanup

`sessions` rows were deleted only on explicit logout, so every expired session an instance ever issued stayed on disk indefinitely. Added `db.DeleteExpiredSessions` plus an hourly loop in `cmd/server/main.go`, using the same detached, cancellable-context pattern as the existing price scan.

#### 🟡 L-04 — bcrypt's 72-byte limit

`golang.org/x/crypto/bcrypt` **refuses** (rather than truncating) passwords longer than 72 bytes. A user choosing a long passphrase received an unexplained `error=bad_request` redirect. `ValidatePasswordStrength` now bounds this explicitly with a dedicated `password_too_long` message.

**Dynamic verification:** a 100-character password → `error=password_too_long`.

#### Verified and found correct

- **Hashing**: bcrypt at `DefaultCost`. *Note*: Argon2id would be a more modern choice, but bcrypt at default cost remains fully acceptable and is not a vulnerability; migrating would require a progressive re-hash path, outside the scope of a security fix.
- **Session cookie**: `HttpOnly` ✓, `Secure` driven by `SESSION_COOKIE_SECURE` (default `true`) ✓, `SameSite=Lax` ✓ (the `Strict` → `Lax` change is documented and justified: Lax still withholds the cookie from cross-site `fetch`/XHR and cross-site form POSTs, and **no `GET` route in this application mutates state**).
- **Session fixation**: a fresh session is created on every login (`finishLogin`); identifiers are never reused.
- **Timing enumeration**: `Authenticate` runs a dummy bcrypt comparison for an unknown email *and* for an OIDC-only account, aligning all three timing profiles.
- **JWT verification**: `alg != "RS256"` is rejected **before any key lookup** (alg-confusion defense), the signature is verified **before** any claim is trusted, and `iss`/`aud`/`exp`/`iat` follow with clock-skew tolerance. That ordering is correct and must be preserved.
- **PKCE S256** plus `state` and `nonce`, with a single-use flow cookie cleared unconditionally.
- **Admin escalation**: impossible through the API. `is_admin` is set only for the very first account created, inside a transaction (`internal/db/users.go`), so two concurrent registrations cannot race into it.

---

### 3.3 XSS & HTML injection — ✅ no vulnerability

**Templates.** `templates/login.html` is the only server-rendered page, through `html/template`, whose escaping is contextual. The `Error` field is never raw query-string content in any case: it is resolved through a lookup table (`loginErrorMessages`) from a fixed, known `?error=` code.

**Frontend.** All 20 `innerHTML` occurrences were inspected individually: **every one** injects a static SVG constant (`TRASH_ICON_SVG`, `PENCIL_ICON_SVG`, `CHEVRON_ICON_SVG`, …) with no interpolation. The code already carries the right warning:

```js
// static/js/app.js:175 — "Static, hard-coded icon markup (never interpolates
// user data) — safe to insert via innerHTML. Never reuse this pattern for
// anything containing a list/item title, URL, or other user-supplied value"
```

There is no `eval`, `new Function`, `document.write`, nor any write to `style.cssText`/`setProperty` with user data. The Space colour — the only user field that reaches CSS-adjacent territory — only ever lands in an `<input type="color">.value` and is server-validated as `^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$` regardless.

**URL sinks.** The three assignments of user data to `href`/`src` (`list_view.js:632`, `list_view.js:570`, `notifications.js:92`) are **all** guarded by `isSafeHttpUrl()` at their call sites (`list_view.js:786`, `:810`, `:1428`, `notifications.js:90`), mirroring `internal/validate.URL` on the server.

**Hardening added (M-07).** `validate.Text()` now strips C0/C1 control characters and Unicode bidirectional overrides (`U+202A`–`U+202E`, `U+2066`–`U+2069`) from every text field. This is **not** an XSS defense — that remains `textContent` and `html/template` — but it closes the adjacent abuses: newline injection into a log line, ANSI escape sequences interpreted when the data is read through `sqlite3`/`jq` in a terminal, and bidirectional rendering tricks in the UI.

**Dynamic verification:** a title of `"Lait\x1b[31mROUGE\nligne2‮EVIL"` is persisted as `"Lait[31mROUGEligne2EVIL"`.

---

### 3.4 CSRF

#### 🟠 M-01 — No anti-CSRF token on the authentication forms

**Description.** `CLAUDE.md` documented an explicit trade-off: *"/auth/login and /auth/register deliberately carry no separate CSRF token: there's no pre-existing session for a forged POST to hijack at that point."* That reasoning is **incomplete**. The attack is not session hijacking but session **imposition**: an attacker makes the victim's browser submit a POST to `/auth/login` carrying **the attacker's own** credentials, from a page the victim visits. `SameSite=Lax` prevents nothing here (no cookie is needed for the request), and the browser accepts the response's `Set-Cookie`. The victim is silently signed into the attacker's account, and **everything they subsequently enter — shopping lists, links, budgets — lands in an account the attacker reads**.

**Remediation.** A new `internal/handlers/csrf.go` implements the classic double-submit pattern.

- `GET /auth/login` issues a 32-byte `crypto/rand` token, set in an **HttpOnly** cookie (`trakka_csrf`, `Path=/auth`, `SameSite=Lax`, 12 h) and rendered into a hidden field on both forms.
- The POST handlers compare the two halves in constant time (`subtle.ConstantTimeCompare`).
- An existing cookie is **reused** rather than regenerated, so several open tabs — or a page restored from the back/forward cache — all stay valid.
- Failure redirects with `error=csrf_failed` and a dedicated user-facing message.

Because the cookie is `HttpOnly` and `SameSite=Lax`, a cross-site attacker can neither read nor predict its value.

**Dynamic verification:** `POST /auth/register` with no token → `error=csrf_failed`; with the token harvested from the page → `302` to `/` and a session established.

#### 🟠 M-02 — The JSON API rested on `SameSite` alone

**Description.** The only CSRF defense for `/api/v1/...` was the session cookie's `SameSite=Lax` attribute. The reasoning is sound *today*, but it is **single-layered**: it depends entirely on the invariant that no `GET` route mutates state (which a future commit can break unnoticed), on `SameSite` behaving correctly in every browser, and on the cookie never moving to `SameSite=None`. On top of that, `POST /auth/logout` remained triggerable cross-site (forced logout of the victim — a nuisance, but state changed from another site).

**Remediation.** A `requireSameOriginWrite` middleware, applied **globally** (so to `/auth/...` as well as `/api/v1/...`), inside the logging and header middleware so a rejected request is still traced:

- for `POST`/`PUT`/`PATCH`/`DELETE` only;
- when `Origin` is present, its **host** must match the request's host (or the host of `BASE_URL`). **Only the host is compared, never the scheme** — behind a TLS-terminating reverse proxy the browser announces `https` while `r.Host` carries only the hostname, so requiring a scheme match would break every such deployment for no gain;
- otherwise, `Sec-Fetch-Site: cross-site` is rejected;
- **neither header present → allowed through**: that is a non-browser client (curl, a script, the container healthcheck), which is by definition not a CSRF vector, since CSRF means borrowing a browser's ambient credentials.

**Dynamic verification** (10 unit test cases plus a running instance): `POST /api/v1/houses` with `Origin: https://evil.example` → `403`; with `Origin: http://127.0.0.1:8099` → `201`; cross-site `POST /auth/logout` → `403`; no header at all (curl) → `201`.

---

### 3.5 Authorization & IDOR

#### 🔴 H-01 — Privilege escalation by list re-tagging (PoC confirmed)

**Description.** `internal/db.AccessLevelForList` grants access to a list through three routes, one of which is a share of the "Space" (`custom_categories`) the list is attached to via `lists.custom_category_id`. `handleListsUpdate` accepted a change to that field from **anyone holding write access** — which a plain `list_shares`/`space_shares` grant confers.

A share recipient could therefore:

1. move the list they were lent into a Space **they own**;
2. share that Space with an arbitrary third party;
3. that third party gains `write` access to a list nobody ever shared with them.

This completely bypasses the explicit rule in `handleListShareCreate` — *"sharing a List requires actual House membership of it, so access granted through a share can never itself be used to extend further access."* The symmetric side effect: the same recipient could **detach** the list from its original Space, revoking every other recipient's access.

**Proof of concept executed** against the pre-fix code:

```
PUT /lists/1 by a write-share recipient -> 200
  {"id":1,"custom_category_id":1,"custom_category":{"user_id":2,...}}
third party's access level to the owner's list: "write"
ESCALATION CONFIRMED
```

**Remediation** (`internal/handlers/lists.go:215-255`): changing `custom_category_id` now requires **actual membership of the list's House** — exactly like deleting it, and for the same reason. A write-share recipient can still rename, retype and re-icon the list; they simply cannot move it between Spaces. A request that echoes back the value it already had (`sameCategoryID`) is not treated as a change, so clients performing a full `PUT` are unaffected.

**Regression tests added** (`internal/handlers/security_test.go`), all three passing:

- `TestListUpdateCannotRetagIntoAnotherUsersSpace` — reproduces the PoC; expects `403` **and** asserts the third party gains no access;
- `TestListUpdateAllowsHouseMemberToRetag` — a legitimate member can still organize their lists;
- `TestListUpdateByShareRecipientKeepsWorkingForOtherFields` — renaming through a share still works.

#### 🟡 L-06 — Account enumeration through invite/share

**Description.** `handleHouseMembersInvite`, `handleListShareCreate` and `handleSpaceShareCreate` resolved the submitted address to a user row and answered `404 "no account exists for this email yet"` when there was none. That reply is an oracle: any authenticated user could ask the instance whether an arbitrary email address has an account here, one request at a time. On a public-registration instance, learning "person X uses this service" is a genuine privacy leak.

The comment in the code explained the constraint honestly — there is no email-sending infrastructure, so an invitation to an unregistered address could never be redeemed, and failing loudly seemed better than creating a ghost row. The fix removes that constraint rather than working around it.

**Remediation.** A new `pending_invitations` table (migration 14) decouples an invitation from a resolved user id:

- An invitation is recorded **against the email address**, with no user lookup, and **grants nothing** at invite time. The response is therefore identical whether or not the address is registered — which is only honest because the invitation is genuinely recorded either way.
- `db.MaterializePendingInvitations` turns invitations into real `house_members` / `list_shares` / `space_shares` rows when the invited person **next authenticates**. It is called from `handleMe` (the endpoint the frontend hits exactly once per app boot), and immediately after local registration and OIDC provisioning.
- Because nothing is granted until the recipient signs in **themselves**, an invitation can never attach data to an address whose owner does not control it.
- The one case still answered differently is *"already a member of this house"* (`409`), which discloses nothing: the caller can read the roster of their own house to learn the same thing.

Two side effects, both improvements: inviting somebody who **has not registered yet now works** (previously impossible), and rosters gained a pending half so a successful invitation no longer looks like it did nothing. Withdrawing an unaccepted invitation needed a new endpoint, since a pending invitation has no user id to name it by — `DELETE /api/v1/{houses,lists,custom-categories}/{id}/invitations?email=…`.

**Residual risk, stated plainly.** A pending invitation becomes a member entry once the recipient signs in, so an attacker who invites an address and watches their own roster can eventually infer that the address exists *and is active*. That is the same behaviour Slack, GitHub and Notion exhibit ("invited" versus "joined"), it requires the victim to act, and it cannot distinguish "no account" from "account exists but inactive". The reliable, instant oracle is gone.

**Regression tests added:** `TestInviteDoesNotDiscloseAccountExistence` asserts that the response for a registered and an unregistered address is byte-identical across all three endpoints (after blanking the echoed address and the row id/timestamp), and `TestInviteGrantsNothingUntilRecipientSignsIn` asserts that membership materializes only on sign-in. Four db-layer tests cover materialization for a new account, case-insensitive matching and permission upsert, an invitation to a since-deleted target granting nothing, and withdrawal.

**Dynamic verification** against a running instance: inviting an unregistered and a registered address returns the same response shape; the roster shows both as `pending` with `user_id: 0`; the invited user's house list is unchanged until they call `/api/v1/me`, at which point the membership appears; withdrawal returns `204` then `404`; a list share to an unregistered address materializes with the correct `write` permission once that address registers, and the new user can immediately post an item to the shared list.

**Later interaction — the sharing autocomplete (`GET /api/v1/users/search`).** Added after this audit, it is deliberately mode-dependent. On a **self-hosted** instance (`SAAS_MODE=false`, the default) it lets any signed-in user find any other account by name or email address: a household's members are meant to find each other, so the instance's account list is intentionally not secret from its own users there — the oracle this finding closed on the invite/share endpoints is moot on such an instance by design. With **`SAAS_MODE=true`** the property above holds in full: the search only ever matches email addresses among the caller's existing contacts (people they already share a house, list or space with), never a stranger's account even by its exact address, so it reveals nothing the caller couldn't already read from their own rosters. Both modes are rate-limited per account (60 searches/minute). Regression test: `TestUserSearchSaaSModeMatchesEmailOnlyAmongContacts` (`internal/handlers/user_search_test.go`).

#### Authorization model — verified map

Every mutating route was traced to its check. No other IDOR was found:

| Resource | Read | Mutate | Check |
|---|---|---|---|
| Houses | member | **owner** (rename/delete) | `authorizeHouseOwner` |
| Members | member | owner, **except self-removal** ("leave") | `house_members.go` |
| Lists | `read` (3 sources) | `write`; **delete and Space change: House member** | `authorizeListAccess` + H-01 |
| Items | `read` via the list | `write` via the list | `authorizeItemAccess` |
| Spaces | owner only | owner only | `GetCustomCategoryForUser` (`WHERE user_id`) |
| Space sharing | owner | owner | `authorizeSpaceOwner` |
| List sharing | **House member** | **House member** | `authorizeHouseAccess` |
| Pinning | share holder | share holder | intentional, covered by dedicated tests |
| Price alerts | House member | `write` on the item | `authorizeItemAccess` |
| Admin | `is_admin` | `is_admin` | `authorizeAdmin` |

One case analysed and judged **correct**: a House member may tag a shared list with their personal Space and share that Space. This is not an escalation, since that member could share the list directly anyway (`handleListShareCreate` requires only House membership, which they have).

Every path identifier goes through `pathID` (strictly positive integer); query identifiers (`house_id`, `list_id`) are parsed and validated before any authorization check.

---

### 3.6 Scraper & SSRF

#### 🟠 M-04 — Incomplete address blocklist

**Description.** `isPublicIP` relied solely on the standard library's predicates (`IsLoopback`, `IsPrivate`, `IsLinkLocalUnicast`, `IsMulticast`, `IsUnspecified`). Correct for the textbook cases, but **several ranges just as effective for reaching internal resources were uncovered**:

| Range | Why it matters |
|---|---|
| `100.64.0.0/10` | CGNAT / shared address space — routinely used for internal networks in cloud and Kubernetes deployments |
| `0.0.0.0/8` | "this network" — on Linux, connecting to `0.x.y.z` reaches the local host |
| `240.0.0.0/4` | reserved, includes the `255.255.255.255` broadcast address |
| `192.0.0.0/24`, TEST-NET-1/2/3, `198.18.0.0/15` | IETF assignments and test ranges |
| `64:ff9b::/96` (NAT64), `2002::/16` (6to4) | **embed an arbitrary IPv4 address**: on a capable host, `64:ff9b::a00:1` reaches `10.0.0.1`, bypassing a naive IPv6 check entirely |

Additionally, an IPv4-mapped IPv6 address (`::ffff:127.0.0.1`) was not unmapped before evaluation.

**Remediation** (`internal/scraper/scraper.go:125-190`): a `blockedRanges` table documented range by range, `To4()` applied up front to unmap, and `IsInterfaceLocalMulticast` added.

**Verification.** A new `TestIsPublicIPRejectsNonRoutableRanges` covers 19 addresses that must be blocked and 4 public ones that must not. Additional dynamic verification against a running instance: six internal URLs (`127.0.0.1`, `169.254.169.254`, `10.0.0.1`, `100.64.0.1`, `::ffff:127.0.0.1`, `0.1.2.3`) all return `price_status: "none"` — nothing is fetched. And against an `httptest` loopback server serving a valid price:

```
loopback fetch correctly refused: no public IP address for host "127.0.0.1"
```

#### 🟠 M-08 — Unbounded scrape concurrency

Every item create/update carrying a URL started a fetch goroutine with no global ceiling. An authenticated user could script a burst of item creations and turn the server into an **outbound request amplifier** — a load generator aimed at a third-party site from Trakka's IP address — while accumulating sockets and 2 MiB read buffers server-side.

A `scrapeSem` semaphore with 8 slots was added (`internal/handlers/scrape.go`). Requests over the limit **wait** for a slot rather than being dropped, and give up with the rest of the lookup when `scrapeTimeout` expires; the user-visible effect of saturation is simply `price_status: "pending"`, which the frontend already handles.

#### Verified and found correct

- Dialing **the validated IP literal** (not the hostname) closes the DNS-rebinding TOCTOU window. TLS still verifies the certificate against the original hostname, since `net/http` uses the request's host for SNI and validation.
- Redirects go back through the same `Transport`, hence the same guard — a redirect to an internal address is blocked at dial time.
- The scheme is re-checked inside `FetchProductInfo` (defense in depth beyond `validate.URL`), `Content-Type` is checked, the body is capped at 2 MiB, redirects are limited to 5, and `resolveImageURL` accepts only an absolute `http(s)` URL — a `javascript:`/`data:` URI can never become an `image_url`.

---

### 3.7 Go practices & architecture

#### Error handling — ✅ conformant

Explicit error paths throughout; `Recover` remains a safety net rather than a handling mechanism. Internal errors never leak to the client: `serverError` logs the real error and returns a generic `"internal server error"`. Sentinels (`db.ErrNotFound`, `auth.ErrInvalidCredentials`, …) are compared with `errors.Is`.

Two minor corrections (L-05): `authorizeItemAccess` returned `500` instead of `404` for a vanished list, and `decodeJSON` silently accepted a multi-document body (`{"a":1}{"a":2}`, second ignored) — now rejected.

#### Goroutines & leaks — ✅ conformant

| Goroutine | Context | Leak possible? |
|---|---|---|
| `scrapeProductInfo` | `context.Background()` + `scrapeTimeout` (never `r.Context()`, cancelled the moment the response is written) | No — buffered channel of size 1, the send never blocks |
| `runPriceAlertScanLoop` | context cancelled at shutdown | No — deferred `ticker.Stop()` |
| `runSessionCleanupLoop` *(added)* | same | No |
| `ListenAndServe` | buffered error channel | No |

**HTTP response body closure: 5 sites out of 5** carry `defer resp.Body.Close()` (`oidc.go` ×3, `scraper.go`, `main.go`). None missed.

`Service.oidc` is protected by an `atomic.Pointer` — necessary, since the admin panel can reconfigure OIDC from a request goroutine. Confirmed race-free: the full suite passes under `-race`.

#### Input validation

**🟠 M-07 — No length bounds.** `decodeJSON`'s 1 MiB cap was the *only* limit: a request could persist a ~1 MiB `name`, echoed back in every response carrying that row thereafter. `quantity` had no ceiling either, and a value near 2³¹ overflowed the frontend's `price × quantity` totals.

A new `internal/validate/text.go` provides `Text()` (normalization plus control-character stripping, see §3.3) and `MaxLen()` (counting **runes**, not bytes, so a limit means the same thing in any script). Bounds applied: names 200, titles 500, icons 32, display name 100, email 254 (RFC 5321), `quantity` 100,000.

**Dynamic verification:** `{"name":"AAAA…5000"}` → `400 {"error":"name is too long"}`; `quantity: 2000000000` → `400 {"error":"quantity is too large"}`.

#### HTTP headers & file server

**🟡 L-01 — Directory listing.** `http.FileServer(http.Dir(...))` generates a browsable index for any directory without an `index.html` — namely `/js/`, `/css/`, `/icons/` and `/locales/`: a free inventory of the client-side surface. `staticFileSystem` (`internal/handlers/app.go`) now returns `404` for a directory lacking `index.html` and refuses any path containing a dot-prefixed component (an editor swap file, a stray `.env`). *Verified: `GET /js/` → 404, `GET /js/app.js` → 200, `GET /` → 200.*

**🟡 L-02 — Missing headers.** Added: `Strict-Transport-Security` (conditioned on `SESSION_COOKIE_SECURE` — asserting it unconditionally would pin a developer's browser to `https` for a local instance that only serves `http`), `Cross-Origin-Opener-Policy: same-origin`, `Cross-Origin-Resource-Policy: same-origin`, and a `Permissions-Policy` denying camera, microphone, geolocation, USB, payment and the rest — Trakka uses none of those capabilities, and denying them means a compromised third-party script could not reach for them either.

#### 🔵 BP-01 — Third-party script in the application's origin (supply chain)

**Description.** `static/index.html` and `templates/login.html` loaded `https://cdn.tailwindcss.com`, which the CSP explicitly allowed. That script executes **inside the application's own origin**: a CDN compromise would give an attacker arbitrary code execution in the page, able to call the API under the visitor's session. The `HttpOnly` cookie means the token itself could not be exfiltrated — but an attacker would not need it. Because the URL is unversioned, **Subresource Integrity is not applicable**: its contents change by design.

**Remediation.** The Play CDN runtime is now **vendored**: the pinned 3.4.17 bundle is served from the application's own origin at `static/js/tailwind.js`, and the CSP's `script-src` is reduced to **`'self'` with no third-party host at all**. The file carries a provenance banner recording its source URL, version and the SHA-256 of the upstream bytes, so a future update is auditable. The service worker's CDN special-casing (`CDN_ASSETS`, `precacheCdnAsset`, `handleCdnAsset` — all built to cache an opaque cross-origin response) became dead code and was removed; the runtime is now an ordinary same-origin app-shell asset.

The bundle was validated before adoption: two independent downloads are byte-identical, it parses cleanly, it contains no `XMLHttpRequest`, and it exposes the `window.tailwind` global that `static/js/tailwind-config.js` assigns to.

**Residual, by design.** `style-src` still needs `'unsafe-inline'`, because that runtime generates its utility CSS into a `<style>` tag at run time rather than serving a stylesheet, and the ~400 KB runtime cost remains. Removing both requires precompiling Tailwind with the CLI, which would introduce the build step this project deliberately does not have — a maintainer's architectural decision, not a security defect. The supply-chain exposure itself, which is what this finding was about, is fully closed.

**Dynamic verification:** the served CSP is now `script-src 'self'`; `/js/tailwind.js` returns 200 with bytes matching the repository file; neither page references `cdn.tailwindcss.com` any more.

#### Client-side data exposure

**🟠 M-06 — Data persisting after logout.** The IndexedDB mirror (houses, lists, items, categories) and the offline write queue **survived logout**. Two concrete consequences on a shared or family browser:

1. `hydrateFromCache()` paints the cache **before** any network call, so the next user saw the previous user's lists appear before a server response corrected the screen;
2. writes the previous user had queued offline were **replayed by `flushQueue()` under the new user's session**.

Remediation: `TrakkaDB.clearAll()` (`static/js/db.js`) clears all five object stores in one transaction; `purgeLocalUserData()` (`static/js/app.js`) calls it along with the API caches and the account-scoped `localStorage` preferences. It fires **(a)** on submission of the logout form (intercepted, purge, then a programmatic `submit()` — which does not re-trigger the listener) and **(b)** defensively in `init()` whenever `GET /api/v1/me` returns an id different from `trakka:cachedUserId`, which also covers an expired session replaced by somebody else's. The application shell (HTML/CSS/JS) is deliberately kept: it is not user data, and the next user keeps an instant load.

#### Secrets & logging

No hardcoded secrets (manual pattern search plus a re-review of the existing `#nosec G101` annotations: they genuinely mark a settings-table key name and a table of UI strings, not credentials). The OIDC client secret is **write-only** through the admin API (`adminSettingsView` exposes only an `oidc_client_secret_set` boolean) — verified dynamically. The logging middleware records only method, path, status and duration: no query string, no headers, no body.

#### Dependencies

`govulncheck`: **0 reachable vulnerabilities**. A single module-level advisory — `GO-2026-5932`, the unmaintained `golang.org/x/crypto/openpgp` — sits in a package Trakka **does not import** (only `bcrypt` is). Not applicable. The dependency surface is remarkably small: `modernc.org/sqlite`, `golang.org/x/crypto`, `golang.org/x/net`, the latter two maintained by the Go team itself.

---

## 4. Remediation summary

### Files created

| File | Purpose |
|---|---|
| `internal/handlers/csrf.go` | Double-submit token for the auth forms + `requireSameOriginWrite` middleware (M-01, M-02) |
| `internal/handlers/ratelimit.go` | Fixed-window limiter, per IP and per email (H-02) |
| `internal/validate/text.go` | `Text()` (normalization, control-character and bidi-override stripping) and `MaxLen()` (M-07) |
| `internal/db/migrations/0014_pending_invitations.sql` | `pending_invitations` table (L-06) |
| `internal/db/pending_invitations.go` | Invitation CRUD + `MaterializePendingInvitations` (L-06) |
| `internal/db/pending_invitations_test.go` | 4 tests: materialization, case-insensitive upsert, deleted target, withdrawal (L-06) |
| `internal/handlers/security_test.go` | Regression tests: H-01 (3), M-02 (10 cases), H-02, L-05, L-06 (2) |
| `static/js/tailwind.js` | Vendored, pinned Tailwind Play CDN runtime 3.4.17 (BP-01) |

### Files modified

| File | Fix |
|---|---|
| `internal/handlers/lists.go` | **H-01**: changing a Space requires House membership; length bounds |
| `internal/handlers/auth.go` | **H-02**, **M-01**, **M-03**, **L-04**, **L-06**: rate limiting, CSRF check, OIDC registration gate, dedicated error messages, invitation materialization |
| `internal/handlers/app.go` | **M-02**, **L-01**, **L-06**: same-origin middleware, `staticFileSystem`, lazy limiters, invitation-revoke routes |
| `internal/handlers/middleware.go` | **L-02**, **BP-01**: conditional HSTS, COOP, CORP, `Permissions-Policy`, `script-src 'self'` |
| `internal/handlers/house_members.go`, `shares.go` | **L-06**: uniform replies, pending rosters, invitation revocation |
| `internal/handlers/scrape.go` | **M-08**: 8-slot concurrency semaphore |
| `internal/handlers/rbac.go`, `json.go` | **L-05**: 404 instead of 500; multi-document JSON rejected |
| `internal/handlers/items.go`, `houses.go`, `categories.go`, `admin.go` | **M-07**: length bounds and text normalization |
| `internal/auth/oidc.go` | **M-05**: `https` enforced (with escape hatch), RSA 2048 floor, bounded bodies, discovery endpoints checked |
| `internal/auth/oidc_login.go`, `errors.go` | **M-03**: provisioning gated on `registration_open`; IdP claim bounds |
| `internal/auth/password.go`, `service.go` | **L-04**, **M-07**: explicit bcrypt limit, email/name bounds |
| `internal/scraper/scraper.go` | **M-04**: `blockedRanges`, IPv4-in-IPv6 unmapping |
| `internal/db/sessions.go`, `cmd/server/main.go` | **L-03**: `DeleteExpiredSessions` + hourly sweep |
| `internal/models/models.go` | **L-06**: `PendingInvitation`, `Pending` roster markers |
| `internal/scraper/scraper_test.go` | 23-address blocklist test (M-04) |
| `templates/login.html` | **M-01**, **BP-01**: hidden `csrf_token` field; vendored Tailwind |
| `static/index.html` | **BP-01**: vendored Tailwind |
| `static/js/db.js`, `static/js/app.js` | **M-06**, **L-06**: `clearAll()`, `purgeLocalUserData()`, account-change detection, pending roster rendering |
| `static/js/shares.js` | **L-06**: pending share rendering and invitation revocation |
| `static/locales/{fr,en}.json` | **L-06**: `pendingBadge` / `ownerBadge` keys |
| `static/sw.js` | Caches `v35` → `v38`; CDN special-casing removed; invitation endpoints require connectivity |

### Validation

```
gofmt -l .                        (no output)
go vet ./...                      (no output)
CGO_ENABLED=1 go test -race ./... ok — db, handlers, scraper, settings, validate
golangci-lint run ./...           0 issues
gosec ./...                       Issues: 0
govulncheck ./...                 0 reachable vulnerabilities
node --check                      app.js, db.js, shares.js, sw.js, tailwind.js
```

**Dynamic validation against a genuinely running instance** (not static analysis alone): CSRF-protected registration and login, cross-origin rejection, the attempt budget exhausting on the eighth try, refusal of a cleartext-HTTP OIDC issuer, registration closure actually closing, length bounds, control-character stripping, directory listing suppressed, all seven security headers present, six internal URLs refused by the SSRF guard, the vendored Tailwind served with `script-src 'self'`, and the full invitation lifecycle (uniform replies, pending roster, materialization on sign-in, withdrawal, permission carried through to a materialized share).

---

## 5. Residual recommendations

None of the following is a vulnerability; all are hardening or hygiene items for the maintainer to weigh.

1. **TLS is mandatory in production.** `compose.yml` serves plain HTTP by design. `SESSION_COOKIE_SECURE` should stay `true`, which **requires** a TLS reverse proxy (Caddy/Traefik/nginx) for the session cookie to be sent at all. It now also gates the HSTS header.
2. **Precompile Tailwind** to drop `style-src 'unsafe-inline'` and the ~400 KB runtime — see BP-01. This is the one remaining CSP relaxation, and closing it means accepting a build step.
3. **30-day session lifetime** (`SESSION_TTL_HOURS=720`), absolute, with no rotation and no idle expiry. Fine for a personal PWA; consider 7 days with sliding renewal for a multi-user instance.
4. **Password policy** is a bare 8-character minimum. Consider checking against a compromised-password list, or requiring more length. The rate limiting added in H-02 is the most effective countermeasure at this stage.
5. **Argon2id** instead of bcrypt, worth considering during a future schema change (requires progressive re-hashing at login).
6. **No audit log** of sensitive actions (admin settings changes, share grants and revocations). Useful once an instance serves more than one household.
7. **No backup rotation** in `/data/backups/` (`internal/db/migrate.go`): those snapshots contain the whole database, password hashes included. They are created `0o750`, but nothing prunes them.
8. **Pending invitations are never expired.** An invitation to an address that never registers stays in `pending_invitations` indefinitely. Harmless (it grants nothing), but a periodic sweep of very old rows would be tidy, alongside the session sweep added in L-03.

---

## 6. Detailed scoring

| Axis | Before | After | Comment |
|---|---|---|---|
| SQL injection | 10 / 10 | 10 / 10 | Faultless — systematic parameterization, layer boundary respected |
| XSS / HTML injection | 9.5 / 10 | 10 / 10 | Already excellent; control characters now neutralized too |
| Authentication & sessions | 6 / 10 | 9 / 10 | Cryptography was correct from the start; rate limiting and OIDC hardening were missing |
| CSRF | 5 / 10 | 9 / 10 | Rested on a single layer that did not cover login |
| Authorization / IDOR | 6 / 10 | 9.5 / 10 | Rigorous model, defeated by one indirect route (H-01) |
| SSRF | 8 / 10 | 10 / 10 | Good design (anti-rebinding); blocklist was incomplete |
| Go practices | 9 / 10 | 9.5 / 10 | No goroutine or response-body leaks |
| Input validation | 6 / 10 | 9 / 10 | Solid semantic validation, no size bounds |
| Client-side confidentiality | 5 / 10 | 9 / 10 | The cache outlived logout |
| Supply chain | 6 / 10 | 9.5 / 10 | Minimal Go dependency surface; the third-party CDN script is now vendored |
| Deployment hardening | 9 / 10 | 9 / 10 | distroless, non-root, read-only, `cap_drop: ALL` — exemplary |
| **Overall** | **6.8 / 10 (B)** | **9.4 / 10 (A)** | |

The gap between A and a perfect score is now essentially items 2, 6 and 7 of §5 — a build-step decision, an audit log, and backup retention — none of which is an exploitable weakness.

---

## 7. Post-audit addendum

### 2026-09-01 — M-02 implementation bug: `Origin: null` on a legitimate navigation

**What happened.** M-02's remediation (`requireSameOriginWrite`, §3.4) was implemented slightly stricter than the design this document describes: it rejected *any* non-empty `Origin` header that didn't match the request's host — including the literal opaque value `"null"` — rather than falling through to the `Sec-Fetch-Site` check the design above already calls for ("*otherwise, Sec-Fetch-Site: cross-site is rejected*"). In practice this broke `POST /auth/login` and `POST /auth/register` in every standards-compliant browser: per the [Fetch spec](https://fetch.spec.whatwg.org/#origin-header), a **navigational** request (an HTML `<form method="post">`, as opposed to a `fetch()` call) whose page has `Referrer-Policy: no-referrer` in effect — which `SecurityHeaders` sets on every response — serializes `Origin` as `"null"` even when the request is genuinely same-origin. `/auth/login`/`/auth/register` are the only two navigational, non-`fetch()` POSTs in the whole app, which is why nothing else broke.

**Why this is a correctness fix, not a security downgrade.** `Origin: null` is exactly the value an attacker-controlled sandboxed iframe would also send when forging a cross-origin POST — that's the scenario the strict check was written to catch, and the fix does not weaken that: an opaque origin can never be "same-site" with anything, so `Sec-Fetch-Site` still reads `cross-site` for a genuine attack and the request is still rejected. The bug only ever rejected *too much* (a false positive breaking legitimate logins), never *too little* — it was a functional defect, not an exploitable gap, and the CSRF score/finding above stands unchanged.

**Fix.** `internal/handlers/csrf.go`'s `requireSameOriginWrite` now treats an `Origin` of `""` (absent) and `"null"` (opaque) identically: both fall back to `Sec-Fetch-Site`. Three test cases were added to `TestRequireSameOriginWrite` (`internal/handlers/security_test.go`) beyond the original 10: the fixed false positive (`Origin: null` + `Sec-Fetch-Site: same-origin` → allowed), confirmation the attack path stays blocked (`Origin: null` + `Sec-Fetch-Site: cross-site` → `403`), and the one narrow residual gap this leaves — a browser old enough to predate Fetch Metadata (`Origin: null`, no `Sec-Fetch-Site` at all) — accepted deliberately as the same "no usable signal → allow" trade-off already made for a non-browser client, and made harmless in practice by the layered defenses M-01's double-submit `csrf_token` (auth forms) and `SameSite=Lax` (everything else) already provide independently of this check.

**Found by:** live user report of `POST /auth/login` failing in Brave, Edge, and a fresh Firefox install, root-caused from the actual browser request headers (`sec-fetch-site: same-origin` alongside `origin: null`) and confirmed via `curl -H "Origin: null"` reproducing the exact `403` before the fix was written.
