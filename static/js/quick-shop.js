'use strict';

// Quick shopping on "Courses" (groceries) lists — the frontend half of
// internal/handlers/products.go:
//
//   - adding a product already on the list never duplicates it: the
//     existing item comes back unchecked, its quantity adjusted
//     (addQuickShopItem; the server applies the same rule to every POST, this
//     is what keeps the offline mirror free of duplicates too);
//   - store aisles: the "Par rayon" sort groups the list in shop order
//     (buildAisleHeader, used by list_view.js's renderItems), and both item
//     forms carry an aisle select (fillAisleSelects);
//   - the House's product catalog (House members only — a share recipient
//     never reads it, see the Go side): autocomplete under the quick-add
//     field, the ⭐ panel of favorites and "time to buy it again" products,
//     the ★ toggle in an item's actions sheet;
//   - the 📷 scanner (scanner.js), then a barcode lookup.
//
// Shares app.js/list_view.js's globals (state, listEls, apiRequest, t,
// renderItems, mergeServerItem, ...), like every page script.

const QUICK_SHOP_LIST_TYPES = ['groceries'];

// internal/catalog.Aisles, in the same (shop walk) order.
const QUICK_SHOP_AISLES = [
  'produce', 'bakery', 'meat_fish', 'dairy', 'pantry', 'breakfast', 'drinks', 'baby', 'pets', 'household', 'hygiene',
  'stationery', 'clothing', 'electronics', 'diy', 'frozen', 'other',
];
const AISLE_EMOJI = {
  produce: '🥕', bakery: '🥖', meat_fish: '🥩', dairy: '🧀', pantry: '🥫', breakfast: '🍪', drinks: '🥤',
  baby: '🍼', pets: '🐾', household: '🧽', hygiene: '🧴',
  stationery: '✏️', clothing: '👕', electronics: '🔌', diy: '🔧', frozen: '🧊', other: '🛒',
};
const MAX_ITEM_QUANTITY = 100000; // internal/handlers.maxItemQuantity
const AUTOCOMPLETE_DEBOUNCE_MS = 120;
const AUTOCOMPLETE_LIMIT = 8;

const quickShopEls = {
  panelButton: document.getElementById('quick-shop-panel-button'),
  scanButton: document.getElementById('quick-shop-scan-button'),
  suggestions: document.getElementById('item-title-suggestions'),
  scanChip: document.getElementById('quick-add-scan-chip'),
  scanChipLabel: document.getElementById('quick-add-scan-chip-label'),
  scanChipClear: document.getElementById('quick-add-scan-chip-clear'),
  itemAisle: document.getElementById('item-aisle'),
  editItemAisle: document.getElementById('edit-item-aisle'),
  sheet: document.getElementById('quick-shop-sheet'),
  closeSheet: document.getElementById('close-quick-shop-sheet-button'),
  sheetStatus: document.getElementById('quick-shop-sheet-status'),
  favorites: document.getElementById('quick-shop-favorites'),
  favoritesEmpty: document.getElementById('quick-shop-favorites-empty'),
  recurring: document.getElementById('quick-shop-recurring'),
  recurringEmpty: document.getElementById('quick-shop-recurring-empty'),
  sections: document.getElementById('quick-shop-sections'),
  membersOnly: document.getElementById('quick-shop-members-only'),
  actionsAisleButton: document.getElementById('item-actions-aisle-button'),
  actionsAisleIcon: document.getElementById('item-actions-aisle-icon'),
  actionsAisleValue: document.getElementById('item-actions-aisle-value'),
  aislePicker: document.getElementById('aisle-picker-sheet'),
  aislePickerItem: document.getElementById('aisle-picker-item'),
  aislePickerOptions: document.getElementById('aisle-picker-options'),
  closeAislePicker: document.getElementById('close-aisle-picker-sheet-button'),
  actionsFavoriteButton: document.getElementById('item-actions-favorite-button'),
  actionsFavoriteIcon: document.getElementById('item-actions-favorite-icon'),
  actionsFavoriteLabel: document.getElementById('item-actions-favorite-label'),
};

const quickShop = {
  // The House whose catalog data below belongs to, and the list it was
  // last loaded for.
  houseId: null,
  listId: null,
  favorites: [],
  favoriteKeys: new Set(),
  recurring: [],
  // A scanned barcode waiting for the quick-add field to be submitted:
  // { barcode, aisleHint, nutrition } (see prefillFromScan).
  pendingScan: null,
  autocompleteTimer: null,
  autocompleteRequest: 0,
  autocompleteOptions: [],
  activeOption: -1,
};

// JS port of internal/catalog.Key: two names are the same product when
// these agree. A mismatch with the Go version is harmless (the server's
// merge decides), only less smooth.
const PRODUCT_KEY_FOLD = { 'œ': 'oe', 'æ': 'ae', 'ß': 'ss', 'ø': 'o' };
function productKey(name) {
  return String(name || '')
    .toLowerCase()
    .replace(/[œæßø]/g, (c) => PRODUCT_KEY_FOLD[c])
    .normalize('NFD')
    .replace(/\p{M}/gu, '')
    .replace(/[^\p{L}\p{N}]+/gu, ' ')
    .trim();
}

function isQuickShopList(list) {
  return Boolean(list) && QUICK_SHOP_LIST_TYPES.includes(list.type);
}

// The catalog (autocomplete, favorites, suggestions, barcodes) is the
// House's: only its members read it.
function canUseCatalog(list) {
  return isQuickShopList(list) && (state.houses || []).some((house) => house.id === list.house_id);
}

function aisleLabel(aisle) {
  return t(`items.aisles.${aisle}`);
}

