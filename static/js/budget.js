'use strict';

// The purchase cycle, the budget and list templates — the frontend half of
// the items' purchase_status/actual_price fields, internal/budget and
// internal/handlers/shopping_templates.go (migrations 32 and 33):
//
//   - an item of a purchase list is "Prévu" (planned, added while
//     estimating), "À acheter" (to buy) or "Acheté" (bought = done); once
//     bought it can carry the price actually paid (actual_price, per unit,
//     like price, which stays the estimate);
//   - computeBudget, the JS port of internal/budget, feeds the summary bar
//     (list_view.js's updateFinanceSummary) live, before any request lands;
//   - the "📝 Prévu" tag on a row, the purchase status picker and "Prix
//     payé" row of #item-actions-sheet, #paid-price-sheet (also offered by
//     the check-off toast), "Valider les articles prévus" and "Enregistrer
//     comme modèle" in #list-options-sheet;
//   - the template picker of the "Nouvelle liste" modal, showing the
//     templates of the type picked — purchase ones (with their budget) or
//     task ones (with their recurrence) — app.js submits what it selected.
//
// Shares app.js/list_view.js's globals (state, listEls, apiRequest, t,
// renderItems, mergeServerItem, ...), like every page script.

const budgetEls = {
  purchaseRow: document.getElementById('item-actions-purchase-row'),
  purchaseOptions: document.querySelectorAll('#item-actions-purchase-row [data-purchase-status]'),
  paidPriceButton: document.getElementById('item-actions-paid-price-button'),
  paidPriceValue: document.getElementById('item-actions-paid-price-value'),
  paidPriceSheet: document.getElementById('paid-price-sheet'),
  paidPriceItem: document.getElementById('paid-price-item'),
  paidPriceForm: document.getElementById('paid-price-form'),
  paidPriceInput: document.getElementById('paid-price-input'),
  paidPriceInputLabel: document.getElementById('paid-price-input-label'),
  paidPriceDetails: document.getElementById('paid-price-details'),
  paidPriceEstimateButton: document.getElementById('paid-price-estimate-button'),
  closePaidPriceSheet: document.getElementById('close-paid-price-sheet-button'),
  validatePlannedButton: document.getElementById('validate-planned-button'),
  validatePlannedLabel: document.getElementById('validate-planned-label'),
  saveTemplateButton: document.getElementById('save-template-button'),
  saveTemplateSheet: document.getElementById('save-template-sheet'),
  saveTemplateForm: document.getElementById('save-template-form'),
  saveTemplateName: document.getElementById('save-template-name'),
  saveTemplateHint: document.getElementById('save-template-hint'),
  saveTemplateSubmit: document.getElementById('save-template-submit'),
  closeSaveTemplateSheet: document.getElementById('close-save-template-sheet-button'),
  templateField: document.getElementById('list-template-field'),
  templateScroller: document.getElementById('list-template-scroller'),
  templateOptions: document.getElementById('list-template-options'),
  templateSummary: document.getElementById('list-template-summary'),
  templateDeleteButton: document.getElementById('list-template-delete-button'),
};

// ---------------------------------------------------------------------------
// Purchase status and budget arithmetic
// ---------------------------------------------------------------------------

// internal/budget.RoundCents: to the cent, halves away from zero like Go's
// math.Round (Math.round alone rounds -0.5 up), and never -0.
function roundCents(amount) {
  const rounded = (Math.sign(amount) * Math.round(Math.abs(amount) * 100)) / 100;
  return rounded === 0 ? 0 : rounded;
}

function itemQuantity(item) {
  return item.quantity > 0 ? item.quantity : 1;
}

// models.PurchaseStatusOf: done wins, then the planned flag. An item built
// locally before any server answer has no purchase_status yet: "to buy".
function purchaseStatusOf(item) {
  if (item.done) return 'bought';
  return item.purchase_status === 'planned' ? 'planned' : 'to_buy';
}

function isPlannedItem(item) {
  return purchaseStatusOf(item) === 'planned';
}

// What a bought item's line actually cost, when a price paid was entered —
// null otherwise (it then counts at its estimate, lineTotal).
function paidLineTotal(item) {
  if (!item.done || typeof item.actual_price !== 'number') return null;
  return item.actual_price * itemQuantity(item);
}

