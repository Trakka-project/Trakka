'use strict';

// Touch gesture layer for the mobile PWA experience: a left-edge swipe-back
// gesture (closes the topmost open modal/bottom sheet, or returns from a
// list's detail view to the dashboard) and per-item swipe actions in the
// list detail view (swipe right to toggle done, swipe left to delete).
//
// Every listener in this file is `{ passive: true }` and none of them ever
// calls `preventDefault` — the visual feedback is applied purely by reading
// touch coordinates and writing CSS transforms/opacity, never by fighting
// the browser's own default handling. That's also what keeps native
// vertical scrolling (of the item list, or the page as a whole) completely
// unaffected: a gesture is only ever classified as a horizontal swipe once
// its first ~10px of movement clearly dominate on the X axis (mirroring
// attachLongPress's own LONG_PRESS_MOVE_TOLERANCE_PX in list_view.js); the
// moment it looks vertical instead, this file stops touching the element
// entirely and lets the page scroll exactly as it would if this file didn't
// exist. Item cards additionally get `touch-action: pan-y` (see
// attachItemSwipeGestures below) so the browser itself never even considers
// a horizontal drag there a candidate for its own scroll/navigation
// handling, leaving that axis free for the transform-based feedback below
// without any passive/preventDefault tension at all.
//
// Both gestures are touch-only by construction (they only ever attach
// `touchstart`/`touchmove`/`touchend` listeners) — a desktop mouse never
// fires those events, so neither gesture does anything there, and
// IS_TOUCH_DEVICE below additionally skips the one-time DOM restructuring
// attachItemSwipeGestures otherwise does to every item row, so a
// mouse-only/no-touchscreen session doesn't carry that overhead for a
// feature it can never trigger.
const IS_TOUCH_DEVICE = 'ontouchstart' in window || (navigator.maxTouchPoints || 0) > 0;

function gestureVibrate(ms) {
  if (!navigator.vibrate) return;
  try {
    navigator.vibrate(ms);
  } catch {
    // Vibration blocked/unsupported — a purely cosmetic touch, skip it, same
    // as attachLongPress's own vibrate call in list_view.js.
  }
}

// ---------------------------------------------------------------------------
// 1. Left-edge swipe-back
// ---------------------------------------------------------------------------

const EDGE_BACK_ZONE_PX = 30; // how close to the left edge a touch must start
const EDGE_BACK_THRESHOLD_PX = 90; // drag distance required to actually trigger "back"
const EDGE_BACK_AXIS_LOCK_PX = 10;
const EDGE_BACK_MAIN_PULL_PX = 28; // how far <main> nudges right at full progress — a hint, not a page transition

// Every modal/bottom sheet in index.html follows the same convention: a
// full-screen backdrop `<div id="...-modal">`/`<div id="...-sheet">` that
// closes itself via its own `click` listener checking
// `event.target === <itself>` (see the `event.target ===` checks in
// app.js/list_view.js/spaces.js/shares.js/notifications.js/admin.js/
// settings.js) — exactly what a real click on the backdrop, outside the
// dialog box, already does. Dispatching a synthetic click directly on that
// element reuses each modal's own close logic without this file needing to
// know a single closeXModal function name, so a new modal/sheet added later
// is covered automatically as long as it follows the same id convention —
// the same reasoning that already lets a single Escape keydown listener per
// modal close all of them independently without any shared registry.
const OVERLAY_SELECTOR = '[id$="-modal"]:not([hidden]), [id$="-sheet"]:not([hidden])';

// Open overlays in the order they were opened, topmost last. Stacked
// overlays (the emoji picker above the list modal) don't reveal their
// z-order in the DOM, so refreshOpenOverlays() keeps this in sync with the
// DOM instead: overlays that closed are dropped, newly opened ones are
// appended. It runs from the history MutationObserver (section 3)
// on every `hidden` change, and again right before it's read.
let openOverlays = [];

