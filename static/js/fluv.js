'use strict';

// "Fluv" section of #user-settings-modal (Intégrations tab): connects the
// user's Fluv, the personal finance app, through GET/PUT/DELETE
// /api/v1/integrations/fluv and POST …/test and …/retry
// (internal/handlers/fluv.go, docs/FLUV.md). Once connected, every item the
// user checks off a purchase list is sent to Fluv as an expense, by the
// server, in the background.
//
// The token is write-only: the server only says whether one is stored, so
// the field stays empty and an empty field on save keeps the stored token.
// There is no address to enter: the server resolves Fluv's host name when
// the connection is saved or tested and locks the connection to those IPs
// (the automatic IP lock, docs/FLUV.md), shown read-only in #fluv-locked-ips.
// The section stays hidden when the server has the integration off (the GET
// answers 404). Disconnecting takes a second click to confirm, like
// api-tokens.js (this app uses no confirm()).
//
// None of these requests is ever queued offline: sw.js lets
// /api/v1/integrations/ go straight to the network. Shares `apiRequest`, `t`
// and `TrakkaToast` with the other page scripts (classic-<script>-tags
// shared scope). refreshFluvSection is called from settings.js's
// openUserSettingsModal.

const fluvEls = {
  section: document.getElementById('fluv-section'),
  status: document.getElementById('fluv-status'),
  endpoint: document.getElementById('fluv-endpoint'),
  lockedIps: document.getElementById('fluv-locked-ips'),
  token: document.getElementById('fluv-token'),
  tokenHint: document.getElementById('fluv-token-hint'),
  enabled: document.getElementById('fluv-enabled'),
  saveButton: document.getElementById('fluv-save-button'),
  testButton: document.getElementById('fluv-test-button'),
  retryButton: document.getElementById('fluv-retry-button'),
  disconnectButton: document.getElementById('fluv-disconnect-button'),
  error: document.getElementById('fluv-error'),
};

const FLUV_CONFIRM_WINDOW_MS = 4000;

// The error codes the server and the sender report (internal/fluv), each
// with a translated message; any other code — one of Fluv's own — falls back
// on fluvErrorText's generic one.
const FLUV_ERROR_CODES = [
  'required', 'invalid_url', 'not_fluv_endpoint', 'unresolvable', 'address_not_allowed',
  'https_required', 'invalid_token', 'unauthenticated', 'not_found', 'rate_limited',
  'server_error', 'redirect_refused', 'rejected', 'unreachable', 'timeout', 'tls_error', 'blocked_address',
  'token_unreadable',
];

// What GET /api/v1/integrations/fluv last said; null while unknown.
let fluvSettings = null;
let fluvDisconnectArmed = false;
let fluvDisconnectTimer = null;

function formatFluvDate(iso) {
  const locale = window.TrakkaI18n && TrakkaI18n.getLang() === 'en' ? 'en-US' : 'fr-FR';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso));
}

function fluvErrorText(code) {
  if (FLUV_ERROR_CODES.includes(code)) return t(`modals.userSettings.fluv.errors.${code}`);
  return t('modals.userSettings.fluv.errors.other', { code });
}

function disarmFluvDisconnect() {
  fluvDisconnectArmed = false;
  if (fluvDisconnectTimer) clearTimeout(fluvDisconnectTimer);
  fluvDisconnectTimer = null;
}

function renderFluvSection() {
  const s = fluvSettings;
  if (!s) return;
  const lines = [];
  if (!s.configured) {
    lines.push(t('modals.userSettings.fluv.notConnected'));
  } else {
    lines.push(t(s.enabled ? 'modals.userSettings.fluv.connected' : 'modals.userSettings.fluv.paused'));
    if (s.last_error_code && s.last_error_at) {
      lines.push(t('modals.userSettings.fluv.lastError', { when: formatFluvDate(s.last_error_at), reason: fluvErrorText(s.last_error_code) }));
    } else if (s.last_success_at) {
      lines.push(t('modals.userSettings.fluv.lastSuccess', { when: formatFluvDate(s.last_success_at) }));
    }
    if (s.pending > 0) lines.push(t('modals.userSettings.fluv.pending', { count: s.pending }));
    if (s.failed > 0) lines.push(t('modals.userSettings.fluv.failed', { count: s.failed }));
  }
  fluvEls.status.textContent = lines.join(' · ');
  const locked = s.pinned_ips || [];
  fluvEls.lockedIps.hidden = !s.configured || locked.length === 0;
  fluvEls.lockedIps.textContent = t('modals.userSettings.fluv.lockedTo', { ips: locked.join(', ') });
  fluvEls.tokenHint.textContent = t(s.token_set ? 'modals.userSettings.fluv.tokenKept' : 'modals.userSettings.fluv.tokenHint');
  fluvEls.testButton.hidden = !s.configured;
  fluvEls.retryButton.hidden = !(s.configured && s.failed > 0);
  fluvEls.disconnectButton.hidden = !s.configured;
  fluvEls.disconnectButton.textContent = t(fluvDisconnectArmed ? 'modals.userSettings.fluv.confirmDisconnect' : 'modals.userSettings.fluv.disconnect');
  fluvEls.disconnectButton.classList.toggle('text-rose-600', fluvDisconnectArmed);
  fluvEls.disconnectButton.classList.toggle('dark:text-rose-400', fluvDisconnectArmed);
}

