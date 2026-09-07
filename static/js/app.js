'use strict';

const API_BASE = '/api/v1';

// `state.currentList` is the full list detail (with items) for whichever
// list is currently open — list_view.js owns rendering it and applies
// optimistic edits directly to its `items` array before the matching
// request resolves. Kept here (rather than in list_view.js) only because
// dashboard navigation (selectList's caller) also needs `currentListId`.
const state = {
  currentListId: null,
  currentList: null,
  currentHouseId: null,
  currentUser: null,
  houses: [],
};

// Remembers the last house selected across reloads (per-browser only,
// nothing shared with the server) so switching houses sticks between visits.
const HOUSE_STORAGE_KEY = 'trakka:currentHouseId';

// Whichever dashboard tab or list was last visited, saved as JSON — either
// { type: 'tab', tab: 'planning' } or { type: 'list', id: 42 } — so
// relaunching the app can reopen there instead of always landing on the
// dashboard (the "keep last page on launch" preference below). Written by
// saveLastView, called from list_view.js's selectList/showDashboard and
// planning.js's setActiveTab — the only three places that change what's
// visible.
const LAST_VIEW_STORAGE_KEY = 'trakka:lastView';

// Local mirror of state.currentUser.keep_last_page (the server-side
// preference, PATCHed via /api/v1/me — see static/js/settings.js), read
// before the /me call resolves so the very first paint can already decide
// whether a restore should even be attempted, without blocking on a network
// round-trip that might never come back (e.g. while offline). The server
// value is authoritative and re-synced into this mirror every time /me
// succeeds (see init() below); this key exists purely to avoid the "restore
// or not" decision depending on a round-trip that might not have resolved
// yet at startup.
const KEEP_LAST_PAGE_STORAGE_KEY = 'trakka:keepLastPage';

// CACHED_USER_STORAGE_KEY records which account the local IndexedDB mirror
// (and the localStorage view/house preferences) were built for. Everything
// cached client-side is per-account data, but nothing tied it to an account
// before: on a shared browser, signing out and signing in as someone else
// left the previous user's houses/lists/items in IndexedDB, where
// hydrateFromCache() would paint them onto the new user's dashboard before
// any network response could correct it — and any writes the previous user
// had queued offline would be replayed by the service worker under the new
// user's session. purgeLocalUserData() below clears all of it; it runs on
// logout, and again defensively whenever /api/v1/me reports a different
// account than this key names.
const CACHED_USER_STORAGE_KEY = 'trakka:cachedUserId';

// purgeLocalUserData drops every trace of the current account's data from
// this browser: the IndexedDB mirror and pending offline write queue, the
// service worker's runtime API cache, and the per-account localStorage
// preferences. Best-effort throughout — a browser that denies storage access
// must not be able to block a logout — so every step is individually
// guarded rather than allowed to reject the whole chain.
async function purgeLocalUserData() {
  if (window.TrakkaDB && window.TrakkaDB.clearAll) {
    try {
      await window.TrakkaDB.clearAll();
    } catch (err) {
      console.warn('Purge du cache local impossible :', err);
    }
  }

  // The app shell (HTML/CSS/JS) is not user data and is deliberately kept —
  // only caches holding API responses are dropped, so the next user still
  // gets an instant shell load.
  if (window.caches) {
    try {
      const keys = await caches.keys();
      await Promise.all(keys.filter((key) => key.includes('runtime')).map((key) => caches.delete(key)));
    } catch (err) {
      console.warn('Purge du cache réseau impossible :', err);
    }
  }

  try {
    [HOUSE_STORAGE_KEY, LAST_VIEW_STORAGE_KEY, KEEP_LAST_PAGE_STORAGE_KEY, CACHED_USER_STORAGE_KEY].forEach((key) =>
      localStorage.removeItem(key)
    );
  } catch (err) {
    console.warn('Purge des préférences locales impossible :', err);
  }
}

function isKeepLastPageEnabled() {
  if (state.currentUser) return state.currentUser.keep_last_page;
  const stored = localStorage.getItem(KEEP_LAST_PAGE_STORAGE_KEY);
  // Defaults to enabled, matching the server column's own DEFAULT 1 (see
  // internal/db/migrations/0010_keep_last_page_preference.sql) for a user
  // whose preference hasn't been fetched yet.
  return stored === null ? true : stored === 'true';
}

// Keeps the localStorage mirror in step with the server value — called once
// state.currentUser is known (init()) and again right after settings.js
// PATCHes a change, so neither has to wait on the other to be correct.
function setKeepLastPagePreference(enabled) {
  localStorage.setItem(KEEP_LAST_PAGE_STORAGE_KEY, String(enabled));
}

// Persists whichever view is now visible. A no-op while the preference is
// off, so turning it off needs no separate cleanup — loadLastView is simply
// never consulted again until it's turned back on, at which point whatever
// was last saved (possibly stale) is used, same as any other "remember my
// last choice" setting in this app (house, theme, language).
function saveLastView(view) {
  if (!isKeepLastPageEnabled()) return;
  try {
    localStorage.setItem(LAST_VIEW_STORAGE_KEY, JSON.stringify(view));
  } catch {
    // Storage unavailable (private browsing, quota) — losing this
    // convenience is harmless, so it's silently ignored.
  }
}

