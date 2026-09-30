'use strict';

// Multi-select ("Sélectionner") mode for the list detail view — shares
// `state`, `listEls`, `apiRequest`, `t`, `renderItems`, `lineTotal`,
// `formatEuro`, `fieldVisibilityFor`, `pendingToggles`, `commitItemPatches`,
// `runWithConcurrency`/`BULK_REQUEST_CONCURRENCY`, `reportBulkErrors`,
// `openLabelManageSheet`, `monthLabel` and `OVERLAY_SELECTOR` with
// app.js/list_view.js/gestures.js/planning.js — the same classic-<script>-
// tags shared-scope pattern reorder.js already uses.
//
// Entered from #list-options-sheet's "Sélectionner des articles" row (or its
// #list-desktop-actions twin on wide screens), or by a long press on any
// item card (see the end of buildItemRow). Unlike reorder mode, renderItems
// keeps rendering the ordinary active/done split: buildItemRow just builds
// each row in its selection shape (decorateSelectableRow below) — a
// selection checkbox in place of the done checkbox, a tap anywhere on the
// card toggling it, every per-item action and swipe gesture left off — and
// the quick-add bar gives way to #bulk-actions-bar, a fixed bottom bar with
// the selection count, the selection's estimated total (shopping-type
// lists), "Tout sélectionner/Tout désélectionner", "Annuler", and the bulk
// actions themselves.
//
// There is no bulk endpoint: every action below still sends one ordinary
// PATCH/DELETE /api/v1/items/{id} per item (BULK_REQUEST_CONCURRENCY at a
// time, see list_view.js), after applying the change to
// state.currentList.items optimistically — which is exactly what lets
// sw.js queue, mirror and temp-id-resolve each of them while offline with no
// change on its side.

const selectionEls = {
  toggleButton: document.getElementById('select-items-button'),
  // Same action shown directly in the header on wide screens instead of
  // inside #list-options-sheet — see #list-desktop-actions' comment in
  // index.html — kept in sync with toggleButton everywhere its hidden/
  // disabled state changes.
  toggleButtonDesktop: document.getElementById('select-items-button-desktop'),
  bar: document.getElementById('bulk-actions-bar'),
  cancelButton: document.getElementById('bulk-cancel-button'),
  count: document.getElementById('bulk-selection-count'),
  total: document.getElementById('bulk-selection-total'),
  toggleAllButton: document.getElementById('bulk-toggle-all-button'),
  doneButton: document.getElementById('bulk-done-button'),
  doneLabel: document.getElementById('bulk-done-label'),
  labelsButton: document.getElementById('bulk-labels-button'),
  targetMonthButton: document.getElementById('bulk-target-month-button'),
  deleteButton: document.getElementById('bulk-delete-button'),
  targetMonthSheet: document.getElementById('bulk-target-month-sheet'),
  targetMonthSubtitle: document.getElementById('bulk-target-month-subtitle'),
  targetMonthOptions: document.getElementById('bulk-target-month-options'),
  closeTargetMonthSheetButton: document.getElementById('close-bulk-target-month-sheet-button'),
  toastContainer: document.getElementById('toast-container'),
};

// List types whose selection gets a live estimated total in the bar —
// shopping/groceries/recurring_shopping. A `groceries` item has no price
// field in the UI (see FIELD_VISIBILITY_BY_TYPE), so in practice the total
// just stays hidden there unless an item still carries a price from before
// its list's type changed — see updateBulkActionsBar.
const SHOPPING_LIST_TYPES = new Set(['shopping', 'groceries', 'recurring_shopping']);

let selectionMode = false;
// Selected items, keyed by String(item.id) rather than by object:
// refreshCurrentList (a sync landing mid-selection) replaces every item
// object in state.currentList, and a selection must survive that.
const selectedItemIds = new Set();
// Keys of the selectable rows the last render drew, per section —
// "Tout sélectionner" targets the active rows plus the done rows only while
// #done-section is expanded, i.e. what's actually on screen.
let renderedActiveKeys = [];
let renderedDoneKeys = [];

