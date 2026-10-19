'use strict';

// Notification bell in the header ("Alertes de prix"), with two sources:
//
// - price_notifications (GET /api/v1/price-notifications, see
//   internal/handlers.notifyPriceChange): the signed-in user's own inbox of
//   price changes on tracked items — a drop, an increase, a better price
//   found — written whether or not push is on, so an alert never depends on
//   push reaching this device. Opening the drawer marks the ones shown read;
//   each entry's ✕ removes it, "Tout effacer" removes them all (DELETE
//   /api/v1/price-notifications[/{id}] — the user's own entries only).
// - price_alerts (see internal/handlers/price_alerts.go): a lower price found
//   for one of the current house's items, on its own page or on Dealabs,
//   waiting to be accepted ("Appliquer le nouveau prix", which PATCHes the
//   item's price) or rejected ("Ignorer").
//
// The badge counts the unread changes plus the pending deals. A tap on a
// card opens the page its price is on (new tab), or else the item in its
// list, scrolled to and highlighted (openNotificationItem). Shares
// `state`, `els`, `apiRequest`, `showError`/`hideError`, `t`, `formatEuro`,
// `isSafeHttpUrl`, `refreshVisibleView`, `selectList`, `revealListItem`
// with app.js/list_view.js/planning.js — same classic-<script>-tags
// shared-scope pattern as those files.
//
// Unlike the planning/urgent tabs this isn't a dashboard view: it's a
// header-level drawer, independent of which tab is currently active, so it
// is refreshed from its own hook points (see refreshNotifications' call
// sites in app.js) rather than through refreshVisibleView's tab switch.

const notifEls = {
  button: document.getElementById('notifications-button'),
  badge: document.getElementById('notifications-badge'),
  modal: document.getElementById('notifications-modal'),
  closeButton: document.getElementById('close-notifications-modal-button'),
  clearButton: document.getElementById('clear-notifications-button'),
  list: document.getElementById('notifications-list'),
};

// Every pending price alert known for state.currentHouseId — re-fetched by
// loadNotifications, re-rendered into the drawer whenever it's open.
let notificationAlerts = [];

// The user's price change inbox (newest first, read or not), and the ids
// that were still unread when the drawer was last opened — those keep their
// "Nouveau" tag for as long as the drawer stays open, even though opening it
// already marked them read.
let priceNotifications = [];
let freshPriceNotificationIds = new Set();

// Ids removed in this page's lifetime: a load that was already in flight
// when one was removed (the 60 s poll), or an offline read while the DELETE
// still sits in sw.js's queue, must not bring it back.
const dismissedPriceNotificationIds = new Set();

// A "deal" entry duplicates the actionable card of a pending alert for the
// same item when that one is shown: only the card is listed then.
function visiblePriceNotifications() {
  const pendingItemIds = new Set(notificationAlerts.map((alert) => alert.item_id));
  return priceNotifications.filter((n) => n.kind !== 'deal' || !pendingItemIds.has(n.item_id));
}

function unreadPriceNotifications() {
  return visiblePriceNotifications().filter((n) => !n.read_at);
}

function updateNotificationsBadge() {
  const count = notificationAlerts.length + unreadPriceNotifications().length;
  notifEls.badge.hidden = count === 0;
  notifEls.badge.textContent = count > 9 ? '9+' : String(count);
}

function buildNotificationsHeading(key) {
  const li = document.createElement('li');
  li.className = 'pt-1 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400';
  li.textContent = t(key);
  return li;
}

function renderNotificationsList() {
  notifEls.list.replaceChildren();
  const changes = visiblePriceNotifications();
  updateClearNotificationsButton();
  if (notificationAlerts.length === 0 && changes.length === 0) {
    const li = document.createElement('li');
    li.className = 'rounded-xl border border-dashed border-slate-200 dark:border-slate-700 p-6 text-center text-sm text-slate-500';
    li.textContent = t('notifications.empty');
    notifEls.list.appendChild(li);
    return;
  }
  if (notificationAlerts.length > 0) {
    notifEls.list.appendChild(buildNotificationsHeading('notifications.dealsTitle'));
    for (const alert of notificationAlerts) notifEls.list.appendChild(buildAlertRow(alert));
  }
  if (changes.length > 0) {
    notifEls.list.appendChild(buildNotificationsHeading('notifications.changesTitle'));
    for (const n of changes) notifEls.list.appendChild(buildPriceNotificationRow(n));
  }
}

