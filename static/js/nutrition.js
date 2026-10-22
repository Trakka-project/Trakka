'use strict';

// Nutrition scores (Nutri-Score / NOVA) on the items of a "Courses" list:
// the frontend of internal/handlers/nutrition.go. An item may carry
// `nutri_score` ("A" to "E") and `nova_group` (1 to 4, how processed it is);
// the server always reports them, and this file decides whether they're
// shown — each user chooses per kind of screen in Paramètres → Général
// ("Scores nutritionnels"): `nutrition_scores_mobile` for a phone (the
// Android app, or a screen narrower than Tailwind's `md`, the breakpoint the
// mobile layout switches at) and `nutrition_scores_web` for a wider browser
// window. Both off by default; with neither on, nothing is shown — no
// badge, no edit field. The NOVA group replaced a Yuka score (0-100) here:
// Yuka has no public API, so it could only ever be typed in. The API still
// reports a `yuka_score` stored before; the PWA neither shows nor sends it,
// so the server leaves it as it is.
//
// The badges are neutral renditions, not the official logos: the
// Nutri-Score a rounded square holding the letter, the NOVA group a disc
// holding the digit, on their colour scales, with text colours picked for
// ≥ 4.5:1 contrast (see .nutri-score-badge / .nova-group-badge in
// base.css). Neither relies on colour alone: the letter/digit is the
// information, and each badge has an aria-label ("Nutri-Score B",
// "NOVA 4 : ultra-transformé").
//
// Shares `state`, `t`, `renderItems`, `isQuickShopList` with app.js/
// list_view.js/quick-shop.js — the same classic-<script> shared scope.

const NUTRI_SCORE_GRADES = ['A', 'B', 'C', 'D', 'E'];

// Below Tailwind's `md` (768px), the layout is the phone one.
const NUTRITION_MOBILE_QUERY = window.matchMedia('(max-width: 767px)');

// The pages the panel's "En savoir plus" links open, per UI language: the
// Nutri-Score by its official body (Santé publique France, in French only)
// and by Open Food Facts, the NOVA classification by Open Food Facts.
const NUTRITION_HELP_LINKS = {
  fr: {
    nutriScoreOfficial: 'https://www.santepubliquefrance.fr/nutrition-et-activite-physique/nutri-score',
    nutriScoreOff: 'https://fr.openfoodfacts.org/nutriscore',
    nova: 'https://fr.openfoodfacts.org/nova',
  },
  en: {
    nutriScoreOfficial: 'https://www.santepubliquefrance.fr/nutrition-et-activite-physique/nutri-score',
    nutriScoreOff: 'https://world.openfoodfacts.org/nutriscore',
    nova: 'https://world.openfoodfacts.org/nova',
  },
};

// The Android app's native plugin (settings.js's androidAppPlugin, the same
// feature test): inside the app, even a tablet counts as mobile.
function isNutritionMobileContext() {
  const plugins = window.Capacitor && window.Capacitor.Plugins;
  return Boolean(plugins && plugins.TrakkaApp) || NUTRITION_MOBILE_QUERY.matches;
}

// Whether this screen shows nutrition scores, per the user's preference for
// its kind. Before /me resolves (state.currentUser null), nothing is shown:
// the feature is opt-in.
function nutritionScoresShown() {
  const user = state.currentUser;
  if (!user) return false;
  return Boolean(isNutritionMobileContext() ? user.nutrition_scores_mobile : user.nutrition_scores_web);
}

// Whether the user shows nutrition scores on any kind of screen: the edit
// form's score fields are offered then, on a Courses list.
function nutritionScoresEnabledAnywhere() {
  const user = state.currentUser;
  return Boolean(user && (user.nutrition_scores_mobile || user.nutrition_scores_web));
}

function isValidNutriScore(grade) {
  return NUTRI_SCORE_GRADES.includes(grade);
}

function isValidNovaGroup(group) {
  return Number.isInteger(group) && group >= 1 && group <= 4;
}