// The group an item is shown under in "Par rayon": its aisle, or "other"
// when it has none (or one this frontend doesn't know).
function aisleGroupKey(item) {
  return QUICK_SHOP_AISLES.includes(item.aisle) ? item.aisle : 'other';
}

function aisleRank(item) {
  return QUICK_SHOP_AISLES.indexOf(aisleGroupKey(item));
}

// The "Par rayon" order (applySortMode's 'aisle' case): shop order, then
// urgent items first within an aisle, otherwise the list's own order.
function compareByAisle(a, b) {
  return aisleRank(a) - aisleRank(b) || (b.is_urgent ? 1 : 0) - (a.is_urgent ? 1 : 0);
}

// The heading row renderItems puts above each aisle's items in "Par rayon".
function buildAisleHeader(aisle, count) {
  const li = document.createElement('li');
  li.className = 'aisle-header flex items-center gap-2 px-1 pt-3 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400';
  li.setAttribute('role', 'presentation');
  const emoji = document.createElement('span');
  emoji.setAttribute('aria-hidden', 'true');
  emoji.textContent = AISLE_EMOJI[aisle] || AISLE_EMOJI.other;
  const label = document.createElement('span');
  label.textContent = aisleLabel(aisle);
  const badge = document.createElement('span');
  badge.className = 'rounded-full bg-slate-200 dark:bg-slate-700 px-2 py-0.5 text-[11px] font-semibold normal-case tracking-normal text-slate-600 dark:text-slate-300';
  badge.textContent = String(count);
  li.append(emoji, label, badge);
  return li;
}

// The small "🥕 Fruits & légumes" tag under a Courses item's title
// (buildItemRow), so where it is in the shop shows in every sort order — the
// "Par rayon" grouping says it with its headings instead, so renderItems
// leaves the tag out there. A button opening the aisle picker, except in
// multi-select mode (interactive: false). Nothing for an item without an aisle.
function buildAisleBadge(item, { interactive = true } = {}) {
  if (!QUICK_SHOP_AISLES.includes(item.aisle)) return null;
  const badge = document.createElement(interactive ? 'button' : 'span');
  if (interactive) badge.type = 'button';
  badge.className = `item-aisle-badge inline-flex min-h-[22px] max-w-full items-center gap-1 self-start rounded-full bg-slate-200/80 px-2 text-[11px] font-medium leading-4 text-slate-600 dark:bg-slate-700/80 dark:text-slate-300${
    interactive ? ' hover:bg-slate-300 dark:hover:bg-slate-600' : ''}${item.done ? ' opacity-60' : ''}`;
  const label = aisleLabel(item.aisle);
  badge.setAttribute('aria-label', t(interactive ? 'quickShop.aisleBadgeAriaLabel' : 'quickShop.aisleBadgeLabel', { aisle: label }));
  const emoji = document.createElement('span');
  emoji.setAttribute('aria-hidden', 'true');
  emoji.textContent = AISLE_EMOJI[item.aisle];
  const text = document.createElement('span');
  text.className = 'truncate';
  text.textContent = label;
  badge.append(emoji, text);
  if (interactive) badge.addEventListener('click', () => openAislePicker(item));
  return badge;
}

// ---------------------------------------------------------------------------
// Aisle picker (#aisle-picker-sheet): fixing an aisle the guess got wrong
// ---------------------------------------------------------------------------

// The item the picker is open for, or null.
let aislePickerItem = null;

function buildAisleOption(aisle, current) {
  const button = document.createElement('button');
  button.type = 'button';
  button.setAttribute('role', 'radio');
  const checked = (aisle || null) === (current || null);
  button.setAttribute('aria-checked', String(checked));
  button.className = 'flex min-h-[48px] min-w-0 items-center gap-2 rounded-xl border px-3 py-2 text-left text-sm font-medium ' +
    'border-slate-200 bg-white text-slate-900 hover:bg-slate-200 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100 dark:hover:bg-slate-700 ' +
    'aria-checked:border-sky-500 aria-checked:bg-sky-500/10 aria-checked:text-sky-700 dark:aria-checked:text-sky-300';
  const emoji = document.createElement('span');
  emoji.setAttribute('aria-hidden', 'true');
  emoji.textContent = aisle ? AISLE_EMOJI[aisle] : '∅';
  // Wraps rather than truncates: "Boucherie & poissonnerie" doesn't fit
  // half a phone's width.
  const text = document.createElement('span');
  text.className = 'min-w-0 flex-1 break-words leading-tight';
  text.textContent = aisle ? aisleLabel(aisle) : t('items.aisleNone');
  button.append(emoji, text);
  if (checked) {
    const check = document.createElement('span');
    check.setAttribute('aria-hidden', 'true');
    check.className = 'shrink-0 text-sky-500 dark:text-sky-400';
    check.textContent = '✓';
    button.appendChild(check);
  }
  button.addEventListener('click', () => {
    const item = aislePickerItem;
    closeAislePicker();
    if (item) setItemAisle(item, aisle);
  });
  return button;
}

function openAislePicker(item) {
  aislePickerItem = item;
  quickShopEls.aislePickerItem.textContent = item.title;
  const current = QUICK_SHOP_AISLES.includes(item.aisle) ? item.aisle : null;
  quickShopEls.aislePickerOptions.replaceChildren(
    ...QUICK_SHOP_AISLES.map((aisle) => buildAisleOption(aisle, current)),
    buildAisleOption('', current),
  );
  quickShopEls.aislePicker.hidden = false;
  TrakkaScrollLock.lock();
  const selected = quickShopEls.aislePickerOptions.querySelector('[aria-checked="true"]');
  (selected || quickShopEls.aislePickerOptions.firstElementChild).focus();
}

function closeAislePicker() {
  aislePickerItem = null;
  quickShopEls.aislePicker.hidden = true;
  TrakkaScrollLock.unlock();
}

