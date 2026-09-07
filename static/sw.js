'use strict';

// Trakka service worker: offline-first app shell + an IndexedDB-backed
// sync queue for API mutations made while offline. Uses static/js/db.js
// for all persistence (see that file for why it's importScripts()-safe).
importScripts('/js/db.js');

// Bump both on any change to APP_SHELL's contents so activate()
// evicts the old cache instead of serving stale assets forever.
const SHELL_CACHE = 'trakka-shell-v84';
const RUNTIME_CACHE = 'trakka-runtime-v84';
const KNOWN_CACHES = [SHELL_CACHE, RUNTIME_CACHE];

const APP_SHELL = [
  '/',
  '/index.html',
  '/js/theme-init.js',
  '/js/theme.js',
  '/js/tailwind.js',
  '/js/tailwind-config.js',
  '/js/i18n.js',
  '/js/app.js',
  '/js/db.js',
  '/js/undo.js',
  '/js/list_view.js',
  '/js/gestures.js',
  '/js/reorder.js',
  '/js/planning.js',
  '/js/urgent.js',
  '/js/spaces.js',
  '/js/shares.js',
  '/js/notifications.js',
  '/js/admin.js',
  '/js/settings.js',
  '/js/push.js',
  '/js/install-help.js',
  '/css/base.css',
  '/css/tokens.css',
  '/locales/fr.json',
  '/locales/en.json',
  '/manifest.json',
  '/icons/favicon.ico',
  '/icons/trakka-favicon.svg',
  '/icons/apple-touch-icon-180.png',
  '/icons/trakka-icon-192.png',
  '/icons/trakka-icon-512.png',
  '/icons/trakka-maskable-192.png',
  '/icons/trakka-maskable-512.png',
  '/icons/trakka-lockup-dark-bg.svg',
  '/icons/trakka-lockup-light-bg.svg',
];

// index.html's styling depends entirely on this cross-origin script (the
// Tailwind Play CDN) actually loading — without it cached, the app shell
// would come back from cache while offline but render completely
// unstyled. Cached separately from APP_SHELL (best-effort, not part of
// the atomic install) so a CDN hiccup at install time can't break the
// installation of Trakka's own app shell.
const API_PREFIX = '/api/v1';
const SYNC_TAG = 'trakka-sync-queue';

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// skipWaiting() here is what makes app.js's update banner (see
// watchForServiceWorkerUpdate in static/js/app.js) meaningful: without it, a
// newly-installed worker would sit in the 'waiting' state until every tab
// running the old one closes, so the banner's "Mettre à jour" button
// wouldn't actually have anything new to switch to yet on click. With it,
// this worker proceeds straight to activating (below) as soon as it's
// installed, so a reload after the banner appears really does pick up the
// new assets.
self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(SHELL_CACHE)
      .then((cache) => cache.addAll(APP_SHELL).then(() => cache))
      .then(() => self.skipWaiting())
  );
});

// Only ever deletes entries from the Cache Storage API (KNOWN_CACHES,
// declared above) — never touches IndexedDB, which is a completely
// separate storage system the browser doesn't tie to cache versioning at
// all. This is what lets a deployed update evict every stale cached asset
// on activation without losing the offline mirror/sync queue db.js
// maintains; see db.js's own onupgradeneeded/onversionchange handling for
// how *that* store evolves safely across versions instead.
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(
        keys.filter((key) => !KNOWN_CACHES.includes(key)).map((key) => caches.delete(key))
      ))
      .then(() => self.clients.claim())
      .then(() => flushQueue())
  );
});

// Real Background Sync (Chrome/Android): fires even if no tab is open.
self.addEventListener('sync', (event) => {
  if (event.tag === SYNC_TAG) {
    event.waitUntil(flushQueue());
  }
});

// Fallback for browsers without Background Sync (notably iOS/iPadOS
// Safari, which never fires 'sync' at all): the page posts this when its
// own 'online' event fires.
self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'flush-queue') {
    event.waitUntil(flushQueue());
  }
});

// ---------------------------------------------------------------------------
// Web Push: a message from internal/handlers.sendToUsers
// (internal/handlers/push.go), encrypted per RFC 8291 — decryption itself is
// handled entirely by the browser/OS push stack before this event ever
// fires; by the time 'push' runs, event.data is already the plaintext JSON
// pushPayload {title, body, url} the Go backend sent. Every push this app
// sends is a real, user-visible notification (never a data-only "silent
// push" with no UI, which browsers restrict/penalize) — `silent: true` on
// the Notification itself is what satisfies the "sans son, discrète" intent
// instead: no sound/vibration, but still shown.
self.addEventListener('push', (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch {
    data = {};
  }
  const title = data.title || 'Trakka';
  const url = data.url || '/';
  event.waitUntil(
    self.registration.showNotification(title, {
      body: data.body || '',
      icon: '/icons/trakka-icon-192.png',
      badge: '/icons/trakka-maskable-192.png',
      silent: true,
      tag: 'trakka-' + url,
      data: { url },
    })
  );
});

// Clicking the notification focuses an already-open tab (and tells it,
// via postMessage, which list to switch to client-side — see app.js's
// handleNotificationClickMessage — rather than relying on the unevenly
// supported WindowClient.navigate(), which an SPA has no real need for
// anyway) or, with no tab open, opens a fresh one straight at the deep
// link; static/js/app.js's own boot-time handleDeepLinkOrRestore reads that
// URL's ?list= query param the same way either path ends up at the right
// list.
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const url = (event.notification.data && event.notification.data.url) || '/';

  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clientList) => {
      for (const client of clientList) {
        if ('focus' in client) {
          client.postMessage({ type: 'trakka-notification-click', url });
          return client.focus();
        }
      }
      if (self.clients.openWindow) {
        return self.clients.openWindow(url);
      }
      return undefined;
    })
  );
});

// ---------------------------------------------------------------------------
// Fetch routing
// ---------------------------------------------------------------------------

self.addEventListener('fetch', (event) => {
  const { request } = event;
  const url = new URL(request.url);

  if (url.origin !== self.location.origin || url.pathname === '/healthz') {
    return; // other cross-origin requests and healthz pass straight through
  }

  // Login/register/logout/OIDC navigations: never cache-first-served like
  // shell requests, never offline-queued like API writes — a queued login
  // can never meaningfully "come back true" later the way a queued list or
  // item write can, and the server-rendered error/mode state on this page
  // can't be meaningfully cached either.
  if (url.pathname.startsWith('/auth/')) {
    return;
  }

  // Cheap, fire-and-forget opportunistic retry: any same-origin request
  // while online is a good moment to notice connectivity is back, without
  // waiting on Background Sync or the page's 'online' event.
  if (self.navigator.onLine) {
    flushQueue().catch(() => {});
  }

  if (url.pathname.startsWith(API_PREFIX + '/')) {
    event.respondWith(handleApiRequest(request, url));
    return;
  }

  event.respondWith(handleShellRequest(request));
});

// ---------------------------------------------------------------------------
// App shell: cache-first, refreshed in the background, offline fallback to
// the cached shell so a deep link still opens the SPA while offline.
// ---------------------------------------------------------------------------

