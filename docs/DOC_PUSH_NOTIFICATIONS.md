# Setting up Web Push notifications

Trakka can send real browser/OS push notifications for two events: (1) another member of a shared list adding or checking off an item, and (2) a recurring item's due date entering its lead time. Delivery uses standard Web Push (VAPID, RFC 8292/8291/8030), implemented from scratch in `internal/webpush` — see [CLAUDE.md](../CLAUDE.md)'s "Web Push notifications" bullet for the full design, and [docs/API.md](API.md#push-notifications) for the endpoint reference. This page is the operational how-to: generate keys, configure the server, and verify the whole chain actually works.

Per the "Documentation" convention in [CLAUDE.md](../CLAUDE.md), this guide is written in English like the rest of the project's technical documentation — only exact UI strings are quoted verbatim in French, since Trakka's UI is French-only.

## 1. Generating and configuring VAPID keys

### Generate a key pair

Use Trakka's own binary — no third-party tool or Node/npm install required:

```bash
go build -o trakka ./cmd/server
./trakka -generate-vapid-keys
```

This prints:

```
VAPID_PUBLIC_KEY=BN....................................
VAPID_PRIVATE_KEY=................................
```

`internal/webpush.GenerateVAPIDKeys` builds these on `crypto/ecdh`/`crypto/ecdsa` (P-256), so the pair is cryptographically equivalent to whatever `npx web-push generate-vapid-keys` would produce — either tool works, but the Go one avoids pulling in Node just for this one-time step, consistent with Trakka's "standard library wherever possible" convention. Run it exactly once per deployment and keep the private key secret; regenerating it later invalidates every browser's existing subscription (see the "Rotating keys" gotcha below).

### Required environment variables

Set all three, or leave all three empty — this is an all-or-nothing switch, the same pattern OIDC already uses:

| Variable | Example | Notes |
|---|---|---|
| `VAPID_PUBLIC_KEY` | `BN....` | From the command above. Sent to the browser via `GET /api/v1/push/vapid-public-key` — not a secret, but there's no reason to hand-copy it into frontend code either. |
| `VAPID_PRIVATE_KEY` | `....` | From the command above. **Treat as sensitive** — same handling as `OIDC_CLIENT_SECRET`: a secret/secret-file in production, never a plain committed value. |
| `VAPID_SUBJECT` | `mailto:ops@example.com` | A contact URI (`mailto:` or `https:`) sent in every VAPID JWT's `sub` claim — some push services (FCM, Mozilla autopush) use this to reach an operator if a sender misbehaves. |

Setting only one or two of the three fails startup outright with a clear error (see section 3 below) rather than silently half-enabling push.

**Local development / `go run`:** export them into your shell, or copy the repo's [.env.example](../.env.example) to `.env` and source it (Trakka itself never reads a `.env` file — it only reads the process environment, see `.env.example`'s own header comment):

```bash
cp .env.example .env
# edit .env, paste the generated keys into VAPID_PUBLIC_KEY / VAPID_PRIVATE_KEY / VAPID_SUBJECT
set -a && source .env && set +a
PORT=8080 DB_PATH=./trakka.db STATIC_DIR=./static TEMPLATES_DIR=./templates go run ./cmd/server
```

**Docker / `compose.yml`:** the shipped `compose.yml` only hardcodes `PORT`/`DB_PATH`/`STATIC_DIR` in its `trakka` service's `environment:` block — every optional feature's variables (OIDC, VAPID, etc.) are left for the deployer to add. Either add the three keys directly to that block:

```yaml
services:
  trakka:
    environment:
      PORT: "8080"
      DB_PATH: /data/trakka.db
      STATIC_DIR: /app/static
      VAPID_PUBLIC_KEY: "BN...."
      VAPID_PRIVATE_KEY: "...."
      VAPID_SUBJECT: "mailto:ops@example.com"
```

or point the service at an env file instead (keeps secrets out of `compose.yml` itself, which is more likely to be committed):

```yaml
services:
  trakka:
    env_file: .env
```

Then `docker compose up -d --build` (or `podman-compose up -d --build`) as usual.

### Rotating keys