// Files item under aisle ("" for none): optimistic, rolled back if the
// server refuses. On a Courses list the server also makes it the House's
// usual aisle for the product (learnProductAisle). Offline, sw.js queues it.
async function setItemAisle(item, aisle) {
  const next = aisle || null;
  if ((item.aisle || null) === next) return;
  if (typeof item.id === 'string' && item.id.startsWith('local-')) return; // not created yet
  hideError();
  const previous = item.aisle || null;
  item.aisle = next;
  renderItems();
  TrakkaToast.success(next
    ? t('quickShop.aisleChanged', { name: item.title, aisle: aisleLabel(next) })
    : t('quickShop.aisleCleared', { name: item.title }));
  try {
    const updated = await apiRequest(`/items/${item.id}`, { method: 'PATCH', body: JSON.stringify({ aisle: next || '' }) });
    mergeServerItem(item, updated);
  } catch (err) {
    item.aisle = previous;
    if (!isNetworkError(err)) showError(err.message);
  } finally {
    renderItems();
  }
  await refreshPendingBadge();
}

quickShopEls.closeAislePicker.addEventListener('click', closeAislePicker);
quickShopEls.aislePicker.addEventListener('click', (event) => {
  if (event.target === quickShopEls.aislePicker) closeAislePicker();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !quickShopEls.aislePicker.hidden) closeAislePicker();
});
// Arrow keys move between the options, like any radiogroup.
quickShopEls.aislePickerOptions.addEventListener('keydown', (event) => {
  const options = [...quickShopEls.aislePickerOptions.children];
  const index = options.indexOf(document.activeElement);
  if (index < 0) return;
  const step = { ArrowDown: 2, ArrowUp: -2, ArrowRight: 1, ArrowLeft: -1 }[event.key];
  if (!step) return;
  event.preventDefault();
  const next = options[Math.min(options.length - 1, Math.max(0, index + step))];
  next.focus();
});

// Fills an aisle <select> with every aisle, after a first option whose
// label is emptyLabelKey (value ""). Rebuilt on a language change.
function fillAisleSelect(select, emptyLabelKey) {
  if (!select) return;
  const value = select.value;
  const empty = document.createElement('option');
  empty.value = '';
  empty.textContent = t(emptyLabelKey);
  const options = [empty];
  for (const aisle of QUICK_SHOP_AISLES) {
    const option = document.createElement('option');
    option.value = aisle;
    option.textContent = `${AISLE_EMOJI[aisle]} ${aisleLabel(aisle)}`;
    options.push(option);
  }
  select.replaceChildren(...options);
  select.value = value;
}

function fillAisleSelects() {
  fillAisleSelect(quickShopEls.itemAisle, 'items.aisleAuto');
  fillAisleSelect(quickShopEls.editItemAisle, 'items.aisleNone');
}

// ---------------------------------------------------------------------------
// Catalog data: favorites and regular purchases, per House
// ---------------------------------------------------------------------------

function setFavorites(favorites) {
  quickShop.favorites = favorites;
  quickShop.favoriteKeys = new Set(favorites.map((p) => productKey(p.name)));
}

function isFavoriteTitle(title) {
  return quickShop.favoriteKeys.has(productKey(title));
}

// Loads the current list's House favorites and suggestions
// (GET /houses/{id}/products/suggestions). Best effort: offline, or for a
// share recipient, the panel just has less to show.
async function loadQuickShopData(list) {
  if (!canUseCatalog(list)) return;
  const houseId = list.house_id;
  const listParam = typeof list.id === 'number' ? `?list_id=${list.id}` : '';
  let data;
  try {
    data = await apiRequest(`/houses/${houseId}/products/suggestions${listParam}`);
  } catch {
    return;
  }
  if (!state.currentList || state.currentList.house_id !== houseId) return;
  const favoritesChanged = quickShop.houseId !== houseId ||
    quickShop.favorites.map((p) => p.id).join() !== (data.favorites || []).map((p) => p.id).join();
  quickShop.houseId = houseId;
  setFavorites(data.favorites || []);
  quickShop.recurring = data.recurring || [];
  if (favoritesChanged) renderItems();
  if (!quickShopEls.sheet.hidden) renderQuickShopPanel();
}

// Called by renderItems: shows the quick-shop controls where they apply,
// and loads the House's catalog data the first time a list of it opens.
function refreshQuickShopControls(list) {
  const catalog = canUseCatalog(list);
  quickShopEls.panelButton.hidden = !catalog;
  quickShopEls.scanButton.hidden = !catalog;
  if (!isQuickShopList(list)) closeAutocomplete();
  if (!list || quickShop.listId !== list.id) {
    quickShop.listId = list ? list.id : null;
    clearPendingScan();
    if (catalog) {
      if (quickShop.houseId !== list.house_id) {
        quickShop.houseId = null;
        setFavorites([]);
        quickShop.recurring = [];
      }
      loadQuickShopData(list);
    }
  }
}

// The small ★ shown after a favorite's title on a Courses list (buildItemRow).
function buildFavoriteStar(item) {
  if (!canUseCatalog(state.currentList) || !isFavoriteTitle(item.title)) return null;
  const star = document.createElement('span');
  star.className = 'shrink-0 text-sm text-amber-500';
  star.textContent = '★';
  star.title = t('quickShop.favoriteAriaLabel');
  star.setAttribute('role', 'img');
  star.setAttribute('aria-label', t('quickShop.favoriteAriaLabel'));
  return star;
}