const PRICE_NOTIFICATION_KIND = {
  drop: { key: 'notifications.kindDrop', className: 'text-[color:var(--tk-price-drop)]' },
  increase: { key: 'notifications.kindIncrease', className: 'text-[color:var(--tk-price-increase)]' },
  deal: { key: 'notifications.kindDeal', className: 'text-[color:var(--tk-price-drop)]' },
  expired: { key: 'notifications.kindExpired', className: 'text-slate-600 dark:text-slate-300' },
};

// One entry of the price change inbox: what happened, to which item, the
// two prices, when, and — for a deal — where.
function buildPriceNotificationRow(n) {
  const kind = PRICE_NOTIFICATION_KIND[n.kind] || PRICE_NOTIFICATION_KIND.drop;
  const li = document.createElement('li');
  li.className = NOTIFICATION_CARD_CLASS;
  li.dataset.priceNotificationId = String(n.id);

  const top = document.createElement('div');
  top.className = 'mb-1 flex items-center justify-between gap-2';
  const label = document.createElement('span');
  label.className = `text-xs font-semibold ${kind.className}`;
  label.textContent = t(kind.key);
  top.appendChild(label);
  const trailing = document.createElement('div');
  trailing.className = 'flex shrink-0 items-center gap-1';
  if (freshPriceNotificationIds.has(n.id)) {
    const fresh = document.createElement('span');
    fresh.className = 'rounded-full bg-sky-500/15 px-2 py-0.5 text-[10px] font-semibold text-sky-700 dark:text-sky-300';
    fresh.textContent = t('notifications.unread');
    trailing.appendChild(fresh);
  }
  const removeBtn = document.createElement('button');
  removeBtn.type = 'button';
  // Always shown (no hover-only reveal: a phone has no hover), with a 44px
  // touch target whose negative margins keep the row as compact as before.
  removeBtn.className = '-my-3 -mr-2.5 flex h-11 w-11 items-center justify-center rounded-lg text-slate-500 dark:text-slate-400 transition hover:bg-rose-500/10 hover:text-rose-600 dark:hover:text-rose-400 active:bg-rose-500/10 active:text-rose-600 dark:active:text-rose-400';
  removeBtn.setAttribute('aria-label', t('notifications.remove'));
  removeBtn.title = t('notifications.remove');
  removeBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" class="h-5 w-5" aria-hidden="true"><path d="M18 6L6 18M6 6l12 12"/></svg>';
  removeBtn.addEventListener('click', () => dismissPriceNotification(n, li));
  trailing.appendChild(removeBtn);
  top.appendChild(trailing);
  li.appendChild(top);

  const title = document.createElement('p');
  title.className = 'truncate text-sm font-medium text-slate-900 dark:text-slate-100';
  title.textContent = n.item_title;
  li.appendChild(title);

  const prices = document.createElement('p');
  prices.className = 'mt-1 flex items-center gap-2 text-sm tabular-nums';
  const oldPrice = document.createElement('span');
  oldPrice.className = 'text-slate-500 line-through';
  oldPrice.textContent = formatEuro(n.old_price);
  const arrow = document.createElement('span');
  arrow.className = 'text-slate-500';
  arrow.setAttribute('aria-hidden', 'true');
  arrow.textContent = '→';
  const newPrice = document.createElement('span');
  newPrice.className = `font-semibold ${kind.className}`;
  newPrice.textContent = formatEuro(n.new_price);
  const when = document.createElement('span');
  when.className = 'ml-auto text-xs text-slate-500';
  when.textContent = formatNotificationDate(n.created_at);
  if (n.kind === 'expired') {
    // One price only: the deal's, no longer available.
    oldPrice.textContent = t('notifications.expiredPrice', { price: formatEuro(n.old_price) });
    oldPrice.classList.remove('line-through');
    prices.append(oldPrice, when);
  } else {
    prices.append(oldPrice, arrow, newPrice, when);
  }
  li.appendChild(prices);

  const externalUrl = priceNotificationExternalUrl(n);
  const target = buildNotificationTarget({
    externalUrl,
    externalKey: n.kind === 'deal' ? 'notifications.viewDeal' : 'notifications.openSite',
    listId: n.list_id,
    itemId: n.item_id,
  });
  target.row.classList.add('mt-2');
  li.appendChild(target.row);
  makeNotificationCardClickable(li, target.primary);
  return li;
}

