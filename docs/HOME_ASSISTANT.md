# Home Assistant integration

Trakka ships a [Home Assistant](https://www.home-assistant.io/) integration, installable with [HACS](https://hacs.xyz/), that turns every Trakka list you can open into a native Home Assistant **to-do list**: shown on the dashboard's To-do cards, editable from Home Assistant, usable from automations, scripts and voice assistants.

- [What you get](#what-you-get)
- [Requirements](#requirements)
- [1. Create an API token in Trakka](#1-create-an-api-token-in-trakka)
- [2. Install the integration](#2-install-the-integration)
- [3. Add it in Home Assistant](#3-add-it-in-home-assistant)
- [Entities](#entities)
- [Actions](#actions)
- [Automations](#automations)
- [Polling and options](#polling-and-options)
- [Security](#security)
- [Troubleshooting](#troubleshooting)
- [Using the API without the integration](#using-the-api-without-the-integration)
- [Development](#development)

## What you get

- **One entity per list**, `todo.trakka_<list name>` (`todo.trakka_courses`, `todo.trakka_taches`…): your own Houses' lists and the lists shared with you. Its state is the number of items still to do.
- **Read and sync**: active and completed items, with their due date (and time) on task lists, and a summary of the quantity, aisle, price, urgency, labels and assignees.
- **Actions**: Home Assistant's own `todo.add_item`, `todo.update_item` (rename, check off, uncheck, reschedule), `todo.remove_item`, `todo.remove_completed_items` and drag-and-drop reordering, plus `trakka.add_item` to add an item with a quantity, an aisle, a price or the urgent flag.
- **Automations**: events fired when an item is added, completed, reopened or removed, carrying the item's details, and Home Assistant's own to-do item triggers.

Changes made in Home Assistant reach Trakka at once; changes made in Trakka reach Home Assistant at the next poll (every 30 seconds by default).

## Requirements

- A Trakka server with API tokens (this release or later), reachable from Home Assistant over HTTP(S).
- Home Assistant **2025.8** or later. HACS is optional but recommended. The Trakka icon and logo show from Home Assistant **2026.3**; older versions show "icon not available" instead, and everything else works.

## 1. Create an API token in Trakka

Home Assistant talks to Trakka with a personal API token, not your password.

1. In Trakka, open **Paramètres** (⚙️) → **Intégrations** → **Jetons d'API (Home Assistant)**.
2. Name the token (e.g. "Home Assistant") and press **Créer un jeton**.
3. Copy the token (it starts with `trk_`) and the server address shown under it. **The token is shown only once**: Trakka keeps only a hash of it. If you lose it, revoke it and create another.

The token acts as your account, on your lists only — see [Security](#security). The same section lists your tokens with when each was last used, and revokes one in two clicks.

Without the PWA, a signed-in session can create one through the API ([docs/API.md](API.md#api-tokens)):

```bash
curl -b cookies.txt -X POST http://localhost:8080/api/v1/api-tokens \
  -H 'Content-Type: application/json' -d '{"name":"Home Assistant"}'
```

## 2. Install the integration

**With HACS** (recommended):

1. HACS → ⋮ → **Custom repositories** → repository `https://github.com/Trakka-project/Trakka`, type **Integration** → **Add**.
2. Search for **Trakka** in HACS, **Download**, then restart Home Assistant.

**By hand**: copy the repository's `custom_components/trakka/` folder into your Home Assistant configuration directory (next to `configuration.yaml`, so it ends up as `config/custom_components/trakka/`), then restart Home Assistant.

## 3. Add it in Home Assistant

**Settings** → **Devices & services** → **Add integration** → **Trakka**, then fill in:

| Field | Value |
|---|---|
| Server URL | The address you open Trakka at, e.g. `https://trakka.example.com` or `http://192.168.1.10:8080`. A trailing `/` or `/api/v1` is fine. |
| API token | The `trk_…` token from step 1. |
| Verify the SSL certificate | Leave on, unless your server uses a self-signed certificate. |

The integration checks the token against the server before saving. Each Trakka account on each server can be added once; add the integration again for another account (each needs its own token).

To change the address later (the server moved, a new domain), use the entry's ⋮ → **Reconfigure**: your entities keep their ids. If the token is revoked, Home Assistant shows a **Re-authenticate** notice asking for a new one.

## Entities

Each list is a to-do entity on a "Trakka" service device. Its entity id comes from the list's name when it first appears (`Tâches` → `todo.trakka_taches`); renaming the list in Trakka renames the entity but keeps its id (rename the id itself in Home Assistant if you like). A new list, or a list newly shared with you, gets its entity at the next poll; a deleted list, or a share that was revoked, loses it.

State: the number of items still to do. Attributes: `list_id`, `list_type` (`todo`, `shopping`, `groceries`, `recurring_shopping`, `custom`) and `read_only`.

How a Trakka item appears:

| Trakka | Home Assistant |
|---|---|
| Title | Item name |
| Checked off (on a purchase list: bought) | Completed |
| Due date, and due time | Due date, or due date and time. Trakka stores due times in its own zone (`APP_TIMEZONE`), converted both ways |
| Quantity, aisle, price, urgent, labels, assignees | The item's description, e.g. `🚨 · × 2 · Dairy & chilled · 1.15 €` (read-only) |
| Position | Order in the list |

What you can do on each list follows Trakka:

- **Due dates** are offered on task lists (`todo`) and recurring purchases (`recurring_shopping`), the list types that have them in Trakka.
- **A list shared with you read-only** is read-only in Home Assistant too: no adding, editing or reordering.
- **On a Courses list** (`groceries`), adding a product that's already there updates that item (and unchecks it if it was bought) instead of adding it twice, exactly as in the app.
- **A recurring task** checked off stays completed until its next occurrence comes due; Trakka then reopens it with the new date.

## Actions

Home Assistant's own to-do actions work on every writable list:

```yaml
action: todo.add_item
target:
  entity_id: todo.trakka_courses
data:
  item: Lait
```

```yaml
action: todo.add_item
target:
  entity_id: todo.trakka_taches
data:
  item: Sortir les poubelles
  due_datetime: "2026-10-20 19:30:00"
```

```yaml
action: todo.update_item
target:
  entity_id: todo.trakka_courses
data:
  item: Lait          # the item's name, or its uid
  status: completed   # or needs_action; or rename: "Lait demi-écrémé"
```

`todo.remove_item`, `todo.remove_completed_items` and `todo.get_items` work as usual; reordering is drag and drop on the To-do card. Assist's "add … to my … list" uses the same actions; it knows a list by its Home Assistant name ("Trakka Courses"), so give the entity an alias if you'd rather say "Courses".

`trakka.add_item` adds an item with Trakka's own details, which `todo.add_item` can't carry:

```yaml
action: trakka.add_item
target:
  entity_id: todo.trakka_courses
data:
  item: Yaourts
  quantity: 4        # optional, 1 to 100000
  aisle: dairy       # optional, one of the aisles below
  price: 2.50        # optional, unit price in euros
  urgent: true       # optional
```

Aisles: `produce`, `bakery`, `meat_fish`, `dairy`, `pantry`, `breakfast`, `drinks`, `baby`, `pets`, `household`, `hygiene`, `stationery`, `clothing`, `electronics`, `diy`, `frozen`, `other` (the action's form lists them by name). On a Courses list an item added without an aisle gets the household's usual one, as in the app.

## Automations

At each poll the integration fires an event per item that changed since the previous one:

| Event | When |
|---|---|
| `trakka_item_added` | an item appeared on a list |
| `trakka_item_completed` | an item was checked off (bought) |
| `trakka_item_reopened` | an item was unchecked — including a recurring task's next occurrence, and a product added again to a Courses list |
| `trakka_item_removed` | an item was deleted |

Their data:

| Key | Example |
|---|---|
| `entity_id` | `todo.trakka_courses` |
| `list_id`, `list_name`, `list_type` | `12`, `Courses`, `groceries` |
| `item_id`, `item` | `342`, `Lait` |
| `quantity`, `aisle`, `price`, `due_date`, `urgent` | `2`, `dairy`, `1.15`, `null`, `false` |

Nothing fires at startup, nor for the items of a list that just appeared. Changes made from Home Assistant fire too, after the refresh that follows them.

```yaml
# Tell the phone when something is added to the shopping list.
triggers:
  - trigger: event
    event_type: trakka_item_added
    event_data:
      list_name: Courses
actions:
  - action: notify.mobile_app_pixel
    data:
      message: "{{ trigger.event.data.item }} ajouté aux courses"
```

```yaml
# Flash the hall light when the bins are taken out.
triggers:
  - trigger: event
    event_type: trakka_item_completed
    event_data:
      entity_id: todo.trakka_taches
      item: Sortir les poubelles
actions:
  - action: light.turn_on
    target:
      entity_id: light.entree
    data:
      flash: short
```

Recent Home Assistant versions also have native to-do triggers ("Item added", "Item completed", "Item removed" on a to-do entity), which work on Trakka lists out of the box; their data only carries the entity and the item ids, so use the events above when you need the item's name or details. And the entity's state (the number of items left) works with ordinary state triggers and conditions: "when `todo.trakka_courses` goes above 10".

## Polling and options

The integration fetches every list with its items in a single request, every 30 seconds by default. Change it in the entry's **Configure** (10 to 3600 seconds); the entry reloads with the new interval. Your own changes from Home Assistant don't wait: each one is followed by an immediate refresh.

While the server can't be reached, the entities are unavailable; they come back by themselves.

## Security

- **What a token can do**: read your lists and their items, and add, change, check off, reorder and delete items — with your account's access rules (a list shared with you read-only stays read-only). Nothing else: not your account, houses, sharing, notifications, the calendar feed, the admin console, nor creating or listing tokens. See [docs/API.md](API.md#api-tokens) for the exact routes.
- **Revoking** a token in Paramètres → Intégrations stops it at once. Use one token per integration, so you can revoke one without touching the others.
- Trakka stores only a SHA-256 hash of each token. Home Assistant, however, keeps the token in its configuration storage (`.storage/core.config_entries`), so treat Home Assistant's backups as sensitive.
- Use `https://` whenever the connection leaves your home network: the token travels in every request's `Authorization` header.
- The integration never follows redirects (so the token never goes to another address), and reports a redirect as a connection error.

## Troubleshooting

| Problem | What to check |
|---|---|
| "Can't reach the Trakka API" when adding it | The URL opens Trakka in a browser from the Home Assistant machine's network. If a reverse proxy puts a login page (Authelia, Authentik, oauth2-proxy…) in front of Trakka, let `/api/v1/` through without it: Trakka authenticates those requests itself. With a self-signed certificate, untick **Verify the SSL certificate**. |
| "This API token is unknown or was revoked" | Create a new token; Home Assistant's **Re-authenticate** notice takes it without removing the entry. |
| "A Trakka API token starts with trk_" | You pasted something else, such as the calendar feed link. |
| Times are off by some hours | Trakka interprets due times in `APP_TIMEZONE` (see [DEPLOYMENT.md](DEPLOYMENT.md)); check it's your zone. |
| A list is missing | Its share was revoked, or it was deleted. A new list appears within one polling interval. |
| "shared with you read-only" | Ask the list's owner for write access in Trakka. |
| "icon not available" instead of the Trakka logo | Home Assistant reads the integration's own images (`custom_components/trakka/brand/`) from 2026.3 on; update Home Assistant, restart it after updating the integration, then reload the page with the cache bypassed (Ctrl+Shift+R), since the browser caches brand images. |

For details, turn on the integration's debug log in `configuration.yaml`:

```yaml
logger:
  logs:
    custom_components.trakka: debug
```

## Using the API without the integration

The same token works from anything that can send an HTTP request, such as Home Assistant's `rest_command` or a script. A list or an item can be found by name, then changed by id:

```bash
TOKEN=trk_…
# The "Courses" list, with its items
curl -H "Authorization: Bearer $TOKEN" "https://trakka.example.com/api/v1/lists?scope=all&include=items&name=Courses"
# The item "Lait" on list 12
curl -H "Authorization: Bearer $TOKEN" "https://trakka.example.com/api/v1/items?list_id=12&title=Lait"
# Add one, with an aisle and a price
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X POST https://trakka.example.com/api/v1/items \
  -d '{"list_id":12,"title":"Lait","quantity":2,"aisle":"dairy","price":1.15}'
# Check it off
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -X PATCH https://trakka.example.com/api/v1/items/342 -d '{"done":true}'
```

Reference: [docs/API.md](API.md#api-tokens).

## Development

The integration lives in `custom_components/trakka/`: `api.py` (the HTTP client), `coordinator.py` (polling and events), `todo.py` (the entities and `trakka.add_item`), `config_flow.py` (setup, re-authentication, reconfiguration, options), plus `strings.json`/`translations/` (English, French), `services.yaml`, `icons.json` and `brand/` (the icon and logo Home Assistant 2026.3+ shows for the integration: `icon.png` 256×256 and `icon@2x.png` 512×512 from `trakka-brand/svg/trakka-symbol.svg`, `logo.png`/`logo@2x.png` 256/512 px high from `trakka-lockup-light-bg.svg`, and `dark_logo.png`/`dark_logo@2x.png` from `trakka-lockup-dark-bg.svg`, all transparent and trimmed; regenerate them from those SVGs if the brand changes). `hacs.json` at the repository root declares it to HACS. It has no Python dependency beyond Home Assistant itself.

Its tests, in `tests/home_assistant/`, run it inside a real Home Assistant core with [pytest-homeassistant-custom-component](https://github.com/MatthewFlamm/pytest-homeassistant-custom-component), against `fake_trakka.py`, an in-memory Trakka API served through Home Assistant's HTTP mocker. They need Python 3.14.2+ (Home Assistant 2026.10):

```bash
cd tests/home_assistant
python3 -m venv .venv && .venv/bin/pip install -r requirements_test.txt
.venv/bin/pytest
```

`test_live_server.py` runs every call against a **real** Trakka server instead, when `TRAKKA_E2E_URL` (on `127.0.0.1`: the harness blocks other addresses) and `TRAKKA_E2E_TOKEN` are set. The server must run with `APP_TIMEZONE=Europe/Paris`, and the token's user must have a `groceries` list named "Courses" and a `todo` list named "Tâches":

```bash
TRAKKA_E2E_URL=http://127.0.0.1:8080 TRAKKA_E2E_TOKEN=trk_… .venv/bin/pytest test_live_server.py
```

Lint and format with [ruff](https://docs.astral.sh/ruff/), with Home Assistant's import order:

```bash
ruff format custom_components/trakka tests/home_assistant
ruff check --select E,F,W,I,UP,B,SIM,RUF,ASYNC,PIE,PT,RET,C4 --ignore E501,RUF001,RUF002,RUF003 \
  --config "lint.isort.known-first-party=['homeassistant','custom_components','fake_trakka']" \
  --config lint.isort.force-sort-within-sections=true --config lint.isort.combine-as-imports=true \
  custom_components/trakka tests/home_assistant
```

To check the oldest supported Home Assistant (2025.8, Python 3.13), run the same tests in a container with the matching harness release and that version's dependency constraints (`homeassistant/package_constraints.txt` from its wheel, plus `pycares<5`, which it predates):

```bash
podman run --rm -v "$PWD:/src:ro,z" -v ./constraints-2025.8.txt:/constraints.txt:ro,z -w /src/tests/home_assistant \
  docker.io/library/python:3.13 sh -c 'pip install -q pytest-homeassistant-custom-component==0.13.269 -c /constraints.txt && pytest -p no:cacheprovider'
```

The native to-do trigger test is skipped there: those triggers are newer than 2025.8.

The server side is `internal/handlers/api_tokens.go` (the bearer-token gate in `RequireSession` and the token routes), `internal/db/api_tokens.go` and migration 35; its tests are `internal/handlers/api_tokens_test.go` and `internal/db/api_tokens_test.go`, run by `go test ./...`.
