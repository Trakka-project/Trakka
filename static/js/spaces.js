'use strict';

// "Espaces" (custom categories) view: a fourth tab alongside the dashboard,
// the planning view and the urgent view (shares `state`, `els`, `apiRequest`,
// `showError`/`hideError`, `t`, `TRASH_ICON_SVG`, `SHARE_ICON_SVG`,
// `refreshPendingBadge`, `buildListCard`, `urlBadges`, `progressBadge`,
// `noBadges`, `isPurchaseList` with app.js, `PENCIL_ICON_SVG` with
// list_view.js, and `openShareModal` with shares.js — same classic-
// <script>-tags shared-scope pattern as planning.js/urgent.js). A custom
// category ("space") is a personal, freeform way to group lists across
// their fixed `type` (e.g. a "Homelab" space mixing a shopping list, a todo
// list and a notes list) — see the "Custom categories" bullet in CLAUDE.md
// for the backend design this talks to. This file also owns the create/
// edit category modal, reused both from this tab and from the "new list"
// modal's category picker in app.js (via the CREATE_CATEGORY_OPTION_VALUE
// sentinel and the onCreated callback openCategoryModal accepts).

const spacesEls = {
  tabBadge: document.getElementById('tab-spaces-badge'),
  newCategoryButton: document.getElementById('new-category-button'),
  emptyNewCategoryButton: document.getElementById('empty-new-category-button'),
  spacesEmpty: document.getElementById('spaces-empty'),
  spacesList: document.getElementById('spaces-list'),
  categoryModal: document.getElementById('category-modal'),
  categoryModalTitle: document.getElementById('category-modal-title'),
  closeCategoryModalButton: document.getElementById('close-category-modal-button'),
  categoryForm: document.getElementById('category-form'),
  categoryName: document.getElementById('category-name'),
  categoryIcon: document.getElementById('category-icon'),
  categoryColor: document.getElementById('category-color'),
  iconPresetButtons: document.querySelectorAll('#category-form [data-icon-preset]'),
  colorPresetButtons: document.querySelectorAll('#category-form [data-color-preset]'),
  deleteCategoryButton: document.getElementById('delete-category-button'),
  categorySubmitButton: document.getElementById('category-submit-button'),
  spaceCardActionsSheet: document.getElementById('space-card-actions-sheet'),
  spaceCardActionsSheetTitle: document.getElementById('space-card-actions-sheet-title'),
  closeSpaceCardActionsSheetButton: document.getElementById('close-space-card-actions-sheet-button'),
  spaceCardActionsPinButton: document.getElementById('space-card-actions-pin-button'),
  spaceCardActionsPinIcon: document.getElementById('space-card-actions-pin-icon'),
  spaceCardActionsPinLabel: document.getElementById('space-card-actions-pin-label'),
};

// Sentinel option value for the "new list" modal's category <select>,
// mirroring CREATE_HOUSE_OPTION_VALUE in app.js — picking it opens the
// create-category modal instead of actually selecting a category.
const CREATE_CATEGORY_OPTION_VALUE = '__create__';

// The caller's custom categories, refreshed on every loadCustomCategories()
// call (startup, opening the Spaces tab, opening the "new list" modal, and
// after any category create/edit/delete) rather than kept incrementally in
// sync — the dataset is small and per-user, so a full refetch is cheap.
let customCategories = [];

// Every list in the current house that has a custom_category_id, each with
// its items already attached (a per-list `GET /lists/{id}` fan-out, the same
// pattern urgent.js/planning.js use) so buildListCard's item count and
// per-type badge are accurate — re-fetched on every loadSpacesView() call.
let spacesLists = [];

// Every Space shared directly with the caller (space_shares, regardless of
// pin state — see loadSharedCustomCategories), and every list reachable
// through one of them (regardless of which House it belongs to, since the
// whole point is a Space someone else owns) — both re-fetched on every
// loadSpacesView() call, the same "small per-user dataset" reasoning
// customCategories/spacesLists above already use. A category the caller
// merely owns never appears here; a category shared with them never
// appears in customCategories — the two arrays are always disjoint.
let sharedCategories = [];
let sharedCategoryLists = [];

