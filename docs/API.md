# API Reference

Base URL: `http://<host>:<port>` (default port `8080`). All endpoints are under `/api/v1`.

All responses are `application/json; charset=utf-8`, except the [calendar feed](#calendar-feed) (`text/calendar`). Every `/api/v1/...` endpoint requires an authenticated session (see [Authentication](#authentication) below) — there is no anonymous access to the JSON API. A few list and item endpoints also accept a personal [API token](#api-tokens) (`Authorization: Bearer trk_…`) instead of the session cookie, for integrations such as Home Assistant. The exceptions are `GET /api/v1/calendar/feed.ics`, which calendar apps poll without a session and which is authenticated by a secret token in its URL instead (see [Calendar feed](#calendar-feed)), and the three `/api/v1/public/lists/{token}...` routes of a list's [public share link](#public-share-links), opened by visitors with no account and authenticated by the secret token in their path. `/auth/...` endpoints and static assets stay unauthenticated, since `/auth/...` is how a session gets established in the first place.

## Conventions

- **Errors** are always `{"error": "<message>"}` with a non-2xx status code. A few endpoints (the [admin backup](#admin-backups) routes) add a machine-readable `"code"` next to it.
- **Timestamps** (`created_at`, `updated_at`) are UTC, ISO-8601 with milliseconds, e.g. `2026-08-26T07:46:03.959Z`.
- **Request bodies** are JSON. Unknown fields in a request body are rejected (400). Body size is capped at 1 MiB.
- IDs are positive integers (SQLite `AUTOINCREMENT`).
- **URLs** (the item `url` field) must be an absolute `http://` or `https://` URL with a non-empty host, or omitted/empty. Anything else — `javascript:`, `data:`, a bare host with no scheme, a malformed string — is rejected with `400 {"error": "url must be an absolute http:// or https:// URL"}`.
- **Security headers**: every response (API and static) carries `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, a strict `Content-Security-Policy`, and `Referrer-Policy: no-referrer`. Every `/api/v1/...` JSON response additionally carries `Cache-Control: no-store` (`internal/handlers/json.go`'s `writeJSON`, the one helper every JSON response funnels through) — this stops the browser's own HTTP disk cache (a layer entirely separate from the service worker's Cache Storage API, which is never used for `/api/v1/...` at all — see the "Offline responses" entry below and `static/sw.js`'s own comments) from ever serving a stale `GET` response, closing off the one remaining vector for a list/item read to resolve to something other than a genuine network round trip or (offline) the IndexedDB-backed fallback.
- **Offline responses**: these are never sent by the Go backend itself — the service worker (`static/sw.js`) synthesizes them client-side when the network is unreachable. A `GET` served from the offline cache carries `X-Trakka-Offline: true`; a mutation accepted into the offline sync queue returns `202` with `X-Trakka-Queued: true` and an optimistic body (list/item ids look like `temp-list-<uuid>`/`temp-item-<uuid>` until they sync). See [docs/PWA.md](PWA.md).

## Authentication

Trakka supports two ways to establish a session, both ending in the same HTTP-only, `SameSite=Lax` `trakka_session` cookie (see the "Mobile/PWA login loop fix" entry in [CLAUDE.md](../CLAUDE.md) for why this is `Lax` rather than `Strict`):

- **Local**: email + password (bcrypt-hashed server-side).
- **OIDC / OAuth2**: a generic Authorization Code + PKCE flow against any standards-compliant provider (Authelia, Authentik, Keycloak, Google, ...), configured via `OIDC_ISSUER`/`OIDC_CLIENT_ID`/`OIDC_CLIENT_SECRET`/`BASE_URL` (see [docs/DEPLOYMENT.md](DEPLOYMENT.md)). Only enabled when all three `OIDC_*` variables are set.

These are classic server-rendered, form-POST endpoints (not JSON) — `/auth/login` doubles as the login *page* (`GET`) and the login *submit* target (`POST`), and successes/failures are full-page redirects, not JSON responses:

| Endpoint | Method | Notes |
|---|---|---|
| `/auth/login` | `GET` | Renders the login/register page, including a hidden `csrf_token` field (see CSRF below) paired with a `trakka_csrf` cookie set on this same response. Redirects to `/` if already authenticated. |
| `/auth/login` | `POST` | Form fields: `email`, `password`, `csrf_token`. On success, sets the session cookie and redirects to `/`; on failure, redirects to `/auth/login?error=invalid_credentials` (or `?error=csrf_failed` if `csrf_token` is missing or doesn't match the `trakka_csrf` cookie). |
| `/auth/register` | `POST` | Form fields: `email`, `password`, `password_confirm`, `display_name`, `csrf_token`. Creates the account, a personal house ("Ma Maison") owned by the new user, a session, then redirects to `/`. `email_taken` on a duplicate email; `registration_closed` (both here and on `GET /auth/login?mode=register`) if an admin has closed registration via [Admin settings](#admin-settings); `csrf_failed` under the same condition as `/auth/login` above. |
| `/auth/logout` | `POST` | Revokes the session and redirects to `/auth/login`. |
| `/auth/methods` | `GET` | JSON: the sign-in methods the login page offers — `{"password": bool, "oidc": bool, "oidc_provider_name": string, "registration": bool}` (`password` and `registration` are `false` in OIDC-exclusive mode; `oidc` is `true` whenever OIDC is enabled and completely configured, even while its provider is unreachable and discovery is being retried). Read by the Android app's native sign-in screen. Public, like the login page, which shows the same; `Cache-Control: no-store`. |
| `/auth/oidc/login` | `GET` | Redirects to the configured OIDC provider. `404` if OIDC isn't configured. If OIDC is configured but discovery hasn't succeeded yet, retries it first (at most once every 5 s), and redirects to `/auth/login?error=oidc_unavailable` if the provider still doesn't answer. Optional `app_challenge`: see [Sign-in from the Android app](#sso-sign-in-from-the-android-app) (`400` if malformed). |
| `/auth/oidc/callback` | `GET` | The provider's redirect target. Verifies the id_token, provisions the account on first login (with a personal house, same as local registration), sets the session cookie, redirects to `/`. For a flow started with `app_challenge`, sets no cookie and redirects to the Android app instead (below). |
| `/auth/oidc/app-session` | `POST` | Form fields: `code`, `verifier`. Redeems the Android app's handoff code (below): sets the session cookie and redirects to `/`, or redirects to `/auth/login?error=oidc_failed`. `404` if OIDC isn't configured. |

Integrations that can't hold a browser session (Home Assistant, scripts) authenticate with a personal **API token** instead: `Authorization: Bearer trk_…` on each request, accepted by a fixed set of list and item routes only — see [API tokens](#api-tokens).

A first-time OIDC login is rejected (`?error=email_taken`) if the claimed email already belongs to a different account (local, or a different OIDC issuer) — accounts are never silently auto-linked by email, since an OIDC provider's email claim isn't guaranteed to be verified.

Both `/auth/login` and `/auth/register` require a `csrf_token` form field matching the `trakka_csrf` cookie set by a prior `GET /auth/login` — see the CSRF paragraph below. There is no way to submit either form correctly without first loading the page, so a scripted login/registration (curl, a test harness) must fetch the page first and extract the token:

```bash
# 1. load the login page to get a trakka_csrf cookie and the matching token
curl -c cookies.txt -s http://localhost:8080/auth/login -o login.html
CSRF_TOKEN=$(grep -o 'name="csrf_token" value="[^"]*"' login.html | head -1 | sed -E 's/.*value="([^"]*)"/\1/')

# 2. submit the login form, cookie jar and token both included
curl -b cookies.txt -c cookies.txt -X POST http://localhost:8080/auth/login \
  -d "email=alice@example.com&password=secret1234&csrf_token=${CSRF_TOKEN}"
curl -b cookies.txt http://localhost:8080/api/v1/me
```

### SSO sign-in from the Android app

The Android app ([MOBILE_BUILD.md](MOBILE_BUILD.md#sso-oidc-passkeys-and-security-keys)) runs the OIDC flow in the phone's browser, where the provider can use WebAuthn (passkeys, security keys), which the app's WebView cannot. The provider's redirect URI stays `/auth/oidc/callback`: nothing changes on the provider's side.

1. The app picks a random `verifier` and opens `GET /auth/oidc/login?app_challenge=<challenge>` in a Custom Tab, where `challenge` is the unpadded base64url SHA-256 of the verifier (PKCE's S256, RFC 7636).
2. On success, `/auth/oidc/callback` answers `302` to `io.github.trakka-project.app://sso?code=<code>` (`Cache-Control: no-store`), with a single-use code valid for 2 minutes and kept in the server's memory; on failure, to `io.github.trakka-project.app://sso?error=<code>`, with the same error codes as `/auth/login?error=`.
3. The app's WebView posts `code` and `verifier` to `/auth/oidc/app-session`. The code is consumed by the first attempt, right or wrong.

The app starts this flow from its own native sign-in screen, shown instead of `GET /auth/login` (it asks `GET /auth/methods` which buttons to show), or from the server's login page (`static/js/login.js`).

Another app registering the same URL scheme can receive the code but cannot redeem it without the verifier. `/auth/oidc/app-session` goes through the same cross-site write check (`Origin`/`Sec-Fetch-Site`) as every other `POST`, so a site cannot make a visitor's browser redeem a code and verifier it obtained for its own account.

### `GET /api/v1/me`

Returns the authenticated user. `401 {"error": "authentication required"}` if the session cookie is missing, invalid, or expired (`401 {"error": "invalid API token"}` for an unknown or revoked [API token](#api-tokens)).

```json
{ "id": 1, "email": "alice@example.com", "display_name": "Alice", "is_admin": false, "created_at": "...", "keep_last_page": true, "language": "en", "reminder_default_offset_days": 0, "reminder_default_time": "09:00", "reminder_default_at_due_time": false, "vibrate_on_notification": true, "reminders_enabled": true, "overdue_tasks_summary_enabled": false, "overdue_tasks_summary_time": "08:00", "collaborator_actions_enabled": true, "item_additions_enabled": true, "list_sharing_enabled": true }
```

`is_admin` grants access to the [Admin](#admin-settings) endpoints below. The very first account ever created on an instance (local or OIDC-provisioned) becomes an admin automatically — see `internal/db.CreateUser` in [CLAUDE.md](../CLAUDE.md) — and any existing admin can grant or revoke it for any other account via [`PATCH /api/v1/admin/users/{id}`](#patch-apiv1adminusersid).

`keep_last_page` (`bool`, defaults to `true`) controls whether the frontend reopens on the last dashboard tab or list the user had open instead of always landing on the dashboard — see the "keep last page on launch" feature in [CLAUDE.md](../CLAUDE.md). The actual last-visited view itself is tracked purely client-side (`localStorage`, per browser, never sent to the server); only this on/off preference is part of the user's profile.

`language` (`string`, `"fr"` or `"en"`) is the account's own UI-language preference, set from the "Langue" section of the "Paramètres" modal (`static/js/i18n.js`/`settings.js`). Never empty in this response: an account that has never set its own preference resolves to the instance's `DEFAULT_APP_LANGUAGE` env var (default `"en"`, see [CLAUDE.md](../CLAUDE.md)) rather than the field being omitted or blank.

`reminder_default_offset_days`/`reminder_default_time` (`integer` ≥ 0, `string` `HH:MM` 24h) are this account's default for "when should I be reminded before a task's due date" — defaulting to `0`/`"09:00"` ("le jour même à 09h00") for a brand new account. The settings UI ("Notifications de tâches") names the offset presets by offset alone, with the time freely editable: `0` is "Le jour même", `1` is "La veille", anything else is "Personnalisé". `reminder_default_at_due_time` (`bool`, default `false`) is the fourth preset, "À l'heure exacte de l'échéance": a task left on the default then reminds at its own `due_time`, and the offset/time pair becomes the fallback for a task that has no due time. See [Reminders](#reminders) under Items for how a task's own reminder either overrides these or resolves from them.

`vibrate_on_notification` (`bool`, defaults to `true`) controls whether this account's [Web Push](#push-notifications) notifications vibrate the device. The server applies it per recipient when it sends a push: the payload carries `"vibrate": [200, 100, 200]` when it is on and no `vibrate` key when it is off, and the service worker shows a payload without one as a silent notification.

The next six fields are Paramètres' **"Types de notifications"** matrix — which notifications this account receives, on every device (one `users` column each, see [DATABASE.md](DATABASE.md#users)). Turning one off only affects this account: everyone else with access to the list still gets theirs.

| Field | Default | Paramètres switch | What it gates |
|---|---|---|---|
| `reminders_enabled` | `true` | Rappels des tâches à échéance | the per-task [reminders](#reminders): the reminder push leaves the account out (the reminder still counts as sent, so turning it back on doesn't deliver every reminder missed meanwhile), and [`GET /api/v1/reminders/upcoming`](#get-apiv1remindersupcoming) returns none. The tasks' own reminder settings and the [calendar feed](#calendar-feed)'s alarms are untouched. |
| `overdue_tasks_summary_enabled` | `false` | Rappel des tâches en retard / non effectuées | the daily overdue-tasks summary, below |
| `collaborator_actions_enabled` | `true` | Actions des collaborateurs | someone else checking off or unchecking an item on a list the account can access |
| `item_additions_enabled` | `true` | Ajout d'items | someone else adding an item to such a list |
| `list_sharing_enabled` | `true` | Partage de listes | a List or Space shared with the account, or an invitation to a House — sent to an existing account when the invitation is made (it takes effect the next time they open the app); an address without an account gets nothing, and the inviting request's reply never depends on it |

The **overdue-tasks summary**: once a day at `overdue_tasks_summary_time` (`string` `HH:MM` 24h, default `"08:00"`, in `APP_TIMEZONE`), one notification listing **every task past its due date** (`due_date` before today) that is still not checked off, oldest first, on every list the account can access — whatever each task's own reminder setting. A task keeps being summarized every day until it is checked off or rescheduled; nothing is sent on a day with nothing overdue. One task: "« Appeler le plombier » (Maison), échue le ven. 2 oct., n'est toujours pas cochée."; several: "3 tâches en retard ne sont pas cochées : A, B, C". By Web Push from the due-reminder scan (so only with push configured, and at most `NOTIF_DUE_SCAN_INTERVAL_MINUTES` late), and in the Android app from `GET /api/v1/reminders/upcoming`'s `overdue_summary`. Turned on after that day's time, the day's summary goes out at the next scan.

### `PATCH /api/v1/me`

Partial update of the caller's own profile preferences — `keep_last_page`, `language`, `vibrate_on_notification`, the notification types above (`reminders_enabled`, `overdue_tasks_summary_enabled`, `overdue_tasks_summary_time`, `collaborator_actions_enabled`, `item_additions_enabled`, `list_sharing_enabled`), the [price alert](#price-tracking) switches (`price_alerts_enabled` — the master switch, `price_drop_alerts_enabled`, `price_increase_alerts_enabled`, `price_change_indicators_enabled`; plain booleans), and/or `reminder_default_offset_days`/`reminder_default_time` — following the same "absent = untouched" convention as `PATCH /api/v1/items/{id}`; any subset may be present in one request. Returns the updated user (same shape as `GET /api/v1/me`). `language` must be `"fr"` or `"en"`, or the request is rejected with `400`. `reminder_default_offset_days`/`reminder_default_time` must be given together (sending only one is rejected with `400`, since the pair is meaningless half-updated); `reminder_default_offset_days` must be ≥ 0 and `reminder_default_time` must be a real `HH:MM` time, else `400`. `reminder_default_at_due_time` may only be sent with that pair (`400` otherwise), and the pair sent without it saves `false` — so a client unaware of the at-due-time preset still saves a plain offset default. `overdue_tasks_summary_time` may be sent on its own, and must be a real, non-empty `HH:MM` time, else `400`.

```bash
curl -b cookies.txt -X PATCH http://localhost:8080/api/v1/me \
  -H 'Content-Type: application/json' -d '{"keep_last_page": false, "language": "fr", "reminder_default_offset_days": 1, "reminder_default_time": "20:00"}'

# "À l'heure exacte de l'échéance", falling back to 08:30 the same day for a task without a time
curl -b cookies.txt -X PATCH http://localhost:8080/api/v1/me \
  -H 'Content-Type: application/json' -d '{"reminder_default_offset_days": 0, "reminder_default_time": "08:30", "reminder_default_at_due_time": true}'

# Price alerts in both directions, no green/red price indicators in lists
curl -b cookies.txt -X PATCH http://localhost:8080/api/v1/me \
  -H 'Content-Type: application/json' -d '{"price_increase_alerts_enabled": true, "price_change_indicators_enabled": false}'

# No per-task reminders and no "item added" pushes; a summary of overdue tasks every morning at 07:30
curl -b cookies.txt -X PATCH http://localhost:8080/api/v1/me \
  -H 'Content-Type: application/json' -d '{"reminders_enabled": false, "item_additions_enabled": false, "overdue_tasks_summary_enabled": true, "overdue_tasks_summary_time": "07:30"}'
```

**CSRF**: `/auth/login`/`/auth/register` each require the `csrf_token` form field described above — a double-submit token, minted and set as an HttpOnly `trakka_csrf` cookie by `GET /auth/login`, that the submitted form field must match (`internal/handlers/csrf.go`). This defends specifically against "login CSRF" (a cross-site POST silently signing a victim into an attacker-controlled account) — a threat `SameSite` alone can't prevent here, since neither of these requests carries a pre-existing session cookie for `SameSite` to withhold. Every subsequent state-changing call instead goes through `/api/v1/...`, protected by the session cookie's `SameSite=Lax` attribute plus an `Origin`/`Sec-Fetch-Site` check (`requireSameOriginWrite`, same file): a cross-site request never carries the session cookie, and the same middleware also rejects a cross-site `/auth/logout` POST, which carries no session cookie either but would still be a nuisance if forgeable.

### `GET /api/v1/config`

Instance-wide settings any signed-in user's frontend needs (as opposed to the admin-only [`GET /api/v1/admin/settings`](#admin-settings)). Read once by `static/js/user-search.js` to adapt the [sharing autocomplete](#get-apiv1userssearch), and by the Home Assistant integration at setup.

```json
{ "saas_mode": false, "timezone": "Europe/Paris" }
```

`timezone` is the IANA name of `APP_TIMEZONE` (`UTC` when unset or unrecognized), the zone an item's `due_time` and reminder times are wall-clock times in.

`saas_mode` (`bool`) mirrors the `SAAS_MODE` env var (default `false`, see [docs/DEPLOYMENT.md](DEPLOYMENT.md#configuration-environment-variables)): `false` for a self-hosted household/group, `true` for a multi-tenant deployment where accounts don't know each other. Env-only — not editable from the admin console.

## Health

### `GET /healthz`

Pings the database. Used by the container `HEALTHCHECK`.

- `200` `{"status": "ok"}`
- `503` `{"error": "database unavailable"}`

## Houses

A House groups related lists together, shared by the members of a household. Every list belongs to exactly one house, and every house has one or more members via `house_members` (`owner` or `member` role) — a house with zero members is unreachable via the API to everyone (this can't normally happen: houses are only ever created together with their owner, see below).

**Access control**: a user can only see and modify houses (and their lists/items) they are a member of. `GET`/access-checked endpoints return `403 {"error": "not a member of this house"}` for a house the caller isn't in; owner-only actions (rename, delete, invite, remove another member) return `403 {"error": "only the house owner can do this"}` for a non-owner member. Every house object in a response carries a `role` field — the caller's own role in that house — so the frontend can conditionally show owner-only controls.

Every new user (on registration or first OIDC login) gets a personal house ("Ma Maison") created automatically with them as owner, so there's always at least one house to land on.

### `GET /api/v1/houses`

Returns the houses the caller is a member of, oldest first.

```bash
curl -b cookies.txt http://localhost:8080/api/v1/houses
```

```json
[
  { "id": 2, "name": "Ma Maison", "created_at": "...", "role": "owner" }
]
```

### `POST /api/v1/houses`

Creates a house with the caller as its owner (atomically — a house is never left without a member).

```bash
curl -b cookies.txt -X POST http://localhost:8080/api/v1/houses -d '{"name": "Maison des Parents"}'
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | trimmed; `400` if empty |

`201` with the created house (`role: "owner"`).

### `GET /api/v1/houses/{id}`

`200` with the house (including the caller's `role`), `403` if the caller isn't a member, `404` if not found. Lists belonging to it are fetched separately via `GET /api/v1/lists?house_id={id}`, not embedded here.

### `PUT /api/v1/houses/{id}`

Renames a house. Owner-only. `403` if the caller isn't the owner, `404` if not found, `400` if `name` is empty.

### `DELETE /api/v1/houses/{id}`

Owner-only. `204` on success. Deleting a house **cascades** to delete all its lists (and therefore all their items) and all its `house_members` rows (`ON DELETE CASCADE`, transitively). `403` if the caller isn't the owner, `404` if not found.

## House members

Membership is how a house is shared between users. There's no email-sending infrastructure in this project, so inviting someone only works once they already have a Trakka account (local or OIDC) — invite-by-email fails clearly (`404`) rather than creating an unredeemable pending invite.

### `GET /api/v1/houses/{id}/members`

Any member can view the roster. Owners are listed first.

```bash
curl -b cookies.txt http://localhost:8080/api/v1/houses/2/members
```

```json
[
  { "house_id": 2, "user_id": 1, "role": "owner", "created_at": "...", "email": "alice@example.com", "display_name": "Alice" },
  { "house_id": 2, "user_id": 2, "role": "member", "created_at": "...", "email": "bob@example.com", "display_name": "Bob" }
]
```

`403` if the caller isn't a member of the house.

### `POST /api/v1/houses/{id}/members`

Owner-only. Invites an existing user by email.

```bash
curl -b cookies.txt -X POST http://localhost:8080/api/v1/houses/2/members -d '{"email": "bob@example.com"}'
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `email` | string | yes | must belong to an existing account, else `404` |

`201` with the created membership (`role: "member"`). `403` if the caller isn't the owner. `404` if no account exists for that email. `409` if already a member.

### `DELETE /api/v1/houses/{id}/members/{userId}`

Removes a member. A user may always remove **themselves** ("leave house") regardless of role; removing anyone else requires being the owner.

`204` on success. `403` if the caller lacks permission. `404` if the target user isn't a member. The person removed is then unassigned from the House's items — except, self-hosted, those of a list they can still open through a share (see [Task assignments](#task-assignments)).

## House product catalog

What a household buys, learned from its "Courses" (`groceries`) lists: one entry per distinct product name ever added to one of them (see [Quick shopping lists](#quick-shopping-lists-courses)), with its usual aisle, whether it's a favorite, how often it was added and bought, and the barcodes scanned for it. Entries outlive the items they came from. Every endpoint below requires House membership (`403` otherwise) — **not** list access: the catalog spans every list of the House, so someone who only has one list [shared](#sharing) with them never reads it (they can still add to that list, which feeds it). A product is:

```json
{"id": 7, "house_id": 1, "name": "Lait demi-écrémé", "aisle": "dairy", "is_favorite": true,
 "add_count": 12, "purchase_count": 9, "first_purchased_at": "2026-08-02T08:00:00.000Z",
 "last_purchased_at": "2026-10-12T17:40:03.112Z", "created_at": "…", "updated_at": "…"}
```

`aisle`, `first_purchased_at` and `last_purchased_at` are omitted when unknown. Migration 31 seeded each House's catalog from the items its Courses lists already held (each checked-off item counting as one purchase on its last update).

### `GET /api/v1/houses/{id}/products?q={text}&limit={n}`

Autocomplete: up to `limit` (default 8, at most 20) products whose name contains `q`, compared like product names are (case, accents and punctuation ignored, LIKE wildcards literal). Names starting with `q` come first, then names with a word starting with it, then the rest; within each group favorites, then the most added/bought, then alphabetical. An empty `q` returns `[]`; `400` for an invalid `limit` or a `q` over 500 characters. With `?favorites=true` instead, every favorite, alphabetically.

```bash
curl -b cookies.txt "http://localhost:8080/api/v1/houses/1/products?q=lai"
```

### `GET /api/v1/houses/{id}/products/suggestions?list_id={listId}`

The quick-add panel: `{"favorites": [product…], "recurring": [suggestion…]}`. `recurring` holds up to 12 products bought (checked off) at least twice, each a product plus `reason`, `avg_interval_days` (average days between two purchases, at least 1) and `days_since_last_purchase`. Those that are due — at least 80 % of their usual interval since the last purchase (`internal/catalog.DueRatio`) — come first with `reason: "due"`, most overdue first; the others follow with `reason: "frequent"`, most bought first. With `list_id` (a list of this House, else `400`), products already active on that list and favorites are left out of `recurring`.

### `POST /api/v1/houses/{id}/products`

Creates or updates a product **by name** — how an item is starred from the list view, where only its title is at hand: `{"name": "Pain de mie", "is_favorite": true}`, optionally `"aisle"` (`""` clears it). The name is matched like everywhere else; an existing product keeps its own spelling. `200` with the product; `400` without a name, or for an unknown aisle.

### `PATCH /api/v1/houses/{id}/products/{productId}`

`{"is_favorite"?: bool, "aisle"?: string}` — absent fields are left as they are, `"aisle": ""` clears it. `200` with the product, `404` if this House has no such product.

### `DELETE /api/v1/houses/{id}/products/{productId}`

Forgets the product (a typo it keeps suggesting, something no longer bought), with its barcodes; items on lists are untouched. It comes back the next time it's added. `204`, or `404`.

### `GET /api/v1/houses/{id}/products/barcode/{code}`

What a scanned barcode is. `code`: 8 to 14 digits (normalized like the item's [`barcode`](#post-apiv1items)), else `400`.

- Filed in the House catalog (an item was added with that `barcode` before): `200` `{"barcode", "name", "aisle", "source": "house", "product": {…}}` — the PWA adds it straight away.
- Otherwise, with `PRODUCT_LOOKUP_ENABLED` (default on, [docs/DEPLOYMENT.md](DEPLOYMENT.md)), [Open Food Facts](https://world.openfoodfacts.org) is asked (`internal/scraper.LookupBarcode`: the barcode digits only, through the same SSRF-guarded client as the price lookups, 6 s at most): `200` `{"barcode", "name", "aisle", "source": "openfoodfacts"}`, the name in the caller's language when known, the aisle from the product's categories (else guessed from its name). Nothing is stored: the PWA puts the name in the quick-add field to be checked, and the item added with the `barcode` files it.
- Unknown: `404` `{"error": "unknown barcode", "code": "not_found"}`; `"code": "lookup_failed"` when Open Food Facts couldn't be reached (logged at warn). Never a `502`/`503`/`504`, which the service worker would take for "Trakka unreachable".

Open Food Facts lookups are limited to 30 per minute per account (`429` with `Retry-After`); codes the House already knows don't count.

## Custom categories

A custom category ("Space") is a personal, freeform way to organize lists beyond the fixed `type` enum — e.g. "Vacances", "Anniversaire de Léo". Unlike a house, a category belongs to exactly one user (whoever created it), not to every house member; any member of a list's house can see a category attached to it, but only its owner can rename, restyle, reorder, or delete it. See [docs/DATABASE.md](DATABASE.md#custom_categories).

### `GET /api/v1/custom-categories`

Returns the caller's own categories, ordered by `position` then `id`.

```bash
curl -b cookies.txt http://localhost:8080/api/v1/custom-categories
```

```json
[
  { "id": 1, "user_id": 1, "name": "Vacances", "icon": "🏖️", "color": "#3366ff", "position": 0, "created_at": "..." }
]
```

`?shared_with_me=true` switches to a different, mutually exclusive mode — mirroring `GET /api/v1/lists`'s own — returning every Space the caller doesn't own but can still see instead of their own categories: either because the owner shared it directly (`access_source: "space_share"`) or because the caller is simply a member of a House that uses it on at least one of its lists, with no share involved at all (`access_source: "house_member"` — see [Pinning a House-visible Space](#patch-apiv1custom-categoriesidsharepin)):

```bash
curl -b cookies.txt http://localhost:8080/api/v1/custom-categories?shared_with_me=true
```

```json
[
  { "id": 2, "user_id": 7, "name": "Homelab", "icon": "🖥️", "color": "", "position": 0, "created_at": "...",
    "access_source": "space_share", "access_permission": "write", "is_pinned_to_dashboard": false },
  { "id": 3, "user_id": 9, "name": "Bricolage", "icon": "🔨", "color": "", "position": 0, "created_at": "...",
    "access_source": "house_member", "access_permission": "write", "is_pinned_to_dashboard": false }
]
```

`access_source`/`access_permission`/`is_pinned_to_dashboard` are only present in this mode — see [Sharing](#sharing) and [Pinning a shared Space](#patch-apiv1custom-categoriesidsharepin). `access_permission` is always `"write"` for `access_source: "house_member"` (House membership has always implied full read/write access — see `db.AccessLevelForList`), and reflects the actual granted permission for `"space_share"`.

### `POST /api/v1/custom-categories`

```bash
curl -X POST http://localhost:8080/api/v1/custom-categories \
  -b cookies.txt -d '{"name": "Vacances", "icon": "🏖️", "color": "#3366ff"}'
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | trimmed; `400` if empty |
| `icon` | string | no | freeform, trimmed; typically an emoji |
| `color` | string | no | must be a 3- or 6-digit hex color (`#RGB`/`#RRGGBB`) if given, else `400`; empty is stored as `""` (no color) |
| `position` | integer | no | defaults to `0`; used for manual ordering |

`201` with the created category, owned by the caller.

### `PUT /api/v1/custom-categories/{id}`

Full replace (same fields/validation as `POST`), scoped to categories the caller owns. `404` if the id doesn't exist **or** belongs to a different user — a category can only ever be found by its owner, so the two cases aren't distinguished.

### `DELETE /api/v1/custom-categories/{id}`

`204` on success, scoped to the caller's own categories the same way `PUT` is (`404` otherwise). Any list attached to the deleted category has its `custom_category_id` reset to `null` (`ON DELETE SET NULL`) — the list itself is untouched, but the category's share recipients lose access to it and are unassigned from its items (see [Task assignments](#task-assignments)).

## Lists

A list has a `type` of `todo`, `shopping`, `groceries`, `recurring_shopping` or `custom`, and belongs to exactly one house (`house_id`). `shopping` (one-off purchases), `groceries` (day-to-day shopping runs) and `recurring_shopping` (subscriptions/recurring purchases) are all purchase-oriented types stored the same way — the frontend just shows a different subset of the item form's fields depending on which one a list has (see `static/js/list_view.js`'s `applyListTypeVisibility`): `groceries` shows name/quantity/aisle (and a price, shown once one of its items has one), `shopping` adds URL/price/target month, `recurring_shopping` adds URL/price/recurrence instead of a target month. Every purchase-oriented list has the [purchase cycle and budget](#purchase-cycle--budget) (planned → to buy → bought, the price actually paid, the list's `budget`). `custom` is accepted by the API for forward compatibility but has no dedicated item-form behavior yet.

A list can optionally be attached to one of its house members' [custom categories](#custom-categories) via `custom_category_id` — see `POST`/`PUT` below. It's orthogonal to `type`: a `shopping` list and a `todo` list can both belong to the same "Vacances" category.

A list also carries a freeform `icon` (typically an emoji, e.g. `🛒`/`🖥️`/`📦`) the frontend shows next to its name; `""` (the default) means no icon was set, in which case the frontend falls back to a fixed icon for the list's `type`.

### `GET /api/v1/lists`

Returns lists belonging to houses the caller is a member of, newest first. Items are **not** embedded here unless `?include=items` asks for them (see `GET /api/v1/lists/{id}`).

Query parameters:
- `type` (optional) — filter to `todo`, `shopping`, `groceries`, `recurring_shopping` or `custom`. `400` if any other value is given.
- `house_id` (optional) — filter to lists belonging to that house. `400` if not a positive integer; `403` if the caller isn't a member of that house.
- `shared_with_me` (optional) — when `"true"`, switches to a different, mutually exclusive mode: instead of the caller's own house-scoped lists, returns every list reachable only via a [List or Space share](#sharing) (see `access_source`/`access_permission` below), excluding any whose house they're already a plain member of. `type`/`house_id` don't apply in this mode.
- `scope` (optional) — `"all"` is a fourth mutually exclusive mode, for integrations: **every** list the caller can open, in one request — the lists of all their houses (newest first), then the `?shared_with_me=true` ones. Each list then carries `access_source`/`access_permission`: `"house_member"`/`"write"` for a house list, the share's own values otherwise, so a client knows which lists are read-only. `type` applies; `house_id` doesn't (`400` if combined). Any other value is a `400`.
- `name` (optional, any mode) — keeps only the lists with that name, compared case-insensitively after trimming (`?scope=all&name=courses`). Names aren't unique, so the result is still an array.
- `include` (optional, any mode) — `"items"` embeds each list's `items`, ordered as in [`GET /api/v1/lists/{id}`](#get-apiv1listsid) (`items` is omitted for an empty list). Any other value is a `400`. The Home Assistant integration polls `?scope=all&include=items`.
- `pinned_house_spaces` (optional) — when `"true"`, a third mutually exclusive mode: every list the caller reaches purely by pinning a Space that's merely *visible* to them through House membership rather than an explicit share (`access_source: "house_member"` — see [Pinning a House-visible Space](#patch-apiv1custom-categoriesidsharepin)). Deliberately not folded into `?shared_with_me=true`'s own query: every row this mode returns already belongs to a House the caller is a member of, which `?shared_with_me=true`'s own exclusion (above) would filter straight back out. This is what lets a pinned House Space's lists show up on the caller's dashboard even while a *different* House they also belong to is the one currently selected. `type`/`house_id` don't apply in this mode either.

```bash
curl -b cookies.txt http://localhost:8080/api/v1/lists
curl -b cookies.txt http://localhost:8080/api/v1/lists?type=shopping
curl -b cookies.txt http://localhost:8080/api/v1/lists?house_id=1
curl -b cookies.txt http://localhost:8080/api/v1/lists?shared_with_me=true
curl -b cookies.txt http://localhost:8080/api/v1/lists?pinned_house_spaces=true
curl -H "Authorization: Bearer trk_…" "http://localhost:8080/api/v1/lists?scope=all&include=items"
curl -H "Authorization: Bearer trk_…" "http://localhost:8080/api/v1/lists?scope=all&name=Courses"
```

```json
[
  { "id": 1, "house_id": 1, "name": "Courses", "type": "shopping", "created_at": "...", "updated_at": "...", "total_amount": 42.5 },
  { "id": 2, "house_id": 1, "name": "Courses de vacances", "type": "shopping", "custom_category_id": 1,
    "custom_category": { "id": 1, "user_id": 1, "name": "Vacances", "icon": "🏖️", "color": "#3366ff", "position": 0, "created_at": "..." },
    "created_at": "...", "updated_at": "..." }
]
```

`total_amount` is the sum of `price * quantity` across every **still-unchecked** item in the list that has a price set (`done = false`) — the list's "reste à dépenser", computed by a correlated subquery in `internal/db`'s shared `listSelect` (not by summing embedded `items`, which this endpoint doesn't even return). A done/purchased item never contributes, regardless of its price. It's omitted entirely when no unchecked item in the list has a price at all (list 2 above); present, including a genuine `0`, otherwise. This is deliberately the same figure a list's own detail view shows in its finance summary as "Reste à dépenser" (`GET /api/v1/lists/{id}`'s embedded `items`, summed client-side by `static/js/list_view.js`'s `updateFinanceSummary`/`lineTotal`) — just computed once in SQL here instead of re-derived per card.

`budget` is the list's budget summary, computed by the same `listSelect` (two more correlated subqueries, then `internal/budget.Summarize`): what was planned against what was actually spent. Every figure is in euros, rounded to the cent:

```json
"budget": {
  "estimated_total": 134.9,
  "spent_total": 55,
  "remaining_total": 75.1,
  "projected_total": 130.1,
  "variance": -4.8,
  "variance_status": "under"
}
```

| Field | Meaning |
|---|---|
| `estimated_total` | the planned budget: `price × quantity` of every item that has a `price` (the estimate), whatever its status |
| `spent_total` | what was actually spent: every bought (`done`) item at `actual_price × quantity` when a price paid was entered, else at its estimate |
| `remaining_total` | the estimate of what isn't bought yet (planned and to buy) — the same figure as `total_amount` |
| `projected_total` | `spent_total + remaining_total`: what the list will have cost if the rest costs what was planned |
| `variance` | `projected_total − estimated_total`, which is also what the purchases made so far cost beyond their estimate. Positive is an overrun, negative a saving; items not bought yet never count as a saving |
| `variance_status` | `"over"` (variance > 0), `"under"` (< 0) or `"on_budget"` (0 to the cent) |

A bought item with an `actual_price` but no `price` is unplanned spending: it adds to `spent_total` and to `variance`, not to `estimated_total`. `budget` is omitted when no item of the list has a price or a price paid (like `total_amount`), and from the `?shared_with_me=true` listing, which has neither. The PWA recomputes the same figures live from the items (`computeBudget` in `static/js/budget.js`, a hand-kept port).

`custom_category`/`custom_category_id` are only present when the list is attached to one — see [Custom categories](#custom-categories). `access_source`/`access_permission`/`is_pinned_to_dashboard` are only present in the `?shared_with_me=true`/`?pinned_house_spaces=true`/`?scope=all` modes above (never on this endpoint's own default, house-scoped listing) — see [Sharing](#sharing). `access_source` is `"list_share"`/`"space_share"` from `?shared_with_me=true`, or `"house_member"` from `?pinned_house_spaces=true`; `?scope=all` has all three, `"house_member"` then meaning any list of one of the caller's houses. For a list reached via `access_source: "space_share"` or `"house_member"`, `is_pinned_to_dashboard` reflects either that list's own individual pin ([`PATCH /api/v1/lists/{id}/share/pin`](#patch-apiv1listsidsharepin)) or the parent Space's own pin as a whole ([`PATCH /api/v1/custom-categories/{id}/share/pin`](#patch-apiv1custom-categoriesidsharepin)) — whichever says pinned wins; for `"house_member"` specifically every returned row is pinned by construction (`?pinned_house_spaces=true` never returns an unpinned one). The dashboard itself is a client-side merge of three calls, deduplicated by list id (a `house_member`-sourced list may already be present in the plain `?house_id=` listing if it happens to belong to the currently selected House): the plain `?house_id=` listing above, `?shared_with_me=true` filtered down to entries with `is_pinned_to_dashboard: true`, and `?pinned_house_spaces=true` in full (see `static/js/app.js`'s `loadDashboard` and `static/js/shares.js`'s `loadPinnedSharedLists`/`loadPinnedHouseSpaceLists`) — there's no server-side "give me my house's lists plus my pinned shares in one call" mode.

### `POST /api/v1/lists`

```bash
curl -X POST http://localhost:8080/api/v1/lists \
  -d '{"name": "Courses", "type": "shopping", "house_id": 1}'
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | trimmed; `400` if empty |
| `type` | string | no | `shopping` (default), `todo`, `groceries`, `recurring_shopping` or `custom`; `400` if anything else |
| `house_id` | integer | yes | must reference an existing house, else `400` |
| `custom_category_id` | integer | no | must reference a [custom category](#custom-categories) owned by the caller, else `400`; omitted/`null` leaves the list unattached |
| `icon` | string | no | trimmed, control characters stripped; freeform (typically an emoji); omitted/empty leaves it unset; `400` if over 32 characters (code points). Any emoji sequence is stored unchanged — skin-tone modifiers, ZWJ sequences (👨‍👩‍👧‍👦), flags and tag sequences all fit well within the limit |
| `template_key` | string | no | create the list from a built-in [template](#list-templates): a purchase one (`cleaning`, `travel`, `moving`, `back_to_school`, `home_maintenance`) or a task one (`cleaning_tasks`, `moving_tasks`, `back_to_school_tasks`); `400` for an unknown key |
| `template_id` | integer | no | create the list from one of the House's own [templates](#list-templates); `400` unless it's a template of `house_id` (the same answer for a missing one and another House's). `400` if sent together with `template_key` |
| `lang` | string | no | `fr` or `en`: the language a built-in template's names are written in. Anything else (or omitted) falls back to the caller's own language, then the instance's `DEFAULT_APP_LANGUAGE` |

`201` with the created list (no `items` field).

**From a template**, the list is created together with a copy of every template item — title, quantity, estimated price (as the item's `price`), aisle and recurrence rule (as its `recurrence_rule`), in order — with no due date and no reminder, in one transaction. In a purchase list (`shopping`, `groceries`, `recurring_shopping`) every item starts with `purchase_status: "planned"`; in a `todo` list, `to_buy` (a task has no purchase cycle). `name`, `type` and `icon` may then be omitted or empty: each takes the template's (a built-in purchase template creates a `groceries` list, a built-in task template a `todo` one). A `type` must belong to the template's **family** — any purchase type for a purchase template, `todo` for a task template, `custom` for a `custom` one — else `400` (`type does not match the template's list type`): a task list never receives prices and aisles, nor a purchase list tasks. A purchase list's `budget.estimated_total` is then the template's total; a task list has no `budget`. A recurring task with no due date starts its series the day it is first checked off (its next occurrence is counted from that day) or from the start date set later. The offline queue refuses this request (`503`): the items come from the server.

```bash
curl -X POST http://localhost:8080/api/v1/lists \
  -d '{"house_id": 1, "template_key": "back_to_school", "lang": "fr"}'
# => {"id": 7, "name": "Rentrée scolaire", "type": "groceries", "icon": "🎒", ..., "budget": {"estimated_total": 134.9, ...}}

curl -X POST http://localhost:8080/api/v1/lists \
  -d '{"house_id": 1, "template_key": "cleaning_tasks", "lang": "fr"}'
# => {"id": 8, "name": "Tâches de Ménage & Entretien", "type": "todo", "icon": "🧹", ...}
```

### `GET /api/v1/lists/{id}`

Returns the list **with its items embedded**, ordered by `position` then `id`.

- `200` with the list, e.g.:
  ```json
  {
    "id": 1, "house_id": 1, "name": "Courses", "type": "shopping",
    "created_at": "...", "updated_at": "...", "total_amount": 3.7,
    "items": [
      { "id": 1, "list_id": 1, "title": "Lait", "url": "https://...", "quantity": 2, "price": 1.85, "price_auto": false, "image_url": "https://...", "done": false, "position": 0, "target_month": "2026-11", "due_date": null, "is_recurring": false, "recurrence_rule": null, "recurrence_end_date": null, "is_urgent": false, "target_price": null, "alert_on_price_drop": false, "labels": [], "reminder_enabled": false, "created_at": "...", "updated_at": "..." }
    ]
  }
  ```
- `404` if the list doesn't exist. `403` unless the caller has at least read [access](#sharing) to the list — house membership, a Space share, or a List share.
- `total_amount` here is the same server-computed field `GET /api/v1/lists` documents above (the list's "reste à dépenser" — unchecked, priced items only) — included on this endpoint too even though `items` is already embedded, so a caller never has to re-derive it by summing `items` itself. So is `budget` (planned vs. actual spending and their variance, see [above](#get-apiv1lists)).

### `PUT /api/v1/lists/{id}`

Full replace of `name`, `type`, `custom_category_id`, and `icon` (same validation as `POST`; omitting/nulling `custom_category_id` **dissociates** the list from whatever category it had, and omitting `icon` clears it, since this is a full replace). `house_id` cannot be changed via this endpoint. Returns the updated list (no `items`). Moving the list to another Space (or out of one) unassigns, self-hosted, the old Space's share recipients who can no longer open it (see [Task assignments](#task-assignments)). `404` if not found, `403` unless the caller has **write** [access](#sharing) to the list (house membership, or a `write` Space/List share), `400` if `custom_category_id` is given but doesn't reference a category the caller owns.

### `DELETE /api/v1/lists/{id}`

`204` on success. Deleting a list **cascades** to delete all its items (`ON DELETE CASCADE`). `404` if not found, `403` if the caller isn't a member of the list's house — deliberately **not** extended to a `write` Space/List share the way `PUT` and the [items](#items) endpoints are, since deleting a list outright is a house-management-level action, not just editing it (see [Sharing](#sharing)).

### `PUT /api/v1/lists/{id}/reorder`

Applies a manual drag-and-drop reordering of every item in the list — the frontend's "⇅ Réordonner" mode (`static/js/reorder.js`). `item_ids` must be the **complete** new ordering: every item currently in the list, each listed exactly once. A partial list, a duplicate, or an id belonging to a different list is rejected outright (`400`) rather than partially applied — accepting a subset would leave the positions of the items left out ambiguous relative to the reordered ones.

```bash
curl -X PUT http://localhost:8080/api/v1/lists/1/reorder \
  -d '{"item_ids": [3, 1, 2]}'
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `item_ids` | array of integers | yes | must be exactly a permutation of the list's current item ids, else `400` |

`200` with the list's items in their new order (same shape as the `items` array embedded by `GET /api/v1/lists/{id}`), each now carrying its updated `position`. `404` if the list doesn't exist. `403` unless the caller has **write** [access](#sharing) to the list — reordering is an editing action on the list's contents, the same bar `PUT`/`PATCH` on an [item](#items) already require, not a house-management-level action like deleting the list itself.

## List templates

Ready-made lists a new list can start from ([`POST /api/v1/lists`](#post-apiv1lists) with `template_key` or `template_id`). Server side: `internal/handlers/shopping_templates.go`, `internal/shoptemplates` (the built-in ones), `internal/db/shopping_templates.go` (the House ones, [tables](DATABASE.md#shopping_templates-and-shopping_template_items) `shopping_templates`/`shopping_template_items`). UI: the "Partir d'un modèle" picker of the "Nouvelle liste" modal and "Enregistrer comme modèle" in a list's options (`static/js/budget.js`).

- **Built-in templates** ship with the code, translated (French and English), so a release can change them without a migration:
  - **Purchase templates** (`list_type: "groceries"`): 🧹 `cleaning` (Ménage & Entretien), ✈️ `travel` (Vacances / Voyage), 📦 `moving` (Déménagement), 🎒 `back_to_school` (Rentrée scolaire), 🏡 `home_maintenance` (Entretien de la maison / Bricolage). Each has 12–15 essential items with their aisle (including the non-food aisles `stationery`, `clothing`, `electronics`, `diy`) and an estimated unit price, editable once the list exists.
  - **Task templates** (`list_type: "todo"`): 🧹 `cleaning_tasks` (Tâches de Ménage & Entretien — 15 chores, each with its own recurrence, from weekly vacuuming to yearly boiler servicing), 📦 `moving_tasks` (To-Do Déménagement — 12 one-off tasks: notice, energy contracts, meter readings, change of address…), 🎒 `back_to_school_tasks` (Check-list Rentrée & Papiers — 12 one-off tasks: school insurance certificate, health form, canteen registration…). No price, no aisle, quantity 1.
- **House templates** are saved by a member from one of the House's lists and belong to the House, like its [product catalog](#house-product-catalog): every member can see, use and delete them; a list-share recipient can't (`403`). A House keeps at most 100, of at most 500 items each.
- **Families**: a template only fills lists of its own family — purchase templates any of `shopping`, `groceries`, `recurring_shopping`; task templates `todo`; a `custom` list's template another `custom` list. `GET` can filter by it (`list_type`), and `POST /api/v1/lists` refuses a mismatch.

A template has this shape (`id`/`house_id`/`created_by`/`created_at` for a House template only, `key` for a built-in one):

```json
{
  "id": 3, "builtin": false, "house_id": 1, "name": "Samedi", "icon": "🧺", "list_type": "groceries",
  "created_by": 1, "created_at": "...", "item_count": 2, "estimated_total": 10.8,
  "items": [
    { "title": "Lait", "quantity": 6, "estimated_price": 1.8, "aisle": "dairy" },
    { "title": "Pain", "quantity": 1 }
  ]
}
```

A task template's items carry a `recurrence_rule` instead (omitted for a one-off task):

```json
{
  "key": "cleaning_tasks", "builtin": true, "name": "Tâches de Ménage & Entretien", "icon": "🧹", "list_type": "todo",
  "item_count": 15, "estimated_total": 0,
  "items": [
    { "title": "Passer l'aspirateur", "quantity": 1, "recurrence_rule": "FREQ=WEEKLY" },
    { "title": "Laver les vitres", "quantity": 1, "recurrence_rule": "FREQ=MONTHLY" }
  ]
}
```

`estimated_price` is a unit price (omitted when unknown), `estimated_total` is `Σ quantity × estimated_price` to the cent (`0` for a task template). `recurrence_rule` is a canonical [recurrence rule](#recurring-items) (`FREQ=WEEKLY`, `FREQ=MONTHLY;INTERVAL=3`, …), copied as is into the new item.

### `GET /api/v1/shopping-templates`

The built-in templates, then the House templates of every House the caller is a member of, newest first.

- `house_id` (optional) — only that House's templates (after the built-in ones). `400` if not a positive integer, `403` if the caller isn't a member.
- `lang` (optional) — `fr` or `en`, the language of the built-in templates; otherwise the caller's own language, then `DEFAULT_APP_LANGUAGE`.
- `list_type` (optional) — only the templates a list of that type may start from, i.e. of its family: `todo` returns the task templates, `shopping`/`groceries`/`recurring_shopping` the purchase ones, `custom` the House's `custom` ones (there is no built-in one). `400` for an unknown type. The PWA loads them all once and filters as the type picked changes.

```bash
curl -b cookies.txt "http://localhost:8080/api/v1/shopping-templates?house_id=1&lang=en"
curl -b cookies.txt "http://localhost:8080/api/v1/shopping-templates?house_id=1&list_type=todo"
```

### `POST /api/v1/shopping-templates`

Saves a list as a template of its House: every item in list order, with its title, quantity and recurrence rule (never its dates) — and, from a purchase list, its aisle and as its estimate the price actually paid when one was entered (`actual_price` — the best guess for next time), else its `price`. A `todo` or `custom` list's template carries no price or aisle, even one an item kept from an earlier list type. The template's `list_type` is the list's type.

| Field | Type | Required | Notes |
|---|---|---|---|
| `list_id` | integer | yes | `400` if missing or not an existing list |
| `name` | string | no | trimmed; defaults to the list's name; `400` if over 200 characters |
| `icon` | string | no | defaults to the list's icon; `400` if over 32 characters |

`201` with the template. `403` unless the caller can read the list **and** is a member of its House. `400` if the list has no items, more than 500, or the House already has 100 templates.

### `DELETE /api/v1/shopping-templates/{id}`

`204`. `404` if no such template, `403` unless the caller is a member of its House. Lists created from it are not affected. Built-in templates can't be deleted.

## Sharing

Beyond house membership, a [Space](#custom-categories) or an individual [List](#lists) can be shared directly with one other user by email, granting `read` or `write` access to it without adding them to the whole parent house. A user's effective access level to a list is the highest of: house membership (always `write`), a `list_shares` grant on the list itself, and a `space_shares` grant on the list's attached custom category, if any. See the "granular sharing" design in [CLAUDE.md](../CLAUDE.md) and [docs/DATABASE.md](DATABASE.md#space_shares-and-list_shares) for the full model.

Sharing a **Space** is its owner's call alone (the same person who can rename/delete it) — `custom_categories` has no other notion of membership. Sharing a **List** requires actual membership of its house, not merely write access granted through another share, so access can never be used to extend itself further.

### `GET /api/v1/users/search`

Autocomplete for the "invite by email" fields — the share modal and the House members modal (`static/js/user-search.js`). Suggests accounts matching `q`; picking one only fills in its email address, which is then submitted to the share/invite endpoint as usual. Never returns the caller. At most 10 results.

What `q` may match depends on the instance's deployment mode ([`GET /api/v1/config`](#get-apiv1config)'s `saas_mode`):

| | Self-hosted (`saas_mode: false`, default) | SaaS (`saas_mode: true`) |
|---|---|---|
| Matched against | display name **and** email address | email address **only** |
| Among | every account on the instance | the caller's **contacts** only: co-members of their houses, recipients of shares on their houses' lists or their own spaces, and the house members/space owner behind anything shared with them |
| Matching | every whitespace-separated term of `q` must appear (case-insensitively, anywhere) in the name or the address — `jean`, `dupont`, `dupont jean`, `jean@exa` all find "Jean Dupont" | `q` must appear (case-insensitively, anywhere) in the address |
| Result | `id`, `email`, `display_name` | `id`, `email` (no `display_name`) |

Trakka stores one `display_name` per account rather than separate first/last names; matching every term anywhere in it is what makes both "Prénom Nom" and "Nom Prénom" work.

SaaS mode never suggests a stranger, **not even by their exact address**: the search would otherwise tell any user whether an arbitrary address has an account, the very oracle [docs/AUDIT.md](AUDIT.md) L-06 closed on the share/invite endpoints. A stranger is still invited by typing their full address, through those endpoints' unchanged, non-disclosing invitation flow.

```bash
curl -b cookies.txt 'http://localhost:8080/api/v1/users/search?q=dupont'
```

```json
[
  { "id": 2, "email": "jean.dupont@example.com", "display_name": "Jean Dupont" }
]
```

| Query | Notes |
|---|---|
| `q` | trimmed; fewer than 2 characters → `200 []`; over 254 bytes → `400`. `%` and `_` match literally. |

Rate-limited per account to 60 searches per minute (`429` with `Retry-After`), so the autocomplete can't be used to bulk-scrape whatever it is allowed to reveal. The frontend debounces keystrokes (250 ms) and stays well under that.

Both share types expose the same three endpoints, swapping `/custom-categories/{id}` for `/lists/{id}`:

### `GET /api/v1/custom-categories/{id}/share` · `GET /api/v1/lists/{id}/share`

Lists everyone a Space/List is currently shared with (the roster shown in the share modal), each with the recipient's `email`/`display_name` joined in. `404` if the category doesn't exist or isn't owned by the caller (Space); `404` if the list doesn't exist, `403` if the caller isn't a member of its house (List).

```bash
curl -b cookies.txt http://localhost:8080/api/v1/lists/1/share
```

```json
[
  { "id": 1, "list_id": 1, "shared_with_user_id": 2, "permission": "read", "is_pinned_to_dashboard": false, "created_at": "...", "email": "bob@example.com", "display_name": "Bob" }
]
```

(A Space's roster carries the same `is_pinned_to_dashboard` field, reflecting whether *that specific recipient* has pinned the whole Space — it's per-share, not a property of the Space itself.)

### `POST /api/v1/custom-categories/{id}/share` · `POST /api/v1/lists/{id}/share`

Grants (or updates the permission of, if one already exists) a share, looked up by the recipient's email — there's no email-sending infrastructure in this project, so an email with no account fails clearly rather than creating a ghost row, mirroring `POST /api/v1/houses/{id}/members`.

```bash
curl -X POST http://localhost:8080/api/v1/lists/1/share \
  -d '{"email": "bob@example.com", "permission": "read"}'
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `email` | string | yes | trimmed; `400` if empty; `404` if no account exists for it |
| `permission` | string | yes | `"read"` or `"write"`, else `400` |

`400` if sharing with yourself, or (List only) if the recipient is already a member of the list's house. `404`/`403` access rules match the `GET` above. `201` with the created/updated share.

### `PATCH /api/v1/lists/{id}/share/pin`

Lets the *recipient* of a share choose whether this list shows up pinned on their own dashboard, alongside their own house's lists, instead of only in the "Partagé avec moi" tab. Unlike every other endpoint in this section, the caller here is the share's recipient, not someone managing the list's house.

Works both for a list shared directly (an existing `list_shares` row) **and** one reached only through a shared Space: in the latter case, there's no `list_shares` row to flip yet, so this auto-creates one — scoped to exactly the permission the Space already grants for this list, so the new row can never itself change the caller's actual access level (see `db.AccessLevelForList`, which already takes the higher of the two sources). This lets one list from an otherwise-unpinned shared Space be pinned individually without pinning the whole Space (see below).

```bash
curl -X PATCH http://localhost:8080/api/v1/lists/1/share/pin -d '{"pinned": true}'
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `pinned` | boolean | yes | `400` if omitted |

`200` with the updated share (same shape as the `POST` above, `is_pinned_to_dashboard` reflecting the new state). `404` if the caller has neither a `list_shares` row nor any Space-based access to this list at all.

Note: if the list's parent Space is *itself* pinned as a whole (see below), this list keeps showing up pinned regardless of this endpoint's own state for it — `GET /api/v1/lists?shared_with_me=true` ORs the two sources together, and there is currently no way to pin a Space while excluding one specific list from it.

### `PATCH /api/v1/custom-categories/{id}/share/pin`

The Space-level equivalent of the List endpoint above: lets a viewer who can see a Space without owning it choose to pin the whole Space, which does two things at once — the Space itself starts showing up in the viewer's own `GET /api/v1/custom-categories?shared_with_me=true` listing (and their "Espaces" tab), and **every list reachable through it** — present at pin time, no per-list action needed — starts coming back pinned too (from `GET /api/v1/lists?shared_with_me=true` for a `space_share`-sourced Space, or `GET /api/v1/lists?pinned_house_spaces=true` for a `house_member`-sourced one), so they all show up on the recipient's dashboard in one action.

There are two ways a caller can be entitled to pin, tried in order:

1. **An explicit `space_shares` grant** (the Space's owner shared it with this caller directly) — flips `space_shares.is_pinned_to_dashboard`. `200` with the updated Space share (same shape as the roster's own rows above).
2. **House-membership-based access** (nobody shared anything; the caller is simply a member of a House that uses this Space on at least one of its lists) — tried only if (1) finds no `space_shares` row for this caller at all. Records the preference in a separate `space_house_pins` table (see [docs/DATABASE.md](DATABASE.md#space_shares-and-list_shares)), since there's no `space_shares` row to flip a flag on. `200` with a smaller response shape instead: `{ "custom_category_id": 1, "is_pinned_to_dashboard": true, "access_source": "house_member" }`.

```bash
curl -X PATCH http://localhost:8080/api/v1/custom-categories/1/share/pin -d '{"pinned": true}'
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `pinned` | boolean | yes | `400` if omitted |

`404` only if *neither* path recognizes the caller — including the category's own owner, who has no `space_shares` row on their own Space, and is explicitly excluded from the House-membership fallback too even though they're typically also a House member of wherever their own Space is used (`db.spaceAccessibleViaHouse` filters out the category's own owner) — there's nothing to pin on a Space you already own and always see.

### `DELETE /api/v1/custom-categories/{id}/share/{userId}` · `DELETE /api/v1/lists/{id}/share/{userId}`

Revokes a share. `204` on success, `404` if no such share exists (also on a repeat call — revoking is not idempotent-silent), otherwise the same `404`/`403` access rules as `GET` above. The recipient is then unassigned from the items of the list (or of the Space's lists) they can no longer open (see [Task assignments](#task-assignments)).

## Public share links

A House member can open a list to anyone holding a secret link, `/public/lists/{token}`, with no Trakka account, in one of four access modes. A list can have several links — one per audience ("Famille" in `full_edit`, "Invités" in `deposit_only`...), each with its own mode, optional label and expiry, revoked on its own (at most 20 per list). Code: `internal/handlers/public_links.go` (`publicModeAllows`); table: [`list_public_links`](DATABASE.md#list_public_links); front end: the share modal's "Liens de partage publics" section (`static/js/public-link.js`) and the visitor's page (`static/public-list.html`, `static/js/public-list.js`).

| `access_mode` | Sees the items | Adds | Checks, unchecks, renames, changes quantities |
|---|---|---|---|
| `read` (👁️ Lecture seule) | yes | — | — |
| `add_only` (➕ Ajout uniquement) | yes | yes | — |
| `deposit_only` (📥 Dépôt à l'aveugle) | **no** — name and icon only | yes | — |
| `full_edit` (✏️ Édition complète) | yes | yes | yes |

No mode deletes. The mode is checked before anything about the list's items is read, so a refused request (`403`) reveals nothing — a `deposit_only` `PATCH` gets the same `403` for a real item id as for a made-up one.

- **The token** is 32 `crypto/rand` bytes (43 base64url characters), one per link. Unlike sessions, the [calendar feed](#calendar-feed) and [API tokens](#api-tokens), it is **stored in clear**, so House members can show and copy every link from the share modal at any time until it is revoked (the owner's choice; a database leak or backup therefore hands out working public links). A leaked link is revoked on its own; the list's other links keep working.
- **Managing** links takes a session and real House membership of the list (like sharing it with a person) — not a share of it, and never an [API token](#api-tokens) (`403`). Not under `/lists/{id}/share`, whose `POST` already shares the list with a person by email. A link can't be edited: to change its mode, label or expiry, create another and revoke the old one.
- **Visitors' routes** sit outside the session gate. A token that is unknown, replaced, disabled or past its `expires_at` is a `404` on all of them — the four can't be told apart from outside — and a request the link's mode doesn't allow is a `403`. Writes are capped at 60 a minute per link (`429`) and adding stops at 500 items on the list (`409`); cross-site writes are refused like every other write (`Origin`/`Sec-Fetch-Site`). A visitor can't delete, set a URL (so they can never make the server fetch a page), a price, a reminder, labels or assignees. Responses carry `Cache-Control: no-store` and `X-Robots-Tag: noindex, nofollow, noarchive`; the server logs every path with the token replaced by `{token}`.

### `GET /api/v1/lists/{id}/public-links`

Every public link of the list, oldest first — expired and suspended ones included (flagged), so they can be seen and revoked. Each with its link, since this is how the share modal copies it: reserved to House members (`403` for anyone else, a share holder included; an API token is refused too), `404` for an unknown list.

```json
[{"id": 7, "label": "Famille", "access_mode": "full_edit", "is_enabled": true, "expired": false,
  "created_at": "2026-10-19T09:22:41.101Z", "expires_at": null, "last_used_at": "2026-10-19T09:23:02.420Z",
  "token": "Xk3…43 characters…", "path": "/public/lists/Xk3…"},
 {"id": 8, "label": null, "access_mode": "deposit_only", "is_enabled": true, "expired": false,
  "created_at": "2026-10-19T09:25:10.002Z", "expires_at": "2026-10-26T09:25:09Z",
  "token": "pQ9…", "path": "/public/lists/pQ9…"}]
```

`label` is `null` for an unnamed link (the modal then names it by its mode); `last_used_at` (to the minute) is omitted until a visitor first opens the link. `is_enabled` is `false` only for a link suspended directly in the database (no route sets it).

### `POST /api/v1/lists/{id}/public-links`

Creates a new link, next to the list's others.

| Field | Type | Required | Notes |
|---|---|---|---|
| `access_mode` | string | yes | `"read"`, `"add_only"`, `"deposit_only"` or `"full_edit"` (see the table above); anything else, `"write"` included, is a `400` |
| `label` | string \| null | no | names the link ("Famille", "Artisans", "Invités"); trimmed, at most 50 characters (`400` beyond); absent, `null` or blank for none |
| `expires_at` | string \| null | no | RFC 3339 date-time in the future; absent, `null` or `""` for never |

`201` with the new link in the shape of `GET` above. `400` for an invalid field, `409` once the list has 20 links.

### `DELETE /api/v1/lists/{id}/public-links/{linkId}`

Revokes that link for good — its token stops working at once; the list's other links are untouched. `204`; `404` for a link the list doesn't have (already revoked, or another list's), `400` for a malformed id.

### `GET /api/v1/public/lists/{token}`

No session. The list as a visitor sees it — its name, icon, type and the link's mode and expiry, and per item only what using the list takes (never the House, its members, assignees, URLs or prices):

```json
{"name": "Courses du samedi", "icon": "🥖", "type": "groceries", "access_mode": "full_edit", "expires_at": null,
 "items": [{"id": 12, "title": "Lait", "quantity": 2, "done": false, "is_urgent": false, "labels": [], "aisle": "Crèmerie"}]}
```

Items come in the list's own order; `aisle`, `due_date` and `due_time` are omitted when unset. For a `deposit_only` link, `items` is always `[]` (the items aren't even read).

### `POST /api/v1/public/lists/{token}/items`

No session; `add_only`, `deposit_only` and `full_edit` links (`403` for `read`). Body: `title` (required, ≤ 500 characters) and `quantity` (optional, default 1, ≤ 100000). `201` with the new item in the shape above — except through a `deposit_only` link, which always answers `201 {"added": true}`, merged or not, so the reply can't tell what the list holds. On a [Courses list](#quick-shopping-lists-courses), a product already on the list is updated instead (`200`), like an authenticated add. The list's users are notified as for an addition by a member, the actor named "Un visiteur (lien public)".

### `PATCH /api/v1/public/lists/{token}/items/{itemId}`

No session; `full_edit` links only (`403` for the three others, before the item is even looked up). Any of `done` (boolean), `title` and `quantity`; anything else is a `400`, as is an empty body. Checking an item off has the same effects as for a member (a recurring item moves to its next occurrence, a Courses purchase is recorded, the list's users are notified). An item of another list is a `404`. `200` with the item.

### `GET /public/lists/{token}`

The visitor's HTML page (`static/public-list.html`): the same static page for every token, served with `Cache-Control: no-store` and `X-Robots-Tag`; `404` for a token outside the base64url alphabet. The page reads the token from its URL and says "Lien indisponible" itself when the API answers `404`. It polls the list every 10 s while visible, to show other people's changes.

## Items

### `GET /api/v1/items?list_id={id}`

`list_id` is required. Returns `200` with an array. `400` if `list_id` is missing, not a positive integer, or doesn't reference an existing list. `403` unless the caller has at least read [access](#sharing) to the list.

`title` (optional) keeps only the items with that title, compared case-insensitively after trimming — how a script that knows an item by name finds the id the write routes take. Titles aren't unique, so the result is still an array (possibly empty).

```bash
curl -b cookies.txt "http://localhost:8080/api/v1/items?list_id=1"
curl -H "Authorization: Bearer trk_…" "http://localhost:8080/api/v1/items?list_id=1&title=lait"
```

### `POST /api/v1/items`

```bash
curl -X POST http://localhost:8080/api/v1/items \
  -d '{"list_id": 1, "title": "Lait", "url": "https://example.com/lait", "quantity": 2, "price": 1.85}'
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `list_id` | integer | yes | must reference an existing list, else `400` |
| `title` | string | yes | trimmed; `400` if empty |
| `url` | string | no | must be an absolute `http://`/`https://` URL if given, else `400`; empty string is stored as `null` |
| `quantity` | integer | no | defaults to `1` if omitted or `<= 0` |
| `price` | number | no | unit price in euros — the estimate the [budget](#purchase-cycle--budget) compares spending with; `400` if negative; omitted/`null` means no price recorded |
| `purchase_status` | string | no | `"planned"` or `"to_buy"` (the default); `"bought"` is refused (`400`): an item is bought by checking it off. See [Purchase cycle & budget](#purchase-cycle--budget) |
| `position` | integer | no | defaults to `0`; used for manual ordering within a list |
| `target_month` | string | no | planned purchase month, `YYYY-MM` (e.g. `"2026-11"`); `400` if given but not in that format; empty string is stored as `null` (unscheduled) |
| `due_date` | string | no | `YYYY-MM-DD`; `400` if given but not a real calendar date in that format. For a recurring item it is the current occurrence's date, moved forward automatically (see [Recurring items](#recurring-items)) |
| `due_time` | string | no | `HH:MM` (24h), an optional time for `due_date` in the instance's `APP_TIMEZONE`; `400` if not a real time, or if given without a `due_date`. What the "at the exact due time" reminder fires at |
| `recurrence_rule` | string | no | an iCalendar RRULE subset (RFC 5545): `FREQ=DAILY\|WEEKLY\|MONTHLY\|YEARLY`, optional `INTERVAL=n` (1–999), and for `FREQ=WEEKLY` optional `BYDAY=` days among `MO,TU,WE,TH,FR,SA,SU` — e.g. `"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR"`. Case, part order, an `RRULE:` prefix and repeated days don't matter: it's stored and returned in one canonical spelling (parts in that order, days Monday first, `INTERVAL` omitted when 1). The older forms `DAILY`/`WEEKLY`/`MONTHLY`/`YEARLY`/`EVERY_X_DAYS:<n>`/`EVERY_X_MONTHS:<n>` are still accepted and come back canonical (`EVERY_X_DAYS:14` → `FREQ=WEEKLY;INTERVAL=2`). `400` for anything else — including `COUNT`/`UNTIL` (use `recurrence_end_date`), `BYDAY` on a non-weekly rule, or ordinal days like `1MO`. There is no separate `is_recurring` request field — `is_recurring` in the response is simply whether this is set |
| `recurrence_end_date` | string | no | `YYYY-MM-DD`; the last date a recurring item should recur on. `400` if given but not a real calendar date |
| `is_urgent` | boolean | no | flags the item as needing attention right away (e.g. "out of toilet paper"); defaults to `false`. Independent of every other field — no validation beyond being a boolean |
| `recurrence_lead_minutes` | integer | no | superseded by `reminder_enabled`/`reminder_offset_days`/`reminder_time` below — still stored and echoed back for API backward compatibility, but no longer read by any scan; `400` if negative |
| `target_price` | number | no | a price threshold in euros; `400` if negative. See [Price drop alerts](#price-drop-alerts) below |
| `alert_on_price_drop` | boolean | no | opts the item into a notification once `price` reaches `target_price`; defaults to `false`. Independent of `target_price` itself being set — see [Price drop alerts](#price-drop-alerts) |
| `labels` | array of strings | no | freeform tags (e.g. `["Bio", "Promo"]`); see [Labels](#labels) below |
| `reminder_enabled` | boolean | no | whether `due_date` should ever produce a reminder push; defaults to `false` if omitted (an explicit `true` is what actually opts a task in) — see [Reminders](#reminders) below |
| `reminder_offset_days` | integer | no | how many whole days before `due_date` the reminder fires (`0` = same day, `1` = the day before); `400` if negative. Omitted/`null` resolves to the caller's own `reminder_default_offset_days` (see [`PATCH /api/v1/me`](#patch-apiv1me)) at write time |
| `reminder_time` | string | no | `HH:MM` (24h) time of day the reminder fires at; `400` if given but not a real time. Omitted/empty resolves to the caller's own `reminder_default_time` at write time |
| `reminder_at_due_time` | boolean | no | the "at the exact due time" mode: remind at `due_date` + `due_time`, with `reminder_offset_days`/`reminder_time` kept as the fallback for an item without a `due_time`. Omitted/`null` resolves to the caller's `reminder_default_at_due_time` — unless the request spells out `reminder_offset_days` or `reminder_time`, which then means `false` |
| `aisle` | string | no | the store aisle, one of `produce`, `bakery`, `meat_fish`, `dairy`, `pantry`, `breakfast`, `drinks`, `baby`, `pets`, `household`, `hygiene`, `stationery`, `clothing`, `electronics`, `diy`, `frozen`, `other` (`internal/catalog.Aisles`, in shop-walk order); `400` for anything else. Omitted/empty: on a "Courses" (`groceries`) list the server picks one — see [Quick shopping lists](#quick-shopping-lists-courses); elsewhere none |
| `aisle_hint` | string | no | same values as `aisle`: a suggestion (the aisle [Open Food Facts](#get-apiv1housesidproductsbarcodecode) gave a scanned product) used on a Courses list only when the household has no aisle of its own for the product; never overrides one |
| `assignee_ids` | array of integers | no | the user ids of the people the item is assigned to (duplicates ignored, at most 50); omitted or `[]` leaves it unassigned. Each must be one of the list's [assignable users](#get-apiv1listsidassignable-users) (`400` otherwise); in SaaS mode, `403` if the caller isn't a member of the list's House. See [Task assignments](#task-assignments) |
| `barcode` | string | no | the EAN/UPC code scanned for this product: 8 to 14 digits (spaces and dashes ignored, a 12-digit UPC-A stored as its 13-digit EAN form), else `400`. On a Courses list it's filed under the product in the House catalog, so the next scan recognizes it; not stored on the item |

**Courses lists never hold the same product twice.** On a `groceries` list, a `title` naming a product already on the list (compared case-, accent- and punctuation-insensitively: `"Lait demi-écrémé"` = `"LAIT DEMI ECREME"`) doesn't create a second item: that item is updated instead and returned with `200` (not `201`) and a response-only `"merged": true` — see [Quick shopping lists](#quick-shopping-lists-courses) for exactly what changes.

Responses also carry `assignees` (always an array, `[]` when unassigned — see [Task assignments](#task-assignments)), `purchase_status` (always) and `actual_price` (once entered) — see [Purchase cycle & budget](#purchase-cycle--budget) — and three read-only fields: `next_due_date` (`YYYY-MM-DD`, only on a checked-off recurring item — see [Recurring items](#recurring-items)), `notification_sent_at` (ISO-8601 UTC time the reminder for the *current* due date/time was pushed, absent while it's still pending) and `reminder_at_due_time`. Like every optional field, they're omitted when empty.

`400` if `list_id` doesn't reference an existing list. `403` unless the caller has **write** [access](#sharing) to the list. `201` with the created item. If `url` is given, the server kicks off a **best-effort** lookup against that URL (see `internal/scraper` in [CLAUDE.md](../CLAUDE.md)) for whichever of `price`/`image_url` the item is still missing (an explicit `price` in the request skips price detection but the image is still looked up), and waits up to ~2.5s for it before responding: if it finishes in time, the `201` response already carries `price` (with `price_auto: true`) and `image_url`, and reports `price_status: "found"`; if the site is slow to respond, the lookup keeps running in the background and the response instead carries `price_status: "pending"` — poll `GET /api/v1/items/{id}` (or re-fetch the list) a few seconds later to pick it up. `price_status: "none"` means there was no `url` to scrape or no price was found in time — this is unaffected by whether an image was found. `image_url`, when present, is always an absolute `http://`/`https://` URL. `price_status` is response-only — it's never persisted and never appears on a plain `GET`.

### Recurring items

Setting `recurrence_rule` (`POST`, `PUT`, or `PATCH`) makes an item recurring — the frontend offers the four fixed cadences plus "Personnalisée…", a sheet building any rule the API accepts (frequency, interval, weekdays, start date). How the next occurrence is found (`internal/recurrence.Rule.After`): `DAILY`/`WEEKLY` without `BYDAY` add `INTERVAL` days/weeks; **`WEEKLY` with `BYDAY`** takes the next listed day later in the same Monday–Sunday week, else the first listed day `INTERVAL` weeks later — so `FREQ=WEEKLY;BYDAY=MO,WE,FR` completed on its Friday occurrence comes back on Monday, and `INTERVAL=2` keeps the off-weeks off; `MONTHLY`/`YEARLY` keep the day of the month, clamped to the month's last day (January 31 → February 28). A recurring item is one row for its whole life — there's no cloned row per occurrence:

1. **Checked off** (`PATCH {"done": true}`, or `PUT` with `done: true`): the item **stays done** (the list's "Terminés" section), keeps `due_date` on the occurrence just completed, and gets `next_due_date` — the next occurrence strictly after today, stepping from `due_date` (or from today if it has none). Completing early still steps from the due date (a weekly task due Friday, done Wednesday, comes back the following Friday); a task left overdue for several periods skips the missed ones rather than coming back already overdue.
2. **Next occurrence starts**: a background scheduler (every minute, whether or not push is configured) un-checks the item and moves `next_due_date` into `due_date` at the start of that day in `APP_TIMEZONE` — or earlier, at its reminder moment, when that comes first (a "la veille à 20:00" reminder brings it back the evening before, so the notification never points at a task still in "Terminés"). The new due date re-arms the reminder.
3. **Un-checked by hand** before then: `next_due_date` is cleared and the item is active again on the occurrence it had.

If `recurrence_end_date` is set and the next occurrence would fall after it, the series has ended: the item stays done with no `next_due_date`, like a non-recurring item. Editing a done item's rule, due date or end date recomputes `next_due_date`; any other edit keeps it. Clearing `recurrence_rule` (an empty string via `PATCH`, or omitting it via `PUT`) turns off recurrence and clears `recurrence_end_date` (and any `next_due_date`); `due_date`/`due_time` are kept, since any item can have a due date.

Like any item with a `due_date`, a recurring one also gets a reminder push once its own reminder moment is reached — see [Reminders](#reminders) below and [Push notifications](#push-notifications) for the full mechanism.

```bash
# make an item repeat every two weeks, due Sunday at 19:00
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"recurrence_rule": "FREQ=WEEKLY;INTERVAL=2", "due_date": "2026-10-11", "due_time": "19:00"}'

# check it off — done:true, due_date unchanged, next_due_date "2026-10-25"
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"done": true}'

# Monday, Wednesday and Friday; checked off on Friday 2026-10-02 → next_due_date "2026-10-05"
curl -X PATCH http://localhost:8080/api/v1/items/2 -d '{"recurrence_rule": "FREQ=WEEKLY;BYDAY=MO,WE,FR", "due_date": "2026-10-02"}'
```

The start date the frontend asks for isn't stored separately: it sets `due_date` to the series' first occurrence (the start date itself, or for a weekly rule the first listed day on or after it), and every later occurrence steps from the current `due_date`.

### Reminders

Any item with a `due_date` — recurring or not — can carry a Google-Tasks-style reminder: `reminder_enabled` gates whether it fires at all, and `reminder_offset_days`/`reminder_time` say how many whole days before `due_date` and at what wall-clock time of day (`HH:MM`, 24h, interpreted in the instance's `APP_TIMEZONE` — see [docs/DEPLOYMENT.md](DEPLOYMENT.md)). With `reminder_at_due_time: true` and a `due_time`, it fires at `due_date` + `due_time` instead ("à l'heure exacte de l'échéance"); without a `due_time`, the offset/time still apply. A reminder is sent once per due date/time: moving the task to another date or time, or its next occurrence starting, re-arms it. Rather than a per-item lead-time duration (the old, recurring-only `recurrence_lead_minutes` model this superseded), an item's reminder can be left to inherit the caller's own global default (`GET`/`PATCH /api/v1/me`'s `reminder_default_offset_days`/`reminder_default_time`) or given its own explicit override, matching the three presets Google Tasks itself offers ("le jour même", "la veille", "personnalisé").

Omitting `reminder_offset_days` (or sending it as `null`) and/or `reminder_time` (or sending it as `""`) on a request that also sets `reminder_enabled: true` resolves that field from the **caller's own current** `reminder_default_offset_days`/`reminder_default_time` **at the moment the request is processed** — this happens once, at write time, not as a live, continuously-updated reference: a later change to your global default only affects tasks you create or edit afterward, not retroactively. This is deliberate, not an oversight — a shared list's task can be visible to several users with different personal defaults, so there is no single well-defined "notify every recipient at their own preferred time" moment; resolving once keeps the reminder a single, unambiguous instant that every recipient with access to the list is notified at together (see [Push notifications](#push-notifications)).

```bash
# use my own current default (e.g. "le jour même à 09h00")
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"reminder_enabled": true}'

# override just this task: 3 days before, at 18:30
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"reminder_enabled": true, "reminder_offset_days": 3, "reminder_time": "18:30"}'

# remind at the task's own due time
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"reminder_enabled": true, "reminder_at_due_time": true, "due_time": "18:00"}'

# turn the reminder off for this task
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"reminder_enabled": false}'
```

### Urgent items

`is_urgent` (`POST`, `PUT`, or `PATCH`) flags an item as needing attention right away — it carries no other behavior server-side beyond being stored and returned as-is. The frontend uses it to sort an unfinished urgent item to the top of its list and to surface it, across every list in the current house, in the dashboard's "Achats & Tâches Urgentes" tab (`static/js/urgent.js`).

```bash
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"is_urgent": true}'
```

### Labels

`labels` (`POST`, `PUT`, or `PATCH`) is a freeform, user-managed set of short tags (e.g. `"Bio"`, `"Promo"`) attached via the label management bottom sheet (`static/js/list_view.js`'s `openLabelManageSheet`) — independent of every other item field, the same "plain, unrelated toggle" relationship `is_urgent` already has to the rest of the item. Each label is cleaned server-side (`internal/validate.Labels`): trimmed, control characters stripped, empties dropped, and case-insensitive duplicates removed (keeping the first occurrence's own casing) — `["  Bio  ", "bio", "Promo"]` is stored as `["Bio", "Promo"]`. `400` if a cleaned label exceeds 30 characters, or if the cleaned set exceeds 20 labels.

On `POST`/`PUT`, an omitted `labels` field means "no labels" (a fresh item, or a `PUT`'s full replace, both default to `[]`); on `PATCH`, an omitted `labels` field leaves the item's existing labels untouched — the same "absent = untouched" convention `target_month`/`recurrence_rule` already follow there — while an explicit `[]` clears them. `labels` is never `null` in a response, always at least `[]`.

The frontend renders an item's labels as small pastel chips under its title in the list detail view's "Détaillé" display mode, or as a compact colored-dot indicator next to the title in "Compact" mode — see [Compact/Detailed view](#compactdetailed-view) below.

```bash
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"labels": ["Bio", "Promo"]}'
```

### Compact/Detailed view

Purely a frontend display preference — there is no server-side field or endpoint behind it. Each list detail view has a density toggle (in the list header) switching between "Détaillé" (the full two-line card: checkbox, title, quantity stepper, price, label chips) and "Compact" (a single line: checkbox, title, a mini label indicator, and the `[⋮]` actions menu — quantity/price stay editable via the edit modal or actions sheet, just not shown on the row). The choice is persisted per list `type` in `localStorage` (`trakka:viewMode:<type>`), so switching a `shopping` list to Compact doesn't affect how a `todo` list renders.

### Price drop alerts

`target_price`/`alert_on_price_drop` (`POST`, `PUT`, or `PATCH`) let a user watch an individual item's own price and be notified once it reaches a threshold they set — distinct from the automatic deal-detection scan behind [`/api/v1/price-alerts`](#price-alerts), which compares an item's price against a *lower price found on its page*, not against a user-chosen number. The two are independent fields ("has a threshold" vs. "wants to be notified about it") rather than one implying the other, the same relationship `recurrence_rule`/`due_date` have to each other — a target price can be set before deciding whether to actually enable the alert for it.

After any request that changes an item's `price` (this endpoint, `PUT`, an in-time scraper result on `POST`/`PUT`/`PATCH`, the background scraper filling in a still-missing price later, or accepting a [price alert](#price-alerts)) or its `target_price`/`alert_on_price_drop`, the server checks whether `alert_on_price_drop` is true, `target_price` is set, and `price <= target_price` — and, only on the **first** request where that becomes true (not on every subsequent read or unrelated edit while it stays true), two things happen: the response carries `price_alert_triggered: true` (a transient, response-only field, the `target_price` counterpart to `price_status` — never persisted, never present on a plain `GET`) so the frontend can show an immediate in-app toast, and every user with access to the item's list gets a push notification (see [Push notifications](#push-notifications)) — regardless of whether this tab is even open, since a price drop found by the background scraper happens after the request that triggered it has already responded.

```bash
# watch this item and be notified once its price drops to 15€ or below
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"target_price": 15, "alert_on_price_drop": true}'

# a later price update that crosses the threshold reports it in the response
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"price": 12.99}'
# => {"id": 1, ..., "price": 12.99, "target_price": 15, "alert_on_price_drop": true, "price_alert_triggered": true}
```

The push goes to every user with access to the item's list who hasn't turned price alerts off (`price_alerts_enabled`, see [Price tracking](#price-tracking)); the item's own threshold is already the opt-in, so the per-direction switches don't apply to it.

In addition to the request-time paths above, a periodic background worker (`internal/handlers.RunTargetPriceScan`, every `SCRAPE_INTERVAL`, default `12h`; see [docs/DEPLOYMENT.md](DEPLOYMENT.md)) independently re-checks — with the same per-item check as the [price tracking](#price-tracking) scan — every not-done item that has a `url`, `alert_on_price_drop: true`, and a `target_price` set (`db.ListItemsForTargetPriceScan`), regardless of whether anyone has touched it recently — this is what catches a price drop on a tracked item nobody happens to edit or re-open. It waits 5 seconds between items (a fixed, not-yet-configurable delay) specifically to avoid hammering a merchant's site with a burst of near-simultaneous requests from this server's IP. Applying a newly-scraped price is a compare-and-swap against the price/url the scan read at the start of that item's check (`db.UpdateItemPriceFromScan`), so a user's own concurrent edit — or a different scan resolving the same item first — is never clobbered; a lost race is silently skipped, the same "not an error" contract every other scraper-driven path in this app follows. A price change that's actually applied runs through the exact same false→true threshold check described above, so it triggers a push notification the same way a manual edit does — there is just no HTTP response for this path to attach an in-app toast to.

See [docs/DOC_TEST_PRICE_ALERTS.md](DOC_TEST_PRICE_ALERTS.md) for a step-by-step manual QA recipe covering this feature end to end (badge, toast, push, and the scraper-driven path).

### Quick shopping lists (Courses)

Everything here applies to lists of type `groceries` ("Courses") only; other list types behave as before. Server side: `internal/handlers/products.go` (with `internal/catalog` for name matching, aisles and buying rhythms). UI: `static/js/quick-shop.js`.

**No duplicates.** `POST /api/v1/items` first looks for the same product on the list (same `internal/catalog.Key`: lowercased, accents and ligatures folded, punctuation and repeated spaces collapsed — no stemming, so `"pomme"` and `"pommes"` stay two products). If it's there:

- still active: its `quantity` grows by the requested `quantity` (capped at 100000);
- already checked off (`done: true`): it comes back unchecked (`done: false`, any `next_due_date` cleared), with the requested `quantity` — a new need, not added to what was already bought;
- in both cases `aisle` is filled in only if the item had none (an explicit `aisle` in the request replaces it), `is_urgent: true` in the request makes it urgent (never the reverse), and every other field of the item (labels, reminder, url, price…) is left as it was.

The answer is that item, with its own `id`, `200` and `"merged": true`. When several copies already exist (lists from before this rule), an active one is preferred over a done one, then the most recently updated. Concurrent additions of the same product are serialized server-side, so two members adding "Pain" at once end with one item of quantity 2. The PWA applies the same rule locally first (a `PATCH` of the existing item, which also works offline); the server-side merge covers every other client, other devices, and requests replayed from the offline queue.

**Aisles.** A new item gets, in order: the `aisle` of the request; the household's usual aisle for that product (the [catalog](#house-product-catalog)); `aisle_hint`; a keyword guess from the name (`internal/catalog.GuessAisle`: `"Tomates"` → `produce`, `"Yaourt à la fraise"` → `dairy`, `"Jus d'orange"` → `drinks`…); or none. Setting an item's `aisle` through `PUT`/`PATCH` makes it the household's usual aisle for that product, so the next addition lands there. In the PWA a Courses list opens sorted "Par rayon" — grouped by aisle in shop-walk order (fresh produce first, frozen food last) — unless another order was picked for it; in any other order each item shows its aisle as a small tag, which (like the "Rayon" row of the item's ⋮ menu) opens a picker to change it.

**History.** Every addition (and every check-off, which counts as a purchase with its date) is recorded in the House's [product catalog](#house-product-catalog), whoever made it — a share recipient included — and feeds autocomplete, favorites and "time to buy it again" suggestions.

### Task assignments

Any item can be assigned to one or more people — the PWA offers it on task (`todo`) lists. Writes take `assignee_ids`, reads return `assignees`:

```bash
# assign "Sortir les poubelles" to Thomas (id 2) and Sarah (id 3)
curl -X PATCH http://localhost:8080/api/v1/items/7 -d '{"assignee_ids": [2, 3]}'
# => {..., "assignees": [{"user_id": 2, "display_name": "Thomas"}, {"user_id": 3, "display_name": "Sarah"}]}

# unassign it
curl -X PATCH http://localhost:8080/api/v1/items/7 -d '{"assignee_ids": []}'
```

Who can be assigned, and who may assign, depends on the deployment mode (`SAAS_MODE`, [`GET /api/v1/config`](#get-apiv1config)'s `saas_mode`):

| | Self-hosted (`saas_mode: false`, default) | SaaS (`saas_mode: true`) |
|---|---|---|
| **Who can be assigned** (the list's *assignable users*) | everyone who can open the list: the members of its House, the recipients of a [share](#sharing) of the list (`read` or `write`), and those of a share of the Space it is filed in | the members of the list's House only |
| **Who may send `assignee_ids`** | anyone with `write` access to the list (House member, or `write` List/Space share) | a member of the list's House only (`403` `"only members of the list's house can assign tasks"` otherwise) — a share recipient sees the assignees and can edit the item's other fields, not change them |

A self-hosted household often keeps personal lists ("Homelab") out of a shared House and shares a Space ("Maison") or a single list instead — the wider rule lets it split those chores without merging Houses. A SaaS instance's accounts don't know each other, so the House stays the boundary there.

- **Validation**: every id must be one of the list's assignable users (`400` `"assignees must be among the list's assignable users"`) — never someone with no access, nor a pending invitation (no account yet). Ids must be positive (`400`), at most 50 people, duplicates ignored. Checked before the request writes anything, so a refused assignment leaves the whole request unapplied. Re-sending an item's current assignees, in any order, changes nothing and needs neither check.
- **`assignees`**: `{user_id, display_name}` in the order they were assigned (an edit keeps the order of the people who stay; newcomers go last). `display_name` is the account's, possibly `""` — no email, since everyone who can read the list reads the item too; [`GET /api/v1/lists/{id}/assignable-users`](#get-apiv1listsidassignable-users) has it.
- **`POST`**: `assignee_ids` assigns the new item. On a Courses list, a product already there is [merged](#quick-shopping-lists-courses) and keeps its own assignees, like its labels.
- **`PATCH`**: absent leaves the assignees as they are (a check-off doesn't touch them); present — `[]` included — replaces them.
- **`PUT`**: the one field a full replace leaves as is when absent, since not everyone who may edit the item may set it; present, it replaces them.
- **Recurring tasks keep their assignees**: a checked-off recurring item is the same row when its next occurrence comes back (see [Recurring items](#recurring-items)), so that occurrence is assigned to the same people — or to whoever it was reassigned to meanwhile.
- **Losing access unassigns**: whoever falls out of a list's assignable users is unassigned from its items right away — on [leaving a House](#delete-apiv1housesidmembersuserid) (self-hosted, not from a list they can still open through a share), a [revoked share](#delete-apiv1custom-categoriesidshareuserid--delete-apiv1listsidshareuserid), a list [moved to another Space](#put-apiv1listsid) or a [deleted Space](#delete-apiv1custom-categoriesid); deleting their account does too. The server also runs this clean-up at startup, so switching `SAAS_MODE` to `true` unassigns the share recipients assigned under the self-hosted rule. Each item it changes gets a new `updated_at`.

#### `GET /api/v1/lists/{id}/assignable-users`

Who the list's items can be assigned to, for the assignee pickers, and whether the caller may assign them:

```json
{
  "can_assign": true,
  "users": [
    { "user_id": 1, "display_name": "Camille", "email": "camille@example.com" },
    { "user_id": 4, "display_name": "Lucas", "email": "lucas@example.com" }
  ]
}
```

`users` follows the table above, sorted by display name then email (`[]`, never `null`). `can_assign` is `true` when the caller may send `assignee_ids` on this list's items. Anyone who can read the list may ask (`403` otherwise, `404` if it doesn't exist); in SaaS mode, a caller outside the list's House gets `{"can_assign": false, "users": []}` — the House roster stays the House's own. Self-hosted, a `read`-only recipient sees the users with `can_assign: false`. Like every `GET`, mirrored by the service worker for offline use.

### Purchase cycle & budget

On a purchase list (`shopping`, `groceries`, `recurring_shopping`) an item goes through three states, reported as `purchase_status`:

| `purchase_status` | Meaning | Stored as |
|---|---|---|
| `planned` ("Prévu") | added while planning or estimating, not yet confirmed as needed — every item of a list created from a [template](#list-templates) starts here | `done = 0`, `is_planned = 1` |
| `to_buy` ("À acheter") | confirmed for the shopping run — the default for a new item | `done = 0`, `is_planned = 0` |
| `bought` ("Acheté") | checked off in the shop | `done = 1` |

`done` stays the source of truth for "bought" (recurrence, reminders, the offline queue all read it); `purchase_status` is derived from it and from `is_planned` on every read, and accepted on writes as a way to set both:

- **`POST`**: `planned` or `to_buy`. On a Courses list, adding a product already there brings it back `to_buy` whatever the request says (adding a product is wanting it).
- **`PATCH`**: `purchase_status` sets `done` itself (`bought` ⇔ `done: true`); sent together with `done`, the two must agree, else `400`. `done: true` alone makes an item `bought`; `done: false` alone leaves a not-bought item planned or to buy as it was, and brings a bought one back `to_buy`.
- **`PUT`** (full replace): `purchase_status`, when sent, decides `done` (`done` can't be told absent from `false` here); omitted, the item is `bought` or `to_buy` per `done`.
- Buying an item clears `is_planned`, so a bought item unchecked later comes back `to_buy`.

**The price actually paid**, `actual_price`, is a unit price like `price`, which stays the estimate it is compared with in the list's [`budget`](#get-apiv1lists). It can only be set on an item that is bought — in the same request or before — else `400` (`"actual_price can only be set on a bought item"`); `null` clears it (the purchase then counts at its estimate); negative is `400`. Taking an item out of `bought` (`PATCH`/`PUT`, a Courses re-add, a recurring item's next occurrence) clears it: the next purchase is a new one. `PUT` clears it when omitted. `POST` doesn't accept it.

```bash
# confirm a planned item
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"purchase_status": "to_buy"}'

# check it off at the price actually paid: 34.50 a unit instead of the estimated 30
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"done": true, "actual_price": 34.5}'
# => {..., "price": 30, "done": true, "purchase_status": "bought", "actual_price": 34.5}

# it cost the estimate after all
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"actual_price": null}'
```

The PWA offers to enter the price paid right when an item is checked off ("Prix payé" in the check-off toast), and from the item's actions sheet and edit modal.

### `GET /api/v1/items/{id}`

`200` with the item, `404` if not found, `403` unless the caller has at least read [access](#sharing) to the item's list.

### `PUT /api/v1/items/{id}`

Full replace — every field below is required except `url`, `quantity`, `price`, `done`, `position`, `target_month`, `due_date`, `due_time`, `recurrence_rule`, `recurrence_end_date`, `is_urgent`, `recurrence_lead_minutes`, `target_price`, `alert_on_price_drop`, `labels`, `reminder_enabled`, `reminder_offset_days`, `reminder_time`, `reminder_at_due_time`, `aisle`, `purchase_status`, `actual_price` fall back to their zero/default value if omitted — and `assignee_ids`, which is left as is when omitted (see [Task assignments](#task-assignments)) (note: unlike `PATCH`, omitting a field here **resets** it, since this is a full replace — an omitted `price` clears any previously recorded price, an omitted `target_month` unschedules the item, an omitted `recurrence_rule` turns off recurrence, an omitted `is_urgent` clears the urgent flag, an omitted `recurrence_lead_minutes` reverts to the instance-wide default lead time, an omitted `target_price`/`alert_on_price_drop` clears the price-drop threshold and turns the alert off, an omitted `labels` clears the label set back to `[]`, an omitted `aisle` clears the aisle, and an omitted `reminder_enabled` turns the reminder off — same as every other boolean field here — clearing `reminder_offset_days`/`reminder_time` along with it). `list_id` cannot be changed via this endpoint.

```bash
curl -X PUT http://localhost:8080/api/v1/items/1 \
  -d '{"title": "Lait", "url": "https://example.com/lait", "quantity": 2, "price": 1.85, "done": true, "position": 0, "target_month": "2026-11", "recurrence_rule": "WEEKLY"}'
```

`404` if not found. `400` if `price`/`target_price` is negative, `target_month` isn't `YYYY-MM`, `due_date`/`recurrence_end_date` isn't `YYYY-MM-DD`, `due_time` isn't `HH:MM` or comes without a `due_date`, or `recurrence_rule` isn't a recognized form. `403` unless the caller has **write** [access](#sharing) to the item's list. `price_auto` is reset to `false` by this endpoint (a full replace is an explicit, manual value, even when `price` is omitted) — except when `price` is the item's current scraper-found price, re-sent unchanged with an unchanged `url`, which keeps `price_auto: true` so the price keeps [following its page](#price-tracking). If `url` changes to something new, `image_url` is cleared (a scraped image is tied to the `url` it was found on) and the same bounded lookup as `POST` above kicks off for whichever of `price`/`image_url` is still missing, with the same `price_status` values in the response. If `done` changes on a recurring item (`recurrence_rule` set, after this request's changes are applied), see "Recurring items" above for how `next_due_date` follows. See [Price drop alerts](#price-drop-alerts) for `target_price`/`alert_on_price_drop`/`price_alert_triggered`.

### `PATCH /api/v1/items/{id}`

Partial update — only send the fields you want to change. This is the endpoint to use for e.g. checking an item off a list, or moving a planned purchase to a different month:

```bash
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"done": true}'
```

All fields (`title`, `url`, `quantity`, `price`, `done`, `position`, `target_month`, `due_date`, `due_time`, `recurrence_rule`, `recurrence_end_date`, `is_urgent`, `recurrence_lead_minutes`, `target_price`, `alert_on_price_drop`, `labels`, `reminder_enabled`, `reminder_offset_days`, `reminder_time`, `reminder_at_due_time`, `aisle`, `purchase_status`, `actual_price`, `assignee_ids`) are optional — see [Purchase cycle & budget](#purchase-cycle--budget) for `purchase_status`/`actual_price` and [Task assignments](#task-assignments) for `assignee_ids`. `aisle`, if given, must be one of the [aisles](#post-apiv1items) or an empty string (clears it); on a Courses list it also becomes the household's usual aisle for the product, and checking an item off there (`done` false → true) records a purchase in the [catalog](#house-product-catalog). `labels`, if given (including an explicit `[]`), replaces the item's whole label set — omitting it entirely leaves the existing labels untouched, the same "absent = untouched" convention as `target_month`/`recurrence_rule`; see [Labels](#labels) above for the cleaning/validation rules. `title`, if given, cannot be empty; `quantity`, if given, must be positive; `price`, if given, must be a non-negative number or `null` (unlike the other fields, `price` distinguishes "omitted" — leave unchanged — from an explicit `null` — clear the recorded price). Sending `price` (either a number or `null`) resets `price_auto` to `false`, since a `price` supplied in the request is a manual value — except the item's current scraper-found price re-sent unchanged with an unchanged `url` (the edit form re-sends the price with every save), which keeps `price_auto: true` so the price keeps [following its page](#price-tracking). `target_month`, if given, must be `YYYY-MM` or an empty string (clears it back to unscheduled) — omitting it entirely leaves the item's schedule untouched. `due_date`/`recurrence_end_date`, if given, must be `YYYY-MM-DD` or an empty string (clears them); clearing `due_date` also clears `due_time`. `due_time`, if given, must be `HH:MM` or an empty string (clears it); a non-empty `due_time` on an item left without a `due_date` is a `400`. `recurrence_rule`, if given, must be one of the recognized forms or an empty string — clearing it this way also clears `recurrence_end_date` (and any pending `next_due_date`), but keeps `due_date`/`due_time`. `recurrence_lead_minutes`, like `price`, distinguishes "omitted" (leave unchanged) from an explicit `null` (revert to the instance-wide default lead time) — a number must be non-negative. `target_price`, like `price`, distinguishes "omitted" (leave unchanged) from an explicit `null` (clear the threshold) — a number must be non-negative; `alert_on_price_drop`, if given, is a plain boolean. `reminder_enabled`, if given, is a plain boolean (`is_urgent`'s "omitted = unchanged" shape, not `price`'s three-way one — there's no "clear" state distinct from `false`); `reminder_offset_days`, like `recurrence_lead_minutes`, distinguishes "omitted" (leave unchanged) from an explicit `null` (resolve from the caller's own current `reminder_default_offset_days`) — a number must be non-negative; `reminder_time`, if given, must be `HH:MM` or an empty string (also resolves from the caller's own current `reminder_default_time`); `reminder_at_due_time`, like `reminder_offset_days`, distinguishes "omitted" (leave unchanged) from `null` (resolve from the caller's `reminder_default_at_due_time`) — see [Reminders](#reminders) above. There is no `image_url` field to set directly — it's scraper-only. If `url` changes to something new, `image_url` is cleared (same reasoning as `PUT` above) and the bounded lookup described under `POST` kicks off for whichever of `price`/`image_url` is still missing, with the same `price_status` values in the response; an unrelated `PATCH` (e.g. `{"done": true}`) never re-triggers it, since `url` isn't changing. If `done` changes on a recurring item, see "Recurring items" above — the response carries the resulting `next_due_date`. See [Price drop alerts](#price-drop-alerts) for `target_price`/`alert_on_price_drop`/`price_alert_triggered`. `404` if not found, `403` unless the caller has **write** [access](#sharing) to the item's list.

```bash
# clear a previously recorded price without touching anything else
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"price": null}'

# reschedule a planned purchase to a different month
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"target_month": "2027-01"}'

# turn off recurrence entirely (the due date stays)
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"recurrence_rule": ""}'
```

### `DELETE /api/v1/items/{id}`

`204` on success, `404` if not found, `403` unless the caller has **write** [access](#sharing) to the item's list.

### `POST /api/v1/items/{id}/price-check`

Runs the [price tracking](#price-tracking) check for this one item right away — the on-demand counterpart to the periodic scans, behind the "Vérifier le prix maintenant" item action (the item's actions sheet, and a 🔄 button next to ✏️/🗑️ on wide screens) — as a **manual** check: the item's own page is fetched and, if it shows a price different from the item's, that price is applied, **even a typed-in one** (the user asked for the page's price; the item follows its page from then on, `price_auto: true`). With `DEAL_SEARCH_ENABLED`, Dealabs is searched too. `400` if the item has no `url`. `404` if not found, `403` unless the caller has **write** [access](#sharing) to the item's list. Otherwise `200`:

```json
{"item": {...}, "alert": null,
 "check": {"status": "ok", "observed_price": 419}}
```

- `item` — the item as it stands after the check (`price`, `previous_price`, `price_changed_at` updated if the page moved it).
- `alert` — the item's pending price alert (freshly created, or one from an earlier check still pending), or `null`.
- `check` — what the check read on the page, so an unread page is never mistaken for an unchanged price: `status` is `ok` (a price was read: `observed_price`), `unreachable` (the page couldn't be read: `error`, e.g. `"unexpected status 403"`), `no_price` (read, but no price on it) or `deal_expired` (a Dealabs deal that has expired). Only `ok` can change the item.

Bounded by 10s for the page plus 8s for the deal search. With `LOG_LEVEL=debug`, every step is logged (see [docs/DEPLOYMENT.md](DEPLOYMENT.md)).

```bash
curl -X POST http://localhost:8080/api/v1/items/1/price-check
```

### `GET /api/v1/items/{id}/price-history`

The prices observed on the item's own page over time, oldest first (last 100): `[{"price": 499, "recorded_at": "..."}, ...]`. A price is recorded when the item's price is first scraped and whenever a later check sees a different one — see [Price tracking](#price-tracking). `404` if not found, `403` unless the caller has at least read [access](#sharing) to the item's list.

## Price tracking

Every not-done item with a `url` (outside `custom` lists) is re-checked in the background, whether or not push notifications are configured or enabled — turning notifications off never stops price checks. Two scans share one per-item check (`internal/handlers.trackItemPrice`) on disjoint sets of items: items with an active [target price](#price-drop-alerts) every `SCRAPE_INTERVAL` (default `12h`), every other tracked item every `PRICE_CHECK_INTERVAL_HOURS` (default `24`), 5 seconds apart (see [docs/DEPLOYMENT.md](DEPLOYMENT.md)). For each item:

1. **Its own page** is fetched — with `Cache-Control: no-cache`/`Pragma: no-cache`, so a CDN or proxy in front of the site serves its current copy (this server keeps no cache of its own). The price shown there is recorded in the item's [price history](#get-apiv1itemsidprice-history) when it differs from the last price recorded there (the history is what the page showed, compared with the page's previous reading — the item is compared with its own price, step 2). That page may be a Dealabs deal the user accepted (see [Price alerts](#price-alerts)): its price is then read from the deal itself, and once the deal has expired it is no longer followed — the item's list is told once (`expired`, below) that the item needs a new link.
2. If the item's price **follows its page** — it has none yet, it was found by the scraper (`price_auto: true`), or the item has an active target price — a different price is applied. When it replaces an earlier price, the item records the movement: `previous_price` (the price before) and `price_changed_at` (when, UTC) — the list view colours a drop green and an increase red for 7 days (unless the user turned `price_change_indicators_enabled` off). A manual `price` or `url` edit clears both. The change is announced as a `drop` or an `increase` (below), and the target price re-checked.
   In a background scan, a price the user **typed in** is not overwritten: a lower one on the page is proposed as a [price alert](#price-alerts) instead. A [manual check](#post-apiv1itemsidprice-check) applies the page's price whatever it was, and the item follows its page from then on.
3. With `DEAL_SEARCH_ENABLED` (default on), the item's **title is searched on Dealabs** (www.dealabs.com) — only when it names a specific product: at least two words one of which has a digit (a model number: "Sony WH-1000XM5") or at least three words. Of the active, non-local deals whose title contains every word of the query, the cheapest one below the item's price — but not below half of it, which would almost always be an accessory or a different product — is proposed as a price alert, linking to the deal. Idealo, LeDenicheur and Google Shopping aren't searched: the first two block automated requests (and LeDenicheur's robots.txt disallows its search), the last has no public search API.

A price alert is proposed once: not while the item already has a pending one, and never again for the same price at the same place once accepted or rejected. Accepting one switches the item to it: price **and** url (see [Price alerts](#price-alerts)).

**Who is told**: every user with access to the item's list, according to their own Paramètres → "Alertes de prix" (`PATCH /api/v1/me`): `price_drop_alerts_enabled` (on by default) for a `drop` and for a better price proposed (`deal`), `price_increase_alerts_enabled` (off by default) for an `increase`, and `price_alerts_enabled` — "Désactiver les alertes de prix" when off — over all of them, over an `expired` deal and over target prices. Every user with access to the list gets an entry in their **in-app inbox** (below), shown in the 🔔 drawer, whatever their settings — the inbox applies each user's *current* settings when it is read, so it always agrees with them and with the list's ▲/▼ indicators (turning `price_increase_alerts_enabled` on shows the increases already detected, not only the next ones). Those who want that kind of alert at the time also get a push notification on whichever devices are subscribed. The inbox entry is what decouples alerts from push: with push off, never set up, or not configured on the instance, the alert is waiting the next time Trakka is opened. A scan move that reaches a target price pushes only the target price's "Bonne affaire", not a second notification about the same drop (the drop is still in the inbox).

### `GET /api/v1/price-notifications`

The caller's own inbox, newest first, read or not (last 50) — only the kinds they currently want (`price_alerts_enabled`, then `price_drop_alerts_enabled` for `drop`/`deal` or `price_increase_alerts_enabled` for `increase`; `expired` under the master switch alone), and only for items on lists they can still access. Read entries are deleted after 30 days, unread ones after 90.

```json
[{"id": 3, "item_id": 1, "item_title": "Console Nintendo Switch 2", "list_id": 1,
  "item_url": "https://www.example-shop.fr/console-switch-2", "kind": "deal",
  "old_price": 499, "new_price": 419, "source_url": "https://www.dealabs.com/bons-plans/console-nintendo-switch-2-3420521",
  "created_at": "2026-10-06T08:17:49.024Z"}]
```

`kind` is `drop`/`increase` (the item's price moved from `old_price` to `new_price`), `deal` (`new_price` was found at `source_url` and waits as a pending price alert) or `expired` (the Dealabs deal at `source_url` the item follows has expired; `old_price` = `new_price` = its price). `read_at` is present once read. `item_title`, `list_id` and `item_url` are joined in from the item at read time: `item_url` is the item's own link as it stands now, omitted when it has none — a `drop`/`increase` carries no `source_url`, so it's what the 🔔 drawer opens when the entry is tapped (otherwise the item in its list, through `/?list={list_id}&item={item_id}`).

### `POST /api/v1/price-notifications/read`

Marks the caller's own entries read: `{"ids": [3, 4]}` (at most 200), or `{}` for all of them. An id that isn't the caller's is ignored. `204`.

### `DELETE /api/v1/price-notifications/{id}`

Removes one of the caller's own entries from their inbox (the ✕ on an entry in the 🔔 drawer): it is no longer listed, and counts as read. Always `204` — an id that isn't the caller's, or was already removed, is ignored rather than reported, so the answer reveals nothing about other users' entries and a replayed request (the offline queue) is harmless. `400` for a non-numeric id.

```bash
curl -X DELETE -b cookies.txt http://localhost:8080/api/v1/price-notifications/3
```

### `DELETE /api/v1/price-notifications`

Removes every entry of the caller's own inbox ("Tout effacer"). Other users' inboxes are untouched, and so are the house's pending [price alerts](#price-alerts), which are resolved with `PATCH /api/v1/price-alerts/{id}` (`rejected` dismisses one). `204`.

Removal is a soft delete (`price_notifications.dismissed_at`, see [DATABASE.md](DATABASE.md#price_notifications)): the row stays until the usual read-entry retention deletes it, so an `expired` deal that was announced and then removed is still not announced again.

## Price alerts

A price alert records a lower price found for an item than its current `price` — on its own page (when the price was typed in, see [Price tracking](#price-tracking)) or on Dealabs — created by the background scans or by `POST /api/v1/items/{id}/price-check` above. Every alert starts `pending` and is resolved exactly once, either `accepted` or `rejected` (dismissed, item untouched) — see `internal/db.AcceptPriceAlert`/`RejectPriceAlert`. **Accepting switches the item to the alert's source**: its `price` becomes `found_price` **and** its `url` becomes `source_url`, and the price follows that page from then on (`price_auto: true`) — the next checks track the price the user chose, at the place they chose it. For a lower price on the item's own page the url doesn't change. For a Dealabs deal the url becomes the deal page: Dealabs only exposes the merchant's own link through a `/visit/` redirect its robots.txt disallows, so the deal page is the one link this server can follow — its price is read from the deal, and when the deal expires the list is told (see [Price tracking](#price-tracking)). Accepting also clears `previous_price`/`price_changed_at` (the user's choice isn't a movement at the store), keeps `image_url` (same product), and starts the new url's price history with `found_price`. A `source_url` that doesn't pass the server's url validation is refused with `409` and the item is left untouched. Each alert carries `changes_url` (read-only): whether accepting would replace the item's url, which the 🔔 drawer words as "Appliquer ce prix et ce lien".

### `GET /api/v1/price-alerts?house_id={id}`

`house_id` is required. `?status=` optionally restricts the result to one of `pending`/`accepted`/`rejected` (the notification bell always passes `status=pending`); omitted returns every status, newest first. `400` if `house_id` is missing/invalid or `status` isn't a recognized value. `403` if the caller isn't a member of the house.

```bash
curl -b cookies.txt "http://localhost:8080/api/v1/price-alerts?house_id=1&status=pending"
```

Each alert includes `item_title`, `list_id` and `changes_url` (joined in from the item at read time, never writable) alongside `item_id`, `original_price` (a snapshot of the item's price when the alert was created), `found_price`, `source_url`, `status`, and `created_at`.

### `PATCH /api/v1/price-alerts/{id}`

```bash
# apply the lower price found
curl -X PATCH http://localhost:8080/api/v1/price-alerts/1 -d '{"status": "accepted"}'

# dismiss it instead
curl -X PATCH http://localhost:8080/api/v1/price-alerts/1 -d '{"status": "rejected"}'
```

`status` is required and must be `"accepted"` or `"rejected"`. `404` if not found, `403` unless the caller has **write** [access](#sharing) to the alert's item's list, `409` if its `source_url` isn't a valid http(s) url (on accept) or if the alert was already resolved (an alert can only ever be actioned once, whichever status wins the race). `200` with the updated alert otherwise.

## Push notifications

Web Push (VAPID, RFC 8292/8291/8030 — see `internal/webpush` in [CLAUDE.md](../CLAUDE.md)) delivers two kinds of notification, both opted into via the same browser subscription: (1) when another member/recipient of a shared list adds or checks off an item, every *other* user with access to that list is notified; (2) for any item with a `due_date` and an enabled reminder — recurring or not, see [Reminders](#reminders) above — every user with access to its list is notified once the current time reaches that item's own reminder moment (`due_date` minus `reminder_offset_days`, at `reminder_time` — or `due_date` at `due_time` in the `reminder_at_due_time` mode — interpreted in the instance's `APP_TIMEZONE`, default `Europe/Paris`, see [docs/DEPLOYMENT.md](DEPLOYMENT.md)). A reminder's payload carries a per-item `tag`, so reminders for two tasks of the same list don't replace each other on the device. Requires `VAPID_PUBLIC_KEY`/`VAPID_PRIVATE_KEY`/`VAPID_SUBJECT` to be configured (see [docs/DEPLOYMENT.md](DEPLOYMENT.md)) — `trakka -generate-vapid-keys` prints a fresh key pair. Notification text is always composed in French server-side (there is no per-user language preference stored server-side to localize it with — see CLAUDE.md's "UI language" convention).

### `GET /api/v1/push/vapid-public-key`

`200` always (never a `404`) with `{"enabled": false}` if push isn't configured on this instance, or `{"enabled": true, "public_key": "..."}` otherwise — the base64url-encoded VAPID public key a browser's `PushManager.subscribe()` needs as `applicationServerKey`.

```bash
curl -b cookies.txt http://localhost:8080/api/v1/push/vapid-public-key
```

### `POST /api/v1/push/subscribe`

Registers (or refreshes) a Web Push subscription for the calling user, in the shape a browser's `PushSubscription.toJSON()` produces:

```bash
curl -X POST http://localhost:8080/api/v1/push/subscribe \
  -d '{"endpoint": "https://fcm.googleapis.com/fcm/send/...", "keys": {"p256dh": "...", "auth": "..."}}'
```

`503` if push isn't configured on this instance. `400` if `endpoint` is missing, isn't an `https://` URL, or `keys.p256dh`/`keys.auth` are missing. `201` with the stored subscription (`id`, `endpoint`, `created_at` — the keys themselves are never echoed back). Upserts on `(user, endpoint)`, so re-subscribing the same endpoint (a browser key rotation, or simply re-enabling push) refreshes the stored keys rather than erroring.

### `DELETE /api/v1/push/subscribe`

```bash
curl -X DELETE http://localhost:8080/api/v1/push/subscribe -d '{"endpoint": "https://fcm.googleapis.com/fcm/send/..."}'
```

`204` whether or not that endpoint was actually subscribed — idempotent by design, since "this endpoint is not subscribed" is the desired end state either way.

### `POST /api/v1/push/test`

Sends one push notification to every subscription registered on the calling user's own account — a diagnostic to confirm VAPID config, subscription, and delivery all actually work end to end, without waiting for a real list change or a due date. See [docs/DOC_PUSH_NOTIFICATIONS.md](DOC_PUSH_NOTIFICATIONS.md#3-vérification-de-bout-en-bout) for the full walkthrough.

```bash
curl -X POST -b cookies.txt http://localhost:8080/api/v1/push/test
```

`200 {"sent_to_subscriptions": N}` on success (delivery itself is still best-effort — a `200` means the attempt was made to `N` subscriptions, not that a device necessarily displayed it). `503` if push isn't configured on this instance; `404` if the calling user has no push subscription registered at all (enable the toggle in Paramètres first).

### `GET /api/v1/reminders/upcoming`

The calling user's task reminders still to come, for the Android app, which schedules them on the phone as local notifications because its WebView has no Web Push ([MOBILE_BUILD.md](MOBILE_BUILD.md#task-reminders-local-notifications)). Independent of push: works without VAPID keys.

Returns the reminders the push scan would send this user: those of every not-done task with an active reminder on a list the user can access (House membership, list share, or Space share), plus the next occurrence of each checked-off recurring task (`next_due_date`), since that task only comes back at its reminder moment at the latest. `remind_at` (UTC) is computed exactly as for the push ([Reminders](#reminders)), in the instance's `APP_TIMEZONE`; `title`, `body` and `url` are the push's. Only reminders in the next 60 days, at most 200, earliest first; past ones are left out. `reminders` is empty when the user turned task reminders off (`reminders_enabled`, [`GET /api/v1/me`](#get-apiv1me)).

`overdue_summary` is the user's next [overdue-tasks summary](#get-apiv1me) still to come, or `null` (turned off, or nothing to list). Before today's summary time, it is today's, listing the tasks due before today; from then on, tomorrow's, listing those due today or earlier — the app asks again whenever a task changes, so a task checked off in the meantime drops out. `item_ids` lets the app cancel it once it has checked them all off, offline too; `title`/`body`/`url` are the push's (`url` opens the tasks' list when they all belong to one, else the dashboard).

```json
{
  "reminders": [
    { "item_id": 12, "list_id": 3, "title": "Arroser", "body": "🔔 Échéance demain — Corvées", "url": "/?list=3", "remind_at": "2026-10-05T07:00:00Z" }
  ],
  "overdue_summary": {
    "title": "Tâches en retard", "body": "2 tâches en retard ne sont pas cochées : Appeler le plombier, Payer le loyer",
    "url": "/?list=3", "remind_at": "2026-10-06T06:00:00Z", "item_ids": [9, 14]
  }
}
```

## Calendar feed

Each user can have one personal [iCalendar](https://www.rfc-editor.org/rfc/rfc5545) feed of their dated tasks, which calendar apps (Nextcloud, Google Calendar, Apple Calendar, Thunderbird) subscribe to by URL. User guide, client setup and limitations: [CALENDAR_EXPORT.md](CALENDAR_EXPORT.md). Implementation: `internal/handlers/calendar_feed.go`, `internal/ical`.

The feed's token is 32 random bytes (base64url, 43 characters), stored only as its SHA-256 hash ([`calendar_feed_tokens`](DATABASE.md#calendar_feed_tokens)), so it is returned exactly once, by the `POST` that creates it. The three management routes below need a session like the rest of the API, and their writes go through the usual cross-origin check.

### `GET /api/v1/calendar/feed-token`

Whether the calling user has a feed link. Never returns the token.

```json
{ "enabled": true, "created_at": "2026-10-03T17:19:58.665Z", "last_used_at": "2026-10-03T18:02:11.204Z" }
```

`last_used_at` is when a calendar app last fetched the feed, omitted if never. Without a link: `{"enabled": false}`.

### `POST /api/v1/calendar/feed-token`

Generates the calling user's feed token, **replacing any previous one**, which stops working immediately. No request body. `201`:

```json
{ "enabled": true, "token": "f1757f…", "created_at": "2026-10-03T17:19:58.665Z" }
```

The feed URL is `<origin>/api/v1/calendar/feed.ics?token=<token>` (the frontend also offers it as `webcal://…`).

### `DELETE /api/v1/calendar/feed-token`

Removes the calling user's feed token, so the link stops working. `204`, whether or not there was one.

### `GET /api/v1/calendar/feed.ics?token=<token>`

**No session needed**: the token is the credential. `200` with `Content-Type: text/calendar; charset=utf-8`, `Cache-Control: no-store`, and an iCalendar document; each fetch updates `last_used_at`. `404` (`{"error": "calendar feed not found"}`) for a missing, unknown, replaced or deleted token — deliberately not `401`, which makes Apple Calendar prompt for a password that can't work.

```bash
curl "http://localhost:8080/api/v1/calendar/feed.ics?token=f1757f…"
```

Contents: one `VEVENT` per task with a date on any list the token's owner can access (house membership, list share, Space share) — the `due_date` of a task still to do, or the `next_due_date` of a checked-off recurring task. Tasks without a date and done non-recurring tasks are left out.

| Task | Event |
|---|---|
| `due_date` only | All-day: `DTSTART;VALUE=DATE` and `DTEND` the next day |
| `due_date` + `due_time` | `DTSTART;TZID=<APP_TIMEZONE>` and `DTEND` 30 minutes later, with a `VTIMEZONE` generated from the zone's current rules (UTC `…Z` times when `APP_TIMEZONE` is UTC or its rules don't fit a two-transition `VTIMEZONE`) |
| `recurrence_rule` | `RRULE` with the stored rule as is (it is already an RFC 5545 subset), plus `UNTIL` from `recurrence_end_date`. Omitted for `FREQ=MONTHLY` on days 29–31 and `FREQ=YEARLY` on February 29, where Trakka's clamping to the month's last day differs from RFC 5545's skipping |
| Reminder enabled | `VALARM` with a `TRIGGER` relative to the event start (all-day events start at local midnight), so it applies to every occurrence: `-PT4H` for "la veille à 20:00", `PT9H` for "le jour même à 09:00", `PT0S` for "à l'heure de l'échéance" |

Every event also carries `UID` `trakka-item-<id>@<host>` (`BASE_URL`'s host, else the request's), `SUMMARY` (the title), `CATEGORIES` and `DESCRIPTION` (the list name), `URL` (`<BASE_URL>/?list=<id>`, only when `BASE_URL` is set), `LAST-MODIFIED` and `TRANSP:TRANSPARENT`. The calendar is named `Trakka — <display name>` and advertises a one-hour refresh (`REFRESH-INTERVAL`, `X-PUBLISHED-TTL`).

## API tokens

Personal tokens for integrations that call the API without a browser session — the [Home Assistant integration](HOME_ASSISTANT.md) first, or a script. Implementation: `internal/handlers/api_tokens.go`; table [`api_tokens`](DATABASE.md#api_tokens).

A token is `trk_` followed by 32 random bytes in base64url (47 characters). It is stored only as its SHA-256 hash, so it is returned exactly once, by the `POST` that creates it. Send it on every request:

```bash
curl -H "Authorization: Bearer trk_…" "http://localhost:8080/api/v1/lists?scope=all&include=items"
```

The request then runs as the token's user, with that user's access rules: a list shared read-only stays read-only (`403`), someone else's list stays out of reach (`403`). But a token opens **only these routes**, whatever its user may do with a session:

| Route | Use |
|---|---|
| `GET /api/v1/me`, `GET /api/v1/config` | who the token belongs to; the instance's time zone |
| `GET /api/v1/lists`, `GET /api/v1/lists/{id}` | the lists and their items (`?scope=all&include=items`, `?name=`) |
| `PUT /api/v1/lists/{id}/reorder` | reorder a list's items |
| `GET`/`POST /api/v1/items`, `GET`/`PUT`/`PATCH`/`DELETE /api/v1/items/{id}` | read, add, change, check off and delete items (`?title=`) |

Every other `/api/v1/...` route answers a token with `403 {"error": "this endpoint is not available to API tokens"}` — the account, houses, sharing, admin, push, the calendar feed and the token routes below (a leaked token can't create another). An unknown or revoked token is a `401 {"error": "invalid API token"}` with `WWW-Authenticate: Bearer realm="trakka", error="invalid_token"`, even if a valid session cookie comes along. An `Authorization` header that isn't `Bearer trk_…` (a reverse proxy's own `Basic` login, or the `id_token` oauth2-proxy forwards) is ignored, and the session cookie decides as usual.

Token requests need no CSRF precaution (a browser never attaches an `Authorization` header by itself); the usual cross-origin check still applies to writes, and lets through clients that send no `Origin`, like Home Assistant. Each use records `last_used_at`, at most once a minute.

The three management routes need a session, like the rest of the API; their writes go through the usual cross-origin check. The PWA offers them in Paramètres → Intégrations → Jetons d'API.

### `GET /api/v1/api-tokens`

The calling user's tokens, oldest first. Never returns the tokens themselves.

```json
[
  { "id": 3, "name": "Home Assistant", "created_at": "2026-10-16T12:13:08.541Z", "last_used_at": "2026-10-16T12:14:02.118Z" }
]
```

`last_used_at` is `null` until the token is first used.

### `POST /api/v1/api-tokens`

Creates a token. Body: `{"name": "Home Assistant"}` — required, at most 200 characters, so the user can tell tokens apart. `201`:

```json
{ "id": 3, "name": "Home Assistant", "token": "trk_0vrV…", "created_at": "2026-10-16T12:13:08.541Z", "last_used_at": null }
```

`400` for a missing or too long name; `409` once the user holds 20 tokens.

### `DELETE /api/v1/api-tokens/{id}`

Revokes a token: it stops working at once. `204`; `404` if the calling user has no such token (including another user's).

## Admin settings

Every endpoint below is gated behind `models.User.IsAdmin` (`403 {"error": "admin access required"}` for anyone else) rather than house membership — these are system-wide, not scoped to a house. The frontend surfaces all of them as a "Console d'Administration" panel, reachable from a button inside the ordinary "Paramètres" modal (`#user-settings-modal`) that's only shown to admins (`static/js/admin.js`) — there is no separate header-level admin button; see CLAUDE.md's session-handoff log for the navigation restructuring session that moved it there.

The instance-settings endpoints (`GET`/`PATCH /api/v1/admin/settings`) manage the `system_settings` table (see [docs/DATABASE.md](DATABASE.md#system_settings)), which takes priority over the equivalent environment variable whenever a row exists (`internal/settings.Resolve`); a value with no row falls back to its env var.

### `GET /api/v1/admin/settings`

```bash
curl -b cookies.txt http://localhost:8080/api/v1/admin/settings
```

```json
{
  "instance_name": "Trakka",
  "registration_open": true,
  "oidc_enabled": false,
  "oidc_issuer": "",
  "oidc_client_id": "",
  "oidc_client_secret_set": false,
  "oidc_exclusive": false,
  "oidc_provider_name": ""
}
```

The OIDC client secret is never returned — `oidc_client_secret_set` only reports whether one is currently stored, the same write-only-secret convention most admin panels use for third-party API credentials. There is no field-level access finer than "admin or not": any admin can read and change every setting.

### `PATCH /api/v1/admin/settings`

Every field is optional — send only what you want to change.

```bash
# rename the instance and close local registration
curl -X PATCH http://localhost:8080/api/v1/admin/settings \
  -d '{"instance_name": "Chez Nous", "registration_open": false}'

# enable OIDC/SSO
curl -X PATCH http://localhost:8080/api/v1/admin/settings \
  -d '{"oidc_enabled": true, "oidc_issuer": "https://idp.example.com", "oidc_client_id": "trakka", "oidc_client_secret": "..."}'
```

- `instance_name` (string, non-empty) — shown in the login page's `<title>` and, once loaded, the SPA header.
- `registration_open` (bool) — when `false`, `GET /auth/login?mode=register` and `POST /auth/register` both redirect with `?error=registration_closed` instead of showing/accepting the registration form. Existing accounts (local or OIDC) can still log in regardless.
- `oidc_enabled` (bool), `oidc_issuer`/`oidc_client_id` (string) — the same three inputs `OIDC_ISSUER`/`OIDC_CLIENT_ID`/`OIDC_CLIENT_SECRET` configure via environment variables (see [docs/DEPLOYMENT.md](DEPLOYMENT.md)), now editable at runtime without a restart.
- `oidc_client_secret` (string) — a non-empty value replaces the stored secret; omitted or empty leaves whatever's currently stored untouched. There is no way to explicitly blank the secret out other than disabling OIDC.
- `oidc_exclusive` (bool) — when `true`, `POST /auth/login` and `POST /auth/register` return `403` and the login page shows only the SSO button. `400` if OIDC isn't enabled or no admin is linked to the configured issuer. Password login comes back on its own if either stops being true (OIDC switched off or its settings incomplete, or no linked admin left) — but **not** while the provider is merely unreachable: a discovery that fails (at startup, typically when the identity provider starts after Trakka) is retried in the background with backoff (10 s up to 5 min) and on every `GET /auth/oidc/login`, which meanwhile redirects to `/auth/login?error=oidc_unavailable` (or the app's `…://sso?error=oidc_unavailable`), and passwords stay off. An admin who still has a session can turn exclusive mode off from the admin console while the provider is unreachable (an unchanged OIDC configuration isn't re-discovered on save). Without one, if the provider is gone for good, turn exclusive mode off directly in the database, then restart Trakka: `sqlite3 trakka.db "UPDATE system_settings SET value='false' WHERE key='oidc_exclusive'"`.
- `oidc_provider_name` (string, optional) — label for the login page's SSO button ("Se connecter avec Authentik"); empty falls back to "SSO". `400` if over 200 characters.

Enabling OIDC (or changing its issuer/client id/secret while already enabled) re-runs OIDC discovery synchronously against the new values, bounded to 10s, **before** anything is persisted: `400` with a descriptive message if `oidc_issuer`/`oidc_client_id`/`oidc_client_secret` aren't all non-empty, if the server's `BASE_URL` environment variable isn't set (still required — see [docs/DEPLOYMENT.md](DEPLOYMENT.md) — since it isn't itself one of the dynamic settings), or if discovery against the new issuer fails — then `{"error": "OIDC discovery timed out after 10s: <hint>"}`, or `{"error": "OIDC discovery failed (<reason>): <error> — <hint>"}` with `<reason>` one of `issuer_mismatch`, `http_status`, `dns`, `tls`, `connection_refused`, `insecure_issuer`, `other` (the same reason and hint are logged at warn level). A save that leaves `oidc_enabled`/`oidc_issuer`/`oidc_client_id`/`oidc_client_secret` as they are (the admin console resends every field) does **not** re-run discovery: it is saved at once even while the provider is unreachable, the current OIDC client (or the background retry) staying as it is. On any of these failures the previously active configuration (and OIDC client) is left completely untouched. On success, the new settings are saved and take effect immediately for the next `/auth/oidc/login` — no server restart needed. `400` also if `instance_name` would end up empty. `200` with the resulting settings (in the same shape as the `GET` above) otherwise.

Like house mutations, this endpoint returns `503` immediately rather than queuing if the browser is offline (`static/sw.js`) — a global, security-sensitive setting change is not something that should silently reapply later once connectivity returns.

### `GET /api/v1/admin/users`

Every account on the instance, ordered by id — unpaginated (Trakka targets small, self-hosted households/groups, not a multi-tenant SaaS).

```bash
curl -b cookies.txt http://localhost:8080/api/v1/admin/users
```

```json
[{ "id": 1, "email": "alice@example.com", "display_name": "Alice", "is_admin": true, "created_at": "...", "keep_last_page": true, "language": "fr" }]
```

Same shape as `GET /api/v1/me` — see [`GET /api/v1/me`](#get-apiv1me) above.

### `PATCH /api/v1/admin/users/{id}`

Grants or revokes the `is_admin` role for another account — the only attribute of another user's account an admin can change from here (email/password/display name stay self-service).

```bash
curl -X PATCH http://localhost:8080/api/v1/admin/users/2 -d '{"is_admin": true}'
```

`is_admin` (bool, required). `400` if it's missing, or if this would demote the very last remaining admin on the instance — since the only way an instance ever gets an admin at all is being the first account ever created (`internal/db.CreateUser`), there is no separate seeding mechanism or CLI to grant the role back once every admin is gone. `404` if `{id}` doesn't exist. `200` with the updated user otherwise.

### `DELETE /api/v1/admin/users/{id}`

Permanently deletes an account — sessions, house memberships, owned Spaces, granted/received shares, push subscriptions, and pending invitations sent by that user are all removed with it (every table referencing `users(id)` does so with `ON DELETE CASCADE`; a house left without any remaining member becomes a harmless orphaned row, the same pre-existing situation `ensureDefaultHouse`'s seed row already is — see [docs/DATABASE.md](DATABASE.md#houses)).

```bash
curl -X DELETE -b cookies.txt http://localhost:8080/api/v1/admin/users/3
```

`400` if `{id}` is the calling admin's own account (self-deletion isn't supported from this panel) or the last remaining admin. `404` if `{id}` doesn't exist. `204` on success.

### `GET /api/v1/admin/spaces`

Every Space (`custom_categories` row) on the instance, across every user, enriched with its owner's identity and how many lists currently reference it.

```bash
curl -b cookies.txt http://localhost:8080/api/v1/admin/spaces
```

```json
[{ "id": 1, "user_id": 4, "name": "Vacances", "icon": "🏖️", "color": "#3366ff", "position": 0, "created_at": "...", "owner_email": "alice@example.com", "owner_display_name": "Alice", "list_count": 2 }]
```

### `DELETE /api/v1/admin/spaces/{id}`

Deletes any user's Space, regardless of ownership — the admin override of [`DELETE /api/v1/custom-categories/{id}`](#custom-categories), which is scoped to the caller's own categories. Any list referencing it has `custom_category_id` reset to `NULL` (`ON DELETE SET NULL`), same as the owner-initiated delete — the list itself is never touched.

```bash
curl -X DELETE -b cookies.txt http://localhost:8080/api/v1/admin/spaces/1
```

`404` if `{id}` doesn't exist. `204` on success.

### `GET /api/v1/admin/logs`

The most recent server log entries, most recent first — a live, in-process snapshot with no persistence across a restart (`internal/logbuffer` wraps an in-memory ring buffer of fixed capacity around the application's own `log/slog` handler; see its package doc). `?limit=N` caps how many are returned (default 200, capped at 1000 regardless).

```bash
curl -b cookies.txt "http://localhost:8080/api/v1/admin/logs?limit=50"
```

```json
[{ "time": "...", "level": "INFO", "message": "applied pending invitations", "attrs": { "user_id": 3, "count": 1 } }]
```

Every mutating admin-management endpoint above (`PATCH`/`DELETE` on `users` and `spaces` — `logs` is read-only, so it has no mutation to gate) returns `503` immediately rather than queuing while offline, the same reasoning as `PATCH /api/v1/admin/settings` above.

## Server restart

Restarts the Trakka process from the admin console (Paramètres → Console d'Administration → Paramètres → "Serveur"). The server answers, then shuts down gracefully and **exits with status `75`** (`EX_TEMPFAIL`) for its supervisor to start it again — `restart: unless-stopped` in `compose.yml`, a Podman restart policy, or systemd's `Restart=on-failure`/`always`. With no supervisor, the server stays stopped. Design: [.claude/backend.md](../.claude/backend.md#server-restart).

Both endpoints need, in this order (the first one that fails answers):

- **No API token**: a request carrying `Authorization: Bearer trk_…` is `403` — at the token gate (neither route is in `apiTokenRoutes`) and again in the handler.
- **An admin session**: `401` without one, `403 {"error": "admin access required"}` for a non-admin.
- **`X-Trakka-Admin-Action: confirm-restart`**: `403` without it (or with any other value), on the `GET` too.

`404 {"error": "server restart is not available"}` when the binary wasn't wired for it (only in tests).

### `GET /api/v1/admin/restart-challenge`

Mints a single-use challenge for the next restart **from this session**, valid 60 seconds. A new one replaces the session's previous one.

```json
{ "challenge": "kq3…", "expires_in": 60 }
```

### `POST /api/v1/admin/restart`

```json
{ "challenge": "kq3…" }
```

`200 {"message": "Server restarting..."}`, and the shutdown starts 500 ms later. The challenge is consumed by the first `POST` that presents one, valid or not: a wrong, expired, reused or other-session challenge is `403 {"error": "invalid or expired restart challenge"}`, and a new `GET` is needed. `409` while a restart is already under way. The service worker never queues either route offline.

## Admin backups

Encrypted, off-site backups of the whole database to a WebDAV folder (Nextcloud, ownCloud, Synology, Apache `mod_dav`, `rclone serve webdav`, …) — on demand, on an automatic schedule, with a retention count — and restore from one of those backups. Every endpoint below is gated by `authorizeAdmin` (`403 {"error": "admin access required"}` for anyone else), checked before the request body is even read: a backup is a copy of every account's data, and a restore replaces it. The frontend is the "Sauvegardes" tab of the admin console (`static/js/admin.js`). Operator-side setup and the disaster-recovery procedure are in [docs/DEPLOYMENT.md](DEPLOYMENT.md#encrypted-webdav-backups); the design is in [.claude/backend.md](../.claude/backend.md#encrypted-webdav-backups).

**How a backup works.** The server takes a consistent hot snapshot of the live SQLite database (`VACUUM INTO`, on a separate read-only connection, so ordinary requests keep writing meanwhile), encrypts it **server-side** with the instance's backup key, and streams it to `<webdav folder>/trakka-backup-<UTC timestamp>.tkb` with a single `PUT`; then it lists the folder (`PROPFIND`) and `DELETE`s the oldest `trakka-backup-*.tkb` files beyond the retention count. Files in the folder that don't match that exact name pattern are never touched — but two Trakka instances must not share one folder, or each would prune the other's backups.

**Encryption.** AES-256-GCM in 64 KiB chunks under a per-file subkey (HKDF-SHA256 of the instance key and a random per-file salt), with each chunk's nonce binding its position and whether it is the last one — so any modification, reordering or truncation of a backup is detected on restore, and neither direction ever holds more than one chunk in memory. The instance key (32 random bytes) is generated the first time a WebDAV URL is saved (or the key is first exported), stored in `backup.key` next to the database with `0600` permissions — never inside the database, so it never travels inside the backups it protects — and shown in its printable form, `TRAKKA-BK1-XXXXX-…` (base32 + a 2-byte checksum that catches typos). **Without this key, a backup cannot be restored**: export it and keep it off the server. The WebDAV password is also stored encrypted (with a subkey of the same key) in `system_settings`, and never returned by any endpoint.

Error responses from these endpoints may carry a machine-readable `code` next to `error` (`{"error": "…", "code": "wrong_key"}`); the admin console translates it into an actionable message. WebDAV-side codes: `auth_failed`, `forbidden`, `not_found`, `not_a_folder`, `not_webdav`, `redirect` (redirects are reported, never followed — following one would resend the credentials), `storage_full`, `blocked_address` (see `BACKUP_WEBDAV_ALLOW_PRIVATE` in [docs/DEPLOYMENT.md](DEPLOYMENT.md#configuration-environment-variables)), `unreachable`, `tls`, `timeout`, `http_error`, `password_unreadable` (the stored password can't be decrypted with the current key file — re-enter it).

None of the write endpoints below goes through the service worker's offline queue: `static/sw.js` lets every non-`GET` `/api/v1/admin/backups/...` request through to the network untouched, so offline they fail immediately instead of being replayed later.

### `GET /api/v1/admin/backups`

Configuration, key status, the operation in progress, recent history and alerts. Local only — it never contacts the WebDAV server, so it's cheap enough for the frontend to call at startup (for admins) to decide whether to light the discreet alert dot on the header's settings button.

```bash
curl -b cookies.txt http://localhost:8080/api/v1/admin/backups
```

```json
{
  "config": {
    "webdav_url": "https://cloud.example.com/remote.php/dav/files/me/trakka/",
    "webdav_username": "me",
    "webdav_password_set": true,
    "auto_enabled": true,
    "frequency": "daily",
    "time": "03:00",
    "weekday": 0,
    "retention": 7
  },
  "key": { "exists": true, "fingerprint": "3f2a-91c0-7b4e-d215", "saved": true },
  "running": null,
  "last_run": { "id": 12, "source": "scheduled", "status": "failed", "started_at": "2026-09-28T01:00:00.412Z", "finished_at": "2026-09-28T01:00:30.415Z", "file_name": "", "size_bytes": 0, "error": "webdav PUT /remote.php/dav/files/me/trakka/trakka-backup-20260928T010000Z.tkb: HTTP 507 Insufficient Storage" },
  "last_success_at": "2026-09-27T01:00:00.388Z",
  "next_run_at": "2026-09-29T01:00:00.000Z",
  "runs": [ … ],
  "alerts": [ { "code": "last_backup_failed", "at": "2026-09-28T01:00:00.412Z", "error": "webdav PUT …: HTTP 507 Insufficient Storage" } ],
  "allow_private_networks": false,
  "time_zone": "Europe/Paris"
}
```

- `runs` holds the 10 most recent attempts, newest first, in the same shape as `last_run`. On a successful run, a non-empty `error` means the backup itself was uploaded but pruning old ones (retention) failed.
- `running` is `null` when idle, otherwise `{"kind": "backup"|"restore", "source": "manual"|"scheduled"|"", "since": "…"}` — held in memory only, never in the database (a snapshot must not contain its own "in progress" row).
- `key.saved` is whether this exact key (by fingerprint) has been exported at least once via `POST /api/v1/admin/backups/key/export`.
- `alerts` (non-blocking — the UI shows them inline in the tab, plus a small dot/⚠️, never a popup):
  - `last_backup_failed` — automatic backups are on, and a scheduled run failed with no successful run (of either kind) since. `at`/`error` describe that failed run.
  - `no_recent_success` — automatic backups are on, and there has been no successful backup for longer than the schedule allows: 3 days for `every_12h`/`daily`, 9 days for `weekly`, counted from the last success or from when the schedule was (re)armed, whichever is later. `days` is how long; `at` is the last success (absent if there never was one).
  - `key_not_saved` — backups are configured but the current key has never been exported.
- `time_zone` is the instance's `APP_TIMEZONE`, which the schedule's `time`/`weekday` are interpreted in.

### `PUT /api/v1/admin/backups/config`

Full replacement of the configuration; responds `200` with the same body as `GET /api/v1/admin/backups`.

```bash
curl -X PUT -b cookies.txt http://localhost:8080/api/v1/admin/backups/config \
  -H 'Content-Type: application/json' \
  -d '{"webdav_url": "https://cloud.example.com/remote.php/dav/files/me/trakka/", "webdav_username": "me",
       "webdav_password": "app-password", "auto_enabled": true, "frequency": "weekly", "time": "03:00",
       "weekday": 0, "retention": 7}'
```

- `webdav_url` — an absolute `http(s)://` **folder** URL (the folder must already exist); a trailing `/` is added if missing. Refused (`400`): other schemes, embedded credentials (`https://user:pw@…`), a query string or a fragment. May be empty only with `auto_enabled: false` — that switches backups off, and also drops the stored password.
- `webdav_username`, `webdav_password` — HTTP Basic credentials (for Nextcloud, use an app password). `webdav_password` is write-only: omitted or empty keeps the stored one.
- `auto_enabled` (bool); `frequency` — `every_12h` (at `time` and 12 hours later), `daily`, or `weekly` (on `weekday`, `0` = Sunday … `6` = Saturday); `time` — `HH:MM`, 24h, in `APP_TIMEZONE`; `retention` — how many backup files to keep in the folder, `1`–`365`.

Switching automatic backups on, or changing the schedule, (re)arms it from now: a slot that already went by today is not treated as missed. The scheduler (checked once a minute) runs each slot once; a failed scheduled run is retried up to twice more for the same slot, 30 minutes apart. A slot missed while the server was down is caught up right after startup.

### `POST /api/v1/admin/backups/test`

Tests an **unsaved** configuration: `PROPFIND` on the folder (credentials accepted, URL is a WebDAV folder), then writes and deletes a small `trakka-write-test-<random>.tmp` file (a read-only share would otherwise only fail at the first real backup). An empty `webdav_password` tests with the stored one.

```bash
curl -X POST -b cookies.txt http://localhost:8080/api/v1/admin/backups/test \
  -H 'Content-Type: application/json' \
  -d '{"webdav_url": "https://cloud.example.com/remote.php/dav/files/me/trakka/", "webdav_username": "me", "webdav_password": ""}'
```

```json
{ "ok": true, "writable": true, "backup_count": 7 }
```

A failed test is still `200` — `{"ok": false, "code": "auth_failed", "error": "webdav PROPFIND /remote.php/…: HTTP 401 Unauthorized"}` — since the test itself ran fine (and a `502`-style status would be mistaken by the frontend for Trakka itself being unreachable). `400` only for an invalid URL. Bounded to 30 s.

### `POST /api/v1/admin/backups/run`

Starts a backup immediately, in the background, and answers `202` with the status (now showing `running`); poll `GET /api/v1/admin/backups` until `running` is `null` again — the outcome is the newest entry in `runs`. `400 {"code": "not_configured"}` if no WebDAV URL is saved; `409 {"code": "busy"}` if a backup or restore is already running.

### `GET /api/v1/admin/backups/remote`

Lists the backup files in the WebDAV folder, newest first (by the UTC timestamp in the file name, not the server's modification date).

```json
{ "files": [ { "name": "trakka-backup-20260929T010000Z.tkb", "size": 188600, "modified": "2026-09-29T01:00:02.000Z" } ] }
```

A WebDAV failure is `200` with `{"files": [], "code": "…", "error": "…"}`; `400 {"code": "not_configured"}` without a saved URL.

### `POST /api/v1/admin/backups/key/export`

Returns the instance's backup key (generating it first if none exists yet) and records that it has been saved, clearing the `key_not_saved` alert. A `POST` rather than a `GET` because it changes that state — no `GET` route in this app may (see the CSRF rule in [.claude/backend.md](../.claude/backend.md#security-rules)). Logged (admin id + fingerprint, never the key).

```json
{
  "key": "TRAKKA-BK1-7QK2M-…",
  "fingerprint": "3f2a-91c0-7b4e-d215",
  "file_name": "trakka-backup-key-3f2a-91c0.key",
  "file_content": "# Trakka backup encryption key\n# Fingerprint: 3f2a-91c0-7b4e-d215\n…\nTRAKKA-BK1-7QK2M-…\n"
}
```

The admin console saves `file_content` as a `.key` file (built client-side from a `Blob`, so the key never appears in a URL, the browser history or an access log) or copies `key` to the clipboard.

### `POST /api/v1/admin/backups/restore`

Replaces **the entire database** with a backup. `multipart/form-data`, fields in any order:

- `file` — an uploaded `.tkb` backup (streamed to a staging file on the data volume, never held in memory; capped at 1 GiB), **or** `remote_name` — the name of a backup in the configured WebDAV folder (must match `trakka-backup-<timestamp>.tkb`), downloaded with a `GET`. Exactly one of the two.
- `key` — the backup key as text (the printable form, or the whole `.key` file content pasted as-is — comment lines, case, dashes and whitespace are all tolerated), and/or `key_file` — the `.key` file uploaded. Both omitted: the instance's current key.

```bash
curl -X POST -b cookies.txt http://localhost:8080/api/v1/admin/backups/restore \
  -F "file=@trakka-backup-20260929T010000Z.tkb" -F "key_file=@trakka-backup-key-3f2a-91c0.key"

curl -X POST -b cookies.txt http://localhost:8080/api/v1/admin/backups/restore \
  -F "remote_name=trakka-backup-20260929T010000Z.tkb"
```

Steps, each of which must succeed before the next one runs: decrypt into a staging file (the key's fingerprint is checked against the backup's header first, then every chunk is authenticated) → `PRAGMA integrity_check`, check it is a Trakka database and not from a newer Trakka release, and apply any pending migrations **to the staged copy** → save a plaintext safety copy of the current database to `<DB_PATH dir>/backups/trakka-pre-restore-<timestamp>.db` → copy the staged file into the live database with SQLite's online backup API over the application's single shared connection (every other query simply waits the few milliseconds this takes; no restart). Any failure before that last step leaves the live database untouched.

```json
{ "from_schema_version": 19, "safety_copy": "trakka-pre-restore-20260929T101530Z.db", "key_adopted": true }
```

After a successful restore:

- `key_adopted: true` means the supplied key differed from the instance's own: it becomes the instance key (the previous `backup.key` is kept as `backup.key.pre-restore-<timestamp>`), so the restored database's own backup settings — including its encrypted WebDAV password — keep working, and future backups use the key you already hold.
- Sessions, users and settings are all whatever the backup held: the admin is typically signed out. The live OIDC client is rebuilt from the restored settings.
- The backup history from before the restore is carried over, and the "no recent success" clock restarts from now.

`400` with a `code` for anything refused: `wrong_key` (the error names both fingerprints), `invalid_key` (malformed or typo'd key), `key_required`, `not_a_backup`, `unsupported_version`, `corrupt` (modified or truncated file, or a damaged database inside), `not_trakka`, `too_new`, `invalid_name`, `not_configured`, or a WebDAV code when fetching `remote_name`; `409 {"code": "busy"}`. The request's read/write deadlines are extended to 15 minutes (the server's defaults are 15 seconds).

## Static assets

Everything under `static/` is served from `/` (e.g. `static/index.html` → `/`, `static/js/app.js` → `/js/app.js`). This is unrelated to the JSON API but shares the same HTTP server and the same security headers.
