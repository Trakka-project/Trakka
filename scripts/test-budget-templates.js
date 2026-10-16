// End-to-end check of the purchase cycle with budget tracking and list
// templates in a real browser: creating a list from a built-in template
// (its items planned, with their aisles and estimated prices), the budget
// summary and its variance, confirming planned items, the "Prix payé" offer
// when checking an item off, the purchase status picker, the edit modal's
// purchase fields, saving a list as a House template and starting a list
// from it, deleting it, the task templates the picker shows for a "Tâches"
// list (and only then), the template cards' carousel staying inside the
// modal at 390, 360 and 320px, the offline refusal, and that nothing
// overflows a 320px-wide phone.
//
// Usage (see README.md): cd scripts && npm run test:budget-templates
// Builds ./cmd/server and runs it on a throwaway database, like the other
// scripts here.

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
      DB_PATH: path.join(workDir, 'budget.db'),
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

async function itemsOf(page, listId) {
  return (await api(page, 'GET', `/api/v1/items?list_id=${listId}`)).body;
}

// SHOTS_DIR=/some/dir saves a screenshot at each step worth looking at.
async function shot(page, name) {
  if (process.env.SHOTS_DIR) await page.screenshot({ path: path.join(process.env.SHOTS_DIR, `${name}.png`) });
}

const row = (page, title) => page.locator('#items-section li.swipe-item', { hasText: title }).first();

const typeOption = (type) => `#create-list-form label[data-type-option]:has(input[value="${type}"])`;

async function templateCardIds(page) {
  return page.locator('#list-template-options [role="radio"]').evaluateAll((cards) => cards.map((card) => card.dataset.templatePickerId));
}

// Where the template carousel sits in the "Nouvelle liste" dialog, at the
// start of its scroll and at the end: it must line up with the fields
// (inside the dialog's padding), and its last card must stop short of its
// right edge.
function carouselLayout(page) {
  return page.evaluate(async () => {
    const settle = () => new Promise((r) => setTimeout(r, 400));
    const dialog = document.querySelector('#new-list-modal [role="dialog"]');
    const scroller = document.getElementById('list-template-scroller');
    const cards = [...document.querySelectorAll('#list-template-options [role="radio"]')];
    const field = document.getElementById('list-name').getBoundingClientRect();
    const d = dialog.getBoundingClientRect();
    const box = scroller.getBoundingClientRect();
    scroller.scrollLeft = 0;
    await settle();
    const first = cards[0].getBoundingClientRect();
    scroller.scrollLeft = scroller.scrollWidth;
    await settle();
    const last = cards[cards.length - 1].getBoundingClientRect();
    const r = (v) => Math.round(v * 10) / 10;
    const out = {
      viewport: document.documentElement.clientWidth,
      pageScrollWidth: document.documentElement.scrollWidth,
      dialogScrollWidth: dialog.scrollWidth - dialog.clientWidth,
      padding: r(parseFloat(getComputedStyle(dialog).paddingLeft)),
      scrollerInset: [r(box.left - d.left), r(d.right - box.right)],
      fieldEdges: [r(box.left - field.left), r(box.right - field.right)],
      firstCardOffset: r(first.left - box.left),
      lastCardGap: r(box.right - last.right),
      atEnd: Math.abs(scroller.scrollLeft - (scroller.scrollWidth - scroller.clientWidth)) < 1,
      cards: cards.length,
    };
    scroller.scrollLeft = 0;
    return out;
  });
}

// Everything visible under `selector` that ends past the viewport's right
// edge (see frontend-pwa.md's mobile layout rule 3).
function measureOverflow(page, selector) {
  return page.evaluate((sel) => {
    const vw = document.documentElement.clientWidth;
    const out = [];
    for (const el of document.querySelectorAll(sel)) {
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.right > vw + 0.5 && !el.closest('[hidden]') && !el.closest('#list-template-options')) {
        out.push(`${el.tagName}#${el.id}.${String(el.className).slice(0, 40)} → ${Math.round(r.right)}`);
      }
    }
    return { inner: window.innerWidth, client: vw, out: out.slice(0, 5) };
  }, selector);
}

