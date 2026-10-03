'use strict';

// "Export de calendrier" section of #user-settings-modal: generates,
// regenerates and disables the user's personal calendar feed link through
// GET/POST/DELETE /api/v1/calendar/feed-token (internal/handlers/
// calendar_feed.go, docs/CALENDAR_EXPORT.md). Calendar apps then poll
// /api/v1/calendar/feed.ics?token=... on their own, with no session.
//
// The server stores only a hash of the token, so the link can be shown
// exactly once: in the POST response, right after it is generated. Reopening
// the modal shows whether a link is active and when an app last fetched it,
// never the link itself — a lost link is replaced by regenerating it, which
// also revokes the old one. Regenerating an active link and disabling it
// both take a second click to confirm, like admin.js's destructive buttons
// (this app uses no native confirm() popup).
//
// None of these requests is ever queued offline: sw.js lets
// /api/v1/calendar/... go straight to the network. Shares `apiRequest`, `t`,
// `TrakkaToast` and list_view.js's `legacyCopyToClipboard` with the other
// page scripts (classic-<script>-tags shared scope). refreshCalendarFeedSection
// is called from settings.js's openUserSettingsModal.

const calendarFeedEls = {
  status: document.getElementById('calendar-feed-status'),
  links: document.getElementById('calendar-feed-links'),
  webcalUrl: document.getElementById('calendar-feed-webcal-url'),
  httpsUrl: document.getElementById('calendar-feed-https-url'),
  copyWebcal: document.getElementById('calendar-feed-copy-webcal'),
  copyHttps: document.getElementById('calendar-feed-copy-https'),
  subscribeLink: document.getElementById('calendar-feed-subscribe-link'),
  generateButton: document.getElementById('calendar-feed-generate-button'),
  disableButton: document.getElementById('calendar-feed-disable-button'),
  error: document.getElementById('calendar-feed-error'),
};

const CALENDAR_FEED_CONFIRM_WINDOW_MS = 4000;

// What GET /api/v1/calendar/feed-token last said; null while unknown
// (loading, or offline).
let calendarFeedInfo = null;
// Which button is waiting for its confirming second click: 'regenerate',
// 'disable' or null.
let calendarFeedArmed = null;
let calendarFeedArmTimer = null;

function calendarFeedUrls(token) {
  const https = `${window.location.origin}/api/v1/calendar/feed.ics?token=${encodeURIComponent(token)}`;
  // webcal:// is http(s):// under another name, which tells the OS to hand
  // the link to the calendar app rather than download the file.
  return { https, webcal: https.replace(/^https?:/, 'webcal:') };
}

function formatCalendarFeedDate(iso) {
  const locale = window.TrakkaI18n && TrakkaI18n.getLang() === 'en' ? 'en-US' : 'fr-FR';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso));
}

function disarmCalendarFeedButtons() {
  calendarFeedArmed = null;
  if (calendarFeedArmTimer) clearTimeout(calendarFeedArmTimer);
  calendarFeedArmTimer = null;
}

function armCalendarFeedButton(which) {
  disarmCalendarFeedButtons();
  calendarFeedArmed = which;
  calendarFeedArmTimer = setTimeout(() => {
    disarmCalendarFeedButtons();
    renderCalendarFeedSection();
  }, CALENDAR_FEED_CONFIRM_WINDOW_MS);
  renderCalendarFeedSection();
}

function renderCalendarFeedSection() {
  const info = calendarFeedInfo;
  if (!info) {
    calendarFeedEls.generateButton.hidden = true;
    calendarFeedEls.disableButton.hidden = true;
    return;
  }
  if (!info.enabled) {
    calendarFeedEls.status.textContent = t('modals.userSettings.calendarFeed.statusNone');
  } else if (info.last_used_at) {
    calendarFeedEls.status.textContent = t('modals.userSettings.calendarFeed.statusActiveUsed', {
      created: formatCalendarFeedDate(info.created_at),
      used: formatCalendarFeedDate(info.last_used_at),
    });
  } else {
    calendarFeedEls.status.textContent = t('modals.userSettings.calendarFeed.statusActiveUnused', {
      created: formatCalendarFeedDate(info.created_at),
    });
  }

  calendarFeedEls.generateButton.hidden = false;
  calendarFeedEls.generateButton.textContent = t(
    !info.enabled
      ? 'modals.userSettings.calendarFeed.generate'
      : calendarFeedArmed === 'regenerate'
        ? 'modals.userSettings.calendarFeed.confirmRegenerate'
        : 'modals.userSettings.calendarFeed.regenerate'
  );
  calendarFeedEls.disableButton.hidden = !info.enabled;
  calendarFeedEls.disableButton.textContent = t(
    calendarFeedArmed === 'disable'
      ? 'modals.userSettings.calendarFeed.confirmDisable'
      : 'modals.userSettings.calendarFeed.disable'
  );
}

