'use strict';

// "Budget & Prévisions Achats" planning view: a second tab alongside the
// dashboard (shares `state`, `els`, `apiRequest`, `showError`/`hideError`,
// `t`, `formatEuro`, `refreshPendingBadge` with app.js/list_view.js, and
// `recurrenceBadgeLabel`/`buildRecurrenceBadge` with list_view.js — same
// classic-<script>-tags shared-scope pattern as those files). It groups two
// kinds of spending by month over a selectable horizon: one-off purchases
// (items with a `target_month` set via the edit-item modal in list_view.js,
// grouped into whichever single month they're scheduled for) and recurring
// items (items with a `recurrence_rule`, projected onto every month of the
// horizon their cadence actually falls in — see projectRecurringOccurrences
// below). A one-off item can be moved to a different month right from here;
// a recurring item's schedule is a function of its cadence, not a single
// month, so it has no such control.

const planningEls = {
  tabDashboard: document.getElementById('tab-dashboard'),
  tabPlanning: document.getElementById('tab-planning'),
  tabUrgent: document.getElementById('tab-urgent'),
  tabSpaces: document.getElementById('tab-spaces'),
  tabShared: document.getElementById('tab-shared'),
  dashboardView: document.getElementById('dashboard-view'),
  planningView: document.getElementById('planning-view'),
  urgentView: document.getElementById('urgent-view'),
  spacesView: document.getElementById('spaces-view'),
  sharedView: document.getElementById('shared-view'),
  horizonButtons: document.querySelectorAll('#planning-view [data-horizon]'),
  total: document.getElementById('planning-total'),
  months: document.getElementById('planning-months'),
};

// The five dashboard tabs (Lists / Budget & Forecast / Urgent / Spaces /
// Shared with me) are mutually exclusive, so the switcher lives here rather
// than being split across each tab's own file — setActiveTab is the single
// place that shows one view and hides the other four. urgent.js/spaces.js/
// shares.js call back into loadUrgentView/loadSpacesView/loadSharedView the
// same deferred-call way buildItemRow calls monthLabel (defined below) —
// all script tags finish loading and defining their top-level functions
// before any click can actually fire.
let activeTab = 'dashboard'; // 'dashboard' | 'planning' | 'urgent' | 'spaces' | 'shared'
let planningHorizon = 3;
// Every one-off scheduled item currently known for state.currentHouseId, as
// { item, listName } — re-grouped by month locally on every horizon change
// rather than re-fetched, since the horizon only narrows/widens which of
// the already-loaded scheduled items are shown.
let planningEntries = [];
// Every recurring item currently known for state.currentHouseId, as
// { item, listName } — re-projected onto the horizon's months (see
// projectRecurringOccurrences) on every render rather than re-fetched, for
// the same reason as planningEntries above.
let planningRecurringEntries = [];

function isDashboardTabActive() {
  return activeTab === 'dashboard' && state.currentListId === null;
}

function isPlanningTabActive() {
  return activeTab === 'planning';
}

function isUrgentTabActive() {
  return activeTab === 'urgent';
}

function isSpacesTabActive() {
  return activeTab === 'spaces';
}

function isSharedTabActive() {
  return activeTab === 'shared';
}

// monthsFromNow(3) with today in August 2026 returns
// ["2026-08", "2026-09", "2026-10"] — the current month counts as the first
// of "the next N months" (an item scheduled for this month is still
// upcoming spending), not skipped in favor of the following one.
function monthsFromNow(count) {
  const now = new Date();
  const months = [];
  for (let i = 0; i < count; i++) {
    const d = new Date(now.getFullYear(), now.getMonth() + i, 1);
    months.push(`${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`);
  }
  return months;
}

// monthLabel formats a "YYYY-MM" string using the currently active UI
// language (see i18n.js) — 'long' for section headings ("Novembre 2026"),
// 'short' for the compact badge list_view.js shows on a scheduled item's
// row ("nov. 2026"). Intl capitalizes "November 2026" on its own in en-US
// but not "novembre 2026" in fr-FR, hence the manual capitalization below.
function monthLabel(monthStr, style) {
  const [year, month] = monthStr.split('-').map(Number);
  // intlLocale is defined in list_view.js.
  const formatted = new Intl.DateTimeFormat(intlLocale(), { month: style === 'short' ? 'short' : 'long', year: 'numeric' }).format(
    new Date(year, month - 1, 1)
  );
  return formatted.charAt(0).toUpperCase() + formatted.slice(1);
}

