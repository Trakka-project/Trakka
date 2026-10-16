// End-to-end check of task assignments in a real browser, self-hosted: a
// House of three (Camille, Thomas, Sarah) and a list-share recipient. The
// avatars on the
// rows, the "Par membre" grouping (a task shared by two shows under both),
// the "Membres" filter and its "Moi" chip (a task added while it's on is
// assigned to me), the picker sheet from the actions sheet and from the
// avatars, the edit modal's field, the multi-select "Assigner" action, a
// recurring assigned task checked off (it keeps its assignees), the share
// recipient joining the people who can be assigned — and assigning — once
// the list is shared with them, an assignment made offline through the
// service worker (named from the roster mirror, synced once back online),
// and that nothing overflows a 320px-wide phone, in light and dark. Then a
// second server in SAAS_MODE, where the share recipient only gets a
// read-only view.
//
// Usage (see README.md): cd scripts && npm run test:assignees
// Builds ./cmd/server and runs it on a throwaway database, like the other
// scripts here.

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
const THOMAS = { name: 'Thomas', email: 'thomas@example.com' };
const SARAH = { name: 'Sarah', email: 'sarah@example.com' };
const GUEST = { name: 'Invité', email: 'guest@example.com' };

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

async function startServer(workDir, { dbName = 'assignees.db', env = {} } = {}) {
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
      DB_PATH: path.join(workDir, dbName),
      STATIC_DIR: path.join(REPO_ROOT, 'static'),
      TEMPLATES_DIR: path.join(REPO_ROOT, 'templates'),
      SESSION_COOKIE_SECURE: 'false',
      DEFAULT_APP_LANGUAGE: 'fr',
      APP_TIMEZONE: 'Europe/Paris',
      REGISTRATION_OPEN: 'true',
      PRODUCT_LOOKUP_ENABLED: 'false',
      LOG_LEVEL: 'warn',
      ...env,
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

async function itemsOf(page, listId) {
  return (await api(page, 'GET', `/api/v1/items?list_id=${listId}`)).body;
}

function assignedNames(item) {
  return (item.assignees || []).map((a) => a.display_name).sort().join(',');
}

// SHOTS_DIR=/some/dir saves a screenshot at each step worth looking at.
async function shot(page, name) {
  if (process.env.SHOTS_DIR) await page.screenshot({ path: path.join(process.env.SHOTS_DIR, `${name}.png`) });
}

// A task's row: li.swipe-item on a touch device — except in multi-select
// mode, where buildItemRow skips the swipe wrapper and the <li> is the
// .item-card itself. Never a group heading.
const ROW_SELECTOR = ':is(#items-active, #items-done) > li:is(.swipe-item, .item-card)';
const row = (page, title) => page.locator(ROW_SELECTOR, { hasText: title }).first();

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
  return context;
}

async function register(context, baseURL, user, errors) {
  const page = await context.newPage();
  page.on('pageerror', (err) => errors.push(`[${user.name}] ${err.message}`));
  page.on('console', (msg) => {
    if (msg.type() === 'error' && !/service worker|Failed to load resource/i.test(msg.text())) errors.push(`[${user.name}] ${msg.text()}`);
  });
  page.on('dialog', (dialog) => dialog.accept());
  await page.goto(`${baseURL}/auth/login?mode=register`);
  await page.fill('#display_name', user.name);
  await page.fill('#email', user.email);
  await page.fill('#password', PASSWORD);
  await page.fill('#password_confirm', PASSWORD);
  await Promise.all([page.waitForURL(`${baseURL}/`), page.click('form:has(#password_confirm) [type=submit]')]);
  await page.waitForTimeout(400);
  user.id = (await api(page, 'GET', '/api/v1/me')).body.id;
  return page;
}

async function openList(page, name) {
  if (await page.isVisible('#items-section')) {
    await page.click('#back-button');
    await page.waitForTimeout(300);
  }
  await page.locator('#dashboard-view').getByText(name, { exact: true }).first().click();
  await page.locator('#items-section').waitFor({ state: 'visible' });
  await page.waitForTimeout(600);
}

async function sortBy(page, mode) {
  await page.click('#sort-items-button');
  await page.click(`#sort-option-${mode}`);
  await page.waitForTimeout(300);
}

