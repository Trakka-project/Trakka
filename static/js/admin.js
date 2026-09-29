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
//   - "Sauvegardes": encrypted WebDAV backups — connection settings,
//     automatic schedule, retention, the encryption key, history, and
//     restore (GET/PUT/POST /api/v1/admin/backups/...). Its alerts also
//     drive a discreet dot on the header's settings button and a ⚠️ on the
//     admin console button, for admins only — never a popup or banner.
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
    backups: document.getElementById('admin-tab-backups'),
  },
  panels: {
    settings: document.getElementById('admin-panel-settings'),
    users: document.getElementById('admin-panel-users'),
    spaces: document.getElementById('admin-panel-spaces'),
    logs: document.getElementById('admin-panel-logs'),
    backups: document.getElementById('admin-panel-backups'),
  },

  // "Paramètres" tab.
  form: document.getElementById('admin-settings-form'),
  instanceName: document.getElementById('admin-instance-name'),
  registrationOpen: document.getElementById('admin-registration-open'),
  oidcEnabled: document.getElementById('admin-oidc-enabled'),
  oidcIssuer: document.getElementById('admin-oidc-issuer'),
  oidcClientId: document.getElementById('admin-oidc-client-id'),
  oidcClientSecret: document.getElementById('admin-oidc-client-secret'),
  oidcExclusive: document.getElementById('admin-oidc-exclusive'),
  oidcProviderName: document.getElementById('admin-oidc-provider-name'),
  settingsError: document.getElementById('admin-settings-error'),
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
  if (adminConsoleEls.openButton.hidden) applyBackupAlertIndicators([]);
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
  else if (tab === 'backups') openAdminBackupsTab();
}

function openAdminConsoleModal() {
  adminConsoleEls.status.hidden = true;
  setAdminConsoleTab('settings');
  loadAdminSettings();
  refreshBackupAlertIndicators();
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
  adminConsoleEls.oidcExclusive.checked = settings.oidc_exclusive;
  adminConsoleEls.oidcProviderName.value = settings.oidc_provider_name;
  applySecretPlaceholder(settings.oidc_client_secret_set);
}