function setTabButtonState(button, active) {
  button.setAttribute('aria-selected', String(active));
  button.classList.toggle('border-sky-500', active);
  button.classList.toggle('bg-sky-500/10', active);
  button.classList.toggle('text-sky-600', active);
  button.classList.toggle('dark:text-sky-300', active);
  button.classList.toggle('border-slate-200', !active);
  button.classList.toggle('dark:border-slate-700', !active);
  button.classList.toggle('bg-white', !active);
  button.classList.toggle('dark:bg-slate-900', !active);
  button.classList.toggle('text-slate-600', !active);
  button.classList.toggle('dark:text-slate-300', !active);
}

function setActiveTab(tab) {
  activeTab = tab;
  // resetScrollPosition is defined in list_view.js (loaded before this
  // file) — see its own doc comment there for the confirmed bug this
  // avoids: without it, switching tabs while scrolled down left every
  // newly-shown tab's own header controls unreachable at their expected
  // on-screen position until the user manually scrolled back up first.
  resetScrollPosition();
  planningEls.dashboardView.hidden = tab !== 'dashboard';
  planningEls.planningView.hidden = tab !== 'planning';
  planningEls.urgentView.hidden = tab !== 'urgent';
  planningEls.spacesView.hidden = tab !== 'spaces';
  planningEls.sharedView.hidden = tab !== 'shared';
  setTabButtonState(planningEls.tabDashboard, tab === 'dashboard');
  setTabButtonState(planningEls.tabPlanning, tab === 'planning');
  setTabButtonState(planningEls.tabUrgent, tab === 'urgent');
  setTabButtonState(planningEls.tabSpaces, tab === 'spaces');
  setTabButtonState(planningEls.tabShared, tab === 'shared');
  // loadDashboard/loadUrgentView/loadSpacesView/loadSharedView are defined
  // in app.js/urgent.js/spaces.js/shares.js, all loaded after this file —
  // safe to call here since this only ever runs from a click, well after
  // every script has finished loading and defined its top-level functions.
  // The dashboard tab used to be the only one of the five that DIDN'T
  // refetch on switch (showDashboard(), used by the logo link, already
  // called loadDashboard() itself, but clicking the tab button directly
  // skipped it) — so switching back to it after pinning a list/space from
  // another tab could show stale data until a full reload. Refetching here
  // too, the same way every other tab already does, is what makes pinning
  // (and any other change made elsewhere) show up immediately.
  if (tab === 'dashboard') loadDashboard();
  if (tab === 'planning') loadPlanningView();
  if (tab === 'urgent') loadUrgentView();
  if (tab === 'spaces') loadSpacesView();
  if (tab === 'shared') loadSharedView();
  // saveLastView is defined in app.js — see the "keep last page on launch"
  // preference there. Called for every tab, 'dashboard' included, so
  // clicking back to Lists sticks too, not just the other four tabs.
  saveLastView({ type: 'tab', tab });
}

planningEls.tabDashboard.addEventListener('click', () => setActiveTab('dashboard'));
planningEls.tabPlanning.addEventListener('click', () => setActiveTab('planning'));
planningEls.tabUrgent.addEventListener('click', () => setActiveTab('urgent'));
planningEls.tabSpaces.addEventListener('click', () => setActiveTab('spaces'));
planningEls.tabShared.addEventListener('click', () => setActiveTab('shared'));

function setHorizonButtonState(button, active) {
  button.classList.toggle('border-sky-500', active);
  button.classList.toggle('bg-sky-500/10', active);
  button.classList.toggle('text-sky-600', active);
  button.classList.toggle('dark:text-sky-300', active);
  button.classList.toggle('border-slate-200', !active);
  button.classList.toggle('dark:border-slate-700', !active);
  button.classList.toggle('bg-white', !active);
  button.classList.toggle('dark:bg-slate-900', !active);
  button.classList.toggle('text-slate-600', !active);
  button.classList.toggle('dark:text-slate-300', !active);
}

for (const button of planningEls.horizonButtons) {
  button.addEventListener('click', () => {
    planningHorizon = Number(button.dataset.horizon);
    for (const other of planningEls.horizonButtons) setHorizonButtonState(other, other === button);
    renderPlanning();
  });
}