// Stars or unstars a product by name (POST /houses/{id}/products, which
// creates it if the catalog doesn't know it yet). Optimistic; offline, the
// service worker queues the request and the star stays.
async function setProductFavorite(name, favorite) {
  const list = state.currentList;
  if (!canUseCatalog(list)) return;
  const key = productKey(name);
  const previous = quickShop.favorites;
  const placeholder = { id: `local-${key}`, name, is_favorite: true };
  setFavorites(favorite
    ? [...previous.filter((p) => productKey(p.name) !== key), placeholder].sort((a, b) => a.name.localeCompare(b.name))
    : previous.filter((p) => productKey(p.name) !== key));
  renderItems();
  if (!quickShopEls.sheet.hidden) renderQuickShopPanel();
  try {
    const product = await apiRequest(`/houses/${list.house_id}/products`, {
      method: 'POST',
      body: JSON.stringify({ name, is_favorite: favorite }),
    });
    if (favorite && product && product.id !== undefined) {
      setFavorites(quickShop.favorites.map((p) => (p === placeholder ? product : p)));
    }
  } catch (err) {
    if (isNetworkError(err)) return;
    setFavorites(previous);
    showError(err.message);
    renderItems();
    if (!quickShopEls.sheet.hidden) renderQuickShopPanel();
  }
}

// ---------------------------------------------------------------------------
// Adding without duplicates
// ---------------------------------------------------------------------------

// The item of items that is the same product as title, as the server picks
// it (internal/handlers.findDuplicateItem): an active one over a done one,
// then the most recently updated.
function findDuplicateItem(items, title) {
  const key = productKey(title);
  if (!key) return null;
  let best = null;
  for (const item of items || []) {
    if (item.pendingDelete || productKey(item.title) !== key) continue;
    if (!best || (best.done && !item.done) || (best.done === item.done && (item.updated_at || '') > (best.updated_at || ''))) {
      best = item;
    }
  }
  return best;
}

// internal/handlers.mergedQuantity: still on the list, quantities add up;
// already bought, the new quantity replaces the old one.
function mergedQuantity(existing, requested) {
  const wanted = requested > 0 ? requested : 1;
  if (existing.done) return wanted;
  return Math.min((existing.quantity || 1) + wanted, MAX_ITEM_QUANTITY);
}

// Adds title to the current Courses list: a product already there is
// brought back / topped up (a PATCH of that item — which the service worker
// applies to its mirror offline, unlike a second create), anything else is
// created. Resolves once the request settles, with 'merged' or 'created'.
// A merge is announced by a toast unless quiet (the ⭐ panel shows it on
// the product's own chip instead).
// `price` (an estimate, or null) and `planned` come from the quick-add
// form's advanced panel; a product already on the list only gets the price
// when it has none, and always comes back "to buy". `nutrition` is the
// {nutri_score?, yuka_score?} a barcode lookup answered (nutritionFromLookup
// in nutrition.js), or null: sent with the item, replacing the item's own.
async function addQuickShopItem({ title, quantity = 1, aisle = '', aisleHint = '', barcode = '', urgent = false, quiet = false, price = null, planned = false, nutrition = null }) {
  const list = state.currentList;
  if (!list) return null;
  const existing = findDuplicateItem(list.items, title);
  let outcome;
  // A row still being created (local-*) has no id to PATCH yet: create, and
  // let the server merge the two.
  if (existing && !(typeof existing.id === 'string' && existing.id.startsWith('local-'))) {
    // A scanned barcode only gets filed under the product by a POST (the
    // server's merge does it): online, the merge goes that way. Offline it
    // stays a PATCH — a queued POST would put a duplicate in the mirror —
    // and the code is filed the next time the product is added.
    const filing = barcode && navigator.onLine ? { title, barcode, aisleHint, quantity } : null;
    await bringBackItem(existing, quantity, { aisle: aisle || aisleHint, urgent, quiet, filing, price, nutrition });
    outcome = 'merged';
  } else {
    outcome = await createQuickShopItem({ title, quantity, aisle, aisleHint, barcode, urgent, quiet, price, planned, nutrition });
  }
  if (!quickShopEls.sheet.hidden) renderQuickShopPanel();
  return outcome;
}

async function bringBackItem(item, quantity, { aisle, urgent, quiet, filing = null, price = null, nutrition = null }) {
  const wasDone = item.done;
  const previous = {
    done: item.done, quantity: item.quantity, aisle: item.aisle, is_urgent: item.is_urgent, next_due_date: item.next_due_date,
    price: item.price, purchase_status: item.purchase_status, actual_price: item.actual_price,
    nutri_score: item.nutri_score, yuka_score: item.yuka_score,
  };
  // Adding a product is wanting it: back "to buy" (not planned), any price
  // paid for its previous purchase dropped — the server's POST merge does
  // the same (db.ReactivateItem).
  const payload = { done: false, purchase_status: 'to_buy', actual_price: null, quantity: mergedQuantity(item, quantity) };
  if (!item.aisle && aisle) payload.aisle = aisle;
  if (urgent && !item.is_urgent) payload.is_urgent = true;
  if (price !== null && typeof item.price !== 'number') payload.price = price;
  if (nutrition) Object.assign(payload, nutrition);

  item.done = false;
  item.next_due_date = null;
  item.purchase_status = 'to_buy';
  item.actual_price = null;
  item.quantity = payload.quantity;
  if (payload.aisle) item.aisle = payload.aisle;
  if (payload.is_urgent) item.is_urgent = true;
  if ('price' in payload) item.price = payload.price;
  if (nutrition) Object.assign(item, nutrition);
  renderItems();
  if (!quiet) {
    TrakkaToast.success(wasDone
      ? t('quickShop.reactivated', { name: item.title })
      : t('quickShop.quantityUpdated', { name: item.title, count: item.quantity }));
  }

  try {
    if (filing) {
      const body = { list_id: item.list_id, title: filing.title, quantity: filing.quantity, barcode: filing.barcode };
      if (filing.aisleHint) body.aisle_hint = filing.aisleHint;
      if (urgent) body.is_urgent = true;
      if (nutrition) Object.assign(body, nutrition);
      const created = await apiRequest('/items', { method: 'POST', body: JSON.stringify(body) });
      if (created && created.id === item.id) {
        mergeServerItem(item, created);
      } else if (created && typeof created.id === 'number') {
        // The server didn't take it for the same product (productKey and
        // catalog.Key disagreed): it's a new item after all.
        Object.assign(item, previous);
        state.currentList.items = [...(state.currentList.items || []), created];
      }
      // A temp-item-* answer means the connection dropped meanwhile and the
      // service worker queued it: the server merges it when it syncs.
    } else {
      const updated = await apiRequest(`/items/${item.id}`, { method: 'PATCH', body: JSON.stringify(payload) });
      mergeServerItem(item, updated);
    }
  } catch (err) {
    Object.assign(item, previous);
    if (!isNetworkError(err)) showError(err.message);
  } finally {
    renderItems();
  }
}

