'use strict';

// Recurrence rules, shared by the page scripts and the service worker: this
// file is loaded both as a <script> and via importScripts() in sw.js, so —
// like db.js — it never touches window/document, only `self` (the Window in
// a page, the ServiceWorkerGlobalScope in the worker).
//
// It is the one hand-kept JS port of internal/recurrence (Parse, String,
// After, Next) and internal/handlers.nextOccurrence — there is no way to
// share code with the Go backend, so any change to the Go rule handling
// must be mirrored here. Rules use the canonical RRULE subset described
// there ("FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR"); the legacy spellings
// (DAILY, EVERY_X_DAYS:<n>, ...) still parse, since an offline mirror can
// hold rows fetched before migration 22. Dates are plain "YYYY-MM-DD"
// strings, computed on UTC midnights so no local DST shift can move a day;
// localDateISO/addDaysISO are the two date helpers the callers share.
(function (global) {
  const WEEKDAYS = ['MO', 'TU', 'WE', 'TH', 'FR', 'SA', 'SU'];
  const FREQS = ['DAILY', 'WEEKLY', 'MONTHLY', 'YEARLY'];
  const MAX_INTERVAL = 999;

  // parseRule returns { freq, interval, byDay } — byDay as Monday-first
  // indices (0 = MO … 6 = SU), sorted and deduplicated — or null for
  // anything internal/recurrence.Parse would reject.
  function parseRule(raw) {
    let s = String(raw || '').trim().toUpperCase();
    if (s.startsWith('RRULE:')) s = s.slice(6);
    if (!s) return null;

    if (FREQS.includes(s)) return { freq: s, interval: 1, byDay: [] };
    const legacy = /^EVERY_X_(DAYS|MONTHS):([1-9][0-9]*)$/.exec(s);
    if (legacy) {
      const n = Number(legacy[2]);
      if (n > MAX_INTERVAL) return null;
      if (legacy[1] === 'MONTHS') return { freq: 'MONTHLY', interval: n, byDay: [] };
      return n % 7 === 0 ? { freq: 'WEEKLY', interval: n / 7, byDay: [] } : { freq: 'DAILY', interval: n, byDay: [] };
    }

    const rule = { freq: null, interval: 1, byDay: [] };
    const seen = new Set();
    for (const part of s.split(';')) {
      const eq = part.indexOf('=');
      const key = eq > 0 ? part.slice(0, eq) : '';
      const value = eq > 0 ? part.slice(eq + 1) : '';
      if (!key || !value || seen.has(key)) return null;
      seen.add(key);
      if (key === 'FREQ') {
        if (!FREQS.includes(value)) return null;
        rule.freq = value;
      } else if (key === 'INTERVAL') {
        if (!/^[0-9]+$/.test(value)) return null;
        const n = Number(value);
        if (n < 1 || n > MAX_INTERVAL) return null;
        rule.interval = n;
      } else if (key === 'BYDAY') {
        const days = new Set();
        for (const code of value.split(',')) {
          const index = WEEKDAYS.indexOf(code);
          if (index === -1) return null;
          days.add(index);
        }
        rule.byDay = [...days].sort((a, b) => a - b);
      } else {
        return null;
      }
    }
    if (!rule.freq || (rule.byDay.length > 0 && rule.freq !== 'WEEKLY')) return null;
    return rule;
  }

  // formatRule is internal/recurrence.Rule.String: the canonical spelling.
  function formatRule(rule) {
    let s = `FREQ=${rule.freq}`;
    if (rule.interval > 1) s += `;INTERVAL=${rule.interval}`;
    if (rule.freq === 'WEEKLY' && rule.byDay && rule.byDay.length > 0) {
      s += `;BYDAY=${[...new Set(rule.byDay)].sort((a, b) => a - b).map((i) => WEEKDAYS[i]).join(',')}`;
    }
    return s;
  }

  // normalizeRule returns the canonical spelling of any accepted rule, or
  // null.
  function normalizeRule(raw) {
    const rule = parseRule(raw);
    return rule ? formatRule(rule) : null;
  }

  function toDate(iso) {
    const date = new Date(`${iso}T00:00:00Z`);
    return Number.isNaN(date.getTime()) ? null : date;
  }

  function toISO(date) {
    return date.toISOString().slice(0, 10);
  }

  // Monday-first weekday index of a UTC date.
  function isoIndex(date) {
    return (date.getUTCDay() + 6) % 7;
  }

  function addDays(date, days) {
    const out = new Date(date.getTime());
    out.setUTCDate(out.getUTCDate() + days);
    return out;
  }

  function addMonthsClamped(date, months) {
    const first = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth() + months, 1));
    const lastDay = new Date(Date.UTC(first.getUTCFullYear(), first.getUTCMonth() + 1, 0)).getUTCDate();
    return new Date(Date.UTC(first.getUTCFullYear(), first.getUTCMonth(), Math.min(date.getUTCDate(), lastDay)));
  }

  // occurrenceAfter is internal/recurrence.Rule.After: the first occurrence
  // strictly after `iso` — see there for how each frequency steps (BYDAY:
  // the next listed day later in the same Monday–Sunday week, else the first
  // listed day `interval` weeks on; months clamp to the month's last day).
  // null for an invalid rule or date.
  function occurrenceAfter(iso, raw) {
    const rule = typeof raw === 'string' ? parseRule(raw) : raw;
    const date = toDate(iso);
    if (!rule || !date) return null;
    switch (rule.freq) {
      case 'DAILY':
        return toISO(addDays(date, rule.interval));
      case 'WEEKLY': {
        if (rule.byDay.length === 0) return toISO(addDays(date, 7 * rule.interval));
        const index = isoIndex(date);
        const later = rule.byDay.find((day) => day > index);
        if (later !== undefined) return toISO(addDays(date, later - index));
        return toISO(addDays(date, 7 * rule.interval - index + rule.byDay[0]));
      }
      case 'MONTHLY':
        return toISO(addMonthsClamped(date, rule.interval));
      default:
        return toISO(addMonthsClamped(date, 12 * rule.interval));
    }
  }

  // nextOccurrence is internal/handlers.nextOccurrence (Rule.Next): step
  // from `from` (the completed occurrence's due date, or `today` when it had
  // none) at least once, then until strictly after `today`.
  function nextOccurrence(from, raw, today) {
    const rule = parseRule(raw);
    if (!rule) return null;
    let next = occurrenceAfter(from || today, rule);
    for (let i = 0; next && next <= today && i < 3660; i++) next = occurrenceAfter(next, rule);
    return next;
  }

  // This device's calendar date, YYYY-MM-DD. Local rather than UTC: "today"
  // must follow the user's own day, which matches the server's APP_TIMEZONE
  // in practice.
  function localDateISO(date = new Date()) {
    const month = String(date.getMonth() + 1).padStart(2, '0');
    const day = String(date.getDate()).padStart(2, '0');
    return `${date.getFullYear()}-${month}-${day}`;
  }

  // The YYYY-MM-DD date `days` days after `iso` (before it when negative).
  function addDaysISO(iso, days) {
    return toISO(addDays(toDate(iso), days));
  }

  // firstOccurrenceOnOrAfter is the first occurrence of a series starting on
  // `start` ("Commence le" in the custom recurrence sheet): `start` itself,
  // unless a weekly rule's BYDAY days exclude its weekday, in which case the
  // first listed day after it. That ignores the interval on purpose: "every
  // 2 weeks on Monday, starting Saturday" starts on the very next Monday
  // (RFC 5545 would count the start's week as week 0 and wait for the week
  // after), and occurrenceAfter then keeps the two-week rhythm from there.
  function firstOccurrenceOnOrAfter(start, raw) {
    const rule = typeof raw === 'string' ? parseRule(raw) : raw;
    const date = toDate(start);
    if (!rule || !date) return null;
    if (rule.freq !== 'WEEKLY' || rule.byDay.length === 0) return start;
    for (let offset = 0; offset < 7; offset++) {
      const candidate = addDays(date, offset);
      if (rule.byDay.includes(isoIndex(candidate))) return toISO(candidate);
    }
    return start;
  }

  global.TrakkaRecurrence = {
    WEEKDAYS,
    MAX_INTERVAL,
    parseRule,
    formatRule,
    normalizeRule,
    occurrenceAfter,
    nextOccurrence,
    firstOccurrenceOnOrAfter,
    localDateISO,
    addDaysISO,
    weekdayIndexOf: (iso) => {
      const date = toDate(iso);
      return date ? isoIndex(date) : null;
    },
  };
})(self);
