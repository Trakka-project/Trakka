'use strict';

// Task assignments — the frontend half of internal/handlers/assignees.go.
// On a task list (FIELD_VISIBILITY_BY_TYPE's `assignees`, list_view.js):
//
//   - each row shows its assignees as small initials avatars under its
//     title (buildAssigneeAvatars), a tap opening the picker sheet;
//   - the quick-add panel and the edit modal carry a member picker
//     (renderQuickAddAssignees/renderEditItemAssignees), the item actions
//     sheet an "Assigner" row and the multi-select bar an "Assigner" action,
//     both opening #assignee-picker-sheet;
//   - "Par membre" (sort mode 'assignee') groups the list under one heading
//     per member, "Mes tâches" first and "Non assignées" last — an item
//     assigned to two people shows under both;
//   - the filter sheet's "Membres" chips ("Moi", each member, "Non
//     assignées") narrow the list to someone's own to-do; a new task added
//     while exactly one member is filtered on is assigned to them, so it
//     doesn't vanish from the view it was added to.
//
// Who can be assigned, and whether the caller may assign, come from the
// server (GET /lists/{id}/assignable-users, mirrored by sw.js for offline
// use; see internal/handlers/assignees.go): self-hosted, everyone who can
// open the list — its House, the recipients of a share of the list or of
// its Space — and anyone who may edit it may assign; in SaaS mode, the
// House's members only. Someone who may not assign sees the avatars, the
// grouping and the filter, but no picker. Names come from the roster when
// it's known (display name, else email), else from the item itself.
//
// Shares app.js/list_view.js's globals (state, listEls, apiRequest, t,
// renderItems, mergeServerItem, fieldVisibilityFor, getFilterState, ...),
// like every page script.

const ASSIGNEE_SORT_MODE = 'assignee';
// The filter value (and group key) for items assigned to nobody.
const UNASSIGNED_KEY = 'none';
// A row shows at most this many avatars; past it, the last slot is "+N".
const MAX_ROW_AVATARS = 3;
// White text on each of these is at least 4.5:1 (WCAG AA), and they stay
// apart from the urgent rose and the selection sky.
const AVATAR_PALETTE = [
  'bg-violet-600', 'bg-emerald-700', 'bg-amber-700', 'bg-sky-700',
  'bg-fuchsia-700', 'bg-teal-700', 'bg-orange-700', 'bg-indigo-600',
];

const assigneeEls = {
  quickAddField: document.getElementById('item-assignees-field'),
  quickAddChips: document.getElementById('item-assignees'),
  quickAddChip: document.getElementById('quick-add-assignees-chip'),
  quickAddChipLabel: document.getElementById('quick-add-assignees-chip-label'),
  quickAddChipClear: document.getElementById('quick-add-assignees-chip-clear'),
  editField: document.getElementById('edit-item-assignees-field'),
  editChips: document.getElementById('edit-item-assignees'),
  editNote: document.getElementById('edit-item-assignees-note'),
  actionsButton: document.getElementById('item-actions-assignees-button'),
  actionsValue: document.getElementById('item-actions-assignees-value'),
  filterSection: document.getElementById('filter-assignees-section'),
  filterChips: document.getElementById('filter-assignees-chips'),
  bulkButton: document.getElementById('bulk-assignees-button'),
  picker: document.getElementById('assignee-picker-sheet'),
  pickerSubtitle: document.getElementById('assignee-picker-subtitle'),
  pickerOptions: document.getElementById('assignee-picker-options'),
  pickerStatus: document.getElementById('assignee-picker-status'),
  closePicker: document.getElementById('close-assignee-picker-sheet-button'),
  donePicker: document.getElementById('assignee-picker-done-button'),
};

// listId → { canAssign, users: [{ user_id, display_name, email }] }, from
// GET /lists/{id}/assignable-users.
const listRosters = new Map();
// listId → the roster request in flight, so a burst of renders asks once.
const rosterRequests = new Map();

// The list the controls were last set up for (refreshAssigneeControls),
// and the members ticked in the quick-add panel for the next new task.
const assigneeUi = { listId: null, quickAddIds: [] };

// ---------------------------------------------------------------------------
// Who can assign, and the list's roster
// ---------------------------------------------------------------------------

function listSupportsAssignees(list) {
  return Boolean(list) && Boolean(fieldVisibilityFor(list.type).assignees);
}