async function createQuickShopItem({ title, quantity, aisle, aisleHint, barcode, urgent, quiet, price = null, planned = false, nutrition = null }) {
  const list = state.currentList;
  const known = [...quickShop.favorites, ...quickShop.recurring].find((p) => productKey(p.name) === productKey(title));
  const optimisticItem = {
    id: `local-${Date.now()}-${Math.random().toString(36).slice(2)}`,
    list_id: list.id,
    title,
    quantity: quantity > 0 ? quantity : 1,
    // Shown in the right aisle group straight away when it's already known.
    aisle: aisle || (known && known.aisle) || aisleHint || null,
    is_urgent: urgent,
    done: false,
    purchase_status: planned ? 'planned' : 'to_buy',
    price,
    position: 0,
    labels: [],
    // The scores it's added with, else the catalog's (the server inherits
    // them the same way, see resolveItemNutrition).
    nutri_score: (nutrition && nutrition.nutri_score) || (known && known.nutri_score) || null,
    yuka_score: nutrition && nutrition.yuka_score !== undefined ? nutrition.yuka_score : (known && known.yuka_score) ?? null,
  };
  list.items = [...(list.items || []), optimisticItem];
  renderItems();

  const payload = { list_id: list.id, title, quantity: optimisticItem.quantity };
  if (price !== null) payload.price = price;
  if (planned) payload.purchase_status = 'planned';
  if (aisle) payload.aisle = aisle;
  if (aisleHint) payload.aisle_hint = aisleHint;
  if (barcode) payload.barcode = barcode;
  if (urgent) payload.is_urgent = true;
  if (nutrition) Object.assign(payload, nutrition);
  let outcome = 'created';
  try {
    const created = await apiRequest('/items', { method: 'POST', body: JSON.stringify(payload) });
    let row = optimisticItem;
    if (created && created.merged) {
      outcome = 'merged';
      // The server found the product already on the list (another device
      // added it, or this view hadn't caught up): that item is the one.
      const twin = (list.items || []).find((it) => it !== optimisticItem && it.id === created.id);
      if (twin) {
        list.items = list.items.filter((it) => it !== optimisticItem);
        row = twin;
      }
      mergeServerItem(row, created);
      if (!quiet) TrakkaToast.success(t('quickShop.quantityUpdated', { name: created.title, count: created.quantity }));
    } else {
      mergeServerItem(optimisticItem, created);
    }
    if (created && created.enrichment_status === 'pending') {
      delete row.enrichment_status;
      scheduleEnrichmentRefresh(row);
    }
  } catch (err) {
    list.items = list.items.filter((it) => it !== optimisticItem);
    if (!isNetworkError(err)) showError(err.message);
    outcome = null;
  } finally {
    renderItems();
  }
  return outcome;
}

// A product typed by hand that the server is looking up in Open Food Facts'
// text search (enrichment_status "pending", internal/handlers/
// product_enrichment.go — its aisle when neither the House catalog nor the
// keyword guess knew it, its Nutri-Score for a user who shows the scores):
// the item is read again a little later, at most twice (the search takes
// 2 to 15 s), and only the fields the search fills — aisle, nutri_score,
// image_url — are taken, where the row still has none, so nothing typed
// meanwhile is lost. Never blocks anything: the row is already on screen.
const ENRICHMENT_REFRESH_DELAYS = [5000, 12000];
function scheduleEnrichmentRefresh(item, attempt = 0) {
  if (attempt >= ENRICHMENT_REFRESH_DELAYS.length) return;
  setTimeout(async () => {
    const onScreen = () => typeof item.id === 'number' && state.currentList && (state.currentList.items || []).includes(item);
    if (!onScreen()) return;
    let fresh;
    try {
      fresh = await apiRequest(`/items/${item.id}`);
    } catch {
      return; // best effort: deleted meanwhile, or offline
    }
    if (!onScreen() || !fresh) return;
    let changed = false;
    for (const field of ['aisle', 'nutri_score', 'image_url']) {
      if (fresh[field] && !item[field]) {
        item[field] = fresh[field];
        changed = true;
      }
    }
    if (changed) renderItems();
    // nutritionScoresEnabledAnywhere is defined in nutrition.js.
    if (!item.aisle || (nutritionScoresEnabledAnywhere() && !item.nutri_score)) scheduleEnrichmentRefresh(item, attempt + 1);
  }, ENRICHMENT_REFRESH_DELAYS[attempt]);
}

// ---------------------------------------------------------------------------
// The quick-add field: submit, autocomplete, scanned barcode
// ---------------------------------------------------------------------------

// Called by list_view.js's quick-add submit on a Courses list, after it
// reset the form: the scanned barcode (if any) goes with this addition.
function takePendingScan() {
  const pending = quickShop.pendingScan;
  clearPendingScan();
  return pending || { barcode: '', aisleHint: '', nutrition: null };
}