// Fills the fields from fluvSettings — on open and after a save, never while
// the user is typing.
function fillFluvFields() {
  const s = fluvSettings;
  fluvEls.endpoint.value = s.endpoint_url || '';
  fluvEls.token.value = '';
  fluvEls.enabled.checked = !s.configured || s.enabled;
}

function showFluvError(err) {
  let message = t('modals.userSettings.fluv.networkRequired');
  if (err && err.code && FLUV_ERROR_CODES.includes(err.code)) message = fluvErrorText(err.code);
  else if (err && !err.isNetworkError && err.message) message = err.message;
  fluvEls.error.textContent = message;
  fluvEls.error.hidden = false;
}

async function refreshFluvSection() {
  disarmFluvDisconnect();
  fluvEls.error.hidden = true;
  fluvSettings = null;
  try {
    fluvSettings = await apiRequest('/integrations/fluv');
  } catch (err) {
    // 404: the operator turned the integration off. Offline: there is
    // nothing useful to show without the server either.
    fluvEls.section.hidden = !(err && err.isNetworkError);
    if (err && err.isNetworkError) fluvEls.status.textContent = t('modals.userSettings.fluv.networkRequired');
    return;
  }
  fluvEls.section.hidden = false;
  fillFluvFields();
  renderFluvSection();
}

async function saveFluvSettings() {
  disarmFluvDisconnect();
  fluvEls.error.hidden = true;
  const payload = {
    endpoint_url: fluvEls.endpoint.value.trim(),
    token: fluvEls.token.value.trim(),
    enabled: fluvEls.enabled.checked,
  };
  fluvEls.saveButton.disabled = true;
  try {
    fluvSettings = await apiRequest('/integrations/fluv', { method: 'PUT', body: JSON.stringify(payload) });
    fluvMetadataCache = null;
    fillFluvFields();
    TrakkaToast.success(t('modals.userSettings.fluv.saved'));
  } catch (err) {
    showFluvError(err);
  } finally {
    fluvEls.saveButton.disabled = false;
    renderFluvSection();
  }
}

async function testFluvConnection() {
  disarmFluvDisconnect();
  fluvEls.error.hidden = true;
  fluvEls.testButton.disabled = true;
  try {
    const result = await apiRequest('/integrations/fluv/test', { method: 'POST' });
    if (result.ok && result.relocked) {
      TrakkaToast.success(t('modals.userSettings.fluv.testRelocked', { ips: (result.pinned_ips || []).join(', ') }));
    } else if (result.ok) TrakkaToast.success(t('modals.userSettings.fluv.testOk'));
    else {
      fluvEls.error.textContent = t('modals.userSettings.fluv.testFailed', { reason: fluvErrorText(result.code) });
      fluvEls.error.hidden = false;
    }
    fluvSettings = await apiRequest('/integrations/fluv');
  } catch (err) {
    showFluvError(err);
  } finally {
    fluvEls.testButton.disabled = false;
    renderFluvSection();
  }
}

async function retryFluvPurchases() {
  disarmFluvDisconnect();
  fluvEls.error.hidden = true;
  fluvEls.retryButton.disabled = true;
  try {
    const result = await apiRequest('/integrations/fluv/retry', { method: 'POST' });
    TrakkaToast.success(t('modals.userSettings.fluv.retried', { count: result.retried }));
    fluvSettings = await apiRequest('/integrations/fluv');
  } catch (err) {
    showFluvError(err);
  } finally {
    fluvEls.retryButton.disabled = false;
    renderFluvSection();
  }
}