async function main() {
  const workDir = await mkdtemp(path.join(tmpdir(), 'trakka-budget-'));
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
    page.on('dialog', (dialog) => dialog.accept());

    await page.goto(`${server.baseURL}/auth/login?mode=register`);
    await page.fill('#display_name', USER.name);
    await page.fill('#email', USER.email);
    await page.fill('#password', USER.password);
    await page.fill('#password_confirm', USER.password);
    await Promise.all([page.waitForURL(`${server.baseURL}/`), page.click('form:has(#password_confirm) [type=submit]')]);
    await page.waitForTimeout(500);
    const [house] = (await api(page, 'GET', '/api/v1/houses')).body;

    // 1. The template picker of "Nouvelle liste".
    await page.click('#new-list-button');
    await page.locator('#list-template-options [data-template-picker-id="builtin:back_to_school"]').waitFor({ timeout: 5000 });
    const cards = await page.locator('#list-template-options [role="radio"]').allTextContents();
    check('the picker offers an empty list and the 5 built-in templates', cards.length === 6 &&
      ['Liste vide', 'Ménage', 'Vacances', 'Déménagement', 'Rentrée scolaire', 'Bricolage'].every((word) => cards.some((c) => c.includes(word))), cards.join(' | '));
    await page.click('#list-template-options [data-template-picker-id="builtin:back_to_school"]');
    const name = await page.inputValue('#list-name');
    const icon = await page.inputValue('#list-icon');
    const type = await page.locator('#create-list-form input[name="list-type"]:checked').getAttribute('value');
    const summary = await page.textContent('#list-template-summary');
    await shot(page, 'template-picker');
    check('picking a template fills the name, icon and type', name === 'Rentrée scolaire' && icon === '🎒' && type === 'groceries', `${name} ${icon} ${type}`);
    check('…and sums it up', /15 articles · budget estimé .*Prévu/.test(summary), summary);

    // A name typed by hand survives picking another template.
    await page.fill('#list-name', 'Fournitures Léa');
    await page.click('#list-template-options [data-template-picker-id="builtin:travel"]');
    await page.click('#list-template-options [data-template-picker-id="builtin:back_to_school"]');
    check('a typed name is never replaced', await page.inputValue('#list-name') === 'Fournitures Léa');
    await page.fill('#list-name', '');
    await page.click('#list-template-options [data-template-picker-id="builtin:back_to_school"]');

    await page.click('#list-submit-button');
    await page.waitForTimeout(800);
    const lists = (await api(page, 'GET', `/api/v1/lists?house_id=${house.id}`)).body;
    const school = lists.find((l) => l.name === 'Rentrée scolaire');
    let items = school ? await itemsOf(page, school.id) : [];
    check('the list is created with the template\'s items, planned, priced and filed', items.length === 15 &&
      items.every((i) => i.purchase_status === 'planned' && typeof i.price === 'number' && i.aisle),
    JSON.stringify(items.slice(0, 3).map((i) => [i.title, i.purchase_status, i.price, i.aisle])));
    check('…and the API reports its budget', school && school.budget && school.budget.estimated_total > 0 && school.budget.spent_total === 0, JSON.stringify(school && school.budget));

    // 2. The list: summary, "Prévu" tags, aisle groups.
    await page.locator('#dashboard-view').getByText('Rentrée scolaire', { exact: true }).first().click();
    await page.locator('#items-section').waitFor({ state: 'visible' });
    await page.waitForTimeout(300);
    const budgetText = await page.textContent('#finance-total');
    const expected = new Intl.NumberFormat('fr-FR', { style: 'currency', currency: 'EUR' }).format(school.budget.estimated_total);
    check('the summary shows the planned budget', await page.isVisible('#finance-summary') && budgetText.replace(/\s/g, ' ') === expected.replace(/\s/g, ' '), `${budgetText} vs ${expected}`);
    check('every row is tagged "Prévu"', (await page.locator('#items-active li.swipe-item', { hasText: 'Prévu' }).count()) === 15);
    const headers = await page.locator('#items-active li.aisle-header').allTextContents();
    check('the new aisles group the list', headers.some((h) => h.includes('Papeterie')) && headers.some((h) => h.includes('High-tech')), headers.join(' | '));
    await shot(page, 'template-list');

    // A tag tap confirms one item.
    await row(page, 'Calculatrice').getByRole('button', { name: /prévu/i }).click();
    await page.waitForTimeout(500);
    items = await itemsOf(page, school.id);
    check('tapping "Prévu" confirms the item', items.find((i) => i.title === 'Calculatrice').purchase_status === 'to_buy');

    // "Valider les articles prévus" confirms the rest.
    await page.click('#list-options-button');
    const validateLabel = await page.textContent('#validate-planned-button');
    await page.click('#validate-planned-button');
    await page.waitForTimeout(1200);
    items = await itemsOf(page, school.id);
    check('"Valider les 14 articles prévus" confirms them all', validateLabel.includes('14') && items.every((i) => i.purchase_status === 'to_buy'), validateLabel.trim());
    check('…and the tags go away', (await page.locator('#items-active li.swipe-item', { hasText: 'Prévu' }).count()) === 0);

    // 3. Checking off offers "Prix payé"; an overrun shows red.
    await row(page, 'Cartable').locator('input[type="checkbox"]').click();
    const paidButton = page.locator('#toast-container button', { hasText: 'Prix payé' });
    await paidButton.waitFor({ timeout: 3000 });
    await paidButton.click();
    await page.locator('#paid-price-sheet').waitFor({ state: 'visible' });
    const prefilled = await page.inputValue('#paid-price-input');
    await page.fill('#paid-price-input', '45');
    const details = await page.textContent('#paid-price-details');
    await shot(page, 'paid-price-sheet');
    check('the sheet starts from the estimate and shows the gap live', prefilled === '39.9' && details.includes('Dépassement de 5,10'), `${prefilled} / ${details}`);
    await page.click('#paid-price-form [type="submit"]');
    await page.waitForTimeout(800);
    items = await itemsOf(page, school.id);
    const bag = items.find((i) => i.title === 'Cartable');
    check('the price paid is saved with the purchase', bag.done && bag.purchase_status === 'bought' && bag.actual_price === 45, JSON.stringify(bag));
    const variance = await page.textContent('#finance-variance');
    const varianceStatus = await page.textContent('#finance-variance-status');
    const varianceRed = await page.evaluate(() => document.getElementById('finance-variance').className.includes('--tk-budget-over'));
    check('the variance shows the overrun, in red, with its word', variance.includes('+5,10') && varianceStatus === 'Dépassement' && varianceRed, `${variance} ${varianceStatus}`);
    const collapsedVariance = await page.textContent('#finance-variance-collapsed');
    check('…also in the collapsed summary', collapsedVariance.includes('+5,10'), collapsedVariance);
    await shot(page, 'budget-over');

    // A saving elsewhere turns it green.
    const calc = items.find((i) => i.title === 'Calculatrice');
    await api(page, 'PATCH', `/api/v1/items/${calc.id}`, { purchase_status: 'bought', actual_price: 10 });
    await page.evaluate(() => refreshCurrentList());
    await page.waitForTimeout(500);
    const greenVariance = await page.textContent('#finance-variance');
    const green = await page.evaluate(() => document.getElementById('finance-variance').className.includes('--tk-budget-under'));
    check('a bigger saving makes the variance green', greenVariance.includes('−4,80') && green && await page.textContent('#finance-variance-status') === 'Économie', greenVariance);
    const listBudget = (await api(page, 'GET', `/api/v1/lists/${school.id}`)).body.budget;
    check('…in step with the server\'s budget', listBudget.variance === -4.8 && listBudget.variance_status === 'under', JSON.stringify(listBudget));
    const paidRow = await row(page, 'Calculatrice').textContent();
    check('a bought row shows what was paid', paidRow.includes('10,00') && paidRow.includes('prévu 19,90'), paidRow.replace(/\s+/g, ' '));

    // 4. The status picker in the item's actions.
    await row(page, 'Trousse').locator('span.cursor-pointer').first().click();
    await page.locator('#item-actions-purchase-row').waitFor({ state: 'visible' });
    const checkedStatus = await page.locator('#item-actions-purchase-row [aria-checked="true"]').getAttribute('data-purchase-status');
    await shot(page, 'item-actions-purchase');
    await page.click('#item-actions-purchase-row [data-purchase-status="planned"]');
    await page.waitForTimeout(500);
    items = await itemsOf(page, school.id);
    check('the actions sheet moves an item back to "Prévu"', checkedStatus === 'to_buy' && items.find((i) => i.title === 'Trousse').purchase_status === 'planned', checkedStatus);

    // 5. The edit modal's purchase fields (a bought item: in "Terminés").
    await page.evaluate(() => { document.getElementById('done-section').open = true; });
    await row(page, 'Cartable').locator('span.cursor-pointer').first().click();
    await page.click('#item-actions-edit-button');
    await page.locator('#edit-item-modal').waitFor({ state: 'visible' });
    const editStatus = await page.inputValue('#edit-item-purchase-status');
    const editPaid = await page.inputValue('#edit-item-actual-price');
    await page.fill('#edit-item-actual-price', '42');
    await page.click('#edit-item-form [type="submit"]');
    await page.waitForTimeout(800);
    items = await itemsOf(page, school.id);
    check('the edit modal shows and saves the price paid', editStatus === 'bought' && editPaid === '45' && items.find((i) => i.title === 'Cartable').actual_price === 42, `${editStatus} ${editPaid}`);

    // 6. Save as a House template — the price paid becomes the estimate.
    await page.click('#list-options-button');
    await page.click('#save-template-button');
    await page.locator('#save-template-sheet').waitFor({ state: 'visible' });
    await page.fill('#save-template-name', 'Fournitures 2026');
    await page.click('#save-template-submit');
    await page.waitForTimeout(800);
    const templates = (await api(page, 'GET', `/api/v1/shopping-templates?house_id=${house.id}`)).body;
    const saved = templates.find((tpl) => tpl.name === 'Fournitures 2026');
    check('the list is saved as a House template', saved && !saved.builtin && saved.item_count === 15 &&
      saved.items.find((i) => i.title === 'Cartable').estimated_price === 42, JSON.stringify(saved && saved.items[0]));

    // …and a new list starts from it.
    await page.click('#back-button');
    await page.click('#new-list-button');
    const houseCard = page.locator(`#list-template-options [data-template-picker-id="house:${saved.id}"]`);
    await houseCard.waitFor({ timeout: 5000 });
    check('the House template is offered, marked as the House\'s', (await houseCard.textContent()).includes('Maison'));
    await houseCard.click();
    check('…with a delete button', await page.isVisible('#list-template-delete-button'));
    await page.click('#list-submit-button');
    await page.waitForTimeout(800);
    const fromHouse = (await api(page, 'GET', `/api/v1/lists?house_id=${house.id}`)).body.find((l) => l.name === 'Fournitures 2026');
    check('a list starts from the House template', fromHouse && (await itemsOf(page, fromHouse.id)).length === 15);

    // …and it can be deleted from the picker.
    await page.click('#new-list-button');
    await houseCard.waitFor({ timeout: 5000 });
    await houseCard.click();
    await page.click('#list-template-delete-button');
    await page.waitForTimeout(600);
    const remaining = (await api(page, 'GET', `/api/v1/shopping-templates?house_id=${house.id}`)).body;
    check('deleting the House template', !remaining.some((tpl) => tpl.id === saved.id) && await houseCard.count() === 0);
    await page.click('#close-modal-button');

    // 7. Task templates: the cards follow the type picked.
    await page.click('#new-list-button');
    await page.locator('#list-template-options [data-template-picker-id="builtin:cleaning"]').waitFor({ timeout: 5000 });
    await page.click(typeOption('todo'));
    const taskIds = await templateCardIds(page);
    check('"Tâches" shows the task templates only', taskIds.join(',') === ',builtin:cleaning_tasks,builtin:moving_tasks,builtin:back_to_school_tasks', taskIds.join(','));
    const taskCards = await page.locator('#list-template-options [role="radio"]').allTextContents();
    check('…with no price on them', taskCards.length === 4 && taskCards.every((text) => !text.includes('€')) && taskCards[2].includes('12 tâches'), taskCards.join(' | '));
    await page.click('#list-template-options [data-template-picker-id="builtin:moving_tasks"]');
    let summaryText = await page.textContent('#list-template-summary');
    check('a checklist template fills the form and says what it holds', await page.inputValue('#list-name') === 'To-Do Déménagement' &&
      await page.inputValue('#list-icon') === '📦' &&
      await page.locator('#create-list-form input[name="list-type"]:checked').getAttribute('value') === 'todo' &&
      summaryText.includes('12 tâches') && summaryText.includes('check-list') && !summaryText.includes('€') && !summaryText.includes('Prévu'), summaryText);
    await page.click('#list-template-options [data-template-picker-id="builtin:cleaning_tasks"]');
    summaryText = await page.textContent('#list-template-summary');
    check('…a chores template, that its tasks repeat', summaryText.includes('15 tâches, toutes récurrentes') &&
      await page.locator('#list-template-options [aria-checked="true"]').getAttribute('data-template-picker-id') === 'builtin:cleaning_tasks', summaryText);
    await page.waitForTimeout(400); // base.css's 150ms color transitions
    await shot(page, 'task-templates');

    // Back to a purchase type: the task template goes, with what it filled.
    await page.click(typeOption('groceries'));
    const purchaseIds = await templateCardIds(page);
    check('a purchase type drops the task template and shows its own', purchaseIds.length === 6 && purchaseIds.includes('builtin:moving') &&
      !purchaseIds.some((id) => id.endsWith('_tasks')) && await page.inputValue('#list-name') === '' &&
      await page.locator('#list-template-options [aria-checked="true"]').getAttribute('data-template-picker-id') === '', purchaseIds.join(','));
    // A purchase template picked from "Achats ponctuels" brings its own type.
    await page.click(typeOption('shopping'));
    await page.click('#list-template-options [data-template-picker-id="builtin:moving"]');
    check('a purchase template picks "Courses"', await page.locator('#create-list-form input[name="list-type"]:checked').getAttribute('value') === 'groceries');
    // A free list has no template at all.
    await page.click(typeOption('custom'));
    check('a free list shows no template picker', await page.isHidden('#list-template-field') && await page.inputValue('#list-name') === '');

    await page.click(typeOption('todo'));
    await page.click('#list-template-options [data-template-picker-id="builtin:cleaning_tasks"]');
    await page.click('#list-submit-button');
    await page.waitForTimeout(800);
    const chores = (await api(page, 'GET', `/api/v1/lists?house_id=${house.id}`)).body.find((l) => l.name === 'Tâches de Ménage & Entretien');
    const choreItems = chores ? await itemsOf(page, chores.id) : [];
    check('the task list is created with recurring tasks, not planned, unpriced', chores && chores.type === 'todo' && !chores.budget &&
      choreItems.length === 15 && choreItems.every((i) => i.recurrence_rule && i.is_recurring && i.purchase_status === 'to_buy' && i.price === undefined && !i.due_date),
    JSON.stringify(choreItems.slice(0, 2)));
    await page.locator('#dashboard-view').getByText('Tâches de Ménage & Entretien', { exact: true }).first().click();
    await page.locator('#items-section').waitFor({ state: 'visible' });
    await page.waitForTimeout(400);
    check('…shown with their cadence, without budget or "Prévu"', await page.isHidden('#finance-summary') &&
      (await page.locator('#items-active li.swipe-item', { hasText: 'Prévu' }).count()) === 0 &&
      (await row(page, 'Laver les vitres').textContent()).includes('Chaque mois'), (await row(page, 'Laver les vitres').textContent()).replace(/\s+/g, ' '));
    await shot(page, 'task-list');

    // Saved as a House template, it is offered for task lists only.
    await page.click('#list-options-button');
    await page.click('#save-template-button');
    await page.locator('#save-template-sheet').waitFor({ state: 'visible' });
    const taskHint = await page.textContent('#save-template-hint');
    await page.fill('#save-template-name', 'Routine maison');
    await page.click('#save-template-submit');
    await page.waitForTimeout(800);
    const routine = (await api(page, 'GET', `/api/v1/shopping-templates?house_id=${house.id}&list_type=todo`)).body.find((tpl) => tpl.name === 'Routine maison');
    check('a task list saves as a task template, recurrence included', taskHint.includes('récurrence') && routine && routine.list_type === 'todo' &&
      routine.items.length === 15 && routine.items.every((i) => i.recurrence_rule && i.estimated_price === undefined), taskHint);
    await page.click('#back-button');
    await page.click('#new-list-button');
    await page.locator('#list-template-options [data-template-picker-id="builtin:cleaning"]').waitFor({ timeout: 5000 });
    const routineCard = `#list-template-options [data-template-picker-id="house:${routine && routine.id}"]`;
    const hiddenForPurchase = await page.locator(routineCard).count() === 0;
    await page.click(typeOption('todo'));
    check('…among the task templates, not the purchase ones', hiddenForPurchase && (await page.locator(routineCard).textContent()).includes('Maison'));
    await page.click(typeOption('shopping'));

    // 8. The carousel stays inside the dialog's padding at phone widths,
    // and its last card stops short of the edge.
    for (const width of [390, 360, 320]) {
      await page.setViewportSize({ width, height: 740 });
      await page.waitForTimeout(300);
      const layout = await carouselLayout(page);
      const near = (a, b) => Math.abs(a - b) <= 1;
      check(`at ${width}px the carousel lines up with the fields and ends with a margin`,
        layout.viewport === width && layout.pageScrollWidth === width && layout.dialogScrollWidth === 0 &&
        near(layout.scrollerInset[0], layout.padding) && near(layout.scrollerInset[1], layout.padding) &&
        near(layout.fieldEdges[0], 0) && near(layout.fieldEdges[1], 0) &&
        near(layout.firstCardOffset, 0) && near(layout.lastCardGap, 16) && layout.atEnd,
      JSON.stringify(layout));
      await shot(page, `carousel-${width}`);
    }
    // A swipe lands on a card's edge (scroll-snap), not halfway through one.
    // A finger dragged 90px with raw touch events — Input.synthesizeScrollGesture's
    // touch mode scrolls nothing in headless Chromium, not even a bare
    // overflow-x box.
    const box = await page.locator('#list-template-scroller').boundingBox();
    const cdp = await context.newCDPSession(page);
    const fingerY = box.y + box.height / 2;
    const fingerX = box.x + box.width * 0.75;
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: fingerX, y: fingerY }] });
    for (let step = 1; step <= 10; step++) {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: fingerX - 9 * step, y: fingerY }] });
      await page.waitForTimeout(16);
    }
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
    await page.waitForTimeout(800);
    const snapped = await page.evaluate(() => {
      const scroller = document.getElementById('list-template-scroller');
      const left = scroller.getBoundingClientRect().left;
      const max = scroller.scrollWidth - scroller.clientWidth;
      const offsets = [...document.querySelectorAll('#list-template-options [role="radio"]')]
        .map((card) => Math.round(card.getBoundingClientRect().left - left));
      return { scrollLeft: scroller.scrollLeft, atEnd: Math.abs(scroller.scrollLeft - max) < 1, offsets };
    });
    check('a swipe snaps to a card', snapped.scrollLeft > 0 && !snapped.atEnd && snapped.offsets.some((o) => Math.abs(o) <= 1), JSON.stringify(snapped));
    await shot(page, 'carousel-swiped');

    // 9. Nothing wider than a 320px phone: the modal with its picker, the list.
    await page.setViewportSize({ width: 320, height: 640 });
    await page.waitForTimeout(300);
    const modal = await measureOverflow(page, '#new-list-modal *');
    await shot(page, 'narrow-modal');
    await page.click('#close-modal-button');
    await page.locator('#dashboard-view').getByText('Rentrée scolaire', { exact: true }).first().click();
    await page.locator('#items-section').waitFor({ state: 'visible' });
    await page.waitForTimeout(3500); // toasts gone
    const listView = await measureOverflow(page, '#items-section *');
    await shot(page, 'narrow-list');
    // Collapsed, the summary's header row carries budget · spent · signed
    // variance on one line.
    await page.evaluate(() => { document.getElementById('finance-summary').open = false; });
    await page.waitForTimeout(200);
    const collapsed = await measureOverflow(page, '#finance-summary *');
    const headerFits = await page.evaluate(() => {
      const summary = document.querySelector('#finance-summary > summary');
      return summary.scrollWidth <= summary.clientWidth;
    });
    await shot(page, 'narrow-collapsed');
    check('nothing overflows at 320px', [modal, listView, collapsed].every((o) => o.inner === 320 && o.client === 320 && o.out.length === 0) && headerFits,
      JSON.stringify([modal, listView, collapsed, headerFits]));
    await page.evaluate(() => {
      document.getElementById('finance-summary').open = true;
      document.documentElement.dataset.theme = 'dark';
      document.documentElement.classList.add('dark');
    });
    await page.waitForTimeout(400);
    await shot(page, 'narrow-list-dark');
    await page.evaluate(() => {
      document.documentElement.dataset.theme = 'light';
      document.documentElement.classList.remove('dark');
    });

    // 10. Offline, a template can't be used.
    await page.click('#back-button');
    await context.setOffline(true);
    await page.click('#new-list-button');
    await page.click('#list-template-options [data-template-picker-id=""]').catch(() => {});
    const offlineSummary = await page.textContent('#list-template-summary');
    check('offline, the picker says templates are unavailable', offlineSummary.includes('indisponibles'), offlineSummary);
    await context.setOffline(false);
    await page.click('#close-modal-button');

    // A Courses list without prices stays as light as before.
    const plain = (await api(page, 'POST', '/api/v1/lists', { name: 'Courses simples', type: 'groceries', house_id: house.id })).body;
    await api(page, 'POST', '/api/v1/items', { list_id: plain.id, title: 'Pain' });
    await page.reload();
    await page.locator('#dashboard-view').getByText('Courses simples', { exact: true }).first().click();
    await page.locator('#items-section').waitFor({ state: 'visible' });
    await page.waitForTimeout(300);
    check('a Courses list with no price shows no budget', await page.isHidden('#finance-summary') && (await page.locator('#items-active .item-card__price').count()) === 0);

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
