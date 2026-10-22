# Maintainer scripts

Tooling for maintaining the repository. Nothing here is part of the shipped app, and the Docker build never copies this directory.

## `generate-showcase-images.js`

Regenerates the screenshots used in the root [README.md](../README.md), writing them to [`docs/assets/`](../docs/assets/).

Requirements: Go 1.27.3+ and Node.js 20+.

```bash
cd scripts
npm install
npx playwright install chromium   # first run only
npm run showcase
```

What it does:

1. Builds `./cmd/server` and starts it on a free local port with a throwaway SQLite database in a temporary directory. Your own `trakka.db` is never read or modified, and the temporary directory is deleted at the end.
2. Registers a demo account and creates English demo data (lists, prices, labels, recurring chores, reminders) through the REST API. Due dates are relative to today, so the "today/tomorrow" badges always look current.
3. Captures desktop (1440×900) and mobile (390×844) screens in both light and dark themes, using headless Chromium.
4. Composes the final images in Chromium: each desktop screen inside a browser-window frame (one full image per theme, plus a diagonal light/dark split of the dashboard), and each mobile screen inside a phone frame.

| File | Content |
|---|---|
| `showcase-desktop-split.png` | Dashboard, light and dark split (README hero) |
| `desktop-{light,dark}.png` | Dashboard, full window, one per theme |
| `desktop-list-{light,dark}.png` | "Baby Essentials" list with its budget summary, full window, one per theme |
| `showcase-mobile-home-{light,dark}.png` | Mobile dashboard |
| `showcase-mobile-selection-{light,dark}.png` | Selection mode with the bulk actions bar |
| `showcase-mobile-recurrence-{light,dark}.png` | Edit sheet of a weekly chore: repeat rule and reminder |

To capture from an instance that is already running, set `TRAKKA_URL` (for example `TRAKKA_URL=http://localhost:8080 npm run showcase`). That instance must be empty and have registration open, because the script creates the demo account and its data from scratch.

The demo content lives in `demoLists()` in the script. When the UI changes (renamed buttons, new element IDs), update the selectors in `captureMobile()`.

## `test-qr-encoder.js`

Headless check of `static/js/qrcode.js`, the QR code encoder of Paramètres → Intégrations → "Connecter l'application mobile via QR Code": it encodes the app's sign-in URL, UTF-8 text and texts of every length from version 1 to 40, and reads each symbol back with [jsQR](https://github.com/cozmo/jsQR), an independent decoder (a devDependency of this directory only, never shipped). No browser needed.

```bash
cd scripts
npm install
npm run test:qr-encoder   # prints ok/FAIL per check, exits 1 on any failure
```

## `test-quick-shopping.js`

End-to-end check of quick shopping lists ("Courses") in headless Chromium, against a freshly built server on a throwaway database (`PRODUCT_LOOKUP_ENABLED=false`, so it never reaches Open Food Facts):

```bash
cd scripts
npm run test:quick-shopping                       # prints ok/FAIL per check, exits 1 on any failure
SHOTS_DIR=/tmp/shots npm run test:quick-shopping  # also saves a screenshot at each step
```

It checks that adding a product already on the list tops it up or brings it back instead of duplicating it (in the browser, online and offline with the service worker, then after the offline queue syncs), autocomplete from the House catalog, the "Par rayon" grouping, favorites and the ⭐ panel, the aisle in the edit modal, and the barcode scanner: Chromium's fake camera plays a generated video of an EAN-13 symbol (`--use-file-for-fake-video-capture`), so the camera, `static/js/barcode.js`'s decoder, the House catalog lookup and the addition all run for real. It also checks that nothing is wider than a 320 px phone. Requirements as above.

## `test-budget-templates.js`

End-to-end check of the purchase cycle with budget tracking and list templates in headless Chromium, against a freshly built server on a throwaway database:

```bash
cd scripts
npm run test:budget-templates                       # prints ok/FAIL per check, exits 1 on any failure
SHOTS_DIR=/tmp/shots npm run test:budget-templates  # also saves a screenshot at each step
```