// The JS port of internal/budget.Summarize, over the items themselves
// rather than internal/db's SQL sums: the planned budget (every estimate),
// what was spent (bought items at their price paid, else their estimate),
// what is left (estimates of what isn't bought), the projection, and the
// variance — what the purchases so far cost beyond their estimate. An item
// bought with a price paid but no estimate is spending outside the budget.
function computeBudget(items) {
  let remaining = 0;
  let boughtEstimated = 0;
  let spent = 0;
  for (const item of items) {
    const estimate = typeof item.price === 'number' ? item.price * itemQuantity(item) : null;
    if (!item.done) {
      if (estimate !== null) remaining += estimate;
      continue;
    }
    if (estimate !== null) boughtEstimated += estimate;
    const paid = paidLineTotal(item);
    if (paid !== null) spent += paid;
    else if (estimate !== null) spent += estimate;
  }
  const variance = roundCents(spent - boughtEstimated);
  return {
    estimatedTotal: roundCents(remaining + boughtEstimated),
    spentTotal: roundCents(spent),
    remainingTotal: roundCents(remaining),
    projectedTotal: roundCents(spent + remaining),
    variance,
    varianceStatus: variance > 0 ? 'over' : variance < 0 ? 'under' : 'on_budget',
  };
}

function itemHasAnyPrice(item) {
  return typeof item.price === 'number' || paidLineTotal(item) !== null;
}

// Whether a list shows its prices (row cells, summary bar): every purchase
// list does, except a Courses list until one of its items has a price —
// most grocery lists never get one, and stay as light as before.
function listShowsPrices(list, items) {
  if (!fieldVisibilityFor(list.type).price) return false;
  return list.type !== 'groceries' || items.some(itemHasAnyPrice);
}

// The PATCH body of a check-off (toggleDone, the bulk toggle): on a
// purchase list the status goes with done, and unchecking drops the price
// paid — what the server does anyway, sent so the offline mirror (sw.js
// merges the body into its copy) matches.
function donePatchPayload(list, done) {
  const payload = { done };
  if (fieldVisibilityFor(list.type).price) {
    payload.purchase_status = done ? 'bought' : 'to_buy';
    if (!done) payload.actual_price = null;
  }
  return payload;
}

// An item still being created (local-* id) has nothing to PATCH yet.
function isLocalItem(item) {
  return typeof item.id === 'string' && item.id.startsWith('local-');
}

// ---------------------------------------------------------------------------
// On the rows: the "📝 Prévu" tag and the price paid
// ---------------------------------------------------------------------------

// The "📝 Prévu" tag under a planned item's title (buildItemRow, next to the
// aisle tag): a tap confirms the item, "à acheter". Plain text in
// multi-select mode (interactive: false). Nothing for any other status.
function buildPlannedBadge(item, { interactive = true } = {}) {
  if (!isPlannedItem(item)) return null;
  const badge = document.createElement(interactive ? 'button' : 'span');
  if (interactive) badge.type = 'button';
  badge.className = `inline-flex min-h-[22px] items-center gap-1 self-start rounded-full bg-amber-500/15 px-2 text-[11px] font-semibold leading-4 text-amber-800 dark:text-amber-300${
    interactive ? ' hover:bg-amber-500/25' : ''}`;
  badge.setAttribute('aria-label', t(interactive ? 'purchase.plannedBadgeAriaLabel' : 'purchase.plannedBadgeLabel', { title: item.title }));
  const emoji = document.createElement('span');
  emoji.setAttribute('aria-hidden', 'true');
  emoji.textContent = '📝';
  const text = document.createElement('span');
  text.textContent = t('purchase.status.planned');
  badge.append(emoji, text);
  if (interactive) {
    badge.addEventListener('click', (event) => {
      event.stopPropagation();
      setItemPurchaseStatus(item, 'to_buy');
    });
  }
  return badge;
}