// Every list reachable purely through a House-visible Space the caller has
// pinned (access_source 'house_member' — CLAUDE.md's "pinned house spaces"
// feature), which may belong to a *different* House than the one currently
// selected — see buildSharedCategorySection's house_member branch, which
// combines this with spacesLists above to show such a Space's full
// contents rather than only whatever happens to also be in the currently
// selected House. Re-fetched on every loadSpacesView() call, same reasoning
// as spacesLists/sharedCategoryLists.
let houseSpaceLists = [];

// The category currently open in the modal for editing, or null when the
// modal is in "create" mode — set by openCategoryModal, read by
// categoryForm's submit handler and the delete button.
let editingCategory = null;

// Set by openCategoryModal's caller (currently only app.js's "new list"
// modal) to learn about a category created through this modal without a
// tighter coupling between the two files — cleared on close.
let categoryCreatedCallback = null;

const CHEVRON_ICON_SVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="h-4 w-4 shrink-0 transition-transform group-open:rotate-90" aria-hidden="true"><path d="M9 6l6 6-6 6"/></svg>';

// Reads the IndexedDB custom-categories mirror, defaulting to [] whenever
// the module isn't available or the read itself fails — same shape as
// app.js's cachedHouses/cachedDashboardLists fallbacks. Extra defense-in-
// depth beyond the service worker's own mirror-wide isolation (see
// enforceMirrorOwnership in sw.js): custom_categories is the one mirrored
// entity that actually carries a user_id (houses/lists have no equivalent
// field to filter on — a house's membership isn't embedded in its own JSON),
// so whenever the current account is already known this filters out
// anything that doesn't belong to it. Skipped when state.currentUser isn't
// resolved yet (e.g. a fully offline reload where /me itself never
// resolved) rather than hiding every category until it is — the mirror is
// already correctly scoped by construction at that point.
async function cachedCustomCategories() {
  if (!window.TrakkaDB) return [];
  try {
    const categories = await window.TrakkaDB.getCustomCategories();
    if (state.currentUser) {
      return categories.filter((category) => category.user_id === state.currentUser.id);
    }
    return categories;
  } catch {
    return [];
  }
}

// Fetches the caller's custom categories and updates the "Espaces" tab's
// highlight dot as a side effect — called at startup (app.js's init), every
// time the Spaces tab is opened, and before the "new list" modal's category
// picker is populated, so all three stay in sync with the latest set. On
// failure (offline, or a transient server error) falls back to whatever the
// IndexedDB mirror last saw rather than wiping the tab's highlight dot and
// the category picker — see the Offline-First requirement in
// CLAUDE.md/docs/PWA.md. Errors are otherwise swallowed rather than shown
// via the error banner: this is a background/supporting fetch, not the
// user's primary action — loadSpacesView below surfaces its own error for
// the tab's main fetch (the house's lists) instead.
async function loadCustomCategories() {
  try {
    customCategories = await apiRequest('/custom-categories');
  } catch {
    customCategories = await cachedCustomCategories();
  }
  updateSpacesTabBadge();
  return customCategories;
}

// Fetches every Space shared directly with the caller (?shared_with_me=true
// — see db.ListSpacesSharedWithUser), the Space-level equivalent of
// shares.js's loadPinnedSharedLists/loadSharedView. This has a real offline
// mirror now (see sw.js's mirrorReadResponse/offlineReadFallback and db.js's
// STORE_SHARED_CATEGORIES), so a plain connectivity failure only leaves the
// "Espaces partagés avec moi" section temporarily empty when there's no
// service worker in play at all — swallowed rather than surfaced via the
// error banner either way, same as loadCustomCategories' own
// background-fetch reasoning above.
async function loadSharedCustomCategories() {
  try {
    sharedCategories = await apiRequest('/custom-categories?shared_with_me=true');
  } catch {
    sharedCategories = [];
  }
  updateSpacesTabBadge();
  return sharedCategories;
}

function updateSpacesTabBadge() {
  spacesEls.tabBadge.hidden = customCategories.length === 0 && sharedCategories.length === 0;
}

