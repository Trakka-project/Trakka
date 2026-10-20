'use strict';

// Nutrition scores (Nutri-Score / Yuka) on the items of a "Courses" list:
// the frontend of internal/handlers/nutrition.go. An item may carry
// `nutri_score` ("A" to "E") and `yuka_score` (0-100); the server always
// reports them, and this file decides whether they're shown — each user
// chooses per kind of screen in Paramètres → Général ("Scores
// nutritionnels"): `nutrition_scores_mobile` for a phone (the Android app,
// or a screen narrower than Tailwind's `md`, the breakpoint the mobile
// layout switches at) and `nutrition_scores_web` for a wider browser
// window. Both off by default; with neither on, nothing is shown — no
// badge, no edit field.
//
// The badge is a neutral rendition, not the official Nutri-Score logo: a
// rounded square holding the letter, on the A-to-E colour scale, with the
// letter's colour picked for contrast (≥ 4.5:1 on every grade — white on A
// and E, whose E is darkened from the logo's #E63E11 for that, dark on B, C
// and D; see .nutri-score-badge in base.css). The Yuka score is a coloured
// dot plus the number, the dot following Yuka's own four bands. Neither
// relies on colour alone: the letter/number is the information, and each
// badge has an aria-label ("Nutri-Score B", "Score Yuka : 88/100").
//
// Shares `state`, `t`, `renderItems`, `isQuickShopList` with app.js/
// list_view.js/quick-shop.js — the same classic-<script> shared scope.

const NUTRI_SCORE_GRADES = ['A', 'B', 'C', 'D', 'E'];

// Below Tailwind's `md` (768px), the layout is the phone one.
const NUTRITION_MOBILE_QUERY = window.matchMedia('(max-width: 767px)');

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

function isValidYukaScore(score) {
  return Number.isInteger(score) && score >= 0 && score <= 100;
}

// Yuka's own bands: excellent (75+), good (50-74), mediocre (25-49), poor.
function yukaBand(score) {
  if (score >= 75) return 'excellent';
  if (score >= 50) return 'good';
  if (score >= 25) return 'mediocre';
  return 'poor';
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

function buildYukaScoreBadge(score) {
  const badge = document.createElement('span');
  badge.className = 'yuka-score-badge';
  badge.setAttribute('role', 'img');
  const label = t('nutrition.yukaScoreLabel', { score });
  badge.setAttribute('aria-label', label);
  badge.title = label;
  const dot = document.createElement('span');
  dot.className = `yuka-score-badge__dot yuka-score-badge__dot--${yukaBand(score)}`;
  dot.setAttribute('aria-hidden', 'true');
  const value = document.createElement('span');
  value.setAttribute('aria-hidden', 'true');
  value.textContent = String(score);
  badge.append(dot, value);
  return badge;
}

// The badge(s) list_view.js's buildItemRow puts right before an item's
// title, or null: when this screen doesn't show the scores, or the item has
// none. A fixed-size, non-shrinking block, vertically centred by the row
// like the checkbox and the kebab, so it never moves them.
function buildNutritionBadge(item) {
  if (!nutritionScoresShown()) return null;
  const grade = isValidNutriScore(item.nutri_score) ? item.nutri_score : null;
  const yuka = isValidYukaScore(item.yuka_score) ? item.yuka_score : null;
  if (!grade && yuka === null) return null;
  const group = document.createElement('span');
  group.className = `nutrition-badges${item.done ? ' opacity-60' : ''}`;
  if (grade) group.appendChild(buildNutriScoreBadge(grade));
  if (yuka !== null) group.appendChild(buildYukaScoreBadge(yuka));
  return group;
}

// The scores a barcode lookup answered (models.BarcodeLookup), as the
// fields to send with the item it adds, or null when it knew none.
function nutritionFromLookup(found) {
  if (!found) return null;
  const scores = {};
  if (isValidNutriScore(found.nutri_score)) scores.nutri_score = found.nutri_score;
  if (isValidYukaScore(found.yuka_score)) scores.yuka_score = found.yuka_score;
  return Object.keys(scores).length > 0 ? scores : null;
}

// ---------------------------------------------------------------------------
// The edit form's "Scores nutritionnels" fields (#edit-item-nutrition)
// ---------------------------------------------------------------------------

const nutritionEls = {
  editFields: document.getElementById('edit-item-nutrition'),
  editNutriScore: document.getElementById('edit-item-nutri-score'),
  editYukaScore: document.getElementById('edit-item-yuka-score'),
};

// Called by openEditItemModal: shown on a Courses list for a user who shows
// the scores somewhere, filled with the item's.
function fillEditItemNutrition(item, list) {
  const shown = Boolean(list && isQuickShopList(list) && nutritionScoresEnabledAnywhere());
  nutritionEls.editFields.hidden = !shown;
  nutritionEls.editNutriScore.value = isValidNutriScore(item.nutri_score) ? item.nutri_score : '';
  nutritionEls.editYukaScore.value = isValidYukaScore(item.yuka_score) ? String(item.yuka_score) : '';
}

// Called by the edit form's submit: the PATCH fields for the scores
// ({nutri_score, yuka_score}, "" / null clearing them), {} when the fields
// are hidden, or null when the Yuka score typed isn't a whole number from 0
// to 100 (the caller shows the error).
function editItemNutritionPayload() {
  if (nutritionEls.editFields.hidden) return {};
  const raw = nutritionEls.editYukaScore.value.trim();
  let yuka = null;
  if (raw !== '') {
    yuka = Number(raw);
    if (!isValidYukaScore(yuka)) return null;
  }
  return { nutri_score: nutritionEls.editNutriScore.value, yuka_score: yuka };
}

// Crossing the `md` breakpoint (rotating a tablet, resizing a window)
// changes which preference applies: re-render the open list.
NUTRITION_MOBILE_QUERY.addEventListener('change', () => {
  if (state.currentList) renderItems();
});
