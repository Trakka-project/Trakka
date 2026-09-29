'use strict';

// Manual drag-and-drop reordering of a list's items — the "⇅ Réordonner"
// button in the list detail header (shares `state`, `listEls`, `apiRequest`,
// `t`, `showError`, `TrakkaToast`, `renderItems`, `listIcon`, `typeLabel`,
// `IS_TOUCH_DEVICE`/`gestureVibrate` with app.js/list_view.js/gestures.js —
// same classic-<script>-tags shared-scope pattern every other feature file
// in this app already uses).
//
// While active, list_view.js's renderItems() is bypassed in favor of
// renderReorderList below (see the branch added at renderItems' own top):
// every item — active and done alike, flattened into one column — is shown
// with a ☰ handle, and everything else that could mutate the list mid-drag
// (the quick-add bar, the done/finance sections) is hidden. Nothing is sent
// to the server until "Valider l'ordre" is pressed (commitReorder); "Annuler"
// (or navigating back to the dashboard) discards the draft order and leaves
// the list exactly as it was.
//
// Desktop uses the native HTML5 Drag and Drop API (draggable="true" +
// dragstart/dragover/dragend, delegated once on the shared container rather
// than re-attached per row on every render); touch devices — which mostly
// never fire those events at all — get a small hand-rolled touch-driven
// equivalent instead, restricted to starting from the handle specifically
// (rather than anywhere on the row) so the rest of a long reorder list stays
// natively scrollable. Both paths converge on the same mechanism: the row
// being dragged is physically moved in the live DOM (insertBefore) the
// moment the pointer crosses a neighboring row's midpoint, so the list
// visually reorders in real time; reorderDraftOrder itself is only resynced
// from the DOM's current order once the drag actually ends.

const reorderEls = {
  toggleButton: document.getElementById('reorder-list-button'),
  // Same action shown directly in the header on wide screens instead of
  // inside #list-options-sheet — see #list-desktop-actions' comment in
  // index.html — kept in sync with toggleButton everywhere its
  // hidden/disabled state changes below.
  toggleButtonDesktop: document.getElementById('reorder-list-button-desktop'),
  actionsBar: document.getElementById('reorder-actions-bar'),
  confirmButton: document.getElementById('reorder-confirm-button'),
  cancelButton: document.getElementById('reorder-cancel-button'),
};

// Static, hard-coded icon markup (never interpolates user data, same rule as
// every other *_ICON_SVG constant in this app) for each row's drag handle.
const REORDER_GRIP_ICON_SVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="h-5 w-5" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16"/></svg>';

let reorderMode = false;
// Array of item objects in their current draft order — only meaningful
// while reorderMode is true. Distinct from state.currentList.items, which
// is left untouched until commitReorder's request actually succeeds, so
// "Annuler" (or any failure) has nothing to undo.
let reorderDraftOrder = null;

function isReorderModeActive() {
  return reorderMode;
}

// Reordering needs every item to already have a real, permanent id — an
// item still sitting in the offline sync queue (a temp-item-* id, see
// isOfflineQueuedItem in list_view.js) has none yet, so it can't be named in
// a PUT .../reorder request at all. Rather than silently reordering around
// such an item (which would desync the moment it does sync), the button
// simply stays hidden until every item has synced. Also hidden with fewer
// than two items, since there is nothing to reorder.
function reorderAvailable() {
  const list = state.currentList;
  if (!list) return false;
  const items = (list.items || []).filter((item) => !item.pendingDelete);
  if (items.length < 2) return false;
  return items.every((item) => typeof item.id === 'number');
}

// Called from list_view.js's renderItems() (the ordinary, non-reorder
// branch) so the button's visibility always reflects the current list's
// state — including a temp-item-* id turning into a real one once the
// offline queue flushes and refreshCurrentList() re-renders.
function updateReorderButtonVisibility() {
  const hidden = reorderMode || !reorderAvailable();
  reorderEls.toggleButton.hidden = hidden;
  reorderEls.toggleButtonDesktop.hidden = hidden;
}

function enterReorderMode() {
  if (!reorderAvailable()) return;
  // closeListOptionsSheet is defined in list_view.js — this button now lives
  // inside #list-options-sheet (see the mobile-header redesign), so entering
  // reorder mode has to close it first, the same as every other row there.
  closeListOptionsSheet();
  hideError();
  // exitSelectionModeIfActive is defined in selection.js — multi-select
  // mode's bulk actions bar and this mode's own bar both take over the
  // bottom of the screen, so only one of the two can be active at once.
  exitSelectionModeIfActive();
  reorderMode = true;
  const items = (state.currentList.items || []).filter((item) => !item.pendingDelete);
  // Matches the server's own ORDER BY position ASC, id ASC (internal/db's
  // ListItemsByList) so the draft starts from exactly what the user is
  // already looking at, active/done sections merged back into one sequence.
  reorderDraftOrder = [...items].sort((a, b) => a.position - b.position || a.id - b.id);
  renderItems();
}