async function disconnectFluv() {
  if (!fluvDisconnectArmed) {
    fluvDisconnectArmed = true;
    fluvDisconnectTimer = setTimeout(() => {
      disarmFluvDisconnect();
      renderFluvSection();
    }, FLUV_CONFIRM_WINDOW_MS);
    renderFluvSection();
    return;
  }
  disarmFluvDisconnect();
  fluvEls.error.hidden = true;
  fluvEls.disconnectButton.disabled = true;
  try {
    await apiRequest('/integrations/fluv', { method: 'DELETE' });
    fluvMetadataCache = null;
    fluvSettings = await apiRequest('/integrations/fluv');
    fillFluvFields();
    TrakkaToast.success(t('modals.userSettings.fluv.disconnected'));
  } catch (err) {
    showFluvError(err);
  } finally {
    fluvEls.disconnectButton.disabled = false;
    renderFluvSection();
  }
}

fluvEls.saveButton.addEventListener('click', saveFluvSettings);
fluvEls.testButton.addEventListener('click', testFluvConnection);
fluvEls.retryButton.addEventListener('click', retryFluvPurchases);
fluvEls.disconnectButton.addEventListener('click', disconnectFluv);
// The fields sit inside #user-settings-form: Enter must save the
// connection, not submit (and close) the settings.
for (const input of [fluvEls.endpoint, fluvEls.token]) {
  input.addEventListener('keydown', (event) => {
    if (event.key !== 'Enter' || event.isComposing) return;
    event.preventDefault();
    saveFluvSettings();
  });
}

// The status line and button labels are built from script, not data-i18n.
document.addEventListener('trakka:lang-changed', () => {
  if (fluvSettings) renderFluvSection();
});

// ---------------------------------------------------------------------------
// Menus of the user's Fluv accounts and categories, read through GET
// /api/v1/integrations/fluv/metadata — the server asks their Fluv with the
// stored token (the browser never talks to Fluv) and answers ok false when
// it can't (a Fluv older than that endpoint, Fluv down). When it answers
// ok, an id field gives way to a <select> (showFluvIdMenu); otherwise, or
// offline, the text field stays (showFluvIdText). The <select> writes its
// value back into the text field, which stays what is validated and saved.
// ---------------------------------------------------------------------------

// A successful answer is reused this long, per language (category names
// come in it); the server caches too.
const FLUV_METADATA_TTL_MS = 60000;
let fluvMetadataCache = null; // {lang, at, promise}

// The metadata, or null when there is none to offer.
function fetchFluvMetadata() {
  const lang = window.TrakkaI18n && TrakkaI18n.getLang() === 'en' ? 'en' : 'fr';
  const now = Date.now();
  if (fluvMetadataCache && fluvMetadataCache.lang === lang && now - fluvMetadataCache.at < FLUV_METADATA_TTL_MS) {
    return fluvMetadataCache.promise;
  }
  const promise = apiRequest(`/integrations/fluv/metadata?lang=${lang}`)
    .then((meta) => (meta && meta.ok ? meta : null))
    .catch(() => null)
    .then((meta) => {
      if (!meta && fluvMetadataCache?.promise === promise) fluvMetadataCache = null;
      return meta;
    });
  fluvMetadataCache = { lang, at: now, promise };
  return promise;
}

// An id field and the menu that may replace it; the label's text follows.
function fluvIdPicker(inputId, selectId, textKey, menuKey) {
  const picker = {
    input: document.getElementById(inputId),
    select: document.getElementById(selectId),
    label: document.querySelector(`label[for="${inputId}"]`),
    textKey,
    menuKey,
  };
  picker.select.addEventListener('change', () => { picker.input.value = picker.select.value; });
  return picker;
}

function setFluvPickerLabel(picker, forId, key) {
  picker.label.htmlFor = forId;
  picker.label.setAttribute('data-i18n', key); // kept on a language change
  picker.label.textContent = t(key);
}

function showFluvIdText(picker) {
  picker.select.hidden = true;
  picker.input.hidden = false;
  setFluvPickerLabel(picker, picker.input.id, picker.textKey);
}

// Fills the menu with options ({value, text}, the first "": Fluv decides)
// and shows it instead of the text field, on the field's value. A value
// the menu doesn't list — an account deleted in Fluv since — stays, as an
// option of its own, so that opening and saving changes nothing.
function showFluvIdMenu(picker, options) {
  const current = picker.input.value.trim().toLowerCase();
  const all = options.slice();
  if (current && !all.some((o) => o.value === current)) {
    all.push({ value: current, text: t('modals.newList.fluv.unknownId', { id: current }) });
  }
  picker.select.replaceChildren(...all.map((o) => new Option(o.text, o.value)));
  picker.select.value = current;
  picker.input.value = current;
  picker.input.hidden = true;
  picker.select.hidden = false;
  setFluvPickerLabel(picker, picker.select.id, picker.menuKey);
}