// Fills a row's price element with what a bought item cost (`paid`, the
// line total): green when at or under its estimate, red above it, the
// estimate spelled out for screen readers and on hover.
function decoratePaidPrice(el, item, paid) {
  const estimated = typeof item.price === 'number' ? lineTotal(item) : null;
  const over = estimated === null ? paid > 0 : roundCents(paid - estimated) > 0;
  el.classList.remove('text-[color:var(--tk-money-total)]');
  el.classList.add(over ? 'text-[color:var(--tk-budget-over)]' : 'text-[color:var(--tk-budget-under)]');
  const description = estimated === null
    ? t('items.paidPriceNoEstimateAriaLabel', { paid: formatEuro(paid) })
    : t('items.paidPriceAriaLabel', { paid: formatEuro(paid), estimated: formatEuro(estimated) });
  const visible = document.createElement('span');
  visible.setAttribute('aria-hidden', 'true');
  visible.textContent = formatEuro(paid);
  const spoken = document.createElement('span');
  spoken.className = 'sr-only';
  spoken.textContent = description;
  el.title = description;
  el.replaceChildren(visible, spoken);
}

// ---------------------------------------------------------------------------
// Changing an item's status
// ---------------------------------------------------------------------------

// Moves an item to `status`. Buying it or unbuying it is a check-off or an
// uncheck, so it goes through toggleDone (its undo toast, its "Prix payé"
// offer, its side effects); planned ↔ to buy, and bought → planned, are a
// plain PATCH with the usual optimistic update and rollback.
async function setItemPurchaseStatus(item, status, { quiet = false } = {}) {
  const current = purchaseStatusOf(item);
  if (current === status || isLocalItem(item)) return;
  if (status === 'bought' || (current === 'bought' && status === 'to_buy')) {
    toggleDone(item);
    return;
  }
  hideError();
  const previous = {
    done: item.done, next_due_date: item.next_due_date ?? null,
    purchase_status: item.purchase_status ?? null, actual_price: item.actual_price ?? null,
  };
  if (item.done) setItemDoneOptimistic(item, false);
  item.purchase_status = status;
  renderItems();
  try {
    const updated = await apiRequest(`/items/${item.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ purchase_status: status, done: false, actual_price: null }),
    });
    mergeServerItem(item, updated);
    if (!quiet) {
      window.TrakkaToast?.success(status === 'to_buy'
        ? t('purchase.validated', { title: item.title })
        : t('purchase.statusChanged', { title: item.title, status: t(`purchase.status.${status}`) }));
    }
  } catch (err) {
    Object.assign(item, previous);
    if (!isNetworkError(err)) showError(err.message);
  } finally {
    renderItems();
  }
  await refreshPendingBadge();
}

// "Valider les articles prévus": every planned item of the open list
// becomes "à acheter" — optimistically at once, then one PATCH each (a
// PATCH per item is what the offline queue knows how to replay), a few at a
// time like the bulk actions (runWithConcurrency, list_view.js).
async function validatePlannedItems() {
  const list = state.currentList;
  if (!list) return;
  const planned = (list.items || []).filter((item) => isPlannedItem(item) && !isLocalItem(item) && !item.pendingDelete);
  if (planned.length === 0) return;
  hideError();
  for (const item of planned) item.purchase_status = 'to_buy';
  renderItems();
  const errors = [];
  await runWithConcurrency(planned, BULK_REQUEST_CONCURRENCY, async (item) => {
    try {
      const updated = await apiRequest(`/items/${item.id}`, { method: 'PATCH', body: JSON.stringify({ purchase_status: 'to_buy' }) });
      mergeServerItem(item, updated);
    } catch (err) {
      item.purchase_status = 'planned';
      errors.push(err);
    }
  });
  renderItems();
  reportBulkErrors(errors);
  const done = planned.length - errors.length;
  if (done > 0) {
    window.TrakkaToast?.success(t(done === 1 ? 'purchase.validatedPlannedOne' : 'purchase.validatedPlannedOther', { count: done }));
  }
  await refreshPendingBadge();
}

// ---------------------------------------------------------------------------
// #item-actions-sheet: status picker and "Prix payé" row
// ---------------------------------------------------------------------------

// Called by openItemActionsSheet (list_view.js) for the item it opens on.
function updateItemActionsPurchase(item) {
  const list = state.currentList;
  const purchaseList = Boolean(list) && fieldVisibilityFor(list.type).price && !isLocalItem(item);
  budgetEls.purchaseRow.hidden = !purchaseList;
  budgetEls.paidPriceButton.hidden = !(purchaseList && item.done);
  if (!purchaseList) return;
  const status = purchaseStatusOf(item);
  for (const option of budgetEls.purchaseOptions) {
    option.setAttribute('aria-checked', String(option.dataset.purchaseStatus === status));
    option.tabIndex = option.dataset.purchaseStatus === status ? 0 : -1;
  }
  const paid = paidLineTotal(item);
  budgetEls.paidPriceValue.textContent = paid === null ? t('modals.itemActions.paidPriceNone') : formatEuro(paid);
}

for (const option of budgetEls.purchaseOptions) {
  option.addEventListener('click', () => {
    const item = itemActionsSheetItem;
    closeItemActionsSheet();
    if (item) setItemPurchaseStatus(item, option.dataset.purchaseStatus);
  });
}
// Arrow keys move between the three statuses, like the aisle picker's.
budgetEls.purchaseRow.addEventListener('keydown', (event) => {
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return;
  const options = [...budgetEls.purchaseOptions];
  const index = options.indexOf(document.activeElement);
  if (index === -1) return;
  event.preventDefault();
  const step = event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 1;
  options[(index + step + options.length) % options.length].focus();
});

budgetEls.paidPriceButton.addEventListener('click', () => {
  const item = itemActionsSheetItem;
  closeItemActionsSheet();
  if (item) openPaidPriceSheet(item);
});

// ---------------------------------------------------------------------------
// #paid-price-sheet
// ---------------------------------------------------------------------------

let paidPriceItem = null;

function openPaidPriceSheet(item) {
  if (isLocalItem(item)) return;
  paidPriceItem = item;
  budgetEls.paidPriceItem.textContent = item.title;
  const quantity = itemQuantity(item);
  budgetEls.paidPriceInputLabel.textContent = quantity > 1
    ? t('modals.paidPrice.inputLabelQuantity', { count: quantity })
    : t('modals.paidPrice.inputLabel');
  const start = typeof item.actual_price === 'number' ? item.actual_price : item.price;
  budgetEls.paidPriceInput.value = typeof start === 'number' ? String(start) : '';
  updatePaidPriceDetails();
  budgetEls.paidPriceSheet.hidden = false;
  TrakkaScrollLock.lock();
  budgetEls.paidPriceInput.focus();
  budgetEls.paidPriceInput.select();
}

function closePaidPriceSheet() {
  paidPriceItem = null;
  budgetEls.paidPriceSheet.hidden = true;
  TrakkaScrollLock.unlock();
}

function parsePaidPrice() {
  const raw = budgetEls.paidPriceInput.value.trim();
  if (raw === '') return null;
  const value = Number.parseFloat(raw);
  return Number.isFinite(value) && value >= 0 ? value : NaN;
}

// "Total payé : 7,00 € · Prévu : 6,00 € · Dépassement de 1,00 €", live.
function updatePaidPriceDetails() {
  const item = paidPriceItem;
  if (!item) return;
  const value = parsePaidPrice();
  const parts = [];
  const estimated = typeof item.price === 'number' ? lineTotal(item) : null;
  if (typeof value === 'number' && !Number.isNaN(value)) {
    const paid = value * itemQuantity(item);
    parts.push(t('modals.paidPrice.lineTotal', { amount: formatEuro(paid) }));
    if (estimated === null) {
      parts.push(t('modals.paidPrice.noEstimate'));
    } else {
      parts.push(t('modals.paidPrice.estimate', { amount: formatEuro(estimated) }));
      const gap = roundCents(paid - estimated);
      if (gap > 0) parts.push(t('modals.paidPrice.over', { amount: formatEuro(gap) }));
      else if (gap < 0) parts.push(t('modals.paidPrice.under', { amount: formatEuro(-gap) }));
      else parts.push(t('modals.paidPrice.onBudget'));
    }
  } else if (estimated !== null) {
    parts.push(t('modals.paidPrice.estimate', { amount: formatEuro(estimated) }));
  }
  budgetEls.paidPriceDetails.textContent = parts.join(' · ');
}

// Saves what one unit of item actually cost (null: it cost the estimate).
// The PATCH also says "bought", so it is right whatever the state of a
// check-off sent just before (the toast commits it first) — and an
// optimistic check-off still waiting in its undo window is superseded.
async function savePaidPrice(item, actualPrice) {
  closePaidPriceSheet();
  hideError();
  const pending = pendingToggles.get(item);
  if (pending) {
    pending.dismiss();
    pendingToggles.delete(item);
  }
  const previous = {
    done: item.done, next_due_date: item.next_due_date ?? null,
    purchase_status: item.purchase_status ?? null, actual_price: item.actual_price ?? null,
  };
  if (!item.done) setItemDoneOptimistic(item, true);
  item.purchase_status = 'bought';
  item.actual_price = actualPrice;
  renderItems();
  try {
    const updated = await apiRequest(`/items/${item.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ done: true, purchase_status: 'bought', actual_price: actualPrice }),
    });
    mergeServerItem(item, updated);
    window.TrakkaToast?.success(t(actualPrice === null ? 'purchase.paidPriceCleared' : 'purchase.paidPriceSaved', { title: item.title }));
  } catch (err) {
    Object.assign(item, previous);
    if (!isNetworkError(err)) showError(err.message);
  } finally {
    if (state.currentListId !== null) renderItems();
    else refreshVisibleView();
  }
  await refreshPendingBadge();
}

