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
//
// This modal also surfaces the app's own update/version status: a
// "Mises à jour" block shows the currently active service worker's cache
// version (getAppVersion, in app.js) and a manual "Vérifier les mises à
// jour" button (manualCheckForUpdate, also in app.js) that forces a
// registration.update() on demand rather than waiting for the
// visibility/focus/interval-driven checks already wired up there.

const userSettingsEls = {
  button: document.getElementById('user-settings-button'),
  modal: document.getElementById('user-settings-modal'),
  closeButton: document.getElementById('close-user-settings-modal-button'),
  body: document.getElementById('user-settings-body'),
  footer: document.getElementById('user-settings-footer'),
  tabList: document.getElementById('user-settings-tabs'),
  tabButtons: {
    general: document.getElementById('user-settings-tab-general'),
    notifications: document.getElementById('user-settings-tab-notifications'),
    prices: document.getElementById('user-settings-tab-prices'),
    calendar: document.getElementById('user-settings-tab-calendar'),
  },
  panels: {
    general: document.getElementById('user-settings-panel-general'),
    notifications: document.getElementById('user-settings-panel-notifications'),
    prices: document.getElementById('user-settings-panel-prices'),
    calendar: document.getElementById('user-settings-panel-calendar'),
  },
  themeSelect: document.getElementById('user-settings-theme'),
  languageSelect: document.getElementById('user-settings-language'),
  form: document.getElementById('user-settings-form'),
  keepLastPage: document.getElementById('user-settings-keep-last-page'),
  vibrate: document.getElementById('user-settings-vibrate'),
  remindersEnabled: document.getElementById('user-settings-reminders-enabled'),
  overdueSummary: document.getElementById('user-settings-overdue-summary'),
  overdueSummaryTimeRow: document.getElementById('user-settings-overdue-summary-time-row'),
  overdueSummaryTime: document.getElementById('user-settings-overdue-summary-time'),
  collaboratorActions: document.getElementById('user-settings-collaborator-actions'),
  itemAdditions: document.getElementById('user-settings-item-additions'),
  listSharing: document.getElementById('user-settings-list-sharing'),
  priceDropAlerts: document.getElementById('user-settings-price-drop-alerts'),
  priceIncreaseAlerts: document.getElementById('user-settings-price-increase-alerts'),
  priceAlertsDisabled: document.getElementById('user-settings-price-alerts-disabled'),
  priceIndicators: document.getElementById('user-settings-price-indicators'),
  reminderPreset: document.getElementById('user-settings-reminder-preset'),
  reminderOffset: document.getElementById('user-settings-reminder-offset'),
  reminderOffsetSuffix: document.getElementById('user-settings-reminder-offset-suffix'),
  reminderTime: document.getElementById('user-settings-reminder-time'),
  reminderFallbackHint: document.getElementById('user-settings-reminder-fallback-hint'),
  status: document.getElementById('user-settings-status'),
  updateVersion: document.getElementById('user-settings-update-version'),
  updateCheckButton: document.getElementById('user-settings-update-check-button'),
  installHelpButton: document.getElementById('install-help-button'),
  androidAppSection: document.getElementById('user-settings-android-app'),
  androidAppServer: document.getElementById('user-settings-android-app-server'),
  androidAppVersion: document.getElementById('user-settings-android-app-version'),
  androidAppChangeButton: document.getElementById('user-settings-android-app-change-button'),
};

// The modal's categories: one panel shown at a time under the fixed header
// and tab bar, so only #user-settings-body ever scrolls. Same
// aria-selected-driven styling as admin.js's setAdminConsoleTab (base.css).
// Every panel stays inside the one form, so "Enregistrer" (pinned in the
// footer) saves the fields of all three form-backed tabs at once; the
// calendar tab has nothing to save, so the footer is hidden there.
function setUserSettingsTab(tab) {
  for (const [name, button] of Object.entries(userSettingsEls.tabButtons)) {
    const active = name === tab;
    button.setAttribute('aria-selected', String(active));
    userSettingsEls.panels[name].hidden = !active;
  }
  userSettingsEls.footer.hidden = tab === 'calendar';
  userSettingsEls.body.scrollTop = 0;
}