function fluvAccountOptions(meta) {
  const linked = meta.accounts.find((a) => a.id === meta.default_account_id);
  return [
    { value: '', text: linked ? t('modals.newList.fluv.linkedAccountNamed', { name: linked.name || linked.id }) : t('modals.newList.fluv.idPlaceholder') },
    ...meta.accounts.map((a) => ({ value: a.id, text: a.name || a.id })),
  ];
}

function fluvCategoryOptions(meta) {
  return [
    { value: '', text: t('modals.newList.fluv.categoryPlaceholder') },
    ...meta.categories.map((c) => {
      const name = c.name || c.id;
      return { value: c.id, text: c.emoji ? `${c.emoji} ${name}` : name };
    }),
  ];
}

// ---------------------------------------------------------------------------
// "Envoi à Fluv" in the list modal (#list-fluv-field): how the purchases the
// user checks off this list are filed in their own Fluv — account, category
// (menus of the user's, else ids copied from Fluv) and need/want — through
// GET/PUT /api/v1/integrations/fluv/lists/{id}. Per user, not per list: a
// shared list's members each send to their own Fluv. Edit mode and purchase
// lists only, once the user connected Fluv. app.js calls
// prepareListFluvDefaults from openListModal, listFluvValidationError before
// saving the list and saveListFluvDefaults after.
// ---------------------------------------------------------------------------

const listFluvEls = {
  field: document.getElementById('list-fluv-field'),
  budgetType: document.getElementById('list-fluv-budget-type'),
  account: document.getElementById('list-fluv-account'),
  category: document.getElementById('list-fluv-category'),
};

// Fluv's ids are UUIDs; the server checks the same shape (fluv.validFluvID).
const FLUV_ID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const listFluvAccountPicker = fluvIdPicker('list-fluv-account', 'list-fluv-account-select',
  'modals.newList.fluv.accountLabel', 'modals.newList.fluv.accountMenuLabel');
const listFluvCategoryPicker = fluvIdPicker('list-fluv-category', 'list-fluv-category-select',
  'modals.newList.fluv.categoryLabel', 'modals.newList.fluv.categoryMenuLabel');

// {listId, defaults} as last read from the server; null while the field is
// hidden, so nothing is saved.
let listFluvLoaded = null;

function listFluvPayload() {
  return {
    account_id: listFluvEls.account.value.trim(),
    category_id: listFluvEls.category.value.trim(),
    budget_type: listFluvEls.budgetType.value,
  };
}

async function prepareListFluvDefaults(list) {
  listFluvEls.field.hidden = true;
  listFluvLoaded = null;
  if (!list || !isPurchaseList(list.type)) return;
  let view;
  try {
    view = await apiRequest(`/integrations/fluv/lists/${list.id}`);
  } catch {
    return; // 404: the integration is off; offline: nothing to show.
  }
  // The modal may have been closed, or opened on another list, meanwhile.
  if (editingList?.id !== list.id || !view.configured) return;
  const defaults = { account_id: view.account_id || '', category_id: view.category_id || '', budget_type: view.budget_type || '' };
  listFluvEls.account.value = defaults.account_id;
  listFluvEls.category.value = defaults.category_id;
  listFluvEls.budgetType.value = defaults.budget_type;
  listFluvLoaded = { listId: list.id, defaults };
  showFluvIdText(listFluvAccountPicker);
  showFluvIdText(listFluvCategoryPicker);
  listFluvEls.field.hidden = false;
  const meta = await fetchFluvMetadata();
  if (!meta || listFluvLoaded?.listId !== list.id || editingList?.id !== list.id) return;
  showFluvIdMenu(listFluvAccountPicker, fluvAccountOptions(meta));
  showFluvIdMenu(listFluvCategoryPicker, fluvCategoryOptions(meta));
}

// Hides the field while a non-purchase type is picked in the modal (nothing
// on such a list is sent), shows it again otherwise.
function syncListFluvField(type) {
  if (listFluvLoaded) listFluvEls.field.hidden = !isPurchaseList(type);
}