// A list created offline has no server id yet, so no roster to ask for.
function isUnsavedList(list) {
  return String(list.id).startsWith('temp-');
}

// The server's answer once the roster is in; until then, a member of the
// list's House (state.houses lists the caller's Houses) — who may assign
// in either mode.
function canAssignIn(list) {
  if (!listSupportsAssignees(list)) return false;
  const known = listRosters.get(list.id);
  if (known) return known.canAssign;
  return (state.houses || []).some((house) => house.id === list.house_id);
}

function currentUserId() {
  return state.currentUser ? state.currentUser.id : null;
}

// The people list's items can be assigned to, or null while unknown
// (never loaded, or offline without a mirror).
function rosterOf(list) {
  const known = list ? listRosters.get(list.id) : null;
  return known ? known.users : null;
}

// Loads the list's roster once per list opening: members, shares and the
// list's Space come and go. Best effort: offline, sw.js answers from its
// mirror if it has one.
function loadListRoster(listId) {
  if (rosterRequests.has(listId)) return rosterRequests.get(listId);
  const request = apiRequest(`/lists/${listId}/assignable-users`)
    .then((data) => {
      if (!data || !Array.isArray(data.users)) return;
      listRosters.set(listId, {
        canAssign: Boolean(data.can_assign),
        users: data.users.map((u) => ({ user_id: u.user_id, display_name: u.display_name || '', email: u.email || '' })),
      });
    })
    .catch(() => {})
    .finally(() => rosterRequests.delete(listId));
  rosterRequests.set(listId, request);
  return request;
}

function memberLabel(member) {
  return member.display_name || member.email || t('assignees.unnamed');
}

// An assignee's name: the roster's (which knows the email of an account
// without a display name), else the one the item carries.
function assigneeName(assignee, list) {
  const member = (rosterOf(list) || []).find((m) => m.user_id === assignee.user_id);
  if (member) return memberLabel(member);
  return assignee.display_name || t('assignees.unnamed');
}

// The roster as the pickers show it: me first, then everyone else by name.
function orderedMembers(roster) {
  const me = currentUserId();
  return roster.slice().sort((a, b) => {
    if (a.user_id === me) return -1;
    if (b.user_id === me) return 1;
    return memberLabel(a).localeCompare(memberLabel(b));
  });
}

// The { user_id, display_name } entries an item gets for `ids`, named from
// the roster or from the assignees it already had.
function resolveAssignees(ids, list, current = []) {
  const roster = rosterOf(list) || [];
  return ids.map((id) => {
    const member = roster.find((m) => m.user_id === id);
    const known = current.find((a) => a.user_id === id);
    return { user_id: id, display_name: member ? member.display_name : (known ? known.display_name : '') };
  });
}

function assigneeIdsOf(item) {
  return (item.assignees || []).map((a) => a.user_id);
}

function sameIdSet(a, b) {
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((id) => set.has(id));
}

// ---------------------------------------------------------------------------
// Avatars
// ---------------------------------------------------------------------------

// Up to two initials: "Sarah Martin" → "SM", "thomas.dupont@…" → "TD".
// Array.from keeps a name starting with an emoji whole.
function initialsOf(name) {
  const words = name.replace(/@.*$/, '').split(/[\s._-]+/).filter(Boolean);
  const letters = words.slice(0, 2).map((word) => Array.from(word)[0]).join('');
  return (letters || '?').toLocaleUpperCase();
}

function avatarColorClass(userId) {
  return AVATAR_PALETTE[Math.abs(Number(userId) || 0) % AVATAR_PALETTE.length];
}

// One round avatar. The ring separates overlapping avatars; its color is
// the card's background.
function buildAvatar(userId, name, { large = false } = {}) {
  const avatar = document.createElement('span');
  avatar.className = `inline-flex ${large ? 'h-8 w-8 text-xs' : 'h-6 w-6 text-[10px]'} shrink-0 select-none items-center justify-center rounded-full font-bold leading-none text-white ring-2 ring-slate-50 dark:ring-slate-900 ${avatarColorClass(userId)}`;
  avatar.setAttribute('aria-hidden', 'true');
  avatar.textContent = initialsOf(name);
  return avatar;
}