function isSelectionModeActive() {
  return selectionMode;
}

function selectionKey(item) {
  return String(item.id);
}

function isItemSelected(item) {
  return selectedItemIds.has(selectionKey(item));
}

// An item the create form just added optimistically still carries its
// `local-*` placeholder id until its POST answers — an id neither the API
// nor sw.js knows, so there's nothing a bulk request could target yet. It
// becomes selectable on the very next render after its create resolves (to
// a real id online, or a temp-item-* id sw.js does know about offline).
function isSelectableItem(item) {
  return !(typeof item.id === 'string' && item.id.startsWith('local-'));
}

// The selected items, in list order, as live objects from
// state.currentList.items.
function selectedItems() {
  const list = state.currentList;
  if (!list) return [];
  return (list.items || []).filter((item) => !item.pendingDelete && selectedItemIds.has(selectionKey(item)));
}

function visibleSelectableKeys() {
  return listEls.doneSection.open ? [...renderedActiveKeys, ...renderedDoneKeys] : renderedActiveKeys;
}

// `initialItem` is the item a long press started from — selected right
// away, so the gesture itself counts as the first selection.
function enterSelectionMode(initialItem = null) {
  if (!state.currentList) return;
  hideError();
  // exitReorderModeIfActive is defined in reorder.js — the two modes each
  // take over the bottom of the screen, so only one can be active at once.
  exitReorderModeIfActive();
  selectionMode = true;
  selectedItemIds.clear();
  if (initialItem && isSelectableItem(initialItem)) selectedItemIds.add(selectionKey(initialItem));
  renderItems();
}

function hideSelectionChrome() {
  selectionEls.bar.hidden = true;
  // Reset rather than left stale for the next entry, which repaints both
  // (updateBulkActionsBar) before the bar is shown again.
  selectionEls.count.textContent = '';
  selectionEls.total.hidden = true;
  listEls.createItemFormAnchor.hidden = false;
  listEls.itemsSection.classList.remove('is-selecting');
  selectionEls.toastContainer.style.bottom = '';
}

// opts.render lets callers that are about to re-render anyway skip a
// redundant extra pass — same convention as reorder.js's exitReorderMode.
function exitSelectionMode({ render = true } = {}) {
  selectionMode = false;
  selectedItemIds.clear();
  if (!selectionEls.targetMonthSheet.hidden) closeBulkTargetMonthSheet();
  hideSelectionChrome();
  if (render) renderItems();
}

// Called by selectList/showDashboard (list_view.js) so a selection never
// carries over to another list or outlives the detail view, and by
// enterReorderMode (reorder.js).
function exitSelectionModeIfActive() {
  if (selectionMode) exitSelectionMode({ render: false });
}

function updateSelectButtonVisibility() {
  const list = state.currentList;
  const hasSelectable = Boolean(list) && (list.items || []).some((item) => !item.pendingDelete && isSelectableItem(item));
  const hidden = selectionMode || !hasSelectable;
  selectionEls.toggleButton.hidden = hidden;
  selectionEls.toggleButtonDesktop.hidden = hidden;
}

// Called at the end of every ordinary renderItems pass (list_view.js) with
// the active/done rows it just drew.
function syncSelectionWithRender(active, done) {
  renderedActiveKeys = active.filter(isSelectableItem).map(selectionKey);
  renderedDoneKeys = done.filter(isSelectableItem).map(selectionKey);
  updateSelectButtonVisibility();
  if (!selectionMode) {
    hideSelectionChrome();
    return;
  }
  // Whatever this render no longer shows — deleted, filtered out, or
  // replaced by a sync under a new id (a temp-item-* id turning real) —
  // drops out of the selection, so a bulk action can only ever reach rows
  // the user can actually see selected.
  const rendered = new Set([...renderedActiveKeys, ...renderedDoneKeys]);
  const hadSelection = selectedItemIds.size > 0;
  for (const key of selectedItemIds) {
    if (!rendered.has(key)) selectedItemIds.delete(key);
  }
  // Pruning just emptied a selection the user had made — leave the mode,
  // same as deselecting the last row by hand (setSelected below). This pass
  // already drew the rows in their selection shape, so redraw them once
  // more: selectionMode is false by then, so that pass lands in the early
  // return above instead of coming back here.
  if (hadSelection && selectedItemIds.size === 0) {
    exitSelectionMode();
    return;
  }
  selectionEls.bar.hidden = false;
  listEls.createItemFormAnchor.hidden = true;
  listEls.itemsSection.classList.add('is-selecting');
  updateBulkActionsBar();
}