// Where tapping an inbox entry leads: the page the price is on, when the
// entry has one — the deal's own page (source_url) for a deal, the item's
// own link (item_url, joined in by the server) for a drop/increase, which
// carries no source_url — else the item in its list. Every url is
// re-checked client-side (isSafeHttpUrl, app.js) even though the server
// only ever stores validated http(s) ones: the same defense in depth as
// every other rendered <a>.
function priceNotificationExternalUrl(n) {
  const candidates = n.kind === 'deal' ? [n.source_url, n.item_url] : [n.item_url, n.source_url];
  return candidates.find((url) => url && isSafeHttpUrl(url)) || null;
}

// The look of a drawer card, inbox entry or pending deal alike: the whole
// card opens its target (makeNotificationCardClickable), hence the pointer
// and the hover. Its backdrop (#notifications-modal) is slate-100, against
// which slate-50 wouldn't show: the hover lifts to plain white instead.
const NOTIFICATION_CARD_CLASS = 'cursor-pointer rounded-xl border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 p-3 transition hover:border-sky-300 hover:bg-white dark:hover:border-sky-700 dark:hover:bg-slate-900';

// The action line at the bottom of a card: its primary link — "Voir la
// promotion ↗"/"Ouvrir le site ↗" with the site's name, or "Voir dans la
// liste" when there's no page to open — plus, next to an outside link, a
// way to the item in its list all the same. Returns the row and the primary
// link, which a tap anywhere else on the card follows.
function buildNotificationTarget({ externalUrl, externalKey, listId, itemId }) {
  const row = document.createElement('div');
  row.className = 'flex min-w-0 items-center gap-2 text-xs';
  if (!externalUrl) {
    const primary = buildNotificationListLink(listId, itemId);
    row.appendChild(primary);
    return { row, primary };
  }
  const primary = document.createElement('a');
  primary.href = externalUrl;
  primary.target = '_blank';
  primary.rel = 'noopener noreferrer';
  primary.title = externalUrl;
  primary.className = 'shrink-0 rounded font-semibold text-sky-600 dark:text-sky-400 hover:underline';
  primary.textContent = t(externalKey);
  row.appendChild(primary);
  const host = notificationLinkHost(externalUrl);
  if (host) {
    const hostEl = document.createElement('span');
    hostEl.className = 'min-w-0 truncate text-slate-500 dark:text-slate-400';
    hostEl.textContent = host;
    row.appendChild(hostEl);
  }
  const inList = buildNotificationListLink(listId, itemId);
  inList.classList.add('ml-auto');
  row.appendChild(inList);
  return { row, primary };
}

// "Voir dans la liste": a real link to the ?list=&item= deep link (app.js's
// handleDeepLinkOrRestore), so it can open in a new tab; a plain click
// opens the list right here instead, scrolled to the item.
function buildNotificationListLink(listId, itemId) {
  const link = document.createElement('a');
  link.href = `/?list=${Number(listId)}&item=${Number(itemId)}`;
  link.className = 'shrink-0 rounded font-medium text-slate-600 dark:text-slate-300 hover:text-sky-600 dark:hover:text-sky-400 hover:underline';
  link.textContent = t('notifications.openInList');
  link.addEventListener('click', (event) => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    openNotificationItem(Number(listId), Number(itemId));
  });
  return link;
}

function notificationLinkHost(url) {
  try {
    return new URL(url).hostname.replace(/^www\./, '');
  } catch {
    return '';
  }
}

// A tap anywhere on the card follows its primary link — except on the
// card's own links and buttons (✕, Appliquer, Ignorer, the links of the
// action line), which do their own thing, and except when it ends a text
// selection.
function makeNotificationCardClickable(li, primary) {
  li.addEventListener('click', (event) => {
    if (event.target.closest('a, button')) return;
    if (String(window.getSelection?.() ?? '') !== '') return;
    primary.click();
  });
}

// Closes the drawer and opens the item's list on it (selectList,
// revealListItem: list_view.js). selectList reports a list that can't be
// opened; an item a filter hides gets a word, the list opening as is.
async function openNotificationItem(listId, itemId) {
  closeNotificationsModal();
  await selectList(listId);
  if (state.currentListId !== listId || revealListItem(itemId)) return;
  if (isFilterStateActive(getFilterState(listId))) TrakkaToast.warning(t('notifications.itemFiltered'));
}

// A short, localized day + time for an inbox entry (created_at is UTC).
function formatNotificationDate(iso) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  const lang = window.TrakkaI18n ? TrakkaI18n.getLang() : 'fr';
  return date.toLocaleString(lang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
}

