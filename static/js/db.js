'use strict';

// Trakka's IndexedDB layer. This file is loaded two ways: as a plain
// <script> on the page, and via importScripts() inside the service worker.
// It must therefore never touch window/document — only `indexedDB` and
// `self`, both of which exist in a Window (where `self === window`) and in
// a ServiceWorkerGlobalScope alike. Attaching to `self.TrakkaDB` makes the
// same implementation reachable from both places.
(function () {
  const DB_NAME = 'trakka';
  const DB_VERSION = 5;

  const STORE_HOUSES = 'houses';
  const STORE_LISTS = 'lists';
  const STORE_ITEMS = 'items';
  const STORE_QUEUE = 'queue';
  const STORE_CUSTOM_CATEGORIES = 'custom_categories';
  const STORE_META = 'meta';
  const STORE_SHARED_LISTS = 'shared_lists';
  const STORE_PINNED_HOUSE_SPACE_LISTS = 'pinned_house_space_lists';
  const STORE_SHARED_CATEGORIES = 'shared_categories';

  let dbPromise = null;

  function open() {
    if (dbPromise) return dbPromise;

    dbPromise = new Promise((resolve, reject) => {
      const request = indexedDB.open(DB_NAME, DB_VERSION);

      // onupgradeneeded only ever *adds* stores/indexes, guarded by
      // objectStoreNames.contains — it must never drop or recreate an
      // existing store, since that would wipe whatever offline data/queue
      // entries a user already has sitting in it. Bumping DB_VERSION and
      // adding one more guarded block here is the only supported way to
      // evolve this schema; see the v3 comment below for the pattern to
      // follow for a new store.
      request.onupgradeneeded = () => {
        const db = request.result;

        if (!db.objectStoreNames.contains(STORE_HOUSES)) {
          db.createObjectStore(STORE_HOUSES, { keyPath: 'id' });
        }
        if (!db.objectStoreNames.contains(STORE_LISTS)) {
          db.createObjectStore(STORE_LISTS, { keyPath: 'id' });
        }
        if (!db.objectStoreNames.contains(STORE_ITEMS)) {
          const items = db.createObjectStore(STORE_ITEMS, { keyPath: 'id' });
          items.createIndex('list_id', 'list_id');
        }
        if (!db.objectStoreNames.contains(STORE_QUEUE)) {
          db.createObjectStore(STORE_QUEUE, { keyPath: 'id', autoIncrement: true });
        }
        // v3: mirror of GET /api/v1/custom-categories, added so the
        // "Espaces" tab (static/js/spaces.js) has something to show while
        // offline — same read-through mirror pattern as houses/lists/items.
        if (!db.objectStoreNames.contains(STORE_CUSTOM_CATEGORIES)) {
          db.createObjectStore(STORE_CUSTOM_CATEGORIES, { keyPath: 'id' });
        }
        // v4: a single small key/value store used to record which
        // authenticated account this whole mirror currently belongs to (see
        // getActiveUserId/setActiveUserId below) — the service worker
        // consults and updates it on every GET /api/v1/me response to
        // enforce that the mirror is never shared across two different
        // accounts on the same browser (a shared/kiosk device, or a session
        // that expired and was replaced by a different login).
        if (!db.objectStoreNames.contains(STORE_META)) {
          db.createObjectStore(STORE_META, { keyPath: 'key' });
        }
        // v5: mirrors of the three "shared with me" stub collections
        // (GET /api/v1/lists?shared_with_me=true, ?pinned_house_spaces=true,
        // and GET /api/v1/custom-categories?shared_with_me=true) — kept
        // separate from STORE_LISTS/STORE_CUSTOM_CATEGORIES since each of
        // these queries has different inclusion rules than a caller's own
        // House-scoped lists/categories (see sw.js's mirrorReadResponse for
        // why they were previously skipped entirely). Storing just the stub
        // row (id + access_source/access_permission/is_pinned_to_dashboard)
        // is enough: a shared list's/category's own full detail already
        // lands in STORE_LISTS/STORE_ITEMS/STORE_CUSTOM_CATEGORIES the first
        // time it's opened, via the ordinary GET /lists/{id} (or
        // /custom-categories/{id}) mirroring these stub-driven views already
        // fan out to when online.
        if (!db.objectStoreNames.contains(STORE_SHARED_LISTS)) {
          db.createObjectStore(STORE_SHARED_LISTS, { keyPath: 'id' });
        }
        if (!db.objectStoreNames.contains(STORE_PINNED_HOUSE_SPACE_LISTS)) {
          db.createObjectStore(STORE_PINNED_HOUSE_SPACE_LISTS, { keyPath: 'id' });
        }
        if (!db.objectStoreNames.contains(STORE_SHARED_CATEGORIES)) {
          db.createObjectStore(STORE_SHARED_CATEGORIES, { keyPath: 'id' });
        }
      };

      // onblocked fires when another tab already holds an open connection
      // at an older DB_VERSION and never closes it — onupgradeneeded above
      // then can't run at all until that other connection goes away, so a
      // deployed update could otherwise leave this tab's IndexedDB mirror
      // stuck uninitialized indefinitely. The onversionchange handler below
      // (attached to every connection this tab itself opens) is the other
      // half of this: it makes an *older* tab close its own connection the
      // moment a newer one needs to upgrade, so onblocked here should only
      // ever fire transiently, if at all.
      request.onblocked = () => {
        console.warn('IndexedDB : mise à niveau bloquée par un autre onglet encore ouvert sur une version plus ancienne.');
      };

      request.onsuccess = () => {
        const db = request.result;
        // Without this, an older tab keeps its connection open forever,
        // which is exactly what triggers onblocked (above) in every other
        // tab whenever a new deployment bumps DB_VERSION. Closing
        // proactively — and dropping the memoized promise so this tab
        // re-opens fresh (and blocked-free) the next time it actually needs
        // the database — lets a multi-tab upgrade complete without asking
        // the user to manually close every other tab first.
        db.onversionchange = () => {
          db.close();
          dbPromise = null;
        };
        resolve(db);
      };
      request.onerror = () => {
        // Don't memoize a permanently-rejected promise: every read/write
        // helper below shares this same cached dbPromise, so a transient
        // failure here (a browser hiccup, a version-upgrade conflict with
        // another open tab, ...) would otherwise disable the entire
        // IndexedDB mirror — and with it every offline fallback in
        // app.js/list_view.js/planning.js/urgent.js/spaces.js — for the rest
        // of this page's lifetime. Clearing it lets the next open() call
        // retry with a fresh request instead of replaying the same failure
        // forever.
        dbPromise = null;
        reject(request.error);
      };
    });

    return dbPromise;
  }

  function requestToPromise(request) {
    return new Promise((resolve, reject) => {
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
  }

  function store(name, mode) {
    return open().then((db) => db.transaction(name, mode).objectStore(name));
  }

  function getAll(name) {
    return store(name, 'readonly').then((s) => requestToPromise(s.getAll()));
  }

  function getOne(name, key) {
    return store(name, 'readonly').then((s) => requestToPromise(s.get(key)));
  }

  function putOne(name, value) {
    return store(name, 'readwrite').then((s) => requestToPromise(s.put(value)));
  }

  function putMany(name, values) {
    return store(name, 'readwrite').then((s) => Promise.all(values.map((v) => requestToPromise(s.put(v)))));
  }

  function deleteOne(name, key) {
    return store(name, 'readwrite').then((s) => requestToPromise(s.delete(key)));
  }

  function getAllByIndex(name, indexName, value) {
    return store(name, 'readonly').then((s) => requestToPromise(s.index(indexName).getAll(value)));
  }

  // ---- Houses mirror ------------------------------------------------------
  //
  // Same read-through mirror pattern as lists/items, kept small (houses are
  // rarely created, and only online — see sw.js) purely so the "Maison"
  // selector and the dashboard still have something to show while offline.

  function getHouses() {
    return getAll(STORE_HOUSES);
  }

  function putHouses(houses) {
    return putMany(STORE_HOUSES, houses);
  }

  function getHouse(id) {
    return getOne(STORE_HOUSES, id);
  }

  // Offline-fallback convenience for app.js/list_view.js: the mirror has no
  // index on lists.house_id (see the comment on deleteHouse above for why a
  // full scan is fine here), so this filters in memory rather than adding one.
  function getListsByHouse(houseId) {
    return getLists().then((lists) => lists.filter((list) => list.house_id === houseId));
  }

  function putHouse(house) {
    return putOne(STORE_HOUSES, house);
  }

  // Cascades to every list (and, via deleteList, every item) filed under
  // this house — mirrors the server's ON DELETE CASCADE chain. No dedicated
  // index on lists.house_id: the mirror is small enough that a full-store
  // scan is cheap, and adding one would only matter at a scale this app
  // never reaches.
  function deleteHouse(id) {
    return getAll(STORE_LISTS)
      .then((lists) => lists.filter((list) => list.house_id === id))
      .then((lists) => Promise.all(lists.map((list) => deleteList(list.id))))
      .then(() => deleteOne(STORE_HOUSES, id));
  }

  // ---- Lists & items mirror ----------------------------------------------
  //
  // A local mirror of whatever the API last returned, kept up to date by
  // the service worker on every successful request. It exists so the app
  // still has something to show when the network is unreachable, and so
  // the service worker can synthesize an API-shaped response for GET
  // requests made while offline.

  // Sorted to match internal/db.ListListsForUser's own `ORDER BY
  // lists.created_at DESC` (newest first) — IndexedDB's getAll() otherwise
  // returns records in ascending primary-key order with no way to request
  // anything else, which silently diverged from the server's order (oldest
  // first vs newest first) and made a dashboard card grid visibly reorder
  // itself between the offline (cache) and online (network) renders of the
  // exact same house. Every caller of getLists()/getListsByHouse() renders
  // lists in whatever order this resolves to, so sorting once here — rather
  // than in each of app.js's/sw.js's own callers — is what keeps the two
  // paths from drifting apart again in the future.
  function compareListsNewestFirst(a, b) {
    const at = typeof a.created_at === 'string' ? a.created_at : '';
    const bt = typeof b.created_at === 'string' ? b.created_at : '';
    if (at !== bt) return at < bt ? 1 : -1;
    // Tie-break deterministically (two lists created within the same
    // millisecond) rather than leaving the order unspecified.
    const aId = Number(a.id);
    const bId = Number(b.id);
    if (Number.isFinite(aId) && Number.isFinite(bId)) return bId - aId;
    return 0;
  }

  function getLists() {
    return getAll(STORE_LISTS).then((lists) => lists.sort(compareListsNewestFirst));
  }

  function putLists(lists) {
    return putMany(STORE_LISTS, lists);
  }

  function getList(id) {
    return getOne(STORE_LISTS, id);
  }

  // Composite read used by app.js/list_view.js's offline fallbacks — mirrors
  // the shape a successful `GET /api/v1/lists/{id}` returns (list fields
  // plus an `items` array), so callers can treat a cache hit and a live
  // response identically. Resolves to null if the list itself isn't mirrored.
  function getListWithItems(id) {
    return getList(id).then((list) => {
      if (!list) return null;
      return getItemsByList(id).then((items) => ({ ...list, items }));
    });
  }

  // Collapses multiple records that resolve to the same logical id into one,
  // keyed on String(record.id) rather than the raw id so a numeric/string
  // mismatch (e.g. "5" vs 5) can never slip two "different" keys through as
  // if they were distinct records — defense in depth alongside the
  // isStaleWrite guard below, on top of every store already using `id` as
  // its IndexedDB keyPath (which alone should make a same-id duplicate
  // impossible, but doesn't protect against two records whose ids merely
  // *look* different). Whichever record sorts last in the input array wins.
  function dedupeById(records) {
    return Array.from(new Map(records.map((record) => [String(record.id), record])).values());
  }

  // True when `incoming` is an older version of `existing` (same id) by its
  // own updated_at timestamp — both are ISO-8601 strings, so a plain string
  // comparison sorts them correctly. Returns false (never stale) whenever
  // either side is missing a comparable updated_at.
  //
  // Deliberately NOT applied inside putItem/putItems/putList/putLists
  // themselves, even though that would be the more obviously "single choke
  // point" place to put it: those four are also used to persist a purely
  // local optimistic edit (sw.js's applyOptimisticEdit/
  // resolveAgainstPendingCreate, timestamped from the client's own clock) and
  // a write's own response (mirrorWriteResult — always the freshest possible
  // state for what it just changed, by construction, never "stale" relative
  // to what preceded it). Guarding those against a cross-clock comparison
  // risks silently dropping a genuine, not-yet-synced offline edit under
  // ordinary client/server clock skew — a worse bug than the one this is
  // meant to fix. A GET response is the one case where "is this actually
  // fresher than what's already mirrored" is genuinely uncertain (two
  // concurrent fetch() calls for the same resource can resolve out of
  // order), so freshItems/freshLists below are called explicitly, only from
  // sw.js's mirrorReadResponse, rather than folded into every write path.
  function isStaleWrite(existing, incoming) {
    if (!existing || !incoming) return false;
    if (typeof existing.updated_at !== 'string' || typeof incoming.updated_at !== 'string') return false;
    return incoming.updated_at < existing.updated_at;
  }

  // Drops any record in `records` that would be a stale overwrite (see
  // isStaleWrite) of what's already mirrored under the same id in `name`, by
  // comparing against one bulk read rather than one `get()` per record.
  async function withoutStaleRecords(name, records) {
    if (!records.length) return records;
    const existingList = await Promise.all(records.map((record) => getOne(name, record.id)));
    return records.filter((record, index) => !isStaleWrite(existingList[index], record));
  }

  // Filters `items`/`lists` down to only the ones that would actually move
  // the mirror forward — see isStaleWrite's doc comment for why this exists
  // and why it's a separate, explicitly-called step rather than baked into
  // putItem(s)/putList(s) themselves. Used by sw.js's mirrorReadResponse
  // before mirroring a GET response, so an out-of-order fetch (issued before
  // some other write, but resolving after it) can't silently revert e.g. a
  // done toggle back to unchecked.
  function freshItems(items) {
    return withoutStaleRecords(STORE_ITEMS, items);
  }

  function freshLists(lists) {
    return withoutStaleRecords(STORE_LISTS, lists);
  }

  function putList(list) {
    return putOne(STORE_LISTS, list);
  }

  function deleteList(id) {
    return getAllByIndex(STORE_ITEMS, 'list_id', id)
      .then((items) => Promise.all(items.map((item) => deleteOne(STORE_ITEMS, item.id))))
      .then(() => deleteOne(STORE_LISTS, id));
  }

  function getItem(id) {
    return getOne(STORE_ITEMS, id);
  }

  // Dedupes on the way out (see dedupeById above) so a view built from this
  // array — the list detail view's items, the finance summary it's computed
  // from, a dashboard/Espaces card's badge totals — can never show or sum
  // the same item twice, regardless of how a duplicate record might have
  // ended up in the store.
  function getItemsByList(listId) {
    return getAllByIndex(STORE_ITEMS, 'list_id', listId).then(dedupeById);
  }

  function putItems(items) {
    return putMany(STORE_ITEMS, items);
  }

  // True for a client-generated placeholder id (e.g. "temp-item-<uuid>") for
  // an item created offline and not yet synced — see sw.js's
  // generateTempId/extractTempId. Such an item can never appear in a server
  // response (the server has no idea it exists yet), so it must never be
  // treated as "stale" just because it's locally-only.
  function isTempId(id) {
    return typeof id === 'string' && id.startsWith('temp-');
  }

  // Prunes every item this device has mirrored under `listId` that isn't in
  // `currentIds` — the id list from a just-received, authoritative
  // `GET /api/v1/lists/{id}` (or `/api/v1/items?list_id=`) response — so an
  // item deleted server-side (from a different session/device, while this
  // one was offline) eventually gets removed from the local mirror too,
  // instead of lingering there forever and inflating this device's own
  // "N restant(s)" counts relative to what the server (and every other
  // device) reports. putItem(s) alone only ever upserts; nothing else ever
  // notices a locally-mirrored row that should no longer exist. A still-
  // queued offline-created item (isTempId) is deliberately kept rather than
  // pruned, since it can never legitimately appear in `currentIds` yet.
  async function pruneRemovedListItems(listId, currentIds) {
    const existing = await getAllByIndex(STORE_ITEMS, 'list_id', listId);
    const keep = new Set(currentIds.map(String));
    const staleIds = existing.filter((item) => !keep.has(String(item.id)) && !isTempId(item.id)).map((item) => item.id);
    if (!staleIds.length) return;
    const s = await store(STORE_ITEMS, 'readwrite');
    await Promise.all(staleIds.map((id) => requestToPromise(s.delete(id))));
  }

  function putItem(item) {
    return putOne(STORE_ITEMS, item);
  }

  function deleteItem(id) {
    return deleteOne(STORE_ITEMS, id);
  }

  // ---- Offline sync queue -------------------------------------------------
  //
  // One entry per mutating API call that could not reach the server. The
  // service worker is the only reader/writer of this store at replay time;
  // `tempId` / `dependsOnListTempId` let it patch a still-queued request's
  // body once an earlier queued "create" receives its real server id (e.g.
  // items added to a list that was itself created while offline).

  function enqueueRequest(entry) {
    return putOne(STORE_QUEUE, entry).then((id) => Object.assign(entry, { id }));
  }

  function getQueue() {
    return getAll(STORE_QUEUE);
  }

  function updateQueueEntry(entry) {
    return putOne(STORE_QUEUE, entry);
  }

  function dequeue(id) {
    return deleteOne(STORE_QUEUE, id);
  }

  // ---- Custom categories ("Spaces") mirror --------------------------------
  //
  // Same read-through mirror pattern as houses/lists/items: kept up to date
  // by the service worker on every successful GET/POST/PUT/DELETE against
  // /api/v1/custom-categories, so the "Espaces" tab still has something to
  // show while offline instead of going blank. There is no offline-write
  // support for categories (unlike lists/items) — see sw.js's
  // queueOfflineWrite — so this mirror is purely a read fallback.

  function getCustomCategories() {
    return getAll(STORE_CUSTOM_CATEGORIES);
  }

  function putCustomCategories(categories) {
    return putMany(STORE_CUSTOM_CATEGORIES, categories);
  }

  function getCustomCategory(id) {
    return getOne(STORE_CUSTOM_CATEGORIES, id);
  }

  function putCustomCategory(category) {
    return putOne(STORE_CUSTOM_CATEGORIES, category);
  }

  function deleteCustomCategory(id) {
    return deleteOne(STORE_CUSTOM_CATEGORIES, id);
  }

  // ---- "Shared with me" stub mirrors ---------------------------------------
  //
  // Read-through mirrors of the three stub collections listed in the v5
  // onupgradeneeded comment above — see sw.js's mirrorReadResponse/
  // offlineReadFallback for how these are kept up to date and served while
  // offline. putX replaces the whole store's contents on every successful
  // fetch (rather than merging) since a stale row here (a share since
  // revoked, a pin since removed) must not linger — the same "the collection
  // response is the source of truth" reasoning putLists/putHouses already
  // apply when the service worker calls them from a fresh GET.

  function getSharedLists() {
    return getAll(STORE_SHARED_LISTS);
  }

  function replaceSharedLists(stubs) {
    return replaceStore(STORE_SHARED_LISTS, stubs);
  }

  function getPinnedHouseSpaceLists() {
    return getAll(STORE_PINNED_HOUSE_SPACE_LISTS);
  }

  function replacePinnedHouseSpaceLists(stubs) {
    return replaceStore(STORE_PINNED_HOUSE_SPACE_LISTS, stubs);
  }

  function getSharedCategories() {
    return getAll(STORE_SHARED_CATEGORIES);
  }

  function replaceSharedCategories(stubs) {
    return replaceStore(STORE_SHARED_CATEGORIES, stubs);
  }

  // Clears then repopulates a store within one readwrite transaction, so a
  // reader can never observe a half-cleared state between the two steps.
  function replaceStore(name, values) {
    return store(name, 'readwrite').then(
      (s) =>
        new Promise((resolve, reject) => {
          const clearReq = s.clear();
          clearReq.onerror = () => reject(clearReq.error);
          clearReq.onsuccess = () => {
            if (!values.length) {
              resolve();
              return;
            }
            let remaining = values.length;
            let settled = false;
            values.forEach((value) => {
              const putReq = s.put(value);
              putReq.onerror = () => {
                if (!settled) {
                  settled = true;
                  reject(putReq.error);
                }
              };
              putReq.onsuccess = () => {
                remaining -= 1;
                if (remaining === 0 && !settled) {
                  settled = true;
                  resolve();
                }
              };
            });
          };
        })
    );
  }

  // ---- Mirror ownership (per-account isolation) ---------------------------
  //
  // Every other store above is a shared-scope mirror of whatever the API
  // last returned for *some* authenticated session on this browser — nothing
  // in its own shape says which account it belongs to. On a normal
  // single-user browser that's harmless, but on a shared/kiosk device where
  // two different Trakka accounts log in sequentially, a mirror left over
  // from the previous account must never be shown to (or mutated on behalf
  // of) the next one. getActiveUserId/setActiveUserId record the id of
  // whichever account this mirror is currently trusted to belong to; sw.js's
  // enforceMirrorOwnership is what actually compares and purges (see there
  // for the full mechanism) — this file only stores the value.

  function getActiveUserId() {
    return getOne(STORE_META, 'activeUserId').then((row) => (row ? row.value : null));
  }

  function setActiveUserId(userId) {
    return putOne(STORE_META, { key: 'activeUserId', value: userId });
  }

  // clearAll wipes every mirrored store, the pending offline write queue, and
  // the mirror-ownership record.
  //
  // This exists because the IndexedDB mirror outlives the session that filled
  // it: nothing cleared it on logout, so on a shared or family device the next
  // person to sign in had the previous user's houses, lists and items painted
  // straight onto their screen by hydrateFromCache() before any network call
  // could correct it — and any writes the previous user had queued offline
  // would have been replayed under the new user's session by flushQueue().
  // app.js calls this when a session ends (logout) or when /api/v1/me comes
  // back as a different account than the cache was built for; sw.js's
  // enforceMirrorOwnership (see getActiveUserId/setActiveUserId above) also
  // calls it directly, from inside the service worker, the moment it observes
  // a GET /api/v1/me response for a different account than the mirror's
  // current owner — this is what actually closes the gap for a browser that
  // never revisits the page online with a chance for app.js's own check to
  // run before going offline again.
  function clearAll() {
    return open().then(
      (db) =>
        new Promise((resolve, reject) => {
          const stores = [
            STORE_HOUSES,
            STORE_LISTS,
            STORE_ITEMS,
            STORE_QUEUE,
            STORE_CUSTOM_CATEGORIES,
            STORE_META,
            STORE_SHARED_LISTS,
            STORE_PINNED_HOUSE_SPACE_LISTS,
            STORE_SHARED_CATEGORIES,
          ].filter((name) => db.objectStoreNames.contains(name));
          if (!stores.length) {
            resolve();
            return;
          }
          const tx = db.transaction(stores, 'readwrite');
          stores.forEach((name) => tx.objectStore(name).clear());
          tx.oncomplete = () => resolve();
          tx.onerror = () => reject(tx.error);
          tx.onabort = () => reject(tx.error);
        })
    );
  }

  self.TrakkaDB = {
    clearAll,
    getHouses,
    putHouses,
    getHouse,
    putHouse,
    deleteHouse,
    getLists,
    putLists,
    getList,
    putList,
    deleteList,
    getListsByHouse,
    getListWithItems,
    getItem,
    getItemsByList,
    putItems,
    putItem,
    pruneRemovedListItems,
    freshItems,
    freshLists,
    deleteItem,
    enqueueRequest,
    getQueue,
    updateQueueEntry,
    dequeue,
    getCustomCategories,
    putCustomCategories,
    getCustomCategory,
    putCustomCategory,
    deleteCustomCategory,
    getSharedLists,
    replaceSharedLists,
    getPinnedHouseSpaceLists,
    replacePinnedHouseSpaceLists,
    getSharedCategories,
    replaceSharedCategories,
    getActiveUserId,
    setActiveUserId,
  };
})();
