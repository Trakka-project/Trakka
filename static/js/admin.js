'use strict';

// Admin "Console d'Administration": a single modal, reachable only from
// inside the ordinary user "Paramètres" modal (settings.js's
// #user-settings-modal) via a button shown only to admins — there is no
// standalone header-level admin button (see CLAUDE.md's session-handoff
// log for the navigation-restructuring session that moved it here). Four
// internal tabs, each backed by its own GET+mutate endpoints under
// /api/v1/admin/...:
//   - "Paramètres": instance name, registration policy, OIDC/SSO config
//     (GET+PATCH /api/v1/admin/settings) — unchanged from the original
//     standalone "Paramètres du Système" panel this replaces, just moved
//     one level deeper.
//   - "Utilisateurs": every account on the instance, with a promote/demote
//     admin-role toggle and an account-deletion action
//     (GET /api/v1/admin/users, PATCH/DELETE /api/v1/admin/users/{id}).
//   - "Espaces": every user's Space (custom category), across the whole
//     instance, with a delete action
//     (GET /api/v1/admin/spaces, DELETE /api/v1/admin/spaces/{id}).
//   - "Logs": a live, in-memory snapshot of recent server log entries, no
//     persistence across a restart (GET /api/v1/admin/logs).
//
// Shares `state`, `apiRequest`, `showError`/`hideError`, `t`,
// `isNetworkError`, `TRASH_ICON_SVG` with app.js/list_view.js — same
// classic-<script>-tags shared-scope pattern as those files. Loaded after
// settings.js/push.js/install-help.js (see index.html's script order) so
// it can follow the exact same "modal stacked on the settings modal"
// pattern install-help.js already established — referencing
// `userSettingsEls` (defined in settings.js) only from inside function
// bodies invoked later, never at top-level parse time, is what makes the
// load order safe regardless.

const adminConsoleEls = {
  openButton: document.getElementById('admin-console-button'),
  modal: document.getElementById('admin-console-modal'),
  closeButton: document.getElementById('close-admin-console-modal-button'),

  tabButtons: {
    settings: document.getElementById('admin-tab-settings'),
    users: document.getElementById('admin-tab-users'),
    spaces: document.getElementById('admin-tab-spaces'),
    logs: document.getElementById('admin-tab-logs'),
  },
  panels: {
    settings: document.getElementById('admin-panel-settings'),
    users: document.getElementById('admin-panel-users'),
    spaces: document.getElementById('admin-panel-spaces'),
    logs: document.getElementById('admin-panel-logs'),
  },

  // "Paramètres" tab.
  form: document.getElementById('admin-settings-form'),
  instanceName: document.getElementById('admin-instance-name'),
  registrationOpen: document.getElementById('admin-registration-open'),
  oidcEnabled: document.getElementById('admin-oidc-enabled'),
  oidcIssuer: document.getElementById('admin-oidc-issuer'),
  oidcClientId: document.getElementById('admin-oidc-client-id'),
  oidcClientSecret: document.getElementById('admin-oidc-client-secret'),
  status: document.getElementById('admin-settings-status'),

  // "Utilisateurs" tab.
  usersList: document.getElementById('admin-users-list'),
  usersEmpty: document.getElementById('admin-users-empty'),

  // "Espaces" tab.
  spacesList: document.getElementById('admin-spaces-list'),
  spacesEmpty: document.getElementById('admin-spaces-empty'),

  // "Logs" tab.
  logsList: document.getElementById('admin-logs-list'),
  logsEmpty: document.getElementById('admin-logs-empty'),
  logsRefreshButton: document.getElementById('admin-logs-refresh-button'),
};

let adminConsoleActiveTab = 'settings';

// Called once state.currentUser is known (app.js's init) and again every
// time the settings modal opens (settings.js) — cheap, and keeps the
// button's visibility correct even if currentUser was still null the
// first time (e.g. the /me call failed while offline and only resolved
// later).
function refreshAdminConsoleButtonVisibility() {
  adminConsoleEls.openButton.hidden = !state.currentUser || !state.currentUser.is_admin;
}

// ---------------------------------------------------------------------------
// Modal open/close + tab switching
// ---------------------------------------------------------------------------