function buildNutriScoreBadge(grade) {
  const badge = document.createElement('span');
  badge.className = `nutri-score-badge nutri-score-badge--${grade.toLowerCase()}`;
  badge.setAttribute('role', 'img');
  const label = t('nutrition.nutriScoreLabel', { grade });
  badge.setAttribute('aria-label', label);
  badge.title = label;
  // The letter is drawn by the span's text, hidden from assistive tech in
  // favour of the label (a lone "B" read out says nothing).
  const letter = document.createElement('span');
  letter.setAttribute('aria-hidden', 'true');
  letter.textContent = grade;
  badge.appendChild(letter);
  return badge;
}

function buildNovaGroupBadge(group) {
  const badge = document.createElement('span');
  badge.className = `nova-group-badge nova-group-badge--${group}`;
  badge.setAttribute('role', 'img');
  const label = t('nutrition.novaGroupLabel', { group, name: t(`nutrition.novaGroupName${group}`) });
  badge.setAttribute('aria-label', label);
  badge.title = label;
  const digit = document.createElement('span');
  digit.setAttribute('aria-hidden', 'true');
  digit.textContent = String(group);
  badge.appendChild(digit);
  return badge;
}

// The badges of an item's scores, in display order (none when unknown).
function buildNutritionBadgeList(grade, group) {
  const badges = [];
  if (isValidNutriScore(grade)) badges.push(buildNutriScoreBadge(grade));
  if (isValidNovaGroup(group)) badges.push(buildNovaGroupBadge(group));
  return badges;
}

// The badge(s) list_view.js's buildItemRow puts right before an item's
// title, or null: when this screen doesn't show the scores, or the item has
// none. A fixed-size, non-shrinking block, vertically centred by the row
// like the checkbox and the kebab, so it never moves them.
function buildNutritionBadge(item) {
  if (!nutritionScoresShown()) return null;
  const badges = buildNutritionBadgeList(item.nutri_score, item.nova_group);
  if (badges.length === 0) return null;
  const group = document.createElement('span');
  group.className = `nutrition-badges${item.done ? ' opacity-60' : ''}`;
  group.append(...badges);
  return group;
}

// The scores a barcode lookup answered (models.BarcodeLookup), as the
// fields to send with the item it adds, or null when it knew none.
function nutritionFromLookup(found) {
  if (!found) return null;
  const scores = {};
  if (isValidNutriScore(found.nutri_score)) scores.nutri_score = found.nutri_score;
  if (isValidNovaGroup(found.nova_group)) scores.nova_group = found.nova_group;
  return Object.keys(scores).length > 0 ? scores : null;
}

// ---------------------------------------------------------------------------
// The edit form's nutrition panel (#edit-item-nutrition)
//
// A collapsed <details> — the item's badges in its summary, so the form
// stays as short as before — that opens on the A-to-E scale and the NOVA
// 1-to-4 scale (radio tiles), what each value means, links to official
// explanations (new tab), and, while either is unknown, a "Rechercher sur
// Open Food Facts" button (GET /houses/{id}/products/nutrition: the House
// catalog, then Open Food Facts' text search on the name in the form). A
// lookup only fills what's still empty in the form: nothing is stored
// before "Enregistrer", whose PATCH then teaches the House catalog like any
// edit.
// ---------------------------------------------------------------------------

const nutritionEls = {
  editFields: document.getElementById('edit-item-nutrition'),
  editSummary: document.getElementById('edit-item-nutrition-summary'),
  editNutriScoreInputs: Array.from(document.querySelectorAll('input[name="edit-item-nutri-score"]')),
  editNutriScoreDescription: document.getElementById('edit-item-nutri-score-description'),
  editNovaGroupInputs: Array.from(document.querySelectorAll('input[name="edit-item-nova-group"]')),
  editNovaGroupDescription: document.getElementById('edit-item-nova-group-description'),
  helpNutriScoreOfficial: document.getElementById('edit-item-nutri-score-help-official'),
  helpNutriScoreOff: document.getElementById('edit-item-nutri-score-help-off'),
  helpNova: document.getElementById('edit-item-nova-help-off'),
  lookup: document.getElementById('edit-item-nutrition-lookup'),
  lookupButton: document.getElementById('edit-item-nutrition-lookup-button'),
  lookupLabel: document.getElementById('edit-item-nutrition-lookup-label'),
  lookupStatus: document.getElementById('edit-item-nutrition-lookup-status'),
};