function buildAlertRow(alert) {
  const li = document.createElement('li');
  li.className = NOTIFICATION_CARD_CLASS;

  const title = document.createElement('p');
  title.className = 'mb-2 truncate text-sm font-medium text-slate-900 dark:text-slate-100';
  title.textContent = alert.item_title;
  li.appendChild(title);

  const compare = document.createElement('div');
  compare.className = 'mb-3 flex items-center gap-3';

  const oldPrice = document.createElement('div');
  oldPrice.className = 'flex flex-col';
  const oldLabel = document.createElement('span');
  oldLabel.className = 'text-xs text-slate-500';
  oldLabel.textContent = t('notifications.oldPrice');
  const oldValue = document.createElement('span');
  oldValue.className = 'text-sm font-medium text-slate-500 line-through';
  oldValue.textContent = formatEuro(alert.original_price);
  oldPrice.append(oldLabel, oldValue);

  const arrow = document.createElement('span');
  arrow.className = 'text-slate-500';
  arrow.setAttribute('aria-hidden', 'true');
  arrow.textContent = '→';

  const newPrice = document.createElement('div');
  newPrice.className = 'flex flex-col';
  const newLabel = document.createElement('span');
  newLabel.className = 'text-xs text-slate-500';
  newLabel.textContent = t('notifications.newPrice');
  const newValue = document.createElement('span');
  newValue.className = 'text-base font-semibold text-emerald-600 dark:text-emerald-400';
  newValue.textContent = formatEuro(alert.found_price);
  newPrice.append(newLabel, newValue);

  compare.append(oldPrice, arrow, newPrice);
  li.appendChild(compare);

  // The page the lower price was found on (see priceNotificationExternalUrl
  // for the client-side scheme re-check).
  const target = buildNotificationTarget({
    externalUrl: alert.source_url && isSafeHttpUrl(alert.source_url) ? alert.source_url : null,
    externalKey: 'notifications.viewDeal',
    listId: alert.list_id,
    itemId: alert.item_id,
  });
  target.row.classList.add('mb-3');
  li.appendChild(target.row);
  makeNotificationCardClickable(li, target.primary);
  // A price found elsewhere (Dealabs) brings its link along when applied —
  // the item then follows that page (db.AcceptPriceAlert): say so.
  if (alert.changes_url) {
    const hint = document.createElement('p');
    hint.className = 'mb-3 text-xs text-slate-500 dark:text-slate-400';
    hint.textContent = t('notifications.changesUrlHint');
    li.appendChild(hint);
  }

  const actions = document.createElement('div');
  actions.className = 'flex gap-2';

  const applyBtn = document.createElement('button');
  applyBtn.type = 'button';
  applyBtn.className = 'flex-1 rounded-xl bg-emerald-500 px-3 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 dark:hover:bg-emerald-400 active:scale-95';
  applyBtn.textContent = t(alert.changes_url ? 'notifications.applyWithLink' : 'notifications.apply');
  applyBtn.addEventListener('click', () => resolveAlert(alert, 'accepted'));

  const ignoreBtn = document.createElement('button');
  ignoreBtn.type = 'button';
  ignoreBtn.className = 'flex-1 rounded-xl border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 px-3 py-2 text-sm font-medium text-slate-600 dark:text-slate-300 transition hover:bg-slate-200 dark:hover:bg-slate-800 active:scale-95';
  ignoreBtn.textContent = t('notifications.ignore');
  ignoreBtn.addEventListener('click', () => resolveAlert(alert, 'rejected'));

  actions.append(applyBtn, ignoreBtn);
  li.appendChild(actions);

  return li;
}

// Fetches the current house's pending price alerts and refreshes the badge
// count; also re-renders the drawer's contents if it's currently open. This
// is a background refresh (house switch, offline sync completing, a tab
// becoming visible again, ...), so a network failure just keeps the last
// known count rather than surfacing an error banner.
//
// The price change inbox is the user's own, not the house's: it loads
// whether or not a house is selected. Either request failing keeps that
// source's last known state.
async function loadNotifications() {
  const inbox = apiRequest('/price-notifications').then(
    (list) => { if (Array.isArray(list)) priceNotifications = list.filter((n) => !dismissedPriceNotificationIds.has(n.id)); },
    () => {},
  );
  // ensureHousesLoaded is defined in app.js, resolved lazily the same way
  // every other cross-file call in this codebase already is — see its own
  // comment for why every house-scoped loader (this one included) must
  // await it before ever using state.currentHouseId in a request.
  await ensureHousesLoaded();
  if (state.currentHouseId === null) {
    notificationAlerts = [];
  } else {
    try {
      const alerts = await apiRequest(`/price-alerts?house_id=${state.currentHouseId}&status=pending`);
      if (Array.isArray(alerts)) notificationAlerts = alerts;
    } catch {
      // keep the last known alerts
    }
  }
  await inbox;
  updateNotificationsBadge();
  if (!notifEls.modal.hidden) {
    renderNotificationsList();
    markShownPriceNotificationsRead();
  }
}