// The avatars under an assigned item's title (buildItemRow), or null for an
// unassigned one. A button opening the picker where the caller
// can assign, plain otherwise (and in multi-select mode).
function buildAssigneeAvatars(item, { interactive = true } = {}) {
  const assignees = item.assignees || [];
  if (assignees.length === 0) return null;
  const list = state.currentList;
  const names = assignees.map((a) => assigneeName(a, list));
  const editable = interactive && canAssignIn(list) && !isUnsavedItem(item);
  const el = document.createElement(editable ? 'button' : 'span');
  const label = t('assignees.assignedTo', { names: names.join(', ') });
  if (editable) {
    el.type = 'button';
    el.setAttribute('aria-label', t('assignees.editAriaLabel', { names: names.join(', '), title: item.title }));
    el.setAttribute('aria-haspopup', 'dialog');
    el.addEventListener('click', () => openAssigneePicker([item]));
  } else {
    el.setAttribute('role', 'img');
    el.setAttribute('aria-label', label);
  }
  el.title = label;
  // Under the title, on the badges' line (buildItemRow): 24px tall like the
  // aisle tag — WCAG 2.5.8's minimum target size — and only as wide as the
  // avatars (self-start, or a flex column would stretch it).
  el.className = `item-assignees flex min-h-[24px] shrink-0 items-center self-start rounded-full p-0.5${editable ? ' hover:bg-slate-200 dark:hover:bg-slate-700' : ''}${item.done ? ' opacity-60' : ''}`;
  const shown = assignees.length > MAX_ROW_AVATARS ? assignees.slice(0, MAX_ROW_AVATARS - 1) : assignees;
  shown.forEach((assignee, index) => {
    const avatar = buildAvatar(assignee.user_id, names[index]);
    if (index > 0) avatar.classList.add('-ml-1.5');
    el.appendChild(avatar);
  });
  if (shown.length < assignees.length) {
    const more = document.createElement('span');
    more.className = '-ml-1.5 inline-flex h-6 min-w-[1.5rem] shrink-0 items-center justify-center rounded-full bg-slate-500 px-1 text-[10px] font-bold leading-none text-white ring-2 ring-slate-50 dark:bg-slate-600 dark:ring-slate-900';
    more.setAttribute('aria-hidden', 'true');
    more.textContent = `+${assignees.length - shown.length}`;
    el.appendChild(more);
  }
  return el;
}

// An item only the page knows about yet (the create request is in flight):
// nothing to PATCH.
function isUnsavedItem(item) {
  return typeof item.id === 'string' && item.id.startsWith('local-');
}

// ---------------------------------------------------------------------------
// "Par membre" grouping and the "Membres" filter
// ---------------------------------------------------------------------------

// The groups "Par membre" shows for `items` (already in display order):
// my tasks, then each other member's by name, then the unassigned ones.
// An item with two assignees is in both groups. Empty groups are left out.
function groupItemsByAssignee(items, list) {
  const me = currentUserId();
  const groups = new Map();
  const unassigned = [];
  for (const item of items) {
    const assignees = item.assignees || [];
    if (assignees.length === 0) {
      unassigned.push(item);
      continue;
    }
    for (const assignee of assignees) {
      if (!groups.has(assignee.user_id)) {
        groups.set(assignee.user_id, { key: assignee.user_id, userId: assignee.user_id, name: assigneeName(assignee, list), items: [] });
      }
      groups.get(assignee.user_id).items.push(item);
    }
  }
  const ordered = [...groups.values()].sort((a, b) => {
    if (a.userId === me) return -1;
    if (b.userId === me) return 1;
    return a.name.localeCompare(b.name);
  });
  if (unassigned.length > 0) ordered.push({ key: UNASSIGNED_KEY, userId: null, name: '', items: unassigned });
  return ordered;
}

// The heading above each group of "Par membre" (same look as the aisle
// headings of "Par rayon").
function buildAssigneeGroupHeader(group) {
  const li = document.createElement('li');
  li.className = 'assignee-header flex items-center gap-2 px-1 pt-3 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400';
  li.setAttribute('role', 'presentation');
  if (group.userId === null) {
    const icon = document.createElement('span');
    icon.setAttribute('aria-hidden', 'true');
    icon.className = 'inline-flex h-6 w-6 items-center justify-center rounded-full border border-dashed border-slate-400 text-[11px] dark:border-slate-500';
    icon.textContent = '?';
    li.appendChild(icon);
  } else {
    li.appendChild(buildAvatar(group.userId, group.name));
  }
  const label = document.createElement('span');
  label.className = 'min-w-0 truncate';
  if (group.userId === null) label.textContent = t('assignees.groupUnassigned');
  else if (group.userId === currentUserId()) label.textContent = t('assignees.groupMine');
  else label.textContent = t('assignees.groupOf', { name: group.name });
  const badge = document.createElement('span');
  badge.className = 'rounded-full bg-slate-200 dark:bg-slate-700 px-2 py-0.5 text-[11px] font-semibold normal-case tracking-normal text-slate-600 dark:text-slate-300';
  badge.textContent = String(group.items.length);
  li.append(label, badge);
  return li;
}