// Fills a <select> (either #list-category in the "new list" modal, or a
// future caller) with "Aucun", one option per known category, and the
// "+ Créer un nouvel espace" sentinel — always rebuilt from scratch rather
// than diffed, since the list is short and this only runs when a modal
// opens.
function populateCategorySelect(selectEl, selectedId) {
  selectEl.replaceChildren();

  const noneOption = document.createElement('option');
  noneOption.value = '';
  noneOption.textContent = t('modals.newList.categoryNone');
  selectEl.appendChild(noneOption);

  for (const category of customCategories) {
    const option = document.createElement('option');
    option.value = String(category.id);
    option.textContent = category.icon ? `${category.icon} ${category.name}` : category.name;
    selectEl.appendChild(option);
  }

  const createOption = document.createElement('option');
  createOption.value = CREATE_CATEGORY_OPTION_VALUE;
  createOption.textContent = t('modals.newList.categoryCreateOption');
  selectEl.appendChild(createOption);

  selectEl.value = selectedId !== null && selectedId !== undefined ? String(selectedId) : '';
}

// ---------------------------------------------------------------------------
// "Espaces" tab: categories rendered as collapsible sections, each holding
// every list (any type) attached to it.
// ---------------------------------------------------------------------------

function isSpacesTabActive() {
  // activeTab/setActiveTab live in planning.js, loaded before this file.
  return activeTab === 'spaces';
}

async function loadSpacesView() {
  await loadCustomCategories();
  await loadSharedCustomCategories();
  await loadSharedCategoryLists();

  if (state.currentHouseId === null) {
    spacesLists = [];
    houseSpaceLists = [];
    renderSpaces();
    return;
  }

  // Stale-while-revalidate: paint immediately from the IndexedDB mirror
  // before the network fetch below even starts — same reasoning as
  // urgent.js/planning.js's own loaders. houseSpaceLists (no offline
  // mirror, see loadPinnedHouseSpaceLists below) is left as whatever it
  // already was rather than blanked, since a stale value there is still
  // better than nothing while the fresh fetch is in flight.
  const cachedFirst = await cachedDashboardLists(state.currentHouseId);
  const cachedCategorized = cachedFirst.filter((list) => list.custom_category_id);
  if (cachedCategorized.length > 0) {
    spacesLists = cachedCategorized;
    renderSpaces();
  }

  try {
    const lists = await apiRequest(`/lists?house_id=${state.currentHouseId}`);
    const categorized = lists.filter((list) => list.custom_category_id);
    spacesLists = await Promise.all(
      categorized.map((list) => apiRequest(`/lists/${list.id}`).catch(() => ({ ...list, items: [] })))
    );
  } catch (err) {
    // Offline, or a transient server error: fall back to the IndexedDB
    // mirror (cachedDashboardLists, defined in app.js) instead of emptying
    // every space — see the Offline-First requirement in
    // CLAUDE.md/docs/PWA.md. A plain connectivity failure stays silent (the
    // header's network badge already says "Hors-ligne"); only a genuine
    // server-side error still raises the banner.
    const cached = await cachedDashboardLists(state.currentHouseId);
    spacesLists = cached.filter((list) => list.custom_category_id);
    if (!isNetworkError(err)) showError(err.message);
  }

  // loadPinnedHouseSpaceLists is defined in shares.js (same cross-file
  // resolution pattern as openShareModal/badgeFnForType elsewhere in this
  // file) — best-effort, same "requires connectivity, no offline mirror"
  // scoping as loadSharedCategoryLists above.
  houseSpaceLists = await loadPinnedHouseSpaceLists().catch(() => []);

  renderSpaces();
}

// Fetches every list reachable via ?shared_with_me=true and keeps only the
// ones belonging to a Space actually in sharedCategories — a list shared
// *individually* (list_shares) that happens to sit in one of the owner's
// other, unshared categories must not be grouped under a "shared space"
// section here, since the caller has no access to that whole Space, only to
// this one list (it already shows up in the "Partagé avec moi" tab
// instead). Best-effort, same offline-mirror scoping as
// loadSharedCustomCategories above — the stub collection now resolves from
// STORE_SHARED_LISTS while offline (see sw.js), so this only actually falls
// through to the empty catch when there's no service worker in play at all.
async function loadSharedCategoryLists() {
  if (sharedCategories.length === 0) {
    sharedCategoryLists = [];
    return;
  }
  try {
    const stubs = await apiRequest('/lists?shared_with_me=true');
    const sharedCategoryIds = new Set(sharedCategories.map((category) => category.id));
    const categorized = stubs.filter((stub) => stub.custom_category_id && sharedCategoryIds.has(stub.custom_category_id));
    sharedCategoryLists = await Promise.all(
      categorized.map((stub) =>
        apiRequest(`/lists/${stub.id}`)
          .then((detailed) => ({ ...detailed, access_source: stub.access_source, access_permission: stub.access_permission }))
          .catch(() => stub)
      )
    );
  } catch {
    sharedCategoryLists = [];
  }
}