function loadLastView() {
  try {
    const raw = localStorage.getItem(LAST_VIEW_STORAGE_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

// Tabs restorable via setActiveTab (planning.js) — 'dashboard' is excluded
// since it's already the default view, needing no restore step at all.
const RESTORABLE_TABS = new Set(['planning', 'urgent', 'spaces', 'shared']);

// Reopens whichever tab or list was last visited. Called once, at the very
// end of init(), after state.currentUser/state.currentHouseId and the
// dashboard are all already resolved. Both branches fail silently (leaving
// whatever the dashboard already painted) rather than surfacing an error —
// a stale/deleted/no-longer-accessible list or an invalid tab name is not
// worth interrupting startup over, since the default "just show the
// dashboard" outcome is already correct in that case.
async function restoreLastView() {
  if (!isKeepLastPageEnabled()) return;
  const view = loadLastView();
  if (!view) return;

  if (view.type === 'list' && Number.isInteger(view.id)) {
    // selectList is defined in list_view.js, resolved lazily the same way
    // every other cross-file call in this function already is — safe here
    // since restoreLastView only ever runs from the tail of init(), well
    // after every script tag has finished loading and defined its
    // top-level functions.
    await selectList(view.id, { silent: true });
    return;
  }
  if (view.type === 'tab' && RESTORABLE_TABS.has(view.tab)) {
    // setActiveTab is defined in planning.js — same lazy-resolution
    // reasoning as selectList above.
    setActiveTab(view.tab);
  }
}

// Consumes a ?list={id} deep link — the URL a push notification's
// notificationclick opens when no Trakka tab is already open (see
// sw.js's own comment on that handler; an already-open tab is routed
// client-side instead, via handleNotificationClickMessage below) — in
// preference to restoreLastView's own saved-tab/list preference, since a
// user who just tapped a notification clearly wants the list it names, not
// wherever they happened to leave off last. Falls through to
// restoreLastView unchanged when there is no such query param, which is
// the ordinary case for every plain reload/relaunch. The query string is
// stripped via history.replaceState once consumed so a later reload of the
// same tab doesn't keep re-triggering the same deep link, and so it can
// never fight with saveLastView's own bookkeeping of what's currently open.
async function handleDeepLinkOrRestore() {
  const params = new URLSearchParams(window.location.search);
  const listIdRaw = params.get('list');
  const listId = Number(listIdRaw);

  if (listIdRaw !== null && Number.isInteger(listId) && listId > 0) {
    history.replaceState(null, '', window.location.pathname);
    // silent: a deleted/no-longer-accessible list must fail quietly back to
    // the dashboard rather than greeting a notification tap with an error
    // banner — same reasoning restoreLastView's own selectList call uses.
    await selectList(listId, { silent: true });
    return;
  }

  await restoreLastView();
}

// Routes a notification click's target list into an *already-open* tab
// without a full reload — the counterpart to handleDeepLinkOrRestore above
// for the case sw.js's notificationclick handler found a client to focus
// instead of opening a fresh window (see push.js, which is what actually
// registers the 'trakka-notification-click' listener this responds to).
function handleNotificationClickMessage(url) {
  let listId = null;
  try {
    const parsed = new URL(url, window.location.origin);
    listId = Number(parsed.searchParams.get('list'));
  } catch {
    return;
  }
  if (Number.isInteger(listId) && listId > 0) {
    selectList(listId, { silent: true });
  }
}

// Ids of lists currently sitting in their undo grace period (see
// removeList below) — a list in here is hidden from the dashboard even
// though it hasn't actually been deleted server-side yet, so a re-render
// triggered by something else during the 5s window (switching houses,
// creating another list, ...) can't make it flash back into view.
const pendingDeletedListIds = new Set();

// Shell/dashboard elements only — the list-detail view's own elements are
// cached separately in list_view.js's `listEls`.
const els = {
  networkDot: document.getElementById('network-dot'),
  networkLabel: document.getElementById('network-label'),
  pendingBadge: document.getElementById('pending-badge'),
  pendingBadgeIcon: document.getElementById('pending-badge-icon'),
  pendingBadgeText: document.getElementById('pending-badge-text'),
  errorBanner: document.getElementById('error-banner'),
  updateBanner: document.getElementById('update-banner'),
  updateReloadButton: document.getElementById('update-reload-button'),
  logoLink: document.getElementById('logo-link'),
  listsSection: document.getElementById('lists-section'),
  houseToolbar: document.getElementById('house-toolbar'),
  houseSelect: document.getElementById('house-select'),
  renameHouseInlineButton: document.getElementById('rename-house-inline-button'),
  renameHouseInlineForm: document.getElementById('rename-house-inline-form'),
  renameHouseInlineInput: document.getElementById('rename-house-inline-input'),
  cancelRenameHouseInlineButton: document.getElementById('cancel-rename-house-inline-button'),
  shoppingLists: document.getElementById('shopping-lists'),
  todoLists: document.getElementById('todo-lists'),
  customLists: document.getElementById('custom-lists'),
  newListButton: document.getElementById('new-list-button'),
  newListModal: document.getElementById('new-list-modal'),
  newListModalTitle: document.getElementById('new-list-modal-title'),
  closeModalButton: document.getElementById('close-modal-button'),
  createListForm: document.getElementById('create-list-form'),
  listNameInput: document.getElementById('list-name'),
  listIconInput: document.getElementById('list-icon'),
  listIconPresetButtons: document.querySelectorAll('#create-list-form [data-list-icon-preset]'),
  typeOptions: document.querySelectorAll('#create-list-form [data-type-option]'),
  listCategorySelect: document.getElementById('list-category'),
  listSubmitButton: document.getElementById('list-submit-button'),
  newHouseModal: document.getElementById('new-house-modal'),
  closeHouseModalButton: document.getElementById('close-house-modal-button'),
  createHouseForm: document.getElementById('create-house-form'),
  houseNameInput: document.getElementById('house-name'),
  manageMembersButton: document.getElementById('manage-members-button'),
  membersModal: document.getElementById('members-modal'),
  closeMembersModalButton: document.getElementById('close-members-modal-button'),
  membersList: document.getElementById('members-list'),
  inviteMemberForm: document.getElementById('invite-member-form'),
  inviteEmailInput: document.getElementById('invite-email'),
};

const CREATE_HOUSE_OPTION_VALUE = '__create__';

// Thin wrapper around TrakkaI18n.t (i18n.js, loaded before this file) that
// degrades to the raw key if the module somehow failed to load — mirrors
// the `!window.TrakkaDB` guard already used below for the optional offline
// module.
function t(key, vars) {
  return window.TrakkaI18n ? window.TrakkaI18n.t(key, vars) : key;
}

// Static, hard-coded icon markup (never interpolates user data) — safe to
// insert via innerHTML. Never reuse this pattern for anything containing a
// list/item title, URL, or other user-supplied value; use textContent or
// createElement for that instead (see buildListCard below, and
// buildItemRow in list_view.js).
const TRASH_ICON_SVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="h-5 w-5" aria-hidden="true">' +
  '<path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0-1 14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2L4 6h16Z"/></svg>';

// Same static-markup-only safety rule as TRASH_ICON_SVG above. Used for the
// 👥 "Partager" button on a list card/space section (see buildListCard
// below and spaces.js's buildCategorySection) and for the 👥 "shared with
// you" indicator on a card in the "Partagé avec moi" tab (shares.js).
const SHARE_ICON_SVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="h-5 w-5" aria-hidden="true">' +
  '<path d="M16 11a4 4 0 1 0-4-4"/><path d="M8 21v-2a4 4 0 0 1 4-4h1"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/></svg>';

// Same static-markup-only safety rule as TRASH_ICON_SVG above. Used for the
// 📌 pin/unpin button buildListCard shows on a card reached via a direct
// List share (list.access_source === 'list_share') — see toggleListPin and
// the "Pinning shared lists" feature in CLAUDE.md.
const PIN_ICON_SVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="h-5 w-5" aria-hidden="true">' +
  '<path d="M12 17v5"/><path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z"/></svg>';

function showError(message) {
  els.errorBanner.textContent = message;
  els.errorBanner.hidden = false;
}

function hideError() {
  els.errorBanner.hidden = true;
  els.errorBanner.textContent = '';
}

// isSafeHttpUrl re-checks, client-side, that a URL is absolute http(s).
// The backend already enforces this before persisting anything, but
// re-validating before ever setting an <a href> is cheap defense in depth.
function isSafeHttpUrl(value) {
  try {
    const parsed = new URL(value, window.location.origin);
    return parsed.protocol === 'http:' || parsed.protocol === 'https:';
  } catch {
    return false;
  }
}

// apiRequest wraps fetch for the JSON API: it always sets the JSON content
// type, parses JSON responses, and turns network failures or non-2xx
// responses into a single Error with a user-facing message. A request the
// service worker queued while offline still comes back as a 2xx (202), so
// no special-casing is needed here for that path.
async function apiRequest(path, options = {}) {
  const method = (options.method || 'GET').toUpperCase();

  // navigator.onLine is a synchronous, immediate signal — when it's already
  // false, skip attempting fetch() at all for a read rather than waiting on
  // one that's guaranteed to fail (which, without a controlling service
  // worker, can take several seconds to time out rather than reject
  // instantly). This is what makes a reload while offline paint every
  // GET-driven view (loadHouses/loadDashboard here, selectList/
  // refreshCurrentList in list_view.js, and planning.js/urgent.js/
  // spaces.js's tab loaders) from IndexedDB instantly instead of stalling
  // first. Never short-circuit a write (POST/PUT/PATCH/DELETE) this way: it
  // still has to reach the service worker's fetch handler even while
  // offline, since that's what queues it for later replay (see sw.js's
  // handleApiWrite/queueOfflineWrite) — skipping fetch() here would silently
  // drop the write instead of queuing it. navigator.onLine can still say
  // `true` on a connection that can't actually reach the server (a captive
  // portal, a down server, ...); that case is unaffected and still runs the
  // normal fetch()-then-catch path below.
  if (method === 'GET' && !navigator.onLine) {
    const err = new Error(t('common.networkError'));
    err.isNetworkError = true;
    throw err;
  }

  let response;
  try {
    response = await fetch(`${API_BASE}${path}`, {
      headers: { 'Content-Type': 'application/json' },
      // Explicit even though 'same-origin' has been the fetch() spec
      // default since 2017: some embedded/WebKit mobile browsers (notably
      // iOS Safari's standalone "Add to Home Screen" PWA mode) have been
      // unreliable about implicit defaults for cookie handling, so this is
      // spelled out rather than relied on implicitly.
      credentials: 'same-origin',
      // Belt-and-suspenders alongside internal/handlers/json.go's
      // Cache-Control: no-store on every API response — this forces the
      // browser's own HTTP disk cache to be skipped for the *request* side
      // too, regardless of what any past response's headers said, so a GET
      // to /api/v1/lists/{id} can never resolve from a stale disk-cache
      // entry instead of either a real network round trip or (offline) the
      // service worker's own IndexedDB-backed fallback.
      cache: 'no-store',
      ...options,
    });
  } catch {
    // fetch() itself failed (no connectivity, DNS failure, ...) rather than
    // the server answering with a non-2xx status — tagged so read-path
    // callers (loadDashboard, planning.js/urgent.js/spaces.js's loaders, ...)
    // can tell "we're offline" apart from a genuine server-side error and
    // skip the blocking error banner for the former, since the header's
    // discreet network badge already communicates that on its own. See
    // isNetworkError below.
    const err = new Error(t('common.networkError'));
    err.isNetworkError = true;
    throw err;
  }

  // The session cookie is missing or expired: every call in app.js and
  // list_view.js funnels through here, so this one check auth-gates the
  // whole SPA, including mid-session expiry. Never resolves so the caller
  // simply stops running rather than acting on a response that never came.
  if (response.status === 401) {
    window.location.href = '/auth/login';
    return new Promise(() => {});
  }

  const text = await response.text();
  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = null;
    }
  }

  if (!response.ok) {
    const message = body && typeof body.error === 'string' ? body.error : `Erreur ${response.status}`;
    const err = new Error(message);
    // sw.js's offlineReadFallback answers a GET it has no explicit
    // IndexedDB mirror for (/me, /admin/settings, /price-alerts,
    // /push/vapid-public-key, a house's /members, a list's/category's
    // /share roster, ...) with a real HTTP 503 rather than letting fetch()
    // throw — every one of its responses carries this header. Without this
    // check, that 503 resolves normally and skips the catch block above
    // entirely, so it was never tagged isNetworkError and slipped straight
    // past every "if (!isNetworkError(err)) showError(...)" guard already in
    // place across the app (loadMembers, loadShareRoster, ...), surfacing a
    // blocking "hors ligne" banner during perfectly ordinary offline
    // navigation. Tagging it here the same way as a genuine fetch()-level
    // failure is what makes those existing guards actually work. This never
    // fires for the deliberate "this action requires connectivity" 503s
    // queueOfflineWrite returns for houses/admin-settings/shares/
    // invitations — those don't carry this header, so they still surface as
    // real, actionable errors.
    if (response.headers.get('X-Trakka-Offline') === 'true') {
      err.isNetworkError = true;
    } else if (
      response.status === 502 ||
      response.status === 504 ||
      (response.status === 503 && !(navigator.serviceWorker && navigator.serviceWorker.controller))
    ) {
      // A 502/504 is never a status this app's own backend emits (see
      // internal/handlers/json.go's writeError — only
      // 400/401/403/404/409/500 ever come from there), so it always means
      // the reverse proxy or the backend process itself is unreachable, not
      // a real application error. The same is true of a bare 503 reaching
      // this point: whenever a service worker is actually controlling the
      // page, sw.js's own isGatewayErrorStatus check already intercepts and
      // converts every genuine backend 502/503/504 before it gets this far
      // (into a cache-served 200 for a read, or a queued write) — the only
      // 503 that legitimately reaches here with a controller present is
      // sw.js's own deliberate "this action requires connectivity" response
      // for houses/admin-settings/shares, which must keep surfacing as a
      // real, actionable error. Without a controlling service worker at all
      // (e.g. the very first load before it's registered, or an unsupported
      // browser), there is no such deliberate 503 to protect, so a bare 503
      // reaching here is unambiguously the same gateway-unreachable case as
      // 502/504. Either way: treat it as a plain connectivity failure
      // instead of a raw "Erreur 502"/"Erreur 503" banner.
      err.isNetworkError = true;
    }
    throw err;
  }

  // This response came from the service worker's offline queue (queued,
  // edited-in-place, or cancelled — see sw.js's queueOfflineWrite/
  // resolveAgainstPendingCreate, which set this header on every response
  // they return) rather than a real server round-trip. Refresh the
  // "unsynced changes" id caches synchronously here, before the caller's own
  // success path re-renders (toggleDone/removeItem/changeQuantity/the
  // create-item form handler, ...), so the pending dot this exact action
  // just affected is already correct the moment that render happens instead
  // of only catching up a moment later once the service worker's own
  // trakka-sync-status broadcast arrives (see handleSyncStatusMessage).
  if (response.headers.get('X-Trakka-Queued') === 'true') {
    await refreshPendingChangeIndicators();
  }

  return body;
}

// True for the Error apiRequest throws when fetch() itself failed, as
// opposed to the server answering with a non-2xx status. A read-path caller
// (any GET-driven loader: loadDashboard here, and planning.js/urgent.js/
// spaces.js's loaders) should never surface the blocking error banner for
// this case while navigating — the header's network dot + "Hors-ligne"
// label already say so on their own, and popping a banner on top of an
// already-rendered cached view reads as an intrusive false alarm rather
// than useful information. A genuine server-side error (5xx, a bug) has no
// such flag and should still be surfaced.
function isNetworkError(err) {
  return Boolean(err && err.isNetworkError);
}

// ---------------------------------------------------------------------------
// Network status + offline sync indicators
//
// Three related but distinct pieces of UI, all driven by the same offline
// sync queue (db.js's STORE_QUEUE, owned exclusively by sw.js):
//
//   1. The header's #pending-badge cycles through 'pending' (☁️⏳, amber —
//      at least one change is queued and nothing is being sent right now),
//      'syncing' (🔄, spinning — sw.js's flushQueue is actively replaying
//      the queue) and 'synced' (☁️✓, briefly, before fading back to plain
//      "En ligne") — see setSyncIndicatorState/handleSyncStatusMessage.
//   2. pendingListIds/pendingItemIds are the id sets buildListCard (below)
//      and list_view.js's renderItems/buildItemRow consult to show a small
//      "unsynced changes" dot on a specific list's card/detail header or a
//      specific item's row — see hasPendingListChanges/hasPendingItemChanges.
//   3. The public `trakka:sync-pending`/`trakka:sync-complete` window
//      events, dispatched from handleSyncStatusMessage, let any other code
//      (this app's own, or a future addition) react to the queue changing
//      without having to know about IndexedDB or the service worker at all.
//
// sw.js tags every queue entry it writes with a listId (and, for an
// item-scoped write, an itemId) precisely so refreshPendingChangeIndicators
// below can derive both id sets with one read of the queue, and posts a
// 'trakka-sync-status' message (handled in registerServiceWorker further
// down) every time the queue's size changes — on every enqueue/cancel, and
// at the start/end of every flushQueue attempt.
// ---------------------------------------------------------------------------

async function updateNetworkStatus() {
  let reachable = navigator.onLine;
  if (reachable) {
    try {
      const res = await fetch('/healthz', { cache: 'no-store' });
      reachable = res.ok;
    } catch {
      reachable = false;
    }
  }

  els.networkDot.classList.toggle('bg-emerald-400', reachable);
  els.networkDot.classList.toggle('bg-amber-400', !reachable);
  els.networkLabel.textContent = reachable ? t('header.online') : t('header.offline');

  await refreshPendingBadge();
}

let pendingListIds = new Set();
let pendingItemIds = new Set();

// Reads the offline sync queue directly from IndexedDB (via db.js) and
// rebuilds pendingListIds/pendingItemIds from it — the service worker owns
// writing to that queue and to the listId/itemId tag on each entry, this
// only ever reads it. Returns the queue's total size so callers that only
// need the count (refreshPendingBadge) don't have to read it a second time.
async function refreshPendingChangeIndicators() {
  if (!window.TrakkaDB) {
    pendingListIds = new Set();
    pendingItemIds = new Set();
    return 0;
  }
  let queue = [];
  try {
    queue = await window.TrakkaDB.getQueue();
  } catch {
    queue = [];
  }
  pendingListIds = new Set(queue.filter((entry) => entry.listId != null).map((entry) => entry.listId));
  pendingItemIds = new Set(queue.filter((entry) => entry.itemId != null).map((entry) => entry.itemId));
  return queue.length;
}

// listId may be a real numeric id or a temp-list-* id (a list created while
// offline, not yet synced) — both are valid Set members, compared by strict
// equality same as any other id lookup in this app (see buildListCard).
function hasPendingListChanges(listId) {
  return pendingListIds.has(listId);
}

function hasPendingItemChanges(itemId) {
  return pendingItemIds.has(itemId);
}

// Auto-hide delay for the transient 'synced' (☁️✓) state before the header
// badge fades back to nothing — long enough to actually notice, short
// enough not to linger once there's nothing left to report.
const SYNC_SYNCED_DISPLAY_MS = 2500;
let syncSyncedTimer = null;

// Drives #pending-badge through its four states. 'idle' hides the badge
// entirely (nothing queued, nothing just finished); the other three show it
// with a state-specific icon/label/tooltip — see the section doc comment
// above. Tailwind's built-in animate-spin/animate-pulse utilities (no
// custom CSS needed) provide the syncing/pending motion.
function setSyncIndicatorState(state, count = 0) {
  clearTimeout(syncSyncedTimer);
  syncSyncedTimer = null;
  els.pendingBadge.dataset.syncState = state;
  els.pendingBadgeIcon.classList.remove('animate-spin', 'animate-pulse');

  if (state === 'idle') {
    els.pendingBadge.hidden = true;
    els.pendingBadge.title = '';
    return;
  }

  els.pendingBadge.hidden = false;
  if (state === 'pending') {
    els.pendingBadgeIcon.textContent = '☁️⏳';
    els.pendingBadgeIcon.classList.add('animate-pulse');
    els.pendingBadgeText.textContent = t('header.pending', { count });
    els.pendingBadge.title = t('header.pending', { count });
  } else if (state === 'syncing') {
    els.pendingBadgeIcon.textContent = '🔄';
    els.pendingBadgeIcon.classList.add('animate-spin');
    els.pendingBadgeText.textContent = t('header.syncing');
    els.pendingBadge.title = t('header.syncing');
  } else if (state === 'synced') {
    els.pendingBadgeIcon.textContent = '☁️✓';
    els.pendingBadgeText.textContent = t('header.synced');
    els.pendingBadge.title = t('header.synced');
    syncSyncedTimer = setTimeout(() => setSyncIndicatorState('idle'), SYNC_SYNCED_DISPLAY_MS);
  }
}

// refreshPendingBadge is the plain "just show the current count" entry
// point, called from updateNetworkStatus and after a handful of ordinary
// (online) mutations elsewhere in this file — it never forces a 'syncing'/
// 'synced' transition of its own (only handleSyncStatusMessage's
// service-worker-driven messages do that), and defers entirely while one of
// those transient states is already showing so it can't cut a 🔄 spin or a
// ☁️✓ confirmation short.
async function refreshPendingBadge() {
  const count = await refreshPendingChangeIndicators();
  const current = els.pendingBadge.dataset.syncState;
  if (current === 'syncing' || current === 'synced') return;
  setSyncIndicatorState(count > 0 ? 'pending' : 'idle', count);
}

// Handles sw.js's 'trakka-sync-status' postMessage (see broadcastQueueState
// in sw.js) — the single source of truth for the header badge's syncing/
// pending/synced transitions and for the public trakka:sync-pending/
// trakka:sync-complete window events. Also refreshes the per-list/per-item
// dot caches and repaints whatever's currently on screen, since this
// message is exactly "the queue changed" regardless of which of
// enqueue/cancel/flush caused it.
function handleSyncStatusMessage({ pending, syncing }) {
  if (syncing) {
    setSyncIndicatorState('syncing', pending);
    window.dispatchEvent(new CustomEvent('trakka:sync-pending', { detail: { count: pending, syncing: true } }));
  } else if (pending === 0) {
    // Only animate the ☁️✓ "synced" confirmation when something was
    // actually showing beforehand (pending or mid-sync) — an idle-to-idle
    // transition (e.g. a queue that was already empty) needs no fanfare.
    const wasActive = els.pendingBadge.dataset.syncState && els.pendingBadge.dataset.syncState !== 'idle';
    setSyncIndicatorState(wasActive ? 'synced' : 'idle', 0);
    if (wasActive) window.dispatchEvent(new CustomEvent('trakka:sync-complete', { detail: {} }));
  } else {
    setSyncIndicatorState('pending', pending);
    window.dispatchEvent(new CustomEvent('trakka:sync-pending', { detail: { count: pending, syncing: false } }));
  }

  refreshPendingChangeIndicators().then(repaintPendingChangeIndicators);
}

// A network-free repaint of whichever surface is currently visible, so a
// list's/item's "unsynced changes" dot appears (or clears) the moment the
// queue changes rather than waiting for that view's next full network
// refresh. renderItems (list_view.js) and renderDashboardFromCache (below)
// both read purely from already-loaded state/IndexedDB — safe to call at
// any time, the same guarantee every optimistic mutation in this app
// already relies on.
function repaintPendingChangeIndicators() {
  if (state.currentListId !== null) {
    renderItems();
  } else if (isDashboardTabActive()) {
    renderDashboardFromCache();
  }
}

// ---------------------------------------------------------------------------
// Houses (top-of-dashboard selector that scopes which lists are shown)
// ---------------------------------------------------------------------------

function populateHouseSelect(houses) {
  els.houseSelect.replaceChildren();
  for (const house of houses) {
    const option = document.createElement('option');
    option.value = String(house.id);
    option.textContent = house.name;
    els.houseSelect.appendChild(option);
  }
  const createOption = document.createElement('option');
  createOption.value = CREATE_HOUSE_OPTION_VALUE;
  createOption.textContent = t('dashboard.createHouseOption');
  els.houseSelect.appendChild(createOption);
}

// Reads the IndexedDB houses mirror, defaulting to [] whenever the module
// isn't available or the read itself fails (e.g. private-browsing profiles
// without IndexedDB) — the shared fallback used whenever a network call
// can't reach the server, so the dashboard degrades to "last known" data
// instead of going blank.
async function cachedHouses() {
  if (!window.TrakkaDB) return [];
  try {
    return await window.TrakkaDB.getHouses();
  } catch {
    return [];
  }
}

// Loads the house list, restores the last-selected house from localStorage
// when it still exists, and otherwise falls back to the first house. Does
// NOT load the dashboard itself — callers do that afterward once
// state.currentHouseId is settled.
async function loadHouses() {
  let houses;
  try {
    houses = await apiRequest('/houses');
  } catch (err) {
    // Offline, or a transient server error (non-2xx): fall back to
    // whatever the IndexedDB mirror last saw rather than wiping the
    // dashboard to empty — see the Offline-First requirement in
    // CLAUDE.md/docs/PWA.md. Never surface the banner for a plain
    // connectivity failure (the header's network badge already covers
    // that); only a genuine server-side error with nothing cached to show
    // for it still gets one.
    houses = await cachedHouses();
    if (houses.length === 0 && !isNetworkError(err)) showError(err.message);
  }

  state.houses = houses;
  populateHouseSelect(houses);

  const stored = Number(localStorage.getItem(HOUSE_STORAGE_KEY));
  const storedIsValid = houses.some((house) => house.id === stored);
  state.currentHouseId = storedIsValid ? stored : (houses[0]?.id ?? null);

  els.houseSelect.value = state.currentHouseId !== null ? String(state.currentHouseId) : CREATE_HOUSE_OPTION_VALUE;
  updateManageMembersButton();
  updateRenameHouseButton();
}

// Tracks whether loadHouses() has resolved at least once for this page
// load, and the in-flight promise while it hasn't. state.currentHouseId is
// only ever authoritative once loadHouses() has validated it against a
// live GET /api/v1/houses (see loadHouses above) — before that it's either
// null or whatever hydrateFromCache() derived from the IndexedDB mirror, a
// value that can be stale (e.g. left over from a different account that
// was previously signed in on this same browser) and isn't guaranteed to
// still belong to the current session. Several independent triggers can
// fire a house-scoped fetch — the initial load itself, a language switch,
// the tab regaining visibility, the 'online' event, and a
// trakka-sync-complete message from the service worker — and any of them
// firing before the first loadHouses() has resolved would hit the backend
// with a house_id the caller doesn't actually have access to yet, which
// correctly 403s but surfaces as a spurious "not a member of this house"
// error banner. ensureHousesLoaded() is the single choke point every
// house-scoped loader (loadDashboard below, notifications.js's
// loadNotifications) awaits first, so none of them can ever run ahead of
// the one authoritative resolution regardless of which trigger fires it.
let housesLoadedOnce = false;
let housesLoadingPromise = null;

async function ensureHousesLoaded() {
  if (housesLoadedOnce) return;
  if (!housesLoadingPromise) housesLoadingPromise = loadHouses();
  await housesLoadingPromise;
  housesLoadedOnce = true;
}

function selectHouse(houseId) {
  state.currentHouseId = houseId;
  localStorage.setItem(HOUSE_STORAGE_KEY, String(houseId));
  els.houseSelect.value = String(houseId);
  updateManageMembersButton();
  updateRenameHouseButton();
  // Switching houses mid-edit is only reachable programmatically (the
  // select is hidden while #rename-house-inline-form is open), but reset
  // defensively so a stale edit never lingers pointed at the wrong house.
  closeRenameHouseInline();
}

function updateManageMembersButton() {
  els.manageMembersButton.hidden = state.currentHouseId === null;
}

// Owner-only, same gate as the "remove member"/invite-form visibility in
// the Members modal — a plain member can view the house name but not
// rename it (see internal/handlers.authorizeHouseOwner).
function updateRenameHouseButton() {
  els.renameHouseInlineButton.hidden = currentHouseRole() !== 'owner';
}

els.houseSelect.addEventListener('change', async (event) => {
  const { value } = event.target;
  if (value === CREATE_HOUSE_OPTION_VALUE) {
    els.houseSelect.value = state.currentHouseId !== null ? String(state.currentHouseId) : CREATE_HOUSE_OPTION_VALUE;
    openNewHouseModal();
    return;
  }
  selectHouse(Number(value));
  await refreshVisibleView();
  // refreshNotifications is defined in notifications.js, resolved lazily
  // the same way refreshVisibleView's own cross-file calls already are.
  refreshNotifications();
});

function openNewHouseModal() {
  els.createHouseForm.reset();
  els.newHouseModal.hidden = false;
  document.body.classList.add('overflow-hidden');
  els.houseNameInput.focus();
}

function closeNewHouseModal() {
  els.newHouseModal.hidden = true;
  document.body.classList.remove('overflow-hidden');
}

els.closeHouseModalButton.addEventListener('click', closeNewHouseModal);
els.newHouseModal.addEventListener('click', (event) => {
  if (event.target === els.newHouseModal) closeNewHouseModal();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !els.newHouseModal.hidden) closeNewHouseModal();
});