// Whether item passes the "Membres" filter `wanted` (user ids and/or
// UNASSIGNED_KEY): assigned to one of them, or to nobody when "Non
// assignées" is on.
function matchesAssigneeFilter(item, wanted) {
  const assignees = item.assignees || [];
  if (assignees.length === 0) return wanted.includes(UNASSIGNED_KEY);
  return assignees.some((a) => wanted.includes(a.user_id));
}

// The people the filter offers on this list: the House roster when it's
// known, plus whoever is assigned to one of its items — me first.
function filterCandidates(list) {
  const byId = new Map();
  for (const member of rosterOf(list) || []) byId.set(member.user_id, memberLabel(member));
  for (const item of (list && list.items) || []) {
    for (const assignee of item.assignees || []) {
      if (!byId.has(assignee.user_id)) byId.set(assignee.user_id, assigneeName(assignee, list));
    }
  }
  const me = currentUserId();
  return [...byId.entries()]
    .map(([userId, name]) => ({ userId, name }))
    .sort((a, b) => (a.userId === me ? -1 : b.userId === me ? 1 : a.name.localeCompare(b.name)));
}

function buildFilterAssigneeChip(value, text, avatarUserId, isSelected) {
  const chip = document.createElement('button');
  chip.type = 'button';
  chip.className = isSelected
    ? 'flex min-h-[36px] items-center gap-1.5 rounded-full bg-sky-500 py-1 pl-1 pr-3 text-sm font-semibold text-white'
    : 'flex min-h-[36px] items-center gap-1.5 rounded-full bg-slate-200/70 dark:bg-slate-700/50 py-1 pl-1 pr-3 text-sm font-medium text-slate-600 dark:text-slate-300 hover:bg-slate-300 dark:hover:bg-slate-600';
  if (avatarUserId === null) {
    const icon = document.createElement('span');
    icon.setAttribute('aria-hidden', 'true');
    icon.className = 'inline-flex h-6 w-6 items-center justify-center rounded-full border border-dashed border-current text-[11px]';
    icon.textContent = '?';
    chip.appendChild(icon);
  } else {
    const avatar = buildAvatar(avatarUserId, text);
    avatar.classList.remove('ring-2');
    chip.appendChild(avatar);
  }
  const label = document.createElement('span');
  label.className = 'assignee-chip-name';
  label.textContent = isSelected ? `✓ ${text}` : text;
  chip.appendChild(label);
  chip.setAttribute('aria-pressed', String(isSelected));
  chip.addEventListener('click', () => toggleFilterAssignee(value));
  return chip;
}

// Fills the filter sheet's "Membres" section (called by list_view.js's
// openFilterItemsSheet and its reset button). Hidden on a list type without
// assignments, or with nobody to offer.
function renderFilterAssigneeChips(filterState) {
  const list = state.currentList;
  const candidates = listSupportsAssignees(list) ? filterCandidates(list) : [];
  assigneeEls.filterSection.hidden = candidates.length === 0;
  assigneeEls.filterChips.replaceChildren();
  if (candidates.length === 0) return;
  const selected = filterState.assignees || [];
  const me = currentUserId();
  for (const { userId, name } of candidates) {
    const text = userId === me ? t('assignees.me') : name;
    assigneeEls.filterChips.appendChild(buildFilterAssigneeChip(userId, text, userId, selected.includes(userId)));
  }
  assigneeEls.filterChips.appendChild(buildFilterAssigneeChip(UNASSIGNED_KEY, t('assignees.unassigned'), null, selected.includes(UNASSIGNED_KEY)));
}