// Merges one or more list arrays, keeping the first occurrence of each id —
// used by buildSharedCategorySection's house_member branch to combine
// spacesLists/houseSpaceLists without showing the same list twice should a
// pinned House Space happen to also tag a list already visible through the
// currently selected House.
function dedupeListsById(lists) {
  const seen = new Set();
  const result = [];
  for (const list of lists) {
    if (seen.has(list.id)) continue;
    seen.add(list.id);
    result.push(list);
  }
  return result;
}

// Called after anything that might have changed the current house's
// categorized lists (offline sync completing, a language switch, a house
// switch, a list/category create-edit-delete) — a no-op unless this tab is
// the one currently visible.
function refreshSpacesIfActive() {
  if (isSpacesTabActive()) loadSpacesView();
}

// A purchase-oriented list shows its sourcing-domain badges, a todo list
// shows its completion progress, and a custom (freeform notes) list shows
// nothing extra — mirrors renderDashboardGrids' per-section badge choice in
// app.js, just resolved per-list here since a single space can mix types.
function badgeFnForType(type) {
  if (type === 'todo') return progressBadge;
  if (type === 'custom') return noBadges;
  return urlBadges;
}

function renderSpaces() {
  const sorted = [...customCategories].sort((a, b) => a.position - b.position || a.id - b.id);
  // The empty state is about the caller's *own* spaces specifically (its
  // "+ Créer" button only makes sense for a category the caller could
  // actually own) — a Space someone else shared with them doesn't count
  // toward it, so it stays showing even when sharedCategories has entries.
  spacesEls.spacesEmpty.hidden = sorted.length > 0;
  spacesEls.spacesList.replaceChildren();
  for (const category of sorted) {
    spacesEls.spacesList.appendChild(buildCategorySection(category));
  }

  // sharedCategories mixes two AccessSources (see
  // db.ListSpacesVisibleToUser): 'space_share' (the owner explicitly shared
  // it) and 'house_member' (nobody shared anything — the caller just
  // happens to be a member of a House that uses it, see the "pinned house
  // spaces" bullet in CLAUDE.md) — rendered as two separate headed sections
  // rather than one, so it's clear at a glance which is which.
  const explicitlyShared = sharedCategories.filter((category) => category.access_source === 'space_share');
  const houseVisible = sharedCategories.filter((category) => category.access_source === 'house_member');

  if (explicitlyShared.length > 0) {
    spacesEls.spacesList.appendChild(buildSharedSectionHeading(t('spaces.sharedSectionTitle')));
    for (const category of explicitlyShared) {
      spacesEls.spacesList.appendChild(buildSharedCategorySection(category));
    }
  }
  if (houseVisible.length > 0) {
    spacesEls.spacesList.appendChild(buildSharedSectionHeading(t('spaces.houseSectionTitle')));
    for (const category of houseVisible) {
      spacesEls.spacesList.appendChild(buildSharedCategorySection(category));
    }
  }
}

function buildSharedSectionHeading(text) {
  const heading = document.createElement('h2');
  heading.className = 'mt-2 text-sm font-semibold text-slate-500 dark:text-slate-400';
  heading.textContent = text;
  return heading;
}