// Splits a house's purchase-oriented lists (each with its items already
// attached) into the two buckets this view budgets separately: recurring
// items (recurrence_rule set — these live mainly in 'recurring_shopping'
// lists, but a recurring item can exist in any list type) always go through
// the recurrence projection regardless of target_month, since a recurring
// cost isn't pinned to a single month; a non-recurring item only counts if
// it has a target_month. Items with neither simply don't appear in this
// view. Shared between the live-network path and the IndexedDB cache
// fallback below so the split can't drift between them.
function applyPlanningEntries(detailed) {
  const entries = [];
  const recurringEntries = [];
  for (const list of detailed) {
    for (const item of list.items || []) {
      if (item.recurrence_rule) {
        recurringEntries.push({ item, listName: list.name });
      } else if (item.target_month) {
        entries.push({ item, listName: list.name });
      }
    }
  }
  planningEntries = entries;
  planningRecurringEntries = recurringEntries;
}

// Fetches every purchase-oriented list in the current house — 'shopping',
// 'groceries' and 'recurring_shopping' alike (anything that isn't 'todo' or
// 'custom'; see isPurchaseList in app.js, which this mirrors) — plus each
// one's items (the same "list_id -> detail" fan-out loadDashboard already
// does for its badges), then hands them to applyPlanningEntries above.
// 'custom' (freeform notes) lists are excluded from the fetch entirely
// rather than just relying on their items never having a
// target_month/recurrence_rule set through the UI — this budget view must
// strictly ignore them even if one somehow carries either field (e.g. set
// directly through the API). Re-run whenever the planning tab is opened,
// the house changes, or the offline queue flushes; horizon changes alone
// just re-filter/re-project the already-loaded result.
async function loadPlanningView() {
  if (state.currentHouseId === null) {
    planningEntries = [];
    planningRecurringEntries = [];
    renderPlanning();
    return;
  }

  // Stale-while-revalidate: paint immediately from the IndexedDB mirror
  // before the network fetch below even starts — same reasoning as
  // urgent.js's loadUrgentView, which this mirrors closely. Re-rendered for
  // real below once the network settles either way.
  const cachedFirst = await cachedDashboardLists(state.currentHouseId);
  const cachedPurchaseLists = cachedFirst.filter((list) => isPurchaseList(list.type));
  if (cachedPurchaseLists.length > 0) {
    applyPlanningEntries(cachedPurchaseLists);
    renderPlanning();
  }

  try {
    const lists = await apiRequest(`/lists?house_id=${state.currentHouseId}`);
    const purchaseLists = lists.filter((list) => isPurchaseList(list.type));
    const detailed = await Promise.all(
      purchaseLists.map((list) => apiRequest(`/lists/${list.id}`).catch(() => ({ ...list, items: [] })))
    );
    applyPlanningEntries(detailed);
  } catch (err) {
    // Offline, or a transient server error: fall back to the IndexedDB
    // mirror (cachedDashboardLists, defined in app.js) instead of emptying
    // the view — see the Offline-First requirement in CLAUDE.md/docs/PWA.md.
    // A plain connectivity failure stays silent (the header's network badge
    // already says "Hors-ligne"); only a genuine server-side error still
    // raises the banner.
    const cached = await cachedDashboardLists(state.currentHouseId);
    applyPlanningEntries(cached.filter((list) => isPurchaseList(list.type)));
    if (!isNetworkError(err)) showError(err.message);
  }
  renderPlanning();
}

// Called after anything that might have changed the current house's
// scheduled items (offline sync completing, a language switch, a house
// switch) — a no-op unless this tab is the one currently visible.
function refreshPlanningIfActive() {
  if (isPlanningTabActive()) loadPlanningView();
}

// ---------------------------------------------------------------------------
// Recurring-item budget projection. The occurrence arithmetic comes from
// static/js/recurrence.js (TrakkaRecurrence), the single JS port of
// internal/recurrence shared with list_view.js and the service worker.
// ---------------------------------------------------------------------------

function todayISO() {
  return new Date().toISOString().slice(0, 10);
}