budgetEls.paidPriceInput.addEventListener('input', updatePaidPriceDetails);
budgetEls.paidPriceForm.addEventListener('submit', (event) => {
  event.preventDefault();
  const item = paidPriceItem;
  if (!item) return;
  const value = parsePaidPrice();
  if (Number.isNaN(value)) {
    showError(t('modals.editItem.actualPriceValidationError'));
    return;
  }
  savePaidPrice(item, value);
});
budgetEls.paidPriceEstimateButton.addEventListener('click', () => {
  const item = paidPriceItem;
  if (item) savePaidPrice(item, null);
});
budgetEls.closePaidPriceSheet.addEventListener('click', closePaidPriceSheet);
budgetEls.paidPriceSheet.addEventListener('click', (event) => {
  if (event.target === budgetEls.paidPriceSheet) closePaidPriceSheet();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !budgetEls.paidPriceSheet.hidden) closePaidPriceSheet();
});

// ---------------------------------------------------------------------------
// #list-options-sheet: "Valider les articles prévus", "Enregistrer comme
// modèle" (#save-template-sheet)
// ---------------------------------------------------------------------------

// Templates are the House's (the server refuses a share recipient).
function canSaveListAsTemplate(list) {
  return (state.houses || []).some((house) => house.id === list.house_id) &&
    (list.items || []).some((item) => !item.pendingDelete);
}

