'use strict';

// "Jetons d'API" section of #user-settings-modal (Intégrations tab): lists,
// creates and revokes the user's API tokens through GET/POST /api/v1/api-tokens
// and DELETE /api/v1/api-tokens/{id} (internal/handlers/api_tokens.go,
// docs/HOME_ASSISTANT.md). Integrations such as Home Assistant then call the
// API with "Authorization: Bearer trk_...", with no session.
//
// The server stores only a hash of each token, so a token can be shown
// exactly once: in the POST response, right after it is created, together
// with this server's address (what Home Assistant asks for next to it).
// Reopening the modal shows each token's name and when it was created and
// last used, never the token itself. Revoking takes a second click to
// confirm, like calendar-feed.js's buttons (this app uses no confirm()).
//
// None of these requests is ever queued offline: sw.js lets
// /api/v1/api-tokens go straight to the network. Shares `apiRequest`, `t`,
// `TrakkaToast` and list_view.js's `legacyCopyToClipboard` with the other
// page scripts (classic-<script>-tags shared scope). refreshApiTokensSection
// is called from settings.js's openUserSettingsModal.

const apiTokensEls = {
  status: document.getElementById('api-tokens-status'),
  list: document.getElementById('api-tokens-list'),
  created: document.getElementById('api-tokens-created'),
  createdValue: document.getElementById('api-tokens-created-value'),
  serverUrl: document.getElementById('api-tokens-server-url'),
  copyToken: document.getElementById('api-tokens-copy-token'),
  copyUrl: document.getElementById('api-tokens-copy-url'),
  name: document.getElementById('api-tokens-name'),
  createButton: document.getElementById('api-tokens-create-button'),
  error: document.getElementById('api-tokens-error'),
};

const API_TOKENS_CONFIRM_WINDOW_MS = 4000;

// What GET /api/v1/api-tokens last said; null while unknown (loading, or
// offline).
let apiTokens = null;
// The id of the token whose "Révoquer" button waits for its confirming
// second click, or null.
let apiTokenArmedId = null;
let apiTokenArmTimer = null;

function formatApiTokenDate(iso) {
  const locale = window.TrakkaI18n && TrakkaI18n.getLang() === 'en' ? 'en-US' : 'fr-FR';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso));
}

function disarmApiTokenRevoke() {
  apiTokenArmedId = null;
  if (apiTokenArmTimer) clearTimeout(apiTokenArmTimer);
  apiTokenArmTimer = null;
}

function armApiTokenRevoke(id) {
  disarmApiTokenRevoke();
  apiTokenArmedId = id;
  apiTokenArmTimer = setTimeout(() => {
    disarmApiTokenRevoke();
    renderApiTokensSection();
  }, API_TOKENS_CONFIRM_WINDOW_MS);
  renderApiTokensSection();
}

function buildApiTokenRow(token) {
  const row = document.createElement('li');
  row.className = 'flex items-center gap-3 rounded-lg border border-slate-200 dark:border-slate-700 px-3 py-2';

  const text = document.createElement('div');
  text.className = 'min-w-0 flex-1';
  const name = document.createElement('p');
  name.className = 'truncate text-sm font-medium text-slate-800 dark:text-slate-100';
  name.textContent = token.name;
  const meta = document.createElement('p');
  meta.className = 'text-xs text-slate-500 dark:text-slate-400';
  meta.textContent = token.last_used_at
    ? t('modals.userSettings.apiTokens.createdUsed', {
      created: formatApiTokenDate(token.created_at),
      used: formatApiTokenDate(token.last_used_at),
    })
    : t('modals.userSettings.apiTokens.createdUnused', { created: formatApiTokenDate(token.created_at) });
  text.append(name, meta);

  const revoke = document.createElement('button');
  revoke.type = 'button';
  revoke.className = 'min-h-[44px] shrink-0 rounded-xl border border-slate-200 dark:border-slate-700 bg-slate-100 dark:bg-slate-800 px-3 py-2 text-sm font-medium text-slate-600 dark:text-slate-300 hover:bg-rose-500/10 hover:text-rose-600 dark:hover:text-rose-400 disabled:opacity-60';
  const armed = apiTokenArmedId === token.id;
  revoke.textContent = t(armed ? 'modals.userSettings.apiTokens.confirmRevoke' : 'modals.userSettings.apiTokens.revoke');
  revoke.setAttribute('aria-label', t('modals.userSettings.apiTokens.revokeAriaLabel', { name: token.name }));
  if (armed) revoke.classList.add('text-rose-600', 'dark:text-rose-400');
  revoke.addEventListener('click', () => revokeApiToken(token, revoke));

  row.append(text, revoke);
  return row;
}