els.createHouseForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  hideError();

  const name = els.houseNameInput.value.trim();
  if (!name) return;

  try {
    const house = await apiRequest('/houses', { method: 'POST', body: JSON.stringify({ name }) });
    await loadHouses();
    selectHouse(house.id);
    closeNewHouseModal();
    await refreshVisibleView();
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  }
});

// ---------------------------------------------------------------------------
// House members (roster + invite-by-email; remove is owner-only)
// ---------------------------------------------------------------------------

function currentHouseRole() {
  return state.houses.find((house) => house.id === state.currentHouseId)?.role ?? null;
}

function currentHouseName() {
  return state.houses.find((house) => house.id === state.currentHouseId)?.name ?? '';
}

// Inline rename of the current house, directly on the dashboard header —
// deliberately not tucked inside the Members modal (that was last
// session's first pass, moved out here for discoverability: renaming is a
// one-click action from the same row as the house selector, no modal to
// open first). #house-toolbar (the label + <select> + pencil + "Membres"
// row) and #rename-house-inline-form are mutually exclusive; toggling one
// hidden and the other visible swaps between browse and edit mode in
// place, the same "one real row, two states" pattern list_view.js's
// quick-add bar uses for its collapsed/expanded advanced panel.
function openRenameHouseInline() {
  if (state.currentHouseId === null) return;
  els.renameHouseInlineInput.value = currentHouseName();
  els.houseToolbar.hidden = true;
  els.renameHouseInlineForm.hidden = false;
  els.renameHouseInlineInput.focus();
  els.renameHouseInlineInput.select();
}