// Called by openListOptionsSheet (list_view.js).
function updateListOptionsPurchaseButtons(list) {
  const planned = fieldVisibilityFor(list.type).price
    ? (list.items || []).filter((item) => isPlannedItem(item) && !isLocalItem(item) && !item.pendingDelete).length
    : 0;
  budgetEls.validatePlannedButton.hidden = planned === 0;
  budgetEls.validatePlannedLabel.textContent = t(planned === 1 ? 'purchase.validatePlannedOne' : 'purchase.validatePlannedOther', { count: planned });
  budgetEls.saveTemplateButton.hidden = !canSaveListAsTemplate(list);
}

budgetEls.validatePlannedButton.addEventListener('click', () => {
  closeListOptionsSheet();
  validatePlannedItems();
});

function openSaveTemplateSheet() {
  const list = state.currentList;
  if (!list) return;
  const count = (list.items || []).filter((item) => !item.pendingDelete).length;
  budgetEls.saveTemplateName.value = list.name;
  // A purchase list's template keeps quantities, aisles and prices; a task
  // or free list's, its items and their recurrence.
  const hint = isPurchaseList(list.type) ? 'templates.saveHint' : 'templates.saveHintTasks';
  budgetEls.saveTemplateHint.textContent = t(`${hint}${count === 1 ? 'One' : 'Other'}`, { count });
  budgetEls.saveTemplateSubmit.disabled = false;
  budgetEls.saveTemplateSheet.hidden = false;
  TrakkaScrollLock.lock();
  budgetEls.saveTemplateName.focus();
  budgetEls.saveTemplateName.select();
}

function closeSaveTemplateSheet() {
  budgetEls.saveTemplateSheet.hidden = true;
  TrakkaScrollLock.unlock();
}