It creates a list from the "Rentrée scolaire" built-in template through the "Nouvelle liste" modal's picker (name, icon and type filled in, a typed name kept), checks its items arrive planned with their prices and aisles and its budget, confirms planned items (one tag tap, then "Valider les articles prévus"), checks an item off and enters the price paid from the toast's "Prix payé" button, checks the variance in the summary (red overrun, then a green saving, in step with the server's `budget`), moves an item back to "Prévu" from its actions sheet, edits a price paid in the edit modal, saves the list as a House template (the price paid becoming the estimate), creates a list from it and deletes it from the picker, switches the type to "Tâches" and checks the picker then offers the task templates only (no price, the type picked automatically, a task template dropped again on a switch back to a purchase type, no picker for "Liste libre"), creates the chores list (recurring tasks, no budget, no "Prévu", 🔁 cadence badges) and saves it as a House template offered under "Tâches" only, measures the template carousel at 390, 360 and 320 px (inside the dialog's padding, aligned with the fields, a 16 px margin after the last card at full scroll) and swipes it with raw touch events to check it snaps to a card, checks the picker's offline message and that a Courses list without prices shows no budget, and that nothing is wider than a 320 px phone — the collapsed summary included. Requirements as above.

## `test-assignees.js`

End-to-end check of task assignments in headless Chromium, against a freshly built server on a throwaway database:

```bash
cd scripts
npm run test:assignees                       # prints ok/FAIL per check, exits 1 on any failure
SHOTS_DIR=/tmp/shots npm run test:assignees  # also saves a screenshot at each step
```

It builds a House of three (Camille, Thomas and Sarah, invited by email) plus a list-share recipient, and checks the avatars on task rows (with their contrast in light and dark), the "Par membre" grouping (a task shared by two people under both headings), the "Membres" filter (a task added while "Moi" is on is assigned to me and stays in view), the picker sheet from the actions sheet and from the avatars, the edit modal's field, the multi-select "Assigner" (tri-state), a recurring assigned task checked off (kept assigned once the undo window commits it), the share recipient joining the assignable people once the list is shared with them — and assigning a task to themselves — (self-hosted, the default), an assignment made offline through the service worker (named from the roster mirror, still there after a reload, synced once back online), and at 320 px that nothing overflows and an assigned task's title is as wide as an unassigned one's. It then starts a second server with `SAAS_MODE=true` and checks that a share recipient there can neither be assigned nor assign, and only gets a read-only view. Requirements as above.

## `test-price-alert-links.js`

End-to-end check, in headless Chromium, that the cards of the 🔔 drawer ("Alertes de prix") lead somewhere: `npm run test:price-alert-links` (`SHOTS_DIR=…` for screenshots). The list and items are real (API of a freshly built server on a throwaway database); the drawer's two sources are served by `page.route`, since only a background scan of a real shop page creates them, and the outside pages are stubbed. It checks the labels and links of each card, that a tap on a card with a link opens it in a new tab without `window.opener`, that ✕ and "Ignorer" don't navigate, that a card without a link opens the list scrolled to the item and highlights it for a moment (also a done item, and the `?list=&item=` deep link), the toast for an item a filter hides, that a `javascript:` url is never linked, the hover feedback, and that nothing overflows the drawer at 320 px in light and dark. Requirements as above.

## `test-nutrition-scores.js`

End-to-end check of the nutrition scores (Nutri-Score / Yuka) on a "Courses" list, in headless Chromium against a freshly built server on a throwaway database: `npm run test:nutrition-scores` (`SHOTS_DIR=…` for screenshots). It checks that nothing is shown by default, that "Afficher sur mobile" and "Afficher sur le web" (Paramètres → Général) each apply only to their kind of screen (390 px phone vs 1280 px window, switched by resizing), that the badge sits between the checkbox and the title, centred on the same line as the checkbox and the kebab, without changing the row's height, the accessible labels, that nothing overflows at 320 px, where a title beside the badge may take two lines (shown whole, or clamped at two) without making the row taller, and that the edit modal sets, refuses (Yuka > 100) and clears the scores. Requirements as above.

## `test-barcode-decoder.js`

Unit test of `static/js/barcode.js`, the scanner's own EAN-13/EAN-8 decoder, with no browser: synthetic symbols with blur, noise, a lighting gradient, upside down and vertical must be read; random stripes and a wrong check digit must not. `npm run test:barcode`.
