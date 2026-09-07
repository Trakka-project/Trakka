# API Reference

Base URL: `http://<host>:<port>` (default port `8080`). All endpoints are under `/api/v1`.

All responses are `application/json; charset=utf-8`. Every `/api/v1/...` endpoint requires an authenticated session (see [Authentication](#authentication) below) — there is no anonymous access to the JSON API. `/auth/...` endpoints and static assets stay unauthenticated, since `/auth/...` is how a session gets established in the first place.

## Conventions

- **Errors** are always `{"error": "<message>"}` with a non-2xx status code.
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
| `/auth/oidc/login` | `GET` | Redirects to the configured OIDC provider. `404` if OIDC isn't configured. |
| `/auth/oidc/callback` | `GET` | The provider's redirect target. Verifies the id_token, provisions the account on first login (with a personal house, same as local registration), sets the session cookie, redirects to `/`. |

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

### `GET /api/v1/me`

Returns the authenticated user. `401 {"error": "authentication required"}` if the session cookie is missing, invalid, or expired.

```json
{ "id": 1, "email": "alice@example.com", "display_name": "Alice", "is_admin": false, "created_at": "...", "keep_last_page": true, "language": "en" }
```

`is_admin` grants access to the [Admin settings](#admin-settings) endpoints below. The very first account ever created on an instance (local or OIDC-provisioned) becomes an admin automatically — see `internal/db.CreateUser` in [CLAUDE.md](../CLAUDE.md) — and there is currently no endpoint to grant or revoke it for any other account.

`keep_last_page` (`bool`, defaults to `true`) controls whether the frontend reopens on the last dashboard tab or list the user had open instead of always landing on the dashboard — see the "keep last page on launch" feature in [CLAUDE.md](../CLAUDE.md). The actual last-visited view itself is tracked purely client-side (`localStorage`, per browser, never sent to the server); only this on/off preference is part of the user's profile.

`language` (`string`, `"fr"` or `"en"`) is the account's own UI-language preference, set from the "Langue" section of the "Paramètres" modal (`static/js/i18n.js`/`settings.js`). Never empty in this response: an account that has never set its own preference resolves to the instance's `DEFAULT_APP_LANGUAGE` env var (default `"en"`, see [CLAUDE.md](../CLAUDE.md)) rather than the field being omitted or blank.

### `PATCH /api/v1/me`

Partial update of the caller's own profile preferences — `keep_last_page` and/or `language` — following the same "absent = untouched" convention as `PATCH /api/v1/items/{id}`; either, both, or neither may be present in one request. Returns the updated user (same shape as `GET /api/v1/me`). `language` must be `"fr"` or `"en"`, or the request is rejected with `400`.

```bash
curl -b cookies.txt -X PATCH http://localhost:8080/api/v1/me \
  -H 'Content-Type: application/json' -d '{"keep_last_page": false, "language": "fr"}'
```

**CSRF**: `/auth/login`/`/auth/register` each require the `csrf_token` form field described above — a double-submit token, minted and set as an HttpOnly `trakka_csrf` cookie by `GET /auth/login`, that the submitted form field must match (`internal/handlers/csrf.go`). This defends specifically against "login CSRF" (a cross-site POST silently signing a victim into an attacker-controlled account) — a threat `SameSite` alone can't prevent here, since neither of these requests carries a pre-existing session cookie for `SameSite` to withhold. Every subsequent state-changing call instead goes through `/api/v1/...`, protected by the session cookie's `SameSite=Lax` attribute plus an `Origin`/`Sec-Fetch-Site` check (`requireSameOriginWrite`, same file): a cross-site request never carries the session cookie, and the same middleware also rejects a cross-site `/auth/logout` POST, which carries no session cookie either but would still be a nuisance if forgeable.

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

`204` on success. `403` if the caller lacks permission. `404` if the target user isn't a member.

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

`204` on success, scoped to the caller's own categories the same way `PUT` is (`404` otherwise). Any list attached to the deleted category has its `custom_category_id` reset to `null` (`ON DELETE SET NULL`) — the list itself is untouched.

## Lists

A list has a `type` of `todo`, `shopping`, `groceries`, `recurring_shopping` or `custom`, and belongs to exactly one house (`house_id`). `shopping` (one-off purchases), `groceries` (day-to-day shopping runs) and `recurring_shopping` (subscriptions/recurring purchases) are all purchase-oriented types stored the same way — the frontend just shows a different subset of the item form's fields depending on which one a list has (see `static/js/list_view.js`'s `applyListTypeVisibility`): `groceries` shows only name/quantity, `shopping` adds URL/price/target month, `recurring_shopping` adds URL/price/recurrence instead of a target month. `custom` is accepted by the API for forward compatibility but has no dedicated item-form behavior yet.

A list can optionally be attached to one of its house members' [custom categories](#custom-categories) via `custom_category_id` — see `POST`/`PUT` below. It's orthogonal to `type`: a `shopping` list and a `todo` list can both belong to the same "Vacances" category.

A list also carries a freeform `icon` (typically an emoji, e.g. `🛒`/`🖥️`/`📦`) the frontend shows next to its name; `""` (the default) means no icon was set, in which case the frontend falls back to a fixed icon for the list's `type`.

### `GET /api/v1/lists`

Returns lists belonging to houses the caller is a member of, newest first. Items are **not** embedded here (see `GET /api/v1/lists/{id}` for that).

Query parameters:
- `type` (optional) — filter to `todo`, `shopping`, `groceries`, `recurring_shopping` or `custom`. `400` if any other value is given.
- `house_id` (optional) — filter to lists belonging to that house. `400` if not a positive integer; `403` if the caller isn't a member of that house.
- `shared_with_me` (optional) — when `"true"`, switches to a different, mutually exclusive mode: instead of the caller's own house-scoped lists, returns every list reachable only via a [List or Space share](#sharing) (see `access_source`/`access_permission` below), excluding any whose house they're already a plain member of. `type`/`house_id` don't apply in this mode.
- `pinned_house_spaces` (optional) — when `"true"`, a third mutually exclusive mode: every list the caller reaches purely by pinning a Space that's merely *visible* to them through House membership rather than an explicit share (`access_source: "house_member"` — see [Pinning a House-visible Space](#patch-apiv1custom-categoriesidsharepin)). Deliberately not folded into `?shared_with_me=true`'s own query: every row this mode returns already belongs to a House the caller is a member of, which `?shared_with_me=true`'s own exclusion (above) would filter straight back out. This is what lets a pinned House Space's lists show up on the caller's dashboard even while a *different* House they also belong to is the one currently selected. `type`/`house_id` don't apply in this mode either.

```bash
curl -b cookies.txt http://localhost:8080/api/v1/lists
curl -b cookies.txt http://localhost:8080/api/v1/lists?type=shopping
curl -b cookies.txt http://localhost:8080/api/v1/lists?house_id=1
curl -b cookies.txt http://localhost:8080/api/v1/lists?shared_with_me=true
curl -b cookies.txt http://localhost:8080/api/v1/lists?pinned_house_spaces=true
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

`custom_category`/`custom_category_id` are only present when the list is attached to one — see [Custom categories](#custom-categories). `access_source`/`access_permission`/`is_pinned_to_dashboard` are only present in the `?shared_with_me=true`/`?pinned_house_spaces=true` modes above (never on this endpoint's own default, house-scoped listing) — see [Sharing](#sharing). `access_source` is `"list_share"`/`"space_share"` from `?shared_with_me=true`, or `"house_member"` from `?pinned_house_spaces=true`. For a list reached via `access_source: "space_share"` or `"house_member"`, `is_pinned_to_dashboard` reflects either that list's own individual pin ([`PATCH /api/v1/lists/{id}/share/pin`](#patch-apiv1listsidsharepin)) or the parent Space's own pin as a whole ([`PATCH /api/v1/custom-categories/{id}/share/pin`](#patch-apiv1custom-categoriesidsharepin)) — whichever says pinned wins; for `"house_member"` specifically every returned row is pinned by construction (`?pinned_house_spaces=true` never returns an unpinned one). The dashboard itself is a client-side merge of three calls, deduplicated by list id (a `house_member`-sourced list may already be present in the plain `?house_id=` listing if it happens to belong to the currently selected House): the plain `?house_id=` listing above, `?shared_with_me=true` filtered down to entries with `is_pinned_to_dashboard: true`, and `?pinned_house_spaces=true` in full (see `static/js/app.js`'s `loadDashboard` and `static/js/shares.js`'s `loadPinnedSharedLists`/`loadPinnedHouseSpaceLists`) — there's no server-side "give me my house's lists plus my pinned shares in one call" mode.

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
| `icon` | string | no | trimmed; freeform (typically an emoji); omitted/empty leaves it unset |

`201` with the created list (no `items` field).

### `GET /api/v1/lists/{id}`

Returns the list **with its items embedded**, ordered by `position` then `id`.

- `200` with the list, e.g.:
  ```json
  {
    "id": 1, "house_id": 1, "name": "Courses", "type": "shopping",
    "created_at": "...", "updated_at": "...", "total_amount": 3.7,
    "items": [
      { "id": 1, "list_id": 1, "title": "Lait", "url": "https://...", "quantity": 2, "price": 1.85, "price_auto": false, "image_url": "https://...", "done": false, "position": 0, "target_month": "2026-11", "due_date": null, "is_recurring": false, "recurrence_rule": null, "recurrence_end_date": null, "is_urgent": false, "target_price": null, "alert_on_price_drop": false, "labels": [], "created_at": "...", "updated_at": "..." }
    ]
  }
  ```
- `404` if the list doesn't exist. `403` unless the caller has at least read [access](#sharing) to the list — house membership, a Space share, or a List share.
- `total_amount` here is the same server-computed field `GET /api/v1/lists` documents above (the list's "reste à dépenser" — unchecked, priced items only) — included on this endpoint too even though `items` is already embedded, so a caller never has to re-derive it by summing `items` itself.

### `PUT /api/v1/lists/{id}`

Full replace of `name`, `type`, `custom_category_id`, and `icon` (same validation as `POST`; omitting/nulling `custom_category_id` **dissociates** the list from whatever category it had, and omitting `icon` clears it, since this is a full replace). `house_id` cannot be changed via this endpoint. Returns the updated list (no `items`). `404` if not found, `403` unless the caller has **write** [access](#sharing) to the list (house membership, or a `write` Space/List share), `400` if `custom_category_id` is given but doesn't reference a category the caller owns.

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

## Sharing

Beyond house membership, a [Space](#custom-categories) or an individual [List](#lists) can be shared directly with one other user by email, granting `read` or `write` access to it without adding them to the whole parent house. A user's effective access level to a list is the highest of: house membership (always `write`), a `list_shares` grant on the list itself, and a `space_shares` grant on the list's attached custom category, if any. See the "granular sharing" design in [CLAUDE.md](../CLAUDE.md) and [docs/DATABASE.md](DATABASE.md#space_shares-and-list_shares) for the full model.

Sharing a **Space** is its owner's call alone (the same person who can rename/delete it) — `custom_categories` has no other notion of membership. Sharing a **List** requires actual membership of its house, not merely write access granted through another share, so access can never be used to extend itself further.

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

Revokes a share. `204` on success, `404` if no such share exists (also on a repeat call — revoking is not idempotent-silent), otherwise the same `404`/`403` access rules as `GET` above.

## Items

### `GET /api/v1/items?list_id={id}`

`list_id` is required. Returns `200` with an array. `400` if `list_id` is missing, not a positive integer, or doesn't reference an existing list. `403` unless the caller has at least read [access](#sharing) to the list.

```bash
curl -b cookies.txt "http://localhost:8080/api/v1/items?list_id=1"
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
| `price` | number | no | unit price in euros; `400` if negative; omitted/`null` means no price recorded |
| `position` | integer | no | defaults to `0`; used for manual ordering within a list |
| `target_month` | string | no | planned purchase month, `YYYY-MM` (e.g. `"2026-11"`); `400` if given but not in that format; empty string is stored as `null` (unscheduled) |
| `due_date` | string | no | `YYYY-MM-DD`; `400` if given but not a real calendar date in that format. For a recurring item this is managed automatically (see `recurrence_rule` below) and generally isn't set directly at creation |
| `recurrence_rule` | string | no | one of `DAILY`, `WEEKLY`, `MONTHLY`, `YEARLY`, or the custom `EVERY_X_DAYS:<n>` form (e.g. `"EVERY_X_DAYS:3"`); `400` if given but unrecognized. There is no separate `is_recurring` request field — `is_recurring` in the response is simply whether this is set |
| `recurrence_end_date` | string | no | `YYYY-MM-DD`; the last date a recurring item should recur on. `400` if given but not a real calendar date |
| `is_urgent` | boolean | no | flags the item as needing attention right away (e.g. "out of toilet paper"); defaults to `false`. Independent of every other field — no validation beyond being a boolean |
| `recurrence_lead_minutes` | integer | no | overrides `NOTIF_RECURRING_TASK_LEAD_TIME` (see [Push notifications](#push-notifications)) for this item alone — how long before `due_date` a reminder push is sent, in minutes; `400` if negative. Meaningless unless `recurrence_rule` is also set |
| `target_price` | number | no | a price threshold in euros; `400` if negative. See [Price drop alerts](#price-drop-alerts) below |
| `alert_on_price_drop` | boolean | no | opts the item into a notification once `price` reaches `target_price`; defaults to `false`. Independent of `target_price` itself being set — see [Price drop alerts](#price-drop-alerts) |
| `labels` | array of strings | no | freeform tags (e.g. `["Bio", "Promo"]`); see [Labels](#labels) below |

`400` if `list_id` doesn't reference an existing list. `403` unless the caller has **write** [access](#sharing) to the list. `201` with the created item. If `url` is given, the server kicks off a **best-effort** lookup against that URL (see `internal/scraper` in [CLAUDE.md](../CLAUDE.md)) for whichever of `price`/`image_url` the item is still missing (an explicit `price` in the request skips price detection but the image is still looked up), and waits up to ~2.5s for it before responding: if it finishes in time, the `201` response already carries `price` (with `price_auto: true`) and `image_url`, and reports `price_status: "found"`; if the site is slow to respond, the lookup keeps running in the background and the response instead carries `price_status: "pending"` — poll `GET /api/v1/items/{id}` (or re-fetch the list) a few seconds later to pick it up. `price_status: "none"` means there was no `url` to scrape or no price was found in time — this is unaffected by whether an image was found. `image_url`, when present, is always an absolute `http://`/`https://` URL. `price_status` is response-only — it's never persisted and never appears on a plain `GET`.

### Recurring items

Setting `recurrence_rule` (`POST`, `PUT`, or `PATCH`) makes an item recurring — the frontend's recurrence dropdown only offers the four fixed cadences, but `EVERY_X_DAYS:<n>` is accepted by the API for any client that wants a custom interval. Completing a recurring item (`PATCH {"done": true}`, or `PUT` with `done: true`) does **not** leave it marked done: the server computes the next occurrence's `due_date` from the rule (advancing from the item's current `due_date`, or from today if it doesn't have one yet) and responds with the *same* item id, `done: false`, and the new `due_date` — there's no cloned row and no second item to track. If `recurrence_end_date` is set and the computed next occurrence would fall after it, the item is left `done: true` instead, exactly like a non-recurring item, and stops advancing from then on. Clearing `recurrence_rule` (an empty string via `PATCH`, or omitting it via `PUT`) turns off recurrence entirely and clears `due_date`/`recurrence_end_date` along with it.

A recurring item with a `due_date` also gets a reminder push once its due date is within its lead time — see [Push notifications](#push-notifications) for the full mechanism and `recurrence_lead_minutes` above for overriding the lead time on this one item.

```bash
# make an item repeat weekly
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"recurrence_rule": "WEEKLY"}'

# check it off — the response comes back with done:false and an advanced due_date
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"done": true}'
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

In addition to the request-time paths above, a periodic background worker (`internal/handlers.RunTargetPriceScan`, every `SCRAPE_INTERVAL`, default `12h`; see [docs/DEPLOYMENT.md](DEPLOYMENT.md)) independently re-scrapes every not-done item that has a `url`, `alert_on_price_drop: true`, and a `target_price` set (`db.ListItemsForTargetPriceScan`), regardless of whether anyone has touched it recently — this is what catches a price drop on a tracked item nobody happens to edit or re-open. It waits 5 seconds between items (a fixed, not-yet-configurable delay) specifically to avoid hammering a merchant's site with a burst of near-simultaneous requests from this server's IP. Applying a newly-scraped price is a compare-and-swap against the price/url the scan read at the start of that item's check (`db.UpdateItemPriceFromScan`), so a user's own concurrent edit — or a different scan resolving the same item first — is never clobbered; a lost race is silently skipped, the same "not an error" contract every other scraper-driven path in this app follows. A price change that's actually applied runs through the exact same false→true threshold check described above, so it triggers a push notification the same way a manual edit does — there is just no HTTP response for this path to attach an in-app toast to.

See [docs/DOC_TEST_PRICE_ALERTS.md](DOC_TEST_PRICE_ALERTS.md) for a step-by-step manual QA recipe covering this feature end to end (badge, toast, push, and the scraper-driven path).

### `GET /api/v1/items/{id}`

`200` with the item, `404` if not found, `403` unless the caller has at least read [access](#sharing) to the item's list.

### `PUT /api/v1/items/{id}`

Full replace — every field below is required except `url`, `quantity`, `price`, `done`, `position`, `target_month`, `due_date`, `recurrence_rule`, `recurrence_end_date`, `is_urgent`, `recurrence_lead_minutes`, `target_price`, `alert_on_price_drop`, `labels` fall back to their zero/default value if omitted (note: unlike `PATCH`, omitting a field here **resets** it, since this is a full replace — an omitted `price` clears any previously recorded price, an omitted `target_month` unschedules the item, an omitted `recurrence_rule` turns off recurrence, an omitted `is_urgent` clears the urgent flag, an omitted `recurrence_lead_minutes` reverts to the instance-wide default lead time, an omitted `target_price`/`alert_on_price_drop` clears the price-drop threshold and turns the alert off, and an omitted `labels` clears the label set back to `[]`). `list_id` cannot be changed via this endpoint.

```bash
curl -X PUT http://localhost:8080/api/v1/items/1 \
  -d '{"title": "Lait", "url": "https://example.com/lait", "quantity": 2, "price": 1.85, "done": true, "position": 0, "target_month": "2026-11", "recurrence_rule": "WEEKLY"}'
```

`404` if not found. `400` if `price`/`target_price` is negative, `target_month` isn't `YYYY-MM`, `due_date`/`recurrence_end_date` isn't `YYYY-MM-DD`, or `recurrence_rule` isn't a recognized form. `403` unless the caller has **write** [access](#sharing) to the item's list. `price_auto` is always reset to `false` by this endpoint (a full replace is always an explicit, manual value, even when `price` is omitted). If `url` changes to something new, `image_url` is cleared (a scraped image is tied to the `url` it was found on) and the same bounded lookup as `POST` above kicks off for whichever of `price`/`image_url` is still missing, with the same `price_status` values in the response. If `done` transitions from `false` to `true` on a recurring item (`recurrence_rule` set, after this request's changes are applied), see "Recurring items" above — the response's `done`/`due_date` may not match what was sent. See [Price drop alerts](#price-drop-alerts) for `target_price`/`alert_on_price_drop`/`price_alert_triggered`.

### `PATCH /api/v1/items/{id}`

Partial update — only send the fields you want to change. This is the endpoint to use for e.g. checking an item off a list, or moving a planned purchase to a different month:

```bash
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"done": true}'
```

All fields (`title`, `url`, `quantity`, `price`, `done`, `position`, `target_month`, `due_date`, `recurrence_rule`, `recurrence_end_date`, `is_urgent`, `recurrence_lead_minutes`, `target_price`, `alert_on_price_drop`, `labels`) are optional. `labels`, if given (including an explicit `[]`), replaces the item's whole label set — omitting it entirely leaves the existing labels untouched, the same "absent = untouched" convention as `target_month`/`recurrence_rule`; see [Labels](#labels) above for the cleaning/validation rules. `title`, if given, cannot be empty; `quantity`, if given, must be positive; `price`, if given, must be a non-negative number or `null` (unlike the other fields, `price` distinguishes "omitted" — leave unchanged — from an explicit `null` — clear the recorded price). Sending `price` (either a number or `null`) always resets `price_auto` to `false`, since a `price` supplied in the request is by definition a manual value. `target_month`, if given, must be `YYYY-MM` or an empty string (clears it back to unscheduled) — omitting it entirely leaves the item's schedule untouched. `due_date`/`recurrence_end_date`, if given, must be `YYYY-MM-DD` or an empty string (clears them). `recurrence_rule`, if given, must be one of the recognized forms or an empty string — clearing it this way also clears `due_date` and `recurrence_end_date`, since neither means anything once the item stops recurring. `recurrence_lead_minutes`, like `price`, distinguishes "omitted" (leave unchanged) from an explicit `null` (revert to the instance-wide default lead time) — a number must be non-negative. `target_price`, like `price`, distinguishes "omitted" (leave unchanged) from an explicit `null` (clear the threshold) — a number must be non-negative; `alert_on_price_drop`, if given, is a plain boolean. There is no `image_url` field to set directly — it's scraper-only. If `url` changes to something new, `image_url` is cleared (same reasoning as `PUT` above) and the bounded lookup described under `POST` kicks off for whichever of `price`/`image_url` is still missing, with the same `price_status` values in the response; an unrelated `PATCH` (e.g. `{"done": true}`) never re-triggers it, since `url` isn't changing. If `done` is being set to `true` on a recurring item, see "Recurring items" above — the response's `done`/`due_date` may not match what was sent. See [Price drop alerts](#price-drop-alerts) for `target_price`/`alert_on_price_drop`/`price_alert_triggered`. `404` if not found, `403` unless the caller has **write** [access](#sharing) to the item's list.

```bash
# clear a previously recorded price without touching anything else
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"price": null}'

# reschedule a planned purchase to a different month
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"target_month": "2027-01"}'

# turn off recurrence entirely
curl -X PATCH http://localhost:8080/api/v1/items/1 -d '{"recurrence_rule": ""}'
```

### `DELETE /api/v1/items/{id}`

`204` on success, `404` if not found, `403` unless the caller has **write** [access](#sharing) to the item's list.

### `POST /api/v1/items/{id}/price-check`

Triggers an immediate, synchronous re-check of this one item's product page for a lower price than what's currently recorded — the on-demand counterpart to the periodic background scan described under "Price alerts" below. `400` if the item has no `url` or no `price` to compare against (nothing to check). `404` if not found, `403` unless the caller has **write** [access](#sharing) to the item's list. Otherwise `200` with `{"alert": null}` if nothing lower was found (or a lower price was already known via an earlier still-pending alert for this item), or `{"alert": {...}}` with the pending alert (freshly created, or the pre-existing one) if one exists.

```bash
curl -X POST http://localhost:8080/api/v1/items/1/price-check
```

## Price alerts

A price alert records a lower price found for an item's `url` than its current `price` — created either by a periodic background scan (every `PRICE_CHECK_INTERVAL_HOURS`, default 24; see [docs/DEPLOYMENT.md](DEPLOYMENT.md)) or by `POST /api/v1/items/{id}/price-check` above. Every alert starts `pending` and is resolved exactly once, either `accepted` (applies `found_price` to the item, marking it `price_auto: true`) or `rejected` (dismissed, item untouched) — see `internal/db.AcceptPriceAlert`/`RejectPriceAlert` in [CLAUDE.md](../CLAUDE.md). The frontend surfaces pending alerts as a badge count on the header's 🔔 button, with a drawer to accept/reject each one (`static/js/notifications.js`).

### `GET /api/v1/price-alerts?house_id={id}`

`house_id` is required. `?status=` optionally restricts the result to one of `pending`/`accepted`/`rejected` (the notification bell always passes `status=pending`); omitted returns every status, newest first. `400` if `house_id` is missing/invalid or `status` isn't a recognized value. `403` if the caller isn't a member of the house.

```bash
curl -b cookies.txt "http://localhost:8080/api/v1/price-alerts?house_id=1&status=pending"
```

Each alert includes `item_title` and `list_id` (joined in for display and click-through, never writable) alongside `item_id`, `original_price` (a snapshot of the item's price when the alert was created), `found_price`, `source_url`, `status`, and `created_at`.

### `PATCH /api/v1/price-alerts/{id}`

```bash
# apply the lower price found
curl -X PATCH http://localhost:8080/api/v1/price-alerts/1 -d '{"status": "accepted"}'

# dismiss it instead
curl -X PATCH http://localhost:8080/api/v1/price-alerts/1 -d '{"status": "rejected"}'
```

`status` is required and must be `"accepted"` or `"rejected"`. `404` if not found, `403` unless the caller has **write** [access](#sharing) to the alert's item's list, `409` if the alert was already resolved (an alert can only ever be actioned once, whichever status wins the race). `200` with the updated alert otherwise.

## Push notifications

Web Push (VAPID, RFC 8292/8291/8030 — see `internal/webpush` in [CLAUDE.md](../CLAUDE.md)) delivers two kinds of notification, both opted into via the same browser subscription: (1) when another member/recipient of a shared list adds or checks off an item, every *other* user with access to that list is notified; (2) for a recurring item with a `due_date`, every user with access to its list is notified once the current time is within its lead time of that date (`NOTIF_RECURRING_TASK_LEAD_TIME`, default `24h` — accepts a plain Go duration like `2h`/`30m` or a whole number of days like `1d`; overridable per-item via `recurrence_lead_minutes`, see [Items](#items) above). Requires `VAPID_PUBLIC_KEY`/`VAPID_PRIVATE_KEY`/`VAPID_SUBJECT` to be configured (see [docs/DEPLOYMENT.md](DEPLOYMENT.md)) — `trakka -generate-vapid-keys` prints a fresh key pair. Notification text is always composed in French server-side (there is no per-user language preference stored server-side to localize it with — see CLAUDE.md's "UI language" convention).

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

## Admin settings

Two endpoints, both gated behind `models.User.IsAdmin` (`403 {"error": "admin access required"}` for anyone else) rather than house membership — these are system-wide, not scoped to a house. They manage the `system_settings` table (see [docs/DATABASE.md](DATABASE.md#system_settings)), which takes priority over the equivalent environment variable whenever a row exists (`internal/settings.Resolve`); a value with no row falls back to its env var. The frontend surfaces this as a "Paramètres du Système" panel behind a ⚙️ button in the header, visible only to admins (`static/js/admin.js`).

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
  "oidc_client_secret_set": false
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

Enabling OIDC (or changing its issuer/client id/secret while already enabled) re-runs OIDC discovery synchronously against the new values, bounded to 10s, **before** anything is persisted: `400` with a descriptive message if `oidc_issuer`/`oidc_client_id`/`oidc_client_secret` aren't all non-empty, if the server's `BASE_URL` environment variable isn't set (still required — see [docs/DEPLOYMENT.md](DEPLOYMENT.md) — since it isn't itself one of the dynamic settings), or if discovery against the new issuer fails. On any of these the previously active configuration (and OIDC client) is left completely untouched. On success, the new settings are saved and take effect immediately for the next `/auth/oidc/login` — no server restart needed. `400` also if `instance_name` would end up empty. `200` with the resulting settings (in the same shape as the `GET` above) otherwise.

Like house mutations, this endpoint returns `503` immediately rather than queuing if the browser is offline (`static/sw.js`) — a global, security-sensitive setting change is not something that should silently reapply later once connectivity returns.

## Static assets

Everything under `static/` is served from `/` (e.g. `static/index.html` → `/`, `static/js/app.js` → `/js/app.js`). This is unrelated to the JSON API but shares the same HTTP server and the same security headers.