for (const [name, button] of Object.entries(userSettingsEls.tabButtons)) {
  button.addEventListener('click', () => setUserSettingsTab(name));
}

// Arrow keys move between tabs, as on any role="tablist".
userSettingsEls.tabList.addEventListener('keydown', (event) => {
  const names = Object.keys(userSettingsEls.tabButtons);
  const current = names.findIndex((name) => userSettingsEls.tabButtons[name] === document.activeElement);
  if (current === -1) return;
  let next;
  if (event.key === 'ArrowRight') next = (current + 1) % names.length;
  else if (event.key === 'ArrowLeft') next = (current - 1 + names.length) % names.length;
  else if (event.key === 'Home') next = 0;
  else if (event.key === 'End') next = names.length - 1;
  else return;
  event.preventDefault();
  setUserSettingsTab(names[next]);
  userSettingsEls.tabButtons[names[next]].focus();
});

// base.css's touch-action/overscroll rules only hold while
// #user-settings-body actually overflows. On a short tab (Calendrier) it has
// nothing to scroll, so a vertical drag on it, or on the empty space under
// its content, skips it and pans the next scrollable thing up: the page
// behind, which <body>'s overflow-hidden doesn't stop on iOS WebKit or the
// Android WebView. The modal covers the whole viewport, so cancelling every
// touchmove the panel can't absorb in its own direction leaves the page
// nothing to react to. Horizontal and multi-finger moves are left alone
// (touch-action already rules out a horizontal pan; pinch-zoom stays).
let userSettingsTouchY = 0;
userSettingsEls.modal.addEventListener('touchstart', (event) => {
  if (event.touches.length === 1) userSettingsTouchY = event.touches[0].clientY;
}, { passive: true });
userSettingsEls.modal.addEventListener('touchmove', (event) => {
  if (event.touches.length !== 1) return;
  const y = event.touches[0].clientY;
  const dy = y - userSettingsTouchY;
  userSettingsTouchY = y;
  if (dy === 0) return;
  const body = userSettingsEls.body;
  if (body.contains(event.target)) {
    const canScrollUp = body.scrollTop > 0;
    const canScrollDown = Math.ceil(body.scrollTop + body.clientHeight) < body.scrollHeight;
    if ((dy > 0 && canScrollUp) || (dy < 0 && canScrollDown)) return;
  }
  if (event.cancelable) event.preventDefault();
}, { passive: false });

// A field failing the browser's own validation on submit may sit on a tab
// that isn't shown: switch to it first, or the browser can't focus the
// field to show its message and the save silently does nothing.
userSettingsEls.form.addEventListener('invalid', (event) => {
  const panel = event.target.closest('[role="tabpanel"]');
  if (panel && panel.hidden) {
    const tab = Object.keys(userSettingsEls.panels).find((name) => userSettingsEls.panels[name] === panel);
    setUserSettingsTab(tab);
  }
}, true);

