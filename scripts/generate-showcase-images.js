// Regenerates the README showcase images in docs/assets/.
//
//   cd scripts && npm install && npx playwright install chromium
//   npm run showcase
//
// What it does, end to end:
//   1. Builds ./cmd/server and starts it on a free port against a throwaway
//      SQLite file in a temp dir — the images never depend on (or touch) a
//      developer's real database. Set TRAKKA_URL to drive an already-running
//      instance instead; it must be an empty one with registration open,
//      since the demo account and its data are created from scratch.
//   2. Registers a demo account and seeds English demo data through the
//      ordinary REST API (same-origin fetch() from the logged-in page, so the
//      CSRF/Origin checks pass exactly as they would for the real frontend).
//   3. Captures raw screenshots with Playwright — desktop (1440×900) and
//      mobile (390×844), each in light and dark — with the service worker
//      blocked so nothing is served from a stale cache.
//   4. Composes the final images in Chromium itself (plain HTML/CSS/SVG
//      rendered to PNG), so there's no native image dependency: each desktop
//      screen inside a browser-window frame (one full image per theme, plus a
//      diagonal light/dark split), and each mobile screen inside a phone frame.
//
// Output (all paths relative, referenced from README.md):
//   docs/assets/showcase-desktop-split.png
//   docs/assets/desktop-{light,dark}.png, desktop-list-{light,dark}.png
//   docs/assets/showcase-mobile-{home,selection,recurrence}-{light,dark}.png

import { chromium } from 'playwright';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const OUT_DIR = path.join(REPO_ROOT, 'docs', 'assets');

const DEMO_USER = { name: 'Alex', email: 'alex@trakka.demo', password: 'showcase-demo-password' };
const TIMEZONE = 'UTC';

const DESKTOP = { width: 1440, height: 900, dpr: 2 };
const MOBILE = { width: 390, height: 844, dpr: 3 };
const THEMES = ['light', 'dark'];

// ---------------------------------------------------------------------------
// Local server lifecycle
// ---------------------------------------------------------------------------

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