function refreshOpenOverlays() {
  const open = Array.from(document.querySelectorAll(OVERLAY_SELECTOR));
  openOverlays = openOverlays.filter((el) => open.includes(el));
  open.forEach((el) => {
    if (!openOverlays.includes(el)) openOverlays.push(el);
  });
}

function isListViewOpen() {
  const itemsSection = document.getElementById('items-section');
  return !!itemsSection && !itemsSection.hidden;
}

// Closes whatever's "on top" right now: the most recently opened
// modal/sheet first, else the list detail view back to the dashboard (via a
// real .click() on the header back button, reusing its existing handler in
// list_view.js rather than duplicating the showDashboard() call here) — the
// same two things the header's back button and a modal's own backdrop click
// already do individually. One level per call, so backing out of the emoji
// picker returns to the list modal underneath it. A no-op at the dashboard
// root, same as pressing Escape there today.
function goBack() {
  refreshOpenOverlays();
  const top = openOverlays[openOverlays.length - 1];
  if (top) {
    top.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window }));
    return;
  }
  const backButton = document.getElementById('back-button');
  if (isListViewOpen() && backButton) backButton.click();
}

// A small pill that slides in from the left edge as the gesture progresses —
// created once and reused for every gesture rather than rebuilt per-swipe,
// since it's page-level chrome, not tied to any one element. Purely
// decorative (aria-hidden): every action it precedes (closing a modal,
// leaving a list) already has its own accessible control, so this is a
// progressive-enhancement affordance, not a UI element that needs to be
// independently reachable.
let backIndicatorEl = null;
function getBackIndicator() {
  if (backIndicatorEl) return backIndicatorEl;
  const el = document.createElement('div');
  el.setAttribute('aria-hidden', 'true');
  el.className =
    'pointer-events-none fixed left-2 top-1/2 z-50 flex h-10 w-10 items-center justify-center rounded-full bg-slate-900/85 dark:bg-slate-100/90 text-white dark:text-slate-900 opacity-0 shadow-lg';
  el.style.transform = 'translateY(-50%) scale(0.6)';
  el.style.transition = 'opacity 120ms ease, transform 120ms ease';
  el.innerHTML =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="h-5 w-5" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg>';
  document.body.appendChild(el);
  backIndicatorEl = el;
  return el;
}

// progress is dx / EDGE_BACK_THRESHOLD_PX — 0 at the start of the gesture,
// 1 once it's far enough to trigger on release. <main> has none of the
// `bg-`/`border-`/`text-` classes base.css's global theme-crossfade rule
// matches, so setting its transition here can't clobber that (see
// attachItemSwipeGestures below, which does have to account for it).
function applyEdgeBackFeedback(progress) {
  const main = document.querySelector('main');
  const indicator = getBackIndicator();
  const clamped = Math.min(Math.max(progress, 0), 1);
  if (main) main.style.transform = `translateX(${clamped * EDGE_BACK_MAIN_PULL_PX}px)`;
  indicator.style.opacity = String(clamped);
  indicator.style.transform = `translateY(-50%) scale(${0.6 + clamped * 0.4})`;
}

function resetEdgeBackFeedback() {
  const main = document.querySelector('main');
  const indicator = getBackIndicator();
  if (main) {
    main.style.transition = 'transform 180ms ease';
    main.style.transform = 'translateX(0)';
    // A `transform` other than `none` — even `translateX(0)` — makes <main>
    // a new containing block for every `position: fixed` descendant it has
    // (the mobile quick-add bar in #create-item-form-anchor, the reorder
    // action bar), so they start resolving `bottom: 0` against <main>'s own
    // box instead of the viewport and scroll away with the page instead of
    // staying pinned — exactly the "quick-add bar disappears while
    // scrolling" bug this clears. Clearing both inline styles once the
    // snap-back animation actually finishes (never mid-drag, since a
    // transform is still needed for the pull feedback while tracking)
    // returns <main> to its ordinary untransformed state so nothing inside
    // it keeps behaving like `position: absolute` for the rest of the
    // session — settleThenRun (defined below, hoisted) is the same
    // transitionend-or-fallback-timer helper the item-swipe gesture already
    // uses for the same "wait for the animation, then act" reasoning.
    settleThenRun(main, () => {
      main.style.transform = '';
      main.style.transition = '';
    });
  }
  indicator.style.transition = 'opacity 180ms ease, transform 180ms ease';
  indicator.style.opacity = '0';
  indicator.style.transform = 'translateY(-50%) scale(0.6)';
}