// Walks a recurring item's occurrence dates forward, starting from its
// current due_date (or today, if it hasn't been completed yet — same
// fallback the Go/JS advance logic uses when there's no prior due date),
// and tallies how many occurrences land in each month of `range`. A MONTHLY
// item lands exactly once per month (so a 10€ monthly item adds 10€ to
// every projected month); a WEEKLY item can land 4-5 times in a given
// month (a FREQ=WEEKLY;BYDAY=MO,WE,FR one, 12-14 times); a
// FREQ=MONTHLY;INTERVAL=3 ("quarterly") item lands in one month out of
// three.
// Occurrences past the item's recurrence_end_date, or past the end of
// `range`, are not counted. Returns a Map<"YYYY-MM", occurrenceCount>.
function projectRecurringOccurrences(item, range) {
  const counts = new Map();
  if (!item.recurrence_rule) return counts;

  const rangeMonths = new Set(range);
  const [lastYear, lastMonthNum] = range[range.length - 1].split('-').map(Number);
  const rangeEnd = new Date(Date.UTC(lastYear, lastMonthNum, 0)).toISOString().slice(0, 10);

  // A checked-off recurring item waits in "Terminés" for its next
  // occurrence (next_due_date): the occurrence it completed is paid and
  // doesn't count, and a finished series (no next_due_date) projects
  // nothing. An active one starts from its own due date.
  let current = item.done ? item.next_due_date : item.due_date || todayISO();
  if (!current) return counts;
  // Parsed once for the whole walk. An unrecognized rule parses to null,
  // which occurrenceAfter answers with null: only `current` then counts.
  const rule = TrakkaRecurrence.parseRule(item.recurrence_rule);
  let iterations = 0;
  const maxIterations = 3660; // generous cap: ~10 years of DAILY occurrences

  while (current <= rangeEnd && iterations < maxIterations) {
    iterations++;
    if (item.recurrence_end_date && current > item.recurrence_end_date) break;

    const month = current.slice(0, 7);
    if (rangeMonths.has(month)) counts.set(month, (counts.get(month) || 0) + 1);

    const next = TrakkaRecurrence.occurrenceAfter(current, rule);
    if (!next || next <= current) break; // unrecognized rule, or no progress — avoid looping forever
    current = next;
  }

  return counts;
}

function renderPlanning() {
  const range = monthsFromNow(planningHorizon);
  const rangeSet = new Set(range);
  const grouped = new Map(range.map((month) => [month, []]));
  let total = 0;

  for (const entry of planningEntries) {
    const month = entry.item.target_month;
    if (!rangeSet.has(month)) continue;
    // Mirrors updateFinanceSummary/lineTotal in list_view.js: item.price is
    // a per-unit price, so the line amount is price * quantity — keeping
    // this in sync with lineTotal is what makes the two totals agree.
    const amount = typeof entry.item.price === 'number' ? entry.item.price * (entry.item.quantity > 0 ? entry.item.quantity : 1) : null;
    grouped.get(month).push({ item: entry.item, listName: entry.listName, recurring: false, occurrenceCount: 1, amount });
    if (amount !== null) total += amount;
  }

  for (const entry of planningRecurringEntries) {
    const occurrences = projectRecurringOccurrences(entry.item, range);
    const quantity = entry.item.quantity > 0 ? entry.item.quantity : 1;
    for (const [month, count] of occurrences) {
      const amount = typeof entry.item.price === 'number' ? entry.item.price * quantity * count : null;
      grouped.get(month).push({ item: entry.item, listName: entry.listName, recurring: true, occurrenceCount: count, amount });
      if (amount !== null) total += amount;
    }
  }

  planningEls.total.textContent = formatEuro(total);
  planningEls.months.replaceChildren();
  for (const month of range) {
    planningEls.months.appendChild(buildMonthCard(month, grouped.get(month)));
  }
}

function buildMonthCard(month, entries) {
  const card = document.createElement('div');
  card.className = 'rounded-2xl border border-slate-200 dark:border-slate-800 bg-slate-50 dark:bg-slate-800/30 p-4';

  const header = document.createElement('div');
  header.className = 'mb-3 flex items-center justify-between gap-3';
  const heading = document.createElement('h3');
  heading.className = 'text-base font-semibold text-slate-900 dark:text-slate-100';
  heading.textContent = monthLabel(month, 'long');
  header.appendChild(heading);

  if (entries.length > 0) {
    const subtotal = document.createElement('span');
    // A projected TOTAL for the month, not an amount spent — takes the
    // same --tk-money-total token as every other total figure (finance
    // summary, item rows, list-card badges) rather than 'emerald', which
    // this used to (incorrectly) reuse for both "total" and "spent".
    subtotal.className = 'text-sm font-semibold text-[color:var(--tk-money-total)]';
    const monthTotal = entries.reduce((sum, entry) => sum + (entry.amount || 0), 0);
    subtotal.textContent = formatEuro(monthTotal);
    header.appendChild(subtotal);
  }

  card.appendChild(header);

  if (entries.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'text-sm text-slate-500';
    empty.textContent = t('planning.emptyMonth');
    card.appendChild(empty);
    return card;
  }

  const list = document.createElement('ul');
  list.className = 'space-y-2';
  for (const entry of entries) list.appendChild(buildPlanningItemRow(entry, month));
  card.appendChild(list);
  return card;
}