async function handleShellRequest(request) {
  if (request.method !== 'GET') {
    return fetch(request);
  }

  const cached = await caches.match(request);
  const networkFetch = fetch(request)
    .then((response) => {
      if (response && response.ok) {
        caches.open(SHELL_CACHE).then((cache) => cache.put(request, response.clone()));
      }
      return response;
    })
    .catch(() => undefined);

  if (cached) {
    networkFetch.catch(() => {});
    return cached;
  }

  const fresh = await networkFetch;
  if (fresh) {
    return fresh;
  }

  const fallback = await caches.match('/index.html');
  return fallback || new Response('Hors ligne', { status: 503, statusText: 'Offline' });
}

// ---------------------------------------------------------------------------
// API: network-first for reads (mirrored into IndexedDB), queued-write for
// mutations made while offline. This stays network-first rather than
// stale-while-revalidate at the SW layer on purpose — a mutation's response
// (a fresh price/quantity/RBAC check, etc.) must never be silently served
// from a stale cache. The "instant paint, then refresh" effect the app's
// own performance story wants is instead implemented one layer up, at each
// view's own call site (loadDashboard in app.js; loadUrgentView/
// loadPlanningView/loadSpacesView; selectList in list_view.js), which
// already reads the exact same IndexedDB mirror this file writes on every
// successful read (mirrorReadResponse below) before ever calling this
// endpoint — see CLAUDE.md's "Loading skeletons & stale-while-revalidate
// view loaders" entry for the full design.
//
// Nothing here ever calls caches.match()/caches.put() for a /api/v1/...
// request — the Cache Storage API (SHELL_CACHE/RUNTIME_CACHE above) backs
// the app shell only (see handleShellRequest). The fetch(request) call
// below is a genuine network round trip every time; internal/handlers/
// json.go additionally sets Cache-Control: no-store on every JSON response,
// so even the browser's own HTTP disk cache (a layer this file has no
// control over) can never hand fetch() a stale GET here instead of either a
// real response or (offline/gateway-down) the IndexedDB fallback below.
// ---------------------------------------------------------------------------

async function handleApiRequest(request, url) {
  if (request.method === 'GET') {
    return handleApiRead(request, url);
  }
  return handleApiWrite(request, url);
}

async function handleApiRead(request, url) {
  try {
    const response = await fetch(request);
    if (response.ok) {
      if (url.pathname === '/api/v1/me') {
        // Reconcile mirror ownership BEFORE this response is handed back to
        // the page — must complete first (unlike the other mirror writes
        // below, which are deliberately fire-and-forget) so nothing
        // downstream that runs the instant apiRequest('/me') resolves (e.g.
        // app.js's hydrateFromCache, which reads IndexedDB directly) can ever
        // observe a mirror that still belongs to a different, previously-
        // authenticated account. See enforceMirrorOwnership.
        await reconcileMirrorOwnershipFromMeResponse(response.clone()).catch(() => {});
      } else {
        mirrorReadResponse(url, response.clone()).catch(() => {});
      }
      return response;
    }
    // A resolved 502/503/504 means the backend or the reverse proxy in front
    // of it is unreachable/overloaded right now — from the page's point of
    // view that's indistinguishable from "no connectivity", so it's treated
    // exactly like the fetch()-itself-failed case below rather than being
    // handed back as a raw error response: apiRequest() (app.js) has no way
    // to tell this apart from a genuine application error (a real 502 the
    // Go backend itself never returns), so without this it would surface a
    // blocking "Erreur 502" banner during what is, from the user's
    // perspective, just an offline/flaky-connection moment.
    if (isGatewayErrorStatus(response.status)) {
      return offlineReadFallback(url);
    }
    return response;
  } catch {
    return offlineReadFallback(url);
  }
}

// 502 Bad Gateway / 503 Service Unavailable / 504 Gateway Timeout: the
// backend process or the reverse proxy in front of it is down, restarting,
// or can't be reached — never a status this app's own handlers return for a
// real application-level error (see internal/handlers/json.go's writeError,
// which only ever emits 400/401/403/404/409/500). Treating these the same
// as a network failure is what lets a flaky/restarting backend degrade to
// "offline" instead of flashing a raw error banner.
function isGatewayErrorStatus(status) {
  return status === 502 || status === 503 || status === 504;
}

// Isolation boundary for the whole IndexedDB mirror (see the "meta" store in
// db.js). GET /api/v1/me is the one response in this app that carries the
// requesting session's own identity, so it's the single point where the
// service worker — the mirror's sole writer — can tell whether the account
// making requests right now is still the same one the mirror was built for.
// On a shared/kiosk browser where a previous user's session simply expired
// or was never explicitly logged out of, the next account to sign in would
// otherwise inherit their houses/lists/items/categories (and any of their
// still-queued offline writes) until app.js's own, page-side mismatch check
// happened to run — which never happens at all while genuinely offline. This
// closes that gap at the source: nothing is mirrored for the new account
// until any stale mirror has already been wiped.
async function enforceMirrorOwnership(userId) {
  if (userId === undefined || userId === null) return;
  const activeUserId = await self.TrakkaDB.getActiveUserId();
  if (activeUserId !== null && activeUserId !== userId) {
    await self.TrakkaDB.clearAll();
  }
  await self.TrakkaDB.setActiveUserId(userId);
}

async function reconcileMirrorOwnershipFromMeResponse(response) {
  let data;
  try {
    data = await response.json();
  } catch {
    return;
  }
  if (data && data.id !== undefined) {
    await enforceMirrorOwnership(data.id);
  }
}

// flushQueue's own ownership check (see its call site) — makes a direct
// request rather than reusing whatever GET /api/v1/me response the page may
// or may not have in flight, since flushQueue can itself be triggered with
// no page open at all (a Background Sync event). Deliberately swallows a
// network failure: if this can't reach the server, there is by definition
// nothing to replay either (the fetch loop right after this would fail the
// exact same way), so leaving the existing ownership record as-is and simply
// falling through to "nothing to do" is correct.
async function verifyMirrorOwnership() {
  try {
    const response = await fetch('/api/v1/me', { credentials: 'same-origin' });
    if (!response.ok) return;
    const data = await response.json().catch(() => null);
    if (data && data.id !== undefined) {
      await enforceMirrorOwnership(data.id);
    }
  } catch {
    // Offline — nothing to verify against, and nothing this flushQueue pass
    // could have replayed anyway.
  }
}

