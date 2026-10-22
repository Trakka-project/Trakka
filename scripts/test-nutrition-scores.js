// End-to-end check of the nutrition scores (Nutri-Score / NOVA) on a
// "Courses" list, in a real browser: hidden by default, shown per kind of
// screen (Paramètres → Scores nutritionnels: mobile and web on their own),
// the badge sitting right before the title without moving the checkbox or
// the kebab, nothing overflowing a 320px phone, and the edit modal's
// collapsible nutrition panel (opened by a tap on the row): badges in its
// summary, the A-E and NOVA scales, the help links, the empty state's
// lookup, and a legacy Yuka score neither shown nor lost.
//
// Usage (see README.md): cd scripts && npm run test:nutrition-scores
// Builds ./cmd/server and runs it on a throwaway database, like the other
// scripts; PRODUCT_LOOKUP_ENABLED=false keeps it off the network.

import { chromium } from 'playwright';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const USER = { name: 'Camille', email: 'camille@example.com', password: 'correct-horse-battery' };

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
  execFileSync('go', ['build', '-o', binary, './cmd/server'], { cwd: REPO_ROOT, stdio: 'inherit' });
  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const child = spawn(binary, [], {
    cwd: REPO_ROOT,
    stdio: ['ignore', 'ignore', 'inherit'],
    env: {
      ...process.env,
      PORT: String(port),
      DB_PATH: path.join(workDir, 'nutrition.db'),
      STATIC_DIR: path.join(REPO_ROOT, 'static'),
      TEMPLATES_DIR: path.join(REPO_ROOT, 'templates'),
      SESSION_COOKIE_SECURE: 'false',
      DEFAULT_APP_LANGUAGE: 'fr',
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

async function setTheme(page, theme) {
  await page.evaluate((th) => TrakkaTheme.set(th), theme);
  await page.waitForTimeout(300);
}

// Ticks/unticks the two "Scores nutritionnels" switches and saves.
async function setDisplay(page, { mobile, web }) {
  await page.click('#user-settings-button');
  await page.locator('#user-settings-modal').waitFor({ state: 'visible' });
  for (const [id, on] of [['#user-settings-nutrition-mobile', mobile], ['#user-settings-nutrition-web', web]]) {
    if ((await page.isChecked(id)) !== on) await page.locator(id).locator('xpath=ancestor::label').click();
  }
  if (process.env.SHOTS_DIR) {
    await page.locator('#user-settings-nutrition-title').scrollIntoViewIfNeeded();
    await shot(page, `settings-mobile-${mobile}-web-${web}`);
  }
  await page.click('#user-settings-footer [type=submit]');
  await page.locator('#user-settings-status').waitFor({ state: 'visible' });
  await page.click('#close-user-settings-modal-button');
  await page.waitForTimeout(200);
}

// Geometry of every active row holding a badge: the badge, the title next
// to it, the checkbox and the trailing kebab/actions.
async function badgeRows(page) {
  return page.$$eval('#items-active .item-card', (rows) => rows.flatMap((li) => {
    const badge = li.querySelector('.nutrition-badges');
    if (!badge) return [];
    const box = (el) => { if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height, cy: r.y + r.height / 2, right: r.right }; };
    const title = badge.nextElementSibling;
    const titleText = title.matches('span') ? title : title.querySelector('span');
    const kebab = [...li.querySelectorAll('.item-card__kebab, .item-card__actions')].find((el) => el.getBoundingClientRect().width > 0);
    return [{
      title: titleText.textContent,
      badge: box(badge),
      titleBox: box(titleText),
      checkbox: box(li.querySelector('input[type=checkbox]')),
      kebab: box(kebab),
      labels: [...badge.querySelectorAll('[role=img]')].map((el) => el.getAttribute('aria-label')),
    }];
  }));
}

async function main() {
  const workDir = await mkdtemp(path.join(tmpdir(), 'trakka-nutrition-'));
  const server = await startServer(workDir);
  const browser = await chromium.launch();
  const errors = [];
  try {
    const context = await browser.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
      locale: 'fr-FR',
      serviceWorkers: 'block',
    });
    await context.addInitScript(() => {
      try { localStorage.setItem('trakka:lang', 'fr'); } catch { /* not needed */ }
    });
    const page = await context.newPage();
    page.on('pageerror', (err) => errors.push(err.message));
    page.on('console', (msg) => {
      if (msg.type() === 'error' && !/service worker/i.test(msg.text())) errors.push(msg.text());
    });

    await page.goto(`${server.baseURL}/auth/login?mode=register`);
    await page.fill('#display_name', USER.name);
    await page.fill('#email', USER.email);
    await page.fill('#password', USER.password);
    await page.fill('#password_confirm', USER.password);
    await Promise.all([page.waitForURL(`${server.baseURL}/`), page.click('form:has(#password_confirm) [type=submit]')]);
    await page.waitForTimeout(500);
    const [house] = (await api(page, 'GET', '/api/v1/houses')).body;
    const list = (await api(page, 'POST', '/api/v1/lists', { name: 'Courses de la semaine', type: 'groceries', house_id: house.id })).body;
    const products = [
      // Nutella also carries a legacy Yuka score: never shown, kept by edits.
      { title: 'Nutella', nutri_score: 'E', nova_group: 4, yuka_score: 12 },
      { title: 'Pommes Golden', nutri_score: 'A', nova_group: 1 },
      { title: 'Yaourt nature', nutri_score: 'B' },
      { title: 'Céréales au chocolat', nutri_score: 'D', nova_group: 4 },
      { title: 'Biscuits fourrés à la confiture de framboise extra-longs', nutri_score: 'C', nova_group: 3 },
      { title: 'Eau minérale' },
    ];
    for (const p of products) {
      const res = await api(page, 'POST', '/api/v1/items', { list_id: list.id, ...p });
      check(`POST ${p.title} answers its scores`, res.body.nutri_score === p.nutri_score && res.body.nova_group === p.nova_group,
        JSON.stringify([res.body.nutri_score, res.body.nova_group]));
    }
    await page.reload();
    await page.locator('#dashboard-view').getByText('Courses de la semaine', { exact: true }).first().click();
    await page.locator('#items-section').waitFor({ state: 'visible' });

    // 1. Off by default.
    check('no badge by default', (await page.locator('.nutrition-badges').count()) === 0);
    await shot(page, '1-mobile-default-off');

    // 2. Mobile on: badges on a phone.
    await setDisplay(page, { mobile: true, web: false });
    let rows = await badgeRows(page);
    check('mobile on: a badge on each scored item', rows.length === 5, rows.map((r) => r.title).join(' | '));
    check('…none on the item without scores', !rows.some((r) => r.title === 'Eau minérale'));
    check('…accessible labels, no Yuka badge', rows.some((r) => r.labels.join('|') === 'Nutri-Score E|NOVA 4 : aliment ultra-transformé'),
      JSON.stringify(rows.map((r) => r.labels)));
    const yaourt = rows.find((r) => r.title === 'Yaourt nature');
    check('…a Nutri-Score alone when no NOVA group', yaourt && yaourt.labels.length === 1 && yaourt.labels[0] === 'Nutri-Score B');
    for (const r of rows) {
      const before = r.badge.right <= r.titleBox.x + 0.5 && r.badge.x > r.checkbox.right;
      const aligned = Math.abs(r.badge.cy - r.checkbox.cy) <= 2 && (!r.kebab || Math.abs(r.kebab.cy - r.checkbox.cy) <= 2);
      check(`"${r.title}": badge between the checkbox and the title`, before, JSON.stringify({ badge: r.badge, title: r.titleBox.x }));
      check(`"${r.title}": badge, checkbox and kebab centred on one line`, aligned,
        JSON.stringify({ badge: r.badge.cy, checkbox: r.checkbox.cy, kebab: r.kebab && r.kebab.cy }));
    }
    const rowHeights = await page.$$eval('#items-active .item-card', (lis) => lis.map((li) => Math.round(li.getBoundingClientRect().height)));
    check('…rows with and without a badge are the same height', new Set(rowHeights).size === 1, rowHeights.join(','));
    await shot(page, '2-mobile-on-light');
    await setTheme(page, 'dark');
    await shot(page, '3-mobile-on-dark');
    await setTheme(page, 'light');

    // 3. 320px: the long title elides, nothing overflows.
    await page.setViewportSize({ width: 320, height: 700 });
    await page.waitForTimeout(200);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    check('320px: no horizontal overflow', overflow <= 0, `${overflow}px`);
    rows = await badgeRows(page);
    const long = rows.find((r) => r.title.startsWith('Biscuits'));
    check('320px: the badge keeps its size beside a long title', long && Math.round(long.badge.h) === 24 && long.badge.w >= 50, JSON.stringify(long && long.badge));
    // Next to a badge the title may take a second line instead of eliding
    // after a few letters: a name that fits on two lines is shown whole, a
    // longer one is clamped at two.
    const titles = await page.$$eval('#items-active .item-card .nutrition-badges', (badges) => badges.map((badge) => {
      const el = badge.nextElementSibling.matches('span') ? badge.nextElementSibling : badge.nextElementSibling.querySelector('span');
      const lineHeight = parseFloat(getComputedStyle(el).lineHeight);
      return { title: el.textContent, lines: Math.round(el.getBoundingClientRect().height / lineHeight), clipped: el.scrollHeight > el.clientHeight + 1 };
    }));
    const pommes = titles.find((x) => x.title === 'Pommes Golden');
    check('320px: a short name beside two badges is shown whole', pommes && !pommes.clipped && pommes.lines <= 2, JSON.stringify(pommes));
    const longTitle = titles.find((x) => x.title.startsWith('Biscuits'));
    check('320px: a long name is clamped at two lines', longTitle && longTitle.lines === 2 && longTitle.clipped, JSON.stringify(longTitle));
    const heights320 = await page.$$eval('#items-active .item-card', (lis) => lis.map((li) => Math.round(li.getBoundingClientRect().height)));
    check('320px: a two-line title doesn\'t make its row taller', new Set(heights320).size === 1, heights320.join(','));
    for (const r of rows) {
      check(`320px "${r.title.slice(0, 20)}": badge still centred with the checkbox`, Math.abs(r.badge.cy - r.checkbox.cy) <= 2);
    }
    await shot(page, '4-mobile-320');

    // 4. A wide window is "web": hidden while web is off, even with mobile on.
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.waitForTimeout(300);
    check('desktop, web off: no badge', (await page.locator('.nutrition-badges').count()) === 0);
    await setDisplay(page, { mobile: false, web: true });
    rows = await badgeRows(page);
    check('desktop, web on: badges', rows.length === 5);
    for (const r of rows) {
      check(`desktop "${r.title}": badge before the title, centred`, r.badge.right <= r.titleBox.x + 0.5 && Math.abs(r.badge.cy - r.checkbox.cy) <= 2);
    }
    await shot(page, '5-desktop-web-on');

    // …and back on a phone, mobile now off: hidden again.
    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(300);
    check('phone, mobile off (web on): no badge', (await page.locator('.nutrition-badges').count()) === 0);

    // 5. Both off: nothing anywhere, and the edit fields hidden.
    await setDisplay(page, { mobile: false, web: false });
    const water = (await api(page, 'GET', `/api/v1/items?list_id=${list.id}&title=${encodeURIComponent('Eau minérale')}`)).body[0];
    await page.evaluate((id) => openEditItemModal(state.currentList.items.find((it) => it.id === id)), water.id);
    check('both off: no score fields in the edit modal', await page.isHidden('#edit-item-nutrition'));
    await page.click('#close-edit-item-modal-button').catch(() => page.keyboard.press('Escape'));
    await page.waitForTimeout(200);

    // 6. A tap on the row opens the item actions sheet, which shows the
    // scores; "Modifier l'article" opens the edit modal, its nutrition panel
    // collapsed on the badges.
    await setDisplay(page, { mobile: true, web: false });
    const nutella = (await api(page, 'GET', `/api/v1/items?list_id=${list.id}&title=Nutella`)).body[0];
    const sheetLabels = () => page.$$eval('#item-actions-nutrition-value [role=img]', (els) => els.map((el) => el.getAttribute('aria-label')).join('|'));
    await page.locator(`li[data-item-id="${nutella.id}"]`).getByText('Nutella', { exact: true }).tap();
    await page.locator('#item-actions-sheet').waitFor({ state: 'visible' });
    check('a tap on the row shows the scores in the actions sheet', await page.isVisible('#item-actions-nutrition-button')
      && (await sheetLabels()) === 'Nutri-Score E|NOVA 4 : aliment ultra-transformé', await sheetLabels());
    await shot(page, '6-actions-sheet');
    await page.tap('#item-actions-edit-button');
    await page.locator('#edit-item-modal').waitFor({ state: 'visible' });
    check('the nutrition panel is shown, collapsed', await page.isVisible('#edit-item-nutrition')
      && !(await page.$eval('#edit-item-nutrition', (d) => d.open)));
    const summaryLabels = await page.$$eval('#edit-item-nutrition-summary [role=img]', (els) => els.map((el) => el.getAttribute('aria-label')));
    check('…its summary carries the item\'s badges', summaryLabels.join('|') === 'Nutri-Score E|NOVA 4 : aliment ultra-transformé', summaryLabels.join('|'));
    check('…the details stay folded away', await page.isHidden('#edit-item-nutri-score-description'));
    await shot(page, '6-edit-modal-collapsed');
    await page.tap('#edit-item-nutrition > summary');
    await page.waitForTimeout(350);
    check('a tap on the summary opens it', await page.$eval('#edit-item-nutrition', (d) => d.open));
    const panelInView = await page.$eval('#edit-item-nutrition', (d) => {
      const box = d.getBoundingClientRect();
      const form = document.getElementById('edit-item-form').getBoundingClientRect();
      return box.top >= form.top - 1 && Math.min(box.bottom, form.bottom) - box.top > 150;
    });
    check('…and scrolls it into view inside the sheet', panelInView);
    check('…the chosen grade is checked', await page.isChecked('input[name="edit-item-nutri-score"][value="E"]'));
    check('…with what it means', (await page.textContent('#edit-item-nutri-score-description')).startsWith('E :'));
    check('…the NOVA group is checked', await page.isChecked('input[name="edit-item-nova-group"][value="4"]'));
    check('…with what it means', (await page.textContent('#edit-item-nova-group-description')).startsWith('NOVA 4 : aliment ultra-transformé'));
    check('…no Yuka field left', (await page.locator('#edit-item-nutrition').getByText('Yuka').count()) === 0);
    check('…no lookup offered when both are known', await page.isHidden('#edit-item-nutrition-lookup'));
    const tiles = await page.$$eval('.nutri-score-option__tile', (els) => els.map((el) => Math.round(el.getBoundingClientRect().height)));
    check('…eleven tap targets at least 44px high (A-E, 1-4, two "Aucun")', tiles.length === 11 && tiles.every((h) => h >= 44), tiles.join(','));
    const links = await page.$$eval('#edit-item-nutrition a.nutrition-help-link', (as) => as.map((a) => ({
      href: a.href, target: a.target, rel: a.rel, visible: a.getBoundingClientRect().height > 0, name: a.textContent.trim(),
    })));
    const hrefs = links.map((l) => l.href);
    check('…help links to the official pages', hrefs.includes('https://www.santepubliquefrance.fr/nutrition-et-activite-physique/nutri-score')
      && hrefs.includes('https://fr.openfoodfacts.org/nutriscore') && hrefs.includes('https://fr.openfoodfacts.org/nova'), JSON.stringify(hrefs));
    check('…opening in a new tab, without opener or referrer', links.length === 3 && links.every((l) => l.visible && l.target === '_blank' && l.rel === 'noopener noreferrer'), JSON.stringify(links));
    check('…saying where they lead and that they open a tab, to screen readers', links.every((l) => l.name.includes('nouvel onglet'))
      && links.filter((l) => l.name.startsWith('Nutri-Score')).length === 2 && links.filter((l) => l.name.startsWith('Groupes NOVA')).length === 1,
      JSON.stringify(links.map((l) => l.name)));
    const helpLines = await page.$$eval('#edit-item-nutrition a.nutrition-help-link', (as) => as.map((a) => Math.round(a.getBoundingClientRect().height)));
    check('…each on one line at 390px', helpLines.every((h) => h <= 32), helpLines.join(','));
    const [popup] = await Promise.all([
      page.context().waitForEvent('page'),
      page.locator('#edit-item-nova-help-off').click({ modifiers: [] }),
    ]);
    check('…a tap opens the NOVA page in a new tab', popup.url().startsWith('https://fr.openfoodfacts.org/nova') || popup.url() === 'about:blank', popup.url());
    await popup.close();
    check('…and leaves the modal open', await page.isVisible('#edit-item-modal'));
    await page.check('input[name="edit-item-nova-group"][value="2"]');
    check('picking a NOVA group updates the summary live', (await page.$$eval('#edit-item-nutrition-summary [role=img]', (els) => els.map((el) => el.getAttribute('aria-label')))).includes('NOVA 2 : ingrédient culinaire transformé'));
    await shot(page, '7-edit-modal-open-light');
    await setTheme(page, 'dark');
    await shot(page, '8-edit-modal-open-dark');
    await setTheme(page, 'light');
    await page.click('#edit-item-form [type=submit]');
    await page.waitForTimeout(500);
    const nutellaSaved = (await api(page, 'GET', `/api/v1/items/${nutella.id}`)).body;
    check('saving keeps the legacy Yuka score it never showed', nutellaSaved.nova_group === 2 && nutellaSaved.nutri_score === 'E' && nutellaSaved.yuka_score === 12,
      JSON.stringify([nutellaSaved.nutri_score, nutellaSaved.nova_group, nutellaSaved.yuka_score]));

    // 7. An item without scores: an empty state with the lookup — here
    // PRODUCT_LOOKUP_ENABLED=false, so the server says so and the form
    // falls back on picking the letter.
    // Through the actions sheet's row: straight onto the panel, unfolded.
    await page.locator(`li[data-item-id="${water.id}"]`).getByText('Eau minérale', { exact: true }).tap();
    await page.locator('#item-actions-sheet').waitFor({ state: 'visible' });
    check('no scores: the actions sheet says so', (await page.textContent('#item-actions-nutrition-value')).trim() === 'Aucun score');
    await page.tap('#item-actions-nutrition-button');
    await page.locator('#edit-item-modal').waitFor({ state: 'visible' });
    await page.waitForTimeout(400);
    check('…its row opens the edit modal on the panel, unfolded', await page.$eval('#edit-item-nutrition', (d) => d.open));
    check('…focused on the panel, not the title (no keyboard)', await page.evaluate(() => document.activeElement === document.querySelector('#edit-item-nutrition > summary')));
    check('…the panel in view', await page.$eval('#edit-item-nutrition', (d) => {
      const box = d.getBoundingClientRect();
      return box.top >= 0 && box.top < window.innerHeight - 120;
    }));
    check('no scores: the summary says so', (await page.textContent('#edit-item-nutrition-summary')).trim() === 'Aucun score');
    check('…"none" is checked', await page.isChecked('input[name="edit-item-nutri-score"][value=""]'));
    check('…the lookup button is offered', await page.isVisible('#edit-item-nutrition-lookup-button'));
    check('…the NOVA group "none" too, explained', await page.isChecked('input[name="edit-item-nova-group"][value=""]')
      && (await page.textContent('#edit-item-nova-group-description')).includes('degré de transformation'));
    await page.setViewportSize({ width: 320, height: 640 });
    await page.waitForTimeout(200);
    const panelOverflow = await page.$eval('#edit-item-nutrition', (d) => d.scrollWidth - d.clientWidth);
    check('320px: the open panel doesn\'t overflow', panelOverflow <= 0, `${panelOverflow}px`);
    await shot(page, '9-edit-modal-empty-320');
    await page.tap('#edit-item-nutrition-lookup-button');
    await page.waitForFunction(() => !document.getElementById('edit-item-nutrition-lookup-button').disabled);
    check('lookup with lookups disabled: said so', (await page.textContent('#edit-item-nutrition-lookup-status')).includes('désactivée'));
    check('…and the button goes', await page.isHidden('#edit-item-nutrition-lookup-button'));
    await shot(page, '10-edit-modal-lookup-disabled');
    await page.setViewportSize({ width: 390, height: 844 });

    // 8. Set, then clear, a product's scores through the panel.
    await page.check('input[name="edit-item-nutri-score"][value="A"]');
    // Lookups are off on this server (said once): the block goes with the message.
    check('picking a grade replaces the lookup\'s message', await page.isHidden('#edit-item-nutrition-lookup'));
    await page.check('input[name="edit-item-nova-group"][value="1"]');
    check('…both known: no lookup left', await page.isHidden('#edit-item-nutrition-lookup'));
    await page.click('#edit-item-form [type=submit]');
    await page.waitForTimeout(500);
    let saved = (await api(page, 'GET', `/api/v1/items/${water.id}`)).body;
    check('edit saves both scores', saved.nutri_score === 'A' && saved.nova_group === 1, JSON.stringify([saved.nutri_score, saved.nova_group]));
    check('…and the row shows them at once', (await page.locator(`li[data-item-id="${water.id}"] .nutri-score-badge--a`).count()) === 1
      && (await page.locator(`li[data-item-id="${water.id}"] .nova-group-badge--1`).count()) === 1);

    await page.evaluate((id) => openEditItemModal(state.currentList.items.find((it) => it.id === id)), water.id);
    await page.tap('#edit-item-nutrition > summary');
    await page.check('input[name="edit-item-nutri-score"][value=""]');
    await page.check('input[name="edit-item-nova-group"][value=""]');
    await page.click('#edit-item-form [type=submit]');
    await page.waitForTimeout(500);
    saved = (await api(page, 'GET', `/api/v1/items/${water.id}`)).body;
    check('emptying the fields clears the scores', saved.nutri_score === undefined && saved.nova_group === undefined, JSON.stringify(saved));
    check('…and the badge goes', (await page.locator(`li[data-item-id="${water.id}"] .nutrition-badges`).count()) === 0);

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