function closeRenameHouseInline() {
  els.renameHouseInlineForm.hidden = true;
  els.houseToolbar.hidden = false;
}

function isRenameHouseInlineOpen() {
  return !els.renameHouseInlineForm.hidden;
}

els.renameHouseInlineButton.addEventListener('click', openRenameHouseInline);
els.cancelRenameHouseInlineButton.addEventListener('click', closeRenameHouseInline);
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && isRenameHouseInlineOpen()) closeRenameHouseInline();
});

els.renameHouseInlineForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  hideError();

  const name = els.renameHouseInlineInput.value.trim();
  if (!name || state.currentHouseId === null) return;

  let house;
  try {
    house = await apiRequest(`/houses/${state.currentHouseId}`, { method: 'PUT', body: JSON.stringify({ name }) });
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return;
  }

  // Patches state.houses in place (rather than a full loadHouses() round
  // trip) so the header selector's option label updates immediately.
  const stored = state.houses.find((h) => h.id === house.id);
  if (stored) stored.name = house.name;
  populateHouseSelect(state.houses);
  els.houseSelect.value = String(state.currentHouseId);

  closeRenameHouseInline();
  window.TrakkaToast?.success(t('dashboard.renameSuccess', { name: house.name }));
});

function buildMemberRow(member, isOwnerView) {
  const li = document.createElement('li');
  li.className = 'flex items-center justify-between gap-2 rounded-xl border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 px-3 py-2';

  const info = document.createElement('div');
  const name = document.createElement('p');
  name.className = 'text-sm font-medium text-slate-900 dark:text-slate-100';
  name.textContent = member.display_name || member.email;
  const email = document.createElement('p');
  email.className = 'text-xs text-slate-500 dark:text-slate-400';
  // A pending entry is an invitation nobody has accepted yet: it becomes a
  // real membership the next time the invited person signs in (see
  // db.MaterializePendingInvitations). Labelling it is what stops a
  // successful invitation from looking like it did nothing at all.
  if (member.pending) {
    email.textContent = `${member.email} · ${t('modals.members.pendingBadge')}`;
  } else if (member.role === 'owner') {
    email.textContent = `${member.email} · ${t('modals.members.ownerBadge')}`;
  } else {
    email.textContent = member.email;
  }
  info.append(name, email);
  li.appendChild(info);

  if (isOwnerView && member.role !== 'owner') {
    const removeBtn = document.createElement('button');
    removeBtn.type = 'button';
    removeBtn.setAttribute('aria-label', t('common.removeMember', { email: member.email }));
    removeBtn.className = 'flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-slate-500 hover:bg-rose-500/10 hover:text-rose-600 dark:hover:text-rose-400';
    removeBtn.innerHTML = TRASH_ICON_SVG;
    removeBtn.addEventListener('click', () =>
      member.pending ? revokeHouseInvitation(member.email) : removeMember(member.user_id)
    );
    li.appendChild(removeBtn);
  }

  return li;
}

// A pending invitation has no user id to address it by, so it is withdrawn
// by email rather than through the members endpoint.
async function revokeHouseInvitation(email) {
  if (state.currentHouseId === null) return;
  hideError();
  try {
    await apiRequest(`/houses/${state.currentHouseId}/invitations?email=${encodeURIComponent(email)}`, {
      method: 'DELETE',
    });
    await loadMembers();
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  }
}

async function loadMembers() {
  if (state.currentHouseId === null) return;
  let members;
  try {
    members = await apiRequest(`/houses/${state.currentHouseId}/members`);
  } catch (err) {
    // No offline mirror for members — a plain connectivity failure just
    // leaves the modal empty without a blocking banner (the header's
    // network badge already covers it); a genuine server-side error still
    // gets one.
    if (!isNetworkError(err)) showError(err.message);
    return;
  }

  const isOwnerView = currentHouseRole() === 'owner';
  els.membersList.replaceChildren();
  for (const member of members) {
    els.membersList.appendChild(buildMemberRow(member, isOwnerView));
  }
  els.inviteMemberForm.hidden = !isOwnerView;
}

function openMembersModal() {
  els.inviteMemberForm.reset();
  els.membersModal.hidden = false;
  document.body.classList.add('overflow-hidden');
  loadMembers();
}

function closeMembersModal() {
  els.membersModal.hidden = true;
  document.body.classList.remove('overflow-hidden');
}

els.manageMembersButton.addEventListener('click', openMembersModal);
els.closeMembersModalButton.addEventListener('click', closeMembersModal);
els.membersModal.addEventListener('click', (event) => {
  if (event.target === els.membersModal) closeMembersModal();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !els.membersModal.hidden) closeMembersModal();
});

els.inviteMemberForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  hideError();

  const email = els.inviteEmailInput.value.trim();
  if (!email || state.currentHouseId === null) return;

  try {
    await apiRequest(`/houses/${state.currentHouseId}/members`, { method: 'POST', body: JSON.stringify({ email }) });
    els.inviteMemberForm.reset();
    await loadMembers();
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  }
});

async function removeMember(userId) {
  hideError();
  try {
    await apiRequest(`/houses/${state.currentHouseId}/members/${userId}`, { method: 'DELETE' });
    await loadMembers();
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  }
}

// ---------------------------------------------------------------------------
// Dashboard (lists grouped by type, with per-card badges)
// ---------------------------------------------------------------------------

// Shared by buildListCard's card subtitle and list_view.js's list-detail
// header: a list's "count" is how many items still need attention, not its
// total size — a fully-checked-off shopping list should read "0 remaining",
// not "12 items". `items` here is expected already filtered to exclude
// any item mid-undo-grace-period (see renderItems' `pendingDelete` filter),
// so a card built from the dashboard's own full-detail fetch (which never
// carries that flag to begin with) can just pass `list.items || []` as-is.
// items.remainingCountOne/remainingCountOther is this file's one hand-rolled
// plural: the underlying t() engine has no ICU plural support, so the
// caller (here) picks which key to use based on the actual count instead.
function remainingItemsLabel(items) {
  const remaining = items.filter((item) => !item.done).length;
  return t(remaining === 1 ? 'items.remainingCountOne' : 'items.remainingCountOther', { count: remaining });
}

function badge(text, palette) {
  const colors = {
    sky: 'bg-sky-500/10 text-sky-600 dark:text-sky-300',
    slate: 'bg-slate-200/60 dark:bg-slate-700/50 text-slate-600 dark:text-slate-300',
    emerald: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-300',
    violet: 'bg-violet-500/10 text-violet-600 dark:text-violet-300',
    orange: 'bg-orange-500/10 text-orange-600 dark:text-orange-300',
    amber: 'bg-amber-500/10 text-amber-600 dark:text-amber-300',
  };
  const span = document.createElement('span');
  span.className = `inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${colors[palette] || colors.slate}`;
  span.textContent = text;
  return span;
}

// One badge per list `type`, always shown on a card regardless of which
// dashboard section it lands in — the "Achats & Sourcing" grid already
// mixes 'groceries'/'shopping'/'recurring_shopping' together under one
// heading (see isPurchaseList below), and the Espaces tab (spaces.js) mixes
// every type in a single space (e.g. a "Homelab" space holding a shopping
// list, a todo list and a notes list side by side), so this is the one
// place that tells them apart at a glance without opening the card. Labels
// are dedicated, short `dashboard.listType*` i18n keys rather than the
// "new list" modal's own (longer, more descriptive) type-picker strings —
// the modal's radio grid has room for "Abonnements & Récurrences", a small
// pill on a card doesn't. Icons/colors are still kept in sync with that
// modal's own type-picker icons (static/index.html's #create-list-form) so
// the same type never shows two different icons in two places.
const LIST_TYPE_BADGE_META = {
  shopping: { icon: '🛒', key: 'dashboard.listTypeShopping', palette: 'sky' },
  recurring_shopping: { icon: '🔄', key: 'dashboard.listTypeRecurring', palette: 'violet' },
  groceries: { icon: '🛍️', key: 'dashboard.listTypeGroceries', palette: 'emerald' },
  todo: { icon: '✅', key: 'dashboard.listTypeTodo', palette: 'orange' },
  custom: { icon: '📝', key: 'dashboard.listTypeCustom', palette: 'slate' },
};

function typeBadge(type) {
  const meta = LIST_TYPE_BADGE_META[type];
  return meta ? badge(`${meta.icon} ${t(meta.key)}`, meta.palette) : null;
}

// A list's own `icon` (set via the create/edit list modal) takes priority
// over its type's fixed default — see LIST_TYPE_BADGE_META above — so a
// list only falls back to a generic type icon until the user picks its own.
function listIcon(list) {
  return list.icon || LIST_TYPE_BADGE_META[list.type]?.icon || '📋';
}