async function mirrorReadResponse(url, response) {
  let data;
  try {
    data = await response.json();
  } catch {
    return;
  }

  const listMatch = url.pathname.match(/^\/api\/v1\/lists\/(\d+)$/);
  // ?shared_with_me=true returns lists from Houses the caller isn't
  // necessarily a member of (see db.ListSharedListsForUser), tagged with
  // access_source/access_permission/is_pinned_to_dashboard — mirroring those
  // stub rows into the same IndexedDB store as the caller's own House-scoped
  // lists would pollute it with rows that don't belong to any of their own
  // Houses, so they get their own dedicated stub mirror instead (see
  // STORE_SHARED_LISTS in db.js) rather than being unioned in. A shared
  // list's own full detail (with items) still lands in the ordinary
  // lists/items mirror the moment it's actually opened, via the plain
  // GET /lists/{id} every one of these stub-driven views fans out to.
  const isSharedWithMe = url.pathname === '/api/v1/lists' && url.searchParams.get('shared_with_me') === 'true';
  // Same reasoning as isSharedWithMe, applied to CLAUDE.md's "pinned house
  // spaces" feature: ?pinned_house_spaces=true also returns lists from
  // Houses the caller belongs to but that aren't necessarily the currently
  // selected one — mirrored into its own STORE_PINNED_HOUSE_SPACE_LISTS stub
  // store rather than the plain lists store, for the same reason.
  const isPinnedHouseSpaces = url.pathname === '/api/v1/lists' && url.searchParams.get('pinned_house_spaces') === 'true';
  // Same reasoning as isSharedWithMe above, applied to Spaces: a category
  // shared with the caller (rather than owned by them) belongs to whoever
  // actually owns it, not to any of the caller's own custom_category_id
  // associations — mirrored into its own STORE_SHARED_CATEGORIES stub store
  // rather than the plain custom_categories mirror, so the caller's own
  // categories (the ones cachedCustomCategories's user_id filter expects to
  // find there) are never polluted with one they don't own.
  const isSharedCategoriesQuery =
    url.pathname === '/api/v1/custom-categories' && url.searchParams.get('shared_with_me') === 'true';

  if (url.pathname === '/api/v1/houses' && Array.isArray(data)) {
    await self.TrakkaDB.putHouses(data);
  } else if (isSharedWithMe && Array.isArray(data)) {
    await self.TrakkaDB.replaceSharedLists(data);
  } else if (isPinnedHouseSpaces && Array.isArray(data)) {
    await self.TrakkaDB.replacePinnedHouseSpaceLists(data);
  } else if (url.pathname === '/api/v1/lists' && Array.isArray(data)) {
    // freshLists (db.js) drops any list this GET would otherwise regress —
    // see its own doc comment for why this guard lives here, at the GET-
    // response mirroring boundary, rather than inside putList/putLists
    // themselves: two concurrent fetch() calls for overlapping list data
    // (e.g. the dashboard's own listing alongside a list detail view's
    // fetch, or simple network reordering) can resolve out of order, and
    // without this an older response landing last could silently revert a
    // field (most importantly `done`, via a stale list's cached
    // total_amount) that a more recent write already updated.
    const lists = await self.TrakkaDB.freshLists(data.map(withoutItems));
    await self.TrakkaDB.putLists(lists);
  } else if (listMatch && data && typeof data === 'object') {
    const { items, ...list } = data;
    const freshList = await self.TrakkaDB.freshLists([list]);
    if (freshList.length) await self.TrakkaDB.putList(list);
    if (Array.isArray(items)) {
      // Same reasoning as freshLists above, applied to this list's own
      // items — this is the guard that actually matters for the reported
      // "a done item shows back up as active while offline" bug: a slower
      // GET /api/v1/lists/{id} issued before a done-toggle's PATCH, but
      // resolving after that PATCH's own response already wrote
      // `done: true` into the mirror, would otherwise silently revert it.
      const freshItemsArr = await self.TrakkaDB.freshItems(items);
      await self.TrakkaDB.putItems(freshItemsArr);
    }
  } else if (url.pathname === '/api/v1/items' && Array.isArray(data)) {
    const items = await self.TrakkaDB.freshItems(data);
    await self.TrakkaDB.putItems(items);
  } else if (isSharedCategoriesQuery && Array.isArray(data)) {
    await self.TrakkaDB.replaceSharedCategories(data);
  } else if (url.pathname === '/api/v1/custom-categories' && Array.isArray(data)) {
    await self.TrakkaDB.putCustomCategories(data);
  }
}

async function offlineReadFallback(url) {
  const headers = { 'Content-Type': 'application/json; charset=utf-8', 'X-Trakka-Offline': 'true' };

  if (url.pathname === '/api/v1/houses') {
    const houses = await self.TrakkaDB.getHouses();
    return new Response(JSON.stringify(houses), { status: 200, headers });
  }

  if (url.pathname === '/api/v1/lists' && url.searchParams.get('shared_with_me') === 'true') {
    // Served from its own stub mirror (see mirrorReadResponse/
    // STORE_SHARED_LISTS above) rather than falling through to the general
    // lists mirror below, which would incorrectly surface the caller's own
    // House-scoped lists in the "Partagé avec moi" tab. Each stub's own
    // full detail (with items) is served separately, by the listMatch
    // branch further down, from whatever the plain lists/items mirror last
    // saw the last time that list was actually opened online.
    const stubs = await self.TrakkaDB.getSharedLists();
    return new Response(JSON.stringify(stubs), { status: 200, headers });
  }

  if (url.pathname === '/api/v1/lists' && url.searchParams.get('pinned_house_spaces') === 'true') {
    // Same reasoning as the shared_with_me case just above, applied to
    // CLAUDE.md's "pinned house spaces" feature — served from its own stub
    // mirror (STORE_PINNED_HOUSE_SPACE_LISTS) rather than falling through to
    // the general lists mirror below (which has no notion of "only the ones
    // reached via a pinned House Space" and would return every one of the
    // caller's own House-scoped lists instead).
    const stubs = await self.TrakkaDB.getPinnedHouseSpaceLists();
    return new Response(JSON.stringify(stubs), { status: 200, headers });
  }

  if (url.pathname === '/api/v1/lists') {
    const houseId = decodeId(url.searchParams.get('house_id'));
    let lists = await self.TrakkaDB.getLists();
    if (houseId != null) lists = lists.filter((list) => list.house_id === houseId);
    return new Response(JSON.stringify(lists), { status: 200, headers });
  }

  const listMatch = url.pathname.match(/^\/api\/v1\/lists\/([^/]+)$/);
  if (listMatch) {
    const list = await self.TrakkaDB.getList(decodeId(listMatch[1]));
    if (!list) return jsonError(headers, 404, 'liste introuvable (hors ligne)');
    const items = await self.TrakkaDB.getItemsByList(list.id);
    return new Response(JSON.stringify({ ...list, items }), { status: 200, headers });
  }

  if (url.pathname === '/api/v1/items') {
    const listId = decodeId(url.searchParams.get('list_id'));
    const items = listId != null ? await self.TrakkaDB.getItemsByList(listId) : [];
    return new Response(JSON.stringify(items), { status: 200, headers });
  }

  if (url.pathname === '/api/v1/custom-categories' && url.searchParams.get('shared_with_me') === 'true') {
    // Served from its own stub mirror (STORE_SHARED_CATEGORIES) rather than
    // falling through to the general categories mirror below, which would
    // incorrectly surface the caller's own categories as "shared with me".
    const stubs = await self.TrakkaDB.getSharedCategories();
    return new Response(JSON.stringify(stubs), { status: 200, headers });
  }

  if (url.pathname === '/api/v1/custom-categories') {
    const categories = await self.TrakkaDB.getCustomCategories();
    return new Response(JSON.stringify(categories), { status: 200, headers });
  }

  return jsonError(headers, 503, 'hors ligne');
}

