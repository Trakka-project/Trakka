'use strict';

// "Répétition personnalisée" — the custom recurrence sheet
// (#recurrence-editor-sheet in index.html), opened by list_view.js's
// wireRecurrenceSelect when "Personnalisée…" is picked in the quick-add
// panel or the edit-item modal, or from the rule summary shown under either
// select. It builds a rule in the canonical RRULE subset of
// static/js/recurrence.js: a frequency (Quotidienne/Hebdomadaire/Mensuelle/
// Annuelle), an interval, the weekdays of a weekly rule (round L M M J V S D
// toggles, several at once — "FREQ=WEEKLY;BYDAY=MO,WE,FR"), and a start
// date. The start date isn't stored: applying hands the caller the first
// occurrence on or after it (TrakkaRecurrence.firstOccurrenceOnOrAfter),
// which becomes the task's due date — the current occurrence every later
// one steps from.
//
// Stacks above the z-40 edit-item modal (z-50). Its id ends in "-sheet", so
// gestures.js's system-back handling covers it with nothing to register: a
// back dispatches a click on the backdrop, which cancels, like ✕, "Annuler"
// and Escape. Uses t() (i18n.js), TrakkaRecurrence (recurrence.js), and
// formatDueLabel/recurrenceBadgeLabel (list_view.js), resolved at call time.

const recurrenceEditorEls = {
  sheet: document.getElementById('recurrence-editor-sheet'),
  form: document.getElementById('recurrence-editor-form'),
  closeButton: document.getElementById('recurrence-editor-close-button'),
  cancelButton: document.getElementById('recurrence-editor-cancel-button'),
  applyButton: document.getElementById('recurrence-editor-apply-button'),
  freqButtons: Array.from(document.querySelectorAll('[data-recurrence-freq]')),
  interval: document.getElementById('recurrence-editor-interval'),
  decrement: document.getElementById('recurrence-editor-interval-decrement'),
  increment: document.getElementById('recurrence-editor-interval-increment'),
  unit: document.getElementById('recurrence-editor-unit'),
  daysField: document.getElementById('recurrence-editor-days-field'),
  dayButtons: Array.from(document.querySelectorAll('[data-recurrence-weekday]')),
  daysError: document.getElementById('recurrence-editor-days-error'),
  start: document.getElementById('recurrence-editor-start'),
  summaryRule: document.getElementById('recurrence-editor-summary-rule'),
  summaryFirst: document.getElementById('recurrence-editor-summary-first'),
};

// Singular/plural unit shown after the interval ("2 semaines").
const RECURRENCE_UNIT_KEYS = {
  DAILY: ['recurrenceEditor.unitDay', 'recurrenceEditor.unitDays'],
  WEEKLY: ['recurrenceEditor.unitWeek', 'recurrenceEditor.unitWeeks'],
  MONTHLY: ['recurrenceEditor.unitMonth', 'recurrenceEditor.unitMonths'],
  YEARLY: ['recurrenceEditor.unitYear', 'recurrenceEditor.unitYears'],
};

// The open session: { freq, interval, byDay (Set of Monday-first indices),
// onApply, onCancel, returnFocus, addedScrollLock }, or null when closed.
let recurrenceEditorSession = null;

function isRecurrenceEditorOpen() {
  return recurrenceEditorSession !== null;
}

// The rule the sheet currently describes, or null while it's incomplete (a
// weekly rule with no day, or no valid start date).
function recurrenceEditorRule() {
  const session = recurrenceEditorSession;
  if (!session) return null;
  if (session.freq === 'WEEKLY' && session.byDay.size === 0) return null;
  if (!/^\d{4}-\d{2}-\d{2}$/.test(recurrenceEditorEls.start.value)) return null;
  return {
    freq: session.freq,
    interval: session.interval,
    byDay: session.freq === 'WEEKLY' ? [...session.byDay].sort((a, b) => a - b) : [],
  };
}