function openUserSettingsModal() {
  userSettingsEls.status.hidden = true;
  setUserSettingsTab('general');
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
  // vibrate_on_notification only matters server-side (internal/handlers'
  // sendToUsers decides per device whether a push carries a vibration
  // pattern), so there's no localStorage mirror: before /me resolves this
  // shows the column's own default, on.
  userSettingsEls.vibrate.checked = state.currentUser ? state.currentUser.vibrate_on_notification !== false : true;
  // "Types de notifications": account-wide, so like the vibration they come
  // from the server only, falling back to the columns' own defaults (all on
  // but the overdue summary, at 08:00) before /me resolves.
  const wants = (field) => (state.currentUser ? state.currentUser[field] !== false : true);
  userSettingsEls.remindersEnabled.checked = wants('reminders_enabled');
  userSettingsEls.collaboratorActions.checked = wants('collaborator_actions_enabled');
  userSettingsEls.itemAdditions.checked = wants('item_additions_enabled');
  userSettingsEls.listSharing.checked = wants('list_sharing_enabled');
  userSettingsEls.overdueSummary.checked = Boolean(state.currentUser && state.currentUser.overdue_tasks_summary_enabled);
  // "Alertes de prix": the same server-only, column-default fallback — drops
  // and the visual indicators on, increases off, the master switch not
  // engaged ("Désactiver" shows the inverse of price_alerts_enabled).
  userSettingsEls.priceDropAlerts.checked = wants('price_drop_alerts_enabled');
  userSettingsEls.priceIncreaseAlerts.checked = Boolean(state.currentUser && state.currentUser.price_increase_alerts_enabled);
  userSettingsEls.priceAlertsDisabled.checked = !wants('price_alerts_enabled');
  userSettingsEls.priceIndicators.checked = wants('price_change_indicators_enabled');
  updatePriceAlertSwitchesState();
  userSettingsEls.overdueSummaryTime.value = (state.currentUser && state.currentUser.overdue_tasks_summary_time) || '08:00';
  updateOverdueSummaryTimeVisibility();
  // state.currentUser's own reminder_default_offset_days/_time/_at_due_time
  // (from GET/PATCH /api/v1/me) drive the preset select (see
  // reminderDefaultsToPreset), falling back to the "Le jour même" defaults
  // if /me hasn't resolved yet (e.g. opened while offline).
  const currentDefault = (state.currentUser && state.currentUser.reminder_default_offset_days != null)
    ? {
        offsetDays: state.currentUser.reminder_default_offset_days,
        time: state.currentUser.reminder_default_time || '09:00',
        atDueTime: Boolean(state.currentUser.reminder_default_at_due_time),
      }
    : { offsetDays: 0, time: '09:00', atDueTime: false };
  userSettingsEls.reminderPreset.value = reminderDefaultsToPreset(currentDefault);
  userSettingsEls.reminderOffset.value = currentDefault.offsetDays;
  userSettingsEls.reminderTime.value = currentDefault.time;
  updateReminderOffsetVisibility();
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
  // refreshUpdateStatusUI is defined below — re-read every time the modal
  // opens for the same "don't trust a cached value" reason as the push
  // toggle above, though in practice the version only ever changes once a
  // deployed update actually takes over (see getAppVersion's own comment).
  refreshUpdateStatusUI();
  refreshAndroidAppSection();
  // refreshCalendarFeedSection is defined in calendar-feed.js (loaded after
  // this file, called only on click like refreshAdminConsoleButtonVisibility
  // above): whether a feed link is active, and when an app last used it.
  refreshCalendarFeedSection();
  userSettingsEls.modal.hidden = false;
  // setUserSettingsTab's own reset above ran while the modal was still
  // display:none, where scrollTop can't be set: redo it now it's shown.
  userSettingsEls.body.scrollTop = 0;
  TrakkaScrollLock.lock();
}

// getAppVersion is defined in app.js.
async function refreshUpdateStatusUI() {
  const version = await getAppVersion();
  userSettingsEls.updateVersion.textContent = version
    ? t('modals.userSettings.updateVersionKnown', { version })
    : t('modals.userSettings.updateVersionUnknown');
}

// Inside Trakka's Android app (android/, docs/MOBILE_BUILD.md), the app's native TrakkaApp
// plugin is reachable from this page, through the bridge the app's WebView injects: show which
// server the app is connected to, with a way to change it. In a browser there is no such plugin,
// and the section stays hidden.
function androidAppPlugin() {
  const plugins = window.Capacitor && window.Capacitor.Plugins;
  return (plugins && plugins.TrakkaApp) || null;
}

async function refreshAndroidAppSection() {
  const plugin = androidAppPlugin();
  if (!plugin) return;
  // The app is already installed: the browser installation help is beside the point.
  userSettingsEls.installHelpButton.hidden = true;
  userSettingsEls.androidAppSection.hidden = false;
  try {
    const { server, appVersion } = await plugin.getServer();
    userSettingsEls.androidAppServer.textContent = server || '';
    userSettingsEls.androidAppVersion.textContent = appVersion
      ? t('modals.userSettings.androidAppVersion', { version: appVersion })
      : '';
  } catch {
    // The bridge to the app failed: leave the section as it was.
  }
}