async function handleApiWrite(request, url) {
  const bodyText = await request.clone().text();
  let body = null;
  if (bodyText) {
    try { body = JSON.parse(bodyText); } catch { body = null; }
  }

  try {
    const response = await fetch(request);
    if (response.ok) {
      mirrorWriteResult(request.method, url, response.clone()).catch(() => {});
      return response;
    }
    // Same reasoning as handleApiRead's identical check above: a resolved
    // 502/503/504 means the backend/reverse proxy is unreachable right now,
    // not that the write was genuinely rejected — queue it for replay
    // exactly as if fetch() itself had thrown, instead of surfacing its
    // error body as a real failure.
    if (isGatewayErrorStatus(response.status)) {
      return queueOfflineWrite(request.method, url, body);
    }
    return response;
  } catch {
    return queueOfflineWrite(request.method, url, body);
  }
}

async function mirrorWriteResult(method, url, response) {
  let data = null;
  try { data = await response.json(); } catch { /* e.g. 204 No Content */ }

  const houseMatch = url.pathname.match(/^\/api\/v1\/houses\/(\d+)$/);
  const listMatch = url.pathname.match(/^\/api\/v1\/lists\/(\d+)$/);
  const reorderMatch = url.pathname.match(/^\/api\/v1\/lists\/(\d+)\/reorder$/);
  const itemMatch = url.pathname.match(/^\/api\/v1\/items\/(\d+)$/);
  const categoryMatch = url.pathname.match(/^\/api\/v1\/custom-categories\/(\d+)$/);

  if (reorderMatch && method === 'PUT' && Array.isArray(data)) {
    // The response is every item in the list, freshly re-ordered — mirror
    // all of them in one go rather than one at a time, the same bulk helper
    // GET /api/v1/lists/{id}'s own item-array mirroring already uses.
    await self.TrakkaDB.putItems(data);
  } else if (url.pathname === '/api/v1/houses' && method === 'POST' && data) {
    await self.TrakkaDB.putHouse(data);
  } else if (houseMatch && method === 'PUT' && data) {
    await self.TrakkaDB.putHouse(data);
  } else if (houseMatch && method === 'DELETE') {
    await self.TrakkaDB.deleteHouse(Number(houseMatch[1]));
  } else if (url.pathname === '/api/v1/lists' && method === 'POST' && data) {
    await self.TrakkaDB.putList(withoutItems(data));
  } else if (listMatch && method === 'PUT' && data) {
    await self.TrakkaDB.putList(withoutItems(data));
  } else if (listMatch && method === 'DELETE') {
    await self.TrakkaDB.deleteList(Number(listMatch[1]));
  } else if (url.pathname === '/api/v1/items' && method === 'POST' && data) {
    await self.TrakkaDB.putItem(data);
    await recomputeListTotalAmount(data.list_id);
  } else if (itemMatch && (method === 'PUT' || method === 'PATCH') && data) {
    await self.TrakkaDB.putItem(data);
    await recomputeListTotalAmount(data.list_id);
  } else if (itemMatch && method === 'DELETE') {
    const deletedId = Number(itemMatch[1]);
    // Read the item BEFORE deleting it — its list_id is only ever known
    // through this row (the DELETE response itself carries no body), and
    // recomputeListTotalAmount needs it to know which list's cached total to
    // refresh.
    const existing = await self.TrakkaDB.getItem(deletedId);
    await self.TrakkaDB.deleteItem(deletedId);
    if (existing) await recomputeListTotalAmount(existing.list_id);
  } else if (url.pathname === '/api/v1/custom-categories' && method === 'POST' && data) {
    await self.TrakkaDB.putCustomCategory(data);
  } else if (categoryMatch && method === 'PUT' && data) {
    await self.TrakkaDB.putCustomCategory(data);
  } else if (categoryMatch && method === 'DELETE') {
    await self.TrakkaDB.deleteCustomCategory(Number(categoryMatch[1]));
  }
}

// ---------------------------------------------------------------------------
// Offline write queue
// ---------------------------------------------------------------------------