function clearPendingScan() {
  quickShop.pendingScan = null;
  quickShopEls.scanChip.hidden = true;
}

function prefillFromScan(barcode, name, aisleHint, nutrition = null) {
  quickShop.pendingScan = { barcode, aisleHint: aisleHint || '', nutrition };
  quickShopEls.scanChipLabel.textContent = t('scanner.pendingChip', { code: barcode });
  quickShopEls.scanChip.hidden = false;
  listEls.itemTitle.value = name || '';
  listEls.itemTitle.focus();
  if (name) listEls.itemTitle.select();
}

function closeAutocomplete() {
  clearTimeout(quickShop.autocompleteTimer);
  quickShop.autocompleteRequest++;
  quickShop.autocompleteOptions = [];
  quickShop.activeOption = -1;
  quickShopEls.suggestions.hidden = true;
  quickShopEls.suggestions.replaceChildren();
  listEls.itemTitle.setAttribute('aria-expanded', 'false');
  listEls.itemTitle.removeAttribute('aria-activedescendant');
}

// What the current list already says about a product: 'active' (with its
// item), 'done', or null.
function listStateOf(name) {
  const item = findDuplicateItem(state.currentList && state.currentList.items, name);
  if (!item) return null;
  return { item, status: item.done ? 'done' : 'active' };
}

function buildListStateBadge(name) {
  const found = listStateOf(name);
  if (!found) return null;
  const badge = document.createElement('span');
  badge.className = found.status === 'active'
    ? 'shrink-0 rounded-full bg-sky-500/10 px-2 py-0.5 text-[11px] font-semibold text-sky-700 dark:text-sky-300'
    : 'shrink-0 rounded-full bg-slate-200 dark:bg-slate-700 px-2 py-0.5 text-[11px] font-semibold text-slate-600 dark:text-slate-300';
  badge.textContent = found.status === 'active'
    ? t('quickShop.inListQuantity', { count: found.item.quantity || 1 })
    : t('quickShop.checked');
  return badge;
}

async function updateAutocomplete() {
  const list = state.currentList;
  const query = listEls.itemTitle.value.trim();
  const key = productKey(query);
  if (!isQuickShopList(list) || !key) {
    closeAutocomplete();
    return;
  }
  const request = ++quickShop.autocompleteRequest;

  // The list's own items first need no network: they also cover a share
  // recipient (no catalog) and offline use.
  const options = [];
  const seen = new Set();
  const push = (name, aisle, favorite) => {
    const k = productKey(name);
    if (!k || seen.has(k)) return;
    seen.add(k);
    options.push({ name, aisle: aisle || '', favorite });
  };
  let remote = [];
  if (canUseCatalog(list)) {
    try {
      remote = await apiRequest(`/houses/${list.house_id}/products?q=${encodeURIComponent(query)}&limit=${AUTOCOMPLETE_LIMIT}`);
    } catch {
      remote = [];
    }
    if (request !== quickShop.autocompleteRequest) return;
  }
  for (const product of remote || []) push(product.name, product.aisle, product.is_favorite);
  for (const item of list.items || []) {
    if (productKey(item.title).includes(key)) push(item.title, item.aisle, isFavoriteTitle(item.title));
  }
  renderAutocomplete(options.slice(0, AUTOCOMPLETE_LIMIT));
}

function renderAutocomplete(options) {
  quickShop.autocompleteOptions = options;
  quickShop.activeOption = -1;
  listEls.itemTitle.removeAttribute('aria-activedescendant');
  if (options.length === 0) {
    quickShopEls.suggestions.hidden = true;
    quickShopEls.suggestions.replaceChildren();
    listEls.itemTitle.setAttribute('aria-expanded', 'false');
    return;
  }
  const rows = options.map((option, index) => {
    const li = document.createElement('li');
    li.id = `item-title-suggestion-${index}`;
    li.setAttribute('role', 'option');
    li.setAttribute('aria-selected', 'false');
    li.className = 'flex min-h-[44px] cursor-pointer items-center gap-2 rounded-xl px-3 py-2 text-sm text-slate-900 dark:text-slate-100 hover:bg-slate-200 dark:hover:bg-slate-700 aria-selected:bg-sky-500/10';
    const emoji = document.createElement('span');
    emoji.setAttribute('aria-hidden', 'true');
    emoji.textContent = AISLE_EMOJI[option.aisle] || '🛒';
    const name = document.createElement('span');
    name.className = 'min-w-0 flex-1 truncate';
    name.textContent = option.name;
    li.append(emoji, name);
    if (option.favorite) {
      const star = document.createElement('span');
      star.className = 'shrink-0 text-amber-500';
      star.textContent = '★';
      star.setAttribute('aria-label', t('quickShop.favoriteAriaLabel'));
      li.appendChild(star);
    }
    const badge = buildListStateBadge(option.name);
    if (badge) li.appendChild(badge);
    // pointerdown, not click: picking must win over the field's blur.
    li.addEventListener('pointerdown', (event) => {
      event.preventDefault();
      pickAutocompleteOption(index);
    });
    return li;
  });
  quickShopEls.suggestions.replaceChildren(...rows);
  quickShopEls.suggestions.hidden = false;
  listEls.itemTitle.setAttribute('aria-expanded', 'true');
}

function highlightOption(index) {
  const rows = quickShopEls.suggestions.children;
  quickShop.activeOption = index;
  for (let i = 0; i < rows.length; i++) rows[i].setAttribute('aria-selected', String(i === index));
  if (index >= 0 && rows[index]) {
    listEls.itemTitle.setAttribute('aria-activedescendant', rows[index].id);
    rows[index].scrollIntoView({ block: 'nearest' });
  } else {
    listEls.itemTitle.removeAttribute('aria-activedescendant');
  }
}