async function waitForHealthy(baseURL, timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${baseURL}/healthz`);
      if (res.ok) return;
    } catch {
      // not listening yet
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`server at ${baseURL} did not become healthy within ${timeoutMs}ms`);
}

async function startServer() {
  const workDir = await mkdtemp(path.join(tmpdir(), 'trakka-showcase-'));
  const binary = path.join(workDir, 'trakka');
  console.log('• building ./cmd/server');
  execFileSync('go', ['build', '-o', binary, './cmd/server'], { cwd: REPO_ROOT, stdio: 'inherit' });

  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const child = spawn(binary, [], {
    cwd: REPO_ROOT,
    stdio: ['ignore', 'ignore', 'inherit'],
    env: {
      ...process.env,
      PORT: String(port),
      DB_PATH: path.join(workDir, 'showcase.db'),
      STATIC_DIR: path.join(REPO_ROOT, 'static'),
      TEMPLATES_DIR: path.join(REPO_ROOT, 'templates'),
      SESSION_COOKIE_SECURE: 'false',
      DEFAULT_APP_LANGUAGE: 'en',
      APP_TIMEZONE: TIMEZONE,
      INSTANCE_NAME: 'Trakka',
      REGISTRATION_OPEN: 'true',
    },
  });
  console.log(`• server starting on ${baseURL} (temp dir ${workDir})`);
  await waitForHealthy(baseURL);

  return {
    baseURL,
    async stop() {
      child.kill('SIGTERM');
      await new Promise((r) => child.once('exit', r));
      await rm(workDir, { recursive: true, force: true });
    },
  };
}

// ---------------------------------------------------------------------------
// Demo data
// ---------------------------------------------------------------------------

function isoDate(offsetDays) {
  const d = new Date();
  d.setUTCHours(12, 0, 0, 0);
  d.setUTCDate(d.getUTCDate() + offsetDays);
  return d.toISOString().slice(0, 10);
}

function isoMonth(offsetMonths) {
  const d = new Date();
  d.setUTCDate(1);
  d.setUTCMonth(d.getUTCMonth() + offsetMonths);
  return d.toISOString().slice(0, 7);
}

const BYDAY = ['SU', 'MO', 'TU', 'WE', 'TH', 'FR', 'SA'];
function weekdayOf(offsetDays) {
  return BYDAY[new Date(`${isoDate(offsetDays)}T12:00:00Z`).getUTCDay()];
}

// Lists are created last-to-first: the dashboard shows the newest first.
// `done: true` items are checked off after creation (PATCH), exactly like a
// user ticking them.
function demoLists() {
  const reminder = (time, offsetDays = 0) => ({ reminder_enabled: true, reminder_offset_days: offsetDays, reminder_time: time, reminder_at_due_time: false });
  return [
    {
      name: 'Gift Ideas', type: 'custom', icon: '🎁',
      items: [
        { title: 'LEGO Botanical set for Sam' },
        { title: 'Cooking class voucher', labels: ['Experience'] },
        { title: 'Wireless earbuds for Dad' },
        { title: 'Photo book of the summer trip', done: true },
      ],
    },
    {
      name: 'Weekend Errands', type: 'todo', icon: '🚗',
      items: [
        { title: 'Return library books', due_date: isoDate(1) },
        { title: 'Pick up dry cleaning', due_date: isoDate(2), due_time: '11:00' },
        { title: 'Car wash & tire pressure check' },
        { title: 'Buy a birthday card for Mia', done: true },
      ],
    },
    {
      name: 'Weekly Chores', type: 'todo', icon: '🧹',
      items: [
        { title: 'Book the pediatrician check-up', due_date: isoDate(0), is_urgent: true, labels: ['Baby'] },
        {
          title: 'Take out the recycling', due_date: isoDate(1), due_time: '19:00',
          recurrence_rule: `FREQ=WEEKLY;BYDAY=${weekdayOf(1)}`, ...reminder('18:30'), labels: ['Outdoor'],
        },
        { title: 'Water the plants', due_date: isoDate(0), recurrence_rule: 'FREQ=DAILY;INTERVAL=3', ...reminder('08:00') },
        {
          title: 'Vacuum the living room', due_date: isoDate(2),
          recurrence_rule: `FREQ=WEEKLY;BYDAY=${weekdayOf(2)}`, ...reminder('10:00'),
        },
        { title: 'Change the bed sheets', due_date: isoDate(4), recurrence_rule: 'FREQ=WEEKLY;INTERVAL=2', ...reminder('09:00') },
        { title: 'Clean out the fridge', due_date: isoDate(9), recurrence_rule: 'FREQ=MONTHLY', ...reminder('20:00', 1) },
        { title: 'Fix the squeaky hallway door', done: true },
        { title: 'Wipe down the high chair', done: true },
      ],
    },
    {
      name: 'Household Refills', type: 'recurring_shopping', icon: '🔁',
      items: [
        { title: 'Coffee beans, 1 kg', price: 22.5, due_date: isoDate(3), recurrence_rule: 'FREQ=WEEKLY;INTERVAL=2' },
        { title: 'Dishwasher tablets', price: 14.99, due_date: isoDate(6), recurrence_rule: 'FREQ=MONTHLY' },
        { title: 'Water filter cartridges', price: 29.9, due_date: isoDate(18), recurrence_rule: 'FREQ=MONTHLY;INTERVAL=3' },
        { title: 'Laundry detergent', price: 11.49, due_date: isoDate(11), recurrence_rule: 'FREQ=MONTHLY' },
      ],
    },
    {
      name: 'Home Office Upgrade', type: 'shopping', icon: '🖥️',
      items: [
        { title: 'Standing desk frame', price: 349, target_month: isoMonth(1), labels: ['Furniture'] },
        { title: 'Ergonomic chair', price: 279, target_month: isoMonth(2), labels: ['Furniture'], target_price: 240, alert_on_price_drop: true },
        { title: 'Monitor arm', price: 59.9 },
        { title: 'LED desk lamp', price: 45 },
        { title: 'USB-C docking station', price: 129.99, done: true },
      ],
    },
    {
      name: 'Groceries', type: 'groceries', icon: '🛒',
      items: [
        { title: 'Coffee beans', is_urgent: true },
        { title: 'Whole milk', quantity: 2 },
        { title: 'Free-range eggs (dozen)' },
        { title: 'Bananas', quantity: 6 },
        { title: 'Sourdough bread' },
        { title: 'Greek yogurt', quantity: 4 },
        { title: 'Aged cheddar' },
        { title: 'Baby spinach', done: true },
        { title: 'Avocados', quantity: 3, done: true },
      ],
    },
    {
      name: 'Baby Essentials', type: 'shopping', icon: '🍼',
      items: [
        { title: 'Diapers, size 3 (box of 96)', quantity: 2, price: 24.99, labels: ['Essentials'], is_urgent: true },
        { title: 'Bottle sterilizer', price: 49.9, labels: ['Feeding'], target_month: isoMonth(0) },
        { title: 'Sleep sack, 6–18 months', price: 34.5, labels: ['Nursery'] },
        { title: 'Baby monitor with camera', price: 89, labels: ['Nursery'], target_month: isoMonth(1), target_price: 75, alert_on_price_drop: true },
        { title: 'Teething rings', quantity: 2, price: 7.99, labels: ['Essentials'] },
        { title: 'Nursing pillow', price: 39.95, labels: ['Feeding'] },
        { title: 'Baby wipes, 6-pack', price: 12.49, labels: ['Essentials'], done: true },
        { title: 'Stroller rain cover', price: 29.99, done: true },
      ],
    },
  ];
}

// Same-origin fetch() from inside the logged-in page: carries the session
// cookie and a genuine Origin header, like the real frontend.
async function api(page, method, url, body) {
  const res = await page.evaluate(async ([m, u, b]) => {
    const r = await fetch(u, {
      method: m,
      headers: b === undefined ? {} : { 'Content-Type': 'application/json' },
      body: b === undefined ? undefined : JSON.stringify(b),
    });
    return { status: r.status, text: await r.text() };
  }, [method, url, body]);
  if (res.status >= 400) throw new Error(`${method} ${url} → ${res.status}: ${res.text}`);
  return res.text ? JSON.parse(res.text) : null;
}

async function register(page, baseURL) {
  await page.goto(`${baseURL}/auth/login?mode=register`);
  await page.fill('#display_name', DEMO_USER.name);
  await page.fill('#email', DEMO_USER.email);
  await page.fill('#password', DEMO_USER.password);
  await page.fill('#password_confirm', DEMO_USER.password);
  await Promise.all([
    page.waitForURL(`${baseURL}/`),
    page.click('form:has(#password_confirm) [type=submit]'),
  ]);
}

async function login(page, baseURL) {
  await page.goto(`${baseURL}/auth/login`);
  await page.fill('#email', DEMO_USER.email);
  await page.fill('#password', DEMO_USER.password);
  await Promise.all([
    page.waitForURL(`${baseURL}/`),
    page.click('form:has(#password):not(:has(#password_confirm)) [type=submit]'),
  ]);
}

async function seed(page, baseURL) {
  console.log('• seeding demo data');
  await register(page, baseURL);
  await api(page, 'PATCH', '/api/v1/me', { language: 'en', keep_last_page: false });

  const [house] = await api(page, 'GET', '/api/v1/houses');
  await api(page, 'PUT', `/api/v1/houses/${house.id}`, { name: 'Home' });

  for (const list of demoLists()) {
    const created = await api(page, 'POST', '/api/v1/lists', {
      name: list.name, type: list.type, icon: list.icon, house_id: house.id,
    });
    for (const [position, { done, ...item }] of list.items.entries()) {
      const it = await api(page, 'POST', '/api/v1/items', { list_id: created.id, position, ...item });
      if (done) await api(page, 'PATCH', `/api/v1/items/${it.id}`, { done: true });
    }
  }
}

// ---------------------------------------------------------------------------
// Raw captures
// ---------------------------------------------------------------------------

async function openContext(browser, baseURL, { viewport, theme }) {
  const mobile = viewport === MOBILE;
  const context = await browser.newContext({
    viewport: { width: viewport.width, height: viewport.height },
    deviceScaleFactor: viewport.dpr,
    isMobile: mobile,
    hasTouch: mobile,
    locale: 'en-US',
    timezoneId: TIMEZONE,
    colorScheme: theme,
    reducedMotion: 'reduce',
    serviceWorkers: 'block',
  });
  // Pin the UI language and theme before any app script runs (theme-init.js
  // reads trakka:theme synchronously in <head>).
  await context.addInitScript(([t]) => {
    try {
      localStorage.setItem('trakka:theme', t);
      localStorage.setItem('trakka:lang', 'en');
    } catch {
      // storage unavailable — the colorScheme emulation still applies
    }
  }, [theme]);
  const page = await context.newPage();
  page.on('pageerror', (err) => console.warn(`  [page error] ${err.message}`));
  await login(page, baseURL);
  await page.locator('#shopping-lists li').filter({ hasText: 'Baby Essentials' }).waitFor();
  await settle(page);
  return { context, page };
}

// Lets fonts, images and any in-flight render finish. (Not
// waitForLoadState('networkidle'): the app keeps periodic requests going, so
// the network never goes idle.)
async function settle(page) {
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(600);
}

async function openList(page, name) {
  await page.locator('#dashboard-view').getByText(name, { exact: true }).first().click();
  await page.locator('#items-section').waitFor({ state: 'visible' });
  await page.locator('#items-active li').first().waitFor();
  await settle(page);
}

async function shot(page, file) {
  const buf = await page.screenshot({ animations: 'disabled', caret: 'hide' });
  if (file) await writeFile(file, buf);
  return buf;
}

async function captureDesktop(browser, baseURL, theme) {
  const { context, page } = await openContext(browser, baseURL, { viewport: DESKTOP, theme });
  const dashboard = await shot(page);
  await openList(page, 'Baby Essentials');
  const list = await shot(page);
  await context.close();
  return { dashboard, list };
}

async function captureMobile(browser, baseURL, theme) {
  const { context, page } = await openContext(browser, baseURL, { viewport: MOBILE, theme });
  const home = await shot(page);

  // Selection & bulk actions: enter multi-select from the list options sheet,
  // then tap a few cards (a tap toggles a card while selecting).
  await openList(page, 'Baby Essentials');
  await page.click('#list-options-button');
  await page.locator('#select-items-button').click();
  await page.locator('#bulk-actions-bar').waitFor({ state: 'visible' });
  for (const title of ['Diapers, size 3 (box of 96)', 'Bottle sterilizer', 'Sleep sack, 6–18 months']) {
    await page.locator('#items-active li').filter({ hasText: title }).first().click();
  }
  await page.evaluate(() => window.scrollTo(0, 0));
  await settle(page);
  const selection = await shot(page);
  await page.click('#bulk-cancel-button');

  // Recurring task & reminder editor: the ⋮ menu's "Edit" on a weekly chore,
  // scrolled so the schedule/recurrence/reminder block is in view.
  await page.click('#back-button');
  await settle(page);
  await openList(page, 'Weekly Chores');
  const row = page.locator('#items-active li').filter({ hasText: 'Take out the recycling' }).first();
  await row.getByRole('button', { name: 'More actions for Take out the recycling' }).click();
  await page.click('#item-actions-edit-button');
  await page.locator('#edit-item-modal').waitFor({ state: 'visible' });
  await page.locator('#edit-item-modal [data-item-field="recurrence"]').first().scrollIntoViewIfNeeded();
  await page.evaluate(() => document.activeElement?.blur());
  await settle(page);
  const recurrence = await shot(page);

  await context.close();
  return { home, selection, recurrence };
}

// ---------------------------------------------------------------------------
// Composition (HTML/CSS rendered by Chromium → PNG)
// ---------------------------------------------------------------------------

const dataURI = (buf) => `data:image/png;base64,${buf.toString('base64')}`;

async function renderHTML(browser, { html, width, height, dpr, out }) {
  const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: dpr });
  await page.setContent(html, { waitUntil: 'load' });
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: out, omitBackground: true });
  await page.close();
  console.log(`  ✓ ${path.relative(REPO_ROOT, out)}`);
}

const FONT_STACK = `"Inter", "Segoe UI", "Noto Sans", system-ui, sans-serif`;

// Browser window frame (title bar with the three dots, rounded corners and
// a soft shadow) around a 1440×900 screen area. `screen` is the markup
// inside that area; `bar` is the title bar's CSS background.
function browserWindowHTML({ screen, bar, css = '' }) {
  const w = DESKTOP.width;
  const h = DESKTOP.height;
  const pad = 36;
  const barHeight = 40;
  return {
    width: w + pad * 2,
    height: h + barHeight + pad * 2,
    html: `<!doctype html><html><head><style>
      * { box-sizing: border-box; margin: 0; }
      html, body { background: transparent; }
      body { padding: ${pad}px; font-family: ${FONT_STACK}; }
      .window { width: ${w}px; border-radius: 14px; overflow: hidden; background: #0f172a;
        box-shadow: 0 24px 60px -12px rgba(15, 23, 42, .45), 0 0 0 1px rgba(148, 163, 184, .35); }
      .bar { height: ${barHeight}px; display: flex; align-items: center; gap: 8px; padding: 0 16px; background: ${bar}; }
      .dot { width: 12px; height: 12px; border-radius: 50%; }
      .screen { position: relative; width: ${w}px; height: ${h}px; }
      .screen img { position: absolute; inset: 0; width: 100%; height: 100%; display: block; }
      ${css}
    </style></head><body>
      <div class="window">
        <div class="bar">
          <span class="dot" style="background:#f87171"></span>
          <span class="dot" style="background:#fbbf24"></span>
          <span class="dot" style="background:#34d399"></span>
        </div>
        <div class="screen">${screen}</div>
      </div>
    </body></html>`,
  };
}

const WINDOW_BAR = { light: '#e2e8f0', dark: '#1e293b' };

// One theme, full window.
function desktopFullHTML(screenshot, theme) {
  return browserWindowHTML({ bar: WINDOW_BAR[theme], screen: `<img src="${dataURI(screenshot)}" alt="">` });
}

// The light screenshot underneath and the dark one clipped to the right of
// a diagonal, plus a thin divider along the cut and a label on each side.
function desktopSplitHTML(light, dark) {
  const w = DESKTOP.width;
  const h = DESKTOP.height;
  // Diagonal from (58% top) to (42% bottom), in screenshot coordinates.
  const x1 = w * 0.58;
  const x2 = w * 0.42;
  const mid = (x1 + x2) / 2;
  return browserWindowHTML({
    bar: `linear-gradient(90deg, ${WINDOW_BAR.light} 0 ${mid}px, ${WINDOW_BAR.dark} ${mid}px)`,
    css: `
      .dark { clip-path: polygon(${x1}px 0, ${w}px 0, ${w}px ${h}px, ${x2}px ${h}px); }
      svg { position: absolute; inset: 0; }
      .tag { position: absolute; bottom: 20px; padding: 6px 14px; border-radius: 999px; font-size: 14px;
        font-weight: 600; letter-spacing: .02em; backdrop-filter: blur(6px); }
      .tag.l { left: 20px; background: rgba(15, 23, 42, .08); color: #0f172a; border: 1px solid rgba(15, 23, 42, .12); }
      .tag.d { right: 20px; background: rgba(248, 250, 252, .1); color: #f8fafc; border: 1px solid rgba(248, 250, 252, .18); }`,
    screen: `
      <img src="${dataURI(light)}" alt="">
      <img class="dark" src="${dataURI(dark)}" alt="">
      <svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}">
        <defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stop-color="#8b5cf6"/><stop offset="1" stop-color="#6366f1"/>
        </linearGradient></defs>
        <line x1="${x1}" y1="0" x2="${x2}" y2="${h}" stroke="url(#g)" stroke-width="3"/>
      </svg>
      <span class="tag l">Light</span>
      <span class="tag d">Dark</span>`,
  });
}