function buildCategorySection(category) {
  const lists = spacesLists.filter((list) => list.custom_category_id === category.id);

  const details = document.createElement('details');
  details.open = true;
  details.className = 'group rounded-2xl border border-slate-200 dark:border-slate-800 bg-slate-50 dark:bg-slate-800/30 p-4';

  const summary = document.createElement('summary');
  summary.className =
    'flex cursor-pointer list-none items-center justify-between gap-2 marker:hidden [&::-webkit-details-marker]:hidden';

  const heading = document.createElement('div');
  heading.className = 'flex min-w-0 items-center gap-2';

  const chevron = document.createElement('span');
  chevron.className = 'flex shrink-0 items-center';
  chevron.innerHTML = CHEVRON_ICON_SVG; // static markup, no interpolated data — safe, see TRASH_ICON_SVG's comment in app.js
  heading.appendChild(chevron);

  const iconSpan = document.createElement('span');
  iconSpan.setAttribute('aria-hidden', 'true');
  iconSpan.className = 'text-xl leading-none';
  iconSpan.textContent = category.icon || '📁';
  heading.appendChild(iconSpan);

  const name = document.createElement('h2');
  name.className = 'truncate text-base font-semibold text-slate-900 dark:text-slate-100';
  name.textContent = category.name;
  heading.appendChild(name);

  const count = document.createElement('span');
  count.className =
    'shrink-0 rounded-full bg-slate-200/60 dark:bg-slate-700/50 px-2 py-0.5 text-xs font-medium text-slate-600 dark:text-slate-300';
  count.textContent = t('spaces.listCount', { count: lists.length });
  heading.appendChild(count);

  summary.appendChild(heading);

  const actions = document.createElement('div');
  actions.className = 'flex shrink-0 items-center gap-1';

  const editBtn = document.createElement('button');
  editBtn.type = 'button';
  editBtn.setAttribute('aria-label', t('spaces.editAriaLabel', { name: category.name }));
  editBtn.className =
    'flex h-9 w-9 items-center justify-center rounded-lg text-slate-500 hover:bg-slate-200 dark:hover:bg-slate-700 hover:text-slate-900 dark:hover:text-slate-200';
  editBtn.innerHTML = PENCIL_ICON_SVG;
  editBtn.addEventListener('click', (event) => {
    event.preventDefault(); // inside a <summary> — don't also toggle the <details>
    openCategoryModal(category);
  });
  actions.appendChild(editBtn);

  // Sharing a Space is the owning user's call alone (see
  // internal/handlers/shares.go's authorizeSpaceOwner) — every category
  // rendered on this tab is always one of the caller's own (see
  // customCategories above), so the button is unconditional here, unlike
  // buildListCard's own share button which is hidden on a card reached via
  // a share. openShareModal is defined in shares.js, resolved lazily the
  // same way openCategoryModal's own cross-file calls already are.
  const shareBtn = document.createElement('button');
  shareBtn.type = 'button';
  shareBtn.setAttribute('aria-label', t('spaces.shareAriaLabel', { name: category.name }));
  shareBtn.className =
    'flex h-9 w-9 items-center justify-center rounded-lg text-slate-500 hover:bg-violet-500/10 hover:text-violet-600 dark:hover:text-violet-400';
  shareBtn.innerHTML = SHARE_ICON_SVG;
  shareBtn.addEventListener('click', (event) => {
    event.preventDefault();
    openShareModal({ kind: 'space', id: category.id, name: category.name });
  });
  actions.appendChild(shareBtn);

  const deleteBtn = document.createElement('button');
  deleteBtn.type = 'button';
  deleteBtn.setAttribute('aria-label', t('spaces.deleteAriaLabel', { name: category.name }));
  deleteBtn.className =
    'flex h-9 w-9 items-center justify-center rounded-lg text-slate-500 hover:bg-rose-500/10 hover:text-rose-600 dark:hover:text-rose-400';
  deleteBtn.innerHTML = TRASH_ICON_SVG;
  deleteBtn.addEventListener('click', (event) => {
    event.preventDefault();
    deleteCategory(category);
  });
  actions.appendChild(deleteBtn);

  summary.appendChild(actions);
  details.appendChild(summary);

  const body = document.createElement('div');
  body.className = 'mt-4';
  if (lists.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'text-sm text-slate-500';
    empty.textContent = t('spaces.emptyCategory');
    body.appendChild(empty);
  } else {
    const grid = document.createElement('ul');
    grid.className = 'grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3';
    for (const list of lists) grid.appendChild(buildListCard(list, badgeFnForType(list.type)(list)));
    body.appendChild(grid);
  }
  details.appendChild(body);

  return details;
}