// Shopping cards show which sites their items link to (up to 3 domains,
// deduplicated, with a "+N" overflow badge) plus the list's "reste à
// dépenser" — the sum of price * quantity across every still-unchecked item
// that has a price ("combien reste-t-il à dépenser", not the list's full
// lifetime cost — a done/purchased item never contributes). This is the same
// figure the list detail view's own finance summary shows via
// lineTotal/updateFinanceSummary in list_view.js, computed server-side
// (list.total_amount, from internal/db's listSelect's `done = 0` filter)
// rather than re-summed here, so this card and the finance summary can never
// drift apart from slightly different client-side logic.
//
// Because `total_amount` only ever sums *unchecked* priced items, it comes
// back `null` both when the list genuinely has nothing left to buy (every
// item checked off) and when it simply has no priced items at all yet —
// those two cases used to render identically (an empty badge row, or
// whatever domain badges happened to exist), leaving a fully-completed list
// looking indistinguishable from a half-empty one. This function now tells
// them apart explicitly: an empty list (no items at all) gets a discreet
// "0 article" pill, and a non-empty list whose items are *all* done — not
// just whose unchecked items have no price — gets a distinct green success
// badge instead of the price total, checked before total_amount so it can
// never be shadowed by a still-null total.
function urlBadges(list) {
  const items = list.items || [];
  const domains = [];
  for (const item of items) {
    if (item.url) {
      try {
        domains.push(new URL(item.url).hostname.replace(/^www\./, ''));
      } catch {
        // malformed URL slipped through some other path; skip it silently
      }
    }
  }
  const unique = [...new Set(domains)];
  const frag = document.createDocumentFragment();
  for (const domain of unique.slice(0, 3)) {
    frag.appendChild(badge(domain, 'sky'));
  }
  if (unique.length > 3) {
    frag.appendChild(badge(`+${unique.length - 3}`, 'slate'));
  }
  // formatEuro is defined in list_view.js, loaded after this file — safe to
  // call here since this only ever runs at render time, well after every
  // script has finished loading (same deferred-call pattern buildItemRow
  // already uses for monthLabel/buildRecurrenceBadge from planning.js).
  // Deliberately the same 'emerald' palette as every other money-positive/
  // success accent in the app (the auto-detected-price sparkle icon, the
  // all-items-done todo badge, ...) rather than a neutral/gray pill, so a
  // list card's price total (or completion badge) reads as "positive" at a
  // glance instead of blending in with the plain domain/type badges around
  // it — this is a deliberate exception to --tk-money-total (tokens.css),
  // which still backs every *other* money figure (the finance summary, item
  // rows, the budget-planning totals), where "Total estimé"/"Déjà dépensé"
  // need to stay visually distinct from "Reste à dépenser" within the same
  // view — this card only ever shows one of the three, so there's no such
  // distinction to preserve here. font-semibold (Tailwind's 600) is bumped
  // explicitly since badge()'s shared font-medium (500) reads too light for
  // a figure/status meant to be spotted quickly.
  if (items.length === 0) {
    frag.appendChild(badge(t('dashboard.listEmptyBadge'), 'slate'));
  } else if (items.every((item) => item.done)) {
    const doneBadge = badge(t('dashboard.listAllDoneBadge'), 'emerald');
    doneBadge.classList.add('font-semibold');
    frag.appendChild(doneBadge);
  } else if (typeof list.total_amount === 'number') {
    const totalBadge = badge(formatEuro(list.total_amount), 'emerald');
    totalBadge.classList.add('font-semibold');
    frag.appendChild(totalBadge);
  }
  return frag;
}

function progressBadge(list) {
  const items = list.items || [];
  const frag = document.createDocumentFragment();
  if (items.length === 0) return frag;
  const done = items.filter((item) => item.done).length;
  frag.appendChild(badge(t('dashboard.todoProgress', { done, total: items.length }), done === items.length ? 'emerald' : 'slate'));
  return frag;
}

// custom (freeform notes) lists have no url or completion concept — see
// FIELD_VISIBILITY_BY_TYPE in list_view.js — so unlike urlBadges/
// progressBadge there is nothing meaningful to summarize beyond the item
// count buildListCard already shows; this only exists so renderGrid can be
// called uniformly with a badge function for every dashboard section.
function noBadges() {
  return document.createDocumentFragment();
}

function emptyState(message) {
  const li = document.createElement('li');
  li.className = 'col-span-full rounded-xl border border-dashed border-slate-200 dark:border-slate-800 p-6 text-center text-sm text-slate-500';
  li.textContent = message;
  return li;
}

function buildListCard(list, badgesFragment) {
  const li = document.createElement('li');
  li.className = 'rounded-2xl border border-slate-200 dark:border-slate-800 bg-slate-100/70 dark:bg-slate-800/40 shadow-sm transition hover:border-slate-300 dark:hover:border-slate-700 hover:bg-slate-200 dark:hover:bg-slate-800/70';

  const row = document.createElement('div');
  row.className = 'flex items-start justify-between gap-2 p-4';

  const openBtn = document.createElement('button');
  openBtn.type = 'button';
  openBtn.className = 'min-h-[44px] flex-1 text-left';
  openBtn.addEventListener('click', () => selectList(list.id));

  // Shown above the name on every card, in every dashboard section, since
  // even the "Achats & Sourcing" grid mixes several types together (see
  // isPurchaseList) and the Espaces tab mixes all of them — see
  // LIST_TYPE_BADGE_META's comment above for why this can't just live
  // inside badgesRow below.
  const typeRow = document.createElement('div');
  typeRow.className = 'mb-2 flex flex-wrap items-center gap-1.5 empty:hidden';
  const typeBadgeEl = typeBadge(list.type);
  if (typeBadgeEl) typeRow.appendChild(typeBadgeEl);
  // list.access_source is set by db.ListSharedListsForUser (see shares.js's
  // "Partagé avec moi" tab: 'list_share'/'space_share') and
  // db.ListPinnedHouseSpaceLists ('house_member', see the "pinned house
  // spaces" bullet in CLAUDE.md) — the 👥 indicator CLAUDE.md's sharing
  // feature asks for, so a list reached some way other than the currently
  // selected House's own ordinary membership is recognizable at a glance
  // among your own.
  if (list.access_source) {
    typeRow.appendChild(badge(`👥 ${t('shares.sharedBadge')}`, 'violet'));
  }
  // is_pinned_to_dashboard only ever comes from db.ListSharedListsForUser
  // (see loadPinnedSharedLists/loadSharedView in shares.js) — a clear visual
  // cue that this card is pinned, independent of the pin button's own
  // active/inactive icon color, since a card can show up in either the
  // dashboard grids or the "Partagé avec moi" tab and this badge should read
  // the same way in both places.
  if (list.is_pinned_to_dashboard) {
    typeRow.appendChild(badge(`📌 ${t('shares.pinnedBadge')}`, 'amber'));
  }
  // hasPendingListChanges reads the offline sync queue's listId tags (see
  // the "Network status + offline sync indicators" section above) — shown
  // whenever this list itself, or any of its items, still has a write
  // sitting in the queue (an offline reorder/add/delete/toggle), and
  // cleared automatically the next time that queue empties out, since
  // refreshPendingChangeIndicators/repaintPendingChangeIndicators re-derive
  // this set from scratch on every queue mutation.
  if (hasPendingListChanges(list.id)) {
    const unsyncedBadge = badge(`⏳ ${t('sync.listBadgeLabel')}`, 'amber');
    unsyncedBadge.classList.add('animate-pulse');
    unsyncedBadge.title = t('sync.listBadgeAriaLabel', { name: list.name });
    typeRow.appendChild(unsyncedBadge);
  }

  const titleRow = document.createElement('div');
  titleRow.className = 'flex min-w-0 items-center gap-2';

  const iconSpan = document.createElement('span');
  iconSpan.setAttribute('aria-hidden', 'true');
  iconSpan.className = 'shrink-0 text-lg leading-none';
  iconSpan.textContent = listIcon(list);

  const title = document.createElement('h3');
  title.className = 'truncate text-base font-semibold text-slate-900 dark:text-slate-100';
  title.textContent = list.name;

  titleRow.append(iconSpan, title);

  const count = document.createElement('p');
  count.className = 'mt-1 text-sm text-slate-500 dark:text-slate-400';
  count.textContent = remainingItemsLabel(list.items || []);

  const badgesRow = document.createElement('div');
  badgesRow.className = 'mt-3 flex flex-wrap gap-1.5 empty:hidden';
  badgesRow.appendChild(badgesFragment);

  openBtn.append(typeRow, titleRow, count, badgesRow);

  const actions = document.createElement('div');
  actions.className = 'flex shrink-0 items-center gap-1';

  // A card reached via db.ListSharedListsForUser (list.access_source set —
  // see shares.js's "Partagé avec moi" tab) shows no edit/share/delete
  // controls at all: managing, editing or deleting a list requires actual
  // House membership (see internal/handlers/shares.go's
  // handleListShareCreate and handleListsDelete), which by definition this
  // list showing up there means the viewer doesn't have. openShareModal is
  // defined in shares.js, resolved lazily the same way openListModal above
  // already is.
  if (!list.access_source) {
    const editBtn = document.createElement('button');
    editBtn.type = 'button';
    editBtn.setAttribute('aria-label', t('common.editList', { name: list.name }));
    editBtn.className = 'flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-slate-500 hover:bg-slate-200 dark:hover:bg-slate-700 hover:text-slate-900 dark:hover:text-slate-200';
    editBtn.innerHTML = PENCIL_ICON_SVG;
    editBtn.addEventListener('click', () => openListModal(list));
    actions.appendChild(editBtn);

    const shareBtn = document.createElement('button');
    shareBtn.type = 'button';
    shareBtn.setAttribute('aria-label', t('common.shareList', { name: list.name }));
    shareBtn.className = 'flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-slate-500 hover:bg-violet-500/10 hover:text-violet-600 dark:hover:text-violet-400';
    shareBtn.innerHTML = SHARE_ICON_SVG;
    shareBtn.addEventListener('click', () => openShareModal({ kind: 'list', id: list.id, name: list.name }));
    actions.appendChild(shareBtn);

    const deleteBtn = document.createElement('button');
    deleteBtn.type = 'button';
    deleteBtn.setAttribute('aria-label', t('common.deleteList', { name: list.name }));
    deleteBtn.className = 'flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-slate-500 hover:bg-rose-500/10 hover:text-rose-600 dark:hover:text-rose-400';
    deleteBtn.innerHTML = TRASH_ICON_SVG;
    deleteBtn.addEventListener('click', () => removeList(list));
    actions.appendChild(deleteBtn);
  } else if (list.access_source === 'list_share' || list.access_source === 'space_share' || list.access_source === 'house_member') {
    // Pinning is offered for a list shared *directly* (a list_shares row
    // the recipient themselves holds), one reached only via a shared Space
    // (space_shares — PATCH /api/v1/lists/{id}/share/pin's backend,
    // db.SetListSharePinned, auto-creates the list_shares row that carries
    // the flag in that case, scoped to exactly the permission the Space
    // already grants), and one reached via a Space merely visible through
    // House membership rather than an explicit share (space_house_pins,
    // access_source 'house_member' — see the "pinned house spaces" bullet
    // in CLAUDE.md; this card only ever shows up here because the caller
    // pinned it, so this button always reads as "unpin" in practice, but
    // stays a plain toggle for consistency with the other two sources).
    // This is the recipient/viewer's own action: pinning makes the card
    // also show up on their own dashboard grids even while a different
    // House is currently selected. Note: if the list's parent Space is
    // *itself* pinned as a whole (see spaces.js's shared-space kebab menu),
    // this per-list toggle can't override that — db.ListSharedListsForUser/
    // db.ListPinnedHouseSpaceLists OR the relevant sources together, so
    // unpinning one list here while its Space stays pinned leaves the card
    // on the dashboard regardless, by design (the Space-level pin is the
    // "master" pin for everything reachable through it).
    //
    // Direct icon toggle on wider screens (hidden md:flex) plus a [⋮] kebab
    // opening #list-card-actions-sheet on narrow ones (flex md:hidden) —
    // the same responsive split buildItemRow's .item-card__actions/
    // .item-card__kebab already use in list_view.js, for the same reason: a
    // bare icon button is less discoverable/labeled than the same action
    // spelled out as text in a sheet once screen space is tight.
    const pinned = !!list.is_pinned_to_dashboard;

    const pinBtn = document.createElement('button');
    pinBtn.type = 'button';
    pinBtn.setAttribute('aria-label', t(pinned ? 'common.unpinList' : 'common.pinList', { name: list.name }));
    pinBtn.className =
      'hidden md:flex h-11 w-11 shrink-0 items-center justify-center rounded-lg hover:bg-amber-500/10 hover:text-amber-600 dark:hover:text-amber-400 ' +
      (pinned ? 'text-amber-500 dark:text-amber-400' : 'text-slate-500');
    pinBtn.innerHTML = PIN_ICON_SVG;
    pinBtn.addEventListener('click', () => toggleListPin(list));
    actions.appendChild(pinBtn);

    const kebabBtn = document.createElement('button');
    kebabBtn.type = 'button';
    kebabBtn.setAttribute('aria-label', t('common.listActionsAriaLabel', { name: list.name }));
    kebabBtn.className =
      'flex md:hidden h-11 w-11 shrink-0 items-center justify-center rounded-lg text-slate-500 hover:bg-slate-200 dark:hover:bg-slate-700';
    // KEBAB_ICON_SVG is defined in list_view.js, loaded after this file —
    // safe because it's only read here at click-time/render-time, well
    // after every script tag has finished loading (see PENCIL_ICON_SVG's
    // use just above for the same already-established cross-file pattern).
    kebabBtn.innerHTML = KEBAB_ICON_SVG;
    kebabBtn.addEventListener('click', () => openListCardActionsSheet(list));
    actions.appendChild(kebabBtn);
  }

  row.append(openBtn, actions);
  li.appendChild(row);
  return li;
}