function showCalendarFeedError(err) {
  calendarFeedEls.error.textContent = err && err.isNetworkError
    ? t('modals.userSettings.calendarFeed.networkRequired')
    : (err && err.message) || t('modals.userSettings.calendarFeed.networkRequired');
  calendarFeedEls.error.hidden = false;
}

function showCalendarFeedLinks(token) {
  const urls = calendarFeedUrls(token);
  calendarFeedEls.webcalUrl.value = urls.webcal;
  calendarFeedEls.httpsUrl.value = urls.https;
  calendarFeedEls.subscribeLink.href = urls.webcal;
  calendarFeedEls.links.hidden = false;
}

function hideCalendarFeedLinks() {
  calendarFeedEls.links.hidden = true;
  calendarFeedEls.webcalUrl.value = '';
  calendarFeedEls.httpsUrl.value = '';
  calendarFeedEls.subscribeLink.href = '#';
}

async function refreshCalendarFeedSection() {
  // The link is only ever shown right after it was generated (see the
  // header comment), so it doesn't survive closing the modal.
  hideCalendarFeedLinks();
  disarmCalendarFeedButtons();
  calendarFeedEls.error.hidden = true;
  calendarFeedInfo = null;
  calendarFeedEls.status.textContent = t('modals.userSettings.calendarFeed.loading');
  renderCalendarFeedSection();
  try {
    calendarFeedInfo = await apiRequest('/calendar/feed-token');
  } catch (err) {
    calendarFeedEls.status.textContent = err.isNetworkError
      ? t('modals.userSettings.calendarFeed.networkRequired')
      : err.message;
    return;
  }
  renderCalendarFeedSection();
}

function setCalendarFeedBusy(busy) {
  calendarFeedEls.generateButton.disabled = busy;
  calendarFeedEls.disableButton.disabled = busy;
}

calendarFeedEls.generateButton.addEventListener('click', async () => {
  if (!calendarFeedInfo) return;
  if (calendarFeedInfo.enabled && calendarFeedArmed !== 'regenerate') {
    armCalendarFeedButton('regenerate');
    return;
  }
  disarmCalendarFeedButtons();
  calendarFeedEls.error.hidden = true;
  setCalendarFeedBusy(true);
  try {
    const created = await apiRequest('/calendar/feed-token', { method: 'POST' });
    calendarFeedInfo = { enabled: true, created_at: created.created_at, last_used_at: null };
    showCalendarFeedLinks(created.token);
  } catch (err) {
    showCalendarFeedError(err);
  } finally {
    setCalendarFeedBusy(false);
    renderCalendarFeedSection();
  }
});

calendarFeedEls.disableButton.addEventListener('click', async () => {
  if (calendarFeedArmed !== 'disable') {
    armCalendarFeedButton('disable');
    return;
  }
  disarmCalendarFeedButtons();
  calendarFeedEls.error.hidden = true;
  setCalendarFeedBusy(true);
  try {
    await apiRequest('/calendar/feed-token', { method: 'DELETE' });
    calendarFeedInfo = { enabled: false };
    hideCalendarFeedLinks();
  } catch (err) {
    showCalendarFeedError(err);
  } finally {
    setCalendarFeedBusy(false);
    renderCalendarFeedSection();
  }
});

async function copyCalendarFeedUrl(input) {
  if (!input.value) return;
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(input.value);
    } else {
      legacyCopyToClipboard(input.value);
    }
    TrakkaToast.success(t('modals.userSettings.calendarFeed.copied'));
  } catch {
    // Leave it selected, ready for a manual copy.
    input.focus();
    input.select();
    calendarFeedEls.error.textContent = t('modals.userSettings.calendarFeed.copyFailed');
    calendarFeedEls.error.hidden = false;
  }
}

calendarFeedEls.copyWebcal.addEventListener('click', () => copyCalendarFeedUrl(calendarFeedEls.webcalUrl));
calendarFeedEls.copyHttps.addEventListener('click', () => copyCalendarFeedUrl(calendarFeedEls.httpsUrl));
for (const input of [calendarFeedEls.webcalUrl, calendarFeedEls.httpsUrl]) {
  input.addEventListener('focus', () => input.select());
}

// The status line and button labels are set from script, not data-i18n.
document.addEventListener('trakka:lang-changed', () => {
  if (calendarFeedInfo) renderCalendarFeedSection();
});
