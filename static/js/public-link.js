'use strict';

// "Liens de partage publics" section of #share-modal (lists only): a list
// can have several public links, one per audience ("Famille", "Artisans",
// "Invités"...), each with its own access mode (👁️ read, ➕ add only, 📥
// blind drop, ✏️ full edit), optional label and expiry. The section lists
// them as compact rows (name or mode, mode badge, status, copy, revoke) and
// creates new ones from a form behind "+ Générer un nouveau lien public",
// through GET/POST /api/v1/lists/{id}/public-links and DELETE
// .../public-links/{linkId} (internal/handlers/public_links.go,
// docs/API.md#public-share-links). Visitors open a link at
// /public/lists/<token>, a page of its own (public-list.html/public-list.js).
//
// The server keeps each link (its token is stored as is, see migration 36)
// and returns it with its settings, so every row can be copied any time
// until it is revoked. A link's mode can't be changed: revoke it and create
// another. Revoking takes a confirming second click, like calendar-feed.js's
// buttons (this app uses no native confirm() popup).
//
// None of these requests is ever queued offline: sw.js lets them go straight
// to the network. Shares `apiRequest`, `t`, `TrakkaToast`, `TRASH_ICON_SVG`
// and list_view.js's `legacyCopyToClipboard` with the other page scripts
// (classic-<script>-tags shared scope). shares.js's
// openShareModal/closeShareModal call
// refreshPublicLinkSection/resetPublicLinkSection.

const publicLinkEls = {
  section: document.getElementById('public-link-section'),
  status: document.getElementById('public-link-status'),
  list: document.getElementById('public-links-list'),
  error: document.getElementById('public-link-error'),
  newButton: document.getElementById('public-link-new-button'),
  form: document.getElementById('public-link-form'),
  label: document.getElementById('public-link-label'),
  modeInputs: Array.from(document.querySelectorAll('input[name="public-link-mode"]')),
  expiry: document.getElementById('public-link-expiry'),
  createButton: document.getElementById('public-link-create-button'),
  cancelButton: document.getElementById('public-link-cancel-button'),
};

const PUBLIC_LINK_CONFIRM_WINDOW_MS = 4000;
const PUBLIC_LINK_COPIED_MS = 2000;
// The server's maxPublicLinksPerList.
const PUBLIC_LINK_MAX_PER_LIST = 20;

// Each access mode's icon, name (the label of an unnamed link) and short
// badge text.
const PUBLIC_LINK_MODES = {
  read: { icon: '👁️', name: 'modals.share.publicLink.modeRead', badge: 'modals.share.publicLink.badgeRead' },
  add_only: { icon: '➕', name: 'modals.share.publicLink.modeAddOnly', badge: 'modals.share.publicLink.badgeAddOnly' },
  deposit_only: { icon: '📥', name: 'modals.share.publicLink.modeDepositOnly', badge: 'modals.share.publicLink.badgeDepositOnly' },
  full_edit: { icon: '✏️', name: 'modals.share.publicLink.modeFullEdit', badge: 'modals.share.publicLink.badgeFullEdit' },
};

// The list whose links the section shows, null while the modal is closed or
// open on a Space.
let publicLinkListId = null;
// What GET /api/v1/lists/{id}/public-links last said; null while unknown
// (loading, or offline).
let publicLinks = null;
// The link whose revoke button is waiting for its confirming second click.
let publicLinkArmedId = null;
let publicLinkArmTimer = null;
// The link whose copy button shows ✅ right now.
let publicLinkCopiedId = null;
let publicLinkCopiedTimer = null;

function publicLinksEndpoint() {
  return `/lists/${publicLinkListId}/public-links`;
}

function publicLinkPageUrl(link) {
  return `${window.location.origin}${link.path}`;
}

function formatPublicLinkDate(iso) {
  const locale = window.TrakkaI18n && TrakkaI18n.getLang() === 'en' ? 'en-US' : 'fr-FR';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso));
}

function publicLinkMode(link) {
  return PUBLIC_LINK_MODES[link.access_mode] || PUBLIC_LINK_MODES.read;
}

function publicLinkName(link) {
  return link.label || t(publicLinkMode(link).name);
}

function publicLinkStatusText(link) {
  if (!link.is_enabled) return t('modals.share.publicLink.rowDisabled');
  if (link.expired) return t('modals.share.publicLink.rowExpired', { date: formatPublicLinkDate(link.expires_at) });
  const parts = [];
  if (link.expires_at) parts.push(t('modals.share.publicLink.rowExpires', { date: formatPublicLinkDate(link.expires_at) }));
  parts.push(
    link.last_used_at
      ? t('modals.share.publicLink.rowUsed', { date: formatPublicLinkDate(link.last_used_at) })
      : t('modals.share.publicLink.rowUnused')
  );
  return parts.join(' · ');
}

// The expires_at to send for the expiry <select>'s current choice.
function publicLinkExpiresAt() {
  const choice = publicLinkEls.expiry.value;
  if (choice === 'never') return null;
  const days = Number(choice);
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().replace(/\.\d{3}Z$/, 'Z');
}