budgetEls.saveTemplateButton.addEventListener('click', () => {
  closeListOptionsSheet();
  openSaveTemplateSheet();
});
budgetEls.saveTemplateForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  const list = state.currentList;
  const name = budgetEls.saveTemplateName.value.trim();
  if (!list || !name) return;
  hideError();
  budgetEls.saveTemplateSubmit.disabled = true;
  try {
    const template = await apiRequest('/shopping-templates', {
      method: 'POST',
      body: JSON.stringify({ list_id: list.id, name }),
    });
    closeSaveTemplateSheet();
    window.TrakkaToast?.success(t('templates.saved', { name: template.name }));
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  } finally {
    budgetEls.saveTemplateSubmit.disabled = false;
  }
});
budgetEls.closeSaveTemplateSheet.addEventListener('click', closeSaveTemplateSheet);
budgetEls.saveTemplateSheet.addEventListener('click', (event) => {
  if (event.target === budgetEls.saveTemplateSheet) closeSaveTemplateSheet();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !budgetEls.saveTemplateSheet.hidden) closeSaveTemplateSheet();
});

// ---------------------------------------------------------------------------
// "Nouvelle liste": the template picker
// ---------------------------------------------------------------------------

// The empty choice ("Liste vide") and each template get a picker id:
// "" for none, "builtin:<key>" for a built-in one, "house:<id>" for one of
// the House's own.
const templatePicker = {
  // Every template of the House, whatever its type; only those of `family`
  // show (visibleListTemplates).
  templates: [],
  selected: '',
  // What the last template picked wrote into the name/icon fields, so
  // picking another one (or none) replaces it — but never a name or icon
  // the user typed themselves.
  filledName: '',
  filledIcon: '',
  request: 0,
  loading: false,
  // The last load failed (offline): said instead of "Liste vide"'s summary.
  unavailable: false,
  // The modal is creating a list (the picker only shows then), and the
  // template family of the type picked in it.
  createMode: false,
  family: 'purchase',
};

// Mirrors models.ListTemplateFamily: the purchase types (Courses, Achats
// ponctuels, Abonnements — isPurchaseList, app.js) share the purchase
// templates; todo and custom lists only take their own. The server refuses
// a template of another family (400).
function listTemplateFamily(type) {
  return isPurchaseList(type) ? 'purchase' : type;
}

function visibleListTemplates() {
  return templatePicker.templates.filter((template) => listTemplateFamily(template.list_type) === templatePicker.family);
}

function templatePickerId(template) {
  return template.builtin ? `builtin:${template.key}` : `house:${template.id}`;
}

function selectedTemplate() {
  return templatePicker.templates.find((template) => templatePickerId(template) === templatePicker.selected) || null;
}

function checkedListType() {
  return els.createListForm.querySelector('input[name="list-type"]:checked')?.value || 'shopping';
}

// What app.js's submit adds to POST /api/v1/lists: the template to start
// from, and the language a built-in one is written in — or null.
function selectedListTemplatePayload() {
  const template = selectedTemplate();
  if (!template) return null;
  return template.builtin
    ? { template_key: template.key, lang: TrakkaI18n.getLang() }
    : { template_id: template.id };
}

// "14 articles" for a purchase template, "12 tâches" for a task one,
// "8 éléments" for a free list's.
const TEMPLATE_COUNT_KEYS = { purchase: 'templates.items', todo: 'templates.tasks' };

function templateItemsLabel(template) {
  const count = template.item_count;
  const base = TEMPLATE_COUNT_KEYS[listTemplateFamily(template.list_type)] || 'templates.entries';
  return t(`${base}${count === 1 ? 'One' : 'Other'}`, { count });
}

// What the summary line says about a template once picked: a purchase
// template's estimated budget (its items are added "Prévu"); for a task
// template no price — how many of its tasks repeat instead.
function templateSummary(template) {
  const items = templateItemsLabel(template);
  const family = listTemplateFamily(template.list_type);
  if (family === 'purchase') {
    return template.estimated_total > 0
      ? t('templates.summary', { items, total: formatEuro(template.estimated_total) })
      : t('templates.summaryNoTotal', { items });
  }
  if (family !== 'todo') return items;
  const recurring = (template.items || []).filter((item) => item.recurrence_rule).length;
  if (recurring === 0) return t('templates.summaryChecklist', { items });
  let recurringLabel;
  if (recurring === template.item_count) recurringLabel = t('templates.recurringAll');
  else recurringLabel = t(recurring === 1 ? 'templates.recurringOne' : 'templates.recurringOther', { count: recurring });
  return t('templates.summaryTasks', { items, recurring: recurringLabel });
}