function updateBulkActionsBar() {
  const list = state.currentList;
  if (!selectionMode || !list) return;
  const items = selectedItems();
  const count = items.length;
  const visibility = fieldVisibilityFor(list.type);

  selectionEls.count.textContent =
    count === 0 ? t('bulk.noneSelected') : t(count === 1 ? 'bulk.selectedOne' : 'bulk.selectedOther', { count });

  // Σ(unit price × quantity) over the selection only — lineTotal
  // (list_view.js) is the same per-line figure the rows and the finance
  // summary use, so the three can never disagree. Hidden outright when no
  // selected item has a price at all, rather than showing a misleading
  // "0,00 €".
  const priced = SHOPPING_LIST_TYPES.has(list.type) ? items.filter((item) => typeof item.price === 'number') : [];
  selectionEls.total.hidden = priced.length === 0;
  if (priced.length > 0) {
    const total = priced.reduce((sum, item) => sum + lineTotal(item), 0);
    selectionEls.total.textContent = t('bulk.total', { amount: formatEuro(total) });
  }

  const visibleKeys = visibleSelectableKeys();
  const allSelected = count > 0 && visibleKeys.every((key) => selectedItemIds.has(key));
  selectionEls.toggleAllButton.textContent = t(allSelected ? 'bulk.deselectAll' : 'bulk.selectAll');
  selectionEls.toggleAllButton.disabled = count === 0 && visibleKeys.length === 0;

  // "Cocher" unless every selected item is already done, in which case the
  // same button un-checks them — the same toggle the row checkbox is.
  selectionEls.doneButton.hidden = !visibility.done;
  selectionEls.doneLabel.textContent = t(count > 0 && items.every((item) => item.done) ? 'bulk.markUndone' : 'bulk.markDone');
  // Same gate as the create/edit forms' own target-month field (only
  // `shopping` plans purchases by month — see FIELD_VISIBILITY_BY_TYPE).
  selectionEls.targetMonthButton.hidden = !visibility.targetMonth;
  for (const button of [selectionEls.doneButton, selectionEls.labelsButton, selectionEls.targetMonthButton, selectionEls.deleteButton]) {
    button.disabled = count === 0;
  }

  // Lifts #toast-container (undo toasts, the "mois appliqué" confirmation)
  // above the bar instead of letting it cover the bar's own buttons.
  selectionEls.toastContainer.style.bottom = `${selectionEls.bar.offsetHeight}px`;
}

// ---------------------------------------------------------------------------
// Rows
// ---------------------------------------------------------------------------

// The leading 44px slot of a row in selection mode, in place of the done
// checkbox/line marker. Wired by decorateSelectableRow below, which also
// knows the row itself.
function buildSelectionToggle(item) {
  const label = document.createElement('label');
  label.className = 'item-select-toggle flex h-11 w-11 shrink-0 cursor-pointer items-center justify-center';
  const checkbox = document.createElement('input');
  checkbox.type = 'checkbox';
  checkbox.className = 'h-5 w-5 rounded-md border-slate-300 dark:border-slate-600 accent-sky-500 focus:ring-sky-500/40';
  checkbox.checked = isItemSelected(item);
  checkbox.disabled = !isSelectableItem(item);
  checkbox.setAttribute('aria-label', t('bulk.selectItemAriaLabel', { title: item.title }));
  label.appendChild(checkbox);
  return label;
}

function paintRowSelection(li, checkbox, selected) {
  li.classList.toggle('is-selected', selected);
  checkbox.checked = selected;
}