function selectedPublicLinkMode() {
  const checked = publicLinkEls.modeInputs.find((input) => input.checked);
  return checked ? checked.value : 'read';
}

function disarmPublicLinkRevoke() {
  publicLinkArmedId = null;
  if (publicLinkArmTimer) clearTimeout(publicLinkArmTimer);
  publicLinkArmTimer = null;
}

function buildPublicLinkRow(link) {
  const working = link.is_enabled && !link.expired;
  const mode = publicLinkMode(link);
  const name = publicLinkName(link);

  const li = document.createElement('li');
  li.className =
    'flex items-center gap-2 rounded-xl border border-slate-200 dark:border-slate-700 bg-white/60 dark:bg-slate-900/60 py-2 pl-3 pr-2' +
    (working ? '' : ' opacity-70');

  const info = document.createElement('div');
  info.className = 'min-w-0 flex-1';
  const title = document.createElement('p');
  title.className = 'truncate text-sm font-medium text-slate-900 dark:text-slate-100';
  title.textContent = name;
  const meta = document.createElement('p');
  meta.className = 'mt-0.5 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-slate-500 dark:text-slate-400';
  const badge = document.createElement('span');
  badge.className = 'inline-flex items-center gap-1 rounded-full bg-slate-200 dark:bg-slate-800 px-2 py-0.5 font-medium text-slate-700 dark:text-slate-300';
  const badgeIcon = document.createElement('span');
  badgeIcon.setAttribute('aria-hidden', 'true');
  badgeIcon.textContent = mode.icon;
  const badgeText = document.createElement('span');
  badgeText.textContent = t(mode.badge);
  badge.append(badgeIcon, badgeText);
  const status = document.createElement('span');
  status.className = working ? '' : 'font-medium text-amber-700 dark:text-amber-300';
  status.textContent = publicLinkStatusText(link);
  meta.append(badge, status);
  info.append(title, meta);

  const copyButton = document.createElement('button');
  copyButton.type = 'button';
  copyButton.className =
    'flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-base text-sky-600 dark:text-sky-400 transition hover:bg-sky-500/10 active:scale-95';
  copyButton.setAttribute('aria-label', t('modals.share.publicLink.copyAria', { name }));
  copyButton.title = t('modals.share.publicLink.copy');
  copyButton.textContent = publicLinkCopiedId === link.id ? '✅' : '📋';
  copyButton.addEventListener('click', () => copyPublicLink(link));

  const revokeButton = document.createElement('button');
  revokeButton.type = 'button';
  const armed = publicLinkArmedId === link.id;
  revokeButton.setAttribute('aria-label', t(armed ? 'modals.share.publicLink.confirmRevokeAria' : 'modals.share.publicLink.revokeAria', { name }));
  if (armed) {
    revokeButton.className =
      'flex h-11 shrink-0 items-center justify-center rounded-lg bg-rose-500/10 px-3 text-xs font-semibold text-rose-600 dark:text-rose-400 active:scale-95';
    revokeButton.textContent = t('modals.share.publicLink.confirmRevoke');
  } else {
    revokeButton.className =
      'flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-slate-500 dark:text-slate-400 transition hover:bg-rose-500/10 hover:text-rose-600 dark:hover:text-rose-400 active:scale-95';
    revokeButton.title = t('modals.share.publicLink.revoke');
    revokeButton.innerHTML = TRASH_ICON_SVG;
  }
  revokeButton.addEventListener('click', () => revokePublicLink(link));

  li.append(info, copyButton, revokeButton);
  return li;
}

function renderPublicLinkSection() {
  if (!publicLinks) {
    publicLinkEls.list.replaceChildren();
    publicLinkEls.newButton.hidden = true;
    return;
  }
  publicLinkEls.status.textContent = publicLinks.length === 0 ? t('modals.share.publicLink.empty') : '';
  publicLinkEls.status.hidden = publicLinks.length > 0;
  publicLinkEls.list.replaceChildren(...publicLinks.map(buildPublicLinkRow));
  const full = publicLinks.length >= PUBLIC_LINK_MAX_PER_LIST;
  publicLinkEls.newButton.hidden = !publicLinkEls.form.hidden || full;
  publicLinkEls.newButton.setAttribute('aria-expanded', String(!publicLinkEls.form.hidden));
}

function showPublicLinkError(err) {
  publicLinkEls.error.textContent = err && err.isNetworkError
    ? t('modals.share.publicLink.networkRequired')
    : (err && err.message) || t('modals.share.publicLink.networkRequired');
  publicLinkEls.error.hidden = false;
}

function resetPublicLinkForm() {
  publicLinkEls.form.reset();
  publicLinkEls.form.hidden = true;
  publicLinkEls.createButton.disabled = false;
}