function renderGrid(container, lists, badgeFn, emptyMessage) {
  container.replaceChildren();
  if (lists.length === 0) {
    container.appendChild(emptyState(emptyMessage));
    return;
  }
  for (const list of lists) {
    container.appendChild(buildListCard(list, badgeFn(list)));
  }
}

// Reads every list belonging to `houseId` from the IndexedDB mirror, each
// merged with its own cached items (db.js's getListWithItems) so the result
// is shaped exactly like a batch of successful `GET /api/v1/lists/{id}`
// responses. Used both for the instant paint on load and as loadDashboard's
// fallback when the network is unreachable.
async function cachedDashboardLists(houseId) {
  if (!window.TrakkaDB) return [];
  try {
    const lists = await window.TrakkaDB.getListsByHouse(houseId);
    const detailed = await Promise.all(lists.map((list) => window.TrakkaDB.getListWithItems(list.id)));
    return detailed.filter(Boolean);
  } catch {
    return [];
  }
}

// A list is purchase-oriented (lands in the "Achats & Sourcing" grid) when
// it's neither a todo list nor a custom/freeform one — 'shopping',
// 'groceries' and 'recurring_shopping' all qualify (see
// models.ValidListTypes). 'custom' gets its own dedicated "Notes & Listes
// Libres" grid instead (see renderDashboardGrids below) — a freeform note/
// idea list has nothing to do with purchasing or tasks, so it must never be
// folded into either existing section, totals, or filter.
function isPurchaseList(type) {
  return type !== 'todo' && type !== 'custom';
}

// Splits `detailed` (a house's lists, each with its items already attached)
// into the dashboard's three mutually exclusive grids and renders all of
// them — shared by the cache-only offline path and the normal network path
// below so the three-way split can't drift between them.
function renderDashboardGrids(detailed) {
  // applyDashboardOverrides layers in any not-yet-committed item edit made
  // in the list detail view (see notifyItemsChanged above) on top of
  // whatever this batch's own source (network or IndexedDB mirror) reports,
  // so a card's badge is never stale by up to 5s relative to what the user
  // just did.
  detailed = applyDashboardOverrides(detailed);
  renderGrid(els.shoppingLists, detailed.filter((l) => isPurchaseList(l.type)), urlBadges, t('dashboard.emptyShopping'));
  renderGrid(els.todoLists, detailed.filter((l) => l.type === 'todo'), progressBadge, t('dashboard.emptyTodo'));
  renderGrid(els.customLists, detailed.filter((l) => l.type === 'custom'), noBadges, t('dashboard.emptyCustom'));
}

// Optimistic dashboard reactivity: list_view.js's toggleDone/removeItem
// apply their own edit to the currently open list's items immediately (well
// before the real PATCH/DELETE actually goes out — both are deferred behind
// TrakkaUndo's 5s undo grace period), but the dashboard itself isn't visible
// while a list is open, so by the time the user navigates back and
// loadDashboard()/renderDashboardFromCache() run, a fetch (network or
// IndexedDB mirror) can still race ahead of that deferred commit and show
// the pre-edit state for up to 5s. notifyItemsChanged (called from
// list_view.js right after every such optimistic apply/undo) snapshots that
// list's items here so any dashboard render in the meantime already reflects
// it — recalculating the remaining-items count and swapping in the "all
// done" badge/price total accordingly (see urlBadges) with no
// location.reload() involved. clearItemsOverride drops the snapshot once the
// deferred commit actually resolves (success or failure), letting a genuine
// fetch be authoritative again rather than staying shadowed indefinitely.
const dashboardOptimisticOverrides = new Map();

function notifyItemsChanged(listId, items) {
  if (listId === null || listId === undefined) return;
  dashboardOptimisticOverrides.set(listId, items.map((item) => ({ ...item })));
  // isDashboardTabActive is defined in planning.js, resolved lazily here the
  // same way refreshVisibleView already calls into that file — safe since
  // this only ever runs well after every script has finished loading.
  if (isDashboardTabActive()) {
    renderDashboardFromCache();
  }
}

function clearItemsOverride(listId) {
  dashboardOptimisticOverrides.delete(listId);
}

function applyDashboardOverrides(lists) {
  return lists.map((list) => {
    if (!dashboardOptimisticOverrides.has(list.id)) return list;
    const items = dashboardOptimisticOverrides.get(list.id);
    return { ...list, items, total_amount: remainingAmount(items) };
  });
}

// Mirrors internal/db.listSelect's total_amount SUM (every priced,
// not-yet-done item's price * quantity; null when nothing qualifies) — see
// urlBadges' own doc comment for why the dashboard normally trusts the
// server-computed total rather than re-deriving it client-side. This is the
// one deliberate exception: dashboardOptimisticOverrides (above) holds an
// edit that hasn't round-tripped to the server, or even reached IndexedDB,
// yet — sw.js's own recomputeListTotalAmount only runs once the real
// PATCH/DELETE actually goes out, up to 5s later (see toggleDone's undo
// grace period in list_view.js) — so there is no fresher total_amount to
// fall back to in the meantime. lineTotal is defined in list_view.js, loaded
// after this file — safe to call here since this only ever runs at render
// time, well after every script has finished loading.
function remainingAmount(items) {
  let total = null;
  for (const item of items) {
    if (item.done || typeof item.price !== 'number') continue;
    total = (total ?? 0) + lineTotal(item);
  }
  return total;
}

// Renders the dashboard purely from the local IndexedDB mirror, with no
// network request involved — this is what keeps lists/items on screen while
// offline instead of the grid going blank. Lists mid-undo-grace-period (see
// removeList) are filtered out here too, same as the network path below.
async function renderDashboardFromCache() {
  if (state.currentHouseId === null) {
    renderDashboardGrids([]);
    return;
  }

  const detailed = (await cachedDashboardLists(state.currentHouseId)).filter(
    (list) => !pendingDeletedListIds.has(list.id)
  );
  renderDashboardGrids(detailed);
}

async function loadDashboard() {
  // See ensureHousesLoaded's own comment above: this guarantees
  // state.currentHouseId is never used for a network request until it's
  // been validated against a live GET /api/v1/houses, no matter which of
  // the several independent triggers called loadDashboard first.
  await ensureHousesLoaded();
  if (state.currentHouseId === null) {
    renderDashboardGrids([]);
    return;
  }

  // Stale-while-revalidate: paint immediately from whatever's already in
  // the local mirror (not just at boot, via hydrateFromCache/init, but on
  // every call — a house switch, returning from a list, a tab click back
  // to "Listes") so the grids never sit empty/stale-looking while the
  // network fetch below is in flight, before repainting for real once it
  // resolves. Cheap and idempotent: renderDashboardFromCache always ends
  // by calling renderGrid, which replaces every card from scratch anyway.
  await renderDashboardFromCache();

  let lists;
  try {
    lists = await apiRequest(`/lists?house_id=${state.currentHouseId}`);
  } catch (err) {
    // Offline, or a transient server error: keep the dashboard populated
    // from the local mirror rather than leaving it blank — see the
    // Offline-First requirement in CLAUDE.md/docs/PWA.md. A plain
    // connectivity failure stays silent (the header's discreet network
    // badge already says "Hors-ligne"); only a genuine server-side error
    // still raises the banner.
    await renderDashboardFromCache();
    if (!isNetworkError(err)) showError(err.message);
    return;
  }

  // Lists mid-undo-grace-period (see removeList) haven't actually been
  // deleted server-side yet, so a plain refetch would still include them —
  // filter them back out here rather than only hiding their card once.
  lists = lists.filter((list) => !pendingDeletedListIds.has(list.id));

  // Fetch each list's detail (items included) in parallel to compute
  // badges. Best effort per list: one failing shouldn't hide the rest —
  // and falls back to that single list's cached detail before giving up
  // and showing it with no items, so a partial network failure doesn't
  // erase items that are still sitting in the local mirror.
  const detailed = await Promise.all(
    lists.map((list) =>
      apiRequest(`/lists/${list.id}`).catch(async () => {
        const cached = window.TrakkaDB ? await window.TrakkaDB.getListWithItems(list.id).catch(() => null) : null;
        return cached || { ...list, items: [] };
      })
    )
  );

  // Alongside the current House's own lists, the dashboard also shows any
  // list shared directly with the caller that they've chosen to pin (see
  // buildListCard's 📌 button, toggleListPin, and loadPinnedSharedLists in
  // shares.js) — CLAUDE.md's "Pinning shared lists" feature — and any list
  // the caller reaches purely by pinning a Space visible to them through
  // House membership (loadPinnedHouseSpaceLists, access_source
  // 'house_member' — CLAUDE.md's "pinned house spaces" feature), which lets
  // a list from a *different* House the caller also belongs to show up here
  // without switching the house selector away from the currently selected
  // one. No offline mirror for either (same "requires connectivity" scoping
  // as the rest of the sharing feature), so neither appears while offline;
  // a best-effort failure in either must never block the rest of the
  // dashboard from rendering.
  const [pinnedShared, pinnedHouseSpace] = await Promise.all([
    loadPinnedSharedLists().catch(() => []),
    loadPinnedHouseSpaceLists().catch(() => []),
  ]);

  // A house_member-sourced list is, in the common single-House case,
  // already present in `detailed` above (it belongs to a House the caller
  // is an ordinary member of — pinning just means "show it even when a
  // *different* House is selected", not "duplicate the card") — dedupe by
  // id, keeping detailed's own copy (fetched with the currently selected
  // House's own context) over the pinned-lists ones.
  const seenListIds = new Set(detailed.map((list) => list.id));
  const extra = [];
  for (const list of [...pinnedShared, ...pinnedHouseSpace]) {
    if (seenListIds.has(list.id)) continue;
    seenListIds.add(list.id);
    extra.push(list);
  }

  renderDashboardGrids([...detailed, ...extra]);
}

// ---------------------------------------------------------------------------
// View switching (list detail rendering itself lives in list_view.js)
// ---------------------------------------------------------------------------