// The Space-level counterpart of buildCategorySection above, for a Space
// the caller doesn't own but can still see — either because the owner
// shared it directly (space_shares) or because the caller is simply a
// member of a House that uses it (space_house_pins, AccessSource
// 'house_member' — see the "pinned house spaces" bullet in CLAUDE.md): no
// edit/delete (only the owner may rename/delete a Space — see
// authorizeSpaceOwner), a 👥 "Partagé"/🏠 "De la Maison" badge depending on
// AccessSource (reusing the same 👥 badge buildListCard already shows on a
// shared list card) and, when pinned, a 📌 "Épinglée" one, and a [⋮] kebab
// opening #space-card-actions-sheet instead of icon buttons — there's only
// ever one action here, so a kebab+sheet needs no responsive desktop/mobile
// split the way buildListCard's pin control does.
function buildSharedCategorySection(category) {
  // A 'house_member' category's lists don't come from sharedCategoryLists —
  // that's sourced from GET /lists?shared_with_me=true, whose own
  // House-membership exclusion (see db.ListSharedListsForUser's own
  // comment) means it never returns a list from a House the caller already
  // belongs to, which is exactly where a house-visible Space's lists live.
  // spacesLists (the currently selected House's own categorized lists) plus
  // houseSpaceLists (any *other* House's lists reachable because this
  // Space is pinned — see loadSpacesView) cover it instead.
  const lists =
    category.access_source === 'house_member'
      ? dedupeListsById([...spacesLists, ...houseSpaceLists]).filter((list) => list.custom_category_id === category.id)
      : sharedCategoryLists.filter((list) => list.custom_category_id === category.id);

  const details = document.createElement('details');
  details.open = true;
  details.className = 'group rounded-2xl border border-slate-200 dark:border-slate-800 bg-slate-50 dark:bg-slate-800/30 p-4';

  const summary = document.createElement('summary');
  summary.className =
    'flex cursor-pointer list-none items-center justify-between gap-2 marker:hidden [&::-webkit-details-marker]:hidden';

  const heading = document.createElement('div');
  heading.className = 'flex min-w-0 flex-wrap items-center gap-2';

  const chevron = document.createElement('span');
  chevron.className = 'flex shrink-0 items-center';
  chevron.innerHTML = CHEVRON_ICON_SVG; // static markup, no interpolated data — safe, see TRASH_ICON_SVG's comment in app.js
  heading.appendChild(chevron);

  const iconSpan = document.createElement('span');
  iconSpan.setAttribute('aria-hidden', 'true');
  iconSpan.className = 'text-xl leading-none';
  iconSpan.textContent = category.icon || '📁';
  heading.appendChild(iconSpan);

  const name = document.createElement('h2');
  name.className = 'truncate text-base font-semibold text-slate-900 dark:text-slate-100';
  name.textContent = category.name;
  heading.appendChild(name);

  if (category.access_source === 'house_member') {
    heading.appendChild(badge(`🏠 ${t('spaces.houseBadge')}`, 'sky'));
  } else {
    heading.appendChild(badge(`👥 ${t('shares.sharedBadge')}`, 'violet'));
  }
  if (category.is_pinned_to_dashboard) {
    heading.appendChild(badge(`📌 ${t('shares.pinnedBadge')}`, 'amber'));
  }

  const count = document.createElement('span');
  count.className =
    'shrink-0 rounded-full bg-slate-200/60 dark:bg-slate-700/50 px-2 py-0.5 text-xs font-medium text-slate-600 dark:text-slate-300';
  count.textContent = t('spaces.listCount', { count: lists.length });
  heading.appendChild(count);

  summary.appendChild(heading);

  const kebabBtn = document.createElement('button');
  kebabBtn.type = 'button';
  kebabBtn.setAttribute('aria-label', t('spaces.actionsAriaLabel', { name: category.name }));
  kebabBtn.className =
    'flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-slate-500 hover:bg-slate-200 dark:hover:bg-slate-700 hover:text-slate-900 dark:hover:text-slate-200';
  // KEBAB_ICON_SVG is defined in list_view.js, loaded after this file —
  // safe because it's only read here at click time, well after every
  // script tag has finished loading (see buildListCard's own use of it in
  // app.js for the same already-established cross-file pattern).
  kebabBtn.innerHTML = KEBAB_ICON_SVG;
  kebabBtn.addEventListener('click', (event) => {
    event.preventDefault(); // inside a <summary> — don't also toggle the <details>
    openSpaceCardActionsSheet(category);
  });
  summary.appendChild(kebabBtn);

  details.appendChild(summary);

  const body = document.createElement('div');
  body.className = 'mt-4';
  if (lists.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'text-sm text-slate-500';
    empty.textContent = t('spaces.emptyCategory');
    body.appendChild(empty);
  } else {
    const grid = document.createElement('ul');
    grid.className = 'grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3';
    for (const list of lists) grid.appendChild(buildListCard(list, badgeFnForType(list.type)(list)));
    body.appendChild(grid);
  }
  details.appendChild(body);

  return details;
}