// Called by shares.js's closeShareModal, and before showing another target.
function resetPublicLinkSection() {
  publicLinkListId = null;
  publicLinks = null;
  disarmPublicLinkRevoke();
  if (publicLinkCopiedTimer) clearTimeout(publicLinkCopiedTimer);
  publicLinkCopiedTimer = null;
  publicLinkCopiedId = null;
  publicLinkEls.section.hidden = true;
  publicLinkEls.error.hidden = true;
  publicLinkEls.status.hidden = false;
  resetPublicLinkForm();
  renderPublicLinkSection();
}

async function loadPublicLinks(listId) {
  let links;
  try {
    links = await apiRequest(publicLinksEndpoint());
  } catch (err) {
    if (publicLinkListId !== listId) return;
    publicLinkEls.status.hidden = false;
    publicLinkEls.status.textContent = err.isNetworkError ? t('modals.share.publicLink.networkRequired') : err.message;
    return;
  }
  if (publicLinkListId !== listId) return; // modal closed or retargeted meanwhile
  publicLinks = links;
  renderPublicLinkSection();
}

// Called by shares.js's openShareModal with the modal's target: shows and
// loads the section for a list, hides it for a Space.
async function refreshPublicLinkSection({ kind, id }) {
  resetPublicLinkSection();
  if (kind !== 'list') return;
  publicLinkListId = id;
  publicLinkEls.section.hidden = false;
  publicLinkEls.status.textContent = t('modals.share.publicLink.loading');
  await loadPublicLinks(id);
}

async function copyPublicLink(link) {
  const url = publicLinkPageUrl(link);
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(url);
    } else {
      legacyCopyToClipboard(url);
    }
  } catch {
    publicLinkEls.error.textContent = t('modals.share.publicLink.copyFailed', { url });
    publicLinkEls.error.hidden = false;
    return;
  }
  TrakkaToast.success(t('modals.share.publicLink.copied', { name: publicLinkName(link) }));
  if (publicLinkCopiedTimer) clearTimeout(publicLinkCopiedTimer);
  publicLinkCopiedId = link.id;
  publicLinkCopiedTimer = setTimeout(() => {
    publicLinkCopiedId = null;
    publicLinkCopiedTimer = null;
    renderPublicLinkSection();
  }, PUBLIC_LINK_COPIED_MS);
  renderPublicLinkSection();
}

async function revokePublicLink(link) {
  const listId = publicLinkListId;
  if (!listId) return;
  if (publicLinkArmedId !== link.id) {
    disarmPublicLinkRevoke();
    publicLinkArmedId = link.id;
    publicLinkArmTimer = setTimeout(() => {
      disarmPublicLinkRevoke();
      renderPublicLinkSection();
    }, PUBLIC_LINK_CONFIRM_WINDOW_MS);
    renderPublicLinkSection();
    return;
  }
  disarmPublicLinkRevoke();
  publicLinkEls.error.hidden = true;
  try {
    await apiRequest(`${publicLinksEndpoint()}/${link.id}`, { method: 'DELETE' });
    if (publicLinkListId !== listId) return;
    TrakkaToast.success(t('modals.share.publicLink.revoked', { name: publicLinkName(link) }));
  } catch (err) {
    if (publicLinkListId === listId) showPublicLinkError(err);
  }
  // Whatever happened, show what the server has now.
  if (publicLinkListId === listId) await loadPublicLinks(listId);
}

publicLinkEls.newButton.addEventListener('click', () => {
  publicLinkEls.error.hidden = true;
  publicLinkEls.form.hidden = false;
  renderPublicLinkSection();
  publicLinkEls.label.focus();
});

publicLinkEls.cancelButton.addEventListener('click', () => {
  resetPublicLinkForm();
  renderPublicLinkSection();
  publicLinkEls.newButton.focus();
});

publicLinkEls.form.addEventListener('submit', async (event) => {
  event.preventDefault();
  const listId = publicLinkListId;
  if (!listId) return;
  publicLinkEls.error.hidden = true;
  publicLinkEls.createButton.disabled = true;
  const label = publicLinkEls.label.value.trim();
  try {
    const created = await apiRequest(publicLinksEndpoint(), {
      method: 'POST',
      body: JSON.stringify({
        access_mode: selectedPublicLinkMode(),
        label: label || null,
        expires_at: publicLinkExpiresAt(),
      }),
    });
    if (publicLinkListId !== listId) return;
    TrakkaToast.success(t('modals.share.publicLink.created', { name: publicLinkName(created) }));
    resetPublicLinkForm();
    await loadPublicLinks(listId);
  } catch (err) {
    if (publicLinkListId === listId) showPublicLinkError(err);
  } finally {
    publicLinkEls.createButton.disabled = false;
  }
});

// Escape inside the form closes the form, not the whole share modal.
publicLinkEls.form.addEventListener('keydown', (event) => {
  if (event.key !== 'Escape') return;
  event.stopPropagation();
  resetPublicLinkForm();
  renderPublicLinkSection();
  publicLinkEls.newButton.focus();
});

// The rows are built from script, not data-i18n.
document.addEventListener('trakka:lang-changed', () => {
  if (publicLinks) renderPublicLinkSection();
});