// Turns an already-built row (buildItemRow's return value, built in its
// selection shape) into a selection target: every other interactive
// descendant — quantity stepper, thumbnail, 🔗 link — goes `inert`, so a tap
// on it falls through to the card and toggles the selection instead of
// changing a quantity or following a link mid-selection. Toggling repaints
// just this row and the bar rather than re-running renderItems, so keyboard
// focus stays on the checkbox.
function decorateSelectableRow(li, item) {
  const checkbox = li.querySelector('.item-select-toggle input');
  for (const el of li.querySelectorAll('button, a, input, select, textarea')) {
    if (el !== checkbox) el.inert = true;
  }
  paintRowSelection(li, checkbox, isItemSelected(item));
  if (!isSelectableItem(item)) {
    li.classList.add('opacity-60');
    return;
  }
  li.classList.add('cursor-pointer');

  // Deselecting the last selected row leaves the mode altogether rather
  // than parking the bar on "Aucune sélection". Entering through the
  // "Sélectionner" button still starts from an empty selection — only a
  // selection that *becomes* empty exits.
  function setSelected(selected) {
    if (selected) selectedItemIds.add(selectionKey(item));
    else selectedItemIds.delete(selectionKey(item));
    if (selectedItemIds.size === 0) {
      exitSelectionMode();
      return;
    }
    paintRowSelection(li, checkbox, selected);
    updateBulkActionsBar();
  }

  checkbox.addEventListener('change', () => setSelected(checkbox.checked));
  li.addEventListener('click', (event) => {
    if (event.target.closest('.item-select-toggle')) return; // the checkbox's own 'change' handles it
    setSelected(!isItemSelected(item));
  });
}

// ---------------------------------------------------------------------------
// Bar controls
// ---------------------------------------------------------------------------

// Selects every row currently on screen, or — once they're all selected —
// "Tout désélectionner" clears the whole selection (including any done rows
// selected before #done-section was collapsed) and leaves the mode.
function toggleSelectAll() {
  const visibleKeys = visibleSelectableKeys();
  const allSelected = selectedItemIds.size > 0 && visibleKeys.every((key) => selectedItemIds.has(key));
  if (allSelected) {
    exitSelectionMode();
    return;
  }
  for (const key of visibleKeys) selectedItemIds.add(key);
  renderItems();
}

selectionEls.toggleButton.addEventListener('click', () => {
  // closeListOptionsSheet is defined in list_view.js — this row lives
  // inside #list-options-sheet, same close-then-act pattern as its siblings.
  closeListOptionsSheet();
  enterSelectionMode();
});
selectionEls.toggleButtonDesktop.addEventListener('click', () => enterSelectionMode());
selectionEls.cancelButton.addEventListener('click', () => exitSelectionMode());
selectionEls.toggleAllButton.addEventListener('click', toggleSelectAll);

// Whether "Tout sélectionner" covers the done rows depends on this
// section's open state, which changes without a re-render.
listEls.doneSection.addEventListener('toggle', () => {
  if (selectionMode) updateBulkActionsBar();
});

// Escape leaves selection mode — but only when no modal/sheet is open, in
// which case that Escape belongs to the overlay's own handler. A capture
// listener runs before those bubble-phase handlers close the overlay, so
// the check below still sees it open.
document.addEventListener(
  'keydown',
  (event) => {
    if (event.key !== 'Escape' || !selectionMode) return;
    if (document.querySelector(OVERLAY_SELECTOR)) return;
    exitSelectionMode();
  },
  true,
);

// ---------------------------------------------------------------------------
// Bulk actions
// ---------------------------------------------------------------------------

