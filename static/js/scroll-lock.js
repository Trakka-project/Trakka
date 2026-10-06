'use strict';

// Page scroll lock for every modal and sheet: while one is open, the page
// behind must not move under the mouse wheel, the keyboard or a drag.
// lock() puts Tailwind's `overflow-hidden` class on both <html> and <body>;
// base.css's "Modal scroll lock" rules explain why <html> is the one that
// matters (its `overflow-x: clip` keeps <body>'s overflow from ever
// reaching the viewport) and why <body> keeps its normal overflow while
// locked. Loaded before every script that opens a modal (index.html), so
// they can all call it at any time.
//
// There's no counter: a modal opening over another one (the emoji picker,
// the recurrence editor) checks isLocked() first and only unlocks what it
// locked itself, as before.
window.TrakkaScrollLock = {
  lock() {
    document.documentElement.classList.add('overflow-hidden');
    document.body.classList.add('overflow-hidden');
  },
  unlock() {
    document.documentElement.classList.remove('overflow-hidden');
    document.body.classList.remove('overflow-hidden');
  },
  isLocked() {
    return document.documentElement.classList.contains('overflow-hidden');
  },
};