// Phone frame: bezel, a status bar tinted to the app's header, and the
// screenshot below it. The raw capture already has the viewport's exact
// aspect, so the frame's screen area is just status bar + viewport.
function phoneHTML(screenshot, theme) {
  const w = MOBILE.width;
  const h = MOBILE.height;
  const status = 44;
  const bezel = 12;
  const pad = 28;
  const fg = theme === 'dark' ? '#f8fafc' : '#0f172a';
  const bg = theme === 'dark' ? '#0f172a' : '#ffffff';
  return {
    width: w + (bezel + pad) * 2,
    height: h + status + (bezel + pad) * 2,
    html: `<!doctype html><html><head><style>
      * { box-sizing: border-box; margin: 0; }
      html, body { background: transparent; }
      body { padding: ${pad}px; font-family: ${FONT_STACK}; }
      .phone { width: ${w + bezel * 2}px; padding: ${bezel}px; border-radius: 58px; background: #0b0f19;
        box-shadow: 0 0 0 2px #334155, 0 22px 50px -14px rgba(15, 23, 42, .55); }
      .screen { position: relative; border-radius: 46px; overflow: hidden; background: ${bg}; }
      .status { height: ${status}px; display: flex; align-items: center; justify-content: space-between;
        padding: 0 30px 0 38px; color: ${fg}; font-size: 15px; font-weight: 600; }
      .island { position: absolute; top: 10px; left: 50%; width: 112px; height: 30px; margin-left: -56px;
        border-radius: 20px; background: #000; }
      .icons { display: flex; gap: 6px; align-items: center; }
      img { display: block; width: ${w}px; height: ${h}px; }
    </style></head><body>
      <div class="phone"><div class="screen">
        <div class="status">
          <span>9:41</span>
          <span class="icons">
            <svg width="18" height="12" viewBox="0 0 18 12" fill="${fg}"><rect x="0" y="8" width="3" height="4" rx="1"/><rect x="5" y="5.5" width="3" height="6.5" rx="1"/><rect x="10" y="3" width="3" height="9" rx="1"/><rect x="15" y="0" width="3" height="12" rx="1"/></svg>
            <svg width="16" height="12" viewBox="0 0 16 12" fill="none" stroke="${fg}" stroke-width="1.8" stroke-linecap="round"><path d="M1.5 4.2a9.5 9.5 0 0 1 13 0"/><path d="M4 7a6 6 0 0 1 8 0"/><circle cx="8" cy="10" r="1.2" fill="${fg}" stroke="none"/></svg>
            <svg width="27" height="13" viewBox="0 0 27 13" fill="none"><rect x=".5" y=".5" width="23" height="12" rx="3.5" stroke="${fg}" opacity=".4"/><rect x="2" y="2" width="18" height="9" rx="2" fill="${fg}"/><rect x="25" y="4.5" width="1.6" height="4" rx=".8" fill="${fg}" opacity=".4"/></svg>
          </span>
        </div>
        <div class="island"></div>
        <img src="${dataURI(screenshot)}" alt="">
      </div></div>
    </body></html>`,
  };
}

