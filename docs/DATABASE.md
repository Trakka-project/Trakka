# Database

Trakka uses SQLite via [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite), a pure-Go driver — there is no CGO dependency on `libsqlite3`, which is what keeps the binary static and the image minimal.

## Location & connection

- Path is set by the `DB_PATH` environment variable (default `/data/trakka.db` in the container, `./trakka.db` for local runs).
- Opened with: `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`.
- The connection pool is capped at **one connection** (`SetMaxOpenConns(1)` / `SetMaxIdleConns(1)`) in `Open()` in [internal/db/db.go](../internal/db/db.go). SQLite allows only one writer at a time regardless of pool size, and the pure-Go driver gains nothing from pooling multiple readers the way `mattn/go-sqlite3` might — a single shared connection avoids `SQLITE_BUSY` errors under concurrent requests. Do not raise this without a specific reason.
- All queries live in [internal/db/lists.go](../internal/db/lists.go), [internal/db/items.go](../internal/db/items.go), [internal/db/houses.go](../internal/db/houses.go), [internal/db/house_members.go](../internal/db/house_members.go), [internal/db/users.go](../internal/db/users.go), [internal/db/sessions.go](../internal/db/sessions.go), and [internal/db/custom_categories.go](../internal/db/custom_categories.go), exclusively as parameterized statements (`?` placeholders) — no other package builds SQL or imports `database/sql`.
- `CreateHouseWithOwner` (in `houses.go`) is the one place in this package that opens an explicit `sql.Tx` — it needs the house-row insert and the owner's `house_members` insert to succeed or fail together, so a house is never left without an owner. The single-connection pool above means this simply serializes against other writes; it doesn't change the pooling rules.

## Schema