function renderRecurrenceEditor() {
  const session = recurrenceEditorSession;
  const els = recurrenceEditorEls;
  for (const button of els.freqButtons) {
    const checked = button.dataset.recurrenceFreq === session.freq;
    button.setAttribute('aria-checked', String(checked));
    button.tabIndex = checked ? 0 : -1;
  }

  els.interval.value = String(session.interval);
  els.decrement.disabled = session.interval <= 1;
  els.increment.disabled = session.interval >= TrakkaRecurrence.MAX_INTERVAL;
  const [singular, plural] = RECURRENCE_UNIT_KEYS[session.freq];
  els.unit.textContent = t(session.interval === 1 ? singular : plural);

  const weekly = session.freq === 'WEEKLY';
  els.daysField.hidden = !weekly;
  for (const button of els.dayButtons) {
    button.setAttribute('aria-pressed', String(session.byDay.has(Number(button.dataset.recurrenceWeekday))));
  }
  els.daysError.hidden = !weekly || session.byDay.size > 0;

  const rule = recurrenceEditorRule();
  els.applyButton.disabled = !rule;
  els.summaryRule.textContent = rule ? recurrenceBadgeLabel(rule) || '' : '';
  const first = rule ? TrakkaRecurrence.firstOccurrenceOnOrAfter(els.start.value, rule) : null;
  els.summaryFirst.textContent = first ? t('recurrenceEditor.firstOccurrence', { date: formatDueLabel(first) }) : '';
}

// A weekly rule with no day yet starts on the start date's own weekday, the
// most likely intent and RRULE's own default.
function preselectStartWeekday() {
  const session = recurrenceEditorSession;
  if (session.freq !== 'WEEKLY' || session.byDay.size > 0) return;
  const index = TrakkaRecurrence.weekdayIndexOf(recurrenceEditorEls.start.value);
  if (index !== null) session.byDay.add(index);
}

// openRecurrenceEditor({ rule, start, returnFocus, onApply, onCancel }):
// `rule` seeds the sheet (any accepted spelling; empty starts on
// "Hebdomadaire"), `start` is the initial "Date de début" (YYYY-MM-DD).
// onApply(rule, firstDate) receives the canonical rule and the first
// occurrence on or after the start date; onCancel() runs on every other way
// out. returnFocus is an element, or a function returning one, focused once
// the sheet has closed and the caller has updated its form.
function openRecurrenceEditor({ rule, start, returnFocus, onApply, onCancel }) {
  if (recurrenceEditorSession) closeRecurrenceEditor(false);
  const parsed = TrakkaRecurrence.parseRule(rule);
  recurrenceEditorSession = {
    freq: parsed ? parsed.freq : 'WEEKLY',
    interval: parsed ? parsed.interval : 1,
    byDay: new Set(parsed ? parsed.byDay : []),
    onApply,
    onCancel,
    returnFocus,
    addedScrollLock: !document.body.classList.contains('overflow-hidden'),
  };
  recurrenceEditorEls.start.value = start || TrakkaRecurrence.localDateISO();
  preselectStartWeekday();
  renderRecurrenceEditor();

  recurrenceEditorEls.sheet.hidden = false;
  if (recurrenceEditorSession.addedScrollLock) document.body.classList.add('overflow-hidden');
  const checked = recurrenceEditorEls.freqButtons.find((button) => button.getAttribute('aria-checked') === 'true');
  (checked || recurrenceEditorEls.freqButtons[0]).focus();
}

function closeRecurrenceEditor(applied) {
  const session = recurrenceEditorSession;
  if (!session) return;
  const rule = applied ? recurrenceEditorRule() : null;
  recurrenceEditorSession = null;
  recurrenceEditorEls.sheet.hidden = true;
  if (session.addedScrollLock) document.body.classList.remove('overflow-hidden');

  if (rule) {
    session.onApply?.(TrakkaRecurrence.formatRule(rule), TrakkaRecurrence.firstOccurrenceOnOrAfter(recurrenceEditorEls.start.value, rule));
  } else {
    session.onCancel?.();
  }
  const target = typeof session.returnFocus === 'function' ? session.returnFocus() : session.returnFocus;
  if (target && typeof target.focus === 'function') target.focus();
}