async function queueOfflineWrite(method, url, body) {
  const headers = { 'Content-Type': 'application/json; charset=utf-8', 'X-Trakka-Queued': 'true' };
  const pathname = url.pathname;
  const now = new Date().toISOString();

  // Houses are simple, rarely-created resources with no offline queueing
  // support: unlike lists/items (which chain one level deep — an item can
  // depend on a list created in the same offline session — houses would
  // need a *second* level, since a list can depend on a house), which
  // docs/PWA.md explicitly calls out as unsupported. Mutating a house
  // requires connectivity; queuing anything else stays exactly as before.
  if (pathname === '/api/v1/houses' || /^\/api\/v1\/houses\/[^/]+$/.test(pathname)) {
    return jsonError(
      { 'Content-Type': 'application/json; charset=utf-8' },
      503,
      'Cette action nécessite une connexion réseau.'
    );
  }

  // Admin settings (OIDC config, registration policy, instance name), and
  // the admin console's user/Space management actions (granting/revoking
  // is_admin, deleting a user or a Space) are rare, system-wide/highly
  // destructive mutations with real side effects — the same reasoning that
  // keeps houses off the offline queue applies here, even more so given
  // what's at stake if a stale queued change (e.g. a demote-the-last-admin
  // rejection, or a delete against a since-changed admin count) silently
  // reapplied later instead of failing in front of the admin right away.
  if (
    pathname === '/api/v1/admin/settings' ||
    /^\/api\/v1\/admin\/users\/[^/]+$/.test(pathname) ||
    /^\/api\/v1\/admin\/spaces\/[^/]+$/.test(pathname)
  ) {
    return jsonError(
      { 'Content-Type': 'application/json; charset=utf-8' },
      503,
      'Cette action nécessite une connexion réseau.'
    );
  }

  // Granting/revoking a List or Space share (or withdrawing an invitation
  // that has not been accepted yet) needs an immediate round-trip and
  // reports success or failure back to the modal right away — queuing it
  // silently, like houses/admin settings above, would surface a confusing
  // failure only once the queue eventually flushes, long after the modal
  // already looked like it worked.
  if (
    /^\/api\/v1\/(custom-categories|lists)\/[^/]+\/share(\/[^/]+)?$/.test(pathname) ||
    /^\/api\/v1\/(custom-categories|lists|houses)\/[^/]+\/invitations$/.test(pathname)
  ) {
    return jsonError(
      { 'Content-Type': 'application/json; charset=utf-8' },
      503,
      'Cette action nécessite une connexion réseau.'
    );
  }

  // A manual drag-and-drop reorder (PUT /lists/{id}/reorder) sends the
  // complete new ordering as item_ids. Unlike houses/admin settings/shares
  // above, this is a plain per-list item mutation exactly like a PATCH on a
  // single item's `position` would be — there is no reason it needs to be
  // exempted from the offline queue, and previously blocking it with a hard
  // 503 here is what surfaced as "Cette action nécessite une connexion
  // réseau." mid-drag (see the Offline-First requirement in
  // CLAUDE.md/docs/PWA.md). queueOfflineReorder below applies the new
  // ordering straight to the local IndexedDB mirror (so it survives a reload
  // even before the request ever reaches the server) and queues the request
  // for replay once back online, synthesizing the same shape a real 200
  // response carries — every item in the list, in its new order — so
  // reorder.js's commitReorder can assign it straight into
  // state.currentList.items exactly as it would for a live response.
  // reorder.js's own reorderAvailable() keeps the "⇅ Réordonner" button
  // hidden whenever any item still has an offline-queued temp-item-* id, so
  // item_ids here is always a set of real, numeric ids already present in
  // the mirror — no temp-id resolution is needed the way pending item/list
  // creates require below.
  const reorderMatch = pathname.match(/^\/api\/v1\/lists\/([^/]+)\/reorder$/);
  if (reorderMatch) {
    return queueOfflineReorder(pathname, decodeId(reorderMatch[1]), body, headers, now);
  }

  // Editing/deleting something that was itself created offline and never
  // reached the server: resolve it against the pending "create" entry
  // directly instead of queuing a request against an id the API has never
  // heard of.
  const pendingId = extractTempId(pathname);
  if (pendingId) {
    return resolveAgainstPendingCreate(pendingId, method, body, headers);
  }

  if (pathname === '/api/v1/lists' && method === 'POST') {
    const tempId = generateTempId('temp-list');
    const list = { id: tempId, house_id: body?.house_id, name: body?.name ?? '', type: body?.type || 'shopping', created_at: now, updated_at: now };
    await self.TrakkaDB.putList(list);
    // listId: tempId — this queue entry *is* the list's own still-pending
    // create, so it's attributed to itself (see the listId/itemId doc
    // comment above resolveQueueTargetIds below).
    await self.TrakkaDB.enqueueRequest({ method, path: pathname, body, tempId, dependsOnListTempId: null, listId: tempId, itemId: null, createdAt: now });
    scheduleSync();
    await broadcastQueueState(false);
    return new Response(JSON.stringify(list), { status: 202, headers });
  }

  if (pathname === '/api/v1/items' && method === 'POST') {
    const tempId = generateTempId('temp-item');
    const listId = body?.list_id;
    // Still-pending list created earlier in this same offline session.
    const dependsOnListTempId = typeof listId === 'string' && listId.startsWith('temp-list') ? listId : null;
    // ...body first so every optional field the create form can send (price,
    // target_price/alert_on_price_drop, is_urgent, recurrence_rule, ...) is
    // mirrored locally too, not just the handful this object used to name
    // explicitly — a priced item created offline must count toward the
    // list's total (see recomputeListTotalAmount below) the same as one
    // created online.
    const item = {
      ...body,
      id: tempId,
      list_id: listId,
      title: body?.title ?? '',
      url: body?.url || null,
      quantity: body?.quantity > 0 ? body.quantity : 1,
      done: false,
      position: body?.position ?? 0,
      created_at: now,
      updated_at: now,
    };
    await self.TrakkaDB.putItem(item);
    await recomputeListTotalAmount(listId);
    await self.TrakkaDB.enqueueRequest({ method, path: pathname, body, tempId, dependsOnListTempId, listId, itemId: tempId, createdAt: now });
    console.debug('[trakka-sync] queued offline item create', tempId, 'for list', listId, body);
    scheduleSync();
    await broadcastQueueState(false);
    return new Response(JSON.stringify(item), { status: 202, headers });
  }

  // Editing/deleting an already-synced list or item. Resolved BEFORE
  // enqueueing/applyOptimisticEdit runs, since a DELETE's optimistic apply
  // removes the row from the mirror — after that point there would be
  // nothing left to look list_id up from (see resolveQueueTargetIds).
  const { listId: targetListId, itemId: targetItemId } = await resolveQueueTargetIds(pathname, body);
  await self.TrakkaDB.enqueueRequest({ method, path: pathname, body, tempId: null, dependsOnListTempId: null, listId: targetListId, itemId: targetItemId, createdAt: now });
  scheduleSync();
  const optimistic = await applyOptimisticEdit(pathname, method, body);
  await broadcastQueueState(false);
  return new Response(JSON.stringify(optimistic ?? { queued: true }), { status: 202, headers });
}

async function applyOptimisticEdit(pathname, method, body) {
  const listMatch = pathname.match(/^\/api\/v1\/lists\/(\d+)$/);
  const itemMatch = pathname.match(/^\/api\/v1\/items\/(\d+)$/);
  const now = new Date().toISOString();

  if (listMatch) {
    const id = Number(listMatch[1]);
    if (method === 'DELETE') {
      await self.TrakkaDB.deleteList(id);
      return null;
    }
    const existing = (await self.TrakkaDB.getList(id)) || { id };
    const updated = { ...existing, ...body, id, updated_at: now };
    await self.TrakkaDB.putList(updated);
    return updated;
  }

  if (itemMatch) {
    const id = Number(itemMatch[1]);
    if (method === 'DELETE') {
      const existing = await self.TrakkaDB.getItem(id);
      await self.TrakkaDB.deleteItem(id);
      if (existing) await recomputeListTotalAmount(existing.list_id);
      return null;
    }
    const existing = (await self.TrakkaDB.getItem(id)) || { id };
    // Step 1: persist the item's own new state (done/quantity/price/...)
    // into the IndexedDB items store first — everything below (the list's
    // cached total, the response handed back to the page) is derived from
    // this row, so it must land before either of them.
    const updated = { ...existing, ...body, id, updated_at: now };
    applyRecurrenceCompletionOffline(updated, existing.done);
    await self.TrakkaDB.putItem(updated);
    // Step 2: recompute the parent list's cached total_amount from every
    // item now mirrored under it (including this one's new done/quantity/
    // price) — see recomputeListTotalAmount's own doc comment for why this
    // has to happen here rather than being left for the next online refetch.
    await recomputeListTotalAmount(updated.list_id);
    return updated;
  }

  return null;
}

// Recomputes and persists a list's cached total_amount — the "reste à
// dépenser" figure the dashboard/Espaces cards render via urlBadges'
// list.total_amount in app.js, mirroring internal/db.listSelect's own SUM
// (every priced, not-yet-done item's price * quantity; SQL NULL/JS null when
// nothing qualifies) — directly from whatever items are currently mirrored
// under `listId` in IndexedDB. The server only ever includes this figure on
// a list-shaped response (GET /api/v1/lists, GET /api/v1/lists/{id}); an
// item-shaped response (POST/PUT/PATCH/DELETE /api/v1/items/{id}) never
// carries it, and the list detail view's own "Reste à dépenser" is instead
// computed live from state.currentList.items on every render (see
// updateFinanceSummary/lineTotal in list_view.js) — so without this, a
// dashboard card's cached total would silently drift out of sync with the
// list detail view the moment an item's done/price/quantity changes,
// whether that change was applied while online (mirrorWriteResult) or
// offline (applyOptimisticEdit/resolveAgainstPendingCreate below). Called
// after every item mutation this file mirrors, in both cases, so the two
// views can never show a different number for the same list again.
async function recomputeListTotalAmount(listId) {
  if (listId === undefined || listId === null) return;
  const list = await self.TrakkaDB.getList(listId);
  if (!list) return; // list itself isn't mirrored (e.g. never opened on this device)
  const items = await self.TrakkaDB.getItemsByList(listId);
  let total = null;
  for (const item of items) {
    if (item.done || typeof item.price !== 'number') continue;
    const quantity = item.quantity > 0 ? item.quantity : 1;
    total = (total ?? 0) + item.price * quantity;
  }
  await self.TrakkaDB.putList({ ...list, total_amount: total });
}