if (IS_TOUCH_DEVICE) {
  (function initEdgeSwipeBack() {
    let tracking = false;
    let startX = 0;
    let startY = 0;
    let axisLocked = null; // 'x' | 'y' | null
    let lastDx = 0;

    document.addEventListener(
      'touchstart',
      (event) => {
        if (event.touches.length !== 1) {
          // A second finger landing mid-drag abandons tracking outright
          // (no "which finger wins" logic here) — but if a drag was already
          // in progress, <main> may already be carrying the pull-feedback
          // transform from applyEdgeBackFeedback below, and abandoning
          // tracking here means neither touchend nor touchcancel's finish()
          // will ever run for this gesture (both bail out on `!tracking`).
          // Without this reset, that transform — even mid-drag, not just
          // the settled translateX(0) resetEdgeBackFeedback normally clears
          // — would stick to <main> for the rest of the session, silently
          // turning it into the containing block for every `position: fixed`
          // descendant it has (the quick-add bar, the reorder action bar)
          // and breaking their viewport anchoring exactly like an unpaired
          // resetEdgeBackFeedback call would.
          if (tracking) resetEdgeBackFeedback();
          tracking = false;
          return;
        }
        const touch = event.touches[0];
        // Item cards run their own independent swipe gesture (see
        // attachItemSwipeGestures below) — a touch starting on one is left
        // entirely to that handler rather than tracked twice. Likewise for
        // #dashboard-tabs, already a native horizontally-scrollable strip
        // (see base.css) that this gesture's own visual "pull" would
        // otherwise visually fight with for no benefit, since it's already
        // scrolled fully left whenever this zone is reachable on it anyway.
        // .reorder-row (static/js/reorder.js) is excluded for the same
        // reason as .swipe-item: a card's own drag handle can sit close
        // enough to the left edge on a narrow phone to fall inside
        // EDGE_BACK_ZONE_PX, and a manual reorder drag must never be
        // hijacked into an edge-back navigation gesture instead.
        // #create-item-form-anchor (the quick-add bar, pinned to the bottom
        // of the viewport on mobile — see its responsive classes in
        // index.html) is excluded too: its text input/buttons span the full
        // width of that bottom strip, so a tap starting near its left edge
        // must reach the input, not get hijacked into a back gesture. The
        // same goes for #bulk-actions-bar (multi-select mode, selection.js),
        // whose "Annuler" button sits at that same left edge.
        if (
          touch.clientX > EDGE_BACK_ZONE_PX ||
          event.target.closest('.swipe-item') ||
          event.target.closest('.reorder-row') ||
          event.target.closest('#dashboard-tabs') ||
          event.target.closest('#create-item-form-anchor') ||
          event.target.closest('#bulk-actions-bar')
        ) {
          tracking = false;
          return;
        }
        tracking = true;
        startX = touch.clientX;
        startY = touch.clientY;
        axisLocked = null;
        lastDx = 0;
        const main = document.querySelector('main');
        if (main) main.style.transition = 'none';
      },
      { passive: true },
    );

    document.addEventListener(
      'touchmove',
      (event) => {
        if (!tracking) return;
        const touch = event.touches[0];
        const dx = touch.clientX - startX;
        const dy = touch.clientY - startY;

        if (axisLocked === null) {
          if (Math.abs(dx) < EDGE_BACK_AXIS_LOCK_PX && Math.abs(dy) < EDGE_BACK_AXIS_LOCK_PX) return;
          axisLocked = Math.abs(dx) > Math.abs(dy) ? 'x' : 'y';
          if (axisLocked === 'y' || dx < 0) {
            // A vertical scroll, or a leftward drag from the edge (not a
            // "back" gesture) — nothing was ever prevented, so there's
            // nothing to undo; just stop reacting to this touch.
            tracking = false;
            return;
          }
        }
        if (axisLocked !== 'x') return;

        lastDx = dx;
        applyEdgeBackFeedback(dx / EDGE_BACK_THRESHOLD_PX);
      },
      { passive: true },
    );

    function finish() {
      if (!tracking) return;
      tracking = false;
      const triggered = lastDx >= EDGE_BACK_THRESHOLD_PX;
      resetEdgeBackFeedback();
      if (triggered) {
        gestureVibrate(15);
        requestBack();
      }
      axisLocked = null;
      lastDx = 0;
    }

    document.addEventListener('touchend', finish, { passive: true });
    document.addEventListener('touchcancel', finish, { passive: true });
  })();
}