// ---------------------------------------------------------------------------
// Shared-space actions bottom sheet ([⋮] on a card built by
// buildSharedCategorySection above) — currently offers only the pin/unpin
// toggle, mirroring app.js's #list-card-actions-sheet almost exactly.
// ---------------------------------------------------------------------------

// The category currently open in the sheet, or null when it's closed — set
// by openSpaceCardActionsSheet, read by the pin button's own click handler
// below (same module-scope-tracked-target pattern as
// listCardActionsSheetList in app.js).
let spaceCardActionsSheetCategory = null;

function openSpaceCardActionsSheet(category) {
  spaceCardActionsSheetCategory = category;
  spacesEls.spaceCardActionsSheetTitle.textContent = category.name;
  const pinned = !!category.is_pinned_to_dashboard;
  spacesEls.spaceCardActionsPinIcon.textContent = pinned ? '📍' : '📌';
  spacesEls.spaceCardActionsPinLabel.textContent = t(pinned ? 'modals.listActions.unpinSpace' : 'modals.listActions.pinSpace');
  spacesEls.spaceCardActionsSheet.hidden = false;
  TrakkaScrollLock.lock();
}

function closeSpaceCardActionsSheet() {
  spaceCardActionsSheetCategory = null;
  spacesEls.spaceCardActionsSheet.hidden = true;
  TrakkaScrollLock.unlock();
}

spacesEls.closeSpaceCardActionsSheetButton.addEventListener('click', closeSpaceCardActionsSheet);
spacesEls.spaceCardActionsSheet.addEventListener('click', (event) => {
  if (event.target === spacesEls.spaceCardActionsSheet) closeSpaceCardActionsSheet();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !spacesEls.spaceCardActionsSheet.hidden) closeSpaceCardActionsSheet();
});

spacesEls.spaceCardActionsPinButton.addEventListener('click', () => {
  const category = spaceCardActionsSheetCategory;
  closeSpaceCardActionsSheet();
  if (category) toggleSpacePin(category);
});

// Pins or unpins a whole Space — one shared explicitly with the caller
// (space_shares) or merely visible to them through House membership
// (space_house_pins, see the "pinned house spaces" bullet in CLAUDE.md) —
// via PATCH /api/v1/custom-categories/{id}/share/pin. Every list reachable
// through it picks up the same pinned state automatically (see
// db.ListSharedListsForUser's space_share branch and
// db.ListPinnedHouseSpaceLists for the house_member one), so unlike
// app.js's toggleListPin there is nothing per-list to also update here.
// refreshSpacesIfActive() re-renders this tab in place; loadDashboard() is
// also re-run so a newly-pinned Space's lists show up there immediately too
// without waiting for the next unrelated dashboard refresh — this function
// is only ever called while the Spaces tab is the active one (see
// buildSharedCategorySection/openSpaceCardActionsSheet), so unlike
// toggleListPin there's no "already handled by refreshVisibleView" case to
// avoid double-fetching. A toast confirms the action, mirroring
// toggleListPin's own.
async function toggleSpacePin(category) {
  hideError();
  const pinning = !category.is_pinned_to_dashboard;
  try {
    await apiRequest(`/custom-categories/${category.id}/share/pin`, {
      method: 'PATCH',
      body: JSON.stringify({ pinned: pinning }),
    });
    await refreshSpacesIfActive();
    await loadDashboard();
    TrakkaToast.success(t(pinning ? 'spaces.pinnedToast' : 'spaces.unpinnedToast', { name: category.name }));
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  }
}