// The app then replaces this page with its own connect screen, which can come back here.
userSettingsEls.androidAppChangeButton.addEventListener('click', () => {
  const plugin = androidAppPlugin();
  if (plugin) plugin.changeServer();
});

function closeUserSettingsModal() {
  userSettingsEls.modal.hidden = true;
  TrakkaScrollLock.unlock();
}

// reminderDefaultsToPreset derives which named preset ("Le jour même"/
// "La veille"/"À l'heure exacte de l'échéance"/"Personnalisé") to show for
// a resolved default. Unlike list_view.js's per-item reminderItemToSelection,
// the time stays editable under every preset here, so only the offset (and
// the at-due-time flag) decides: "Le jour même à 08:00" is still "Le jour
// même". Kept as its own small copy rather than shared, since list_view.js
// isn't loaded on every page this modal could in principle appear on.
function reminderDefaultsToPreset({ offsetDays, atDueTime }) {
  if (atDueTime) return 'at_due_time';
  if (offsetDays === 0) return 'same_day';
  if (offsetDays === 1) return 'day_before';
  return 'custom';
}

// updateReminderOffsetVisibility shows the "N jour(s) avant à" number input
// only for the "Personnalisé" preset — "Le jour même"/"la veille" fix the
// offset implicitly (0/1) with nothing further to enter, matching how
// list_view.js's own per-item reminder select only reveals its offset input
// for "custom". "À l'heure exacte de l'échéance" keeps the time input as
// the fallback for a task without a due time, and says so.
function updateReminderOffsetVisibility() {
  const preset = userSettingsEls.reminderPreset.value;
  const isCustom = preset === 'custom';
  userSettingsEls.reminderOffset.hidden = !isCustom;
  userSettingsEls.reminderOffsetSuffix.hidden = !isCustom;
  userSettingsEls.reminderFallbackHint.hidden = preset !== 'at_due_time';
}

// The summary's time of day only matters while the summary is on.
function updateOverdueSummaryTimeVisibility() {
  userSettingsEls.overdueSummaryTimeRow.hidden = !userSettingsEls.overdueSummary.checked;
}

userSettingsEls.overdueSummary.addEventListener('change', updateOverdueSummaryTimeVisibility);

// "Désactiver les alertes de prix" overrides both directions: while it is
// on they are greyed out (and left as they were, so turning alerts back on
// restores the previous choice).
function updatePriceAlertSwitchesState() {
  const disabled = userSettingsEls.priceAlertsDisabled.checked;
  for (const input of [userSettingsEls.priceDropAlerts, userSettingsEls.priceIncreaseAlerts]) {
    input.disabled = disabled;
    input.closest('label').classList.toggle('cursor-pointer', !disabled);
    input.closest('label').classList.toggle('opacity-60', disabled);
  }
}

userSettingsEls.priceAlertsDisabled.addEventListener('change', updatePriceAlertSwitchesState);