// ---------------------------------------------------------------------------
// 2. Item card swipe actions — list detail view only (see the single call
//    site in buildItemRow, list_view.js): swipe right to toggle done (green
//    background + ✓), swipe left to delete (red background + 🗑️). Both
//    reuse toggleDone/removeItem exactly as the checkbox/trash button
//    already do, including removeItem's own 5s undo-toast grace period, so
//    a swipe-to-delete is never permanent without a chance to undo it.
// ---------------------------------------------------------------------------

const ITEM_SWIPE_THRESHOLD_PX = 80; // minimum drag distance before a release commits the action
const ITEM_SWIPE_MAX_PX = 120; // visual travel cap before rubber-banding kicks in
const ITEM_SWIPE_AXIS_LOCK_PX = 10;

// A full-bleed colored layer behind the card's sliding foreground — see
// attachItemSwipeGestures below for how the two stack. `border-radius:
// inherit` (set via inline style rather than a Tailwind arbitrary-value
// class, to avoid any dependency on which Tailwind version the Play CDN
// happens to be serving) picks up whatever rounding the card itself has
// (rounded-2xl) so the color never spills past the card's own corners.
function buildSwipeBackground(kind) {
  const isDone = kind === 'done';
  const bg = document.createElement('div');
  bg.setAttribute('aria-hidden', 'true');
  bg.className =
    'pointer-events-none absolute inset-0 flex items-center px-6 text-2xl text-white opacity-0 ' +
    (isDone ? 'justify-start bg-emerald-500' : 'justify-end bg-rose-500');
  bg.style.borderRadius = 'inherit';
  bg.style.transition = 'opacity 120ms ease';
  const icon = document.createElement('span');
  icon.textContent = isDone ? '✓' : '🗑️';
  bg.appendChild(icon);
  return bg;
}

// Runs `callback` once the CSS transition on `el` finishes (or after a fixed
// fallback delay, in case `el` gets detached — e.g. by an unrelated
// renderItems() re-render firing before the transition completes — and
// never dispatches `transitionend` at all) — used so a swiped-past-threshold
// card visually finishes sliding away before the underlying toggleDone/
// removeItem mutation actually applies, rather than the two happening in
// the same instant.
function settleThenRun(el, callback) {
  let done = false;
  function run() {
    if (done) return;
    done = true;
    el.removeEventListener('transitionend', run);
    callback();
  }
  el.addEventListener('transitionend', run);
  setTimeout(run, 220);
}