// Tab buttons carry no color/border/background utility classes of their
// own (see index.html) — every visual state (default, :hover, and
// [aria-selected="true"]) is driven entirely by static/css/base.css off
// this one attribute, so toggling it here is the only DOM change needed.
// This replaces an earlier version that toggled a dozen individual
// Tailwind color/border/background classes by hand on every click, which
// could leave an inactive tab's label unreadable (its own text color and
// the active tab's accent color ending up on the same element depending
// on click order) — only revealing itself via ::selection when the text
// was manually highlighted. A single state attribute removes that whole
// class of bug: exactly one CSS rule can ever apply to a given button.
function setAdminConsoleTab(tab) {
  adminConsoleActiveTab = tab;
  for (const [name, button] of Object.entries(adminConsoleEls.tabButtons)) {
    const active = name === tab;
    button.setAttribute('aria-selected', String(active));
    adminConsoleEls.panels[name].hidden = !active;
  }

  if (tab === 'users') loadAdminUsers();
  else if (tab === 'spaces') loadAdminSpaces();
  else if (tab === 'logs') loadAdminLogs();
}

function openAdminConsoleModal() {
  adminConsoleEls.status.hidden = true;
  setAdminConsoleTab('settings');
  loadAdminSettings();
  adminConsoleEls.modal.hidden = false;
  document.body.classList.add('overflow-hidden');
}

function closeAdminConsoleModal() {
  adminConsoleEls.modal.hidden = true;
  // Opened from on top of the settings modal — only release the shared
  // body scroll-lock if that one isn't still open behind it (same pattern
  // as install-help.js's closeInstallHelpModal).
  if (userSettingsEls.modal.hidden) {
    document.body.classList.remove('overflow-hidden');
  }
}

adminConsoleEls.openButton.addEventListener('click', openAdminConsoleModal);
adminConsoleEls.closeButton.addEventListener('click', closeAdminConsoleModal);
adminConsoleEls.modal.addEventListener('click', (event) => {
  if (event.target === adminConsoleEls.modal) closeAdminConsoleModal();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !adminConsoleEls.modal.hidden) closeAdminConsoleModal();
});
for (const [name, button] of Object.entries(adminConsoleEls.tabButtons)) {
  button.addEventListener('click', () => setAdminConsoleTab(name));
}

// ---------------------------------------------------------------------------
// Shared: an inline "click again to confirm" destructive-action button.
// Avoids a native confirm() popup (this app uses none anywhere else) while
// still requiring a deliberate second action before something as
// consequential as deleting an account or a Space actually happens.
// ---------------------------------------------------------------------------

const ADMIN_CONFIRM_WINDOW_MS = 4000;

function buildConfirmableDeleteButton(ariaLabel, onConfirm) {
  const button = document.createElement('button');
  button.type = 'button';
  button.setAttribute('aria-label', ariaLabel);
  button.className =
    'flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-slate-500 hover:bg-rose-500/10 hover:text-rose-600 dark:hover:text-rose-400';
  button.innerHTML = TRASH_ICON_SVG;

  let armed = false;
  let timer = null;

  // Armed state swaps the fixed w-9 icon square for an auto-width pill,
  // since "Confirmer la suppression ?" doesn't fit a 36px square.
  const disarm = () => {
    armed = false;
    if (timer) clearTimeout(timer);
    button.innerHTML = TRASH_ICON_SVG;
    button.classList.remove('bg-rose-500/10', 'text-rose-600', 'dark:text-rose-400', 'px-3', 'whitespace-nowrap');
    button.classList.add('text-slate-500', 'w-9');
  };

  button.addEventListener('click', () => {
    if (!armed) {
      armed = true;
      button.classList.remove('text-slate-500', 'w-9');
      button.classList.add('bg-rose-500/10', 'text-rose-600', 'dark:text-rose-400', 'px-3', 'whitespace-nowrap');
      button.textContent = t('modals.adminConsole.confirmDelete');
      timer = setTimeout(disarm, ADMIN_CONFIRM_WINDOW_MS);
      return;
    }
    disarm();
    onConfirm();
  });

  return button;
}

function formatAdminTimestamp(iso) {
  try {
    const locale = window.TrakkaI18n ? TrakkaI18n.getLang() : 'fr';
    return new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(iso));
  } catch {
    return iso;
  }
}

// ---------------------------------------------------------------------------
// "Paramètres" tab — instance name, registration policy, OIDC/SSO config.
// ---------------------------------------------------------------------------

function applySecretPlaceholder(secretSet) {
  adminConsoleEls.oidcClientSecret.value = '';
  adminConsoleEls.oidcClientSecret.placeholder = secretSet
    ? t('modals.adminSettings.oidcClientSecretPlaceholderSet')
    : t('modals.adminSettings.oidcClientSecretPlaceholderUnset');
}

async function loadAdminSettings() {
  let settings;
  try {
    settings = await apiRequest('/admin/settings');
  } catch (err) {
    // No offline mirror for admin settings — same "leave the panel empty
    // without a blocking banner" reasoning as app.js's loadMembers.
    if (!isNetworkError(err)) showError(err.message);
    return;
  }
  adminConsoleEls.instanceName.value = settings.instance_name;
  adminConsoleEls.registrationOpen.checked = settings.registration_open;
  adminConsoleEls.oidcEnabled.checked = settings.oidc_enabled;
  adminConsoleEls.oidcIssuer.value = settings.oidc_issuer;
  adminConsoleEls.oidcClientId.value = settings.oidc_client_id;
  applySecretPlaceholder(settings.oidc_client_secret_set);
}

