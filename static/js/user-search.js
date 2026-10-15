'use strict';

// Sharing autocomplete: as the user types into one of the "invite by email"
// fields — the share modal's (#share-email, shares.js) and the House
// members modal's (#invite-email, app.js) — a dropdown suggests matching
// accounts from GET /api/v1/users/search, and picking one fills in its
// email address. The form's own submit handler is untouched: it still
// posts whatever address ends up in the field, so a typed address nobody
// was suggested for (a stranger, someone without an account yet) keeps
// going through the ordinary invitation flow.
//
// What can be found depends on the instance's deployment mode, read once
// from GET /api/v1/config (internal/config.Config.SaaSMode): self-hosted,
// anyone on the instance by name or email, shown as name + email; SaaS,
// only the caller's existing contacts and by email address alone, shown as
// the address alone. The backend enforces that either way — the frontend
// only adapts the placeholder and the rows. Until the mode is known (or
// while offline) each field keeps its email-only placeholder, the one that
// is true in both modes.
//
// Shares `apiRequest` with app.js — same classic-<script>-tags
// shared-scope pattern as every other file here. Suggestion rows are built
// with createElement/textContent only (never innerHTML): a display name is
// arbitrary user input.

const USER_SEARCH_DEBOUNCE_MS = 250;
// Below the backend's own minimum (handlers.userSearchMinRunes) the request
// would just come back empty.
const USER_SEARCH_MIN_CHARS = 2;

// Each autocompleted field, with the i18n keys of its two placeholders.
const userSearchFields = [
  {
    input: document.getElementById('share-email'),
    list: document.getElementById('share-email-suggestions'),
    searchPlaceholderKey: 'modals.share.searchPlaceholder',
    emailPlaceholderKey: 'modals.share.emailPlaceholder',
  },
  {
    input: document.getElementById('invite-email'),
    list: document.getElementById('invite-email-suggestions'),
    searchPlaceholderKey: 'modals.members.searchPlaceholder',
    emailPlaceholderKey: 'modals.members.invitePlaceholder',
  },
];

// null until GET /api/v1/config has answered.
let userSearchSaaSMode = null;
let userSearchConfigRequest = null;

// Fetches the deployment mode once; a failure (offline, server down) is
// forgotten so the next focus on a field retries it.
function loadUserSearchConfig() {
  if (userSearchSaaSMode !== null) return Promise.resolve();
  if (!userSearchConfigRequest) {
    userSearchConfigRequest = apiRequest('/config')
      .then((config) => {
        userSearchSaaSMode = Boolean(config && config.saas_mode);
        for (const field of userSearchFields) applyUserSearchPlaceholder(field);
      })
      .catch(() => {})
      .finally(() => {
        userSearchConfigRequest = null;
      });
  }
  return userSearchConfigRequest;
}

// Points the field's data-i18n-placeholder at the mode's key and lets i18n.js
// apply it, so it also follows a later language switch — and, before the
// locale files have loaded, leaves the HTML's own fallback text in place
// rather than showing a raw key.
function applyUserSearchPlaceholder(field) {
  const key = userSearchSaaSMode === false ? field.searchPlaceholderKey : field.emailPlaceholderKey;
  field.input.setAttribute('data-i18n-placeholder', key);
  TrakkaI18n.applyTranslations(field.input.parentElement);
}