// Resolves which list (and, for an item write, which item) a queued
// list-or-item write should be attributed to. Every queue entry carries a
// listId (and itemId, for an item-scoped write) precisely so app.js's
// "unsynced changes" indicator (refreshPendingChangeIndicators, driving the
// small dot on a list's card/detail header and an item's row) can be
// derived with one read of the queue, without re-parsing every entry's HTTP
// path itself. Must run BEFORE applyOptimisticEdit, since a queued DELETE's
// optimistic apply removes the row from the IndexedDB mirror — after that
// point there would be nothing left to look list_id up from.
async function resolveQueueTargetIds(pathname, body) {
  const listMatch = pathname.match(/^\/api\/v1\/lists\/([^/]+)$/);
  if (listMatch) {
    return { listId: decodeId(listMatch[1]), itemId: null };
  }
  const itemMatch = pathname.match(/^\/api\/v1\/items\/([^/]+)$/);
  if (itemMatch) {
    const id = decodeId(itemMatch[1]);
    const existing = await self.TrakkaDB.getItem(id);
    const listId = existing ? existing.list_id : (body && body.list_id !== undefined ? body.list_id : null);
    return { listId, itemId: id };
  }
  return { listId: null, itemId: null };
}

// Applies a drag-and-drop reorder locally (position = index in item_ids,
// matching db.ReorderItems's own server-side assignment exactly) and queues
// the PUT for replay once back online. Returns every item currently mirrored
// under this list, freshly sorted, the same shape as a live 200 response —
// commitReorder (reorder.js) reads this straight into
// state.currentList.items. Any id in item_ids that isn't in the local mirror
// (shouldn't happen — see the comment at this function's one call site) is
// silently skipped rather than failing the whole request.
async function queueOfflineReorder(pathname, listId, body, headers, now) {
  const itemIds = Array.isArray(body?.item_ids) ? body.item_ids : [];
  const items = await self.TrakkaDB.getItemsByList(listId);
  const byId = new Map(items.map((item) => [String(item.id), item]));

  const reordered = [];
  itemIds.forEach((id, index) => {
    const item = byId.get(String(id));
    if (item) reordered.push({ ...item, position: index, updated_at: now });
  });
  if (reordered.length) await self.TrakkaDB.putItems(reordered);

  await self.TrakkaDB.enqueueRequest({ method: 'PUT', path: pathname, body, tempId: null, dependsOnListTempId: null, listId, itemId: null, createdAt: now });
  scheduleSync();
  await broadcastQueueState(false);

  const all = await self.TrakkaDB.getItemsByList(listId);
  all.sort((a, b) => a.position - b.position || a.id - b.id);
  return new Response(JSON.stringify(all), { status: 202, headers });
}

// ---------------------------------------------------------------------------
// Recurring-item completion, mirrored offline. This is a hand-kept JS port
// of applyRecurrenceCompletion/nextDueDate in internal/handlers/recurrence.go
// — there's no way to share code between the two runtimes — so that
// checking off a recurring item while offline advances it to its next
// occurrence exactly the way the server would, rather than leaving it
// stuck "done" until the sync queue flushes and a refetch corrects it. Any
// change to the Go version's rule handling must be mirrored here too.
// ---------------------------------------------------------------------------

function applyRecurrenceCompletionOffline(updated, wasDone) {
  if (wasDone || !updated.done || !updated.recurrence_rule) return;

  const next = nextDueDateOffline(updated.due_date || '', updated.recurrence_rule);
  if (!next) return; // unrecognized rule — leave it done, same as the Go path

  if (updated.recurrence_end_date && next > updated.recurrence_end_date) return;

  updated.due_date = next;
  updated.done = false;
}

function nextDueDateOffline(currentDueDate, rule) {
  const base = currentDueDate ? new Date(`${currentDueDate}T00:00:00Z`) : new Date();
  if (Number.isNaN(base.getTime())) return null;

  if (rule === 'DAILY') {
    base.setUTCDate(base.getUTCDate() + 1);
  } else if (rule === 'WEEKLY') {
    base.setUTCDate(base.getUTCDate() + 7);
  } else if (rule === 'MONTHLY') {
    base.setUTCMonth(base.getUTCMonth() + 1);
  } else if (rule === 'YEARLY') {
    base.setUTCFullYear(base.getUTCFullYear() + 1);
  } else {
    const match = /^EVERY_X_DAYS:([1-9][0-9]*)$/.exec(rule);
    if (!match) return null;
    base.setUTCDate(base.getUTCDate() + Number(match[1]));
  }

  return base.toISOString().slice(0, 10);
}

// A PATCH/PUT/DELETE against a temp-* id targets something that only ever
// existed locally. There is no server-side counterpart to call yet, so the
// change is folded into the still-queued "create" entry instead.
async function resolveAgainstPendingCreate(tempId, method, body, headers) {
  const isList = tempId.startsWith('temp-list');
  const queue = await self.TrakkaDB.getQueue();
  const entry = queue.find((e) => e.tempId === tempId);

  if (method === 'DELETE') {
    if (entry) await self.TrakkaDB.dequeue(entry.id);
    if (isList) {
      // Cancel any queued item-creates that were waiting on this list —
      // their target is being cancelled entirely, so they must not be
      // replayed against a list id that will never exist.
      for (const queued of queue) {
        if (queued.dependsOnListTempId === tempId) {
          await self.TrakkaDB.dequeue(queued.id);
        }
      }
      await self.TrakkaDB.deleteList(tempId);
    } else {
      const existing = await self.TrakkaDB.getItem(tempId);
      await self.TrakkaDB.deleteItem(tempId);
      if (existing) await recomputeListTotalAmount(existing.list_id);
    }
    await broadcastQueueState(false);
    // headers carries X-Trakka-Queued — apiRequest (app.js) checks that on
    // every response to know it needs to refresh its pending-list/item id
    // caches, so even this body-less cancellation response has to carry it,
    // same as every other response this function/queueOfflineWrite return.
    return new Response(null, { status: 204, headers });
  }

  if (entry) {
    entry.body = { ...entry.body, ...body };
    await self.TrakkaDB.updateQueueEntry(entry);
  }
  // The DELETE branch above already broadcasts after cancelling the pending
  // create; an edit merged into it must too, or the header's pending-count
  // badge (driven by broadcastQueueState's postMessage, unlike the per-item
  // dot, which apiRequest's own X-Trakka-Queued handling already refreshes
  // synchronously — see app.js) would keep showing whatever it last knew
  // until some unrelated queue mutation happened to fire it. The item/list
  // itself is never actually removed from the queue by an edit — see the
  // `entry` merge just above — so "pending" status is preserved either way;
  // this only fixes the header widget lagging behind that already-correct
  // state.
  await broadcastQueueState(false);

  const now = new Date().toISOString();
  if (isList) {
    const existing = (await self.TrakkaDB.getList(tempId)) || { id: tempId };
    const updated = { ...existing, ...body, id: tempId, updated_at: now };
    await self.TrakkaDB.putList(updated);
    return new Response(JSON.stringify(updated), { status: 200, headers });
  }

  const existing = (await self.TrakkaDB.getItem(tempId)) || { id: tempId };
  const updated = { ...existing, ...body, id: tempId, updated_at: now };
  applyRecurrenceCompletionOffline(updated, existing.done);
  await self.TrakkaDB.putItem(updated);
  await recomputeListTotalAmount(updated.list_id);
  return new Response(JSON.stringify(updated), { status: 200, headers });
}

