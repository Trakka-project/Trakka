'use strict';

// User "Paramètres" panel: a header button, visible to every signed-in user
// (unlike the admin-only "Paramètres du Système" panel in admin.js), opening
// a modal that reads/writes GET+PATCH /api/v1/me, plus the Appearance
// (theme) and Language pickers that used to live as their own header
// dropdowns (static/js/theme.js/i18n.js still own the underlying
// get/set/apply logic — window.TrakkaTheme/window.TrakkaI18n — this file
// just wires the two <select> elements that now live in this modal instead;
// see CLAUDE.md's session-handoff log for the header-cleanup session that
// moved them here). keep_last_page is the "reopen on the last tab/list
// visited" preference, whose actual save/restore machinery
// (LAST_VIEW_STORAGE_KEY, isKeepLastPageEnabled,
// saveLastView/loadLastView/restoreLastView) lives in app.js, hooked from
// list_view.js's selectList/showDashboard and planning.js's setActiveTab —
// this file only owns the toggle's UI and persisting it to the user's
// profile. Shares `state`, `apiRequest`, `showError`/`hideError`, `t`,
// `isKeepLastPageEnabled`, `setKeepLastPagePreference` with
// app.js/list_view.js/planning.js/admin.js — same classic-<script>-tags
// shared-scope pattern as those files.
//
// Like the notifications bell and the admin panel, this is a header-level
// control rather than a dashboard tab, so it has its own open/close wiring
// here instead of going through refreshVisibleView.

const userSettingsEls = {
  button: document.getElementById('user-settings-button'),
  modal: document.getElementById('user-settings-modal'),
  closeButton: document.getElementById('close-user-settings-modal-button'),
  themeSelect: document.getElementById('user-settings-theme'),
  languageSelect: document.getElementById('user-settings-language'),
  form: document.getElementById('user-settings-form'),
  keepLastPage: document.getElementById('user-settings-keep-last-page'),
  status: document.getElementById('user-settings-status'),
};

function openUserSettingsModal() {
  userSettingsEls.status.hidden = true;
  // TrakkaTheme/TrakkaI18n are defined in theme.js/i18n.js (loaded before
  // this file) — re-read every time the modal opens, the same "don't trust
  // a cached value" reasoning refreshPushToggleUI below already follows,
  // since either can also change from outside this modal (the OS-level
  // "Auto" theme, or a stale in-memory currentLang before /me's own
  // reconciliation in app.js's init() has resolved).
  userSettingsEls.themeSelect.value = window.TrakkaTheme ? TrakkaTheme.get() : 'auto';
  userSettingsEls.languageSelect.value = window.TrakkaI18n ? TrakkaI18n.getLang() : 'fr';
  // isKeepLastPageEnabled is defined in app.js — prefers state.currentUser's
  // server value, falling back to the localStorage mirror if /me hasn't
  // resolved yet (e.g. opened while offline).
  userSettingsEls.keepLastPage.checked = isKeepLastPageEnabled();
  // refreshPushToggleUI is defined in push.js — re-checked every time the
  // modal opens (not just cached from an earlier check) since notification
  // permission/subscription state can change outside the app at any time,
  // most notably the user revoking it via the browser's own site settings.
  refreshPushToggleUI();
  // refreshAdminConsoleButtonVisibility is defined in admin.js (loaded
  // after this file — see index.html's script order and install-help.js's
  // own doc comment on why that's safe: this call only ever runs later, on
  // click, by which point every deferred script has already run).
  refreshAdminConsoleButtonVisibility();
  userSettingsEls.modal.hidden = false;
  document.body.classList.add('overflow-hidden');
}

function closeUserSettingsModal() {
  userSettingsEls.modal.hidden = true;
  document.body.classList.remove('overflow-hidden');
}

userSettingsEls.button.addEventListener('click', openUserSettingsModal);
userSettingsEls.closeButton.addEventListener('click', closeUserSettingsModal);
userSettingsEls.modal.addEventListener('click', (event) => {
  if (event.target === userSettingsEls.modal) closeUserSettingsModal();
});
document.addEventListener('keydown', (event) => {
  // install-help.js's and admin.js's modals can each open on top of this
  // one (from buttons added to the form below) — when either is the one
  // currently visible, let its own Escape handler close just that one
  // instead of both at once (same pattern as app.js/spaces.js's new-list/
  // category modal pair).
  if (event.key === 'Escape' && !userSettingsEls.modal.hidden && installHelpEls.modal.hidden && adminConsoleEls.modal.hidden) {
    closeUserSettingsModal();
  }
});

userSettingsEls.form.addEventListener('submit', async (event) => {
  event.preventDefault();
  hideError();
  userSettingsEls.status.hidden = true;

  const keepLastPage = userSettingsEls.keepLastPage.checked;
  let user;
  try {
    user = await apiRequest('/me', { method: 'PATCH', body: JSON.stringify({ keep_last_page: keepLastPage }) });
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return;
  }

  state.currentUser = user;
  // setKeepLastPagePreference is defined in app.js — keeps the localStorage
  // mirror in step immediately, rather than waiting for the next reload's
  // /me call to do it.
  setKeepLastPagePreference(user.keep_last_page);
  userSettingsEls.status.textContent = t('modals.userSettings.saved');
  userSettingsEls.status.hidden = false;
});

// Theme and language apply the instant they're picked (like the header
// dropdowns they replace), rather than waiting for the "Enregistrer" button
// below — that button only ever governed keep_last_page.

userSettingsEls.themeSelect.addEventListener('change', () => {
  // TrakkaTheme.set is purely client-side (localStorage only, see
  // theme.js) — there is no server round-trip to fail here.
  TrakkaTheme.set(userSettingsEls.themeSelect.value);
});

userSettingsEls.languageSelect.addEventListener('change', async () => {
  hideError();
  const lang = userSettingsEls.languageSelect.value;
  // Apply immediately — TrakkaI18n.setLang works from the already-fetched
  // dictionary/localStorage regardless of network state, so the interface
  // re-renders in the new language even if the PATCH below can't reach the
  // server right now (e.g. offline).
  await TrakkaI18n.setLang(lang);
  try {
    const user = await apiRequest('/me', { method: 'PATCH', body: JSON.stringify({ language: lang }) });
    // A PATCH queued offline by the service worker comes back as a bare
    // {queued: true} placeholder rather than the real user object (see
    // sw.js's queueOfflineWrite) — only adopt the response when it's
    // genuinely the updated profile, so state.currentUser is never
    // overwritten with that placeholder.
    if (user && typeof user.language === 'string') {
      state.currentUser = user;
    }
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
  }
});
