'use strict';

// Trakka's universal undo/rollback toast: the one place any mutation that's
// easy to trigger by mistake (deleting a list, deleting an item, toggling an
// item's done state) can ask for a short grace period before its real API
// call goes out. TrakkaUndo.schedule() shows a bottom snackbar with a
// countdown and an "Annuler" button; the caller supplies `onCommit` (run
// once the countdown reaches zero — this is where the real apiRequest()
// call belongs) and `onUndo` (run if the user clicks the button in time —
// this is where an already-applied optimistic DOM/state change gets
// reverted). Exactly one of the two ever runs, exactly once.
//
// Because the real network request only ever happens from inside
// `onCommit`, after the delay, this module never touches `fetch` or the
// service worker itself — from sw.js's point of view a commit's request is
// indistinguishable from one sent immediately, so the existing offline
// queueing in static/sw.js (queueOfflineWrite, resolveAgainstPendingCreate,
// ...) applies unchanged whether the request happens to go out while online
// or offline. See docs/PWA.md for that queueing.
(function () {
  const DEFAULT_DELAY_MS = 5000;

  function getContainer() {
    return document.getElementById('toast-container');
  }

  function schedule({ message, undoLabel, delay = DEFAULT_DELAY_MS, onCommit, onUndo }) {
    const root = getContainer();
    if (!root) {
      // No toast host on this page — fail safe by committing right away
      // rather than silently dropping the mutation.
      onCommit();
      return { dismiss() {} };
    }

    let settled = false;

    const toast = document.createElement('div');
    toast.setAttribute('role', 'status');
    toast.className =
      'pointer-events-auto relative flex items-center gap-3 overflow-hidden rounded-xl border border-slate-200 dark:border-slate-700 bg-slate-100 dark:bg-slate-800 py-3 pl-4 pr-3 shadow-xl';

    const text = document.createElement('span');
    text.className = 'text-sm text-slate-900 dark:text-slate-100';
    text.textContent = message;

    const undoBtn = document.createElement('button');
    undoBtn.type = 'button';
    undoBtn.className =
      'shrink-0 rounded-lg bg-sky-500/10 px-3 py-1.5 text-sm font-semibold text-sky-600 dark:text-sky-300 hover:bg-sky-500/20';
    undoBtn.textContent = undoLabel;

    // Purely visual countdown: a bar that shrinks from full width to zero
    // over `delay`ms via a single CSS transition, rather than a per-frame
    // JS loop — the actual expiry is driven by the setTimeout below, this
    // just shows how much of it is left.
    const progress = document.createElement('div');
    progress.className = 'absolute inset-x-0 bottom-0 h-0.5 bg-sky-400/70';
    progress.style.width = '100%';

    toast.append(text, undoBtn, progress);
    root.appendChild(toast);

    // Deferred to the next frame so the browser paints the starting
    // width:100% first — changing it to 0% in the same tick as appending
    // the element would collapse both into one style recalculation and the
    // bar would just appear empty, with no visible animation.
    requestAnimationFrame(() => {
      progress.style.transition = `width ${delay}ms linear`;
      progress.style.width = '0%';
    });

    const timer = setTimeout(() => finish(onCommit), delay);

    function teardown() {
      clearTimeout(timer);
      toast.remove();
    }

    function finish(callback) {
      if (settled) return;
      settled = true;
      teardown();
      callback();
    }

    undoBtn.addEventListener('click', () => finish(onUndo));

    return {
      // Tears the toast down without running either callback — used when a
      // caller supersedes its own still-pending action with a newer one
      // (e.g. re-checking a checkbox before the previous toggle committed)
      // rather than the user actually clicking "Annuler".
      dismiss() {
        if (settled) return;
        settled = true;
        teardown();
      },
    };
  }

  // A short-lived, non-interactive confirmation toast — reuses the same
  // #toast-container as the undo/rollback toast above, but with no undo
  // button or countdown bar, since it confirms a mutation that's already
  // been committed rather than one still open to being undone. Used e.g. by
  // app.js's "rename house" flow.
  function success(message, delay = 3000) {
    const root = getContainer();
    if (!root) return;

    const toast = document.createElement('div');
    toast.setAttribute('role', 'status');
    toast.className =
      'pointer-events-auto flex items-center gap-2 rounded-xl border border-emerald-200 dark:border-emerald-800 bg-slate-100 dark:bg-slate-800 py-3 px-4 text-sm text-slate-900 dark:text-slate-100 shadow-xl';

    const icon = document.createElement('span');
    icon.setAttribute('aria-hidden', 'true');
    icon.className = 'text-emerald-500';
    icon.textContent = '✓';

    const text = document.createElement('span');
    text.textContent = message;

    toast.append(icon, text);
    root.appendChild(toast);

    setTimeout(() => toast.remove(), delay);
  }

  // The same short-lived toast for something that didn't work out but needs
  // no action right away (an amber ⚠ instead of the green ✓) — e.g. a price
  // check that couldn't read the product page.
  function warning(message, delay = 5000) {
    const root = getContainer();
    if (!root) return;

    const toast = document.createElement('div');
    toast.setAttribute('role', 'status');
    toast.className =
      'pointer-events-auto flex items-center gap-2 rounded-xl border border-amber-300 dark:border-amber-700 bg-slate-100 dark:bg-slate-800 py-3 px-4 text-sm text-slate-900 dark:text-slate-100 shadow-xl';

    const icon = document.createElement('span');
    icon.setAttribute('aria-hidden', 'true');
    icon.className = 'text-amber-500';
    icon.textContent = '⚠';

    const text = document.createElement('span');
    text.textContent = message;

    toast.append(icon, text);
    root.appendChild(toast);

    setTimeout(() => toast.remove(), delay);
  }

  window.TrakkaUndo = { schedule };
  window.TrakkaToast = { success, warning };
})();
