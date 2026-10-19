// End-to-end check, in headless Chromium, that a card of the 🔔 drawer
// ("Alertes de prix") leads somewhere: the page its price is on, in a new
// tab (target=_blank, rel="noopener noreferrer"), when there is one — the
// item's own link for a drop, the deal's page for a deal — or else the
// item in its Trakka list, scrolled into view and highlighted (also when
// it's in the collapsed "done" section, and through a ?list=&item= deep
// link, and a push notification tapped while Trakka is open). The card's
// ✕ and its "Ignorer" button must not navigate; a tap on
// a card whose item a filter hides says so. Checks the hover feedback, a
// 320px-wide phone, and a dark-theme drawer.
//
// The list and its items are real (created through the API of a freshly
// built server on a throwaway database); the drawer's two sources — the
// inbox (GET /api/v1/price-notifications) and the House's pending deals
// (GET /api/v1/price-alerts) — are served by page.route, since only a
// background price scan of a real shop page creates them. That their JSON
// carries item_url/list_id/item_id is checked server-side, in
// internal/handlers' TestHandlePriceNotificationsCarryItemLink. Outside
// pages are stubbed too: nothing leaves the machine.
//
// Usage (see README.md): cd scripts && npm run test:price-alert-links

import { chromium } from 'playwright';
import { spawn, execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const PASSWORD = 'correct-horse-battery';
const OWNER = { name: 'Camille', email: 'camille@example.com' };
const SHOP_URL = 'https://www.shop.example.com/casque-sony';
const DEAL_URL = 'https://www.dealabs.com/bons-plans/aspirateur-123';

let failures = 0;
function check(name, ok, detail = '') {
  if (!ok) failures++;
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`);
}

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.unref();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

async function startServer(workDir) {
  const binary = path.join(workDir, 'trakka');
  if (!existsSync(binary)) execFileSync('go', ['build', '-o', binary, './cmd/server'], { cwd: REPO_ROOT, stdio: 'inherit' });
  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const child = spawn(binary, [], {
    cwd: REPO_ROOT,
    stdio: ['ignore', 'ignore', 'inherit'],
    env: {
      ...process.env,
      PORT: String(port),
      DB_PATH: path.join(workDir, 'price-alert-links.db'),
      STATIC_DIR: path.join(REPO_ROOT, 'static'),
      TEMPLATES_DIR: path.join(REPO_ROOT, 'templates'),
      SESSION_COOKIE_SECURE: 'false',
      DEFAULT_APP_LANGUAGE: 'fr',
      APP_TIMEZONE: 'Europe/Paris',
      REGISTRATION_OPEN: 'true',
      PRODUCT_LOOKUP_ENABLED: 'false',
      LOG_LEVEL: 'warn',
    },
  });
  const deadline = Date.now() + 20_000;
  for (;;) {
    try {
      if ((await fetch(`${baseURL}/healthz`)).ok) break;
    } catch {
      // not listening yet
    }
    if (Date.now() > deadline) throw new Error('server did not start');
    await new Promise((r) => setTimeout(r, 200));
  }
  return { baseURL, stop: () => new Promise((r) => { child.once('exit', r); child.kill('SIGTERM'); }) };
}

async function api(page, method, url, body) {
  return page.evaluate(async ([m, u, b]) => {
    const r = await fetch(u, { method: m, headers: b === undefined ? {} : { 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) });
    const text = await r.text();
    return { status: r.status, body: text ? JSON.parse(text) : null };
  }, [method, url, body]);
}

// SHOTS_DIR=/some/dir saves a screenshot at each step worth looking at.
async function shot(page, name) {
  if (process.env.SHOTS_DIR) await page.screenshot({ path: path.join(process.env.SHOTS_DIR, `${name}.png`) });
}

function measureOverflow(page) {
  return page.evaluate(() => {
    const vw = document.documentElement.clientWidth;
    const out = [];
    for (const el of document.querySelectorAll('#notifications-modal *')) {
      const r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0 || r.right <= vw + 0.5) continue;
      let clipped = false;
      for (let p = el.parentElement; p; p = p.parentElement) {
        const style = getComputedStyle(p);
        if (/(auto|scroll|hidden|clip)/.test(style.overflowX) && p.getBoundingClientRect().right <= vw + 0.5) { clipped = true; break; }
      }
      if (!clipped) out.push(`${el.tagName}.${String(el.className).slice(0, 40)} → ${Math.round(r.right)}`);
    }
    return out.slice(0, 5);
  });
}

async function newContext(browser, opts = {}) {
  const context = await browser.newContext({
    viewport: { width: 390, height: 844 },
    isMobile: true,
    hasTouch: true,
    locale: 'fr-FR',
    serviceWorkers: 'block',
    ...opts,
  });
  await context.addInitScript(() => {
    try { localStorage.setItem('trakka:lang', 'fr'); } catch { /* not needed */ }
  });
  // The outside pages a card opens.
  await context.route(/^https:\/\/www\.(shop\.example|dealabs)\.com\//, (route) =>
    route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>Boutique</title><p>Boutique</p>' }));
  return context;
}

async function register(context, baseURL, user, errors) {
  const page = await context.newPage();
  watchErrors(page, errors);
  await page.goto(`${baseURL}/auth/login?mode=register`);
  await page.fill('#display_name', user.name);
  await page.fill('#email', user.email);
  await page.fill('#password', PASSWORD);
  await page.fill('#password_confirm', PASSWORD);
  await Promise.all([page.waitForURL(`${baseURL}/`), page.click('form:has(#password_confirm) [type=submit]')]);
  await page.waitForTimeout(400);
  return page;
}

function watchErrors(page, errors) {
  page.on('pageerror', (err) => errors.push(err.message));
  page.on('console', (msg) => {
    if (msg.type() === 'error' && !/service worker|Failed to load resource/i.test(msg.text())) errors.push(msg.text());
  });
}

// Serves the drawer's two sources. `fixtures.alerts` are the House's
// pending deals; a PATCH resolving one answers like the server would.
async function serveDrawer(page, fixtures) {
  await page.route('**/api/v1/price-notifications', (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    return route.fulfill({ status: 200, contentType: 'application/json', headers: { 'Cache-Control': 'no-store' }, body: JSON.stringify(fixtures.notifications) });
  });
  await page.route(/\/api\/v1\/price-alerts(\?|\/)/, (route) => {
    const req = route.request();
    if (req.method() === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', headers: { 'Cache-Control': 'no-store' }, body: JSON.stringify(fixtures.alerts) });
    }
    fixtures.patched.push(req.url());
    const id = Number(req.url().split('/').pop());
    const status = JSON.parse(req.postData() || '{}').status;
    fixtures.alerts = fixtures.alerts.filter((a) => a.id !== id);
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id, status }) });
  });
}

async function openDrawer(page) {
  if (await page.isVisible('#items-section')) {
    await page.click('#back-button');
    await page.waitForTimeout(300);
  }
  await page.click('#notifications-button');
  await page.locator('#notifications-modal').waitFor({ state: 'visible' });
  await page.waitForTimeout(400);
}

const card = (page, title) => page.locator('#notifications-list > li', { hasText: title }).first();

// Whether the item's row is in the list view, inside the viewport, and
// highlighted. On a touch device the row is the swipe wrapper, the card
// inside it.
async function rowState(page, itemId) {
  await page.waitForTimeout(800); // the smooth scroll
  return page.evaluate((id) => {
    const row = document.querySelector(`#items-section [data-item-id="${id}"]`);
    if (!row) return null;
    const cardEl = row.classList.contains('item-card') ? row : row.querySelector(':scope > .item-card');
    const r = row.getBoundingClientRect();
    return {
      inView: r.top >= 0 && r.bottom <= window.innerHeight,
      spotlight: cardEl.classList.contains('item-card--spotlight'),
      outline: getComputedStyle(cardEl).outlineStyle,
    };
  }, itemId);
}