// One tap adds the suggestion, with whatever quantity the quick-add panel
// holds, and leaves the field empty and focused for the next product.
function pickAutocompleteOption(index) {
  const option = quickShop.autocompleteOptions[index];
  if (!option) return;
  const quantity = Number.parseInt(listEls.itemQuantity.value, 10) || 1;
  const urgent = listEls.itemUrgent.checked;
  const aisle = quickShopEls.itemAisle.value;
  const scan = takePendingScan();
  closeAutocomplete();
  listEls.createItemForm.reset();
  listEls.itemQuantity.value = '1';
  listEls.itemTitle.focus();
  addQuickShopItem({ title: option.name, quantity, urgent, aisle, aisleHint: option.aisle || scan.aisleHint, barcode: scan.barcode, nutrition: scan.nutrition })
    .then(() => refreshPendingBadge());
}

listEls.itemTitle.setAttribute('role', 'combobox');
listEls.itemTitle.setAttribute('aria-autocomplete', 'list');
listEls.itemTitle.setAttribute('aria-controls', 'item-title-suggestions');
listEls.itemTitle.setAttribute('aria-expanded', 'false');
// index.html ships autocomplete="on": Android's Chromium (Chrome and the
// Capacitor WebView) maps "off" to TYPE_TEXT_FLAG_NO_SUGGESTIONS, which hides
// the keyboard's suggestion/autocorrect strip. Only a mouse-driven device — no
// on-screen keyboard to lose — turns it off, so the browser's form-history
// dropdown can't cover the suggestions above. Set once, before any focus: an
// IME restart mid-typing would flicker the keyboard.
if (window.matchMedia('(hover: hover) and (pointer: fine)').matches) {
  listEls.itemTitle.setAttribute('autocomplete', 'off');
}

listEls.itemTitle.addEventListener('input', () => {
  clearTimeout(quickShop.autocompleteTimer);
  if (!isQuickShopList(state.currentList)) return;
  quickShop.autocompleteTimer = setTimeout(updateAutocomplete, AUTOCOMPLETE_DEBOUNCE_MS);
});
listEls.itemTitle.addEventListener('keydown', (event) => {
  if (quickShopEls.suggestions.hidden) return;
  const count = quickShop.autocompleteOptions.length;
  if (event.key === 'ArrowDown') {
    event.preventDefault();
    highlightOption((quickShop.activeOption + 1) % count);
  } else if (event.key === 'ArrowUp') {
    event.preventDefault();
    highlightOption(quickShop.activeOption <= 0 ? count - 1 : quickShop.activeOption - 1);
  } else if (event.key === 'Enter' && quickShop.activeOption >= 0) {
    event.preventDefault();
    pickAutocompleteOption(quickShop.activeOption);
  } else if (event.key === 'Escape') {
    // Only the suggestions: an open sheet's own Escape handling is untouched.
    event.stopPropagation();
    closeAutocomplete();
  }
});
listEls.itemTitle.addEventListener('blur', () => {
  setTimeout(closeAutocomplete, 150);
});
listEls.createItemForm.addEventListener('submit', closeAutocomplete, true);
quickShopEls.scanChipClear.addEventListener('click', () => {
  clearPendingScan();
  listEls.itemTitle.focus();
});

// ---------------------------------------------------------------------------
// The ⭐ panel: favorites and regular purchases, one tap to add
// ---------------------------------------------------------------------------

function buildProductChip(product, { favorite, hint }) {
  const chip = document.createElement('div');
  chip.className = 'quick-shop-chip inline-flex max-w-full items-stretch overflow-hidden rounded-full border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900';

  const add = document.createElement('button');
  add.type = 'button';
  add.className = 'flex min-h-[44px] min-w-0 items-center gap-1.5 py-1.5 pl-3 pr-2 text-left text-sm font-medium text-slate-900 dark:text-slate-100 hover:bg-slate-100 dark:hover:bg-slate-800';
  add.setAttribute('aria-label', t('quickShop.addProductAriaLabel', { name: product.name }));
  const emoji = document.createElement('span');
  emoji.setAttribute('aria-hidden', 'true');
  emoji.textContent = AISLE_EMOJI[product.aisle] || '🛒';
  const text = document.createElement('span');
  text.className = 'flex min-w-0 flex-col leading-tight';
  const name = document.createElement('span');
  name.className = 'truncate';
  name.textContent = product.name;
  text.appendChild(name);
  if (hint) {
    const sub = document.createElement('span');
    sub.className = 'truncate text-[11px] font-normal text-slate-500 dark:text-slate-400';
    sub.textContent = hint;
    text.appendChild(sub);
  }
  add.append(emoji, text);
  const badge = buildListStateBadge(product.name);
  if (badge) add.appendChild(badge);
  add.addEventListener('click', () => {
    addQuickShopItem({ title: product.name, aisleHint: product.aisle || '', quiet: true }).then(() => refreshPendingBadge());
    quickShopEls.sheetStatus.textContent = t('quickShop.added', { name: product.name });
  });

  const star = document.createElement('button');
  star.type = 'button';
  star.className = `flex min-h-[44px] w-10 shrink-0 items-center justify-center border-l border-slate-200 dark:border-slate-700 text-base hover:bg-slate-100 dark:hover:bg-slate-800 ${favorite ? 'text-amber-500' : 'text-slate-400'}`;
  star.textContent = favorite ? '★' : '☆';
  star.setAttribute('aria-pressed', String(favorite));
  star.setAttribute('aria-label', t(favorite ? 'quickShop.removeFavoriteAriaLabel' : 'quickShop.addFavoriteAriaLabel', { name: product.name }));
  star.addEventListener('click', () => setProductFavorite(product.name, !favorite));

  chip.append(add, star);
  return chip;
}