// The message to show instead of saving the list, or null.
function listFluvValidationError() {
  if (!listFluvLoaded || listFluvEls.field.hidden) return null;
  const { account_id: account, category_id: category } = listFluvPayload();
  if ((account && !FLUV_ID_PATTERN.test(account)) || (category && !FLUV_ID_PATTERN.test(category))) {
    return t('modals.newList.fluv.invalidId');
  }
  return null;
}

// Saves the defaults if they changed. The list itself is already saved: a
// failure here only warns.
async function saveListFluvDefaults(listId) {
  if (!listFluvLoaded || listFluvLoaded.listId !== listId || listFluvEls.field.hidden) return;
  const payload = listFluvPayload();
  const before = listFluvLoaded.defaults;
  listFluvLoaded = null;
  if (payload.account_id === before.account_id && payload.category_id === before.category_id && payload.budget_type === before.budget_type) return;
  try {
    await apiRequest(`/integrations/fluv/lists/${listId}`, { method: 'PUT', body: JSON.stringify(payload) });
  } catch (err) {
    const reason = err && err.code === 'invalid_fluv_id' ? t('modals.newList.fluv.invalidId')
      : (err && !err.isNetworkError && err.message) || t('modals.userSettings.fluv.networkRequired');
    window.TrakkaToast?.warning(t('modals.newList.fluv.saveFailed', { reason }));
  }
}

// ---------------------------------------------------------------------------
// "Envoi à Fluv" for one purchase (#fluv-purchase-sheet): the buyer files it
// in another Fluv account, adds a shipping fee, or doesn't send it at all,
// through GET/PUT /api/v1/integrations/fluv/items/{id} — the shipping fee
// is the item's (PATCH /items/{id}, shipping_price). Offered by a toast
// right after a check-off is committed (offerFluvPurchaseOptions, from
// list_view.js's toggleDone and budget.js's savePaidPrice), while the
// purchase still waits its ~20 s before going out, and from
// #item-actions-sheet (updateItemActionsFluv, from openItemActionsSheet).
// Only the buyer has a purchase to change: for anyone else, an item bought
// without a price, or a user without Fluv, the server answers queued and
// skipped false, and nothing is offered.
// ---------------------------------------------------------------------------

const fluvPurchaseEls = {
  sheet: document.getElementById('fluv-purchase-sheet'),
  form: document.getElementById('fluv-purchase-form'),
  item: document.getElementById('fluv-purchase-item'),
  state: document.getElementById('fluv-purchase-state'),
  skip: document.getElementById('fluv-purchase-skip'),
  account: document.getElementById('fluv-purchase-account'),
  shipping: document.getElementById('fluv-purchase-shipping'),
  close: document.getElementById('close-fluv-purchase-sheet-button'),
  actionsButton: document.getElementById('item-actions-fluv-button'),
  actionsValue: document.getElementById('item-actions-fluv-value'),
};

const fluvPurchaseAccountPicker = fluvIdPicker('fluv-purchase-account', 'fluv-purchase-account-select',
  'modals.newList.fluv.accountLabel', 'modals.newList.fluv.accountMenuLabel');

// Set once the server answers 404 on these routes (the integration is off):
// no more requests this session.
let fluvItemRoutesOff = false;
// The item #fluv-purchase-sheet is open on, and the item #item-actions-sheet
// last asked about (its answer may come after the sheet moved on).
let fluvPurchaseItem = null;
let fluvActionsItem = null;
let fluvActionsView = null;

// The caller's purchase of item, or null when there is nothing to offer.
async function fetchFluvPurchase(item) {
  if (fluvItemRoutesOff || isLocalItem(item)) return null;
  let view;
  try {
    view = await apiRequest(`/integrations/fluv/items/${item.id}`);
  } catch (err) {
    if (err && err.status === 404) fluvItemRoutesOff = true;
    return null; // offline: nothing to offer
  }
  return view.queued || view.skipped ? view : null;
}

function fluvPurchaseStateText(view) {
  if (view.skipped) return t('fluvPurchase.stateSkipped');
  return t(view.sent ? 'fluvPurchase.stateSent' : 'fluvPurchase.stateQueued');
}

async function offerFluvPurchaseOptions(item) {
  const view = await fetchFluvPurchase(item);
  if (!view || !view.queued) return;
  window.TrakkaToast?.withAction(t('fluvPurchase.toast', { title: item.title }), {
    label: t('fluvPurchase.options'),
    onClick: () => openFluvPurchaseSheet(item, view),
  });
}