async function main() {
  const workDir = await mkdtemp(path.join(tmpdir(), 'trakka-price-links-'));
  const server = await startServer(workDir);
  const browser = await chromium.launch();
  const errors = [];
  try {
    const context = await newContext(browser);
    const page = await register(context, server.baseURL, OWNER, errors);
    const [house] = (await api(page, 'GET', '/api/v1/houses')).body;
    const list = (await api(page, 'POST', '/api/v1/lists', { name: 'Équipement maison', type: 'shopping', house_id: house.id })).body;
    const create = async (body) => (await api(page, 'POST', '/api/v1/items', { list_id: list.id, price: 20, ...body })).body;
    // Enough items that the ones looked for are below the fold.
    for (let i = 1; i <= 25; i++) await create({ title: `Article ${i}` });
    const headphones = await create({ title: 'Casque Sony WH-1000XM5', url: SHOP_URL, price: 329 });
    const batteries = await create({ title: 'Piles AA rechargeables', price: 18 });
    const vacuum = await create({ title: 'Aspirateur balai', price: 249 });
    const kettle = await create({ title: 'Bouilloire inox', price: 45 });
    await api(page, 'PATCH', `/api/v1/items/${kettle.id}`, { done: true });

    const now = new Date().toISOString();
    const entry = (id, item, extra) => ({ id, item_id: item.id, item_title: item.title, list_id: list.id, kind: 'drop', old_price: 50, new_price: 40, created_at: now, ...extra });
    const fixtures = {
      notifications: [
        entry(1, headphones, { item_url: SHOP_URL, old_price: 379, new_price: 329 }),
        entry(2, batteries, { old_price: 22, new_price: 18 }),
        entry(3, kettle, { kind: 'increase', old_price: 39, new_price: 45 }),
        // A javascript: url never becomes a link: the entry falls back to the list.
        entry(4, vacuum, { kind: 'increase', item_url: 'javascript:alert(1)', old_price: 239, new_price: 249 }),
      ],
      alerts: [{ id: 9, item_id: vacuum.id, item_title: vacuum.title, list_id: list.id, original_price: 249, found_price: 199, source_url: DEAL_URL, changes_url: true, status: 'pending', created_at: now }],
      patched: [],
    };
    await serveDrawer(page, fixtures);
    await page.reload();
    await page.waitForTimeout(800);

    // 1. What each card offers.
    await openDrawer(page);
    await shot(page, '01-drawer');
    const hpCard = card(page, headphones.title);
    const hpPrimary = hpCard.getByRole('link', { name: 'Ouvrir le site ↗' });
    check('a drop with a link: "Ouvrir le site ↗"', await hpPrimary.count() === 1);
    check('…opening a new tab, noopener noreferrer',
      await hpPrimary.getAttribute('target') === '_blank' && await hpPrimary.getAttribute('rel') === 'noopener noreferrer' && await hpPrimary.getAttribute('href') === SHOP_URL);
    check('…with the site\'s name', (await hpCard.textContent()).includes('shop.example.com'));
    check('…and "Voir dans la liste" too', await hpCard.getByRole('link', { name: 'Voir dans la liste' }).count() === 1);
    const batCard = card(page, batteries.title);
    check('a drop without a link: only "Voir dans la liste"',
      await batCard.getByRole('link').count() === 1 && await batCard.getByRole('link', { name: 'Voir dans la liste' }).getAttribute('href') === `/?list=${list.id}&item=${batteries.id}`);
    const vacuumEntry = page.locator(`#notifications-list > li[data-price-notification-id="4"]`);
    check('a javascript: item_url is never linked', await vacuumEntry.locator('a[href^="javascript"]').count() === 0 && await vacuumEntry.getByRole('link').count() === 1);
    const dealCard = page.locator('#notifications-list > li:not([data-price-notification-id])', { hasText: vacuum.title });
    check('a pending deal: "Voir la promotion ↗" to the deal', await dealCard.getByRole('link', { name: 'Voir la promotion ↗' }).getAttribute('href') === DEAL_URL);
    check('cards show a pointer', await hpCard.evaluate((el) => getComputedStyle(el).cursor) === 'pointer');

    // 2. A tap on the card body (its title) opens the shop page in a new tab.
    let [popup] = await Promise.all([context.waitForEvent('page'), hpCard.locator('p').first().click()]);
    await popup.waitForLoadState();
    check('tapping a card with a link opens it in a new tab', popup.url() === SHOP_URL, popup.url());
    check('…with no window.opener', await popup.evaluate(() => window.opener === null));
    await popup.close();
    check('…the drawer stays open, the app where it was', await page.isVisible('#notifications-modal') && page.url() === `${server.baseURL}/`);

    // 3. ✕ and "Ignorer" don't navigate.
    const pagesBefore = context.pages().length;
    await page.locator('#notifications-list > li[data-price-notification-id="3"] button[aria-label="Supprimer cette alerte"]').click();
    await page.waitForTimeout(600);
    check('✕ removes the entry without navigating',
      context.pages().length === pagesBefore && await page.isVisible('#notifications-modal') && await page.isHidden('#items-section') &&
      await page.locator('#notifications-list > li[data-price-notification-id="3"]').count() === 0);
    fixtures.notifications = fixtures.notifications.filter((n) => n.id !== 3);
    await dealCard.getByRole('button', { name: 'Ignorer' }).click();
    await page.waitForTimeout(600);
    check('"Ignorer" resolves the deal without navigating',
      context.pages().length === pagesBefore && await page.isVisible('#notifications-modal') && await page.isHidden('#items-section') &&
      fixtures.patched.length === 1 && fixtures.patched[0].endsWith('/price-alerts/9'));

    // 4. A tap on a card without a link opens the list on the item.
    await batCard.locator('p').first().click();
    await page.locator('#items-section').waitFor({ state: 'visible' });
    await page.waitForTimeout(250);
    let state = await rowState(page, batteries.id);
    check('tapping a card without a link opens its list, drawer closed', await page.isHidden('#notifications-modal') && state !== null);
    check('…scrolled to the item', state?.inView === true, JSON.stringify(state));
    check('…highlighted', state?.spotlight === true && state?.outline === 'solid', JSON.stringify(state));
    await shot(page, '02-spotlight');
    await page.waitForTimeout(1800);
    state = await rowState(page, batteries.id);
    check('…for a moment only', state?.spotlight === false, JSON.stringify(state));

    // 5. "Voir dans la liste" next to an outside link.
    await openDrawer(page);
    await card(page, headphones.title).getByRole('link', { name: 'Voir dans la liste' }).click();
    await page.locator('#items-section').waitFor({ state: 'visible' });
    await page.waitForTimeout(250);
    state = await rowState(page, headphones.id);
    check('"Voir dans la liste" opens the list on the item, no new tab', context.pages().length === pagesBefore && state?.inView && state?.spotlight, JSON.stringify(state));

    // 6. A done item: the "done" section opens first.
    fixtures.notifications.push(entry(5, kettle, { old_price: 49, new_price: 45 }));
    await openDrawer(page);
    await card(page, kettle.title).locator('p').first().click();
    await page.locator('#items-section').waitFor({ state: 'visible' });
    await page.waitForTimeout(250);
    state = await rowState(page, kettle.id);
    check('a done item: the done section opens, the item is shown',
      await page.evaluate(() => document.getElementById('done-section').open) && state?.inView && state?.spotlight, JSON.stringify(state));

    // 7. A filter hiding the item: the list opens, and says why.
    await page.evaluate((id) => localStorage.setItem(`trakka:filters:${id}`, JSON.stringify({ status: 'active' })), list.id);
    await openDrawer(page);
    await card(page, kettle.title).locator('p').first().click();
    await page.locator('#items-section').waitFor({ state: 'visible' });
    await page.waitForTimeout(300);
    check('an item a filter hides: a toast says so',
      await page.locator('#toast-container').getByText('Cet article est masqué par les filtres de la liste.').count() === 1);
    await page.evaluate((id) => localStorage.removeItem(`trakka:filters:${id}`), list.id);

    // 8. The ?list=&item= deep link.
    await page.goto(`${server.baseURL}/?list=${list.id}&item=${batteries.id}`);
    await page.locator('#items-section').waitFor({ state: 'visible' });
    await page.waitForTimeout(400);
    state = await rowState(page, batteries.id);
    check('the ?list=&item= deep link opens the list on the item', state?.inView && state?.spotlight, JSON.stringify(state));
    check('…and is consumed', page.url() === `${server.baseURL}/`, page.url());

    // 9. A push tapped while Trakka is already open: sw.js posts the
    //    payload's url (priceChangePayload's /?list=&item=) to the tab.
    await page.click('#back-button');
    await page.waitForTimeout(300);
    await page.evaluate((url) => handleNotificationClickMessage(url), `/?list=${list.id}&item=${headphones.id}`);
    await page.locator('#items-section').waitFor({ state: 'visible' });
    state = await rowState(page, headphones.id);
    check('a push tapped in an open tab opens the list on the item', state?.inView && state?.spotlight, JSON.stringify(state));
    await context.close();

    // 10. Desktop: the hover lifts the card.
    const desktop = await newContext(browser, { viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false });
    const desktopPage = await desktop.newPage();
    watchErrors(desktopPage, errors);
    await desktopPage.goto(`${server.baseURL}/auth/login`);
    await desktopPage.fill('#email', OWNER.email);
    await desktopPage.fill('#password', PASSWORD);
    await Promise.all([desktopPage.waitForURL(`${server.baseURL}/`), desktopPage.click('form:has(#password) [type=submit]')]);
    await serveDrawer(desktopPage, fixtures);
    await desktopPage.reload();
    await desktopPage.waitForTimeout(600);
    await openDrawer(desktopPage);
    const desktopCard = card(desktopPage, batteries.title);
    const bg = () => desktopCard.evaluate((el) => getComputedStyle(el).backgroundColor);
    await desktopPage.mouse.move(5, 5);
    await desktopPage.waitForTimeout(300);
    const before = await bg();
    await desktopCard.hover({ position: { x: 20, y: 30 } });
    await desktopPage.waitForTimeout(300);
    const after = await bg();
    check('hovering a card changes its background', before !== after, `${before} → ${after}`);
    await desktop.close();

    // 11. A 320px phone, light and dark: nothing overflows the drawer.
    for (const colorScheme of ['light', 'dark']) {
      const small = await newContext(browser, { viewport: { width: 320, height: 640 }, colorScheme });
      const smallPage = await small.newPage();
      watchErrors(smallPage, errors);
      await smallPage.goto(`${server.baseURL}/auth/login`);
      await smallPage.fill('#email', OWNER.email);
      await smallPage.fill('#password', PASSWORD);
      await Promise.all([smallPage.waitForURL(`${server.baseURL}/`), smallPage.click('form:has(#password) [type=submit]')]);
      fixtures.alerts = [{ id: 10, item_id: vacuum.id, item_title: vacuum.title, list_id: list.id, original_price: 249, found_price: 199, source_url: DEAL_URL, changes_url: true, status: 'pending', created_at: now }];
      await serveDrawer(smallPage, fixtures);
      await smallPage.reload();
      await smallPage.waitForTimeout(600);
      await openDrawer(smallPage);
      const overflow = await measureOverflow(smallPage);
      check(`320px, ${colorScheme}: nothing overflows the drawer`, overflow.length === 0, overflow.join(' | '));
      await shot(smallPage, `03-drawer-320-${colorScheme}`);
      await small.close();
    }

    check('no page errors', errors.length === 0, errors.join(' | '));
  } finally {
    await browser.close();
    await server.stop();
    await rm(workDir, { recursive: true, force: true });
  }
  process.exit(failures ? 1 : 0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