There is no rotation endpoint or automatic re-keying, by design (see the "Deliberately not done" note in CLAUDE.md's Web Push handoff log): a browser's `PushManager.subscribe()` ties a subscription to the specific public key it was created with, so replacing `VAPID_PUBLIC_KEY`/`VAPID_PRIVATE_KEY` invalidates every existing subscriber's registration — their next `subscribe()` attempt throws `InvalidStateError` until they unsubscribe and re-enable the toggle. Only rotate deliberately, and expect every user to need to re-opt-in afterward.

## 2. Server prerequisites

### HTTPS is mandatory

Both the browser's `PushManager.subscribe()` call and the underlying service worker require a [secure context](https://developer.mozilla.org/en-US/docs/Web/Security/Secure_Contexts) — `https://`, or exactly `http://localhost` for local testing. This is a browser/PWA-spec requirement, not something Trakka's server enforces or can work around: on plain HTTP served from anywhere other than `localhost`, the service worker simply never registers, so the "Activer les notifications push" toggle in "Paramètres" will report the feature as unsupported (see `static/js/push.js`'s `refreshPushToggleUI`) — there is no error to debug, because the browser never gets far enough to talk to Trakka at all.

`compose.yml` ships as plain HTTP by design (see [docs/PWA.md](PWA.md)); put a TLS-terminating reverse proxy (Caddy, Traefik, nginx) in front for anything beyond `localhost` testing, and keep `SESSION_COOKIE_SECURE=true` (the default) once you do — see [docs/DEPLOYMENT.md](DEPLOYMENT.md).

### How delivery is triggered — no separate worker process

Push delivery isn't a standalone worker/cron binary — it's goroutines started inline by the same `trakka` process, from `cmd/server/main.go`:

- **Shared-list changes** (an item added, checked off or unchecked) fire immediately, in a detached goroutine started right from the HTTP handler that made the change (`notifyListChange`, `internal/handlers/push.go`) — there is nothing to schedule for this one; it happens the moment the triggering request is handled.
- **Task due-date reminders** (any item with a `due_date`, not just a recurring one — see [docs/API.md](API.md#reminders)) run on a `time.Ticker` (`runDueReminderScanLoop`), started only when `NOTIF_DUE_SCAN_INTERVAL_MINUTES > 0` **and** `PushEnabled()` is true — i.e. this loop doesn't even start if VAPID isn't configured, since it would have nothing to send. It runs one scan immediately at startup, then again every `NOTIF_DUE_SCAN_INTERVAL_MINUTES` (default `1`, so an "at the exact due time" reminder isn't late by more than a minute). Each item carries its own already-resolved `reminder_offset_days`/`reminder_time` (how many days before `due_date`, and at what time of day, interpreted in `APP_TIMEZONE`) and `reminder_at_due_time` (fire at `due_date` + `due_time` instead, when the item has a due time) rather than an instance-wide lead time — set explicitly, or resolved from the account's own `reminder_default_offset_days`/`reminder_default_time`/`reminder_default_at_due_time` ("Paramètres" → "Notifications de tâches", `PATCH /api/v1/me`) at the moment the item was last created/edited. The notification reads e.g. "Sortir les poubelles — 🔔 Échéance aujourd'hui à 18:00 — Maison" and carries a per-item `tag`, so two tasks' reminders never replace each other on the device. Users who turned "Rappels des tâches à échéance" off (`users.reminders_enabled`) are left out of the recipients. Likewise, a shared-list change only reaches the users who kept "Ajout d'items" (`item_additions_enabled`) or "Actions des collaborateurs" (`collaborator_actions_enabled`, check-offs **and** unchecks) on.
- **The daily overdue-tasks summary** ("Rappel des tâches en retard / non effectuées", `users.overdue_tasks_summary_enabled`, off by default) runs on the same ticker, right after each reminder scan (`RunOverdueSummaryScan`, `internal/handlers/overdue_summary.go`): once a user's `overdue_tasks_summary_time` (default `08:00`, `APP_TIMEZONE`) has passed and today's hasn't been handled yet, it lists **every** task of theirs past its due date and still not done (however many days late) and sends one notification ("Tâches en retard — 3 tâches en retard ne sont pas cochées : …", tag `trakka-overdue-summary`) to that user only — or nothing on a day with nothing overdue — then records the day (`users.overdue_tasks_summary_sent_on`).
- **Invitations and shares** ("Partage de listes", `users.list_sharing_enabled`) are sent from a detached goroutine when a House invitation or a List/Space share is created (`notifyInvitation`), to the invited address's account if it exists — the HTTP reply is the same either way.
- **Recurring tasks coming back** is not a notification but shares the timing: a checked-off recurring task waits in "Terminés" with a `next_due_date` (see [docs/API.md](API.md#recurring-items)), and `runNextOccurrenceLoop` (every minute, started unconditionally — it works without VAPID too) un-checks it once that occurrence starts: the start of its day, or its reminder moment if earlier. Each reminder scan also runs this pass first, so a task whose reminder is due is back in the active list when the push for it goes out.

Both mechanisms share the same underlying fan-out (`sendToUsers`): every subscription belonging to every user with access to the affected list is sent to concurrently and best-effort — one recipient's unreachable device never blocks or fails delivery to another's, and a subscription the push service reports as permanently gone (HTTP 404/410) is deleted automatically so future scans stop retrying it.

## 3. End-to-end verification

### 3.1 Confirm VAPID keys actually loaded at startup

There's no single "VAPID keys valid" banner in the logs, but two things together tell you unambiguously:

**A misconfiguration refuses to start, loudly.** If only one or two of the three `VAPID_*` variables are set, the server exits immediately with:

```json
{"level":"ERROR","msg":"invalid configuration","error":"VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY and VAPID_SUBJECT must all be set together, or none of them"}
```

If the process is running at all past this point, your three variables are at least internally consistent (all set, or all empty).

**A correctly-loaded config starts the reminder scan.** Grep the startup logs for:

```json
{"level":"INFO","msg":"starting periodic due-date reminder scan","interval":1800000000000}
```

This line is only ever logged when `cfg.PushEnabled()` (all three VAPID vars non-empty) is true — its absence means push is currently disabled on this instance, even if the process started fine otherwise (an all-empty VAPID config is a valid, silent "disabled" state, not an error).

**Or just ask the API directly** — the most direct check, and what the frontend itself relies on:

```bash
curl -b cookies.txt http://localhost:8080/api/v1/push/vapid-public-key
# disabled: {"enabled":false}
# enabled:  {"enabled":true,"public_key":"BN...."}
```

### 3.2 Subscribe a real browser

1. Open Trakka in an actual browser over HTTPS (or `http://localhost`).
2. Log in, open "Paramètres", and enable "Activer les notifications push". This triggers the browser's own native permission prompt — nothing server-side can skip or pre-fill it.
3. Accepting calls `PushManager.subscribe()` with the key from `GET /api/v1/push/vapid-public-key`, then registers the result via `POST /api/v1/push/subscribe`.

Confirm the subscription actually landed server-side:

```bash
curl -X POST -b cookies.txt http://localhost:8080/api/v1/push/test
```

### 3.3 Send yourself a test notification

`POST /api/v1/push/test` (added alongside this guide) sends one push to every subscription on the calling account — the fastest way to validate the whole chain (VAPID signing → encryption → the push service → the browser → `sw.js`'s `push` listener → the OS notification) without waiting for a real list change or a recurring item's due date:

```bash
curl -X POST -b cookies.txt http://localhost:8080/api/v1/push/test
```

Responses:

| Status | Meaning |
|---|---|
| `200 {"sent_to_subscriptions": N}` | Delivery was attempted to `N` subscription(s) on your account. A `200` confirms the attempt was made — not that a device necessarily displayed it (delivery to the push service itself is still best-effort, same as every other notification this app sends); check the device. |
| `503` | Push isn't configured on this instance (see section 1). |
| `404` | No push subscription is registered for your account yet — repeat step 3.2 first. |

A real device with the toggle enabled should show a system notification titled "Trakka" within a few seconds. If nothing arrives:

- Re-check `GET /api/v1/push/vapid-public-key` reports `enabled: true` — a `404`/`503` from the test endpoint above already told you this, but it's worth confirming the *browser* fetched the same key it's currently subscribed with (re-toggling push off and on again re-subscribes with whatever key the server currently reports).
- Check the server logs at `Debug` level around the test call — a failed individual delivery is logged as `"sending push notification failed"` with the underlying error (e.g. an expired/invalid subscription, a push-service-side rejection). A permanently gone subscription (404/410 from the push service) is deleted automatically rather than logged as an ongoing failure — if that happens, `POST /api/v1/push/test` will start returning `404` again until you re-subscribe.
- Confirm the browser tab (or the installed PWA) isn't in a state where notifications are blocked at the OS level — this is outside Trakka's control entirely; the in-app toggle only reflects what the browser itself reports via `Notification.permission`.

### 3.4 Exercise the two real triggers

Once the test endpoint confirms basic delivery works, validate the actual features it's standing in for:

- **Shared-list change**: from a *second* account with access to a shared list (see [docs/API.md#sharing](API.md#sharing)), add an item or check one off. The first account should receive a push within moments — no scan or delay involved, since this path fires synchronously off the triggering request.
- **Task due-date reminder**: on any item (recurring or not), set `due_date` to today and `reminder_enabled`/`reminder_offset_days`/`reminder_time` (e.g. `{"reminder_enabled": true, "reminder_offset_days": 0, "reminder_time": "00:00"}`, via `PATCH /api/v1/items/{id}`) so its reminder moment has already passed, then either wait for the next scan tick or temporarily lower `NOTIF_DUE_SCAN_INTERVAL_MINUTES` for a faster test loop. The `"running due-date reminder scan"` line (carries `item_count`) is logged at debug level now that the scan runs every minute; confirm the push arrives, or read the item back — `notification_sent_at` is set once the reminder has been sent.
- **Recurring task + reminder**: create a `DAILY` task due today with a "la veille" reminder whose time has already passed today (e.g. `{"recurrence_rule": "DAILY", "reminder_enabled": true, "reminder_offset_days": 1, "reminder_time": "00:01"}`), then check it off. Within a minute the log shows `"recurring task back for its next occurrence"`, the task is active again due tomorrow, and its reminder is sent in the same pass.

## See also

- [docs/API.md#push-notifications](API.md#push-notifications) — full endpoint reference.
- [docs/DEPLOYMENT.md](DEPLOYMENT.md) — the complete environment-variable table, including every other `NOTIF_DUE_SCAN_INTERVAL_MINUTES`/`APP_TIMEZONE`/`SCRAPE_INTERVAL` knob.
- [docs/PWA.md](PWA.md) — why HTTPS (or `localhost`) is required for service workers in general, not just push.
- [docs/DOC_TEST_PRICE_ALERTS.md](DOC_TEST_PRICE_ALERTS.md) — a similar manual QA walkthrough for the *other* notification-adjacent feature (per-item target-price alerts), useful as a template if you need to script a fuller push-notification QA pass later.