// ---------------------------------------------------------------------------

async function main() {
  await mkdir(OUT_DIR, { recursive: true });
  const server = process.env.TRAKKA_URL ? null : await startServer();
  const baseURL = process.env.TRAKKA_URL || server.baseURL;
  const browser = await chromium.launch();

  try {
    const seedContext = await browser.newContext({ serviceWorkers: 'block', locale: 'en-US', timezoneId: TIMEZONE });
    await seed(await seedContext.newPage(), baseURL);
    await seedContext.close();

    console.log('• capturing screens');
    const desktop = {};
    const mobile = {};
    for (const theme of THEMES) {
      desktop[theme] = await captureDesktop(browser, baseURL, theme);
      mobile[theme] = await captureMobile(browser, baseURL, theme);
    }

    console.log('• composing images');
    const split = desktopSplitHTML(desktop.light.dashboard, desktop.dark.dashboard);
    await renderHTML(browser, { ...split, dpr: 1.25, out: path.join(OUT_DIR, 'showcase-desktop-split.png') });

    for (const theme of THEMES) {
      const full = desktopFullHTML(desktop[theme].dashboard, theme);
      await renderHTML(browser, { ...full, dpr: 1.25, out: path.join(OUT_DIR, `desktop-${theme}.png`) });
      const fullList = desktopFullHTML(desktop[theme].list, theme);
      await renderHTML(browser, { ...fullList, dpr: 1.25, out: path.join(OUT_DIR, `desktop-list-${theme}.png`) });
    }

    for (const theme of THEMES) {
      for (const screen of ['home', 'selection', 'recurrence']) {
        const phone = phoneHTML(mobile[theme][screen], theme);
        await renderHTML(browser, { ...phone, dpr: 2, out: path.join(OUT_DIR, `showcase-mobile-${screen}-${theme}.png`) });
      }
    }
  } finally {
    await browser.close();
    if (server) await server.stop();
  }
  console.log('done.');
}

main().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