adminConsoleEls.form.addEventListener('submit', async (event) => {
  event.preventDefault();
  hideError();
  adminConsoleEls.status.hidden = true;

  const body = {
    instance_name: adminConsoleEls.instanceName.value.trim(),
    registration_open: adminConsoleEls.registrationOpen.checked,
    oidc_enabled: adminConsoleEls.oidcEnabled.checked,
    oidc_issuer: adminConsoleEls.oidcIssuer.value.trim(),
    oidc_client_id: adminConsoleEls.oidcClientId.value.trim(),
  };
  // The secret field only ever carries a *new* value: it's never
  // pre-filled with the stored one (see adminSettingsView server-side), so
  // an empty field always means "leave it as-is", matching the backend's
  // own "empty = unchanged" convention for this one field.
  if (adminConsoleEls.oidcClientSecret.value) {
    body.oidc_client_secret = adminConsoleEls.oidcClientSecret.value;
  }

  let settings;
  try {
    settings = await apiRequest('/admin/settings', { method: 'PATCH', body: JSON.stringify(body) });
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return;
  }

  adminConsoleEls.oidcIssuer.value = settings.oidc_issuer;
  adminConsoleEls.oidcClientId.value = settings.oidc_client_id;
  applySecretPlaceholder(settings.oidc_client_secret_set);
  adminConsoleEls.status.textContent = t('modals.adminSettings.saved');
  adminConsoleEls.status.hidden = false;
});

// ---------------------------------------------------------------------------
// "Utilisateurs" tab — every account, with an admin-role toggle and delete.
// ---------------------------------------------------------------------------

function buildAdminUserRow(user, adminCount) {
  const li = document.createElement('li');
  li.className =
    'flex items-center justify-between gap-2 rounded-xl border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 px-3 py-2';

  const info = document.createElement('div');
  info.className = 'min-w-0';
  const name = document.createElement('p');
  name.className = 'truncate text-sm font-medium text-slate-900 dark:text-slate-100';
  name.textContent = user.display_name || user.email;
  const email = document.createElement('p');
  email.className = 'truncate text-xs text-slate-500 dark:text-slate-400';
  email.textContent = user.email;
  info.append(name, email);
  li.appendChild(info);

  const actions = document.createElement('div');
  actions.className = 'flex shrink-0 items-center gap-2';

  if (user.is_admin) {
    actions.appendChild(badge(t('modals.adminConsole.users.adminBadge'), 'sky'));
  }

  const isSelf = state.currentUser && state.currentUser.id === user.id;
  const isLastAdmin = user.is_admin && adminCount <= 1;

  const toggleButton = document.createElement('button');
  toggleButton.type = 'button';
  toggleButton.className =
    'min-h-[36px] shrink-0 rounded-lg border border-slate-200 dark:border-slate-700 bg-slate-100 dark:bg-slate-800/60 px-3 py-1.5 text-xs font-medium text-slate-600 dark:text-slate-300 hover:bg-slate-200 dark:hover:bg-slate-800 disabled:cursor-not-allowed disabled:opacity-50';
  toggleButton.textContent = user.is_admin ? t('modals.adminConsole.users.demote') : t('modals.adminConsole.users.promote');
  toggleButton.disabled = user.is_admin && isLastAdmin;
  toggleButton.title = toggleButton.disabled ? t('modals.adminConsole.users.lastAdminHint') : '';
  toggleButton.addEventListener('click', () => setAdminUserRole(user.id, !user.is_admin));
  actions.appendChild(toggleButton);

  const deleteButton = buildConfirmableDeleteButton(
    t('modals.adminConsole.users.deleteAriaLabel', { email: user.email }),
    () => deleteAdminUser(user.id)
  );
  deleteButton.disabled = isSelf || isLastAdmin;
  if (deleteButton.disabled) {
    deleteButton.classList.add('cursor-not-allowed', 'opacity-40');
    deleteButton.title = isSelf ? t('modals.adminConsole.users.selfDeleteHint') : t('modals.adminConsole.users.lastAdminHint');
  }
  actions.appendChild(deleteButton);

  li.appendChild(actions);
  return li;
}

async function loadAdminUsers() {
  let users;
  try {
    users = await apiRequest('/admin/users');
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return;
  }
  const adminCount = users.filter((u) => u.is_admin).length;
  adminConsoleEls.usersEmpty.hidden = users.length > 0;
  adminConsoleEls.usersList.replaceChildren();
  for (const user of users) {
    adminConsoleEls.usersList.appendChild(buildAdminUserRow(user, adminCount));
  }
}

