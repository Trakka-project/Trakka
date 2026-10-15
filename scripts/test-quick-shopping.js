// End-to-end check of quick shopping lists ("Courses") in a real browser:
// no duplicates (an existing product is topped up or brought back),
// autocomplete, the "Par rayon" grouping, favorites and the ⭐ panel, the
// aisle in the edit modal, and the barcode scanner — fed by Chromium's fake
// camera playing a generated video of an EAN-13 symbol, so the camera,
// static/js/barcode.js's decoder, the House catalog lookup and the addition
// all run for real. Also checks that nothing overflows a 320px-wide phone.
//
// Usage (see README.md): cd scripts && npm run test:quick-shopping
// Builds ./cmd/server and runs it on a throwaway database, like the
// showcase script; PRODUCT_LOOKUP_ENABLED=false keeps it off the network.

import { chromium } from 'playwright';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath, pathToFileURL } from 'node:url';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const USER = { name: 'Camille', email: 'camille@example.com', password: 'correct-horse-battery' };
const NUTELLA = '3017620422003';

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
      DB_PATH: path.join(workDir, 'quick.db'),
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

// A Y4M video (what Chromium's fake camera plays) of an EAN-13 symbol,
// black on white, slightly tilted lighting so the decoder's thresholding
// has something to do.
async function writeBarcodeVideo(file, code) {
  const L = ['0001101', '0011001', '0010011', '0111101', '0100011', '0110001', '0101111', '0111011', '0110111', '0001011'];
  const G = L.map((p) => p.split('').map((c) => (c === '1' ? '0' : '1')).reverse().join(''));
  const R = L.map((p) => p.split('').map((c) => (c === '1' ? '0' : '1')).join(''));
  const PARITY = ['LLLLLL', 'LLGLGG', 'LLGGLG', 'LLGGGL', 'LGLLGG', 'LGGLLG', 'LGGGLL', 'LGLGLG', 'LGLGGL', 'LGGLGL'];
  const d = code.split('').map(Number);
  let modules = '101';
  for (let i = 1; i <= 6; i++) modules += (PARITY[d[0]][i - 1] === 'L' ? L : G)[d[i]];
  modules += '01010';
  for (let i = 7; i <= 12; i++) modules += R[d[i]];
  modules += '101';

  const W = 640;
  const H = 480;
  const m = 4.2;
  const x0 = Math.round((W - modules.length * m) / 2);
  const y = Buffer.alloc(W * H);
  for (let row = 0; row < H; row++) {
    for (let col = 0; col < W; col++) {
      const pos = Math.floor((col - x0) / m);
      const inSymbol = row > H * 0.3 && row < H * 0.7 && pos >= 0 && pos < modules.length;
      const dark = inSymbol && modules[pos] === '1';
      y[row * W + col] = (dark ? 30 : 225) - Math.round((col / W) * 30);
    }
  }
  const chroma = Buffer.alloc((W / 2) * (H / 2), 128);
  const frame = Buffer.concat([Buffer.from('FRAME\n'), y, chroma, chroma]);
  const frames = Array.from({ length: 10 }, () => frame);
  await writeFile(file, Buffer.concat([Buffer.from(`YUV4MPEG2 W${W} H${H} F10:1 Ip A1:1 C420jpeg\n`), ...frames]));
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

async function addViaQuickAdd(page, title) {
  await page.fill('#item-title', title);
  await page.press('#item-title', 'Enter');
  await page.waitForTimeout(400);
}

async function main() {
  const workDir = await mkdtemp(path.join(tmpdir(), 'trakka-quick-'));
  const video = path.join(workDir, 'barcode.y4m');
  await writeBarcodeVideo(video, NUTELLA);
  const server = await startServer(workDir);
  const browser = await chromium.launch({
    args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', `--use-file-for-fake-video-capture=${video}`],
  });
  const errors = [];
  try {
    const context = await browser.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
      locale: 'fr-FR',
      serviceWorkers: 'block',
      permissions: ['camera'],
    });
    await context.addInitScript(() => {
      try { localStorage.setItem('trakka:lang', 'fr'); } catch { /* not needed */ }
    });
    const page = await context.newPage();
    page.on('pageerror', (err) => errors.push(err.message));
    // Expected noise: the service worker is blocked in this context (its
    // registration fails), and an unknown barcode's lookup is a 404 the
    // browser logs as a failed resource load — checked by URL below.
    page.on('console', (msg) => {
      const text = msg.text();
      if (msg.type() !== 'error' || /service worker/i.test(text) || /Failed to load resource.*404/.test(text)) return;
      errors.push(text);
    });
    const notFound = [];
    page.on('response', (res) => { if (res.status() === 404) notFound.push(new URL(res.url()).pathname); });

    // Account, House, a "Courses" list.
    await page.goto(`${server.baseURL}/auth/login?mode=register`);
    await page.fill('#display_name', USER.name);
    await page.fill('#email', USER.email);
    await page.fill('#password', USER.password);
    await page.fill('#password_confirm', USER.password);
    await Promise.all([page.waitForURL(`${server.baseURL}/`), page.click('form:has(#password_confirm) [type=submit]')]);
    await page.waitForTimeout(500);
    const [house] = (await api(page, 'GET', '/api/v1/houses')).body;
    const list = (await api(page, 'POST', '/api/v1/lists', { name: 'Courses de la semaine', type: 'groceries', house_id: house.id })).body;
    await page.reload();
    await page.locator('#dashboard-view').getByText('Courses de la semaine', { exact: true }).first().click();
    await page.locator('#items-section').waitFor({ state: 'visible' });

    check('⭐ and 📷 shown on a Courses list', await page.isVisible('#quick-shop-panel-button') && await page.isVisible('#quick-shop-scan-button'));

    // 1. No duplicates: active → quantity adds up.
    await addViaQuickAdd(page, 'Lait demi-écrémé');
    await addViaQuickAdd(page, 'lait demi ecreme');
    let items = await itemsOf(page, list.id);
    check('adding the same product twice keeps one item', items.length === 1 && items[0].quantity === 2, JSON.stringify(items.map((i) => [i.title, i.quantity])));
    check('aisle guessed for a new product', items[0].aisle === 'dairy', items[0].aisle);
    check('one row on screen', (await page.locator('#items-active li.swipe-item').count()) === 1);

    // 2. Done → brought back with the new quantity.
    await api(page, 'PATCH', `/api/v1/items/${items[0].id}`, { done: true });
    await page.evaluate(() => refreshCurrentList());
    await page.waitForTimeout(300);
    await page.click('#quick-add-toggle');
    await page.fill('#item-quantity', '1');
    await addViaQuickAdd(page, 'LAIT DEMI-ÉCRÉMÉ');
    items = await itemsOf(page, list.id);
    check('a checked product added again comes back unchecked', items.length === 1 && !items[0].done && items[0].quantity === 1,
      JSON.stringify(items.map((i) => [i.title, i.done, i.quantity])));
    const doneRows = await page.locator('#items-done li.swipe-item').count();
    check('…and leaves the "Terminés" section', doneRows === 0, `${doneRows} done rows`);

    // More products for the next checks.
    for (const title of ['Pâtes', 'Pain de mie', 'Glace vanille', 'Tomates', 'Papier toilette']) await addViaQuickAdd(page, title);

    // 3. Autocomplete from the House catalog.
    await page.fill('#item-title', 'pa');
    await page.locator('#item-title-suggestions li').first().waitFor({ timeout: 3000 });
    const suggestions = await page.locator('#item-title-suggestions li').allTextContents();
    await shot(page, 'autocomplete');
    check('autocomplete suggests catalog products', suggestions.some((s) => s.includes('Pâtes')) && suggestions.some((s) => s.includes('Pain de mie')), suggestions.join(' | '));
    check('…marking the ones already on the list', suggestions.some((s) => s.includes('Dans la liste')), suggestions.join(' | '));
    await page.locator('#item-title-suggestions li', { hasText: 'Pâtes' }).dispatchEvent('pointerdown');
    await page.waitForTimeout(500);
    items = await itemsOf(page, list.id);
    const pasta = items.find((i) => i.title === 'Pâtes');
    check('picking a suggestion already on the list tops it up', pasta && pasta.quantity === 2 && items.filter((i) => i.title === 'Pâtes').length === 1);

    // 4. A Courses list opens grouped "Par rayon" without anyone choosing it.
    const headers = await page.locator('#items-active li.aisle-header').allTextContents();
    const order = headers.map((h) => h.replace(/\d+$/, '').trim());
    await shot(page, 'aisles');
    check('a Courses list is grouped "Par rayon" by default, in shop order', order.length >= 5 && order[0].includes('Fruits') && order[order.length - 1].includes('Surgelés'), order.join(' > '));
    check('…without the tags the headings already give', (await page.locator('#items-active .item-aisle-badge').count()) === 0);
    const sortHighlighted = await page.evaluate(() => document.getElementById('sort-items-button').classList.contains('bg-sky-500/10'));
    await page.click('#sort-items-button');
    const checkedOption = await page.locator('#sort-items-sheet [data-sort-mode]:has(.sort-option-check:not([hidden]))').getAttribute('data-sort-mode');
    const manualLabel = await page.textContent('#sort-option-manual-label');
    check('the sort sheet shows "Par rayon" as the (unhighlighted) default', !sortHighlighted && checkedOption === 'aisle' && manualLabel === 'Ordre de la liste', `${checkedOption}, "${manualLabel}", highlighted ${sortHighlighted}`);

    // In the list's own order, each row tags its aisle.
    await page.click('#sort-option-manual');
    await page.waitForTimeout(300);
    const tomatoTag = await page.locator('#items-active li.swipe-item', { hasText: 'Tomates' }).locator('.item-aisle-badge').textContent();
    check('outside "Par rayon", a row tags its aisle', (await page.locator('#items-active li.aisle-header').count()) === 0 && tomatoTag.includes('Fruits & légumes'), tomatoTag);
    await shot(page, 'aisle-tags-light');

    // The tag opens the aisle picker; picking files the item, and the House learns it.
    await page.locator('#items-active li.swipe-item', { hasText: 'Tomates' }).locator('.item-aisle-badge').click();
    await page.locator('#aisle-picker-sheet').waitFor({ state: 'visible' });
    const pickerChecked = await page.locator('#aisle-picker-options [aria-checked="true"]').textContent();
    await shot(page, 'aisle-picker-light');
    await page.locator('#aisle-picker-options [role="radio"]', { hasText: 'Épicerie salée' }).click();
    await page.waitForTimeout(500);
    items = await itemsOf(page, list.id);
    const tomatoProduct = (await api(page, 'GET', `/api/v1/houses/${house.id}/products?q=tomates`)).body[0];
    check('the tag opens the aisle picker, which changes the aisle', pickerChecked.includes('Fruits & légumes') && items.find((i) => i.title === 'Tomates').aisle === 'pantry', pickerChecked);
    check('…and the House catalog learns it', tomatoProduct && tomatoProduct.aisle === 'pantry', JSON.stringify(tomatoProduct));

    // The ⋮ menu has a "Rayon" row too.
    await page.locator('#items-active li.swipe-item', { hasText: 'Pâtes' }).locator('span.cursor-pointer').first().click();
    await page.locator('#item-actions-aisle-button').waitFor({ state: 'visible' });
    const aisleRow = await page.textContent('#item-actions-aisle-button');
    await page.click('#item-actions-aisle-button');
    await page.locator('#aisle-picker-sheet').waitFor({ state: 'visible' });
    await page.locator('#aisle-picker-options [role="radio"]', { hasText: 'Sans rayon' }).click();
    await page.waitForTimeout(500);
    items = await itemsOf(page, list.id);
    check('the ⋮ menu shows the aisle and changes it', aisleRow.includes('Épicerie salée') && !items.find((i) => i.title === 'Pâtes').aisle, aisleRow);
    await page.locator('#items-active li.swipe-item', { hasText: 'Pâtes' }).locator('span.cursor-pointer').first().click();
    await page.click('#item-actions-aisle-button');
    await page.locator('#aisle-picker-options [role="radio"]', { hasText: 'Épicerie salée' }).click();
    await page.waitForTimeout(500);

    // The tag stays readable in both themes (WCAG AA for small text: 4.5:1),
    // its translucent background composited over the card behind it.
    const tagContrast = async () => page.evaluate(() => {
      const parse = (c) => (c.match(/[\d.]+/g) || []).map(Number);
      const over = (top, under) => {
        const a = top[3] === undefined ? 1 : top[3];
        return [0, 1, 2].map((i) => top[i] * a + under[i] * (1 - a));
      };
      const background = (el) => {
        const layers = [];
        for (let node = el; node; node = node.parentElement) {
          const c = parse(getComputedStyle(node).backgroundColor);
          if (c.length >= 3 && (c[3] === undefined || c[3] > 0)) {
            layers.push(c);
            if (c[3] === undefined || c[3] >= 1) break;
          }
        }
        return layers.reverse().reduce((under, top) => over(top, under), [255, 255, 255]);
      };
      const lum = (rgb) => {
        const [r, g, b] = rgb.map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; });
        return 0.2126 * r + 0.7152 * g + 0.0722 * b;
      };
      const tag = document.querySelector('#items-active .item-aisle-badge');
      const fg = parse(getComputedStyle(tag).color);
      const bg = background(tag);
      const [a, b] = [lum(fg.slice(0, 3)), lum(bg)].sort((x, y) => y - x);
      return Math.round(((a + 0.05) / (b + 0.05)) * 100) / 100;
    });
    const lightContrast = await tagContrast();
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
    await page.waitForTimeout(300);
    const darkContrast = await tagContrast();
    await shot(page, 'aisle-tags-dark');
    await page.locator('#items-active li.swipe-item', { hasText: 'Tomates' }).locator('.item-aisle-badge').click();
    await page.locator('#aisle-picker-sheet').waitFor({ state: 'visible' });
    await shot(page, 'aisle-picker-dark');
    await page.click('#close-aisle-picker-sheet-button');
    check('the aisle tag is readable in light and dark themes (≥ 4.5:1)', lightContrast >= 4.5 && darkContrast >= 4.5, `light ${lightContrast}, dark ${darkContrast}`);
    if (process.env.SHOTS_DIR) {
      // Wide screen (web), both themes.
      await page.setViewportSize({ width: 1280, height: 800 });
      await page.waitForTimeout(300);
      await shot(page, 'aisle-tags-desktop-dark');
      await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
      await page.waitForTimeout(300);
      await shot(page, 'aisle-tags-desktop-light');
      await page.setViewportSize({ width: 390, height: 844 });
    }
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));

    // Back to "Par rayon" for the rest.
    await page.click('#sort-items-button');
    await page.click('#sort-option-aisle');
    await page.waitForTimeout(300);

    // 5. Favorite from the actions sheet, then the ⭐ panel.
    await page.locator('#items-active li.swipe-item', { hasText: 'Pain de mie' }).locator('span.cursor-pointer').first().click();
    await page.locator('#item-actions-favorite-button').waitFor({ state: 'visible' });
    await page.click('#item-actions-favorite-button');
    await page.waitForTimeout(500);
    check('a favorite shows its ★ in the list', (await page.locator('#items-active li.swipe-item', { hasText: 'Pain de mie' }).locator('[aria-label="Favori"]').count()) === 1);
    await page.click('#quick-shop-panel-button');
    await page.locator('#quick-shop-favorites .quick-shop-chip').first().waitFor();
    await page.locator('#quick-shop-favorites .quick-shop-chip button').first().click();
    await page.waitForTimeout(500);
    items = await itemsOf(page, list.id);
    await shot(page, 'panel');
    check('a tap in the ⭐ panel tops the favorite up', items.find((i) => i.title === 'Pain de mie').quantity === 2);
    await page.click('#close-quick-shop-sheet-button');

    // 6. Aisle in the edit modal, learned by the catalog.
    await page.locator('#items-active li.swipe-item', { hasText: 'Papier toilette' }).locator('span.cursor-pointer').first().click();
    await page.click('#item-actions-edit-button');
    await page.selectOption('#edit-item-aisle', 'hygiene');
    await page.click('#edit-item-form [type=submit]');
    await page.waitForTimeout(500);
    items = await itemsOf(page, list.id);
    const products = (await api(page, 'GET', `/api/v1/houses/${house.id}/products?q=papier`)).body;
    check('the edit modal sets the aisle', items.find((i) => i.title === 'Papier toilette').aisle === 'hygiene');
    check('…which the House catalog learns', products[0] && products[0].aisle === 'hygiene', JSON.stringify(products));

    // 7. Scanner: fake camera → barcode.js → unknown code → name typed →
    //    added with the code → scanned again → recognized and topped up.
    await page.click('#quick-shop-scan-button');
    await page.locator('#barcode-scanner-sheet').waitFor({ state: 'visible' });
    await page.locator('#barcode-scanner-sheet').waitFor({ state: 'hidden', timeout: 15000 });
    await page.locator('#quick-add-scan-chip').waitFor({ state: 'visible' });
    const chip = await page.textContent('#quick-add-scan-chip-label');
    await shot(page, 'scan-chip');
    check('the camera reads the EAN-13 (JS decoder)', chip.includes(NUTELLA), chip);
    await addViaQuickAdd(page, 'Nutella');
    let catalogHit = await api(page, 'GET', `/api/v1/houses/${house.id}/products/barcode/${NUTELLA}`);
    check('the scanned code is filed under the product', catalogHit.status === 200 && catalogHit.body.name === 'Nutella', JSON.stringify(catalogHit));
    await page.click('#quick-shop-scan-button');
    await page.locator('#barcode-scanner-sheet').waitFor({ state: 'hidden', timeout: 15000 });
    await page.waitForTimeout(800);
    items = await itemsOf(page, list.id);
    const nutella = items.filter((i) => i.title === 'Nutella');
    check('scanning a known product adds it directly (no duplicate)', nutella.length === 1 && nutella[0].quantity === 2, JSON.stringify(nutella));

    if (process.env.SHOTS_DIR) {
      // The live camera view, decoding paused so the sheet stays open.
      await page.evaluate(() => { window.__decode = window.TrakkaBarcode.decode; window.TrakkaBarcode.decode = () => null; });
      await page.click('#quick-shop-scan-button');
      await page.locator('#barcode-scanner-viewport').waitFor({ state: 'visible' });
      await page.waitForTimeout(800);
      await shot(page, 'scanner-camera');
      await page.click('#close-barcode-scanner-button');
      await page.evaluate(() => { window.TrakkaBarcode.decode = window.__decode; });
    }

    // Camera refused: the sheet says so, and the digits can be typed.
    await page.evaluate(() => {
      navigator.mediaDevices.getUserMedia = () => Promise.reject(new DOMException('denied', 'NotAllowedError'));
    });
    await page.click('#quick-shop-scan-button');
    await page.locator('#barcode-scanner-sheet').waitFor({ state: 'visible' });
    await page.fill('#barcode-scanner-manual-input', '12');
    await page.click('#barcode-scanner-manual-form [type=submit]');
    await page.waitForTimeout(300);
    const status = await page.textContent('#barcode-scanner-status');
    check('camera refused: the sheet offers typing the code', await page.isVisible('#barcode-scanner-sheet') && /chiffres/.test(status), status);
    await shot(page, 'scanner');
    await page.fill('#barcode-scanner-manual-input', '3017 6204 22003');
    await page.click('#barcode-scanner-manual-form [type=submit]');
    await page.locator('#barcode-scanner-sheet').waitFor({ state: 'hidden' });
    await page.waitForTimeout(800);
    items = await itemsOf(page, list.id);
    check('a typed code is recognized like a scanned one', items.find((i) => i.title === 'Nutella').quantity === 3);

    // An unknown code whose product is already on the list: topped up, and
    // the code is filed under it all the same.
    const PASTA_CODE = '8076800195057';
    await page.click('#quick-shop-scan-button');
    await page.locator('#barcode-scanner-sheet').waitFor({ state: 'visible' });
    await page.fill('#barcode-scanner-manual-input', PASTA_CODE);
    await page.click('#barcode-scanner-manual-form [type=submit]');
    await page.locator('#quick-add-scan-chip').waitFor({ state: 'visible' });
    await addViaQuickAdd(page, 'pâtes');
    await page.waitForTimeout(400);
    items = await itemsOf(page, list.id);
    catalogHit = await api(page, 'GET', `/api/v1/houses/${house.id}/products/barcode/${PASTA_CODE}`);
    check('scanning a product already on the list tops it up and files its code',
      items.filter((i) => i.title === 'Pâtes').length === 1 && items.find((i) => i.title === 'Pâtes').quantity === 3 && catalogHit.status === 200 && catalogHit.body.name === 'Pâtes',
      JSON.stringify([items.filter((i) => i.title === 'Pâtes'), catalogHit]));

    // Inside the Android app (a simulated TrakkaApp plugin): the browser
    // camera is never tried — the app refuses it to every page — whatever
    // the native scanner answers.
    await page.evaluate(() => {
      window.__gumCalls = 0;
      navigator.mediaDevices.getUserMedia = () => {
        window.__gumCalls++;
        return Promise.reject(new DOMException('denied', 'NotAllowedError'));
      };
      window.Capacitor = { Plugins: { TrakkaApp: {} } }; // an app built before scanBarcode
    });
    const scanInApp = async () => {
      await page.click('#quick-shop-scan-button');
      await page.waitForTimeout(400);
      return { open: await page.isVisible('#barcode-scanner-sheet'), status: await page.textContent('#barcode-scanner-status') };
    };
    let inApp = await scanInApp();
    check('old app: asks to update the app, no "camera refused"', inApp.open && /mettez|dernière version/.test(inApp.status) && !/refusé/.test(inApp.status), inApp.status);
    await page.click('#close-barcode-scanner-button');

    await page.evaluate(() => {
      window.Capacitor.Plugins.TrakkaApp.scanBarcode = () => Promise.reject(Object.assign(new Error('Camera permission denied'), { code: 'camera_denied' }));
    });
    inApp = await scanInApp();
    check('app, Android permission refused: points to the Android settings', inApp.open && /réglages Android/.test(inApp.status), inApp.status);
    await page.click('#close-barcode-scanner-button');

    await page.evaluate(() => {
      window.Capacitor.Plugins.TrakkaApp.scanBarcode = () => Promise.reject(Object.assign(new Error('Scan cancelled'), { code: 'cancelled' }));
    });
    inApp = await scanInApp();
    check('app, scan cancelled: nothing opens', !inApp.open);

    const nutellaBefore = (await itemsOf(page, list.id)).find((i) => i.title === 'Nutella').quantity;
    await page.evaluate((code) => {
      window.Capacitor.Plugins.TrakkaApp.scanBarcode = () => Promise.resolve({ code });
    }, NUTELLA);
    inApp = await scanInApp();
    await page.waitForTimeout(600);
    const nutellaAfter = (await itemsOf(page, list.id)).find((i) => i.title === 'Nutella').quantity;
    check('app, native scan: the product is added', !inApp.open && nutellaAfter === nutellaBefore + 1, `${nutellaBefore} → ${nutellaAfter}`);
    check('app: the browser camera was never asked for', await page.evaluate(() => window.__gumCalls) === 0);
    await page.evaluate(() => { delete window.Capacitor; });

    // 8. Nothing wider than a 320px phone, grouped or with the aisle tags.
    await page.setViewportSize({ width: 320, height: 640 });
    await page.waitForTimeout(300);
    const measureOverflow = () => page.evaluate(() => {
      const vw = document.documentElement.clientWidth;
      const out = [];
      for (const el of document.querySelectorAll('#create-item-form *, #items-active *')) {
        const r = el.getBoundingClientRect();
        if (r.width > 0 && r.right > vw + 0.5 && !el.closest('[hidden]')) out.push(`${el.tagName}#${el.id}.${String(el.className).slice(0, 40)} → ${Math.round(r.right)}`);
      }
      return { inner: window.innerWidth, client: vw, out: out.slice(0, 5) };
    });
    const grouped = await measureOverflow();
    await page.click('#sort-items-button');
    await page.click('#sort-option-manual');
    await page.waitForTimeout(300);
    const tagged = await measureOverflow();
    await page.click('#sort-items-button');
    await page.click('#sort-option-aisle');
    await page.waitForTimeout(3500); // toasts gone
    await shot(page, 'narrow-320');
    check('nothing overflows at 320px', [grouped, tagged].every((o) => o.inner === 320 && o.client === 320 && o.out.length === 0), JSON.stringify([grouped, tagged]));

    // 9. Offline, with the service worker: adding a product already on the
    //    list must not leave a duplicate in the offline mirror either, and
    //    everything syncs once back online.
    const swContext = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, locale: 'fr-FR' });
    const swPage = await swContext.newPage();
    swPage.on('pageerror', (err) => errors.push(`[sw] ${err.message}`));
    await swPage.goto(`${server.baseURL}/auth/login`);
    await swPage.fill('#email', USER.email);
    await swPage.fill('#password', USER.password);
    await Promise.all([swPage.waitForURL(`${server.baseURL}/`), swPage.click('form:has(#password):not(:has(#password_confirm)) [type=submit]')]);
    await swPage.evaluate(() => navigator.serviceWorker.ready);
    await swPage.reload(); // now controlled by the service worker
    await swPage.evaluate(() => navigator.serviceWorker.ready);
    await swPage.locator('#dashboard-view').getByText('Courses de la semaine', { exact: true }).first().click();
    await swPage.locator('#items-section').waitFor({ state: 'visible' });
    await swPage.waitForTimeout(800);
    const before = (await itemsOf(swPage, list.id)).find((i) => i.title === 'Lait demi-écrémé');

    await swContext.setOffline(true);
    await addViaQuickAdd(swPage, 'lait demi-écrémé');
    await addViaQuickAdd(swPage, 'Beurre doux');
    await swPage.waitForTimeout(500);
    await swPage.reload();
    await swPage.locator('#items-section').waitFor({ state: 'visible' }).catch(() => {});
    if (await swPage.isHidden('#items-section')) {
      await swPage.locator('#dashboard-view').getByText('Courses de la semaine', { exact: true }).first().click();
      await swPage.locator('#items-section').waitFor({ state: 'visible' });
    }
    await swPage.waitForTimeout(800);
    const offlineTitles = await swPage.locator('#items-active li.swipe-item span.cursor-pointer').allTextContents();
    const milkRows = offlineTitles.filter((title) => title.startsWith('Lait demi')).length;
    check('offline: the mirror holds no duplicate after a reload', milkRows === 1 && offlineTitles.includes('Beurre doux'), offlineTitles.join(' | '));

    await swContext.setOffline(false);
    await swPage.evaluate(() => fetch('/api/v1/me'));
    await swPage.waitForTimeout(2500);
    const synced = await itemsOf(swPage, list.id);
    const milk = synced.filter((i) => i.title.startsWith('Lait demi'));
    check('back online: the offline additions are synced without duplicates',
      milk.length === 1 && milk[0].quantity === before.quantity + 1 && synced.filter((i) => i.title === 'Beurre doux').length === 1,
      JSON.stringify(synced.map((i) => [i.title, i.quantity])));
    await swContext.close();

    check('no page errors', errors.length === 0, errors.join(' | '));
    const expected404 = [`/api/v1/houses/${house.id}/products/barcode/${NUTELLA}`, `/api/v1/houses/${house.id}/products/barcode/8076800195057`];
    const unexpected404 = notFound.filter((p) => !expected404.includes(p));
    check('the only 404s are the first lookups of the unknown barcodes', notFound.length === 2 && unexpected404.length === 0, notFound.join(', '));
    if (process.env.KEEP_SCREENSHOT) {
      await page.screenshot({ path: process.env.KEEP_SCREENSHOT });
      console.log(`screenshot: ${pathToFileURL(process.env.KEEP_SCREENSHOT)}`);
    }
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