// Restructures an already-built item `<li>` (buildItemRow's return value, in
// list_view.js) so it can slide horizontally: every child buildItemRow
// appended is moved into a `.swipe-item__foreground` wrapper carrying the
// card's original classes (border/background/padding/layout), so it looks
// completely unchanged while not being swiped, with one or two colored
// action layers sitting behind it, revealed as the foreground slides away.
// `canToggleDone` mirrors buildItemRow's own `showCheckbox` — a `custom`
// list has no "done" concept (see FIELD_VISIBILITY_BY_TYPE in list_view.js),
// so a rightward swipe there is simply clamped to zero and never reveals or
// triggers anything, exactly like its checkbox column being replaced by a
// plain line-number marker instead.
function attachItemSwipeGestures(li, item, { canToggleDone }) {
  if (!IS_TOUCH_DEVICE) return;

  const originalClassName = li.className;
  const foreground = document.createElement('div');
  foreground.className = originalClassName;
  foreground.style.position = 'relative';
  foreground.style.touchAction = 'pan-y';
  while (li.firstChild) foreground.appendChild(li.firstChild);

  li.className = 'swipe-item relative overflow-hidden rounded-2xl';

  const doneBg = canToggleDone ? buildSwipeBackground('done') : null;
  const deleteBg = buildSwipeBackground('delete');
  if (doneBg) li.appendChild(doneBg);
  li.appendChild(deleteBg);
  li.appendChild(foreground);

  let dragging = false;
  let startX = 0;
  let startY = 0;
  let axisLocked = null;
  let dx = 0;

  function setBackgroundOpacity(delta) {
    if (delta > 0 && doneBg) {
      doneBg.style.opacity = String(Math.min(delta / ITEM_SWIPE_THRESHOLD_PX, 1));
      deleteBg.style.opacity = '0';
    } else if (delta < 0) {
      deleteBg.style.opacity = String(Math.min(-delta / ITEM_SWIPE_THRESHOLD_PX, 1));
      if (doneBg) doneBg.style.opacity = '0';
    } else {
      if (doneBg) doneBg.style.opacity = '0';
      deleteBg.style.opacity = '0';
    }
  }

  foreground.addEventListener(
    'touchstart',
    (event) => {
      if (event.touches.length !== 1) {
        dragging = false;
        return;
      }
      const touch = event.touches[0];
      startX = touch.clientX;
      startY = touch.clientY;
      dragging = true;
      axisLocked = null;
      dx = 0;
      // Zero transition latency while actively dragging, so the card tracks
      // the finger 1:1 rather than lagging behind it.
      foreground.style.transition = 'none';
    },
    { passive: true },
  );

  // A long press on this same card (attachLongPress in buildItemRow) can
  // enter multi-select mode mid-touch; if the finger then keeps moving, this
  // swipe must not go on to complete or delete the item. The re-render that
  // mode triggers has already detached this card, so just stop tracking.
  function abandonForSelectionMode() {
    dragging = false;
    dx = 0;
    axisLocked = null;
    foreground.style.transform = '';
    setBackgroundOpacity(0);
  }

  foreground.addEventListener(
    'touchmove',
    (event) => {
      if (!dragging) return;
      if (isSelectionModeActive()) {
        abandonForSelectionMode();
        return;
      }
      const touch = event.touches[0];
      const rawDx = touch.clientX - startX;
      const rawDy = touch.clientY - startY;

      if (axisLocked === null) {
        if (Math.abs(rawDx) < ITEM_SWIPE_AXIS_LOCK_PX && Math.abs(rawDy) < ITEM_SWIPE_AXIS_LOCK_PX) return;
        axisLocked = Math.abs(rawDx) > Math.abs(rawDy) ? 'x' : 'y';
        if (axisLocked === 'y') {
          // A vertical scroll — `touch-action: pan-y` above already told the
          // browser this element only ever hands *vertical* panning to its
          // own native handling, so nothing further needs to happen here:
          // just stop tracking this touch as a swipe candidate.
          dragging = false;
          return;
        }
      }

      // Rightward (toggle done) is clamped to zero when this list type has
      // no "done" concept at all; leftward (delete) always applies.
      let clamped = rawDx;
      if (clamped > 0 && !canToggleDone) clamped = 0;
      const max = ITEM_SWIPE_MAX_PX;
      if (Math.abs(clamped) > max) {
        // Diminishing-returns rubber-band past the visual cap, so it never
        // feels like the card can be dragged indefinitely far.
        const over = Math.abs(clamped) - max;
        clamped = Math.sign(clamped) * (max + Math.sqrt(over) * 4);
      }
      dx = clamped;
      foreground.style.transform = `translateX(${clamped}px)`;
      setBackgroundOpacity(clamped);
    },
    { passive: true },
  );

  function finish() {
    if (!dragging) return;
    if (isSelectionModeActive()) {
      abandonForSelectionMode();
      return;
    }
    dragging = false;
    // Restores (rather than drops) the app-wide theme-crossfade transition
    // this element's own bg-/border-/text- classes normally carry via
    // base.css's global rule — plain assignment to `.style.transition`
    // otherwise fully replaces that shorthand instead of adding to it.
    foreground.style.transition = 'transform 180ms ease, background-color 150ms ease, border-color 150ms ease, color 150ms ease';

    if (dx >= ITEM_SWIPE_THRESHOLD_PX && canToggleDone) {
      gestureVibrate(15);
      foreground.style.transform = 'translateX(100%)';
      settleThenRun(foreground, () => toggleDone(item));
    } else if (dx <= -ITEM_SWIPE_THRESHOLD_PX) {
      gestureVibrate(15);
      foreground.style.transform = 'translateX(-100%)';
      settleThenRun(foreground, () => removeItem(item));
    } else {
      foreground.style.transform = 'translateX(0)';
      setBackgroundOpacity(0);
    }
    dx = 0;
    axisLocked = null;
  }

  foreground.addEventListener('touchend', finish, { passive: true });
  foreground.addEventListener('touchcancel', finish, { passive: true });
}

