'use strict';

// "Fluv" section of #user-settings-modal (Intégrations tab): connects the
// user's Fluv, the personal finance app, through GET/PUT/DELETE
// /api/v1/integrations/fluv and POST …/test and …/retry
// (internal/handlers/fluv.go, docs/FLUV.md). Once connected, every item the
// user checks off a purchase list is sent to Fluv as an expense, by the
// server, in the background.
//
// The token is write-only: the server only says whether one is stored, so
// the field stays empty and an empty field on save keeps the stored token.
// The section stays hidden when the server has the integration off (the GET
// answers 404). Disconnecting takes a second click to confirm, like
// api-tokens.js (this app uses no confirm()).
//
// None of these requests is ever queued offline: sw.js lets
// /api/v1/integrations/ go straight to the network. Shares `apiRequest`, `t`
// and `TrakkaToast` with the other page scripts (classic-<script>-tags
// shared scope). refreshFluvSection is called from settings.js's
// openUserSettingsModal.

const fluvEls = {
  section: document.getElementById('fluv-section'),
  status: document.getElementById('fluv-status'),
  endpoint: document.getElementById('fluv-endpoint'),
  pinnedIps: document.getElementById('fluv-pinned-ips'),
  token: document.getElementById('fluv-token'),
  tokenHint: document.getElementById('fluv-token-hint'),
  enabled: document.getElementById('fluv-enabled'),
  saveButton: document.getElementById('fluv-save-button'),
  testButton: document.getElementById('fluv-test-button'),
  retryButton: document.getElementById('fluv-retry-button'),
  disconnectButton: document.getElementById('fluv-disconnect-button'),
  error: document.getElementById('fluv-error'),
};

const FLUV_CONFIRM_WINDOW_MS = 4000;

// The error codes the server and the sender report (internal/fluv), each
// with a translated message; any other code — one of Fluv's own — falls back
// on fluvErrorText's generic one.
const FLUV_ERROR_CODES = [
  'required', 'invalid_url', 'not_fluv_endpoint', 'too_many', 'invalid_ip', 'address_not_allowed',
  'host_not_pinned', 'https_required', 'invalid_token', 'unauthenticated', 'not_found', 'rate_limited',
  'server_error', 'redirect_refused', 'rejected', 'unreachable', 'timeout', 'tls_error', 'blocked_address',
  'token_unreadable',
];

// What GET /api/v1/integrations/fluv last said; null while unknown.
let fluvSettings = null;
let fluvDisconnectArmed = false;
let fluvDisconnectTimer = null;

function formatFluvDate(iso) {
  const locale = window.TrakkaI18n && TrakkaI18n.getLang() === 'en' ? 'en-US' : 'fr-FR';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso));
}

function fluvErrorText(code) {
  if (FLUV_ERROR_CODES.includes(code)) return t(`modals.userSettings.fluv.errors.${code}`);
  return t('modals.userSettings.fluv.errors.other', { code });
}

function disarmFluvDisconnect() {
  fluvDisconnectArmed = false;
  if (fluvDisconnectTimer) clearTimeout(fluvDisconnectTimer);
  fluvDisconnectTimer = null;
}

function renderFluvSection() {
  const s = fluvSettings;
  if (!s) return;
  const lines = [];
  if (!s.configured) {
    lines.push(t('modals.userSettings.fluv.notConnected'));
  } else {
    lines.push(t(s.enabled ? 'modals.userSettings.fluv.connected' : 'modals.userSettings.fluv.paused'));
    if (s.last_error_code && s.last_error_at) {
      lines.push(t('modals.userSettings.fluv.lastError', { when: formatFluvDate(s.last_error_at), reason: fluvErrorText(s.last_error_code) }));
    } else if (s.last_success_at) {
      lines.push(t('modals.userSettings.fluv.lastSuccess', { when: formatFluvDate(s.last_success_at) }));
    }
    if (s.pending > 0) lines.push(t('modals.userSettings.fluv.pending', { count: s.pending }));
    if (s.failed > 0) lines.push(t('modals.userSettings.fluv.failed', { count: s.failed }));
  }
  fluvEls.status.textContent = lines.join(' · ');
  fluvEls.tokenHint.textContent = t(s.token_set ? 'modals.userSettings.fluv.tokenKept' : 'modals.userSettings.fluv.tokenHint');
  fluvEls.testButton.hidden = !s.configured;
  fluvEls.retryButton.hidden = !(s.configured && s.failed > 0);
  fluvEls.disconnectButton.hidden = !s.configured;
  fluvEls.disconnectButton.textContent = t(fluvDisconnectArmed ? 'modals.userSettings.fluv.confirmDisconnect' : 'modals.userSettings.fluv.disconnect');
  fluvEls.disconnectButton.classList.toggle('text-rose-600', fluvDisconnectArmed);
  fluvEls.disconnectButton.classList.toggle('dark:text-rose-400', fluvDisconnectArmed);
}