function buildPlanningItemRow(entry, currentMonth) {
  const { item, listName, recurring, occurrenceCount, amount } = entry;
  const li = document.createElement('li');
  li.className = 'flex flex-wrap items-center gap-2 rounded-xl border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 px-3 py-2';

  const info = document.createElement('div');
  info.className = 'min-w-0 flex-1';
  const titleRow = document.createElement('div');
  titleRow.className = 'flex flex-wrap items-center gap-2';
  const title = document.createElement('p');
  title.className = 'truncate text-sm font-medium text-slate-900 dark:text-slate-100';
  title.textContent = item.quantity > 1 ? `${item.title} × ${item.quantity}` : item.title;
  titleRow.appendChild(title);
  // Recurring items get the same 🔄 + frequency badge list_view.js shows on
  // their row (recurrenceBadgeLabel/buildRecurrenceBadge, defined there and
  // loaded before this file) — this is the visual distinction the planning
  // view needs between a one-off scheduled purchase and an automatically
  // projected recurring charge.
  if (recurring) {
    const badge = buildRecurrenceBadge(item);
    if (badge) titleRow.appendChild(badge);
  }
  info.appendChild(titleRow);
  const listLabel = document.createElement('p');
  listLabel.className = 'truncate text-xs text-slate-500 dark:text-slate-400';
  listLabel.textContent = listName;
  info.appendChild(listLabel);
  li.appendChild(info);

  if (amount !== null) {
    const price = document.createElement('span');
    price.className = 'shrink-0 text-sm font-medium text-slate-600 dark:text-slate-300';
    // A recurring item landing more than once in the same month (e.g. a
    // WEEKLY item) shows its summed monthly amount plus an "× N" hint so
    // the total doesn't look like a single line's price.
    price.textContent = occurrenceCount > 1 ? `${formatEuro(amount)} (${t('planning.occurrenceCount', { n: occurrenceCount })})` : formatEuro(amount);
    li.appendChild(price);
  }

  // Rescheduling only makes sense for a one-off purchase — a recurring
  // item's presence in a given month comes from its cadence, not a single
  // target_month field, so there's nothing here to move.
  if (recurring) return li;

  // Lets the user "report" (postpone) or pull forward a planned purchase
  // right from this view — a plain <select> rather than a modal, since a
  // single field is all a reschedule needs. Options span a fixed 12-month
  // window regardless of the active horizon filter, so an item can be
  // pushed out past the currently visible range.
  const select = document.createElement('select');
  select.setAttribute('aria-label', t('planning.moveAriaLabel', { title: item.title }));
  select.className =
    'shrink-0 rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 px-2 py-1.5 text-xs text-slate-800 dark:text-slate-200 focus:border-sky-500 focus:outline-none focus:ring-2 focus:ring-sky-500/30';

  const unscheduledOption = document.createElement('option');
  unscheduledOption.value = '';
  unscheduledOption.textContent = t('planning.unscheduled');
  select.appendChild(unscheduledOption);

  for (const month of monthsFromNow(12)) {
    const option = document.createElement('option');
    option.value = month;
    option.textContent = monthLabel(month, 'long');
    option.selected = month === currentMonth;
    select.appendChild(option);
  }

  select.addEventListener('change', () => moveItemToMonth(item, select.value));
  li.appendChild(select);

  return li;
}

async function moveItemToMonth(item, newMonth) {
  hideError();
  const previous = item.target_month;
  item.target_month = newMonth || null;
  renderPlanning();

  try {
    const updated = await apiRequest(`/items/${item.id}`, { method: 'PATCH', body: JSON.stringify({ target_month: newMonth }) });
    Object.assign(item, updated);
  } catch (err) {
    item.target_month = previous;
    if (!isNetworkError(err)) showError(err.message);
  } finally {
    // Unscheduling drops the item out of this view entirely, same as any
    // other item that has never had a target_month set.
    if (!item.target_month) {
      planningEntries = planningEntries.filter((entry) => entry.item !== item);
    }
    renderPlanning();
  }
  await refreshPendingBadge();
}