// Called by openItemActionsSheet (list_view.js): the row shows once the
// server says there is a purchase of the user's to change.
async function updateItemActionsFluv(item) {
  fluvPurchaseEls.actionsButton.hidden = true;
  fluvActionsItem = item;
  fluvActionsView = null;
  const list = state.currentList;
  if (!list || !isPurchaseList(list.type) || !item.done) return;
  const view = await fetchFluvPurchase(item);
  if (!view || fluvActionsItem !== item) return;
  fluvActionsView = view;
  fluvPurchaseEls.actionsValue.textContent = fluvPurchaseStateText(view);
  fluvPurchaseEls.actionsButton.hidden = false;
}

function openFluvPurchaseSheet(item, view) {
  fluvPurchaseItem = item;
  fluvPurchaseEls.item.textContent = item.title;
  fluvPurchaseEls.state.textContent = fluvPurchaseStateText(view);
  fluvPurchaseEls.skip.checked = Boolean(view.skipped);
  fluvPurchaseEls.account.value = view.account_id || '';
  showFluvIdText(fluvPurchaseAccountPicker);
  fluvPurchaseEls.shipping.value = typeof item.shipping_price === 'number' ? String(item.shipping_price) : '';
  fluvPurchaseEls.sheet.hidden = false;
  TrakkaScrollLock.lock();
  fluvPurchaseEls.skip.focus();
  fetchFluvMetadata().then((meta) => {
    if (meta && fluvPurchaseItem === item && !fluvPurchaseEls.sheet.hidden) {
      showFluvIdMenu(fluvPurchaseAccountPicker, fluvAccountOptions(meta));
    }
  });
}

function closeFluvPurchaseSheet() {
  if (fluvPurchaseEls.sheet.hidden) return;
  fluvPurchaseItem = null;
  fluvPurchaseEls.sheet.hidden = true;
  TrakkaScrollLock.unlock();
}

// The shipping fee typed in the sheet: null for none, NaN when invalid.
function parseFluvPurchaseShipping() {
  const raw = fluvPurchaseEls.shipping.value.trim();
  if (raw === '') return null;
  const value = Number.parseFloat(raw);
  return Number.isFinite(value) && value >= 0 ? value : NaN;
}

// Files the purchase first, then saves a changed shipping fee: the server
// corrects the purchase as it then stands (in place while it is unsent).
async function saveFluvPurchase(item) {
  hideError();
  const account = fluvPurchaseEls.account.value.trim();
  if (account && !FLUV_ID_PATTERN.test(account)) {
    showError(t('modals.newList.fluv.invalidId'));
    return;
  }
  const shipping = parseFluvPurchaseShipping();
  if (Number.isNaN(shipping)) {
    showError(t('modals.editItem.shippingPriceValidationError'));
    return;
  }
  const skip = fluvPurchaseEls.skip.checked;
  closeFluvPurchaseSheet();
  try {
    await apiRequest(`/integrations/fluv/items/${item.id}`, {
      method: 'PUT',
      body: JSON.stringify({ account_id: account, skip }),
    });
    if (shipping !== (typeof item.shipping_price === 'number' ? item.shipping_price : null)) {
      const updated = await apiRequest(`/items/${item.id}`, { method: 'PATCH', body: JSON.stringify({ shipping_price: shipping }) });
      mergeServerItem(item, updated);
      if (state.currentListId !== null) renderItems();
    }
    window.TrakkaToast?.success(t(skip ? 'fluvPurchase.savedSkipped' : 'fluvPurchase.saved', { title: item.title }));
  } catch (err) {
    const reason = err && err.code === 'no_purchase' ? t('fluvPurchase.noPurchase')
      : err && err.code === 'invalid_fluv_id' ? t('modals.newList.fluv.invalidId')
        : (err && !err.isNetworkError && err.message) || t('modals.userSettings.fluv.networkRequired');
    window.TrakkaToast?.warning(t('fluvPurchase.saveFailed', { reason }));
  }
}

fluvPurchaseEls.form.addEventListener('submit', (event) => {
  event.preventDefault();
  const item = fluvPurchaseItem;
  if (item) saveFluvPurchase(item);
});
fluvPurchaseEls.close.addEventListener('click', closeFluvPurchaseSheet);
fluvPurchaseEls.sheet.addEventListener('click', (event) => {
  if (event.target === fluvPurchaseEls.sheet) closeFluvPurchaseSheet();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') closeFluvPurchaseSheet();
});
fluvPurchaseEls.actionsButton.addEventListener('click', () => {
  const item = fluvActionsItem;
  const view = fluvActionsView;
  closeItemActionsSheet();
  if (item && view) openFluvPurchaseSheet(item, view);
});