// The list of the item being edited, set by fillEditItemNutrition.
let nutritionEditList = null;
// The AbortController of the lookup in flight, null when none: a new
// fillEditItemNutrition (another item opened) cancels it.
let nutritionLookupController = null;
// The lookup's outcome to show, as an i18n key and what fills it (kept
// rather than the text so a language change re-translates it), null for
// none.
let nutritionLookupMessage = null;
// The server answered search "disabled" (PRODUCT_LOOKUP_ENABLED off): the
// button stays hidden for the rest of the session.
let nutritionLookupDisabled = false;

function selectedEditNutriScore() {
  const checked = nutritionEls.editNutriScoreInputs.find((input) => input.checked);
  return checked && isValidNutriScore(checked.value) ? checked.value : '';
}

function setEditNutriScore(grade) {
  for (const input of nutritionEls.editNutriScoreInputs) input.checked = input.value === grade;
}

// The NOVA group picked, or null for "Aucun".
function selectedEditNovaGroup() {
  const checked = nutritionEls.editNovaGroupInputs.find((input) => input.checked);
  const group = checked ? Number(checked.value) : NaN;
  return checked && checked.value !== '' && isValidNovaGroup(group) ? group : null;
}

function setEditNovaGroup(group) {
  const value = isValidNovaGroup(group) ? String(group) : '';
  for (const input of nutritionEls.editNovaGroupInputs) input.checked = input.value === value;
}

// Points the "En savoir plus" links at the pages in the UI language.
function applyNutritionHelpLinks() {
  const lang = window.TrakkaI18n && window.TrakkaI18n.getLang() === 'en' ? 'en' : 'fr';
  const links = NUTRITION_HELP_LINKS[lang];
  nutritionEls.helpNutriScoreOfficial.href = links.nutriScoreOfficial;
  nutritionEls.helpNutriScoreOff.href = links.nutriScoreOff;
  nutritionEls.helpNova.href = links.nova;
}

// "Nutri-Score B et NOVA 4" — what a lookup found, for its message.
function describeFoundScores(grade, group) {
  const parts = [];
  if (grade) parts.push(t('nutrition.nutriScoreLabel', { grade }));
  if (group) parts.push(t('nutrition.novaGroupShort', { group }));
  return parts.join(t('nutrition.and'));
}

// Redraws the panel from the form's current values: the summary's badges,
// each scale's description, and the lookup block.
function renderEditItemNutrition() {
  const grade = selectedEditNutriScore();
  const group = selectedEditNovaGroup();

  const badges = buildNutritionBadgeList(grade, group);
  if (badges.length === 0) {
    const none = document.createElement('span');
    none.className = 'text-xs font-normal text-slate-500 dark:text-slate-400';
    none.textContent = t('nutrition.noScores');
    badges.push(none);
  }
  nutritionEls.editSummary.replaceChildren(...badges);

  nutritionEls.editNutriScoreDescription.textContent = grade
    ? t(`nutrition.gradeDescription${grade}`)
    : t('nutrition.gradeUnknown');
  nutritionEls.editNovaGroupDescription.textContent = group
    ? t(`nutrition.novaGroupDescription${group}`)
    : t('nutrition.novaGroupUnknown');

  // The button while either is unknown; the block also stays up while it
  // reports a lookup's outcome (which may just have filled both).
  const canLookUp = (!grade || !group) && !nutritionLookupDisabled;
  nutritionEls.lookupButton.hidden = !canLookUp;
  nutritionEls.lookup.hidden = !canLookUp && !nutritionLookupMessage;
  nutritionEls.lookupStatus.textContent = nutritionLookupMessage
    ? t(nutritionLookupMessage.key, nutritionLookupParams(nutritionLookupMessage))
    : t('nutrition.lookupHint');
}