async function setAdminUserRole(userId, isAdmin) {
  hideError();
  try {
    await apiRequest(`/admin/users/${userId}`, { method: 'PATCH', body: JSON.stringify({ is_admin: isAdmin }) });
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return;
  }
  await loadAdminUsers();
}

async function deleteAdminUser(userId) {
  hideError();
  try {
    await apiRequest(`/admin/users/${userId}`, { method: 'DELETE' });
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return;
  }
  await loadAdminUsers();
}

// ---------------------------------------------------------------------------
// "Espaces" tab — every user's Space, across the whole instance.
// ---------------------------------------------------------------------------

function buildAdminSpaceRow(space) {
  const li = document.createElement('li');
  li.className =
    'flex items-center justify-between gap-2 rounded-xl border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 px-3 py-2';

  const swatch = document.createElement('span');
  swatch.setAttribute('aria-hidden', 'true');
  swatch.className = 'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-base';
  swatch.style.backgroundColor = space.color ? `${space.color}22` : '';
  swatch.textContent = space.icon || '🗂️';

  const info = document.createElement('div');
  info.className = 'min-w-0 flex-1';
  const name = document.createElement('p');
  name.className = 'truncate text-sm font-medium text-slate-900 dark:text-slate-100';
  name.textContent = space.name;
  const meta = document.createElement('p');
  meta.className = 'truncate text-xs text-slate-500 dark:text-slate-400';
  meta.textContent = t('modals.adminConsole.spaces.ownerAndCount', {
    owner: space.owner_display_name || space.owner_email,
    count: space.list_count,
  });
  info.append(name, meta);

  const deleteButton = buildConfirmableDeleteButton(
    t('modals.adminConsole.spaces.deleteAriaLabel', { name: space.name }),
    () => deleteAdminSpace(space.id)
  );

  li.append(swatch, info, deleteButton);
  return li;
}

async function loadAdminSpaces() {
  let spaces;
  try {
    spaces = await apiRequest('/admin/spaces');
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return;
  }
  adminConsoleEls.spacesEmpty.hidden = spaces.length > 0;
  adminConsoleEls.spacesList.replaceChildren();
  for (const space of spaces) {
    adminConsoleEls.spacesList.appendChild(buildAdminSpaceRow(space));
  }
}

async function deleteAdminSpace(categoryId) {
  hideError();
  try {
    await apiRequest(`/admin/spaces/${categoryId}`, { method: 'DELETE' });
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return;
  }
  await loadAdminSpaces();
}

// ---------------------------------------------------------------------------
// "Logs" tab — a live, in-memory snapshot of recent server activity.
// ---------------------------------------------------------------------------

const ADMIN_LOG_LEVEL_CLASSES = {
  ERROR: 'text-rose-600 dark:text-rose-400',
  WARN: 'text-amber-600 dark:text-amber-400',
  INFO: 'text-sky-600 dark:text-sky-400',
  DEBUG: 'text-slate-500 dark:text-slate-400',
};

function buildAdminLogRow(entry) {
  const li = document.createElement('li');
  li.className = 'rounded-lg border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 px-3 py-2';

  const line = document.createElement('p');
  line.className = 'flex flex-wrap items-baseline gap-2';

  const time = document.createElement('span');
  time.className = 'text-slate-500 dark:text-slate-400';
  time.textContent = formatAdminTimestamp(entry.time);

  const level = document.createElement('span');
  level.className = `font-semibold ${ADMIN_LOG_LEVEL_CLASSES[entry.level] || ADMIN_LOG_LEVEL_CLASSES.INFO}`;
  level.textContent = entry.level;

  const message = document.createElement('span');
  message.className = 'text-slate-800 dark:text-slate-200';
  message.textContent = entry.message;

  line.append(time, level, message);
  li.appendChild(line);

  if (entry.attrs && Object.keys(entry.attrs).length > 0) {
    const attrs = document.createElement('p');
    attrs.className = 'mt-1 truncate text-slate-500 dark:text-slate-400';
    attrs.textContent = JSON.stringify(entry.attrs);
    li.appendChild(attrs);
  }

  return li;
}

async function loadAdminLogs() {
  let entries;
  try {
    entries = await apiRequest('/admin/logs');
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return;
  }
  adminConsoleEls.logsEmpty.hidden = entries.length > 0;
  adminConsoleEls.logsList.replaceChildren();
  for (const entry of entries) {
    adminConsoleEls.logsList.appendChild(buildAdminLogRow(entry));
  }
}

adminConsoleEls.logsRefreshButton.addEventListener('click', loadAdminLogs);