function toggleFilterAssignee(value) {
  const list = state.currentList;
  if (!list) return;
  const filterState = getFilterState(list.id);
  const current = filterState.assignees || [];
  filterState.assignees = current.includes(value) ? current.filter((v) => v !== value) : [...current, value];
  setFilterState(list.id, filterState);
  renderFilterAssigneeChips(filterState);
  updateFilterButtonHighlight(list.id);
  resetQuickAddAssignees(list);
  renderItems();
}

// ---------------------------------------------------------------------------
// Member toggles (the quick-add panel and the edit modal)
// ---------------------------------------------------------------------------

function buildMemberToggle(member, pressed, onToggle) {
  const me = currentUserId();
  const name = member.user_id === me ? t('assignees.me') : memberLabel(member);
  const button = document.createElement('button');
  button.type = 'button';
  button.setAttribute('aria-pressed', String(pressed));
  button.className = pressed
    ? 'flex min-h-[40px] max-w-full items-center gap-1.5 rounded-full border border-sky-500 bg-sky-500/10 py-1 pl-1 pr-3 text-sm font-semibold text-sky-700 dark:text-sky-300'
    : 'flex min-h-[40px] max-w-full items-center gap-1.5 rounded-full border border-slate-200 bg-white py-1 pl-1 pr-3 text-sm font-medium text-slate-600 hover:bg-slate-200 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800';
  const avatar = buildAvatar(member.user_id, memberLabel(member));
  avatar.classList.remove('ring-2');
  const label = document.createElement('span');
  label.className = 'min-w-0 truncate';
  label.textContent = name;
  button.append(avatar, label);
  if (pressed) {
    const check = document.createElement('span');
    check.setAttribute('aria-hidden', 'true');
    check.textContent = '✓';
    button.appendChild(check);
  }
  button.addEventListener('click', onToggle);
  return button;
}

function toggledIds(ids, id) {
  return ids.includes(id) ? ids.filter((v) => v !== id) : [...ids, id];
}

// The quick-add default: the one member the filter shows, if it shows
// exactly one — so a task added to "Tâches de Thomas" is Thomas'.
function defaultQuickAddIds(list) {
  if (!canAssignIn(list)) return [];
  const wanted = (getFilterState(list.id).assignees || []);
  const ids = wanted.filter((v) => typeof v === 'number');
  return ids.length === 1 && wanted.length === 1 ? ids : [];
}

function resetQuickAddAssignees(list) {
  assigneeUi.quickAddIds = defaultQuickAddIds(list);
  renderQuickAddAssignees(list);
}

// The assignee_ids of the next quick-add, or null for none.
function quickAddAssigneeIds() {
  return canAssignIn(state.currentList) && assigneeUi.quickAddIds.length > 0 ? assigneeUi.quickAddIds.slice() : null;
}

function renderQuickAddAssignees(list) {
  if (!list) return;
  const roster = rosterOf(list);
  const show = canAssignIn(list) && Boolean(roster) && roster.length > 0;
  assigneeEls.quickAddField.hidden = !show;
  if (show) {
    // Someone who lost access meanwhile can't be preselected any more.
    assigneeUi.quickAddIds = assigneeUi.quickAddIds.filter((id) => roster.some((m) => m.user_id === id));
  }
  assigneeUi.signature = quickAddSignature(list);
  if (!show) {
    assigneeEls.quickAddChip.hidden = true;
    return;
  }
  assigneeEls.quickAddChips.replaceChildren(...orderedMembers(roster).map((member) =>
    buildMemberToggle(member, assigneeUi.quickAddIds.includes(member.user_id), () => {
      assigneeUi.quickAddIds = toggledIds(assigneeUi.quickAddIds, member.user_id);
      renderQuickAddAssignees(state.currentList);
    })));
  updateQuickAddAssigneesChip(list);
}

// The "👤 Pour Thomas ✕" chip under the quick-add bar while the panel is
// collapsed: a preselected assignee stays visible, like the 📅 pill does
// for a due date.
function updateQuickAddAssigneesChip(list) {
  const ids = assigneeUi.quickAddIds;
  const collapsed = listEls.quickAddAdvanced.hidden;
  assigneeEls.quickAddChip.hidden = !(collapsed && ids.length > 0 && canAssignIn(list));
  if (assigneeEls.quickAddChip.hidden) return;
  const me = currentUserId();
  const names = resolveAssignees(ids, list).map((a) => (a.user_id === me ? t('assignees.me') : assigneeName(a, list)));
  assigneeEls.quickAddChipLabel.textContent = t('assignees.quickAddChip', { names: names.join(', ') });
}