let isFlushing = false;

async function flushQueue() {
  if (isFlushing) return;
  isFlushing = true;
  let processedAny = false;
  // sw.js's own fetch handler opportunistically calls flushQueue() on
  // *every* same-origin request while online (see the top-level 'fetch'
  // listener above) — hadEntries is what keeps that from broadcasting a
  // 'trakka-sync-status' message (and triggering a client-side repaint) on
  // every single ordinary API call once the queue is already empty; it only
  // ever fires when this attempt actually found something to do.
  let hadEntries = false;

  try {
    let queue = await self.TrakkaDB.getQueue();
    console.debug('[trakka-sync] flushQueue start, entries:', queue.length);

    if (queue.length > 0) {
      // Confirm this queue still belongs to whoever is actually
      // authenticated right now, before replaying a single entry against
      // their session. Without this, a previous account's still-queued
      // offline write could be replayed under a different, now-logged-in
      // account's cookies: this function is also called opportunistically
      // on *every* same-origin fetch while online (see the top-level
      // 'fetch' listener), including the very first GET /api/v1/me a freshly
      // logged-in session issues — so this can race
      // reconcileMirrorOwnershipFromMeResponse above rather than reliably
      // running after it. verifyMirrorOwnership re-derives the same
      // ownership decision directly (its own request to /api/v1/me), and
      // purges the queue along with everything else on a mismatch — so if
      // it did purge, the re-read below simply comes back empty and there is
      // nothing left to replay.
      await verifyMirrorOwnership();
      queue = await self.TrakkaDB.getQueue();
    }

    hadEntries = queue.length > 0;
    // "syncing" only ever means "flushQueue is actively replaying entries
    // right now" — skipped entirely when there's nothing to replay, so
    // reconnecting with an empty queue never flashes a spinner for no
    // reason.
    if (hadEntries) await broadcastQueueState(true, queue.length);
    for (const entry of queue) {
      try {
        console.debug('[trakka-sync] replaying', entry.method, entry.path, entry.tempId ? `(pending id ${entry.tempId})` : '', entry.body);
        const response = await fetch(entry.path, {
          method: entry.method,
          headers: { 'Content-Type': 'application/json' },
          credentials: 'same-origin',
          body: entry.body != null ? JSON.stringify(entry.body) : undefined,
        });
        console.debug('[trakka-sync] response', entry.method, entry.path, response.status);

        // A 401 means the session expired while this write sat queued —
        // unlike an ordinary 4xx rejection, retrying *will* fix this once
        // the user logs back in, so the entry (and everything queued after
        // it, to preserve ordering) must stay queued rather than being
        // discarded as if the server had permanently rejected it.
        if (response.status === 401) {
          console.debug('[trakka-sync] session expired mid-queue, leaving entry (and the rest) queued', entry.id);
          break;
        }

        if (response.ok) {
          if (entry.tempId) {
            const data = await response.json().catch(() => null);
            if (data && data.id !== undefined) {
              console.debug('[trakka-sync] synced, remapping', entry.tempId, '->', data.id);
              await remapTempId(queue, entry.tempId, data.id);
            } else {
              console.debug('[trakka-sync] synced but response carried no id to remap', entry.tempId, data);
            }
          }
          await self.TrakkaDB.dequeue(entry.id);
          processedAny = true;
          continue;
        }

        if (isDefinitiveClientError(response.status)) {
          console.debug('[trakka-sync] definitive rejection, discarding entry', entry.id, response.status);
          // The server has permanently rejected this exact mutation (a 403
          // "not a member of this house" from an orphaned mirror-ownership
          // mismatch, a 404 because its parent list/item no longer exists,
          // a 400 validation error, ...) — retrying the identical request
          // again later can never turn this into a success, so leaving it
          // queued forever would just mean it's replayed, and fails, on
          // every future flush. discardRejectedEntry both drops the entry
          // and reconciles whatever local IndexedDB state it left behind,
          // so a create the server never accepted (or an edit/delete of
          // something that's since vanished server-side) can't linger
          // forever as a "ghost" — present in the mirror, absent from the
          // backend, with nothing left to ever retry or correct it once its
          // one queue entry is gone.
          await discardRejectedEntry(entry, response.status, queue);
          processedAny = true;
          continue;
        }

        // Anything else (a 5xx, or a status this app's own backend never
        // actually returns) is treated as transient, exactly like fetch()
        // throwing below: stop here and retry this and every entry queued
        // after it on the next pass. Discarding real, not-yet-synced user
        // data on what might just be a momentary server-side hiccup would
        // be a worse outcome than leaving it queued a little longer.
        console.debug('[trakka-sync] transient failure, will retry on next flush', entry.id, response.status);
        break;
      } catch (err) {
        // Still offline: stop here, leave this and later entries queued
        // for the next attempt so ordering (and dependent temp ids) holds.
        console.debug('[trakka-sync] fetch threw (likely still offline), will retry on next flush', entry.id, err);
        break;
      }
    }
  } finally {
    isFlushing = false;
  }

  // Broadcast the final state whenever this attempt had anything to do,
  // whether or not the queue fully drained (still offline, or a 401 left
  // the rest queued): the header indicator and the per-list/per-item dots
  // need an up-to-date pending count either way, not just on a full
  // success. Skipped when hadEntries is false — see its own comment above.
  if (hadEntries) await broadcastQueueState(false);
  if (processedAny) await notifyClients({ type: 'trakka-sync-complete' });
}

// Everything in 400-499 except 401 (session expired — handled separately in
// flushQueue's loop, since logging back in *can* turn a retry into a
// success), 408 (request timeout) and 429 (rate limited) is a rejection no
// amount of retrying the exact same request will ever turn into a success —
// the request itself is invalid, forbidden, or targets something that
// doesn't exist. Used by flushQueue to decide when a queue entry must be
// discarded (and its local mirror state reconciled, see
// discardRejectedEntry) rather than left queued for another attempt.
function isDefinitiveClientError(status) {
  return status >= 400 && status < 500 && status !== 401 && status !== 408 && status !== 429;
}