userSettingsEls.reminderPreset.addEventListener('change', () => {
  updateReminderOffsetVisibility();
  // Picking "Le jour même"/"la veille" fills in its named default (0 at
  // 09:00 / 1 at 20:00, per this feature's own spec) — the time input stays
  // visible and freely editable afterward (unlike list_view.js's per-item
  // reminder select, where these two presets are fixed with no further
  // input at all), so this is only the starting suggestion, not a lock.
  if (userSettingsEls.reminderPreset.value === 'same_day') {
    userSettingsEls.reminderOffset.value = 0;
    userSettingsEls.reminderTime.value = '09:00';
  } else if (userSettingsEls.reminderPreset.value === 'day_before') {
    userSettingsEls.reminderOffset.value = 1;
    userSettingsEls.reminderTime.value = '20:00';
  } else if (userSettingsEls.reminderPreset.value === 'at_due_time') {
    // The fallback for a task without a due time: the same day.
    userSettingsEls.reminderOffset.value = 0;
  }
});

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
  const vibrateOnNotification = userSettingsEls.vibrate.checked;
  // Read straight from the offset/time inputs' current values, not the
  // preset select — "Personnalisé" has no other source, and for
  // "same_day"/"day_before" the change listener above already filled them
  // in with that preset's own fixed values, so this is always what's
  // actually shown on screen.
  const reminderDefaultOffsetDays = Math.max(0, Number.parseInt(userSettingsEls.reminderOffset.value, 10) || 0);
  const reminderDefaultTime = userSettingsEls.reminderTime.value || '09:00';
  const reminderDefaultAtDueTime = userSettingsEls.reminderPreset.value === 'at_due_time';
  const payload = {
    keep_last_page: keepLastPage,
    vibrate_on_notification: vibrateOnNotification,
    reminders_enabled: userSettingsEls.remindersEnabled.checked,
    overdue_tasks_summary_enabled: userSettingsEls.overdueSummary.checked,
    overdue_tasks_summary_time: userSettingsEls.overdueSummaryTime.value || '08:00',
    collaborator_actions_enabled: userSettingsEls.collaboratorActions.checked,
    item_additions_enabled: userSettingsEls.itemAdditions.checked,
    list_sharing_enabled: userSettingsEls.listSharing.checked,
    price_alerts_enabled: !userSettingsEls.priceAlertsDisabled.checked,
    price_drop_alerts_enabled: userSettingsEls.priceDropAlerts.checked,
    price_increase_alerts_enabled: userSettingsEls.priceIncreaseAlerts.checked,
    price_change_indicators_enabled: userSettingsEls.priceIndicators.checked,
    reminder_default_offset_days: reminderDefaultOffsetDays,
    reminder_default_time: reminderDefaultTime,
    reminder_default_at_due_time: reminderDefaultAtDueTime,
  };
  let user;
  try {
    user = await apiRequest('/me', { method: 'PATCH', body: JSON.stringify(payload) });
  } catch (err) {
    if (!isNetworkError(err)) showError(err.message);
    return;
  }

  // Queued offline, the PATCH comes back as sw.js's {queued: true}
  // placeholder rather than the profile (see the language picker below): keep
  // the profile, with what was just saved, so the next opening of this modal
  // and local-reminders.js see the new preferences until the server's copy
  // replaces it.
  if (user && typeof user.id === 'number') {
    state.currentUser = user;
  } else if (state.currentUser) {
    state.currentUser = { ...state.currentUser, ...payload };
  }
  // setKeepLastPagePreference is defined in app.js — keeps the localStorage
  // mirror in step immediately, rather than waiting for the next reload's
  // /me call to do it.
  setKeepLastPagePreference(keepLastPage);
  // The visual price indicators follow state.currentUser at render time
  // (recentPriceMovement in list_view.js): re-render whatever is on screen
  // so turning them on or off shows right away. refreshVisibleView is
  // defined in app.js.
  refreshVisibleView();
  // Which price alerts the 🔔 inbox shows follows these same preferences
  // (filtered server-side when read): reload it. refreshNotifications is
  // defined in notifications.js.
  refreshNotifications();
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

// manualCheckForUpdate is defined in app.js. Unlike the theme/language
// selects above, this doesn't touch /api/v1/me at all — it's purely a
// service-worker/cache concern, so there's no isNetworkError-guarded
// showError path here, just a toast either way (TrakkaUndo.js's
// TrakkaToast, shared with the rest of the app).
userSettingsEls.updateCheckButton.addEventListener('click', async () => {
  userSettingsEls.updateCheckButton.disabled = true;
  try {
    const outcome = await manualCheckForUpdate();
    if (outcome === 'updated') {
      // Close the settings modal so #update-banner — which
      // watchForServiceWorkerUpdate's own listener will show once the new
      // worker reaches 'installed' — is actually visible right away instead
      // of sitting behind this modal's overlay.
      closeUserSettingsModal();
      return;
    }
    const toastKey = {
      'up-to-date': 'modals.userSettings.updateCheckUpToDate',
      unsupported: 'modals.userSettings.updateCheckUnsupported',
      error: 'modals.userSettings.updateCheckError',
    }[outcome];
    if (toastKey) TrakkaToast.success(t(toastKey));
  } finally {
    userSettingsEls.updateCheckButton.disabled = false;
  }
});