assigneeEls.quickAddChipClear.addEventListener('click', () => {
  assigneeUi.quickAddIds = [];
  renderQuickAddAssignees(state.currentList);
  listEls.itemTitle.focus();
});

// The quick-add panel opening/closing decides whether the chip shows.
new MutationObserver(() => updateQuickAddAssigneesChip(state.currentList))
  .observe(listEls.quickAddAdvanced, { attributes: true, attributeFilter: ['hidden'] });

// The edit modal's "Assignée à" field: the ids ticked so far, set when the
// modal opens (openEditItemModal → renderEditItemAssignees).
let editAssigneeIds = null;

function renderEditItemAssignees(item) {
  const list = state.currentList;
  const supports = listSupportsAssignees(list);
  const canAssign = canAssignIn(list);
  const roster = rosterOf(list);
  assigneeEls.editField.hidden = !supports || (!canAssign && (item.assignees || []).length === 0);
  assigneeEls.editChips.replaceChildren();
  assigneeEls.editNote.hidden = true;
  editAssigneeIds = null;
  if (assigneeEls.editField.hidden) return;
  if (!canAssign || !roster) {
    // Read only: someone who may not assign, or no roster (offline, never
    // loaded).
    const names = (item.assignees || []).map((a) => assigneeName(a, list));
    assigneeEls.editNote.hidden = false;
    assigneeEls.editNote.textContent = [
      names.length > 0 ? t('assignees.assignedTo', { names: names.join(', ') }) : t('assignees.nobody'),
      canAssign ? t('assignees.rosterUnavailable') : t('assignees.cannotAssign'),
    ].join(' — ');
    return;
  }
  editAssigneeIds = assigneeIdsOf(item);
  const draw = () => {
    assigneeEls.editChips.replaceChildren(...orderedMembers(roster).map((member) =>
      buildMemberToggle(member, editAssigneeIds.includes(member.user_id), () => {
        editAssigneeIds = toggledIds(editAssigneeIds, member.user_id);
        draw();
      })));
  };
  draw();
}

// What the edit form's PATCH should carry for the assignees: their new ids
// when they changed, else null (left out, so a share recipient's save and
// an unrelated edit never touch them).
function editItemAssigneeIds(item) {
  if (editAssigneeIds === null || sameIdSet(editAssigneeIds, assigneeIdsOf(item))) return null;
  return editAssigneeIds.slice();
}

// ---------------------------------------------------------------------------
// Picker sheet (#assignee-picker-sheet): one item (its avatars, its
// actions sheet) or the multi-select selection. Every tap commits.
// ---------------------------------------------------------------------------

let assigneePickerItems = null;

// Per-item coalescing, the same pattern as list_view.js's commitItemPatches
// (whose body keys are item fields — here the request says assignee_ids
// and the item holds assignees): the last server-confirmed assignees plus
// the id of the latest request, so a slow earlier response never overrides
// a later tap.
const pendingAssigneeUpdates = new Map();

function repaintAssignees() {
  renderItems();
  if (assigneePickerItems) renderAssigneePicker();
}

async function commitAssignees(items, idsFor) {
  hideError();
  const list = state.currentList;
  const jobs = [];
  for (const item of items) {
    if (isUnsavedItem(item)) continue;
    const ids = idsFor(item);
    if (!ids || sameIdSet(ids, assigneeIdsOf(item))) continue;
    const pending = pendingAssigneeUpdates.get(item);
    const committed = pending ? pending.committed : (item.assignees || []).slice();
    const requestId = Symbol('assignees');
    item.assignees = resolveAssignees(ids, list, item.assignees || []);
    pendingAssigneeUpdates.set(item, { committed, requestId });
    jobs.push({ item, ids, committed, requestId });
  }
  if (jobs.length === 0) return;
  repaintAssignees();

  const errors = [];
  await runWithConcurrency(jobs, BULK_REQUEST_CONCURRENCY, async ({ item, ids, committed, requestId }) => {
    try {
      const updated = await apiRequest(`/items/${item.id}`, { method: 'PATCH', body: JSON.stringify({ assignee_ids: ids }) });
      if (pendingAssigneeUpdates.get(item)?.requestId !== requestId) return; // superseded by a newer tap
      mergeServerItem(item, updated);
      pendingAssigneeUpdates.delete(item);
    } catch (err) {
      if (pendingAssigneeUpdates.get(item)?.requestId !== requestId) return;
      item.assignees = committed;
      pendingAssigneeUpdates.delete(item);
      errors.push(err);
    }
  });
  repaintAssignees();
  reportBulkErrors(errors);
  await refreshPendingBadge();
}