// A queued mutation the server has permanently rejected (see
// isDefinitiveClientError). Drops the queue entry and cleans up whatever
// local IndexedDB state it left behind, so a create the server never
// actually accepted — or an edit/delete of something that no longer exists
// there — can't linger forever as a "ghost": present in the mirror, absent
// from the backend, and never retried again since the one queue entry that
// would have retried it is gone. `queue` is the in-memory array flushQueue is
// currently iterating, so a dependent entry cancelled here (see the tempId
// branch below) is also skipped for the rest of this same pass, exactly like
// remapTempId already does for a *successful* create.
async function discardRejectedEntry(entry, status, queue) {
  await self.TrakkaDB.dequeue(entry.id);

  if (entry.tempId) {
    // This was itself a still-pending "create" that the server refused
    // outright (a 404 "liste introuvable" because its parent list was
    // itself deleted before this could sync, a validation error, ...) —
    // there is no server-side row to reconcile against, so the only correct
    // outcome is to remove the local phantom and anything that depended on
    // it, exactly like the user explicitly deleting it mid-queue already
    // does (see resolveAgainstPendingCreate's DELETE branch above, which
    // this mirrors).
    if (entry.tempId.startsWith('temp-list')) {
      for (const queued of queue) {
        if (queued.dependsOnListTempId === entry.tempId) {
          await self.TrakkaDB.dequeue(queued.id);
        }
      }
      await self.TrakkaDB.deleteList(entry.tempId); // cascades to its items
    } else {
      const existing = await self.TrakkaDB.getItem(entry.tempId);
      await self.TrakkaDB.deleteItem(entry.tempId);
      if (existing) await recomputeListTotalAmount(existing.list_id);
    }
    return;
  }

  if (status === 404) {
    // The edited/deleted resource itself no longer exists server-side —
    // keeping it in the local mirror would show the user something the
    // backend has no record of at all, with nothing left to ever correct it
    // (its queue entry, the only thing that would have retried it, is gone).
    if (entry.itemId != null) {
      const existing = await self.TrakkaDB.getItem(entry.itemId);
      await self.TrakkaDB.deleteItem(entry.itemId);
      if (existing) await recomputeListTotalAmount(existing.list_id);
    } else if (entry.listId != null) {
      await self.TrakkaDB.deleteList(entry.listId);
    }
    return;
  }

  // Any other definitive rejection (400 validation, 403 forbidden, 409
  // conflict, ...) of an edit to something that still exists server-side:
  // the queued mutation itself is dropped, but the resource is still real,
  // so re-fetch its parent list to pull the true, current server state back
  // over whatever the optimistic edit locally guessed wrong — otherwise the
  // mirror would keep showing a change the server never actually applied,
  // with nothing left to ever correct it.
  if (entry.listId != null && !String(entry.listId).startsWith('temp-list')) {
    await reconcileListFromServer(entry.listId);
  }
}

// Best-effort re-fetch of one list (with its items) straight from the
// server, to pull the mirror back in line with reality after a queued edit
// was rejected for a reason that leaves the underlying resource itself
// untouched (see discardRejectedEntry). Deliberately swallows every
// failure: if the server can't be reached right now there's nothing more
// this pass can do about it, and the next successful GET
// /api/v1/lists/{id} (whenever the user next opens that list) will correct
// the mirror the ordinary way regardless.
async function reconcileListFromServer(listId) {
  try {
    const path = `/api/v1/lists/${listId}`;
    const response = await fetch(path, { credentials: 'same-origin' });
    if (response.ok) {
      await mirrorReadResponse(new URL(path, self.location.origin), response);
    }
  } catch {
    // Offline, or the list is itself gone — leave the mirror as-is.
  }
}

// Tells every open tab the offline sync queue's current size, and whether
// flushQueue is actively replaying it right now — drives the header's
// pending/syncing/synced indicator and the per-list/per-item "unsynced
// changes" dots (see app.js's handleSyncStatusMessage/
// refreshPendingChangeIndicators), and the trakka:sync-pending/
// trakka:sync-complete window events the rest of the UI listens for.
// Called after every point that adds to or removes from the queue
// (queueOfflineWrite/queueOfflineReorder/resolveAgainstPendingCreate above)
// and at the start/end of flushQueue itself.
async function broadcastQueueState(syncing, pendingOverride) {
  const pending = pendingOverride !== undefined ? pendingOverride : (await self.TrakkaDB.getQueue()).length;
  await notifyClients({ type: 'trakka-sync-status', pending, syncing });
}

async function remapTempId(queue, tempId, realId) {
  const isList = tempId.startsWith('temp-list');

  if (isList) {
    const list = await self.TrakkaDB.getList(tempId);
    if (list) {
      // Re-parent any locally-mirrored items BEFORE deleting the temp list
      // record: TrakkaDB.deleteList() cascades to every item still filed
      // under that list_id, and until this point that includes items
      // whose own "create" is later in this same queue.
      const orphanedItems = await self.TrakkaDB.getItemsByList(tempId);
      for (const item of orphanedItems) {
        await self.TrakkaDB.putItem({ ...item, list_id: realId });
      }
      await self.TrakkaDB.deleteList(tempId);
      await self.TrakkaDB.putList({ ...list, id: realId });
    }
  } else {
    const item = await self.TrakkaDB.getItem(tempId);
    if (item) {
      await self.TrakkaDB.deleteItem(tempId);
      await self.TrakkaDB.putItem({ ...item, id: realId });
    }
  }

  // Mutate the in-memory queue entries (not just IndexedDB) so this same
  // flushQueue() pass picks up the corrected list_id when it reaches them.
  for (const queued of queue) {
    if (queued.dependsOnListTempId === tempId) {
      queued.dependsOnListTempId = null;
      queued.body = { ...queued.body, list_id: realId };
      await self.TrakkaDB.updateQueueEntry(queued);
    }
  }
}

// payload defaults to the original 'trakka-sync-complete' shape this
// function always sent before it grew a parameter — broadcastQueueState
// above is what actually sends the newer 'trakka-sync-status' payload.
async function notifyClients(payload = { type: 'trakka-sync-complete' }) {
  const clients = await self.clients.matchAll({ includeUncontrolled: true, type: 'window' });
  for (const client of clients) {
    client.postMessage(payload);
  }
}

async function scheduleSync() {
  if (!('sync' in self.registration)) return;
  try {
    await self.registration.sync.register(SYNC_TAG);
  } catch {
    // Background Sync unsupported or denied (all of iOS/iPadOS Safari, and
    // some browsers without the permission granted) — the page's 'online'
    // handler and the opportunistic per-fetch check above cover this.
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function withoutItems(list) {
  const { items, ...rest } = list;
  return rest;
}

function extractTempId(pathname) {
  const match = pathname.match(/^\/api\/v1\/(?:lists|items)\/(temp-[^/]+)$/);
  return match ? match[1] : null;
}

function generateTempId(prefix) {
  const random = (self.crypto && self.crypto.randomUUID)
    ? self.crypto.randomUUID()
    : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `${prefix}-${random}`;
}

function decodeId(raw) {
  if (raw == null) return null;
  if (raw.startsWith('temp-')) return raw;
  const n = Number(raw);
  return Number.isFinite(n) ? n : raw;
}

function jsonError(headers, status, message) {
  return new Response(JSON.stringify({ error: message }), { status, headers });
}