function renderApiTokensSection() {
  apiTokensEls.list.replaceChildren();
  if (!apiTokens) return;
  apiTokensEls.status.textContent = apiTokens.length === 0 ? t('modals.userSettings.apiTokens.none') : '';
  apiTokensEls.status.hidden = apiTokens.length !== 0;
  for (const token of apiTokens) {
    apiTokensEls.list.append(buildApiTokenRow(token));
  }
}

function showApiTokensError(err) {
  let message = t('modals.userSettings.apiTokens.networkRequired');
  if (err && err.status === 409) message = t('modals.userSettings.apiTokens.tooMany');
  else if (err && !err.isNetworkError && err.message) message = err.message;
  apiTokensEls.error.textContent = message;
  apiTokensEls.error.hidden = false;
}

function hideCreatedApiToken() {
  apiTokensEls.created.hidden = true;
  apiTokensEls.createdValue.value = '';
  apiTokensEls.serverUrl.value = '';
}

async function refreshApiTokensSection() {
  // A new token is only ever shown right after it was created (see the
  // header comment), so it doesn't survive closing the modal.
  hideCreatedApiToken();
  disarmApiTokenRevoke();
  apiTokensEls.error.hidden = true;
  apiTokens = null;
  apiTokensEls.list.replaceChildren();
  apiTokensEls.status.hidden = false;
  apiTokensEls.status.textContent = t('modals.userSettings.apiTokens.loading');
  try {
    apiTokens = await apiRequest('/api-tokens');
  } catch (err) {
    apiTokensEls.status.textContent = err.isNetworkError
      ? t('modals.userSettings.apiTokens.networkRequired')
      : err.message;
    return;
  }
  renderApiTokensSection();
}

async function createApiToken() {
  if (!apiTokens) return;
  disarmApiTokenRevoke();
  apiTokensEls.error.hidden = true;
  const name = apiTokensEls.name.value.trim() || t('modals.userSettings.apiTokens.defaultName');
  apiTokensEls.createButton.disabled = true;
  try {
    const created = await apiRequest('/api-tokens', {
      method: 'POST',
      body: JSON.stringify({ name }),
    });
    apiTokens.push({ id: created.id, name: created.name, created_at: created.created_at, last_used_at: null });
    apiTokensEls.createdValue.value = created.token;
    apiTokensEls.serverUrl.value = window.location.origin;
    apiTokensEls.created.hidden = false;
    apiTokensEls.name.value = '';
  } catch (err) {
    showApiTokensError(err);
  } finally {
    apiTokensEls.createButton.disabled = false;
    renderApiTokensSection();
  }
}

async function revokeApiToken(token, button) {
  if (apiTokenArmedId !== token.id) {
    armApiTokenRevoke(token.id);
    return;
  }
  disarmApiTokenRevoke();
  apiTokensEls.error.hidden = true;
  button.disabled = true;
  try {
    await apiRequest(`/api-tokens/${encodeURIComponent(token.id)}`, { method: 'DELETE' });
    apiTokens = apiTokens.filter((entry) => entry.id !== token.id);
    TrakkaToast.success(t('modals.userSettings.apiTokens.revoked', { name: token.name }));
  } catch (err) {
    // Already gone (revoked from another device): the list was just stale.
    if (err && err.status === 404) apiTokens = apiTokens.filter((entry) => entry.id !== token.id);
    else showApiTokensError(err);
  } finally {
    renderApiTokensSection();
  }
}

apiTokensEls.createButton.addEventListener('click', createApiToken);
// The field sits inside #user-settings-form: Enter must create a token, not
// submit (and close) the settings.
apiTokensEls.name.addEventListener('keydown', (event) => {
  if (event.key !== 'Enter' || event.isComposing) return;
  event.preventDefault();
  createApiToken();
});

async function copyApiTokenField(input) {
  if (!input.value) return;
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(input.value);
    } else {
      legacyCopyToClipboard(input.value);
    }
    TrakkaToast.success(t('modals.userSettings.apiTokens.copied'));
  } catch {
    // Leave it selected, ready for a manual copy.
    input.focus();
    input.select();
    apiTokensEls.error.textContent = t('modals.userSettings.apiTokens.copyFailed');
    apiTokensEls.error.hidden = false;
  }
}

apiTokensEls.copyToken.addEventListener('click', () => copyApiTokenField(apiTokensEls.createdValue));
apiTokensEls.copyUrl.addEventListener('click', () => copyApiTokenField(apiTokensEls.serverUrl));
for (const input of [apiTokensEls.createdValue, apiTokensEls.serverUrl]) {
  input.addEventListener('focus', () => input.select());
}

// The rows and the status line are built from script, not data-i18n.
document.addEventListener('trakka:lang-changed', () => {
  if (apiTokens) renderApiTokensSection();
});