// A tap on a member: off for everyone when every item already has them,
// otherwise on for each item that doesn't — for one item, a plain toggle
// (the label sheet's rule).
function togglePickerMember(userId) {
  const items = assigneePickerItems || [];
  const all = items.every((item) => assigneeIdsOf(item).includes(userId));
  commitAssignees(items, (item) => {
    const ids = assigneeIdsOf(item);
    if (all) return ids.filter((id) => id !== userId);
    return ids.includes(userId) ? null : [...ids, userId];
  });
}

function buildPickerOption(member, state3) {
  const me = currentUserId();
  const button = document.createElement('button');
  button.type = 'button';
  button.setAttribute('role', 'checkbox');
  button.setAttribute('aria-checked', state3);
  button.className = 'flex min-h-[52px] min-w-0 items-center gap-3 rounded-xl border px-3 py-2 text-left text-base font-medium ' +
    'border-slate-200 bg-white text-slate-900 hover:bg-slate-200 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100 dark:hover:bg-slate-700 ' +
    'aria-checked:border-sky-500 aria-checked:bg-sky-500/10 aria-checked:text-sky-700 dark:aria-checked:text-sky-300';
  const avatar = buildAvatar(member.user_id, memberLabel(member), { large: true });
  avatar.classList.remove('ring-2');
  const text = document.createElement('span');
  text.className = 'assignee-option-name min-w-0 flex-1 truncate';
  text.textContent = member.user_id === me ? t('assignees.meWithName', { name: memberLabel(member) }) : memberLabel(member);
  const check = document.createElement('span');
  check.setAttribute('aria-hidden', 'true');
  check.className = 'shrink-0 text-lg text-sky-500 dark:text-sky-400';
  check.textContent = state3 === 'true' ? '✓' : state3 === 'mixed' ? '–' : '';
  button.append(avatar, text, check);
  button.addEventListener('click', () => togglePickerMember(member.user_id));
  return button;
}

function renderAssigneePicker() {
  const items = assigneePickerItems || [];
  const list = state.currentList;
  const roster = rosterOf(list);
  assigneeEls.pickerSubtitle.textContent = items.length === 1
    ? items[0].title
    : t('assignees.pickerSubtitleOther', { count: items.length });
  const focusedIndex = [...assigneeEls.pickerOptions.children].indexOf(document.activeElement);
  if (!roster || roster.length === 0) {
    assigneeEls.pickerOptions.replaceChildren();
    assigneeEls.pickerStatus.hidden = false;
    assigneeEls.pickerStatus.textContent = t(roster ? 'assignees.noMembers' : 'assignees.rosterUnavailable');
    return;
  }
  assigneeEls.pickerStatus.hidden = true;
  assigneeEls.pickerOptions.replaceChildren(...orderedMembers(roster).map((member) => {
    const count = items.filter((item) => assigneeIdsOf(item).includes(member.user_id)).length;
    const checked = count === 0 ? 'false' : count === items.length ? 'true' : 'mixed';
    return buildPickerOption(member, checked);
  }));
  // A re-render after a tap keeps focus where it was.
  if (focusedIndex >= 0) assigneeEls.pickerOptions.children[focusedIndex]?.focus();
}

function openAssigneePicker(items) {
  const list = state.currentList;
  if (!canAssignIn(list) || items.length === 0) return;
  assigneePickerItems = items;
  renderAssigneePicker();
  assigneeEls.picker.hidden = false;
  TrakkaScrollLock.lock();
  (assigneeEls.pickerOptions.firstElementChild || assigneeEls.donePicker).focus();
  if (!rosterOf(list) && !isUnsavedList(list)) {
    loadListRoster(list.id).then(() => {
      if (assigneePickerItems) {
        renderAssigneePicker();
        assigneeEls.pickerOptions.firstElementChild?.focus();
      }
    });
  }
}

function closeAssigneePicker() {
  assigneePickerItems = null;
  assigneeEls.picker.hidden = true;
  TrakkaScrollLock.unlock();
}