// A kept lookup message's params, translated now.
function nutritionLookupParams(message) {
  return {
    found: describeFoundScores(message.grade, message.group),
    product: message.product || '',
    name: message.name || '',
  };
}

function setNutritionLookupBusy(busy) {
  nutritionEls.lookupButton.disabled = busy;
  nutritionEls.lookupButton.setAttribute('aria-busy', String(busy));
  nutritionEls.lookupLabel.textContent = t(busy ? 'nutrition.lookupSearching' : 'nutrition.lookupButton');
}

function cancelNutritionLookup() {
  if (nutritionLookupController) nutritionLookupController.abort();
  nutritionLookupController = null;
  setNutritionLookupBusy(false);
}

// Called by openEditItemModal: shown on a Courses list for a user who shows
// the scores somewhere, filled with the item's, collapsed.
function fillEditItemNutrition(item, list) {
  const shown = Boolean(list && isQuickShopList(list) && nutritionScoresEnabledAnywhere());
  cancelNutritionLookup();
  nutritionEditList = shown ? list : null;
  nutritionLookupMessage = null;
  nutritionEls.editFields.hidden = !shown;
  nutritionEls.editFields.open = false;
  setEditNutriScore(isValidNutriScore(item.nutri_score) ? item.nutri_score : '');
  setEditNovaGroup(item.nova_group);
  applyNutritionHelpLinks();
  renderEditItemNutrition();
}

// Called by openEditItemModal for opts.focus 'nutrition': unfolds the panel
// (its toggle handler scrolls it into view) and focuses its summary. False
// when the panel isn't offered for this item.
function openEditItemNutritionPanel() {
  if (nutritionEls.editFields.hidden) return false;
  nutritionEls.editFields.open = true;
  nutritionEls.editFields.querySelector('summary').focus({ preventScroll: true });
  return true;
}

// The outcome of a lookup (models.NutritionLookup) as the message to show,
// given what it filled in the form.
function nutritionLookupOutcome(found, filledGrade, filledGroup) {
  if (filledGrade || filledGroup) {
    const message = { grade: filledGrade, group: filledGroup };
    if (found.source === 'house') return { key: 'nutrition.lookupFoundHouse', ...message };
    if (found.matched_name) return { key: 'nutrition.lookupFoundOff', product: found.matched_name, ...message };
    return { key: 'nutrition.lookupFoundOffBrand', ...message };
  }
  if (found.search === 'disabled') return { key: 'nutrition.lookupDisabled' };
  if (found.search === 'unavailable') return { key: 'nutrition.lookupUnavailable' };
  return { key: 'nutrition.lookupNotFound', name: found.name };
}

async function lookUpEditItemNutrition() {
  const list = nutritionEditList;
  // listEls is list_view.js's: the name as it stands in the form, edits
  // included.
  const name = listEls.editItemTitle.value.trim();
  if (!list || !name) {
    nutritionLookupMessage = { key: 'nutrition.lookupNoName' };
    renderEditItemNutrition();
    return;
  }
  cancelNutritionLookup();
  const controller = new AbortController();
  nutritionLookupController = controller;
  nutritionLookupMessage = { key: 'nutrition.lookupSearchingHint' };
  setNutritionLookupBusy(true);
  renderEditItemNutrition();

  let found = null;
  let failure = null;
  try {
    found = await apiRequest(`/houses/${list.house_id}/products/nutrition?name=${encodeURIComponent(name)}`, { signal: controller.signal });
  } catch (err) {
    failure = err;
  }
  // Cancelled meanwhile (another item opened): its answer is stale.
  if (nutritionLookupController !== controller) return;
  nutritionLookupController = null;
  setNutritionLookupBusy(false);

  if (failure || !found) {
    let key = 'nutrition.lookupFailed';
    if (isNetworkError(failure)) key = 'nutrition.lookupOffline';
    else if (failure && failure.status === 429) key = 'nutrition.lookupRateLimited';
    nutritionLookupMessage = { key };
    renderEditItemNutrition();
    return;
  }
  if (found.search === 'disabled') nutritionLookupDisabled = true;

  // Only what's still empty: a value picked meanwhile wins.
  let filledGrade = '';
  let filledGroup = null;
  if (isValidNutriScore(found.nutri_score) && !selectedEditNutriScore()) {
    setEditNutriScore(found.nutri_score);
    filledGrade = found.nutri_score;
  }
  if (isValidNovaGroup(found.nova_group) && selectedEditNovaGroup() === null) {
    setEditNovaGroup(found.nova_group);
    filledGroup = found.nova_group;
  }
  nutritionLookupMessage = nutritionLookupOutcome(found, filledGrade, filledGroup);
  renderEditItemNutrition();
}