// opts.render lets callers that are about to re-render anyway (commitReorder,
// which calls renderItems() itself right after) skip a redundant extra pass.
function exitReorderMode(opts = {}) {
  const { render = true } = opts;
  reorderMode = false;
  reorderDraftOrder = null;
  reorderEls.actionsBar.hidden = true;
  listEls.createItemFormAnchor.hidden = false;
  if (render) renderItems();
}

// Discards the in-progress draft without saving anything — called by
// showDashboard/selectList (list_view.js) so leaving the list mid-drag
// can't strand the UI in reorder mode, and by refreshCurrentList (a
// background sync landing while reordering is rare, but state.currentList
// gets fully replaced there, which would leave reorderDraftOrder pointing at
// stale item objects).
function exitReorderModeIfActive() {
  if (reorderMode) exitReorderMode({ render: false });
}

async function commitReorder() {
  if (!reorderDraftOrder || state.currentListId === null) return;
  const itemIds = reorderDraftOrder.map((item) => item.id);
  try {
    const updated = await apiRequest(`/lists/${state.currentListId}/reorder`, {
      method: 'PUT',
      body: JSON.stringify({ item_ids: itemIds }),
    });
    state.currentList.items = updated;
    TrakkaToast.success(t('items.reorderSuccess'));
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  } finally {
    exitReorderMode();
  }
}

reorderEls.toggleButton.addEventListener('click', enterReorderMode);
reorderEls.toggleButtonDesktop.addEventListener('click', enterReorderMode);
reorderEls.cancelButton.addEventListener('click', () => exitReorderMode());
reorderEls.confirmButton.addEventListener('click', commitReorder);

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

function buildReorderRow(item) {
  const li = document.createElement('li');
  li.className =
    'reorder-row flex items-center gap-3 rounded-2xl border border-slate-200 dark:border-slate-800 bg-slate-50 dark:bg-slate-800/30 p-3 shadow-sm';
  li.dataset.itemId = String(item.id);
  li.draggable = true;

  const handle = document.createElement('span');
  handle.className =
    'reorder-handle flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-slate-400 dark:text-slate-500';
  handle.innerHTML = REORDER_GRIP_ICON_SVG;
  handle.setAttribute('aria-hidden', 'true');
  li.appendChild(handle);

  const title = document.createElement('span');
  title.className = item.done
    ? 'min-w-0 flex-1 truncate text-base font-medium text-slate-500 line-through opacity-60'
    : 'min-w-0 flex-1 truncate text-base font-medium text-slate-900 dark:text-slate-100';
  title.textContent =
    item.quantity > 1 ? `${item.title} × ${item.quantity}` : item.title;
  li.appendChild(title);

  return li;
}

// Renders the flat, draggable draft list directly from reorderDraftOrder —
// the counterpart to list_view.js's renderItems for the active/done split,
// invoked instead of it (see the branch at the top of renderItems) for as
// long as reorderMode stays true.
function renderReorderList() {
  const list = state.currentList;
  if (!list || !reorderDraftOrder) return;

  listEls.itemsHeading.textContent = `${listIcon(list)} ${list.name} (${typeLabel(list.type)})`;
  listEls.itemsRemainingCount.textContent = t('items.reorderHint');

  listEls.financeSummary.hidden = true;
  listEls.createItemFormAnchor.hidden = true;
  listEls.doneSection.hidden = true;
  reorderEls.toggleButton.hidden = true;
  reorderEls.toggleButtonDesktop.hidden = true;

  listEls.itemsActive.replaceChildren();
  for (const item of reorderDraftOrder) {
    listEls.itemsActive.appendChild(buildReorderRow(item));
  }

  reorderEls.actionsBar.hidden = false;
}

// ---------------------------------------------------------------------------
// Drag & drop mechanics — shared by both the HTML5 DnD path (desktop) and
// the touch path (mobile) below.
// ---------------------------------------------------------------------------

// The classic "closest sortable target" algorithm: among every row other
// than the one being dragged, finds the one whose vertical center sits just
// below `y` (the smallest negative offset) — that row is where the dragged
// element should land in front of. Returns null when `y` is below every
// remaining row, meaning the dragged element belongs at the very end.
function reorderRowAfter(dragging, y) {
  const rows = [...listEls.itemsActive.querySelectorAll('.reorder-row')].filter((row) => row !== dragging);
  let closest = null;
  let closestOffset = Number.NEGATIVE_INFINITY;
  for (const row of rows) {
    const box = row.getBoundingClientRect();
    const offset = y - (box.top + box.height / 2);
    if (offset < 0 && offset > closestOffset) {
      closestOffset = offset;
      closest = row;
    }
  }
  return closest;
}