function setRecurrenceEditorFreq(freq) {
  recurrenceEditorSession.freq = freq;
  preselectStartWeekday();
  renderRecurrenceEditor();
}

for (const button of recurrenceEditorEls.freqButtons) {
  button.addEventListener('click', () => setRecurrenceEditorFreq(button.dataset.recurrenceFreq));
}

// Arrow keys move the choice within the frequency radiogroup (the WAI-ARIA
// radio group pattern — only the checked pill is in the tab order).
recurrenceEditorEls.freqButtons[0].parentElement.addEventListener('keydown', (event) => {
  const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key];
  if (!step || !recurrenceEditorSession) return;
  event.preventDefault();
  const buttons = recurrenceEditorEls.freqButtons;
  const current = buttons.findIndex((button) => button.dataset.recurrenceFreq === recurrenceEditorSession.freq);
  const next = buttons[(current + step + buttons.length) % buttons.length];
  setRecurrenceEditorFreq(next.dataset.recurrenceFreq);
  next.focus();
});

for (const button of recurrenceEditorEls.dayButtons) {
  button.addEventListener('click', () => {
    const index = Number(button.dataset.recurrenceWeekday);
    const days = recurrenceEditorSession.byDay;
    if (days.has(index)) days.delete(index);
    else days.add(index);
    renderRecurrenceEditor();
  });
}

function setRecurrenceEditorInterval(value) {
  const n = Number.parseInt(value, 10);
  recurrenceEditorSession.interval = Math.min(TrakkaRecurrence.MAX_INTERVAL, Math.max(1, Number.isFinite(n) ? n : 1));
  renderRecurrenceEditor();
}

recurrenceEditorEls.decrement.addEventListener('click', () => setRecurrenceEditorInterval(recurrenceEditorSession.interval - 1));
recurrenceEditorEls.increment.addEventListener('click', () => setRecurrenceEditorInterval(recurrenceEditorSession.interval + 1));
// Typing updates the summary live but only clamps (and rewrites the field)
// on commit, so clearing the field to type a new number isn't fought.
recurrenceEditorEls.interval.addEventListener('input', () => {
  const n = Number.parseInt(recurrenceEditorEls.interval.value, 10);
  if (!Number.isFinite(n) || n < 1 || n > TrakkaRecurrence.MAX_INTERVAL) return;
  recurrenceEditorSession.interval = n;
  const caret = recurrenceEditorEls.interval.value;
  renderRecurrenceEditor();
  recurrenceEditorEls.interval.value = caret;
});
recurrenceEditorEls.interval.addEventListener('change', () => setRecurrenceEditorInterval(recurrenceEditorEls.interval.value));

recurrenceEditorEls.start.addEventListener('input', () => {
  preselectStartWeekday();
  renderRecurrenceEditor();
});

recurrenceEditorEls.form.addEventListener('submit', (event) => {
  event.preventDefault();
  if (recurrenceEditorRule()) closeRecurrenceEditor(true);
});
recurrenceEditorEls.cancelButton.addEventListener('click', () => closeRecurrenceEditor(false));
recurrenceEditorEls.closeButton.addEventListener('click', () => closeRecurrenceEditor(false));
recurrenceEditorEls.sheet.addEventListener('click', (event) => {
  if (event.target === recurrenceEditorEls.sheet) closeRecurrenceEditor(false);
});

// Capture phase on window, ahead of every document-level Escape handler —
// the same reasoning as emoji-picker.js: an Escape meant for this sheet must
// not also close the edit-item modal underneath it.
window.addEventListener('keydown', (event) => {
  if (event.key !== 'Escape' || !isRecurrenceEditorOpen()) return;
  event.preventDefault();
  event.stopPropagation();
  closeRecurrenceEditor(false);
}, true);

// A language switch while the sheet is open re-renders its script-built
// text (unit, summary); the static labels follow data-i18n on their own.
document.addEventListener('trakka:lang-changed', () => {
  if (isRecurrenceEditorOpen()) renderRecurrenceEditor();
});