// Called by the edit form's submit: the PATCH fields for the scores
// ({nutri_score, nova_group}, "" / null clearing them), or {} when the
// panel is hidden. The legacy yuka_score is never sent: the server leaves
// it as it is.
function editItemNutritionPayload() {
  if (nutritionEls.editFields.hidden) return {};
  return { nutri_score: selectedEditNutriScore(), nova_group: selectedEditNovaGroup() };
}

for (const input of [...nutritionEls.editNutriScoreInputs, ...nutritionEls.editNovaGroupInputs]) {
  input.addEventListener('change', () => {
    // A value picked by hand: the lookup's outcome no longer applies.
    nutritionLookupMessage = null;
    renderEditItemNutrition();
  });
}
nutritionEls.lookupButton.addEventListener('click', lookUpEditItemNutrition);

// Opened, the panel is brought into view inside the form's own scroll
// (the modal is a bottom sheet on a phone, the panel often below the fold).
nutritionEls.editFields.addEventListener('toggle', () => {
  if (!nutritionEls.editFields.open) return;
  const smooth = !window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  requestAnimationFrame(() => {
    nutritionEls.editFields.scrollIntoView({ block: 'nearest', behavior: smooth ? 'smooth' : 'auto' });
  });
});

document.addEventListener('trakka:lang-changed', () => {
  if (!nutritionEls.editFields.hidden) {
    setNutritionLookupBusy(Boolean(nutritionLookupController));
    applyNutritionHelpLinks();
    renderEditItemNutrition();
  }
});

// ---------------------------------------------------------------------------
// The item actions sheet's "🥗 Scores nutritionnels" row
//
// A tap on an item's title opens #item-actions-sheet, not the edit modal:
// this row puts the scores on that first screen (the badges, or "Aucun
// score"), and opens the edit modal on its nutrition panel, unfolded.
// ---------------------------------------------------------------------------

const nutritionActionsEls = {
  button: document.getElementById('item-actions-nutrition-button'),
  value: document.getElementById('item-actions-nutrition-value'),
};

// Called by list_view.js's openItemActionsSheet: shown where the edit form
// offers the panel (fillEditItemNutrition's condition).
function updateItemActionsNutritionButton(item) {
  const list = state.currentList;
  const show = Boolean(list && isQuickShopList(list) && nutritionScoresEnabledAnywhere());
  nutritionActionsEls.button.hidden = !show;
  if (!show) return;
  const badges = buildNutritionBadgeList(item.nutri_score, item.nova_group);
  if (badges.length === 0) {
    nutritionActionsEls.value.replaceChildren();
    nutritionActionsEls.value.textContent = t('nutrition.noScores');
  } else {
    nutritionActionsEls.value.replaceChildren(...badges);
  }
}

nutritionActionsEls.button.addEventListener('click', () => {
  // itemActionsSheetItem/closeItemActionsSheet are list_view.js's.
  const item = itemActionsSheetItem;
  closeItemActionsSheet();
  if (item) openEditItemModal(item, { focus: 'nutrition' });
});

// Crossing the `md` breakpoint (rotating a tablet, resizing a window)
// changes which preference applies: re-render the open list.
NUTRITION_MOBILE_QUERY.addEventListener('change', () => {
  if (state.currentList) renderItems();
});