// ---------------------------------------------------------------------------
// 3. System back (one history entry per open level)
// ---------------------------------------------------------------------------
//
// Android's back action (the gesture-navigation edge swipe or the back
// button) pops a browser history entry. Trakka is a single-document SPA that
// never navigates, so in an installed PWA there is nothing to pop and the
// system closes the app, even with a modal or a list open. Every open level
// (the list detail view, then each overlay stacked above it) therefore gets
// its own same-URL history entry, whose state records its depth
// (`trakkaDepth`); the entry the page loaded on is anchored at depth 0.
// System back pops one entry, and the `popstate` listener closes levels
// (goBack) until the UI is no deeper than the entry it landed on. Closing a
// level any other way (✕, Cancel, Escape, the header back button) drops the
// entries above it with history.go(-n), so no orphaned entry is left for a
// later back press to hit with nothing to close.
//
// Chrome's history manipulation intervention (see
// docs/history_manipulation_intervention.md in Chromium) shapes all of this:
// - A pushState made without a user activation marks *every* entry of the
//   document as skipped by the back button, so the next back leaves the
//   app. The page's first entries therefore wait for its first interaction
//   (a list reopened at boot by restoreLastView gets its entry on the first
//   tap).
// - After a back/forward, an earlier activation no longer counts for new
//   entries until the next one. So nothing is ever pushed in response to a
//   back, which is why there is one entry per level rather than a single
//   guard re-pushed after each back (the first version of this fix, which
//   let the second back press close the app). `activationHonored` tracks
//   this; a push it blocks waits for the next activation.
// - Playwright's page.goBack() is not subject to the intervention; only the
//   browser's own back action is (Alt+Left in a real Chrome window).
//
// No open/close function needs to know about any of this: a
// MutationObserver on the overlays' and #items-section's `hidden` attribute
// resyncs the history after every change, so an overlay added later is
// covered as long as it follows OVERLAY_SELECTOR's id convention.