adminConsoleEls.form.addEventListener('submit', async (event) => {
  event.preventDefault();
  hideError();
  adminConsoleEls.settingsError.hidden = true;
  adminConsoleEls.status.hidden = true;

  const body = {
    instance_name: adminConsoleEls.instanceName.value.trim(),
    registration_open: adminConsoleEls.registrationOpen.checked,
    oidc_enabled: adminConsoleEls.oidcEnabled.checked,
    oidc_issuer: adminConsoleEls.oidcIssuer.value.trim(),
    oidc_client_id: adminConsoleEls.oidcClientId.value.trim(),
    oidc_exclusive: adminConsoleEls.oidcExclusive.checked,
    oidc_provider_name: adminConsoleEls.oidcProviderName.value.trim(),
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
    // Shown inside the form: app.js's error banner sits under this modal.
    if (!isNetworkError(err)) {
      adminConsoleEls.settingsError.textContent = err.message;
      adminConsoleEls.settingsError.hidden = false;
    }
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

// ---------------------------------------------------------------------------
// "Sauvegardes" tab — encrypted WebDAV backups.
//
// Everything sensitive happens server-side: the database is snapshotted,
// encrypted and uploaded by the server (internal/backup); this page only
// edits the configuration, triggers actions and shows status. The WebDAV
// password is write-only (never sent back), and the encryption key only
// ever leaves the server through the explicit download/copy buttons.
// ---------------------------------------------------------------------------

const backupEls = {
  headerDot: document.getElementById('user-settings-alert-dot'),
  consoleAlert: document.getElementById('admin-console-alert'),
  tabAlert: document.getElementById('admin-tab-backups-alert'),

  alerts: document.getElementById('admin-backups-alerts'),
  statusBadge: document.getElementById('admin-backups-status-badge'),
  lastRun: document.getElementById('admin-backups-last-run'),
  lastSuccess: document.getElementById('admin-backups-last-success'),
  nextRun: document.getElementById('admin-backups-next-run'),
  runButton: document.getElementById('admin-backups-run-button'),
  runError: document.getElementById('admin-backups-run-error'),

  form: document.getElementById('admin-backups-form'),
  url: document.getElementById('admin-backups-url'),
  username: document.getElementById('admin-backups-username'),
  password: document.getElementById('admin-backups-password'),
  privateHint: document.getElementById('admin-backups-private-hint'),
  testButton: document.getElementById('admin-backups-test-button'),
  testResult: document.getElementById('admin-backups-test-result'),
  auto: document.getElementById('admin-backups-auto'),
  frequency: document.getElementById('admin-backups-frequency'),
  weekdayField: document.getElementById('admin-backups-weekday-field'),
  weekday: document.getElementById('admin-backups-weekday'),
  time: document.getElementById('admin-backups-time'),
  scheduleHint: document.getElementById('admin-backups-schedule-hint'),
  retention: document.getElementById('admin-backups-retention'),
  formError: document.getElementById('admin-backups-form-error'),
  formStatus: document.getElementById('admin-backups-form-status'),

  keyFingerprint: document.getElementById('admin-backups-key-fingerprint'),
  keyDownload: document.getElementById('admin-backups-key-download'),
  keyCopy: document.getElementById('admin-backups-key-copy'),
  keyStatus: document.getElementById('admin-backups-key-status'),

  runsList: document.getElementById('admin-backups-runs'),
  runsEmpty: document.getElementById('admin-backups-runs-empty'),

  remoteRefresh: document.getElementById('admin-backups-remote-refresh'),
  remoteMessage: document.getElementById('admin-backups-remote-message'),
  remoteList: document.getElementById('admin-backups-remote-list'),

  restoreSection: document.getElementById('admin-backups-restore-section'),
  restoreForm: document.getElementById('admin-backups-restore-form'),
  restoreUpload: document.getElementById('admin-backups-restore-upload'),
  restoreFile: document.getElementById('admin-backups-restore-file'),
  restoreRemote: document.getElementById('admin-backups-restore-remote'),
  restoreRemoteName: document.getElementById('admin-backups-restore-remote-name'),
  restoreRemoteClear: document.getElementById('admin-backups-restore-remote-clear'),
  restoreKey: document.getElementById('admin-backups-restore-key'),
  restoreKeyFile: document.getElementById('admin-backups-restore-key-file'),
  restoreError: document.getElementById('admin-backups-restore-error'),
  restoreStatus: document.getElementById('admin-backups-restore-status'),
  restoreSubmit: document.getElementById('admin-backups-restore-submit'),
};

// While a backup runs, the tab re-reads the (local, cheap) status endpoint
// this often until it finishes — only while the tab is actually on screen.
const BACKUP_POLL_MS = 2000;

let backupStatus = null;
let backupPollTimer = null;
let backupRestoreRemoteName = '';
let backupRestoreArmed = false;
let backupRestoreArmTimer = null;

function backupT(key, vars) {
  return t(`modals.adminConsole.backups.${key}`, vars);
}

// backupReason turns an error code from the server (see internal/backup's
// Code* constants and admin_backups.go's writeCodedError) into a localized,
// actionable explanation, falling back to the raw technical message.
function backupReason(code, raw) {
  if (code) {
    const key = `modals.adminConsole.backups.errors.${code}`;
    const message = t(key);
    if (message !== key) return message;
  }
  return raw || '';
}

function backupErrorMessage(err) {
  if (isNetworkError(err)) return t('common.networkError');
  return backupReason(err.code, err.message);
}

function backupLocale() {
  return window.TrakkaI18n ? TrakkaI18n.getLang() : 'fr';
}

function formatBackupDateTime(iso) {
  try {
    return new Intl.DateTimeFormat(backupLocale(), { dateStyle: 'short', timeStyle: 'short' }).format(new Date(iso));
  } catch {
    return iso;
  }
}

function formatBackupSize(bytes) {
  const n = Number(bytes) || 0;
  const [value, unit] = n >= 1024 * 1024 ? [n / (1024 * 1024), 'megabyte'] : [n / 1024, 'kilobyte'];
  try {
    return new Intl.NumberFormat(backupLocale(), { style: 'unit', unit, maximumFractionDigits: 1 }).format(value);
  } catch {
    return `${Math.round(n / 1024)} KB`;
  }
}

function showBackupMessage(el, text, tone) {
  const tones = {
    ok: 'text-emerald-600 dark:text-emerald-400',
    error: 'text-rose-600 dark:text-rose-400',
    muted: 'text-slate-500 dark:text-slate-400',
  };
  el.classList.remove(...Object.values(tones).flatMap((c) => c.split(' ')));
  if (tone) el.classList.add(...tones[tone].split(' '));
  el.textContent = text;
  el.hidden = !text;
}

// ---- Alert indicators (header dot, console button, tab) -------------------

function applyBackupAlertIndicators(alerts) {
  const has = Array.isArray(alerts) && alerts.length > 0;
  backupEls.headerDot.hidden = !has;
  backupEls.consoleAlert.hidden = !has;
  backupEls.tabAlert.hidden = !has;
}

// Called from app.js's init() once /me has resolved, and whenever the
// admin console opens: a quiet, local-only status read (it never contacts
// the WebDAV server) that lights up the indicators when something needs
// attention. Failures — including being offline — just leave them as-is.
async function refreshBackupAlertIndicators() {
  if (!state.currentUser || !state.currentUser.is_admin) {
    applyBackupAlertIndicators([]);
    return;
  }
  try {
    const st = await apiRequest('/admin/backups');
    applyBackupAlertIndicators(st.alerts);
  } catch {
    // Not worth a banner: the tab itself reports problems when opened.
  }
}

function buildBackupAlert(alert) {
  const li = document.createElement('li');
  li.className =
    'flex gap-2 rounded-xl border border-amber-300 dark:border-amber-700/70 bg-amber-50 dark:bg-amber-950/30 px-3 py-2 text-sm text-amber-800 dark:text-amber-200';
  const icon = document.createElement('span');
  icon.setAttribute('aria-hidden', 'true');
  icon.textContent = '⚠️';
  const body = document.createElement('div');
  body.className = 'min-w-0';
  const text = document.createElement('p');

  if (alert.code === 'last_backup_failed') {
    const when = new Date(alert.at);
    let date = alert.at;
    let time = '';
    try {
      date = new Intl.DateTimeFormat(backupLocale(), { day: '2-digit', month: '2-digit' }).format(when);
      time = new Intl.DateTimeFormat(backupLocale(), { hour: '2-digit', minute: '2-digit' }).format(when);
    } catch {
      // keep the raw timestamp
    }
    text.textContent = backupT('alerts.lastBackupFailed', { date, time });
    if (alert.error) {
      const detail = document.createElement('p');
      detail.className = 'mt-0.5 break-words text-xs opacity-80';
      detail.textContent = alert.error;
      body.append(text, detail);
    } else {
      body.append(text);
    }
  } else if (alert.code === 'no_recent_success') {
    text.textContent = alert.at
      ? backupT('alerts.noRecentSuccessSince', { days: alert.days, date: formatBackupDateTime(alert.at) })
      : backupT('alerts.noRecentSuccessNever', { days: alert.days });
    body.append(text);
  } else if (alert.code === 'key_not_saved') {
    text.textContent = backupT('alerts.keyNotSaved');
    body.append(text);
  } else {
    text.textContent = alert.code;
    body.append(text);
  }

  li.append(icon, body);
  return li;
}

// ---- Status ---------------------------------------------------------------

function buildBackupRunRow(run) {
  const li = document.createElement('li');
  li.className = 'rounded-lg border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 px-3 py-2 text-sm';

  const line = document.createElement('p');
  line.className = 'flex flex-wrap items-center gap-x-2 gap-y-1';
  const dot = document.createElement('span');
  dot.setAttribute('aria-hidden', 'true');
  dot.className = `h-2 w-2 shrink-0 rounded-full ${run.status === 'success' ? 'bg-emerald-500' : 'bg-rose-500'}`;
  const summary = document.createElement('span');
  summary.className = 'text-slate-800 dark:text-slate-200';
  summary.textContent = backupT('runSummary', {
    date: formatBackupDateTime(run.started_at),
    status: backupT(run.status === 'success' ? 'statusSuccess' : 'statusFailed'),
    source: backupT(run.source === 'scheduled' ? 'sourceScheduled' : 'sourceManual'),
  });
  line.append(dot, summary);
  if (run.status === 'success' && run.size_bytes) {
    const size = document.createElement('span');
    size.className = 'ml-auto text-xs text-slate-500 dark:text-slate-400';
    size.textContent = formatBackupSize(run.size_bytes);
    line.appendChild(size);
  }
  li.appendChild(line);

  if (run.error) {
    const detail = document.createElement('p');
    detail.className = `mt-1 break-words text-xs ${run.status === 'success' ? 'text-amber-700 dark:text-amber-300' : 'text-rose-600 dark:text-rose-400'}`;
    detail.textContent = run.error;
    li.appendChild(detail);
  }
  return li;
}

function renderBackupStatus(st) {
  backupStatus = st;
  applyBackupAlertIndicators(st.alerts);

  backupEls.alerts.replaceChildren(...st.alerts.map(buildBackupAlert));
  backupEls.alerts.hidden = st.alerts.length === 0;

  const badgeColors = {
    sky: 'bg-sky-500/10 text-sky-600 dark:text-sky-300',
    emerald: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-300',
    rose: 'bg-rose-500/10 text-rose-600 dark:text-rose-300',
    slate: 'bg-slate-200/60 dark:bg-slate-700/50 text-slate-600 dark:text-slate-300',
  };
  let badge = ['statusNever', 'slate'];
  if (st.running) badge = ['statusRunning', 'sky'];
  else if (st.last_run) badge = st.last_run.status === 'success' ? ['statusSuccess', 'emerald'] : ['statusFailed', 'rose'];
  backupEls.statusBadge.className = `inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${badgeColors[badge[1]]}`;
  backupEls.statusBadge.textContent = backupT(badge[0]);

  if (st.running) {
    backupEls.lastRun.textContent = st.running.kind === 'restore'
      ? backupT('runningRestore')
      : backupT('runningBackup', { time: formatBackupDateTime(st.running.since) });
  } else if (st.last_run) {
    backupEls.lastRun.textContent = `${formatBackupDateTime(st.last_run.started_at)} — ${backupT(st.last_run.status === 'success' ? 'statusSuccess' : 'statusFailed')}`;
  } else {
    backupEls.lastRun.textContent = backupT('never');
  }
  backupEls.lastSuccess.textContent = st.last_success_at ? formatBackupDateTime(st.last_success_at) : backupT('never');
  backupEls.nextRun.textContent = st.next_run_at ? formatBackupDateTime(st.next_run_at) : backupT('autoOff');

  backupEls.runButton.disabled = Boolean(st.running) || !st.config.webdav_url;
  backupEls.restoreSubmit.disabled = Boolean(st.running);

  backupEls.keyFingerprint.textContent = st.key.exists ? st.key.fingerprint : backupT('keyNone');
  backupEls.privateHint.hidden = st.allow_private_networks;

  backupEls.runsEmpty.hidden = st.runs.length > 0;
  backupEls.runsList.replaceChildren(...st.runs.map(buildBackupRunRow));

  updateBackupScheduleUI();
}

async function loadAdminBackupStatus() {
  let st;
  try {
    st = await apiRequest('/admin/backups');
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return null;
  }
  renderBackupStatus(st);
  return st;
}

function scheduleBackupPoll() {
  clearTimeout(backupPollTimer);
  backupPollTimer = setTimeout(async () => {
    // Stop quietly once the admin has moved on; the next visit reloads.
    if (adminConsoleEls.modal.hidden || adminConsoleActiveTab !== 'backups') return;
    const st = await loadAdminBackupStatus();
    if (st && st.running) {
      scheduleBackupPoll();
    } else {
      loadRemoteBackups();
    }
  }, BACKUP_POLL_MS);
}

async function openAdminBackupsTab() {
  backupEls.formStatus.hidden = true;
  backupEls.formError.hidden = true;
  backupEls.testResult.hidden = true;
  backupEls.runError.hidden = true;
  backupEls.keyStatus.hidden = true;
  const st = await loadAdminBackupStatus();
  if (!st) return;
  fillBackupForm(st.config);
  loadRemoteBackups();
  if (st.running) scheduleBackupPoll();
}

// ---- Configuration form ---------------------------------------------------

// Monday-first, as both locales write their weeks; values are Go's
// time.Weekday (0 = Sunday).
const BACKUP_WEEKDAYS = [1, 2, 3, 4, 5, 6, 0];

function fillBackupWeekdayOptions(selected) {
  const formatter = new Intl.DateTimeFormat(backupLocale(), { weekday: 'long', timeZone: 'UTC' });
  backupEls.weekday.replaceChildren(
    ...BACKUP_WEEKDAYS.map((value) => {
      const option = document.createElement('option');
      option.value = String(value);
      // 2026-01-04 was a Sunday, so day 4 + value lands on that weekday.
      option.textContent = formatter.format(new Date(Date.UTC(2026, 0, 4 + value)));
      return option;
    })
  );
  backupEls.weekday.value = String(selected);
}

function fillBackupForm(config) {
  backupEls.url.value = config.webdav_url;
  backupEls.username.value = config.webdav_username;
  backupEls.password.value = '';
  backupEls.password.placeholder = config.webdav_password_set
    ? backupT('passwordPlaceholderSet')
    : backupT('passwordPlaceholderUnset');
  backupEls.auto.checked = config.auto_enabled;
  backupEls.frequency.value = config.frequency;
  fillBackupWeekdayOptions(config.weekday);
  backupEls.time.value = config.time;
  backupEls.retention.value = String(config.retention);
  updateBackupScheduleUI();
}

function updateBackupScheduleUI() {
  const frequency = backupEls.frequency.value;
  backupEls.weekdayField.hidden = frequency !== 'weekly';
  const zone = (backupStatus && backupStatus.time_zone) || 'UTC';
  const [hh, mm] = (backupEls.time.value || '03:00').split(':');
  const hour = Number(hh) || 0;
  const pad = (n) => String(n).padStart(2, '0');
  if (frequency === 'every_12h') {
    backupEls.scheduleHint.textContent = backupT('scheduleHintEvery12h', {
      time1: `${pad(hour % 12)}:${mm}`,
      time2: `${pad((hour % 12) + 12)}:${mm}`,
      zone,
    });
  } else if (frequency === 'weekly') {
    const selected = backupEls.weekday.options[backupEls.weekday.selectedIndex];
    backupEls.scheduleHint.textContent = backupT('scheduleHintWeekly', {
      weekday: selected ? selected.textContent : '',
      time: backupEls.time.value,
      zone,
    });
  } else {
    backupEls.scheduleHint.textContent = backupT('scheduleHintDaily', { time: backupEls.time.value, zone });
  }
}

backupEls.frequency.addEventListener('change', updateBackupScheduleUI);
backupEls.weekday.addEventListener('change', updateBackupScheduleUI);
backupEls.time.addEventListener('input', updateBackupScheduleUI);

backupEls.form.addEventListener('submit', async (event) => {
  event.preventDefault();
  backupEls.formError.hidden = true;
  backupEls.formStatus.hidden = true;

  const body = {
    webdav_url: backupEls.url.value.trim(),
    webdav_username: backupEls.username.value.trim(),
    auto_enabled: backupEls.auto.checked,
    frequency: backupEls.frequency.value,
    time: backupEls.time.value,
    weekday: Number(backupEls.weekday.value) || 0,
    retention: Number.parseInt(backupEls.retention.value, 10) || 0,
  };
  // Write-only, like the OIDC client secret: an empty field keeps the
  // stored password.
  if (backupEls.password.value) body.webdav_password = backupEls.password.value;

  let st;
  try {
    st = await apiRequest('/admin/backups/config', { method: 'PUT', body: JSON.stringify(body) });
  } catch (err) {
    showBackupMessage(backupEls.formError, backupErrorMessage(err), 'error');
    return;
  }
  renderBackupStatus(st);
  fillBackupForm(st.config);
  showBackupMessage(backupEls.formStatus, backupT('saved'), 'ok');
  loadRemoteBackups();
});

backupEls.testButton.addEventListener('click', async () => {
  backupEls.testButton.disabled = true;
  showBackupMessage(backupEls.testResult, backupT('testRunning'), 'muted');
  let res;
  try {
    res = await apiRequest('/admin/backups/test', {
      method: 'POST',
      body: JSON.stringify({
        webdav_url: backupEls.url.value.trim(),
        webdav_username: backupEls.username.value.trim(),
        // Empty means "test with the stored password".
        webdav_password: backupEls.password.value,
      }),
    });
  } catch (err) {
    showBackupMessage(backupEls.testResult, backupT('testFailed', { reason: backupErrorMessage(err) }), 'error');
    return;
  } finally {
    backupEls.testButton.disabled = false;
  }
  if (res.ok) {
    showBackupMessage(backupEls.testResult, backupT('testOk', { count: res.backup_count }), 'ok');
  } else {
    showBackupMessage(backupEls.testResult, backupT('testFailed', { reason: backupReason(res.code, res.error) }), 'error');
  }
});

// ---- Manual backup ----------------------------------------------------------

backupEls.runButton.addEventListener('click', async () => {
  backupEls.runError.hidden = true;
  backupEls.runButton.disabled = true;
  let st;
  try {
    st = await apiRequest('/admin/backups/run', { method: 'POST' });
  } catch (err) {
    showBackupMessage(backupEls.runError, backupErrorMessage(err), 'error');
    backupEls.runButton.disabled = false;
    return;
  }
  renderBackupStatus(st);
  scheduleBackupPoll();
});

// ---- Encryption key -----------------------------------------------------------

async function exportBackupKey() {
  backupEls.keyStatus.hidden = true;
  try {
    return await apiRequest('/admin/backups/key/export', { method: 'POST' });
  } catch (err) {
    showBackupMessage(backupEls.keyStatus, backupErrorMessage(err), 'error');
    return null;
  }
}

backupEls.keyDownload.addEventListener('click', async () => {
  const exported = await exportBackupKey();
  if (!exported) return;
  // A Blob + object URL rather than a navigation to an API route, so the
  // key never sits in a URL, the browser history, or a server access log.
  const blobUrl = URL.createObjectURL(new Blob([exported.file_content], { type: 'text/plain' }));
  const link = document.createElement('a');
  link.href = blobUrl;
  link.download = exported.file_name;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(blobUrl), 1000);
  showBackupMessage(backupEls.keyStatus, backupT('keyDownloaded'), 'ok');
  loadAdminBackupStatus();
});

backupEls.keyCopy.addEventListener('click', async () => {
  const exported = await exportBackupKey();
  if (!exported) return;
  try {
    await navigator.clipboard.writeText(exported.key);
    showBackupMessage(backupEls.keyStatus, backupT('keyCopied'), 'ok');
  } catch {
    showBackupMessage(backupEls.keyStatus, backupT('keyCopyFailed'), 'error');
  }
  loadAdminBackupStatus();
});

// ---- Remote backups -------------------------------------------------------------

// Backup file names embed their UTC creation time
// (trakka-backup-20260929T030000Z.tkb, see internal/backup.backupFileName).
function backupNameToDate(name) {
  const m = /^trakka-backup-(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z\.tkb$/.exec(name);
  if (!m) return null;
  return new Date(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]));
}

function buildRemoteBackupRow(file) {
  const li = document.createElement('li');
  li.className =
    'flex items-center justify-between gap-2 rounded-lg border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 px-3 py-2';

  const info = document.createElement('div');
  info.className = 'min-w-0';
  const title = document.createElement('p');
  title.className = 'text-sm text-slate-800 dark:text-slate-200';
  const date = backupNameToDate(file.name);
  title.textContent = [date ? formatBackupDateTime(date.toISOString()) : file.name, formatBackupSize(file.size)].join(' · ');
  const name = document.createElement('p');
  name.className = 'truncate font-mono text-xs text-slate-500 dark:text-slate-400';
  name.textContent = file.name;
  info.append(title, name);

  const restoreButton = document.createElement('button');
  restoreButton.type = 'button';
  restoreButton.className =
    'min-h-[36px] shrink-0 rounded-lg border border-slate-200 dark:border-slate-700 bg-slate-100 dark:bg-slate-800/60 px-3 py-1.5 text-xs font-medium text-slate-600 dark:text-slate-300 hover:bg-slate-200 dark:hover:bg-slate-800';
  restoreButton.textContent = backupT('remoteRestore');
  restoreButton.setAttribute('aria-label', backupT('remoteRestoreAriaLabel', { name: file.name }));
  restoreButton.addEventListener('click', () => selectRemoteBackupForRestore(file.name));

  li.append(info, restoreButton);
  return li;
}

async function loadRemoteBackups() {
  backupEls.remoteList.replaceChildren();
  if (!backupStatus || !backupStatus.config.webdav_url) {
    showBackupMessage(backupEls.remoteMessage, backupT('remoteNotConfigured'), 'muted');
    return;
  }
  showBackupMessage(backupEls.remoteMessage, backupT('remoteLoading'), 'muted');
  let res;
  try {
    res = await apiRequest('/admin/backups/remote');
  } catch (err) {
    showBackupMessage(backupEls.remoteMessage, backupT('remoteError', { reason: backupErrorMessage(err) }), 'error');
    return;
  }
  if (res.error) {
    showBackupMessage(backupEls.remoteMessage, backupT('remoteError', { reason: backupReason(res.code, res.error) }), 'error');
    return;
  }
  if (res.files.length === 0) {
    showBackupMessage(backupEls.remoteMessage, backupT('remoteEmpty'), 'muted');
    return;
  }
  backupEls.remoteMessage.hidden = true;
  backupEls.remoteList.replaceChildren(...res.files.map(buildRemoteBackupRow));
}

backupEls.remoteRefresh.addEventListener('click', loadRemoteBackups);

// ---- Restore ------------------------------------------------------------------

function selectRemoteBackupForRestore(name) {
  backupRestoreRemoteName = name;
  backupEls.restoreRemoteName.textContent = name;
  backupEls.restoreRemote.hidden = false;
  backupEls.restoreUpload.hidden = true;
  backupEls.restoreFile.value = '';
  backupEls.restoreError.hidden = true;
  backupEls.restoreSection.scrollIntoView({ behavior: 'smooth', block: 'start' });
  backupEls.restoreKey.focus({ preventScroll: true });
}

function clearRemoteBackupForRestore() {
  backupRestoreRemoteName = '';
  backupEls.restoreRemote.hidden = true;
  backupEls.restoreUpload.hidden = false;
}

backupEls.restoreRemoteClear.addEventListener('click', clearRemoteBackupForRestore);

// The same "click again to confirm" pattern as buildConfirmableDeleteButton
// above, rather than a native confirm() popup: replacing every account's
// data needs a deliberate second action.
function disarmBackupRestore() {
  backupRestoreArmed = false;
  clearTimeout(backupRestoreArmTimer);
  backupEls.restoreSubmit.textContent = backupT('restoreSubmit');
}

backupEls.restoreForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  backupEls.restoreError.hidden = true;
  backupEls.restoreStatus.hidden = true;

  const file = backupEls.restoreFile.files[0];
  if (!backupRestoreRemoteName && !file) {
    showBackupMessage(backupEls.restoreError, backupT('restoreNoSource'), 'error');
    return;
  }
  if (!backupRestoreArmed) {
    backupRestoreArmed = true;
    backupEls.restoreSubmit.textContent = backupT('restoreConfirm');
    backupRestoreArmTimer = setTimeout(disarmBackupRestore, ADMIN_CONFIRM_WINDOW_MS);
    return;
  }
  disarmBackupRestore();

  const form = new FormData();
  const keyText = backupEls.restoreKey.value.trim();
  if (keyText) form.append('key', keyText);
  const keyFile = backupEls.restoreKeyFile.files[0];
  if (keyFile) form.append('key_file', keyFile);
  if (backupRestoreRemoteName) form.append('remote_name', backupRestoreRemoteName);
  else form.append('file', file);

  backupEls.restoreSubmit.disabled = true;
  backupEls.runButton.disabled = true;
  showBackupMessage(backupEls.restoreStatus, backupT('restoreRunning'), 'muted');

  let result;
  try {
    // headers: {} replaces apiRequest's JSON Content-Type, so the browser
    // sets multipart/form-data with its own boundary.
    result = await apiRequest('/admin/backups/restore', { method: 'POST', body: form, headers: {} });
  } catch (err) {
    backupEls.restoreStatus.hidden = true;
    showBackupMessage(backupEls.restoreError, backupErrorMessage(err), 'error');
    backupEls.restoreSubmit.disabled = false;
    loadAdminBackupStatus();
    return;
  }

  backupEls.restoreKey.value = '';
  backupEls.restoreKeyFile.value = '';
  showBackupMessage(backupEls.restoreStatus, backupT(result.key_adopted ? 'restoreDoneKeyAdopted' : 'restoreDone'), 'ok');
  // Every session is now whatever the restored database held, so a full
  // reload is the only safe next step: either this session survived and
  // the app reloads the restored data, or /me answers 401 and apiRequest
  // sends the admin to the sign-in page.
  setTimeout(() => {
    window.location.href = '/';
  }, 3000);
});