// Deletion is deferred behind a 5s undo grace period, mirroring removeList
// in app.js — the category disappears from the tab right away, and the
// actual DELETE only fires from TrakkaUndo's onCommit if the countdown runs
// out without the user clicking "Annuler". A deleted category's lists are
// never themselves deleted (the backend detaches them via ON DELETE SET
// NULL — see CLAUDE.md's "Custom categories" bullet), so they simply drop
// out of every space and become reachable from the ordinary dashboard tabs
// again once the delete actually lands.
function deleteCategory(category) {
  hideError();
  customCategories = customCategories.filter((c) => c.id !== category.id);
  updateSpacesTabBadge();
  refreshSpacesIfActive();

  TrakkaUndo.schedule({
    message: t('undo.categoryDeleted', { name: category.name }),
    undoLabel: t('undo.cancel'),
    onUndo: async () => {
      await loadCustomCategories();
      await refreshSpacesIfActive();
    },
    onCommit: async () => {
      try {
        await apiRequest(`/custom-categories/${category.id}`, { method: 'DELETE' });
      } catch (err) {
        if (!isNetworkError(err)) showError(err.message);
      }
      await loadCustomCategories();
      await refreshSpacesIfActive();
    },
  });
}

// ---------------------------------------------------------------------------
// Create/edit category modal
// ---------------------------------------------------------------------------

// Opens the modal in create mode (category === null) or edit mode
// (prefilled from an existing category). `onCreated(category)`, when given,
// fires once after a successful *create* only (not an edit) — used by
// app.js's "new list" modal to select the just-created category in its
// picker without this file needing to know anything about that form.
function openCategoryModal(category, { onCreated } = {}) {
  editingCategory = category || null;
  categoryCreatedCallback = onCreated || null;

  spacesEls.categoryForm.reset();
  spacesEls.categoryName.value = category?.name || '';
  spacesEls.categoryIcon.value = category?.icon || '';
  spacesEls.categoryColor.value = category?.color || '#0ea5e9';
  spacesEls.categoryModalTitle.textContent = t(editingCategory ? 'modals.newCategory.titleEdit' : 'modals.newCategory.titleCreate');
  spacesEls.categorySubmitButton.textContent = t(editingCategory ? 'modals.newCategory.submitEdit' : 'modals.newCategory.submitCreate');
  spacesEls.deleteCategoryButton.hidden = !editingCategory;

  spacesEls.categoryModal.hidden = false;
  TrakkaScrollLock.lock();
  spacesEls.categoryName.focus();
}

function closeCategoryModal() {
  spacesEls.categoryModal.hidden = true;
  // This modal can be opened on top of the "new list" modal (via its
  // category picker's "+ Créer un nouvel espace" option), so only release
  // the shared body scroll-lock if that other modal isn't still open —
  // otherwise closing this one would let the page scroll behind it.
  if (els.newListModal.hidden) {
    TrakkaScrollLock.unlock();
  }
  editingCategory = null;
  categoryCreatedCallback = null;
}

spacesEls.newCategoryButton.addEventListener('click', () => openCategoryModal(null));
spacesEls.emptyNewCategoryButton.addEventListener('click', () => openCategoryModal(null));
spacesEls.closeCategoryModalButton.addEventListener('click', closeCategoryModal);
spacesEls.categoryModal.addEventListener('click', (event) => {
  if (event.target === spacesEls.categoryModal) closeCategoryModal();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !spacesEls.categoryModal.hidden) closeCategoryModal();
});

for (const button of spacesEls.iconPresetButtons) {
  button.addEventListener('click', () => {
    spacesEls.categoryIcon.value = button.dataset.iconPreset;
  });
}
for (const button of spacesEls.colorPresetButtons) {
  button.addEventListener('click', () => {
    spacesEls.categoryColor.value = button.dataset.colorPreset;
  });
}

spacesEls.deleteCategoryButton.addEventListener('click', () => {
  if (!editingCategory) return;
  const category = editingCategory;
  closeCategoryModal();
  deleteCategory(category);
});

spacesEls.categoryForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  hideError();

  const name = spacesEls.categoryName.value.trim();
  if (!name) return;
  const payload = {
    name,
    icon: spacesEls.categoryIcon.value.trim(),
    color: spacesEls.categoryColor.value,
    position: editingCategory?.position ?? 0,
  };

  try {
    const isEdit = editingCategory !== null;
    const category = isEdit
      ? await apiRequest(`/custom-categories/${editingCategory.id}`, { method: 'PUT', body: JSON.stringify(payload) })
      : await apiRequest('/custom-categories', { method: 'POST', body: JSON.stringify(payload) });
    await loadCustomCategories();
    const callback = !isEdit ? categoryCreatedCallback : null;
    closeCategoryModal();
    if (callback) callback(category);
    await refreshSpacesIfActive();
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  }
});