function attachUserAutocomplete(field) {
  const { input, list } = field;
  let debounceTimer = null;
  let controller = null;
  let results = [];
  let activeIndex = -1;

  function close() {
    clearTimeout(debounceTimer);
    if (controller) controller.abort();
    controller = null;
    results = [];
    activeIndex = -1;
    list.hidden = true;
    list.replaceChildren();
    input.setAttribute('aria-expanded', 'false');
    input.removeAttribute('aria-activedescendant');
  }

  function setActive(index) {
    activeIndex = index;
    list.querySelectorAll('[role="option"]').forEach((option, i) => {
      const active = i === index;
      option.setAttribute('aria-selected', String(active));
      option.classList.toggle('bg-sky-500/10', active);
      if (active) {
        input.setAttribute('aria-activedescendant', option.id);
        option.scrollIntoView({ block: 'nearest' });
      }
    });
    if (index < 0) input.removeAttribute('aria-activedescendant');
  }

  function pick(index) {
    const user = results[index];
    if (!user) return;
    input.value = user.email;
    close();
    input.focus();
  }

  function buildOption(user, index) {
    const li = document.createElement('li');
    li.id = `${list.id}-${index}`;
    li.setAttribute('role', 'option');
    li.setAttribute('aria-selected', 'false');
    li.className = 'cursor-pointer px-4 py-2 hover:bg-slate-100 dark:hover:bg-slate-800';

    const email = document.createElement('p');
    if (user.display_name) {
      const name = document.createElement('p');
      name.className = 'truncate text-sm font-medium text-slate-900 dark:text-slate-100';
      name.textContent = user.display_name;
      email.className = 'truncate text-xs text-slate-500 dark:text-slate-400';
      li.appendChild(name);
    } else {
      email.className = 'truncate text-sm text-slate-900 dark:text-slate-100';
    }
    email.textContent = user.email;
    li.appendChild(email);

    // mousedown, not click, decides before the input's blur closes the list;
    // preventDefault keeps the focus (and the mobile keyboard) on the input.
    li.addEventListener('mousedown', (event) => event.preventDefault());
    li.addEventListener('click', () => pick(index));
    return li;
  }

  // Opens the dropdown above the field when it wouldn't fit below — the
  // usual case on a phone, where both modals are bottom sheets and the
  // on-screen keyboard takes the rest (visualViewport excludes it).
  function place() {
    const viewportBottom = window.visualViewport
      ? window.visualViewport.offsetTop + window.visualViewport.height
      : window.innerHeight;
    const rect = input.getBoundingClientRect();
    const spaceBelow = viewportBottom - rect.bottom;
    const up = spaceBelow < list.offsetHeight + 8 && rect.top > spaceBelow;
    list.classList.toggle('top-full', !up);
    list.classList.toggle('mt-1', !up);
    list.classList.toggle('bottom-full', up);
    list.classList.toggle('mb-1', up);
  }

  function render() {
    list.replaceChildren(...results.map(buildOption));
    activeIndex = -1;
    const open = results.length > 0;
    list.hidden = !open;
    if (open) place();
    input.setAttribute('aria-expanded', String(open));
    input.removeAttribute('aria-activedescendant');
  }

  async function search(query) {
    if (controller) controller.abort();
    const current = new AbortController();
    controller = current;
    let found;
    try {
      found = await apiRequest(`/users/search?q=${encodeURIComponent(query)}`, { signal: current.signal });
    } catch {
      // Offline, rate-limited, aborted by a newer keystroke: suggestions are
      // a convenience, so a failure just shows none.
      found = null;
    }
    if (controller !== current) return; // a newer search (or close()) superseded this one
    controller = null;
    if (document.activeElement !== input) return;
    results = Array.isArray(found) ? found : [];
    render();
  }

  input.addEventListener('input', () => {
    clearTimeout(debounceTimer);
    const query = input.value.trim();
    if (query.length < USER_SEARCH_MIN_CHARS) {
      close();
      return;
    }
    debounceTimer = setTimeout(() => search(query), USER_SEARCH_DEBOUNCE_MS);
  });

  input.addEventListener('keydown', (event) => {
    if (list.hidden) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setActive((activeIndex + 1) % results.length);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      setActive(activeIndex <= 0 ? results.length - 1 : activeIndex - 1);
    } else if (event.key === 'Enter' && (activeIndex >= 0 || !input.value.includes('@'))) {
      // Picks the highlighted suggestion instead of submitting the form —
      // or the first one when what was typed is a name rather than an
      // address, which the email field couldn't submit anyway.
      event.preventDefault();
      pick(Math.max(activeIndex, 0));
    } else if (event.key === 'Escape') {
      // Closes just the dropdown: the modals' own document-level Escape
      // handlers (app.js, shares.js) would otherwise close the whole modal.
      event.stopPropagation();
      close();
    }
  });

  input.addEventListener('focus', loadUserSearchConfig);
  input.addEventListener('blur', close);
  input.form.addEventListener('submit', close);
  input.form.addEventListener('reset', close);
}

for (const field of userSearchFields) attachUserAutocomplete(field);
loadUserSearchConfig();
