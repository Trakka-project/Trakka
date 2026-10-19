# Manual QA Test Guide — Price Drop Alerts (Target Price)

This document is a step-by-step manual test recipe for Trakka's **"price drop alert"** feature (`target_price` / `alert_on_price_drop` on an item). It's meant for a human tester who hasn't necessarily read the code, and closes an item left open in [CLAUDE.md](../CLAUDE.md): *"Price drop alerts (per-item target price) have never been exercised in a browser or against a real subscribed push service."*

Per the "Documentation" convention in [CLAUDE.md](../CLAUDE.md), this guide is written in English like the rest of the project's technical documentation — only the exact UI strings you should see on screen (badges, toasts, labels, error messages) are quoted verbatim in French, since Trakka's UI is French-only.

Every `curl` command below was actually run against a fresh local instance while writing this guide (not just reasoned about) — see section 4 for the two real gotchas that surfaced doing that.

## 1. Objective

Verify that:

1. a target price can be set and the alert opted into on an item;
2. as soon as the item's price reaches that threshold, a visual badge appears, an in-app toast is shown, and a push notification is sent;
3. as long as the price stays under the threshold, the alert does not re-fire on every subsequent edit;
4. a price drop detected automatically (scraping the product page) fires the same alert as if the user had edited the price by hand.

## 2. Two mechanisms not to confuse

Trakka has **two** distinct price-related systems — this guide is about the first one, but Case 4 below also touches the second:

| | **Personal target-price alert** (this guide's subject) | **Better-deal detection** (`price_alerts`) |
|---|---|---|
| Fields | `items.target_price`, `items.alert_on_price_drop` | separate `price_alerts` table |
| Who sets the threshold? | The user, manually (a number in €) | Nobody — an automatic comparison against a price found on the product page |
| Trigger | The moment `price <= target_price`, immediately | A periodic scan (or `POST /items/{id}/price-check`) creates a **pending** alert, which the user must then accept or reject (the 🔔 bell in the header) |
| Action required | None — it's automatic the moment the condition is true | The user must click "Appliquer le nouveau prix" for `price` to actually change |

See [docs/API.md#price-drop-alerts](API.md#price-drop-alerts) and [docs/API.md#price-alerts](API.md#price-alerts) for the full endpoint reference for each.

## 3. Prerequisites

- The Trakka server running locally:
  ```bash
  PORT=8080 DB_PATH=./trakka.db STATIC_DIR=./static TEMPLATES_DIR=./templates go run ./cmd/server
  ```
- `curl` installed, with a cookie jar file (`cookies.txt`) to carry the session across calls.
- (Optional, for the SQL shortcuts in section 6) the `sqlite3` CLI installed on your machine — a separate system tool, unrelated to the server's pure-Go driver (`modernc.org/sqlite`); not required to run Trakka itself, only to inspect/edit the database by hand during testing.
- (Optional, to verify the push notification) `VAPID_PUBLIC_KEY`/`VAPID_PRIVATE_KEY`/`VAPID_SUBJECT` configured (`trakka -generate-vapid-keys` to generate a pair), and a real browser with the "Activer les notifications push" toggle enabled in Settings. Without this, the server simply has nothing to deliver a push to — at minimum still test the in-app toast, which doesn't depend on VAPID being configured.

## 4. Quick setup (bootstrap)

### What is `cookies.txt`?

It's a plain text file curl creates and maintains for you — a "cookie jar" in curl's own terminology — in the [Netscape cookie file format](https://curl.se/docs/http-cookies.html). It does not exist until the first command that passes `-c cookies.txt` runs; from then on:
- `-c cookies.txt` tells curl to **write** every cookie the server sets in that response into the file (overwriting it with the new full set each time — curl always rewrites the whole jar, not just appended lines).
- `-b cookies.txt` tells curl to **read** the file and send whatever cookies it contains back to the server on that request.

Nothing else in this guide creates or touches this file by hand — it's purely curl's own bookkeeping. Run `cat cookies.txt` at any point to see exactly what's in it; right after step 1 below it should contain one line for `trakka_csrf`, and right after a successful step 2 it should have *also* gained a `trakka_session` line — if that second line is missing after step 2, the registration did not actually succeed, no matter what step 2's own output showed.

### Two things easy to get wrong here

Both of these were caught by actually running this bootstrap, not just by reading the code:

1. **`/auth/register` (like `/auth/login`) requires a `csrf_token` form field** matching the `trakka_csrf` cookie a prior `GET /auth/login` set — see [docs/API.md#authentication](API.md#authentication) and the "CSRF" security rule in [CLAUDE.md](../CLAUDE.md) for why. Skipping this is the single most common way this bootstrap silently fails: the `POST` gets redirected straight to `/auth/login?...&error=csrf_failed` with **no** cookie set at all — and since a bare `curl` with no `-i`/`-v` prints nothing for a redirect with an empty body, this looks identical to a successful request until the very next call comes back `401`.

   **The trap inside this trap** (this is almost certainly what happened if you got `csrf_failed` despite fetching the login page first): the two lines of step 1 below — fetching the page and extracting the token — must always be run **together, every single time**, never one without the other. `GET /auth/login` mints a **brand-new** `trakka_csrf` cookie on every call that doesn't already carry a valid one back (i.e. every time you run it without `-b cookies.txt`, which step 1 deliberately doesn't use, since it's meant to be the very first call). If you re-run just the `curl ... -o login.html` line again later — to refresh `login.html`, or just by re-running an old command from your shell history — without also re-running the `CSRF_TOKEN=$(...)` line right after it, `cookies.txt` now holds a *new* token while your shell's `$CSRF_TOKEN` variable still holds the *old* one from before. They no longer match, and `checkCSRFToken` rejects the submission. This was confirmed by deliberately reproducing it: re-fetching the page alone changes the cookie file's token every time, even though `login.html`'s content looks identical. If this happens, just re-run both lines of step 1 together again immediately before retrying step 2 — and check with `echo "$CSRF_TOKEN"` that it's non-empty and matches the value curl just wrote to `cookies.txt`.
2. **Your own house is not `house_id: 1`.** A fresh database seeds a permanently orphaned "Maison Principale" row at id `1` before any account exists (see the "Houses" bullet in [CLAUDE.md](../CLAUDE.md) — it's harmless and invisible everywhere else, since nobody is ever a member of it), so the first real account's own "Ma Maison" actually lands at id `2` or higher. Always read the id back from `GET /api/v1/houses` rather than assuming `1`.

The recipe below captures every id into a shell variable for exactly this reason — copy the **whole** block and run it as one unit rather than hand-substituting numbers or re-running individual lines later:

```bash
# 1. Load the login page first to obtain the CSRF cookie + token — these two
#    lines are a single unit, always run together (see the trap above)
curl -c cookies.txt -s http://localhost:8080/auth/login -o login.html
CSRF_TOKEN=$(grep -o 'name="csrf_token" value="[^"]*"' login.html | head -1 | sed -E 's/.*value="([^"]*)"/\1/')
echo "CSRF_TOKEN=${CSRF_TOKEN}"          # sanity check: must be non-empty
cat cookies.txt                          # sanity check: the token above must appear on the trakka_csrf line

# 2. Create a test account (also creates a personal house, "Ma Maison")
curl -b cookies.txt -c cookies.txt -i -X POST http://localhost:8080/auth/register \
  -d "email=qa@example.com&password=password123&password_confirm=password123&display_name=QA&csrf_token=${CSRF_TOKEN}"
# The -i above prints the response headers — look for "HTTP/1.1 302 Found" with
# "Location: /" (success) rather than "Location: /auth/login?...&error=..." (failure).
# If you see "error=csrf_failed" here, go back and re-run step 1's two lines
# together, then retry step 2 — do not reuse an old $CSRF_TOKEN.

# 3. Confirm the session actually works
cat cookies.txt                          # should now also contain a trakka_session line
curl -b cookies.txt -s http://localhost:8080/api/v1/me
# => { "id": 1, "email": "qa@example.com", ... }  — a 401 here means step 2 failed; re-check the Location header above

# 4. Fetch the id of the auto-created house (do NOT assume 1 — see above)
HOUSE_ID=$(curl -b cookies.txt -s http://localhost:8080/api/v1/houses | grep -o '"id":[0-9]*' | head -1 | grep -o '[0-9]*')
echo "HOUSE_ID=${HOUSE_ID}"

# 5. Create a dedicated shopping list for testing
LIST_ID=$(curl -b cookies.txt -s -X POST http://localhost:8080/api/v1/lists \
  -d "{\"name\": \"QA Alertes Prix\", \"type\": \"shopping\", \"house_id\": ${HOUSE_ID}}" \
  | grep -o '"id":[0-9]*' | head -1 | grep -o '[0-9]*')
echo "LIST_ID=${LIST_ID}"
```

Keep `cookies.txt` on disk, and keep `$CSRF_TOKEN`/`$HOUSE_ID`/`$LIST_ID`/`$ITEM_ID` set **in the same shell** for every remaining command in this guide — `cookies.txt` survives on disk between commands (and between terminal sessions), but shell variables do not: opening a new terminal tab/window, or a new SSH session, starts with none of them set. If a later command in this guide suddenly fails with a `401` or an obviously wrong id, first run `echo "$HOUSE_ID $LIST_ID $ITEM_ID"` to check they're still set in *this* shell before assuming something in the API broke. If you already have an account from a previous run, skip step 2 and do a normal login instead (same two-step CSRF dance, see [docs/API.md#authentication](API.md#authentication)) — re-registering the same email fails with `email_taken`, which is a different redirect than `csrf_failed` but is just as silent without `-i`.

---

## 5. Test scenarios

### Case 1 — Setting a price alert on an item

**Steps (via the UI):**
1. Open the "QA Alertes Prix" list.
2. In the quick-add bar, click the `⚙️` button (advanced options) to expand the panel.
3. Fill in: title `Casque audio`, price `100`, and the **"Alerte si prix < X €"** field (placeholder of the `#item-target-price` input) with `80`.
4. Submit.

**Equivalent `curl`:**
```bash
ITEM_ID=$(curl -b cookies.txt -s -X POST http://localhost:8080/api/v1/items \
  -d "{\"list_id\": ${LIST_ID}, \"title\": \"Casque audio\", \"price\": 100, \"target_price\": 80, \"alert_on_price_drop\": true}" \
  | grep -o '"id":[0-9]*' | head -1 | grep -o '[0-9]*')
echo "ITEM_ID=${ITEM_ID}"
```

**Expected result:**
- The "Casque audio" item appears in the list with its price displayed normally (`100,00 €`), **without** an alert badge.
- The JSON response contains `"target_price": 80`, `"alert_on_price_drop": true`, and does **not** contain `"price_alert_triggered"` (that field is only present in a response when it's `true` — see `omitempty` on `models.Item.PriceAlertTriggered`).
- Keep `$ITEM_ID` set — every remaining case reuses it.

---

### Case 2 — Triggering the alert (price ≤ target price)

**Steps (via the UI):**
1. Open the "Casque audio" item (kebab `[⋮]` or tap the row → "✏️ Modifier").
2. Change the price from `100` to `75` and save.

**Equivalent `curl`:**
```bash
curl -b cookies.txt -s -X PATCH "http://localhost:8080/api/v1/items/${ITEM_ID}" -d '{"price": 75}'
```

**Expected result:**

| Check | Detail |
|---|---|
| **Visual badge** | An amber 🔥 badge appears on the item's row, with the exact text: `🔥 Bonne affaire : 75,00 € (sous le seuil de 80,00 €)` |
| **In-app toast** | A success toast appears immediately after saving: `🔥 « Casque audio » est passé à 75,00 € (sous votre seuil de 80,00 €) !` |
| **JSON response** | Contains `"price": 75, "price_alert_triggered": true` |
| **Push notification** | If the push toggle is enabled on at least one account with access to the list: a real system notification arrives, titled `🔥 Bonne affaire !`, body `« Casque audio » est passé à 75.00 € (sous votre seuil de 80.00 €)`, and clicking it opens/focuses Trakka on the relevant list, scrolled to the item and highlighting it (`/?list=<id>&item=<id>`) |

> The exact price shown in the badge/toast comes from the frontend's EUR formatter (`75,00 €`), while the push notification's body is composed server-side with `%.2f` (`75.00 €`, a dot instead of a comma) — that's an expected difference between the two texts, not a bug.

---

### Case 3 — Not triggering / not re-triggering

**3a. Price going back above the threshold**

```bash
curl -b cookies.txt -s -X PATCH "http://localhost:8080/api/v1/items/${ITEM_ID}" -d '{"price": 85}'
```
**Expected:** the 🔥 badge disappears from the item's row, no toast, no notification. The response does not contain `price_alert_triggered`.

**3b. Price stays under the threshold on a subsequent edit (no re-trigger)**

Start from a price already under the threshold, then change something without going back above it:
```bash
curl -b cookies.txt -s -X PATCH "http://localhost:8080/api/v1/items/${ITEM_ID}" -d '{"price": 75}'   # triggers once (Case 2)
curl -b cookies.txt -s -X PATCH "http://localhost:8080/api/v1/items/${ITEM_ID}" -d '{"price": 70}'   # still under 80 €
```
**Expected:** the 2nd request must **not** re-trigger the toast/push — this is a false→true transition, not a level check re-evaluated on every request (see `checkPriceDropAlert` in the code). The 2nd request's response does not contain `price_alert_triggered`, even though the 🔥 badge stays visible (the condition is still true, only the *trigger* doesn't repeat). This was confirmed directly: the 1st `PATCH` above returns `"price_alert_triggered":true`, the 2nd returns the item with no such field at all.

**3c. Re-arming: crossing back above then below re-triggers**

```bash
curl -b cookies.txt -s -X PATCH "http://localhost:8080/api/v1/items/${ITEM_ID}" -d '{"price": 85}'   # above → no badge
curl -b cookies.txt -s -X PATCH "http://localhost:8080/api/v1/items/${ITEM_ID}" -d '{"price": 79}'   # back under → should re-trigger
```
**Expected:** the last request contains `"price_alert_triggered": true` — the alert correctly "re-armed" in between.

---

### Case 4 — Automatic update via a merchant URL (scraping)

⚠️ **Important heads-up before starting**: Trakka's scraper is designed to refuse contacting a local/private address (`127.0.0.1`, `localhost`, a `192.168.x.x`/`10.x.x.x` network, etc. — this is the anti-SSRF protection documented in [CLAUDE.md](../CLAUDE.md)). **A small local HTML server spun up on your own machine to fake a product page will therefore not work** — the URL needs to be genuinely public (a real storefront, a GitHub Pages page, a public tunnel such as `ngrok`/`cloudflared`, etc.).

**Case 4a — An item created with just a URL (the scraper finds the price on its own)**

1. Create an item with a **real, public** product-page URL whose displayed price you already know, a `target_price` **above** that known price, and `alert_on_price_drop: true`, but **without** giving `price`:
   ```bash
   curl -b cookies.txt -s -X POST http://localhost:8080/api/v1/items \
     -d "{\"list_id\": ${LIST_ID}, \"title\": \"Article scrapé\", \"url\": \"https://exemple-marchand.tld/produit\", \"target_price\": 999, \"alert_on_price_drop\": true}"
   ```
2. Check `price_status` in the response:
   - `"found"`: the price was found within the bounded wait (~2.5s) — check the same response directly for `price_alert_triggered`.
   - `"pending"`: the site took longer to respond — wait a few seconds, then re-fetch `GET /api/v1/items/<item_id>` (or reopen the list in the UI) to see the price, the badge, and possibly the toast appear once the background lookup finishes.
   - `"none"`: nothing was found on the page — try a different URL (see the known Amazon/anti-bot limitation documented in [CLAUDE.md](../CLAUDE.md)).

**Case 4b — Deferred detection + manual acceptance (the other mechanism, `price_alerts`)**

This sub-case exercises the full chain: "the scraper finds a lower price on an already-tracked page → the user accepts it → the personal target-price alert also fires if the new price satisfies it." Since it involves the periodic/on-demand scan of the *other* feature (see section 2), the most reliable way to test it without depending on a real price drop happening online is to **seed a pending `price_alerts` row directly** in the database (see the SQL tip in section 6), then accept it — this exact sequence was run end to end while writing this guide:

```bash
# 1. The existing item from Case 1-3, with the threshold changed to 40 and a current price of 50 €
curl -b cookies.txt -s -X PATCH "http://localhost:8080/api/v1/items/${ITEM_ID}" -d '{"price": 50, "target_price": 40, "alert_on_price_drop": true}'

# 2. Seed a pending "better price found" alert (see section 6 for the exact SQL command)
#    → simulates "the scraper found 35 € on the product page"

# 3. List pending alerts for the house, and capture the alert id
ALERT_ID=$(curl -b cookies.txt -s "http://localhost:8080/api/v1/price-alerts?house_id=${HOUSE_ID}&status=pending" \
  | grep -o '"id":[0-9]*' | head -1 | grep -o '[0-9]*')
echo "ALERT_ID=${ALERT_ID}"

# 4. Accept it
curl -b cookies.txt -s -X PATCH "http://localhost:8080/api/v1/price-alerts/${ALERT_ID}" -d '{"status": "accepted"}'
```

**Expected after step 4** (confirmed live):
- `GET /api/v1/items/${ITEM_ID}` shows `"price": 35, "price_auto": true`.
- The 🔥 badge now appears on the item in the UI (35 € ≤ 40 €).
- A push notification is sent to every user with access to the list (note from the code: this endpoint doesn't return the item in its own response, so there's **no in-app toast** for this specific action — only the push fires; that's documented behavior, not an oversight).

---

### Edge cases (bonus, don't skip these)

| # | Step | Expected result |
|---|---|---|
| L1 | Set the price **exactly equal** to the threshold (`price = target_price`) | Triggers (`<=` comparison, not `<`) |
| L2 | Turn the alert off (`alert_on_price_drop: false`) then lower the price further | No badge, no toast, no push |
| L3 | Clear the threshold (`target_price: null`) on an item whose badge was showing | The 🔥 badge disappears immediately |
| L4 | Send a negative `target_price`, e.g. `-5` | `400 {"error": "target_price cannot be negative"}` (UI message: *"Le prix cible doit être un nombre positif."*) — confirmed live |
| L5 | Try to change the price from an account with **read-only** access to a shared list | `403` — the alert logically can't fire since the edit itself is rejected |
| L6 | `PUT` (full replace) omitting `target_price`/`alert_on_price_drop` | Both are reset (threshold cleared, alert turned off) — different from `PATCH`, which leaves omitted fields untouched |

---

## 6. Tips to speed up testing (Dev / QA)

### Changing the price without waiting for real scraping

The fastest and most **faithful** way to simulate "the price just changed" is a plain `curl PATCH` (see every example above) — the trigger logic (`checkPriceDropAlert`) is exactly the same whether the price arrives from a manual edit or from the scraper: both code paths call the same function. There's no need to wait on a real storefront to validate the threshold behavior itself.

### ⚠️ Trap: never edit `price` directly in SQL if you want the alert to fire

```sql
-- TRIGGERS NO ALERT — no badge will update until the UI does a fresh GET:
UPDATE items SET price = 60 WHERE id = 1;
```

All of the detection logic (`priceAlertCondition`/`checkPriceDropAlert`) lives in the Go HTTP handler code, **not** in a SQLite trigger. Editing `price` directly in the database bypasses that logic entirely: no toast, no push, and `price_alert_triggered` will never be reported for that change. Only use a direct SQL command to:
- **set up a starting state** without wanting to fire a notification (e.g. resetting a price between two runs);
- **seed a pending `price_alerts` row** (see just below) — that table is only ever read by the alert logic at **acceptance** time (`PATCH .../price-alerts/{id}`), which does go through the real Go code.

First locate the database file your local instance is using (`DB_PATH`, `./trakka.db` by default locally):
```bash
sqlite3 ./trakka.db
```
> The server runs in WAL mode with a single connection; a one-off SQL query while the server is running is safe, but avoid holding a long transaction or running a `VACUUM` while the app is in active use.
>
> If you don't have the `sqlite3` CLI installed, the same insert can be done with a throwaway Go program calling `db.Open()` + `(*db.DB).CreatePriceAlertIfNonePending(ctx, itemID, originalPrice, foundPrice, sourceURL)` from `internal/db/price_alerts.go` — that's exactly how this guide's Case 4b was verified in an environment with no `sqlite3` binary available.

### Seeding a "better price found" alert (`price_alerts`) without real scraping — for Case 4b

```sql
INSERT INTO price_alerts (item_id, original_price, found_price, source_url)
VALUES (<item_id>, 50, 35, 'https://exemple-marchand.tld/produit');
-- id, status ('pending') and created_at all have defaults — no need to set them.
```
Unlike an `UPDATE items SET price = ...`, this is a **safe** shortcut for testing: the row stays "pending" until you accept or reject it via the API/UI (the 🔔 bell), at which point the real Go logic (price update + target-threshold check) runs normally.

### Resetting to replay a scenario multiple times

The personal target-price alert has **no** persistent "already triggered" flag in the database (unlike `price_alerts.status`) — `price_alert_triggered` is a transient field, never stored. The "active" state is purely derived from `price <= target_price` at the current moment. To replay the trigger several times in a row on the same item, just cycle through a false→true transition each time:

```bash
# Raise the price back above the threshold ("disarm")...
curl -b cookies.txt -s -X PATCH "http://localhost:8080/api/v1/items/${ITEM_ID}" -d '{"price": 999}'
# ...then bring it back under it ("re-arm" → triggers again)
curl -b cookies.txt -s -X PATCH "http://localhost:8080/api/v1/items/${ITEM_ID}" -d '{"price": 75}'
```

To start from a fully clean state between two test passes, it's simplest to delete the test item and recreate a fresh one (Case 1) rather than hand-editing state via SQL.

### Automated complement: the existing Go tests

Before even running through this guide manually, run the automated test suite that already covers the pure trigger logic (useful as a regression check after any code change):

```bash
go test ./internal/handlers/... -run 'PriceAlert|TargetPrice' -v
```

This runs `TestPriceAlertConditionPure` (all 4 conditions combined) and the end-to-end `TestHandleItems{Create,Update,Patch}PriceAlertTrigger` tests (`internal/handlers/price_target_alerts_test.go`), which cover exactly Cases 1–3 above at the HTTP handler level (no browser needed). This manual guide is still necessary for everything those tests can't see: badge rendering, the toast, and an actual push notification.

### Verifying the push notification without a browser

If you don't have a browser handy to confirm a push is actually received, you can at least confirm the server *attempted* to send one:
- Check the server logs (`log/slog`, JSON on stdout) right after the trigger — a delivery failure is logged there (`"error"`); a successful send deliberately leaves no positive trace by design (best-effort, silent on success).
- Confirm an account actually has a registered subscription:
  ```bash
  sqlite3 ./trakka.db "SELECT id, user_id, endpoint FROM push_subscriptions;"
  ```
  An empty table means no push can be delivered at all — there's no point digging further on the notification side until at least one row exists here.

---

## 7. Sign-off checklist

Check these off during the test session — see also the [PR template](../.github/PULL_REQUEST_TEMPLATE.md)'s own checklist for the rest of the project's validation procedure.

- [ ] Case 1 — threshold set, no badge while the price stays above it
- [ ] Case 2 — correct 🔥 badge, correct toast, `price_alert_triggered: true` in the response
- [ ] Case 2 — push notification actually received on a real device/browser
- [ ] Case 3a — badge disappears when the price goes back above the threshold
- [ ] Case 3b — no re-trigger while staying under the same threshold
- [ ] Case 3c — correct re-trigger after re-arming
- [ ] Case 4a — an item created with just a URL triggers the alert if the scraped price already satisfies it
- [ ] Case 4b — accepting a `price_alerts` row applies the price **and** triggers the personal target-price alert when applicable
- [ ] L1–L6 — edge cases
- [ ] `go test ./internal/handlers/... -run 'PriceAlert|TargetPrice'` passes

## 8. References

- [docs/API.md#price-drop-alerts](API.md#price-drop-alerts) — endpoint reference
- [docs/API.md#price-alerts](API.md#price-alerts) — the other mechanism (better-deal detection)
- [docs/API.md#authentication](API.md#authentication) — the `csrf_token` two-step login/register recipe used in section 4
- [CLAUDE.md](../CLAUDE.md) — "Price drop alerts (per-item target price)" section for the full design, and the "What's left" list for this guide's context
- `internal/handlers/price_target_alerts.go` / `price_target_alerts_test.go` — the implementation and its matching automated tests