function refreshVisibleView() {
  if (state.currentListId !== null) {
    refreshCurrentList();
  } else if (isPlanningTabActive()) {
    // isPlanningTabActive/refreshPlanningIfActive are defined in
    // planning.js, resolved lazily here the same way showDashboard is
    // below — see that comment for why cross-file calls like this are safe.
    refreshPlanningIfActive();
  } else if (isUrgentTabActive()) {
    // isUrgentTabActive/refreshUrgentIfActive are defined in urgent.js,
    // resolved lazily the same way.
    refreshUrgentIfActive();
  } else if (isSpacesTabActive()) {
    // isSpacesTabActive/refreshSpacesIfActive are defined in spaces.js,
    // resolved lazily the same way.
    refreshSpacesIfActive();
  } else if (isSharedTabActive()) {
    // isSharedTabActive/refreshSharedIfActive are defined in shares.js,
    // resolved lazily the same way.
    refreshSharedIfActive();
  } else {
    loadDashboard();
  }
}

// The logo keeps its plain `href="/"` (full reload, works with JS disabled
// or a middle-click/new-tab), but a same-tab left-click intercepts it to
// hop back to the dashboard in place — showDashboard is defined in
// list_view.js, resolved lazily here the same way selectList is above.
els.logoLink.addEventListener('click', (event) => {
  event.preventDefault();
  hideError();
  showDashboard();
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

// Deletion is deferred behind a 5s undo grace period rather than sent
// immediately: the list disappears from the dashboard right away (via
// pendingDeletedListIds, since there's no per-list DOM node kept around to
// just re-show — loadDashboard always rebuilds the grid from a fresh
// fetch), and the actual DELETE only fires from TrakkaUndo's onCommit if
// the countdown runs out without the user clicking "Annuler". Because nothing
// hits the network until then, this needs no special handling for the
// offline queue in sw.js — the eventual apiRequest call is indistinguishable
// from one made right away, whether it goes out online or offline.
function removeList(list) {
  hideError();
  pendingDeletedListIds.add(list.id);
  loadDashboard();

  TrakkaUndo.schedule({
    message: t('undo.listDeleted', { name: list.name }),
    undoLabel: t('undo.cancel'),
    onUndo: () => {
      pendingDeletedListIds.delete(list.id);
      loadDashboard();
    },
    onCommit: async () => {
      try {
        await apiRequest(`/lists/${list.id}`, { method: 'DELETE' });
      } catch (err) {
        if (!isNetworkError(err)) showError(err.message);
      }
      pendingDeletedListIds.delete(list.id);
      await loadDashboard();
      await refreshPendingBadge();
    },
  });
}

// Pins or unpins a directly-shared list (or one reached via a House-visible
// Space — access_source 'house_member', see the "pinned house spaces"
// bullet in CLAUDE.md) on the caller's own dashboard (see buildListCard's
// pin button above and PATCH /api/v1/lists/{id}/share/pin). Unlike
// removeList this isn't optimistic/undo-able — it's a quick, infrequent
// toggle, so it follows shares.js's simpler await-then-refresh pattern (see
// revokeShare) rather than the coalesced-optimistic pattern item quantity/
// urgent toggles use for rapid-fire clicks. refreshVisibleView() (defined
// above) re-renders whichever tab is currently on screen; loadDashboard()
// is also re-run whenever that wasn't the dashboard already (isDashboardTabActive,
// defined in planning.js — avoids fetching it twice when it was) so the
// dashboard's own underlying data is fresh the instant the user next looks
// at it, without waiting on some unrelated future refresh. A toast confirms
// the action the same way house-rename's TrakkaToast.success does, since
// there's otherwise no visible feedback for a card that may not even be on
// screen right now (e.g. pinned from the "Partagé avec moi" tab).
async function toggleListPin(list) {
  hideError();
  const pinning = !list.is_pinned_to_dashboard;
  try {
    await apiRequest(`/lists/${list.id}/share/pin`, {
      method: 'PATCH',
      body: JSON.stringify({ pinned: pinning }),
    });
    await refreshVisibleView();
    if (!isDashboardTabActive()) await loadDashboard();
    TrakkaToast.success(t(pinning ? 'shares.pinnedToast' : 'shares.unpinnedToast', { name: list.name }));
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  }
}

// ---------------------------------------------------------------------------
// Shared-list card actions bottom sheet — the mobile-first replacement for
// the 📌 pin/unpin icon button shown directly on a shared list's card on
// wider screens (see buildListCard's list_share branch above, and
// #item-actions-sheet in list_view.js for the identical pattern this
// mirrors). Currently offers only the pin/unpin toggle, since that's the
// only action a shared list's card exposes at all — any future
// shared-list-card action should be added here too rather than growing a
// second sheet.
// ---------------------------------------------------------------------------

const listCardActionsEls = {
  sheet: document.getElementById('list-card-actions-sheet'),
  title: document.getElementById('list-card-actions-sheet-title'),
  closeButton: document.getElementById('close-list-card-actions-sheet-button'),
  pinButton: document.getElementById('list-card-actions-pin-button'),
  pinIcon: document.getElementById('list-card-actions-pin-icon'),
  pinLabel: document.getElementById('list-card-actions-pin-label'),
};

// The list currently open in the sheet, or null when it's closed — set by
// openListCardActionsSheet, read by the pin button's own click handler below
// (the same "track the acted-on item at module scope" pattern
// itemActionsSheetItem uses in list_view.js).
let listCardActionsSheetList = null;

function openListCardActionsSheet(list) {
  listCardActionsSheetList = list;
  listCardActionsEls.title.textContent = list.name;
  const pinned = !!list.is_pinned_to_dashboard;
  listCardActionsEls.pinIcon.textContent = pinned ? '📍' : '📌';
  listCardActionsEls.pinLabel.textContent = t(pinned ? 'modals.listActions.unpin' : 'modals.listActions.pin');
  listCardActionsEls.sheet.hidden = false;
  document.body.classList.add('overflow-hidden');
}

function closeListCardActionsSheet() {
  listCardActionsSheetList = null;
  listCardActionsEls.sheet.hidden = true;
  document.body.classList.remove('overflow-hidden');
}

listCardActionsEls.closeButton.addEventListener('click', closeListCardActionsSheet);
listCardActionsEls.sheet.addEventListener('click', (event) => {
  if (event.target === listCardActionsEls.sheet) closeListCardActionsSheet();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !listCardActionsEls.sheet.hidden) closeListCardActionsSheet();
});

listCardActionsEls.pinButton.addEventListener('click', () => {
  const list = listCardActionsSheetList;
  closeListCardActionsSheet();
  if (list) toggleListPin(list);
});

// ---------------------------------------------------------------------------
// "New/edit list" modal — one shared modal for both, mirroring
// spaces.js's openCategoryModal (editingList === null means "create").
// ---------------------------------------------------------------------------

// The list currently open in the modal for editing, or null when the modal
// is in "create" mode — set by openListModal, read by createListForm's
// submit handler.
let editingList = null;

function setListTypeSelection(value) {
  for (const label of els.typeOptions) {
    const input = label.querySelector('input');
    const active = input.value === value;
    input.checked = active;
    label.classList.toggle('border-sky-500', active);
    label.classList.toggle('bg-sky-500/10', active);
    label.classList.toggle('text-sky-600', active);
    label.classList.toggle('dark:text-sky-300', active);
    label.classList.toggle('border-slate-200', !active);
    label.classList.toggle('dark:border-slate-700', !active);
    label.classList.toggle('bg-white', !active);
    label.classList.toggle('dark:bg-slate-900', !active);
    label.classList.toggle('text-slate-600', !active);
    label.classList.toggle('dark:text-slate-300', !active);
  }
}

// Opens the modal in create mode (list === null) or edit mode (prefilled
// from an existing list, name/icon/type/custom_category_id all editable —
// house_id stays fixed, matching PUT /api/v1/lists/{id} not accepting it).
function openListModal(list) {
  editingList = list || null;

  els.createListForm.reset();
  els.listNameInput.value = list?.name || '';
  els.listIconInput.value = list?.icon || '';
  setListTypeSelection(list?.type || 'shopping');
  els.newListModalTitle.textContent = t(editingList ? 'modals.newList.titleEdit' : 'modals.newList.titleCreate');
  els.listSubmitButton.textContent = t(editingList ? 'modals.newList.submitEdit' : 'modals.newList.submitCreate');
  // populateCategorySelect/loadCustomCategories are defined in spaces.js,
  // resolved lazily the same way every other cross-file call in this file
  // already is — refetching here (rather than trusting whatever spaces.js
  // last cached) keeps the picker correct even if a category was created/
  // deleted in another tab since the last time the Spaces tab was opened.
  loadCustomCategories().then(() => populateCategorySelect(els.listCategorySelect, list?.custom_category_id ?? null));
  els.newListModal.hidden = false;
  document.body.classList.add('overflow-hidden');
  els.listNameInput.focus();
}

function closeNewListModal() {
  els.newListModal.hidden = true;
  document.body.classList.remove('overflow-hidden');
  editingList = null;
  els.newListButton.focus();
}

for (const label of els.typeOptions) {
  label.querySelector('input').addEventListener('change', (event) => setListTypeSelection(event.target.value));
}

els.newListButton.addEventListener('click', () => openListModal(null));
els.closeModalButton.addEventListener('click', closeNewListModal);
els.newListModal.addEventListener('click', (event) => {
  if (event.target === els.newListModal) closeNewListModal();
});
document.addEventListener('keydown', (event) => {
  // The category modal can open on top of this one (see the change listener
  // below) — when it's the one currently visible, let its own Escape
  // handler in spaces.js close just that one instead of both at once.
  if (event.key === 'Escape' && !els.newListModal.hidden && spacesEls.categoryModal.hidden) closeNewListModal();
});

for (const button of els.listIconPresetButtons) {
  button.addEventListener('click', () => {
    els.listIconInput.value = button.dataset.listIconPreset;
  });
}

// CREATE_CATEGORY_OPTION_VALUE is defined in spaces.js (the module that owns
// custom categories) — selecting it here is a shortcut into the "new space"
// modal without leaving the list-creation flow, the same sentinel-option
// pattern els.houseSelect already uses for CREATE_HOUSE_OPTION_VALUE.
els.listCategorySelect.addEventListener('change', (event) => {
  if (event.target.value !== CREATE_CATEGORY_OPTION_VALUE) return;
  event.target.value = '';
  openCategoryModal(null, { onCreated: (category) => populateCategorySelect(els.listCategorySelect, category.id) });
});

els.createListForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  hideError();

  const name = els.listNameInput.value.trim();
  const icon = els.listIconInput.value.trim();
  const type = els.newListModal.querySelector('input[name="list-type"]:checked')?.value || 'shopping';
  const categoryValue = els.listCategorySelect.value;
  const customCategoryId = categoryValue && categoryValue !== CREATE_CATEGORY_OPTION_VALUE ? Number(categoryValue) : null;
  if (!name) return;

  try {
    const isEdit = editingList !== null;
    if (isEdit) {
      await apiRequest(`/lists/${editingList.id}`, {
        method: 'PUT',
        body: JSON.stringify({ name, type, icon, custom_category_id: customCategoryId }),
      });
    } else {
      if (state.currentHouseId === null) return;
      await apiRequest('/lists', {
        method: 'POST',
        body: JSON.stringify({ name, type, icon, house_id: state.currentHouseId, custom_category_id: customCategoryId }),
      });
    }
    closeNewListModal();
    await loadDashboard();
    await refreshSpacesIfActive();
    // No-op if the currently open list detail view isn't the one just
    // edited (refreshCurrentList is a no-op when state.currentListId is
    // null) — see list_view.js.
    await refreshCurrentList();
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  }
  await refreshPendingBadge();
});

