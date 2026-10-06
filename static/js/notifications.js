'use strict';

// Notification bell in the header ("Alertes de prix"), with two sources:
//
// - price_notifications (GET /api/v1/price-notifications, see
//   internal/handlers.notifyPriceChange): the signed-in user's own inbox of
//   price changes on tracked items — a drop, an increase, a better price
//   found — written whether or not push is on, so an alert never depends on
//   push reaching this device. Opening the drawer marks the ones shown read.
// - price_alerts (see internal/handlers/price_alerts.go): a lower price found
//   for one of the current house's items, on its own page or on Dealabs,
//   waiting to be accepted ("Appliquer le nouveau prix", which PATCHes the
//   item's price) or rejected ("Ignorer").
//
// The badge counts the unread changes plus the pending deals. Shares
// `state`, `els`, `apiRequest`, `showError`/`hideError`, `t`, `formatEuro`,
// `isSafeHttpUrl`, `refreshVisibleView` with app.js/list_view.js/planning.js
// — same classic-<script>-tags shared-scope pattern as those files.
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
  li.className = 'rounded-xl border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 p-3';

  const top = document.createElement('div');
  top.className = 'mb-1 flex items-center justify-between gap-2';
  const label = document.createElement('span');
  label.className = `text-xs font-semibold ${kind.className}`;
  label.textContent = t(kind.key);
  top.appendChild(label);
  if (freshPriceNotificationIds.has(n.id)) {
    const fresh = document.createElement('span');
    fresh.className = 'rounded-full bg-sky-500/15 px-2 py-0.5 text-[10px] font-semibold text-sky-700 dark:text-sky-300';
    fresh.textContent = t('notifications.unread');
    top.appendChild(fresh);
  }
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

  // Same defense-in-depth scheme re-check as buildAlertRow's link below.
  if (n.source_url && isSafeHttpUrl(n.source_url)) {
    const link = document.createElement('a');
    link.href = n.source_url;
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    link.className = 'mt-1 block truncate text-xs text-sky-600 dark:text-sky-400 hover:underline';
    link.textContent = n.source_url;
    li.appendChild(link);
  }
  return li;
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
  li.className = 'rounded-xl border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 p-3';

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

  // isSafeHttpUrl (defined in app.js) re-checks the scheme client-side even
  // though the backend only ever persists a validated http(s) source_url —
  // the same defense-in-depth pattern used for every other rendered <a>.
  if (alert.source_url && isSafeHttpUrl(alert.source_url)) {
    const link = document.createElement('a');
    link.href = alert.source_url;
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    link.className = 'mb-3 block truncate text-xs text-sky-600 dark:text-sky-400 hover:underline';
    link.textContent = alert.source_url;
    li.appendChild(link);
  }
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
    (list) => { if (Array.isArray(list)) priceNotifications = list; },
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
// return to the tab, and list reloads — see refreshCurrentList).
const NOTIFICATIONS_POLL_MS = 60 * 1000;
setInterval(() => {
  if (document.visibilityState === 'visible' && state.currentUser) loadNotifications();
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
}

notifEls.button.addEventListener('click', () => {
  openNotificationsModal();
  loadNotifications();
});
notifEls.closeButton.addEventListener('click', closeNotificationsModal);
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
