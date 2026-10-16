# Maintainer scripts

Tooling for maintaining the repository. Nothing here is part of the shipped app, and the Docker build never copies this directory.

## `generate-showcase-images.js`

Regenerates the screenshots used in the root [README.md](../README.md), writing them to [`docs/assets/`](../docs/assets/).

Requirements: Go 1.27.2+ and Node.js 20+.

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

## `test-barcode-decoder.js`

Unit test of `static/js/barcode.js`, the scanner's own EAN-13/EAN-8 decoder, with no browser: synthetic symbols with blur, noise, a lighting gradient, upside down and vertical must be read; random stripes and a wrong check digit must not. `npm run test:barcode`.