assigneeEls.closePicker.addEventListener('click', closeAssigneePicker);
assigneeEls.donePicker.addEventListener('click', closeAssigneePicker);
assigneeEls.picker.addEventListener('click', (event) => {
  if (event.target === assigneeEls.picker) closeAssigneePicker();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !assigneeEls.picker.hidden) closeAssigneePicker();
});
// Arrow keys move between the options, like any list of checkboxes.
assigneeEls.pickerOptions.addEventListener('keydown', (event) => {
  const options = [...assigneeEls.pickerOptions.children];
  const index = options.indexOf(document.activeElement);
  const step = { ArrowDown: 1, ArrowUp: -1 }[event.key];
  if (index < 0 || !step) return;
  event.preventDefault();
  options[Math.min(options.length - 1, Math.max(0, index + step))].focus();
});

// ---------------------------------------------------------------------------
// Item actions sheet row and multi-select action
// ---------------------------------------------------------------------------

// Called by list_view.js's openItemActionsSheet: "👤 Assigner · Thomas".
function updateItemActionsAssigneesButton(item) {
  const show = canAssignIn(state.currentList) && !isUnsavedItem(item);
  assigneeEls.actionsButton.hidden = !show;
  if (!show) return;
  const names = (item.assignees || []).map((a) => assigneeName(a, state.currentList));
  assigneeEls.actionsValue.textContent = names.length > 0 ? names.join(', ') : t('assignees.nobody');
}

assigneeEls.actionsButton.addEventListener('click', () => {
  const item = itemActionsSheetItem;
  closeItemActionsSheet();
  if (item) openAssigneePicker([item]);
});

// Called by selection.js's updateBulkActionsBar.
function updateBulkAssigneesButton(list, count) {
  assigneeEls.bulkButton.hidden = !canAssignIn(list);
  assigneeEls.bulkButton.disabled = count === 0;
}

assigneeEls.bulkButton.addEventListener('click', () => {
  const items = selectedItems();
  if (items.length > 0) openAssigneePicker(items);
});

// ---------------------------------------------------------------------------
// Per-render setup (called by list_view.js's renderItems)
// ---------------------------------------------------------------------------

// What the quick-add toggles currently show — redrawn only when it changes,
// not on every render (a redraw would take the focus off a toggle).
function quickAddSignature(list) {
  const roster = rosterOf(list) || [];
  return [canAssignIn(list), roster.map((m) => `${m.user_id}:${memberLabel(m)}`).join(','), assigneeUi.quickAddIds.join(',')].join('|');
}

// What the roster loaded for the open list depends on: the list, and the
// Space it's filed in (whose share recipients are assignable, self-hosted).
function rosterKey(list) {
  return `${list.id}:${list.custom_category_id ?? ''}`;
}

function refreshAssigneeControls(list) {
  if (!list) return;
  if (assigneeUi.listId !== list.id) {
    assigneeUi.listId = list.id;
    assigneeUi.signature = null;
    assigneeUi.quickAddIds = defaultQuickAddIds(list);
  }
  // Once per list opening, and again when the list moves to another Space
  // or a share may have changed (forgetAssignableUsers). The roster it
  // replaces stays in use until the new one is in.
  if (listSupportsAssignees(list) && !isUnsavedList(list) && assigneeUi.rosterKey !== rosterKey(list)) {
    assigneeUi.rosterKey = rosterKey(list);
    loadListRoster(list.id).then(() => {
      // Names and pickers can only be drawn once the roster is in.
      if (state.currentList && state.currentList.id === list.id) renderItems();
    });
  }
  if (quickAddSignature(list) !== assigneeUi.signature) renderQuickAddAssignees(list);
}

// Has the next render ask the server for the roster again — called when the
// list view closes (list_view.js's showDashboard), so the next opening
// reflects members, shares and Spaces changed meanwhile, and when the share
// modal closes (shares.js), where a share of this list or its Space may
// have come or gone.
function forgetAssignableUsers() {
  assigneeUi.rosterKey = null;
  if (state.currentList) refreshAssigneeControls(state.currentList);
}

document.addEventListener('trakka:lang-changed', () => {
  if (!state.currentList) return;
  renderQuickAddAssignees(state.currentList);
  if (assigneePickerItems) renderAssigneePicker();
});