Built up by a versioned sequence of migrations in [internal/db/migrations/](../internal/db/migrations/) (`0001_init.sql`, `0002_recurring_items.sql`, ...), embedded into the binary via `//go:embed` and applied by the engine in [internal/db/migrate.go](../internal/db/migrate.go). Unlike the single idempotent `schema.sql` this replaced, each migration is a plain, un-guarded script (`CREATE TABLE`, `ALTER TABLE ADD COLUMN`, no `IF NOT EXISTS` needed) that runs at most once per database, ever — tracked via SQLite's built-in `PRAGMA user_version`, an integer stored in the database file's own header, so no extra table is needed to record it. See [Evolving the schema](#evolving-the-schema) below for the full mechanism, including how an existing pre-migration-system database is adopted and why one migration (widening `lists.type`'s `CHECK` constraint) is Go code instead of a `.sql` file.

### `houses`

| Column | Type | Notes |
|---|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| `name` | `TEXT NOT NULL` | |
| `created_at` | `TEXT NOT NULL` | ISO-8601 UTC, set by `strftime` default |

Groups related lists together. `ensureDefaultHouse()` (`internal/db/db.go`, run right after the schema and the `lists.house_id` migration below, on every startup) inserts a house named "Maison Principale" the first time no house exists at all, and backfills `house_id` on any list that doesn't have one yet — which is every list on an upgrade from before this table existed. This is what lets `house_id` be treated as effectively required at the application level (see below) despite being a nullable column at the SQL level.

Since the `house_members` table (below) was added, every house also needs an owner to be reachable via the API at all — `CreateHouseWithOwner()` (`internal/db/houses.go`) is now the only way houses get created through normal use (via `POST /api/v1/houses`, registration, or first OIDC login), and always adds an owner atomically. `ensureDefaultHouse()`'s "Maison Principale" seed predates that requirement and is **not** given an owner, so on a fresh database it becomes a permanently orphaned row: nobody is a member of it, so it's invisible to every RBAC-scoped query and can never be renamed or deleted via the API either. This is harmless (one dead row, no data exposure) and deliberately left as-is rather than special-cased, since it's a leftover from before per-user houses existed and nothing in the app depends on it.

### `lists`

| Column | Type | Notes |
|---|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| `house_id` | `INTEGER` | `REFERENCES houses(id) ON DELETE CASCADE`; nullable at the SQL level only until `ensureDefaultHouse()` backfills it — see below |
| `name` | `TEXT NOT NULL` | |
| `type` | `TEXT NOT NULL DEFAULT 'shopping'` | `CHECK (type IN ('todo', 'shopping', 'groceries', 'recurring_shopping', 'custom'))` — `shopping`/`groceries`/`recurring_shopping` are all purchase-oriented types the frontend renders with different item-form fields (see the "Adaptation des Formulaires" note in CLAUDE.md); `custom` is accepted for forward compatibility but has no dedicated UI yet. This CHECK was widened from just `('shopping', 'todo')` in migration 6 (SQLite has no `ALTER TABLE ... ADD/DROP CONSTRAINT`, so widening it meant rebuilding the table — see `internal/db/migrate_list_types.go` and "Evolving the schema" below). |
| `custom_category_id` | `INTEGER` | nullable; `REFERENCES custom_categories(id) ON DELETE SET NULL` — see [`custom_categories`](#custom_categories) below. `ON DELETE SET NULL` (rather than `CASCADE`) is deliberate: deleting a category unassigns it from any list, it never deletes the list itself |
| `icon` | `TEXT NOT NULL DEFAULT ''` | freeform display icon (typically an emoji, e.g. `🛒`/`🖥️`/`📦`), same convention as `custom_categories.icon`; `''` means none was set |
| `created_at` | `TEXT NOT NULL` | ISO-8601 UTC, set by `strftime` default |
| `updated_at` | `TEXT NOT NULL` | updated explicitly by handlers on write |

### `items`

| Column | Type | Notes |
|---|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| `list_id` | `INTEGER NOT NULL` | `REFERENCES lists(id) ON DELETE CASCADE` |
| `title` | `TEXT NOT NULL` | |
| `url` | `TEXT` | nullable; used for shopping-list product links |
| `quantity` | `INTEGER NOT NULL DEFAULT 1` | |
| `price` | `REAL` | nullable; unit price in euros, optional, primarily used on `shopping` lists |
| `price_auto` | `INTEGER NOT NULL DEFAULT 0` | booleans stored as 0/1, same convention as `done`; `1` when `price` was filled in by `internal/scraper`'s background lookup rather than typed in by a user — see [API.md](API.md#post-apiv1items) |
| `image_url` | `TEXT` | nullable; product image found by the same background lookup as `price` (og:image/JSON-LD/twitter:image). Scraper-only — there is no request field to set it manually — and cleared back to `NULL` whenever `url` changes to something new, since an image found for the previous `url` no longer describes the item |
| `target_month` | `TEXT` | nullable; planned purchase month in `YYYY-MM` form (validated by `internal/validate.Month`), used by the "Budget & Prévisions Achats" planning view (`static/js/planning.js`) to group and total upcoming spending — see [API.md](API.md#post-apiv1items) |
| `done` | `INTEGER NOT NULL DEFAULT 0` | `CHECK (done IN (0, 1))` — booleans are stored as 0/1, converted to Go `bool` in `scanItem()` |
| `position` | `INTEGER NOT NULL DEFAULT 0` | manual ordering within a list; settable per-item via `POST`/`PUT`/`PATCH /api/v1/items`, or in bulk (a full drag-and-drop reorder) via `PUT /api/v1/lists/{id}/reorder` — see [API.md](API.md#put-apiv1listsidreorder) and `db.ReorderItems` |
| `due_date` | `TEXT` | nullable; `YYYY-MM-DD` (validated by `internal/validate.Date`). For a recurring item, this is the current occurrence's due date, advanced automatically on completion rather than edited directly — see `recurrence_rule` below and [API.md](API.md#recurring-items) |
| `is_recurring` | `INTEGER NOT NULL DEFAULT 0` | booleans stored as 0/1, same convention as `done`/`price_auto`. Not settable independently — always written as `recurrence_rule IS NOT NULL` (see `CreateItem`/`UpdateItem` in `internal/db/items.go`), so it can never disagree with `recurrence_rule` |
| `recurrence_rule` | `TEXT` | nullable; one of `DAILY`/`WEEKLY`/`MONTHLY`/`YEARLY` or the custom `EVERY_X_DAYS:<n>` form (validated by `internal/validate.Recurrence`). `NULL` means the item doesn't repeat |
| `recurrence_end_date` | `TEXT` | nullable; `YYYY-MM-DD` (validated by `internal/validate.Date`) — the last date a recurring item should recur on. Once the next computed occurrence would fall after it, the item stops advancing and simply stays `done` |
| `is_urgent` | `INTEGER NOT NULL DEFAULT 0` | booleans stored as 0/1, same convention as `done`/`price_auto`; flags an item needing attention right away, independent of every other column |
| `recurrence_lead_minutes` | `INTEGER` | nullable; overrides `NOTIF_RECURRING_TASK_LEAD_TIME` (see [`push_subscriptions`](#push_subscriptions) below) for this item alone — how long before `due_date` a reminder push is sent, in minutes. `NULL` means "use the instance-wide default"; meaningless unless `recurrence_rule` is also set |
| `due_reminder_sent_for` | `TEXT` | nullable; internal bookkeeping only — never exposed via the API or `models.Item`. Records the exact `due_date` value a recurring-item reminder push has already been sent for (see `internal/db.ListItemsForRecurringNotifyScan`/`MarkRecurringReminderSent`), so the periodic scan can exclude it with a plain `WHERE` clause rather than fetching every recurring item and filtering in Go. Storing the due date itself, rather than a boolean/timestamp flag, is what makes this re-arm automatically the moment `due_date` next changes for any reason — the stored value simply stops matching it, with no separate "clear the flag" step anywhere in the codebase |
| `target_price` | `REAL` | nullable; a user-set price threshold in euros — see [API.md](API.md#price-drop-alerts). Independent of `alert_on_price_drop`, the same "has a threshold" vs. "wants to be notified" relationship `due_date`/`recurrence_rule` have to each other |
| `alert_on_price_drop` | `INTEGER NOT NULL DEFAULT 0` | booleans stored as 0/1, same convention as `done`/`price_auto`/`is_urgent`; opts the item into a notification (in-app toast + push) once `price` reaches `target_price` — the actual comparison and delivery is `internal/handlers.checkPriceDropAlert`, called after every code path that can change `price`, `target_price`, or `alert_on_price_drop` itself (manual edits, the scraper's initial fill-in, and accepting a [price alert](API.md#price-alerts)) |
| `labels` | `TEXT NOT NULL DEFAULT '[]'` | a JSON array of strings — a freeform, user-managed set of short tags (e.g. `"Bio"`, `"Promo"`), independent of every other column. Stored as JSON rather than a normalized join table since a label has no attributes of its own worth a dedicated table for, the same reasoning `recurrence_rule`/`target_month` already established for a small freeform value with no cross-item querying need. Defaults to `'[]'` rather than `NULL` so `scanItem` never has to special-case a missing column; cleaned and deduplicated by `internal/validate.Labels` before every write via `internal/db.SetItemLabels`, a dedicated method rather than another `CreateItem`/`UpdateItem` parameter — see [API.md](API.md#labels) |
| `created_at` | `TEXT NOT NULL` | |
| `updated_at` | `TEXT NOT NULL` | |

### `users`

| Column | Type | Notes |
|---|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| `email` | `TEXT NOT NULL UNIQUE COLLATE NOCASE` | case-insensitive uniqueness |
| `password_hash` | `TEXT` | nullable; bcrypt hash, `NULL` for OIDC-only accounts |
| `oidc_subject` | `TEXT` | nullable; the provider's `sub` claim |
| `oidc_issuer` | `TEXT` | nullable; the provider's issuer URL |
| `display_name` | `TEXT NOT NULL DEFAULT ''` | |
| `is_admin` | `INTEGER NOT NULL DEFAULT 0` | 0/1 boolean; grants access to `/api/v1/admin/...` (see [docs/API.md](API.md#admin-settings)) |
| `keep_last_page` | `INTEGER NOT NULL DEFAULT 1` | 0/1 boolean; whether the frontend reopens on the user's last-visited dashboard tab/list instead of always landing on the dashboard — settable via `PATCH /api/v1/me` (see [docs/API.md](API.md#patch-apiv1me)) |
| `created_at` | `TEXT NOT NULL` | ISO-8601 UTC, set by `strftime` default |

`CHECK (password_hash IS NOT NULL OR oidc_subject IS NOT NULL)` — every user must have at least one way to authenticate. A unique partial index, `idx_users_oidc_identity` on `(oidc_issuer, oidc_subject) WHERE oidc_subject IS NOT NULL`, enforces that an OIDC identity is unique *within* its issuer (not globally, since two different providers could coincidentally reuse the same `sub` string).

`is_admin` is never settable through the registration or profile API — `internal/db.CreateUser` sets it automatically (inside a transaction, so two concurrent registrations against a brand new database can't race each other into both becoming admin) for the very first account ever created, local or OIDC-provisioned, and defaults to `0` for every account after that. There is currently no endpoint to grant or revoke it for an existing account.

Unlike `is_admin`, `keep_last_page` is an ordinary per-user preference: it defaults to enabled (`1`) for every account and is freely settable by that account itself via `PATCH /api/v1/me`. Which tab or list was actually last visited is never stored here (or anywhere server-side) — that's tracked purely in the browser's `localStorage`, per the frontend's existing per-browser-only convention for this kind of state (current house, theme, language); this column only records the on/off choice itself, so it follows the user across devices/browsers while the actual "where to reopen" stays local to each one.

### `sessions`

| Column | Type | Notes |
|---|---|---|
| `id` | `TEXT PRIMARY KEY` | hex-encoded SHA-256 of the raw cookie token — **never the raw token itself**, so a database leak alone can't hand out live sessions |
| `user_id` | `INTEGER NOT NULL` | `REFERENCES users(id) ON DELETE CASCADE` |
| `expires_at` | `TEXT NOT NULL` | same `strftime` format as other timestamps, so it stays lexicographically comparable to `strftime('now')` in the lookup query |
| `created_at` | `TEXT NOT NULL` | |

Indexed by `user_id` (`idx_sessions_user_id`). Deleting a user cascades to delete all their sessions (logged out everywhere).

### `house_members`

| Column | Type | Notes |
|---|---|---|
| `house_id` | `INTEGER NOT NULL` | `REFERENCES houses(id) ON DELETE CASCADE` |
| `user_id` | `INTEGER NOT NULL` | `REFERENCES users(id) ON DELETE CASCADE` |
| `role` | `TEXT NOT NULL DEFAULT 'member'` | `CHECK (role IN ('owner', 'member'))` |
| `created_at` | `TEXT NOT NULL` | |

`PRIMARY KEY (house_id, user_id)` — a user can only have one membership row per house. Indexed additionally by `user_id` (`idx_house_members_user_id`), for the "which houses is this user in" queries the RBAC layer runs on every request. This is the join table access control (`internal/handlers` — see `authorizeHouseAccess`/`authorizeHouseOwner`) is built on: a house's lists/items are only visible to its members, and only its owner can rename/delete the house or manage its membership. See [docs/API.md](API.md#houses) for the resulting endpoint behavior.

### `price_alerts`

| Column | Type | Notes |
|---|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| `item_id` | `INTEGER NOT NULL` | `REFERENCES items(id) ON DELETE CASCADE` |
| `original_price` | `REAL NOT NULL` | snapshot of the item's `price` when the alert was created |
| `found_price` | `REAL NOT NULL` | the lower price found at `source_url` |
| `source_url` | `TEXT NOT NULL` | the item's `url` at the time it was scraped |
| `status` | `TEXT NOT NULL DEFAULT 'pending'` | `CHECK (status IN ('pending', 'accepted', 'rejected'))` |
| `created_at` | `TEXT NOT NULL` | |

A row here is created by `internal/handlers`' periodic or on-demand price-drop check (`internal/handlers/price_alerts.go` — see [CLAUDE.md](../CLAUDE.md)) whenever `internal/scraper.FetchProductInfo` finds a price on `item_id`'s `url` lower than its currently recorded `price`, and only if that item doesn't already have a `pending` alert (`CreatePriceAlertIfNonePending`'s `WHERE NOT EXISTS` guard) — otherwise a repeat periodic scan would spawn a fresh alert every run before the existing one is resolved. `original_price` deliberately isn't re-read from `items.price` at accept/reject time: it's what the comparison was actually made against, so it must stay fixed even if the item's price changes in the meantime. Accepting an alert (`AcceptPriceAlert`) applies `found_price` to the item (also setting `price_auto = 1`) and flips the alert to `accepted` inside a single transaction — the same "must not leave inconsistent state" reasoning as `CreateHouseWithOwner`. Once an alert leaves `pending` (either direction) it can never be re-actioned; both `AcceptPriceAlert` and `RejectPriceAlert` guard on `WHERE status = 'pending'` and return `ErrNotFound` otherwise. See [docs/API.md](API.md#price-alerts) for the resulting endpoint behavior.

### `custom_categories`

| Column | Type | Notes |
|---|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| `user_id` | `INTEGER NOT NULL` | `REFERENCES users(id) ON DELETE CASCADE` |
| `name` | `TEXT NOT NULL` | |
| `icon` | `TEXT NOT NULL DEFAULT ''` | freeform, typically an emoji; `''` means no icon |
| `color` | `TEXT NOT NULL DEFAULT ''` | hex color (`#RGB`/`#RRGGBB`, validated by `internal/validate.Color`); `''` means no color |
| `position` | `INTEGER NOT NULL DEFAULT 0` | manual ordering, same convention as `items.position` |
| `created_at` | `TEXT NOT NULL` | ISO-8601 UTC, set by `strftime` default |

A brand new table, introduced by migration 7. Unlike every other grouping concept in this schema (`houses`, shared by all `house_members`), a custom category is owned by exactly one user and never shared: `user_id` is who created it, and every lookup/mutation in `internal/db/custom_categories.go` (`GetCustomCategoryForUser`, `UpdateCustomCategoryForUser`, `DeleteCustomCategoryForUser`) scopes its `WHERE` clause on it, so a category can only ever be found, edited, or deleted by its owner — a mismatched `user_id` comes back as the same `ErrNotFound` as a nonexistent id, never a separate "forbidden" outcome, so a lookup can't be used to probe for another user's category ids. `lists.custom_category_id` (above) is how a list attaches to one; deleting a category clears that column on any list that referenced it (`ON DELETE SET NULL`) rather than cascading. Indexed by `user_id` (`idx_custom_categories_user_id`) for the "list my categories" query, and `lists.custom_category_id` is separately indexed (`idx_lists_custom_category_id`) for the reverse join `GET /api/v1/lists` uses to embed a list's category. See [docs/API.md](API.md#custom-categories) for the resulting endpoint behavior.

### `space_shares` and `list_shares`

| Column | Type | Notes |
|---|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| `custom_category_id` (space_shares) / `list_id` (list_shares) | `INTEGER NOT NULL` | `REFERENCES custom_categories(id)` / `REFERENCES lists(id)`, `ON DELETE CASCADE` |
| `shared_with_user_id` | `INTEGER NOT NULL` | `REFERENCES users(id) ON DELETE CASCADE` |
| `permission` | `TEXT NOT NULL DEFAULT 'read'` | `CHECK (permission IN ('read', 'write'))` |
| `created_at` | `TEXT NOT NULL` | ISO-8601 UTC, set by `strftime` default |
| `is_pinned_to_dashboard` | `INTEGER NOT NULL DEFAULT 0` | `list_shares`: added by migration 11; `space_shares`: added by migration 12; see below |

Two brand new tables, introduced by migration 9, implementing granular sharing on top of house membership: a Space (`custom_categories` row) or an individual List can be shared directly with one other user, giving them `read` or `write` access to it without adding them to the whole parent house — see the "granular sharing" bullet in [CLAUDE.md](../CLAUDE.md) and [docs/API.md](API.md#sharing) for the full design. Each table carries a `UNIQUE (custom_category_id, shared_with_user_id)` / `UNIQUE (list_id, shared_with_user_id)` constraint, so `internal/db.CreateOrUpdateSpaceShare`/`CreateOrUpdateListShare` can `INSERT ... ON CONFLICT ... DO UPDATE SET permission = excluded.permission` — re-sharing with a different permission updates the existing row instead of erroring or creating a duplicate.

`internal/db.AccessLevelForList` is the single place that combines all three access sources into one read/write verdict for a given user and list: house membership (always `write`, exactly as it always implied before this feature), a `list_shares` row on the list itself, and a `space_shares` row on the list's `custom_category_id` (if any) — whichever of the two is present, and the higher of the two if both are. `internal/db.ListSharedListsForUser` is the reverse query the "Partagé avec moi" tab uses: every list reachable via either share table, excluding any whose house the caller is already a plain member of (already visible the ordinary way, so repeating it there would just be noise). `internal/db.ListSpacesVisibleToUser` is the Space-level equivalent, backing `GET /api/v1/custom-categories?shared_with_me=true` — as of the `space_house_pins` addition below, it also unions in every Space the caller can see purely through House membership rather than an explicit `space_shares` grant, tagged `access_source: "house_member"`.

`list_shares.is_pinned_to_dashboard` (migration 11) lets the *recipient* of a List share — direct or reached only via a shared Space — choose to have that one list show up on their own dashboard alongside their house's own lists, instead of only in the "Partagé avec moi" tab — set via `PATCH /api/v1/lists/{id}/share/pin` (`internal/handlers.handleListSharePin`/`internal/db.SetListSharePinned`), see [docs/API.md](API.md#patch-apiv1listsidsharepin). Originally this had no `space_shares` equivalent and was scoped to a direct List share only, since a list reached only through a shared Space had no `list_shares` row of its own to carry the flag; `SetListSharePinned` now auto-creates that row on demand in that case, scoped to exactly the permission the Space already grants for the list (via a `lists JOIN space_shares` lookup on the list's `custom_category_id`), so the insert can never itself change the recipient's `AccessLevelForList` verdict — it just gives the individual pin flag somewhere to live.

`space_shares.is_pinned_to_dashboard` (migration 12) is the Space-level counterpart: the *recipient* of a Space share can pin the whole Space via `PATCH /api/v1/custom-categories/{id}/share/pin` (`internal/handlers.handleSpaceSharePin`/`internal/db.SetSpaceSharePinned`), see [docs/API.md](API.md#patch-apiv1custom-categoriesidsharepin). Unlike a List, a Space reached via an explicit share has exactly one access path, so there's no auto-create fallback to speak of there — a `space_shares` row always already exists if the recipient was ever explicitly shared the Space at all (a *different* access path, House membership, is handled by `space_house_pins` below instead, precisely because it has no `space_shares` row to speak of). `ListSharedListsForUser` derives a `space_share`-sourced list's own `IsPinnedToDashboard` directly from *this* column (`ss.is_pinned_to_dashboard`, not a hardcoded `false` any more) rather than from any `list_shares` row on that list — pinning a Space is what lets every list reachable through it (present and future, no per-list action needed) come back pinned in one step. When a list is reachable both via its own `list_shares` row and via its Space, whichever source says pinned wins (the same "OR", not "AND", the higher-of-two-permissions dedup already uses) — a practical consequence is that unpinning one list individually cannot override its parent Space being pinned as a whole; the Space-level pin is the "master" pin for everything reachable through it.

### `space_house_pins`

| Column | Type | Notes |
|---|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| `custom_category_id` | `INTEGER NOT NULL` | `REFERENCES custom_categories(id) ON DELETE CASCADE` |
| `user_id` | `INTEGER NOT NULL` | `REFERENCES users(id) ON DELETE CASCADE` |
| `created_at` | `TEXT NOT NULL` | ISO-8601 UTC, set by `strftime` default |

A brand new table, introduced by migration 13, extending Space pinning to a House member who neither owns a Space nor holds a `space_shares` grant on it, but can still see it because at least one of its tagged lists belongs to a House they're a member of (`internal/db.spaceAccessibleViaHouse`) — e.g. one House member tags a shared list with their own personal Space, and a fellow member (never explicitly invited to that Space) can still discover and pin it. Unlike `space_shares`/`list_shares`, `space_house_pins` has no `permission` column at all: it doesn't grant any access on its own (a House member's actual read/write access to the underlying lists is already fully determined by ordinary House membership via `internal/db.AccessLevelForList`, completely independent of this table) — it exists purely to record a per-user dashboard-pin *preference*, so it carries nothing else. Row presence means pinned; there is no separate boolean flag the way `list_shares`/`space_shares.is_pinned_to_dashboard` use, so `internal/db.SetSpaceHousePinned` unpins by `DELETE`ing the row outright rather than flipping it, and pins via `INSERT ... ON CONFLICT (custom_category_id, user_id) DO NOTHING` (safe against two concurrent pin requests). `UNIQUE (custom_category_id, user_id)` backs that upsert; `idx_space_house_pins_user_id` (on `user_id`) supports the "which Spaces has this user pinned" queries below.

Because access here is derived live from House membership rather than stored, `spaceAccessibleViaHouse` — and every query that reads this table — is always evaluated against the *current* House membership/list-tagging state, not a snapshot taken when the row was created: if the tagged list is later moved to a different Space, or the pinning user later leaves the House, their `space_house_pins` row simply becomes inert (no longer surfaced anywhere) rather than needing active cleanup. `spaceAccessibleViaHouse` also explicitly excludes the category's own owner (`cc.user_id != ?`, mirroring `ListSpacesVisibleToUser`'s own exclusion) — without it, an owner would typically also be a House member of wherever their own Space is used, and could otherwise "pin" their own already-visible Space through this fallback for no purpose. `internal/handlers.handleSpaceSharePin` tries `SetSpaceSharePinned` (the explicit-share path) first and only falls back to `SetSpaceHousePinned` if that reports `ErrNotFound` — an explicit grant, if one exists, always takes priority. `internal/db.ListSpacesVisibleToUser`'s `access_source: "house_member"` branch and `internal/db.ListPinnedHouseSpaceLists` (backing `GET /api/v1/lists?pinned_house_spaces=true`, see [docs/API.md](API.md#get-apiv1lists)) are what surface a pin made this way — a `house_member`-sourced list is often already visible via the caller's own currently-selected House on the ordinary dashboard, so the practical effect of pinning is mainly felt when a *different* House the caller also belongs to is the one currently selected; `static/js/app.js`'s `loadDashboard` dedupes by list id across all three sources it merges (own-House listing, pinned shares, pinned House spaces) so a list already visible one way is never shown twice.

### `system_settings`

| Column | Type | Notes |
|---|---|---|
| `key` | `TEXT PRIMARY KEY` | e.g. `instance_name`, `registration_open`, `oidc_enabled`, `oidc_issuer`, `oidc_client_id`, `oidc_client_secret` — see `internal/settings.Resolve` |
| `value` | `TEXT NOT NULL` | booleans stored as the literal strings `"true"`/`"false"` (parsed with `strconv.ParseBool`), everything else as plain text |
| `updated_at` | `TEXT NOT NULL` | ISO-8601 UTC, set by `strftime` default, refreshed on every upsert |

A generic key/value store for runtime settings an admin can change from the "Paramètres du Système" panel (`/api/v1/admin/settings`, see [docs/API.md](API.md#admin-settings)) without a server restart — currently the OIDC/SSO configuration, whether local registration is open, and the instance's display name. A key with a row here always overrides its equivalent environment variable (`internal/config`); a key with no row falls back to that variable (or its own default) instead — this per-key fallback is why the table can have, say, only `oidc_issuer` overridden while `oidc_client_id`/`oidc_client_secret` still come from `OIDC_CLIENT_ID`/`OIDC_CLIENT_SECRET`. Every write goes through `internal/db.SetSettings`, which upserts a whole batch of keys inside a single transaction so a multi-field `PATCH` can never leave the table with only some of its keys updated. Nothing outside `internal/handlers/admin.go` writes to this table.

### `push_subscriptions`

| Column | Type | Notes |
|---|---|---|
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| `user_id` | `INTEGER NOT NULL` | `REFERENCES users(id) ON DELETE CASCADE` |
| `endpoint` | `TEXT NOT NULL` | the push service URL a browser's `PushSubscription.endpoint` reports; validated `https://` at the API boundary (`internal/handlers.handlePushSubscribe`) and re-validated by `internal/webpush.Send` before ever being dialed (see the SSRF note below) |
| `p256dh` | `TEXT NOT NULL` | base64url-encoded subscription public key, from `PushSubscription.getKey('p256dh')` |
| `auth` | `TEXT NOT NULL` | base64url-encoded subscription auth secret, from `PushSubscription.getKey('auth')` |
| `user_agent` | `TEXT NOT NULL DEFAULT ''` | the subscribing browser's `User-Agent` at subscribe time, purely informational (no code branches on it) |
| `created_at` | `TEXT NOT NULL` | ISO-8601 UTC, set by `strftime` default |

`UNIQUE (user_id, endpoint)` — `internal/db.CreatePushSubscription` upserts on it, so a browser re-subscribing the same endpoint (a key rotation, or simply re-enabling push after having granted it once already) refreshes the stored `p256dh`/`auth`/`user_agent` in place rather than erroring or creating a duplicate row. `endpoint` is a genuinely user-influenced value this server later makes an outbound HTTPS request to (`internal/webpush.Send`, delivering the encrypted push) — the same SSRF risk category as an item's `url` (see CLAUDE.md's SSRF security rule), defended the same way: `internal/webpush` carries its own independent copy of the resolve-then-dial-the-checked-IP guard `internal/scraper` already uses, rather than trusting the endpoint's hostname to actually resolve to a public push service at request time. Deleting a `user` cascades to delete all their subscriptions (they simply stop receiving pushes); a subscription a push service itself reports gone (`404`/`410` — RFC 8030 §7.3) is deleted by `internal/handlers.sendToUsers` regardless of which user it belonged to.

Indexes: `idx_items_list_id` (on `items.list_id`), `idx_items_target_month` (on `items.target_month`), `idx_lists_type` (on `lists.type`), `idx_lists_house_id` (on `lists.house_id`), `idx_lists_custom_category_id` (on `lists.custom_category_id`), `idx_sessions_user_id`, `idx_house_members_user_id`, `idx_users_oidc_identity` (unique, partial), `idx_price_alerts_item_id`, `idx_price_alerts_status`, `idx_custom_categories_user_id`, `idx_space_shares_user_id` (on `space_shares.shared_with_user_id`), `idx_list_shares_user_id` (on `list_shares.shared_with_user_id`), `idx_space_house_pins_user_id` (on `space_house_pins.user_id`), `idx_push_subscriptions_user_id` (on `push_subscriptions.user_id`).

Each table above was built up over several migrations rather than in one `CREATE TABLE` — see [Evolving the schema](#evolving-the-schema) and [internal/db/migrations/](../internal/db/migrations/) for the full, real sequence (`houses`/`lists`/`items`/`users`/`sessions`/`house_members` in migration 1; `price`/`price_auto`/`image_url`/`target_month` folded into `items`' initial shape in migration 1 as well; `due_date`/`is_recurring`/`recurrence_rule`/`recurrence_end_date` in migration 2; `is_urgent` in migration 3; `price_alerts` in migration 4; `is_admin`/`system_settings` in migration 5; the `lists.type` `CHECK` widening in migration 6; `custom_categories`/`lists.custom_category_id` in migration 7; `lists.icon` in migration 8; `space_shares`/`list_shares` in migration 9; `users.keep_last_page` in migration 10; `list_shares.is_pinned_to_dashboard` in migration 11; `space_shares.is_pinned_to_dashboard` in migration 12; `space_house_pins` in migration 13; `pending_invitations` in migration 14; `push_subscriptions`/`items.recurrence_lead_minutes`/`items.due_reminder_sent_for` in migration 15; `items.target_price`/`items.alert_on_price_drop` in migration 16; `items.labels` in migration 18). Each migration's own file carries the indexes its columns need, added in the same migration as the column itself now that ordering is enforced by the engine rather than by careful startup-sequencing in Go.

Deleting a row from `houses` cascades to delete all its `lists` (which in turn cascades to delete all their `items` and `list_shares`) and all its `house_members` rows — requiring `foreign_keys=ON` (already set on every connection), and working transitively without any extra code. Deleting a `custom_categories` row cascades to delete its `space_shares` and `space_house_pins` rows (but only detaches, via `ON DELETE SET NULL`, the `lists.custom_category_id` of any list that referenced it — see the `custom_categories` section above). Deleting a `user` cascades to their `sessions`, `house_members`, and their own `custom_categories` (which, per above, further cascades to that category's `space_shares`/`space_house_pins` and detaches any list's `custom_category_id` that referenced it), as well as any `space_shares`/`list_shares`/`space_house_pins` row where they are the recipient (`shared_with_user_id`/`user_id`); houses they owned are **not** deleted (a house survives its owner's account being removed, though it may then be left without an owner — that's an acknowledged edge case, not actively guarded against, since account deletion isn't exposed via the API today).

## Evolving the schema

Trakka has a real versioned migration engine (`internal/db/migrate.go`), not the single idempotent `schema.sql` this project used before. Every startup:

1. Reads `PRAGMA user_version` (an integer stored in the database file's own header).
2. If it's already at the latest known migration version, does nothing — the common case, on every ordinary restart.
3. Otherwise applies each migration newer than the current version, in order, each inside its own transaction that also bumps `user_version` on success — a crash or error partway through can never leave the schema and its recorded version disagreeing with each other, since `PRAGMA user_version` itself participates in the transaction (unlike, say, `PRAGMA foreign_keys`, which does not).

**Adding a new migration** normally means nothing more than dropping a new file at `internal/db/migrations/NNNN_description.sql` (the next sequential 4-digit number, a lowercase/underscore name) — it's picked up automatically via `//go:embed migrations/*.sql`, no Go code to touch. Unlike the old `addColumnIfMissing()`-guarded approach, the script itself needs **no** existence guards (no `IF NOT EXISTS`, no `ADD COLUMN IF NOT EXISTS`) — versioning already guarantees it runs at most once per database, ever:

- New tables/indexes: plain `CREATE TABLE` / `CREATE INDEX`.
- New columns on an existing table: plain `ALTER TABLE ... ADD COLUMN`.
- Widening a `CHECK` constraint (e.g. `lists.type`, migration 6): SQLite has no `ALTER TABLE ... ADD/DROP CONSTRAINT`, so this still means rebuilding the table — and that rebuild needs `PRAGMA foreign_keys = OFF` set *outside* any transaction, which the engine's generic one-script-one-transaction wrapper can't express. This is the one documented exception: register it in `goMigrations` in `internal/db/migrate.go` and write it as an ordinary Go function instead of a `.sql` file — see `internal/db/migrate_list_types.go` for the concrete pattern (create the replacement table under a *temporary* name, copy every row preserving its id, `DROP TABLE` the *old* table — never rename it away — then `RENAME` the temporary one into the now-free original name, verifying with `PRAGMA foreign_key_check` before committing). The ordering there was arrived at by testing, not just reading the docs: renaming the *original* table out of the way first makes SQLite rewrite every other table's foreign key definitions that pointed at it (e.g. `items.list_id`) to reference the temporary name instead — confirmed empirically, and `PRAGMA foreign_keys = OFF` does *not* suppress this, only `PRAGMA legacy_alter_table` would.
- Never edit an already-shipped migration file in place — a database that already applied it recorded that in `user_version` and will never run it again, so an edit would only affect databases that haven't reached that version yet, silently diverging from ones that have. Add a new migration instead, exactly as you would with any other schema change management tool.

**Adopting an existing database.** A database created by Trakka's pre-migration-engine code has tables already but `PRAGMA user_version` still at `0` (SQLite's default for a file that's never had it set). `migrate()` detects this (`hasExistingSchema`: any user table already present) and stamps it directly at the latest version without running any migration SQL, rather than replaying scripts that would immediately fail on already-existing tables/columns. This is safe because the old code always applied every schema change unconditionally on every startup and refused to start at all on any failure, so any database that ever started successfully already has the full schema.

## Backup

The whole database is the single file at `DB_PATH` (plus its `-wal`/`-shm` sidecar files while the process is running, due to WAL mode).

**Automatic, pre-migration backups.** Whenever a startup is about to apply a migration to an already-versioned database (i.e. not a brand new empty file, and not the one-time "adopt an existing database" case above — see "Evolving the schema"), `internal/db/migrate.go`'s `backupBeforeMigration` snapshots the live database first, via `VACUUM INTO` — a single ordinary SQL statement (SQLite 3.27+) that produces a consistent, compacted copy of the live database, safe to run against an open, WAL-mode connection, needing nothing beyond `database/sql` (no CGO backup API). The snapshot lands at `<directory containing DB_PATH>/backups/trakka-v<from>-to-v<to>-<UTC timestamp>.db` — for the default `DB_PATH=/data/trakka.db` this is `/data/backups/`, already writable under `compose.yml`'s `read_only: true` root filesystem since it's a subdirectory of the `/data` named volume. Nothing currently prunes old backups automatically — clean out `/data/backups/` periodically if disk space matters for your deployment.

**Manual backups.** To back up safely while Trakka is running, either:
- use SQLite's own backup mechanism (e.g. `sqlite3 /data/trakka.db ".backup /path/to/copy.db"`, or the same `VACUUM INTO` the automatic mechanism above uses), or
- stop the container and copy the volume's contents.

Copying `trakka.db` alone while the process is live, without also capturing `trakka.db-wal`, can produce an inconsistent copy.