// Moves `row` to sit just before whatever reorderRowAfter(y) resolves to, if
// that isn't already where it is. Returns whether a move actually happened,
// which the touch handler below uses to know when to reset its own running
// drag offset (see attachReorderTouchHandlers).
function moveReorderRowTo(row, y) {
  const after = reorderRowAfter(row, y);
  if (after === row.nextSibling) return false;
  listEls.itemsActive.insertBefore(row, after);
  return true;
}

// Re-derives reorderDraftOrder from the live DOM order of .reorder-row
// elements — called once a drag actually ends (dragend/touchend), never
// during the drag itself, since every intermediate DOM move above is purely
// visual until then.
function syncReorderDraftFromDOM() {
  if (!reorderDraftOrder) return;
  const byId = new Map(reorderDraftOrder.map((item) => [String(item.id), item]));
  const rows = [...listEls.itemsActive.querySelectorAll('.reorder-row')];
  const next = rows.map((row) => byId.get(row.dataset.itemId)).filter(Boolean);
  if (next.length === reorderDraftOrder.length) reorderDraftOrder = next;
}

// ---------------------------------------------------------------------------
// Desktop: native HTML5 Drag and Drop API, delegated once on the shared
// container (listEls.itemsActive is never replaced across re-renders — only
// its children are — so these listeners never need re-attaching per row).
// ---------------------------------------------------------------------------

listEls.itemsActive.addEventListener('dragstart', (event) => {
  if (!reorderMode) return;
  const row = event.target.closest('.reorder-row');
  if (!row) {
    event.preventDefault();
    return;
  }
  row.classList.add('dragging');
  event.dataTransfer.effectAllowed = 'move';
  // Firefox refuses to start a drag at all unless dataTransfer carries some
  // data — the value itself isn't used by the drop handling below, which
  // reads DOM order instead once the drag ends.
  event.dataTransfer.setData('text/plain', row.dataset.itemId || '');
});

listEls.itemsActive.addEventListener('dragover', (event) => {
  if (!reorderMode) return;
  event.preventDefault(); // required for this element to be a valid drop target at all
  const dragging = listEls.itemsActive.querySelector('.reorder-row.dragging');
  if (dragging) moveReorderRowTo(dragging, event.clientY);
});

listEls.itemsActive.addEventListener('dragend', (event) => {
  if (!reorderMode) return;
  const row = event.target.closest('.reorder-row');
  if (row) row.classList.remove('dragging');
  syncReorderDraftFromDOM();
});

// ---------------------------------------------------------------------------
// Mobile: touch events, starting only from the ☰ handle (unlike the desktop
// path above, which allows a drag to start anywhere on the row) so the rest
// of a long reorder list stays natively scrollable with a plain touch
// anywhere else on a card. IS_TOUCH_DEVICE/gestureVibrate come from
// gestures.js, loaded before this file.
// ---------------------------------------------------------------------------

if (IS_TOUCH_DEVICE) {
  let touchDragRow = null;
  let touchStartY = 0;

  listEls.itemsActive.addEventListener(
    'touchstart',
    (event) => {
      if (!reorderMode || event.touches.length !== 1) return;
      const handle = event.target.closest('.reorder-handle');
      const row = handle && handle.closest('.reorder-row');
      if (!row) return;
      touchDragRow = row;
      touchStartY = event.touches[0].clientY;
      row.classList.add('dragging');
      row.style.position = 'relative';
      row.style.zIndex = '20';
      gestureVibrate(15);
    },
    { passive: true },
  );

  listEls.itemsActive.addEventListener(
    'touchmove',
    (event) => {
      if (!touchDragRow) return;
      const y = event.touches[0].clientY;
      // If crossing a neighbor's midpoint actually moved the row in the DOM,
      // the row's own untransformed resting position just changed too —
      // resetting the running offset to 0 (and re-baselining touchStartY at
      // the current finger position) is what keeps it tracking the finger
      // smoothly across that swap instead of visually jumping by about one
      // row's height.
      if (moveReorderRowTo(touchDragRow, y)) {
        touchStartY = y;
        touchDragRow.style.transform = 'translateY(0)';
      } else {
        touchDragRow.style.transform = `translateY(${y - touchStartY}px)`;
      }
    },
    { passive: true },
  );

  function finishTouchDrag() {
    if (!touchDragRow) return;
    touchDragRow.classList.remove('dragging');
    touchDragRow.style.transform = '';
    touchDragRow.style.position = '';
    touchDragRow.style.zIndex = '';
    touchDragRow = null;
    syncReorderDraftFromDOM();
  }

  listEls.itemsActive.addEventListener('touchend', finishTouchDrag, { passive: true });
  listEls.itemsActive.addEventListener('touchcancel', finishTouchDrag, { passive: true });
}