// Marks the inbox entries the drawer is showing read, server-side and
// locally (so the badge drops them at once). They stay tagged "Nouveau"
// until the drawer is closed. Best effort: if the request fails (offline,
// where sw.js queues it), they are simply still unread on the next load.
function markShownPriceNotificationsRead() {
  const unread = unreadPriceNotifications();
  if (unread.length === 0) return;
  const now = new Date().toISOString();
  for (const n of unread) {
    freshPriceNotificationIds.add(n.id);
    n.read_at = now;
  }
  renderNotificationsList();
  updateNotificationsBadge();
  apiRequest('/price-notifications/read', { method: 'POST', body: JSON.stringify({ ids: unread.map((n) => n.id) }) }).catch(() => {});
}

// Called from app.js's hook points (house switch, offline sync completing,
// a language switch, the tab regaining visibility) — resolved lazily the
// same way planning.js/urgent.js's refresh functions are called from there.
function refreshNotifications() {
  loadNotifications();
}

// A background scan can find a price change at any time: while Trakka stays
// open and visible, look again every minute (the hooks above cover a
// return to the tab, and list reloads — see refreshCurrentList) — at the
// 🔔 and at the open list's own prices (applyServerPriceChanges, defined in
// list_view.js), which the same scan moved: an increase shows its ▲ even
// for a user whose 🔔 leaves increases out.
const NOTIFICATIONS_POLL_MS = 60 * 1000;
setInterval(() => {
  if (document.visibilityState !== 'visible' || !state.currentUser) return;
  loadNotifications();
  applyServerPriceChanges();
}, NOTIFICATIONS_POLL_MS);

function openNotificationsModal() {
  notifEls.modal.hidden = false;
  TrakkaScrollLock.lock();
  renderNotificationsList();
  markShownPriceNotificationsRead();
}

function closeNotificationsModal() {
  notifEls.modal.hidden = true;
  TrakkaScrollLock.unlock();
  freshPriceNotificationIds = new Set();
  disarmClearNotifications();
}

notifEls.button.addEventListener('click', () => {
  openNotificationsModal();
  loadNotifications();
});
notifEls.closeButton.addEventListener('click', closeNotificationsModal);
notifEls.clearButton.addEventListener('click', onClearNotificationsClick);
notifEls.modal.addEventListener('click', (event) => {
  if (event.target === notifEls.modal) closeNotificationsModal();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !notifEls.modal.hidden) closeNotificationsModal();
});

// Resolving is optimistic (the row disappears immediately) with a rollback
// on failure, the same pattern urgent.js's markUrgentItemDone uses.
// Accepting can change an item's price while it's on screen elsewhere (its
// own list view, the planning view, ...), hence the refreshVisibleView()
// call — defined in app.js, resolved lazily the same way.
async function resolveAlert(alert, status) {
  hideError();
  notificationAlerts = notificationAlerts.filter((a) => a.id !== alert.id);
  renderNotificationsList();
  updateNotificationsBadge();

  try {
    await apiRequest(`/price-alerts/${alert.id}`, { method: 'PATCH', body: JSON.stringify({ status }) });
  } catch (err) {
    notificationAlerts = [...notificationAlerts, alert];
    renderNotificationsList();
    updateNotificationsBadge();
    if (!isNetworkError(err)) showError(err.message);
    return;
  }
  // The inbox's "deal" entry for this item was hidden behind the card just
  // resolved (visiblePriceNotifications): now that it would show, it is
  // news to nobody here — mark it read rather than let it raise the badge.
  markShownPriceNotificationsRead();
  refreshVisibleView();
}

// Slides an entry out and folds its height away (base.css's
// .notification-row--leaving) before `done` re-renders the list without
// it. The current height is pinned first so max-height has something to
// transition from. Under prefers-reduced-motion it goes at once.
const NOTIFICATION_LEAVE_MS = 290;

