'use strict';

// The visitor's view of a list's public share link (static/public-list.html,
// served at /public/lists/<token>). Self-contained: none of the app's page
// scripts load here, only i18n.js — no session, no service worker, no
// offline queue. Everything comes from /api/v1/public/lists/<token>
// (internal/handlers/public_links.go): GET to read the list, POST .../items
// to add an item and PATCH .../items/{id} to check or uncheck one — each only
// as far as the link's access mode allows (MODES below, the same table as
// the server's publicModeAllows). A deposit_only link shows no items at all:
// the page is then just a drop box, confirming each addition and listing
// what this visit dropped (in memory only).
//
// Checking an item applies at once and is rolled back if the server refuses
// it. Other people's changes show up by polling every POLL_INTERVAL_MS while
// the page is visible (and right away when it becomes visible again), the
// same "no push channel for this" choice as the Home Assistant integration.
// Every dynamic string goes through textContent/createElement, never
// innerHTML (see .claude/frontend-pwa.md's DOM conventions).

(function () {
  const POLL_INTERVAL_MS = 10000;
  const ERROR_HIDE_MS = 5000;

  const els = {
    badge: document.getElementById('public-access-badge'),
    loading: document.getElementById('public-loading'),
    notFound: document.getElementById('public-not-found'),
    content: document.getElementById('public-content'),
    icon: document.getElementById('public-list-icon'),
    name: document.getElementById('public-list-name'),
    progress: document.getElementById('public-list-progress'),
    hint: document.getElementById('public-list-hint'),
    error: document.getElementById('public-error'),
    addForm: document.getElementById('public-add-form'),
    addTitle: document.getElementById('public-add-title'),
    addQuantity: document.getElementById('public-add-quantity'),
    deposit: document.getElementById('public-deposit'),
    depositForm: document.getElementById('public-deposit-form'),
    depositTitle: document.getElementById('public-deposit-title-input'),
    depositQuantity: document.getElementById('public-deposit-quantity'),
    depositConfirm: document.getElementById('public-deposit-confirm'),
    depositSent: document.getElementById('public-deposit-sent'),
    depositSentList: document.getElementById('public-deposit-sent-list'),
    empty: document.getElementById('public-empty'),
    items: document.getElementById('public-items'),
    doneHeading: document.getElementById('public-done-heading'),
    doneItems: document.getElementById('public-done-items'),
    expires: document.getElementById('public-expires'),
  };

  const LIST_TYPE_ICONS = { groceries: '🛒', shopping: '🛍️', recurring_shopping: '🔁', todo: '✅' };

  // What each access mode shows and allows, mirroring the server's
  // publicModeAllows (which enforces it): view the items, add one, edit
  // (check, uncheck) existing ones.
  const BADGE_NEUTRAL = 'bg-slate-200 dark:bg-slate-800 text-slate-700 dark:text-slate-300';
  const MODES = {
    read: { icon: '👁️', badge: 'publicList.badgeRead', hint: 'publicList.readOnlyHint', view: true, add: false, edit: false, badgeClass: BADGE_NEUTRAL },
    add_only: { icon: '➕', badge: 'publicList.badgeAddOnly', hint: 'publicList.addOnlyHint', view: true, add: true, edit: false, badgeClass: 'bg-sky-500/15 text-sky-700 dark:text-sky-300' },
    deposit_only: { icon: '📥', badge: 'publicList.badgeDepositOnly', hint: null, view: false, add: true, edit: false, badgeClass: 'bg-violet-500/15 text-violet-700 dark:text-violet-300' },
    full_edit: { icon: '✏️', badge: 'publicList.badgeFullEdit', hint: null, view: true, add: true, edit: true, badgeClass: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300' },
  };

  // /public/lists/<token>: the token is the path's last segment.
  const token = decodeURIComponent(window.location.pathname.split('/').filter(Boolean).pop() || '');
  const apiBase = `/api/v1/public/lists/${encodeURIComponent(token)}`;

  // The list as last read from the server.
  let list = null;
  // Titles this visit dropped through a deposit_only link, newest first.
  const deposited = [];
  // Checks not yet confirmed by the server, by item id: done flag. Applied
  // over `list` on every render, so a poll landing mid-request doesn't
  // flicker the checkbox back.
  const pendingDone = new Map();
  let pollTimer = null;
  let errorTimer = null;
  let gone = false;

  function t(key, vars) {
    return window.TrakkaI18n ? TrakkaI18n.t(key, vars) : key;
  }

  // The current link's entry in MODES; an unknown mode allows nothing.
  function mode() {
    return (list && MODES[list.access_mode]) || { ...MODES.read, view: false };
  }

  function formatDate(iso, withTime) {
    const locale = window.TrakkaI18n && TrakkaI18n.getLang() === 'en' ? 'en-US' : 'fr-FR';
    const options = withTime ? { dateStyle: 'medium', timeStyle: 'short' } : { dateStyle: 'medium' };
    // A bare due date (YYYY-MM-DD) is a calendar day, not a UTC instant.
    const date = /^\d{4}-\d{2}-\d{2}$/.test(iso) ? new Date(`${iso}T00:00:00`) : new Date(iso);
    return new Intl.DateTimeFormat(locale, options).format(date);
  }

  function showError(message) {
    els.error.textContent = message;
    els.error.hidden = false;
    if (errorTimer) clearTimeout(errorTimer);
    errorTimer = setTimeout(() => {
      els.error.hidden = true;
    }, ERROR_HIDE_MS);
  }

  function errorMessageFor(status) {
    if (status === 403) return t('publicList.forbiddenError');
    if (status === 429) return t('publicList.rateLimited');
    if (status === 409) return t('publicList.listFull');
    return t('publicList.genericError');
  }

  async function request(path, options = {}) {
    const response = await fetch(apiBase + path, {
      ...options,
      credentials: 'omit',
      cache: 'no-store',
      headers: options.body ? { 'Content-Type': 'application/json' } : undefined,
    });
    let data = null;
    try {
      data = await response.json();
    } catch {
      // 204 or a non-JSON error page from a proxy: keep data null.
    }
    if (!response.ok) {
      const err = new Error((data && data.error) || response.statusText);
      err.status = response.status;
      throw err;
    }
    return data;
  }

  // The link stopped working (revoked, disabled, expired): nothing more to
  // show or poll.
  function showGone() {
    gone = true;
    stopPolling();
    els.loading.hidden = true;
    els.content.hidden = true;
    els.badge.hidden = true;
    els.notFound.hidden = false;
  }

  function renderBadge() {
    const current = mode();
    els.badge.replaceChildren();
    const icon = document.createElement('span');
    icon.setAttribute('aria-hidden', 'true');
    icon.textContent = current.icon;
    const label = document.createElement('span');
    label.textContent = t(current.badge);
    els.badge.append(icon, label);
    els.badge.className = `inline-flex items-center gap-1 rounded-full px-3 py-1 text-center text-xs font-semibold leading-tight ${current.badgeClass}`;
    els.badge.hidden = false;
  }

  function itemDone(item) {
    return pendingDone.has(item.id) ? pendingDone.get(item.id) : item.done;
  }

  function buildChip(text, className) {
    const chip = document.createElement('span');
    chip.className = `inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${className}`;
    chip.textContent = text;
    return chip;
  }

  function buildItemRow(item) {
    const done = itemDone(item);
    const writable = mode().edit;

    const li = document.createElement('li');
    const label = document.createElement('label');
    label.className =
      'flex min-h-[52px] items-start gap-3 rounded-xl border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 px-3 py-3' +
      (writable ? ' cursor-pointer active:bg-slate-100 dark:active:bg-slate-800' : '');

    const checkbox = document.createElement('input');
    checkbox.type = 'checkbox';
    checkbox.checked = done;
    checkbox.disabled = !writable;
    checkbox.className = 'mt-0.5 h-5 w-5 shrink-0 accent-sky-500 disabled:opacity-60';
    checkbox.setAttribute('aria-label', t('publicList.toggleLabel', { title: item.title }));
    if (writable) checkbox.addEventListener('change', () => toggleItem(item, checkbox.checked));

    const body = document.createElement('span');
    body.className = 'min-w-0 flex-1';
    const title = document.createElement('span');
    title.className = `block break-words text-base ${done ? 'text-slate-400 line-through dark:text-slate-500' : ''}`;
    title.textContent = item.title;
    body.appendChild(title);

    const meta = document.createElement('span');
    meta.className = 'mt-1 flex flex-wrap gap-1';
    if (item.quantity > 1) {
      meta.appendChild(buildChip(t('publicList.quantity', { count: item.quantity }), 'bg-sky-500/15 text-sky-700 dark:text-sky-300'));
    }
    if (item.is_urgent && !done) {
      meta.appendChild(buildChip(`🔥 ${t('publicList.urgent')}`, 'bg-rose-500/15 text-rose-700 dark:text-rose-300'));
    }
    if (item.aisle) {
      meta.appendChild(buildChip(item.aisle, 'bg-amber-500/15 text-amber-800 dark:text-amber-300'));
    }
    if (item.due_date) {
      const due = item.due_time ? `${formatDate(item.due_date)} ${item.due_time}` : formatDate(item.due_date);
      meta.appendChild(buildChip(t('publicList.due', { date: due }), 'bg-slate-200 dark:bg-slate-800 text-slate-700 dark:text-slate-300'));
    }
    for (const name of item.labels || []) {
      meta.appendChild(buildChip(`#${name}`, 'bg-violet-500/15 text-violet-700 dark:text-violet-300'));
    }
    if (meta.childElementCount > 0) body.appendChild(meta);

    label.append(checkbox, body);
    li.appendChild(label);
    return li;
  }

  function render() {
    if (!list) return;
    els.loading.hidden = true;
    els.notFound.hidden = true;
    els.content.hidden = false;
    renderBadge();

    document.title = `${list.name} · Trakka`;
    els.icon.textContent = list.icon || LIST_TYPE_ICONS[list.type] || '📋';
    els.name.textContent = list.name;

    const current = mode();
    renderDeposit(current);
    for (const el of [els.progress, els.items, els.doneItems]) el.hidden = !current.view;
    if (!current.view) {
      els.empty.hidden = true;
      els.doneHeading.hidden = true;
      els.addForm.hidden = true;
      els.hint.hidden = true;
      renderExpiry();
      return;
    }

    // Not done first, urgent ones on top; otherwise the list's own order.
    const open = list.items.filter((item) => !itemDone(item));
    const done = list.items.filter((item) => itemDone(item));
    open.sort((a, b) => Number(b.is_urgent) - Number(a.is_urgent));

    els.progress.textContent = list.items.length
      ? t('publicList.remaining', { count: open.length, total: list.items.length })
      : '';
    els.empty.hidden = list.items.length > 0;
    els.items.replaceChildren(...open.map(buildItemRow));
    els.doneItems.replaceChildren(...done.map(buildItemRow));
    els.doneHeading.hidden = done.length === 0;

    els.addForm.hidden = !current.add;
    els.hint.hidden = !current.hint;
    els.hint.textContent = current.hint ? t(current.hint) : '';
    renderExpiry();
  }

  function renderExpiry() {
    els.expires.hidden = !list.expires_at;
    els.expires.textContent = list.expires_at ? t('publicList.expires', { date: formatDate(list.expires_at, true) }) : '';
  }

  // The drop box of a deposit_only link: shown instead of the list.
  function renderDeposit(current) {
    els.deposit.hidden = current.view || !current.add;
    els.depositSent.hidden = deposited.length === 0;
    els.depositSentList.replaceChildren(
      ...deposited.map((title) => buildChip(`✓ ${title}`, 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'))
    );
  }

  async function load() {
    if (gone) return;
    try {
      list = await request('');
    } catch (err) {
      if (err.status === 404) {
        showGone();
      } else if (!list) {
        els.loading.textContent = t('publicList.networkError');
      }
      return;
    }
    render();
  }

  async function toggleItem(item, done) {
    pendingDone.set(item.id, done);
    render();
    try {
      const updated = await request(`/items/${item.id}`, { method: 'PATCH', body: JSON.stringify({ done }) });
      const index = list.items.findIndex((it) => it.id === updated.id);
      if (index >= 0) list.items[index] = updated;
    } catch (err) {
      if (err.status !== 404) showError(errorMessageFor(err.status));
      // 404: the item, or the whole link, is gone; 403: the link's mode
      // changed meanwhile. Either way, the next read says what is true now.
      if (err.status === 404 || err.status === 403) load();
    } finally {
      pendingDone.delete(item.id);
      render();
    }
  }

  els.addForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const title = els.addTitle.value.trim();
    if (!title || !mode().add) return;
    const quantity = Math.max(1, Number.parseInt(els.addQuantity.value, 10) || 1);
    const submit = els.addForm.querySelector('button[type="submit"]');
    submit.disabled = true;
    try {
      await request('/items', { method: 'POST', body: JSON.stringify({ title, quantity }) });
      els.addTitle.value = '';
      els.addQuantity.value = '1';
      await load();
    } catch (err) {
      if (err.status !== 404) showError(errorMessageFor(err.status));
      if (err.status === 404 || err.status === 403) load();
    } finally {
      submit.disabled = false;
      els.addTitle.focus();
    }
  });

  els.depositForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const title = els.depositTitle.value.trim();
    if (!title || !mode().add) return;
    const quantity = Math.max(1, Number.parseInt(els.depositQuantity.value, 10) || 1);
    const submit = els.depositForm.querySelector('button[type="submit"]');
    submit.disabled = true;
    els.depositConfirm.hidden = true;
    try {
      await request('/items', { method: 'POST', body: JSON.stringify({ title, quantity }) });
      deposited.unshift(quantity > 1 ? `${title} × ${quantity}` : title);
      els.depositTitle.value = '';
      els.depositQuantity.value = '1';
      els.depositConfirm.textContent = t('publicList.depositConfirm', { title });
      els.depositConfirm.hidden = false;
      render();
    } catch (err) {
      if (err.status !== 404) showError(errorMessageFor(err.status));
      if (err.status === 404 || err.status === 403) load();
    } finally {
      submit.disabled = false;
      els.depositTitle.focus();
    }
  });

  function stopPolling() {
    if (pollTimer) clearInterval(pollTimer);
    pollTimer = null;
  }

  function startPolling() {
    stopPolling();
    if (gone) return;
    pollTimer = setInterval(() => {
      if (pendingDone.size === 0) load();
    }, POLL_INTERVAL_MS);
  }

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') {
      load();
      startPolling();
    } else {
      stopPolling();
    }
  });
  document.addEventListener('trakka:lang-changed', render);

  const ready = window.TrakkaI18n ? TrakkaI18n.ready.catch(() => {}) : Promise.resolve();
  ready.then(() => {
    document.title = `${t('publicList.pageTitle')} · Trakka`;
    load();
    startPolling();
  });
})();