function measureOverflow(page) {
  return page.evaluate(() => {
    const vw = document.documentElement.clientWidth;
    const out = [];
    for (const el of document.querySelectorAll('body *')) {
      const r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0 || r.right <= vw + 0.5) continue;
      let clipped = false;
      for (let p = el.parentElement; p; p = p.parentElement) {
        const style = getComputedStyle(p);
        if (/(auto|scroll|hidden|clip)/.test(style.overflowX) && p.getBoundingClientRect().right <= vw + 0.5) { clipped = true; break; }
      }
      if (!clipped) out.push(`${el.tagName}#${el.id}.${String(el.className).slice(0, 40)} → ${Math.round(r.right)}`);
    }
    return { inner: window.innerWidth, client: vw, out: out.slice(0, 5) };
  });
}

// WCAG contrast ratio of an avatar's text on its background.
function avatarContrast(page) {
  return page.evaluate(() => {
    const parse = (c) => (c.match(/[\d.]+/g) || []).slice(0, 3).map(Number);
    const lum = ([r, g, b]) => {
      const f = (v) => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4; };
      return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
    };
    return [...document.querySelectorAll('#items-active .item-assignees > span')].map((el) => {
      const s = getComputedStyle(el);
      const a = lum(parse(s.color));
      const b = lum(parse(s.backgroundColor));
      return Math.round(((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)) * 100) / 100;
    });
  });
}

// 11. SAAS_MODE: only the House's members can be assigned, and only they
//     can assign — a list-share recipient sees who does what, read only.
async function saasModeChecks(browser, workDir, errors) {
  const saas = await startServer(workDir, { dbName: 'assignees-saas.db', env: { SAAS_MODE: 'true' } });
  try {
    const ownerContext = await newContext(browser);
    const owner = await register(ownerContext, saas.baseURL, OWNER, errors);
    const guestContext = await newContext(browser);
    const guest = await register(guestContext, saas.baseURL, GUEST, errors);
    const [house] = (await api(owner, 'GET', '/api/v1/houses')).body;
    const list = (await api(owner, 'POST', '/api/v1/lists', { name: 'Ménage', type: 'todo', house_id: house.id })).body;
    await api(owner, 'POST', '/api/v1/items', { list_id: list.id, title: 'Sortir les poubelles', assignee_ids: [OWNER.id] });
    const shared = await api(owner, 'POST', `/api/v1/lists/${list.id}/share`, { email: GUEST.email, permission: 'write' });
    check('SaaS: shared the list with the guest', shared.status === 201 || shared.status === 202, String(shared.status));
    await api(guest, 'GET', '/api/v1/me'); // materializes the share
    const refused = await api(owner, 'POST', '/api/v1/items', { list_id: list.id, title: 'Refusé', assignee_ids: [GUEST.id] });
    check('SaaS: the API refuses a share recipient as assignee', refused.status === 400, String(refused.status));
    const assignable = (await api(guest, 'GET', `/api/v1/lists/${list.id}/assignable-users`)).body;
    check('SaaS: the guest may not assign, nor see the House roster', assignable && assignable.can_assign === false && assignable.users.length === 0, JSON.stringify(assignable));

    await guest.goto(`${saas.baseURL}/?list=${list.id}`);
    await guest.locator('#items-section').waitFor({ state: 'visible' });
    await guest.waitForTimeout(800);
    const guestRow = row(guest, 'Sortir les poubelles');
    const guestAvatar = guestRow.locator('.item-assignees');
    check('SaaS: the guest sees the avatars, not as a button', (await guestAvatar.count()) === 1 &&
      (await guestAvatar.evaluate((el) => el.tagName)) === 'SPAN', await guestAvatar.evaluate((el) => el.tagName).catch(() => 'none'));
    await guestRow.locator('.item-card__kebab').click();
    check('…no "Assigner" row in their actions sheet', await guest.isHidden('#item-actions-assignees-button'));
    await guest.click('#item-actions-edit-button');
    const note = await guest.textContent('#edit-item-assignees-note');
    check('…a read-only line in their edit modal', await guest.isVisible('#edit-item-assignees-note') && /Camille/.test(note) && /ne pouvez pas modifier/.test(note), note);
    await guest.fill('#edit-item-title', 'Sortir les poubelles (jaune)');
    await guest.click('#edit-item-form [type="submit"]');
    await guest.waitForTimeout(700);
    const bins = (await itemsOf(owner, list.id)).find((i) => i.title === 'Sortir les poubelles (jaune)');
    check('…and their edits leave the assignees alone', bins && assignedNames(bins) === 'Camille', JSON.stringify(bins && bins.assignees));
    await ownerContext.close();
    await guestContext.close();
  } finally {
    await saas.stop();
  }
}