// Fills the fields from fluvSettings — on open and after a save, never while
// the user is typing.
function fillFluvFields() {
  const s = fluvSettings;
  fluvEls.endpoint.value = s.endpoint_url || '';
  fluvEls.pinnedIps.value = (s.pinned_ips || []).join(', ');
  fluvEls.token.value = '';
  fluvEls.enabled.checked = !s.configured || s.enabled;
}

function showFluvError(err) {
  let message = t('modals.userSettings.fluv.networkRequired');
  if (err && err.code && FLUV_ERROR_CODES.includes(err.code)) message = fluvErrorText(err.code);
  else if (err && !err.isNetworkError && err.message) message = err.message;
  fluvEls.error.textContent = message;
  fluvEls.error.hidden = false;
}

async function refreshFluvSection() {
  disarmFluvDisconnect();
  fluvEls.error.hidden = true;
  fluvSettings = null;
  try {
    fluvSettings = await apiRequest('/integrations/fluv');
  } catch (err) {
    // 404: the operator turned the integration off. Offline: there is
    // nothing useful to show without the server either.
    fluvEls.section.hidden = !(err && err.isNetworkError);
    if (err && err.isNetworkError) fluvEls.status.textContent = t('modals.userSettings.fluv.networkRequired');
    return;
  }
  fluvEls.section.hidden = false;
  fillFluvFields();
  renderFluvSection();
}

async function saveFluvSettings() {
  disarmFluvDisconnect();
  fluvEls.error.hidden = true;
  const pinnedIps = fluvEls.pinnedIps.value.split(/[\s,;]+/).filter(Boolean);
  const payload = {
    endpoint_url: fluvEls.endpoint.value.trim(),
    pinned_ips: pinnedIps,
    token: fluvEls.token.value.trim(),
    enabled: fluvEls.enabled.checked,
  };
  fluvEls.saveButton.disabled = true;
  try {
    fluvSettings = await apiRequest('/integrations/fluv', { method: 'PUT', body: JSON.stringify(payload) });
    fillFluvFields();
    TrakkaToast.success(t('modals.userSettings.fluv.saved'));
  } catch (err) {
    showFluvError(err);
  } finally {
    fluvEls.saveButton.disabled = false;
    renderFluvSection();
  }
}

async function testFluvConnection() {
  disarmFluvDisconnect();
  fluvEls.error.hidden = true;
  fluvEls.testButton.disabled = true;
  try {
    const result = await apiRequest('/integrations/fluv/test', { method: 'POST' });
    if (result.ok) TrakkaToast.success(t('modals.userSettings.fluv.testOk'));
    else {
      fluvEls.error.textContent = t('modals.userSettings.fluv.testFailed', { reason: fluvErrorText(result.code) });
      fluvEls.error.hidden = false;
    }
    fluvSettings = await apiRequest('/integrations/fluv');
  } catch (err) {
    showFluvError(err);
  } finally {
    fluvEls.testButton.disabled = false;
    renderFluvSection();
  }
}

async function retryFluvPurchases() {
  disarmFluvDisconnect();
  fluvEls.error.hidden = true;
  fluvEls.retryButton.disabled = true;
  try {
    const result = await apiRequest('/integrations/fluv/retry', { method: 'POST' });
    TrakkaToast.success(t('modals.userSettings.fluv.retried', { count: result.retried }));
    fluvSettings = await apiRequest('/integrations/fluv');
  } catch (err) {
    showFluvError(err);
  } finally {
    fluvEls.retryButton.disabled = false;
    renderFluvSection();
  }
}

async function disconnectFluv() {
  if (!fluvDisconnectArmed) {
    fluvDisconnectArmed = true;
    fluvDisconnectTimer = setTimeout(() => {
      disarmFluvDisconnect();
      renderFluvSection();
    }, FLUV_CONFIRM_WINDOW_MS);
    renderFluvSection();
    return;
  }
  disarmFluvDisconnect();
  fluvEls.error.hidden = true;
  fluvEls.disconnectButton.disabled = true;
  try {
    await apiRequest('/integrations/fluv', { method: 'DELETE' });
    fluvSettings = await apiRequest('/integrations/fluv');
    fillFluvFields();
    TrakkaToast.success(t('modals.userSettings.fluv.disconnected'));
  } catch (err) {
    showFluvError(err);
  } finally {
    fluvEls.disconnectButton.disabled = false;
    renderFluvSection();
  }
}

fluvEls.saveButton.addEventListener('click', saveFluvSettings);
fluvEls.testButton.addEventListener('click', testFluvConnection);
fluvEls.retryButton.addEventListener('click', retryFluvPurchases);
fluvEls.disconnectButton.addEventListener('click', disconnectFluv);
// The fields sit inside #user-settings-form: Enter must save the
// connection, not submit (and close) the settings.
for (const input of [fluvEls.endpoint, fluvEls.pinnedIps, fluvEls.token]) {
  input.addEventListener('keydown', (event) => {
    if (event.key !== 'Enter' || event.isComposing) return;
    event.preventDefault();
    saveFluvSettings();
  });
}

// The status line and button labels are built from script, not data-i18n.
document.addEventListener('trakka:lang-changed', () => {
  if (fluvSettings) renderFluvSection();
});