function animateNotificationRowsOut(rows, done) {
  if (rows.length === 0 || window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
    done();
    return;
  }
  for (const row of rows) row.style.maxHeight = `${row.offsetHeight}px`;
  // Forces the pinned height to apply before the class changes it.
  void rows[0].offsetHeight;
  for (const row of rows) row.classList.add('notification-row--leaving');
  setTimeout(done, NOTIFICATION_LEAVE_MS);
}

// Removing is optimistic (the entry leaves at once), rolled back if the
// server refuses — the same pattern as resolveAlert. Offline, sw.js queues
// the DELETE and replays it later; the server ignores an id that isn't the
// caller's or is already gone, so a replay is harmless.
async function dismissPriceNotification(n, row) {
  hideError();
  dismissedPriceNotificationIds.add(n.id);
  priceNotifications = priceNotifications.filter((p) => p.id !== n.id);
  updateNotificationsBadge();
  row.inert = true;
  animateNotificationRowsOut([row], () => {
    if (!notifEls.modal.hidden) renderNotificationsList();
  });

  try {
    await apiRequest(`/price-notifications/${n.id}`, { method: 'DELETE' });
  } catch (err) {
    dismissedPriceNotificationIds.delete(n.id);
    priceNotifications = [...priceNotifications, n].sort(byNewestPriceNotification);
    renderNotificationsList();
    updateNotificationsBadge();
    if (!isNetworkError(err)) showError(err.message);
  }
}

function byNewestPriceNotification(a, b) {
  return b.created_at.localeCompare(a.created_at) || b.id - a.id;
}

// "Tout effacer" empties the user's inbox (the "Variations de prix"
// section). The pending deal cards above it belong to the whole house and
// keep their own Appliquer/Ignorer buttons, so they stay. A first click
// arms the button ("Confirmer ?"), a second one within
// NOTIFICATIONS_CLEAR_CONFIRM_MS clears — no native confirm(), the same
// two-step pattern as admin.js's buildConfirmableDeleteButton.
const NOTIFICATIONS_CLEAR_CONFIRM_MS = 4000;
let clearNotificationsTimer = null;

// Shown only while the drawer lists at least one inbox entry (a "deal"
// entry hidden behind its pending card doesn't count, though clearing
// removes it too).
function updateClearNotificationsButton() {
  const hasEntries = visiblePriceNotifications().length > 0;
  notifEls.clearButton.hidden = !hasEntries;
  notifEls.clearButton.title = t('notifications.clearAllLabel');
  if (!hasEntries) disarmClearNotifications();
}

function disarmClearNotifications() {
  if (clearNotificationsTimer === null) return;
  clearTimeout(clearNotificationsTimer);
  clearNotificationsTimer = null;
  notifEls.clearButton.textContent = t('notifications.clearAll');
  notifEls.clearButton.classList.remove('bg-rose-500/10', 'text-rose-600', 'dark:text-rose-400');
  notifEls.clearButton.classList.add('text-slate-600', 'dark:text-slate-300');
}

function onClearNotificationsClick() {
  if (clearNotificationsTimer === null) {
    notifEls.clearButton.textContent = t('notifications.clearAllConfirm');
    notifEls.clearButton.classList.remove('text-slate-600', 'dark:text-slate-300');
    notifEls.clearButton.classList.add('bg-rose-500/10', 'text-rose-600', 'dark:text-rose-400');
    clearNotificationsTimer = setTimeout(disarmClearNotifications, NOTIFICATIONS_CLEAR_CONFIRM_MS);
    return;
  }
  disarmClearNotifications();
  clearPriceNotifications();
}

async function clearPriceNotifications() {
  hideError();
  const removed = priceNotifications;
  for (const n of removed) dismissedPriceNotificationIds.add(n.id);
  priceNotifications = [];
  updateNotificationsBadge();
  const rows = [...notifEls.list.querySelectorAll('[data-price-notification-id]')];
  for (const row of rows) row.inert = true;
  animateNotificationRowsOut(rows, () => {
    if (!notifEls.modal.hidden) renderNotificationsList();
  });

  try {
    await apiRequest('/price-notifications', { method: 'DELETE' });
  } catch (err) {
    for (const n of removed) dismissedPriceNotificationIds.delete(n.id);
    priceNotifications = [...removed, ...priceNotifications].sort(byNewestPriceNotification);
    renderNotificationsList();
    updateNotificationsBadge();
    if (!isNetworkError(err)) showError(err.message);
  }
}