// ---------------------------------------------------------------------------
// Service worker registration
// ---------------------------------------------------------------------------

// Registers sw.js (app-shell caching + offline write queue) and wires the
// two ways a flush can be triggered on browsers without Background Sync
// (i.e. all of iOS/iPadOS Safari): the page's own 'online' event, and a
// notification once the service worker has actually flushed the queue.
function registerServiceWorker() {
  if (!('serviceWorker' in navigator)) return;

  navigator.serviceWorker.register('/sw.js')
    .then((registration) => watchForServiceWorkerUpdate(registration))
    .catch((err) => {
      console.error('Échec de l’enregistrement du service worker :', err);
    });

  navigator.serviceWorker.addEventListener('message', (event) => {
    if (event.data && event.data.type === 'trakka-sync-complete') {
      refreshVisibleView();
      updateNetworkStatus();
      refreshNotifications();
    }
    // 'trakka-sync-status' is the finer-grained sibling of the message
    // above — sent on every queue mutation, not just once a flush attempt
    // fully finishes, so the header badge and the per-list/per-item dots
    // can react immediately instead of only once refreshVisibleView's own
    // network round-trip resolves. See handleSyncStatusMessage's own
    // comment for the full state machine.
    if (event.data && event.data.type === 'trakka-sync-status') {
      handleSyncStatusMessage(event.data);
    }
    // Posted by sw.js's notificationclick handler when it focused an
    // already-open tab instead of opening a fresh one — routes to the
    // notification's target list client-side rather than relying on the
    // unevenly supported WindowClient.navigate(). See
    // handleNotificationClickMessage above.
    if (event.data && event.data.type === 'trakka-notification-click') {
      handleNotificationClickMessage(event.data.url);
    }
  });
}

// Detects a deployed frontend update and surfaces it as a discreet,
// dismissible-by-inaction banner rather than reloading on the user's
// behalf: sw.js's install handler already calls self.skipWaiting() and its
// activate handler calls self.clients.claim(), so the new service worker
// (and its bumped cache versions) takes over almost immediately regardless
// — but the *page's own already-loaded JS* stays the old version until an
// actual reload happens, and forcing that reload automatically could wipe
// out whatever the user is mid-typing in a form. Reaching 'installed'
// while navigator.serviceWorker.controller is already set is the standard
// signal that this is a genuine update (a new worker replacing one already
// controlling the page), not the very first install on a fresh visit,
// which has no controller yet and needs no banner.
function watchForServiceWorkerUpdate(registration) {
  if (!registration) return;

  registration.addEventListener('updatefound', () => {
    const installingWorker = registration.installing;
    if (!installingWorker) return;

    installingWorker.addEventListener('statechange', () => {
      if (installingWorker.state === 'installed' && navigator.serviceWorker.controller) {
        showUpdateBanner();
      }
    });
  });
}

function showUpdateBanner() {
  els.updateBanner.hidden = false;
}

els.updateReloadButton?.addEventListener('click', () => {
  window.location.reload();
});

// Forces an immediate retry of anything still sitting in the offline sync
// queue (a create/edit made while offline that never reached the server) at
// app boot, rather than waiting on an incidental fetch (sw.js's own
// per-request opportunistic flushQueue() call in its 'fetch' listener) or the
// 'online' event below to happen to trigger one. Without this, a mutation
// still queued at the end of a previous session could sit unpushed
// indefinitely after the app is reopened already online — nothing else
// necessarily calls fetch() again before the user notices something's wrong,
// e.g. an install that only ever ships to a controlled home screen and gets
// reopened straight from there. navigator.serviceWorker.ready only resolves
// once some worker is actually controlling the page, so this is a safe no-op
// on first-ever load (no worker yet) and on a browser with no service worker
// support at all.
async function syncOnStartup() {
  if (!('serviceWorker' in navigator)) return;
  try {
    const registration = await navigator.serviceWorker.ready;
    if (navigator.onLine) {
      registration.active?.postMessage({ type: 'flush-queue' });
    }
  } catch {
    // Registration never resolved (unsupported/insecure context) — nothing
    // this function could reconcile.
  }
}

window.addEventListener('online', () => {
  updateNetworkStatus();
  navigator.serviceWorker.controller?.postMessage({ type: 'flush-queue' });
});
window.addEventListener('offline', updateNetworkStatus);
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) {
    updateNetworkStatus();
    // Picks up whatever the periodic backend price-drop scan found while
    // this tab was in the background, without requiring a full reload.
    refreshNotifications();
  }
});

// Paints the dashboard from whatever's already in IndexedDB before any
// network request is made — an empty mirror (brand new browser profile)
// just renders the normal empty state, so this is always safe to call.
// This is the "instant paint" half of the stale-while-revalidate load: the
// network refresh in init() below repaints over this once it resolves.
async function hydrateFromCache() {
  const houses = await cachedHouses();
  state.houses = houses;
  populateHouseSelect(houses);

  const stored = Number(localStorage.getItem(HOUSE_STORAGE_KEY));
  const storedIsValid = houses.some((house) => house.id === stored);
  state.currentHouseId = storedIsValid ? stored : (houses[0]?.id ?? null);
  els.houseSelect.value = state.currentHouseId !== null ? String(state.currentHouseId) : CREATE_HOUSE_OPTION_VALUE;
  updateManageMembersButton();
  updateRenameHouseButton();

  // Warmed before the first render below (rather than left to whenever the
  // next queue mutation happens to broadcast) so a leftover, never-flushed
  // offline queue from a previous session already shows its "unsynced
  // changes" dots on the very first paint, not only once something changes.
  await refreshPendingChangeIndicators();
  await renderDashboardFromCache();

  // Also warm the custom-categories ("Espaces") mirror here, not just lists/
  // items, so the "Espaces" tab's highlight dot and the "new list" modal's
  // category picker are already correct the instant a reload finishes,
  // rather than waiting on loadCustomCategories()'s own network-first call
  // further down in init() to fail over to the cache. customCategories/
  // updateSpacesTabBadge/cachedCustomCategories are defined in spaces.js,
  // loaded after this file — safe to reference here despite that <script>
  // load order because this only runs after hydrateFromCache has already
  // crossed a real IndexedDB await (cachedHouses() above), by which point
  // every script tag on the page has long finished executing and defined
  // its top-level functions — the same timing guarantee init()'s own later
  // call to loadCustomCategories() already relies on. cachedCustomCategories
  // itself is what applies the per-account defense-in-depth filter (see its
  // own doc comment in spaces.js) — a no-op at this exact point since
  // state.currentUser isn't resolved yet this early in init(), but keeping
  // this call routed through the same helper avoids two copies of that
  // filtering logic drifting apart.
  if (window.TrakkaDB) {
    customCategories = await cachedCustomCategories();
    updateSpacesTabBadge();
  }
}

async function init() {
  // Offline-first hydration: paint immediately from the local mirror so a
  // reload while offline (or on a slow connection) never shows a blank
  // dashboard while the requests below are still in flight.
  await hydrateFromCache();

  // Fire-and-forget: reconcile the offline sync queue with the server right
  // away rather than waiting for it to happen incidentally. Not awaited so a
  // slow/never-resolving service-worker-readiness check can never delay the
  // rest of startup.
  syncOnStartup();

  try {
    // A real 401 here redirects to /auth/login via apiRequest's 401
    // handling and never resolves, so nothing below runs for an
    // unauthenticated visitor. This only catches "couldn't reach the
    // server at all" (offline, or the service worker's offline fallback,
    // which doesn't special-case /me and answers 503) — expected while
    // offline, so it just keeps whatever hydrateFromCache already painted
    // instead of aborting startup.
    state.currentUser = await apiRequest('/me');
    // If the mirror painted above belongs to a different account (a shared
    // browser, or a session that expired and was replaced by someone else's),
    // drop it and repaint from scratch rather than leaving another user's
    // data on screen. See CACHED_USER_STORAGE_KEY.
    let cachedUserId = null;
    try {
      cachedUserId = localStorage.getItem(CACHED_USER_STORAGE_KEY);
    } catch {
      cachedUserId = null;
    }
    if (cachedUserId !== null && cachedUserId !== String(state.currentUser.id)) {
      await purgeLocalUserData();
      state.houses = [];
      state.lists = [];
      state.currentHouseId = null;
      populateHouseSelect(state.houses);
    }
    try {
      localStorage.setItem(CACHED_USER_STORAGE_KEY, String(state.currentUser.id));
    } catch {
      /* private mode / storage denied — the purge above still ran */
    }
    setKeepLastPagePreference(state.currentUser.keep_last_page);
    // Reconcile the account's own language preference (server-authoritative
    // — always "fr"/"en", never empty, see internal/handlers.resolveUserLanguage)
    // against whatever i18n.js already applied from localStorage/browser-
    // language guessing before this resolved, e.g. a brand new account whose
    // instance-wide default (DEFAULT_APP_LANGUAGE) differs from the
    // browser's own language. Own try/catch so a failed locale fetch can't
    // be mistaken for the session check above having failed.
    if (window.TrakkaI18n && state.currentUser.language !== TrakkaI18n.getLang()) {
      try {
        await TrakkaI18n.setLang(state.currentUser.language);
      } catch {
        // Locale fetch failed — keep whatever language was already applied.
      }
    }
  } catch (err) {
    console.warn('Session non vérifiée (probablement hors ligne) :', err);
  }
  // refreshAdminConsoleButtonVisibility is defined in admin.js, resolved
  // lazily the same way every other cross-file call in this function
  // already is.
  refreshAdminConsoleButtonVisibility();

  // Stale-while-revalidate: now refresh from the network. Both calls fall
  // back to the cache again on their own if this fails, so it's safe to
  // run unconditionally regardless of whether the /me check above worked.
  await ensureHousesLoaded();
  await loadDashboard();
  // loadNotifications is defined in notifications.js, resolved lazily the
  // same way every other cross-file call in this function already is.
  await loadNotifications();
  // loadCustomCategories is defined in spaces.js — fetches the user's custom
  // categories once at startup (and updates the "Espaces" tab's highlight
  // dot as a side effect) so the tab reflects reality immediately, without
  // waiting for it to be opened.
  await loadCustomCategories();
  // Reopens a push notification's ?list= deep link if this load came from
  // one, otherwise the last-visited tab/list per the "keep last page"
  // preference (see handleDeepLinkOrRestore/restoreLastView above) — must
  // run last, once everything either might need (the dashboard, the
  // current house, custom categories for the "Espaces" tab) is ready.
  await handleDeepLinkOrRestore();
}

// Re-render everything that embeds translated strings inside JS-generated
// markup (data-i18n only covers static HTML, which i18n.js already
// re-applies on its own after a language switch).
document.addEventListener('trakka:lang-changed', () => {
  populateHouseSelect(state.houses);
  els.houseSelect.value = state.currentHouseId !== null ? String(state.currentHouseId) : CREATE_HOUSE_OPTION_VALUE;
  refreshVisibleView();
  updateNetworkStatus();
  refreshNotifications();
});

// Logout is a plain form POST (a full-page navigation, not an apiRequest),
// so the local purge has to happen before the form is allowed to submit —
// hence preventDefault, await, then submit() programmatically. submit()
// bypasses this listener, so there is no recursion.
const logoutForm = document.querySelector('form[action="/auth/logout"]');
if (logoutForm) {
  logoutForm.addEventListener('submit', (event) => {
    event.preventDefault();
    purgeLocalUserData().finally(() => logoutForm.submit());
  });
}

init();
updateNetworkStatus();
registerServiceWorker();