function buildTemplateCard({ id, icon, name, meta, house }) {
  const button = document.createElement('button');
  button.type = 'button';
  button.setAttribute('role', 'radio');
  const checked = id === templatePicker.selected;
  button.setAttribute('aria-checked', String(checked));
  button.tabIndex = checked ? 0 : -1;
  button.dataset.templatePickerId = id;
  // The focus ring is drawn inside the card: the scroller would clip one
  // drawn outside the first or last card.
  button.className = 'flex w-28 shrink-0 snap-start flex-col items-start gap-1 rounded-xl border p-2.5 text-left ' +
    'border-slate-200 bg-white hover:bg-slate-200 dark:border-slate-700 dark:bg-slate-900 dark:hover:bg-slate-700 ' +
    'aria-checked:border-sky-500 aria-checked:bg-sky-500/10 dark:aria-checked:border-sky-500 dark:aria-checked:bg-sky-500/10 ' +
    'focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-sky-500';
  const top = document.createElement('span');
  top.className = 'flex w-full items-center justify-between gap-1';
  const iconEl = document.createElement('span');
  iconEl.className = 'text-2xl leading-none';
  iconEl.setAttribute('aria-hidden', 'true');
  iconEl.textContent = icon;
  top.appendChild(iconEl);
  if (house) {
    const badge = document.createElement('span');
    badge.className = 'rounded-full bg-slate-200 px-1.5 text-[10px] font-semibold text-slate-600 dark:bg-slate-700 dark:text-slate-300';
    badge.textContent = t('templates.houseBadge');
    top.appendChild(badge);
  }
  const nameEl = document.createElement('span');
  nameEl.className = 'line-clamp-2 min-h-[2rem] text-xs font-semibold leading-4 text-slate-900 dark:text-slate-100';
  nameEl.textContent = name;
  const metaEl = document.createElement('span');
  metaEl.className = 'text-[11px] leading-4 text-slate-600 dark:text-slate-400';
  metaEl.textContent = meta;
  button.append(top, nameEl, metaEl);
  button.addEventListener('click', () => selectListTemplate(id));
  return button;
}

// Only a purchase template's card shows its estimated total: a task list
// has no prices.
function renderListTemplates() {
  const visible = visibleListTemplates();
  const cards = [buildTemplateCard({ id: '', icon: '📄', name: t('templates.none'), meta: '' })];
  for (const template of visible) {
    const items = templateItemsLabel(template);
    cards.push(buildTemplateCard({
      id: templatePickerId(template),
      icon: template.icon || listIcon({ type: template.list_type }),
      name: template.name,
      meta: listTemplateFamily(template.list_type) === 'purchase' && template.estimated_total > 0
        ? `${items} · ${formatEuro(template.estimated_total)}`
        : items,
      house: !template.builtin,
    }));
  }
  budgetEls.templateOptions.replaceChildren(...cards);
  // A type with no template at all (a free list, usually) has nothing to
  // pick from: no field, rather than a lone "Liste vide".
  budgetEls.templateField.hidden = !templatePicker.createMode ||
    (!templatePicker.loading && !templatePicker.unavailable && visible.length === 0);
  updateListTemplateSummary(templatePicker.loading ? t('templates.loading') : undefined);
}

function updateListTemplateSummary(message) {
  const template = selectedTemplate();
  budgetEls.templateDeleteButton.hidden = !template || template.builtin;
  if (message) {
    budgetEls.templateSummary.textContent = message;
  } else if (!template) {
    budgetEls.templateSummary.textContent = t(templatePicker.unavailable ? 'templates.unavailable' : 'templates.noneSummary');
  } else {
    budgetEls.templateSummary.textContent = templateSummary(template);
  }
}