// Check/uncheck the whole selection behind a single undo toast, with the
// same deferred-commit semantics as toggleDone (list_view.js). Each item
// gets its own pendingToggles entry tagged with this batch's `batch` symbol,
// so a later single toggle or delete of one of these items within the grace
// period simply takes it over — toggleDone/removeItem replace or drop the
// entry, and this batch then skips that item on commit/undo — instead of
// cancelling the whole batch.
function bulkToggleDone() {
  const list = state.currentList;
  const listId = state.currentListId;
  const items = selectedItems();
  if (items.length === 0) return;
  const newDone = !items.every((item) => item.done);
  exitSelectionMode({ render: false });

  const entries = [];
  for (const item of items) {
    const pending = pendingToggles.get(item);
    const committedDone = pending ? pending.committedDone : item.done;
    if (pending) pending.dismiss();
    item.done = newDone;
    if (newDone === committedDone) {
      pendingToggles.delete(item); // back to the server-confirmed state — nothing to send
    } else {
      entries.push({ item, committedDone });
    }
  }
  renderItems();
  notifyItemsChanged(listId, list.items);
  if (entries.length === 0) return;

  const batch = Symbol('bulk-toggle');
  const ownsItem = (item) => pendingToggles.get(item)?.batch === batch;
  const plural = entries.length === 1 ? 'One' : 'Other';
  TrakkaUndo.schedule({
    message: t(`bulk.${newDone ? 'markedDone' : 'markedUndone'}${plural}`, { count: entries.length }),
    undoLabel: t('undo.cancel'),
    onUndo: () => {
      for (const { item, committedDone } of entries) {
        if (!ownsItem(item)) continue;
        pendingToggles.delete(item);
        item.done = committedDone;
      }
      renderItems();
      notifyItemsChanged(listId, list.items);
    },
    onCommit: async () => {
      const toSend = entries.filter(({ item }) => ownsItem(item));
      for (const { item } of toSend) pendingToggles.delete(item);
      const errors = [];
      await runWithConcurrency(toSend, BULK_REQUEST_CONCURRENCY, async ({ item, committedDone }) => {
        try {
          const updated = await apiRequest(`/items/${item.id}`, { method: 'PATCH', body: JSON.stringify({ done: newDone }) });
          Object.assign(item, updated);
        } catch (err) {
          item.done = committedDone;
          errors.push(err);
        }
      });
      // Same post-commit refresh logic as toggleDone's own onCommit — see
      // its comment for why a refetch is only used once the user has left
      // the list entirely.
      clearItemsOverride(listId);
      if (state.currentListId !== null) {
        renderItems();
      } else {
        refreshVisibleView();
      }
      reportBulkErrors(errors);
      await refreshPendingBadge();
    },
  });
  for (const { item, committedDone } of entries) {
    // dismiss is a no-op: superseding one item must not cancel the batch.
    pendingToggles.set(item, { batch, committedDone, dismiss() {} });
  }
}

// Deletes the whole selection behind a single undo toast — the same 5s
// grace period as removeItem (list_view.js): items are only flagged
// `pendingDelete` (hidden from every view) until the countdown runs out.
// Each one is then spliced out of the list only once its own DELETE
// succeeds, so a failure just clears the flag and the item reappears in
// its original place.
function bulkDelete() {
  const list = state.currentList;
  const items = selectedItems();
  if (items.length === 0) return;
  exitSelectionMode({ render: false });

  for (const item of items) {
    const pendingToggle = pendingToggles.get(item);
    if (pendingToggle) {
      pendingToggle.dismiss();
      pendingToggles.delete(item);
    }
    item.pendingDelete = true;
  }
  renderItems();
  notifyItemsChanged(list.id, list.items.filter((i) => !i.pendingDelete));

  TrakkaUndo.schedule({
    message: t(items.length === 1 ? 'bulk.deletedOne' : 'bulk.deletedOther', { count: items.length }),
    undoLabel: t('undo.cancel'),
    onUndo: () => {
      for (const item of items) delete item.pendingDelete;
      renderItems();
      notifyItemsChanged(list.id, list.items.filter((i) => !i.pendingDelete));
    },
    onCommit: async () => {
      const errors = [];
      await runWithConcurrency(items, BULK_REQUEST_CONCURRENCY, async (item) => {
        try {
          await apiRequest(`/items/${item.id}`, { method: 'DELETE' });
          const index = list.items.indexOf(item);
          if (index !== -1) list.items.splice(index, 1);
        } catch (err) {
          delete item.pendingDelete;
          errors.push(err);
        }
      });
      clearItemsOverride(list.id);
      if (state.currentList === list) {
        renderItems();
      } else {
        refreshVisibleView();
      }
      reportBulkErrors(errors);
      await refreshPendingBadge();
    },
  });
}