const HISTORY_DEPTH_KEY = 'trakkaDepth';

// Set while a history.go() issued by syncHistory is still in flight:
// history.state only changes once the traversal lands.
let historyTraversalPending = false;
let activationHonored = !navigator.userActivation || navigator.userActivation.hasBeenActive;
let dropEntriesTimer = null;

function historyDepth() {
  const current = history.state;
  return current && Number.isInteger(current[HISTORY_DEPTH_KEY]) ? current[HISTORY_DEPTH_KEY] : 0;
}

function uiDepth() {
  refreshOpenOverlays();
  return openOverlays.length + (isListViewOpen() ? 1 : 0);
}

function syncHistory() {
  if (historyTraversalPending) return; // resynced by the popstate listener
  const want = uiDepth();
  const have = historyDepth();
  if (want > have && activationHonored) {
    for (let depth = have + 1; depth <= want; depth += 1) {
      history.pushState({ [HISTORY_DEPTH_KEY]: depth }, '');
    }
  } else if (want < have && dropEntriesTimer === null) {
    // Deferred to a task of its own, so a close followed by an open split
    // across microtasks (an `await` between the two) reuses the entry
    // instead of dropping it and then needing a push after a traversal.
    dropEntriesTimer = setTimeout(dropExtraEntries, 0);
  }
}

function dropExtraEntries() {
  dropEntriesTimer = null;
  if (historyTraversalPending) return;
  const extra = historyDepth() - uiDepth();
  if (extra <= 0) return;
  historyTraversalPending = true;
  history.go(-extra);
}

// The in-app edge swipe (section 1) goes through the same history step as
// the system back, so both close exactly one level and the history stays in
// step. When the two are out of step (the first entry still waiting for an
// interaction), it closes directly and syncHistory catches up.
function requestBack() {
  const depth = historyDepth();
  if (!historyTraversalPending && depth > 0 && depth === uiDepth()) {
    history.back();
    return;
  }
  goBack();
}

(function initHistory() {
  // Anchor the entry the page loaded on at depth 0. After a reload, the
  // entries below it belong to the previous document (going back to one
  // loads the page again, it doesn't fire popstate), so depth is never
  // counted from them.
  history.replaceState({ ...(history.state || {}), [HISTORY_DEPTH_KEY]: 0 }, '');

  const observer = new MutationObserver(syncHistory);
  document.querySelectorAll('[id$="-modal"], [id$="-sheet"], #items-section').forEach((el) => {
    observer.observe(el, { attributes: true, attributeFilter: ['hidden'] });
  });

  window.addEventListener('popstate', () => {
    activationHonored = false;
    if (historyTraversalPending) {
      historyTraversalPending = false;
    } else {
      // A back (or forward) by the user: close levels until the UI is no
      // deeper than the entry it landed on. Usually one level; more only if
      // the browser skipped entries. Stops if an overlay won't close.
      const target = historyDepth();
      let depth = uiDepth();
      while (depth > target) {
        goBack();
        const next = uiDepth();
        if (next >= depth) break;
        depth = next;
      }
    }
    syncHistory();
  });

  // The activation-triggering input events. Capture phase, so the flag is
  // set before the tap's own click handler opens anything.
  // navigator.userActivation.isActive filters out the ones that didn't
  // activate (a touchend ending a scroll).
  const onActivation = (event) => {
    if (event.type === 'keydown' && event.key === 'Escape') return;
    if (event.type === 'pointerdown' && event.pointerType !== 'mouse') return;
    if (navigator.userActivation && !navigator.userActivation.isActive) return;
    activationHonored = true;
    syncHistory();
  };
  ['keydown', 'mousedown', 'pointerdown', 'pointerup', 'touchend'].forEach((type) => {
    window.addEventListener(type, onActivation, { capture: true, passive: true });
  });
})();