function recurringHint(suggestion) {
  const days = Math.max(1, Math.round(suggestion.avg_interval_days || 0));
  return suggestion.reason === 'due'
    ? t('quickShop.dueHint', { days })
    : t('quickShop.frequentHint', { count: suggestion.purchase_count });
}

function renderQuickShopPanel() {
  const list = state.currentList;
  const catalog = canUseCatalog(list);
  quickShopEls.sections.hidden = !catalog;
  quickShopEls.membersOnly.hidden = catalog;
  if (!catalog) return;

  quickShopEls.favorites.replaceChildren(...quickShop.favorites.map((product) => buildProductChip(product, { favorite: true })));
  quickShopEls.favoritesEmpty.hidden = quickShop.favorites.length > 0;

  const recurring = quickShop.recurring.filter((s) => !isFavoriteTitle(s.name));
  quickShopEls.recurring.replaceChildren(...recurring.map((s) => buildProductChip(s, { favorite: false, hint: recurringHint(s) })));
  quickShopEls.recurringEmpty.hidden = recurring.length > 0;
}

function openQuickShopSheet() {
  const list = state.currentList;
  if (!list) return;
  quickShopEls.sheetStatus.textContent = '';
  renderQuickShopPanel();
  quickShopEls.sheet.hidden = false;
  TrakkaScrollLock.lock();
  loadQuickShopData(list);
}

function closeQuickShopSheet() {
  quickShopEls.sheet.hidden = true;
  TrakkaScrollLock.unlock();
}

quickShopEls.panelButton.addEventListener('click', openQuickShopSheet);
quickShopEls.closeSheet.addEventListener('click', closeQuickShopSheet);
quickShopEls.sheet.addEventListener('click', (event) => {
  if (event.target === quickShopEls.sheet) closeQuickShopSheet();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !quickShopEls.sheet.hidden) closeQuickShopSheet();
});

// ---------------------------------------------------------------------------
// ★ in an item's actions sheet
// ---------------------------------------------------------------------------

// Called by list_view.js's openItemActionsSheet: the "Rayon" row of a
// Courses item, showing its aisle (any list member who can edit the list
// can change it — unlike the catalog features, it's the item's own field).
function updateItemActionsAisleButton(item) {
  const show = isQuickShopList(state.currentList);
  quickShopEls.actionsAisleButton.hidden = !show;
  if (!show) return;
  const known = QUICK_SHOP_AISLES.includes(item.aisle);
  quickShopEls.actionsAisleIcon.textContent = known ? AISLE_EMOJI[item.aisle] : '🛒';
  quickShopEls.actionsAisleValue.textContent = known ? aisleLabel(item.aisle) : t('items.aisleNone');
}

quickShopEls.actionsAisleButton.addEventListener('click', () => {
  const item = itemActionsSheetItem;
  closeItemActionsSheet();
  if (item) openAislePicker(item);
});

// Called by list_view.js's openItemActionsSheet.
function updateItemActionsFavoriteButton(item) {
  const show = canUseCatalog(state.currentList);
  quickShopEls.actionsFavoriteButton.hidden = !show;
  if (!show) return;
  const favorite = isFavoriteTitle(item.title);
  quickShopEls.actionsFavoriteIcon.textContent = favorite ? '★' : '☆';
  quickShopEls.actionsFavoriteLabel.textContent = t(favorite ? 'quickShop.removeFavorite' : 'quickShop.addFavorite');
}

quickShopEls.actionsFavoriteButton.addEventListener('click', () => {
  // itemActionsSheetItem/closeItemActionsSheet live in list_view.js.
  const item = itemActionsSheetItem;
  closeItemActionsSheet();
  if (item) setProductFavorite(item.title, !isFavoriteTitle(item.title));
});

// ---------------------------------------------------------------------------
// 📷 Scan, recognize, add
// ---------------------------------------------------------------------------

// Scans a barcode and asks the House catalog (then Open Food Facts) what
// it is: a product the household already scanned is added right away;
// one found elsewhere is put in the quick-add field to check (its name
// comes from a public database) and confirm; an unknown one leaves the
// field empty for its name — the barcode goes with the addition either way,
// so the catalog recognizes it next time.
async function scanAndAdd() {
  const list = state.currentList;
  if (!canUseCatalog(list)) return;
  const code = await TrakkaScanner.scan();
  if (!code || state.currentList !== list) return;

  let found = null;
  let failure = null;
  try {
    found = await apiRequest(`/houses/${list.house_id}/products/barcode/${encodeURIComponent(code)}`);
  } catch (err) {
    failure = err;
  }
  if (state.currentList !== list) return;
  if (found && found.source === 'house') {
    const outcome = await addQuickShopItem({ title: found.name, aisleHint: found.aisle || '', barcode: code, nutrition: nutritionFromLookup(found) });
    if (outcome === 'created') TrakkaToast.success(t('quickShop.added', { name: found.name }));
    await refreshPendingBadge();
    return;
  }
  if (found) {
    prefillFromScan(code, found.name, found.aisle, nutritionFromLookup(found));
    TrakkaToast.success(t('scanner.foundExternal'), 4000);
    return;
  }
  prefillFromScan(code, '', '');
  TrakkaToast.warning(t(isNetworkError(failure) ? 'scanner.lookupOffline' : 'scanner.unknown'), 6000);
}

quickShopEls.scanButton.addEventListener('click', scanAndAdd);

// The options are translated: built once the dictionary has loaded, and
// again on every language change.
window.TrakkaI18n.ready.then(fillAisleSelects, fillAisleSelects);
document.addEventListener('trakka:lang-changed', () => {
  fillAisleSelects();
  if (!quickShopEls.sheet.hidden) renderQuickShopPanel();
});