// Same per-item coalescing map as pendingLabelUpdates (list_view.js), for
// the target_month field.
const pendingTargetMonthUpdates = new Map();

// `month` is "YYYY-MM", or "" for "Non planifié" — PATCH's explicit clear
// value for target_month (stored as NULL server-side). Only items whose
// month actually changes are sent.
function applyBulkTargetMonth(items, month) {
  const changed = items.filter((item) => (item.target_month || '') !== month);
  if (changed.length === 0) return;
  commitItemPatches(changed, pendingTargetMonthUpdates, () => ({ target_month: month }));
  const count = changed.length;
  const key = month
    ? count === 1 ? 'bulk.targetMonthAppliedOne' : 'bulk.targetMonthAppliedOther'
    : count === 1 ? 'bulk.targetMonthClearedOne' : 'bulk.targetMonthClearedOther';
  TrakkaToast.success(t(key, { count, month: month ? monthLabel(month, 'long') : '' }));
}

// Lists the create form's own target-month options (#item-target-month,
// filled by ensureTargetMonthOptions in list_view.js on every render) as
// one tappable row each — "Non planifié" plus the next 12 months — so the
// wording always matches the forms. A ✓ marks the month every selected item
// already shares, if any.
function openBulkTargetMonthSheet() {
  const items = selectedItems();
  if (items.length === 0) return;
  ensureTargetMonthOptions();
  const months = new Set(items.map((item) => item.target_month || ''));
  const shared = months.size === 1 ? [...months][0] : null;

  selectionEls.targetMonthSubtitle.textContent = t(
    items.length === 1 ? 'bulk.targetMonthSubtitleOne' : 'bulk.targetMonthSubtitleOther',
    { count: items.length },
  );
  selectionEls.targetMonthOptions.replaceChildren();
  for (const option of listEls.itemTargetMonth.options) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className =
      'flex items-center gap-3 rounded-xl px-3 py-3 text-left text-base font-medium text-slate-900 dark:text-slate-100 hover:bg-slate-200 dark:hover:bg-slate-700';
    const label = document.createElement('span');
    label.className = 'min-w-0 flex-1 truncate';
    label.textContent = option.textContent;
    const check = document.createElement('span');
    check.className = 'ml-auto text-lg text-sky-500 dark:text-sky-400';
    check.setAttribute('aria-hidden', 'true');
    check.textContent = '✓';
    check.hidden = option.value !== shared;
    button.append(label, check);
    button.addEventListener('click', () => {
      closeBulkTargetMonthSheet();
      applyBulkTargetMonth(items, option.value);
    });
    selectionEls.targetMonthOptions.appendChild(button);
  }
  selectionEls.targetMonthSheet.hidden = false;
  document.body.classList.add('overflow-hidden');
}

function closeBulkTargetMonthSheet() {
  selectionEls.targetMonthSheet.hidden = true;
  document.body.classList.remove('overflow-hidden');
}

selectionEls.doneButton.addEventListener('click', bulkToggleDone);
selectionEls.deleteButton.addEventListener('click', bulkDelete);
selectionEls.labelsButton.addEventListener('click', () => {
  const items = selectedItems();
  if (items.length > 0) openLabelManageSheet(items);
});
selectionEls.targetMonthButton.addEventListener('click', openBulkTargetMonthSheet);

selectionEls.closeTargetMonthSheetButton.addEventListener('click', closeBulkTargetMonthSheet);
selectionEls.targetMonthSheet.addEventListener('click', (event) => {
  if (event.target === selectionEls.targetMonthSheet) closeBulkTargetMonthSheet();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !selectionEls.targetMonthSheet.hidden) closeBulkTargetMonthSheet();
});