// Picking a template fills in its name, icon and type — replacing what a
// previous pick filled in, never what the user typed. Its type is always of
// the family shown, so the cards stay as they are.
function selectListTemplate(id) {
  templatePicker.selected = id;
  const template = selectedTemplate();
  const nameUntouched = els.listNameInput.value.trim() === '' || els.listNameInput.value === templatePicker.filledName;
  const iconUntouched = els.listIconInput.value.trim() === '' || els.listIconInput.value === templatePicker.filledIcon;
  const name = template ? template.name : '';
  const icon = template ? template.icon || '' : '';
  if (nameUntouched) els.listNameInput.value = name;
  if (iconUntouched) {
    els.listIconInput.value = icon;
    syncListIconClearButton();
  }
  templatePicker.filledName = name;
  templatePicker.filledIcon = icon;
  if (template) setListTypeSelection(template.list_type);
  for (const card of budgetEls.templateOptions.querySelectorAll('[data-template-picker-id]')) {
    const checked = card.dataset.templatePickerId === id;
    card.setAttribute('aria-checked', String(checked));
    card.tabIndex = checked ? 0 : -1;
  }
  updateListTemplateSummary();
}

// Called by setListTypeSelection (app.js) whenever the type changes —
// picked by hand, or by a template — so the cards follow it: the other
// family's cards go, and so does a template of it picked earlier (with the
// name and icon it filled in), since the server would refuse it.
function syncListTemplatesToType(type) {
  const family = listTemplateFamily(type);
  if (family === templatePicker.family) return;
  templatePicker.family = family;
  if (!templatePicker.createMode) return;
  const template = selectedTemplate();
  if (template && listTemplateFamily(template.list_type) !== family) selectListTemplate('');
  renderListTemplates();
  budgetEls.templateScroller.scrollLeft = 0;
}

// Called by openListModal (app.js) on every open: shown in create mode only,
// reset to "Liste vide", then every template for the current House is
// (re)loaded — the House's own may have changed since the last open — and
// those of the type picked show.
async function resetListTemplatePicker(createMode) {
  templatePicker.createMode = createMode;
  templatePicker.family = listTemplateFamily(checkedListType());
  templatePicker.selected = '';
  templatePicker.filledName = '';
  templatePicker.filledIcon = '';
  templatePicker.unavailable = false;
  templatePicker.loading = createMode;
  budgetEls.templateScroller.scrollLeft = 0;
  renderListTemplates();
  if (!createMode) return;
  const request = ++templatePicker.request;
  const params = new URLSearchParams({ lang: TrakkaI18n.getLang() });
  if (state.currentHouseId !== null) params.set('house_id', String(state.currentHouseId));
  try {
    const templates = await apiRequest(`/shopping-templates?${params}`);
    if (request !== templatePicker.request) return;
    templatePicker.templates = Array.isArray(templates) ? templates : [];
  } catch {
    if (request !== templatePicker.request) return;
    templatePicker.templates = [];
    templatePicker.unavailable = true;
  }
  templatePicker.loading = false;
  renderListTemplates();
}

budgetEls.templateOptions.addEventListener('keydown', (event) => {
  if (!['ArrowLeft', 'ArrowRight'].includes(event.key)) return;
  const cards = [...budgetEls.templateOptions.querySelectorAll('[data-template-picker-id]')];
  const index = cards.indexOf(document.activeElement);
  if (index === -1) return;
  event.preventDefault();
  const next = cards[(index + (event.key === 'ArrowLeft' ? -1 : 1) + cards.length) % cards.length];
  selectListTemplate(next.dataset.templatePickerId);
  next.focus();
  next.scrollIntoView({ block: 'nearest', inline: 'nearest' });
});

budgetEls.templateDeleteButton.addEventListener('click', async () => {
  const template = selectedTemplate();
  if (!template || template.builtin) return;
  if (!window.confirm(t('templates.deleteConfirm', { name: template.name }))) return;
  hideError();
  try {
    await apiRequest(`/shopping-templates/${template.id}`, { method: 'DELETE' });
    templatePicker.templates = templatePicker.templates.filter((candidate) => candidate !== template);
    selectListTemplate('');
    renderListTemplates();
    window.TrakkaToast?.success(t('templates.deleted', { name: template.name }));
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  }
});