async function main() {
  const workDir = await mkdtemp(path.join(tmpdir(), 'trakka-assignees-'));
  const server = await startServer(workDir);
  const browser = await chromium.launch();
  const errors = [];
  try {
    // 0. Four accounts; Thomas and Sarah join Camille's House, the guest
    //    gets the list shared with them (write) without joining.
    const ownerContext = await newContext(browser);
    const page = await register(ownerContext, server.baseURL, OWNER, errors);
    const others = {};
    for (const user of [THOMAS, SARAH, GUEST]) {
      const context = await newContext(browser);
      others[user.name] = { context, page: await register(context, server.baseURL, user, errors) };
    }
    const [house] = (await api(page, 'GET', '/api/v1/houses')).body;
    for (const user of [THOMAS, SARAH]) {
      const invited = await api(page, 'POST', `/api/v1/houses/${house.id}/members`, { email: user.email });
      check(`invited ${user.name}`, invited.status === 201 || invited.status === 202, String(invited.status));
      await api(others[user.name].page, 'GET', '/api/v1/me'); // materializes the invitation
    }
    const roster = (await api(page, 'GET', `/api/v1/houses/${house.id}/members`)).body;
    check('the House has three members', roster.filter((m) => !m.pending).length === 3, JSON.stringify(roster.map((m) => [m.display_name, m.pending])));

    const list = (await api(page, 'POST', '/api/v1/lists', { name: 'Ménage', type: 'todo', house_id: house.id })).body;
    const today = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Paris' }).format(new Date());
    const create = (body) => api(page, 'POST', '/api/v1/items', { list_id: list.id, ...body });
    await create({ title: 'Sortir les poubelles', assignee_ids: [THOMAS.id] });
    await create({ title: "Passer l'aspirateur", assignee_ids: [THOMAS.id, SARAH.id] });
    const watering = (await create({ title: 'Arroser les plantes', due_date: today, recurrence_rule: 'FREQ=DAILY', assignee_ids: [OWNER.id] })).body;
    await create({ title: 'Faire les vitres' });
    const refused = await create({ title: 'Refusé', assignee_ids: [GUEST.id] });
    check('the API refuses an assignee who can\'t open the list', refused.status === 400, String(refused.status));

    // 1. The rows show their assignees.
    await page.reload();
    await page.waitForTimeout(500);
    await openList(page, 'Ménage');
    const avatarsOf = (title) => row(page, title).locator('.item-assignees > span').allTextContents();
    check('one avatar for a task assigned to Thomas', JSON.stringify(await avatarsOf('Sortir les poubelles')) === '["T"]');
    check('two avatars for a task shared by Thomas and Sarah', JSON.stringify(await avatarsOf("Passer l'aspirateur")) === '["T","S"]');
    check('no avatar on an unassigned task', (await row(page, 'Faire les vitres').locator('.item-assignees').count()) === 0);
    const ariaLabel = await row(page, "Passer l'aspirateur").locator('.item-assignees').getAttribute('aria-label');
    check('the avatars name the assignees for screen readers', /Thomas, Sarah/.test(ariaLabel || ''), ariaLabel);
    const contrasts = await avatarContrast(page);
    check('avatar initials are legible (≥ 4.5:1) in light mode', contrasts.length > 0 && contrasts.every((c) => c >= 4.5), contrasts.join(', '));
    await shot(page, 'rows');

    // 2. "Par membre".
    await sortBy(page, 'assignee');
    const headers = (await page.locator('#items-active li.assignee-header').allTextContents()).map((h) => h.replace(/\s+/g, ' ').trim());
    check('"Par membre" groups: mine, Sarah, Thomas, unassigned',
      JSON.stringify(headers) === JSON.stringify(['CMes tâches1', 'STâches de Sarah1', 'TTâches de Thomas2', '?Non assignées1']), JSON.stringify(headers));
    const vacuumRows = await page.locator(ROW_SELECTOR, { hasText: "Passer l'aspirateur" }).count();
    check('a task shared by two shows under both', vacuumRows === 2, String(vacuumRows));
    await shot(page, 'by-member');

    // 3. The "Membres" filter: "Moi" shows my own to-do, and a task added
    //    meanwhile is mine.
    await page.click('#filter-items-button');
    // The names alone, without the avatars' initials next to them.
    const chips = await page.locator('#filter-assignees-chips .assignee-chip-name').allTextContents();
    check('the filter offers me, each member and "Non assignées"',
      JSON.stringify(chips) === JSON.stringify(['Moi', 'Sarah', 'Thomas', 'Non assignées']), JSON.stringify(chips));
    await page.locator('#filter-assignees-chips button', { hasText: 'Moi' }).click();
    await page.click('#close-filter-items-sheet-button');
    await page.waitForTimeout(300);
    let titles = await page.locator(`${ROW_SELECTOR} span.cursor-pointer`).allTextContents();
    check('"Moi" shows only my tasks', JSON.stringify(titles) === JSON.stringify(['Arroser les plantes']), JSON.stringify(titles));
    check('the quick-add bar says the next task is mine', await page.isVisible('#quick-add-assignees-chip') &&
      (await page.textContent('#quick-add-assignees-chip-label')).includes('Pour Moi'), await page.textContent('#quick-add-assignees-chip-label'));
    await page.fill('#item-title', 'Nettoyer le four');
    await page.press('#item-title', 'Enter');
    await page.waitForTimeout(700);
    let items = await itemsOf(page, list.id);
    const oven = items.find((i) => i.title === 'Nettoyer le four');
    check('a task added under "Moi" is assigned to me', oven && assignedNames(oven) === 'Camille', JSON.stringify(oven && oven.assignees));
    titles = await page.locator(`${ROW_SELECTOR} span.cursor-pointer`).allTextContents();
    check('…and stays in view', titles.includes('Nettoyer le four'), JSON.stringify(titles));
    await page.click('#quick-add-assignees-chip-clear');
    check('the chip can be cleared', await page.isHidden('#quick-add-assignees-chip'));
    await page.click('#filter-items-button');
    await page.click('#filter-reset-button');
    await page.click('#close-filter-items-sheet-button');
    await sortBy(page, 'manual');

    // 4. The picker, from the actions sheet: each tap commits.
    await row(page, 'Faire les vitres').locator('.item-card__kebab').click();
    check('the actions sheet has an "Assigner" row saying "Personne"', await page.isVisible('#item-actions-assignees-button') &&
      (await page.textContent('#item-actions-assignees-value')).trim() === 'Personne');
    await page.click('#item-actions-assignees-button');
    await page.locator('#assignee-picker-sheet').waitFor({ state: 'visible' });
    const options = await page.locator('#assignee-picker-options .assignee-option-name').allTextContents();
    check('the picker lists me first, then the others by name', JSON.stringify(options) === JSON.stringify(['Camille (moi)', 'Sarah', 'Thomas']), JSON.stringify(options));
    await shot(page, 'picker');
    await page.locator('#assignee-picker-options [role="checkbox"]', { hasText: 'Sarah' }).click();
    await page.waitForTimeout(500);
    await page.locator('#assignee-picker-options [role="checkbox"]', { hasText: 'Thomas' }).click();
    await page.waitForTimeout(500);
    await page.locator('#assignee-picker-options [role="checkbox"]', { hasText: 'Sarah' }).click();
    await page.waitForTimeout(700);
    const checkedStates = await page.locator('#assignee-picker-options [role="checkbox"]').evaluateAll((els) => els.map((el) => el.getAttribute('aria-checked')));
    items = await itemsOf(page, list.id);
    check('three taps (Sarah on, Thomas on, Sarah off) leave Thomas', assignedNames(items.find((i) => i.title === 'Faire les vitres')) === 'Thomas' &&
      JSON.stringify(checkedStates) === '["false","false","true"]', JSON.stringify(checkedStates));
    await page.click('#assignee-picker-done-button');
    check('the picker closes', await page.isHidden('#assignee-picker-sheet'));

    // 5. The edit modal.
    await row(page, 'Sortir les poubelles').locator('span.cursor-pointer').click();
    await page.click('#item-actions-edit-button');
    await page.locator('#edit-item-modal').waitFor({ state: 'visible' });
    const pressed = await page.locator('#edit-item-assignees button[aria-pressed="true"]').allTextContents();
    check('the edit modal shows who it is assigned to', pressed.length === 1 && pressed[0].includes('Thomas'), JSON.stringify(pressed));
    await page.locator('#edit-item-assignees button', { hasText: 'Sarah' }).click();
    await shot(page, 'edit-modal');
    await page.click('#edit-item-form [type="submit"]');
    await page.waitForTimeout(700);
    items = await itemsOf(page, list.id);
    check('saving the modal assigns Sarah too', assignedNames(items.find((i) => i.title === 'Sortir les poubelles')) === 'Sarah,Thomas');

    // 6. The avatars open the picker; multi-select assigns several at once.
    await row(page, 'Sortir les poubelles').locator('.item-assignees').click();
    check('tapping the avatars opens the picker', await page.isVisible('#assignee-picker-sheet'));
    await page.click('#close-assignee-picker-sheet-button');
    await page.click('#list-options-button');
    await page.click('#select-items-button');
    await row(page, 'Faire les vitres').click();
    await row(page, 'Nettoyer le four').click();
    check('the bulk bar offers "Assigner"', await page.isVisible('#bulk-assignees-button') && await page.isEnabled('#bulk-assignees-button'));
    await page.click('#bulk-assignees-button');
    const bulkStates = await page.locator('#assignee-picker-options [role="checkbox"]').evaluateAll((els) => els.map((el) => el.getAttribute('aria-checked')));
    check('the picker shows a member assigned to some of the selection as mixed', JSON.stringify(bulkStates) === '["mixed","false","mixed"]', JSON.stringify(bulkStates));
    await page.locator('#assignee-picker-options [role="checkbox"]', { hasText: 'Sarah' }).click();
    await page.waitForTimeout(800);
    items = await itemsOf(page, list.id);
    check('one tap assigns Sarah to both', assignedNames(items.find((i) => i.title === 'Faire les vitres')) === 'Sarah,Thomas' &&
      assignedNames(items.find((i) => i.title === 'Nettoyer le four')) === 'Camille,Sarah');
    await page.click('#assignee-picker-done-button');
    await page.click('#bulk-cancel-button');
    // Grouped "Par membre", a task shared by two shows twice: selecting it
    // from one row ticks both.
    await sortBy(page, 'assignee');
    await page.click('#list-options-button');
    await page.click('#select-items-button');
    await row(page, "Passer l'aspirateur").click();
    const vacuumSelected = await page.locator(ROW_SELECTOR, { hasText: "Passer l'aspirateur" }).evaluateAll((els) => els.map((el) => el.classList.contains('is-selected')));
    check('in "Par membre", selecting a shared task ticks both of its rows', JSON.stringify(vacuumSelected) === '[true,true]', JSON.stringify(vacuumSelected));
    await page.click('#bulk-cancel-button');
    await sortBy(page, 'manual');

    // 7. A recurring assigned task, checked off, keeps its assignee. The
    //    PATCH only goes out once the 5 s undo window closes (toggleDone →
    //    TrakkaUndo.schedule's onCommit): the row moves at once, the server
    //    follows.
    await row(page, 'Arroser les plantes').locator('input[type="checkbox"]').click();
    await page.waitForTimeout(300);
    const doneAvatars = await page.locator('#items-done > li', { hasText: 'Arroser les plantes' }).first().locator('.item-assignees > span').allTextContents();
    check('a checked-off assigned task moves to "Terminés" with its avatar', JSON.stringify(doneAvatars) === '["C"]', JSON.stringify(doneAvatars));
    let checkedOff = null;
    for (const deadline = Date.now() + 10_000; Date.now() < deadline; await page.waitForTimeout(250)) {
      checkedOff = (await itemsOf(page, list.id)).find((i) => i.id === watering.id);
      if (checkedOff.done) break;
    }
    check('…once committed, it is scheduled again on the server, still assigned', checkedOff.done && checkedOff.next_due_date > today && assignedNames(checkedOff) === 'Camille',
      JSON.stringify([checkedOff.done, checkedOff.next_due_date, checkedOff.assignees]));

    // 8. 320px and dark mode.
    await page.setViewportSize({ width: 320, height: 700 });
    await sortBy(page, 'assignee');
    const grouped = await measureOverflow(page);
    await sortBy(page, 'manual');
    const plain = await measureOverflow(page);
    await shot(page, 'narrow-320');
    check('nothing overflows at 320px', [grouped, plain].every((o) => o.inner === 320 && o.client === 320 && o.out.length === 0), JSON.stringify([grouped, plain]));
    // The overflow check can't see a title squeezed to a few letters per
    // line (what avatars in a column of their own did): an assigned task's
    // title must keep the room an unassigned one has.
    await api(page, 'POST', '/api/v1/items', { list_id: list.id, title: 'Tâche témoin' });
    await page.click('#back-button');
    await openList(page, 'Ménage');
    const titleWidths = await page.evaluate((selector) => Object.fromEntries([...document.querySelectorAll(`${selector} span.cursor-pointer`)]
      .map((el) => [el.textContent, Math.round(el.getBoundingClientRect().width)])), ROW_SELECTOR);
    const reference = titleWidths['Tâche témoin'];
    const assignedWidths = Object.entries(titleWidths).filter(([title]) => title !== 'Tâche témoin');
    check('at 320px, an assigned task\'s title is as wide as an unassigned one\'s', reference >= 80 && assignedWidths.length >= 4 &&
      assignedWidths.every(([, width]) => width === reference), JSON.stringify(titleWidths));
    await shot(page, 'narrow-320-titles');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
    await page.waitForTimeout(300);
    const darkContrasts = await avatarContrast(page);
    check('avatar initials are legible (≥ 4.5:1) in dark mode', darkContrasts.length > 0 && darkContrasts.every((c) => c >= 4.5), darkContrasts.join(', '));
    await shot(page, 'dark');
    await page.emulateMedia({ colorScheme: 'light' });

    // 9. Self-hosted, sharing the list makes the recipient assignable, and
    //    lets them assign: the owner's picker offers them once the list is
    //    opened again, and they can take a task themselves.
    const shared = await api(page, 'POST', `/api/v1/lists/${list.id}/share`, { email: GUEST.email, permission: 'write' });
    check('shared the list with the guest', shared.status === 201 || shared.status === 202, String(shared.status));
    const guest = others[GUEST.name].page;
    await api(guest, 'GET', '/api/v1/me'); // materializes the share
    await openList(page, 'Ménage');
    await row(page, 'Passer').locator('.item-card__kebab').click();
    await page.click('#item-actions-assignees-button');
    await page.locator('#assignee-picker-options [role="checkbox"]').first().waitFor();
    const ownerOptions = await page.locator('#assignee-picker-options .assignee-option-name').allTextContents();
    check('after sharing, the owner\'s picker offers the guest', ownerOptions.length === 4 && ownerOptions.includes('Invité'), JSON.stringify(ownerOptions));
    await page.click('#assignee-picker-done-button');

    await guest.goto(`${server.baseURL}/?list=${list.id}`);
    await guest.locator('#items-section').waitFor({ state: 'visible' });
    await guest.waitForTimeout(800);
    const guestRow = row(guest, 'Sortir les poubelles');
    const guestAvatar = guestRow.locator('.item-assignees');
    check('the guest sees the avatars, as a button', (await guestAvatar.count()) === 1 &&
      (await guestAvatar.evaluate((el) => el.tagName)) === 'BUTTON', await guestAvatar.evaluate((el) => el.tagName).catch(() => 'none'));
    await guestRow.locator('.item-card__kebab').click();
    check('…an "Assigner" row in their actions sheet', await guest.isVisible('#item-actions-assignees-button'));
    await guest.click('#item-actions-assignees-button');
    await guest.locator('#assignee-picker-options [role="checkbox"]').first().waitFor();
    const guestOptions = await guest.locator('#assignee-picker-options .assignee-option-name').allTextContents();
    check('…whose picker lists the House and themselves, first', guestOptions.length === 4 && guestOptions[0] === 'Invité (moi)', JSON.stringify(guestOptions));
    await guest.locator('#assignee-picker-options [role="checkbox"]', { hasText: 'Invité (moi)' }).click();
    await guest.waitForTimeout(700);
    await guest.click('#assignee-picker-done-button');
    items = await itemsOf(page, list.id);
    check('…and they can take a task', assignedNames(items.find((i) => i.title === 'Sortir les poubelles')) === 'Invité,Sarah,Thomas',
      JSON.stringify(items.find((i) => i.title === 'Sortir les poubelles').assignees));
    await guestRow.locator('.item-card__kebab').click();
    await guest.click('#item-actions-edit-button');
    await guest.fill('#edit-item-title', 'Sortir les poubelles (jaune)');
    await guest.click('#edit-item-form [type="submit"]');
    await guest.waitForTimeout(700);
    items = await itemsOf(page, list.id);
    const bins = items.find((i) => i.title === 'Sortir les poubelles (jaune)');
    check('…and an edit leaves the assignees alone', bins && assignedNames(bins) === 'Invité,Sarah,Thomas', JSON.stringify(bins && bins.assignees));
    await guest.click('#sort-items-button');
    await guest.click('#sort-option-assignee');
    await guest.waitForTimeout(300);
    const guestHeaders = (await guest.locator('#items-active li.assignee-header').allTextContents()).map((h) => h.replace(/\s+/g, ' ').trim());
    check('…and "Par membre" puts their own first', /Mes tâches/.test(guestHeaders[0] || '') && guestHeaders.some((h) => h.includes('Tâches de Thomas')), JSON.stringify(guestHeaders));

    // 10. Offline, with the service worker: the roster comes from its
    //     mirror, the assignment is shown and survives a reload, then syncs.
    const swContext = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, locale: 'fr-FR' });
    await swContext.addInitScript(() => {
      try { localStorage.setItem('trakka:lang', 'fr'); } catch { /* not needed */ }
    });
    const swPage = await swContext.newPage();
    swPage.on('pageerror', (err) => errors.push(`[sw] ${err.message}`));
    await swPage.goto(`${server.baseURL}/auth/login`);
    await swPage.fill('#email', OWNER.email);
    await swPage.fill('#password', PASSWORD);
    await Promise.all([swPage.waitForURL(`${server.baseURL}/`), swPage.click('form:has(#password):not(:has(#password_confirm)) [type=submit]')]);
    await swPage.evaluate(() => navigator.serviceWorker.ready);
    await swPage.reload(); // now controlled by the service worker
    await swPage.evaluate(() => navigator.serviceWorker.ready);
    await openList(swPage, 'Ménage');
    await swPage.waitForTimeout(500); // the roster is fetched (and mirrored) on opening

    await swContext.setOffline(true);
    await swPage.reload();
    await swPage.locator('#items-section').waitFor({ state: 'visible' }).catch(() => {});
    if (await swPage.isHidden('#items-section')) await openList(swPage, 'Ménage');
    await swPage.waitForTimeout(800);
    await swPage.locator(ROW_SELECTOR, { hasText: 'Passer' }).first().locator('.item-card__kebab').click();
    await swPage.click('#item-actions-assignees-button');
    await swPage.locator('#assignee-picker-options [role="checkbox"]').first().waitFor({ timeout: 3000 }).catch(() => {});
    const offlineOptions = await swPage.locator('#assignee-picker-options [role="checkbox"]').count();
    check('offline: the picker still lists everyone assignable (roster mirror)', offlineOptions === 4, String(offlineOptions));
    await swPage.locator('#assignee-picker-options [role="checkbox"]', { hasText: 'Camille' }).click();
    await swPage.waitForTimeout(600);
    await swPage.click('#assignee-picker-done-button');
    await swPage.reload();
    await swPage.locator('#items-section').waitFor({ state: 'visible' }).catch(() => {});
    if (await swPage.isHidden('#items-section')) await openList(swPage, 'Ménage');
    await swPage.waitForTimeout(800);
    const offlineAvatars = await swPage.locator(ROW_SELECTOR, { hasText: 'Passer' }).first().locator('.item-assignees > span').allTextContents();
    check('offline: the assignment is kept and named after a reload', JSON.stringify(offlineAvatars) === '["T","S","C"]', JSON.stringify(offlineAvatars));
    const offlineLabel = await swPage.locator(ROW_SELECTOR, { hasText: 'Passer' }).first().locator('.item-assignees').getAttribute('aria-label');
    check('…with real names', /Camille/.test(offlineLabel || ''), offlineLabel);

    await swContext.setOffline(false);
    await swPage.evaluate(() => fetch('/api/v1/me'));
    await swPage.waitForTimeout(2500);
    items = await itemsOf(swPage, list.id);
    check('back online: the offline assignment is synced', assignedNames(items.find((i) => i.title === "Passer l'aspirateur")) === 'Camille,Sarah,Thomas',
      JSON.stringify(items.find((i) => i.title === "Passer l'aspirateur").assignees));
    await swContext.close();

    await saasModeChecks(browser, workDir, errors);

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
