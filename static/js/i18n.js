'use strict';

// Ultra-light i18n engine: no build step, no dependencies. Loaded as a
// classic <script> before app.js/list_view.js so `window.TrakkaI18n` is
// available to them (mirrors how those two files already share globals).
// The interactive picker itself is the #user-settings-language <select>
// inside #user-settings-modal, owned and wired by static/js/settings.js
// (this file only exposes window.TrakkaI18n.setLang/getLang for it to
// call) — there used to be a header dropdown here instead; see CLAUDE.md's
// session-handoff log for the cleanup that moved it into the "Paramètres"
// modal alongside the theme picker.
//
// Static markup is translated declaratively via data-i18n* attributes:
//   data-i18n="path.to.key"            -> element.textContent
//   data-i18n-attr="aria-label"        -> combined with data-i18n above to
//                                          target that attribute instead of
//                                          textContent
//   data-i18n-placeholder="path.key"   -> element's placeholder attribute
//   data-i18n-aria-label="path.key"    -> element's aria-label attribute
//
// Dynamic strings built in app.js/list_view.js (item counts, badges, error
// messages, ...) are not covered by data-i18n and should call
// `TrakkaI18n.t('some.key', { count })` directly where translation matters.
(function () {
  const LANG_STORAGE_KEY = 'trakka:lang';
  const SUPPORTED_LANGS = ['fr', 'en'];
  const DEFAULT_LANG = 'fr';

  // Bootstrap copy of fr.json's content (kept in sync with
  // static/locales/fr.json) so t() never returns a raw, untranslated key
  // during the brief window between page load and the /locales/*.json
  // fetch resolving — it just mirrors the French text already hardcoded
  // in index.html (this app's default language) until then.
  let translations = {
    common: {
      deleteList: 'Supprimer {name}',
      removeMember: 'Retirer {email}',
    },
    header: {
      backToDashboard: 'Retour au tableau de bord',
      logout: 'Se déconnecter',
      langFr: 'Français',
      langEn: 'English',
      online: 'En ligne',
      offline: 'Hors-ligne',
      pending: '{count} en attente',
      themeLight: 'Clair',
      themeDark: 'Sombre',
      themeAuto: 'Système',
    },
    dashboard: {
      house: 'Maison',
      members: 'Membres',
      title: 'Tableau de bord',
      newList: 'Nouvelle liste',
      shoppingHeading: 'Achats & Sourcing',
      todoHeading: 'Espaces Tâches',
      createHouseOption: '+ Créer une Maison',
      tabLists: 'Listes',
      // Kept in sync with fr.json's dashboard.listAllDoneBadge/listEmptyBadge
      // — buildListCard's urlBadges() (app.js) calls t() for these on every
      // dashboard render, including the very first one (hydrateFromCache(),
      // called from init() before TrakkaI18n.ready has necessarily resolved
      // the real /locales/fr.json fetch) — without an entry here, that first
      // paint fell back to t()'s raw-key behavior and showed the literal
      // string "dashboard.listAllDoneBadge" on a fully-completed list's card
      // instead of translated text.
      listAllDoneBadge: '🎉 Terminé',
      listEmptyBadge: '0 article',
    },
    items: {
      back: 'Retour au tableau de bord',
      financeTotal: 'Total estimé',
      financeSpent: 'Déjà dépensé',
      financeRemaining: 'Reste à dépenser',
      titlePlaceholder: 'Ajouter un article ou une tâche…',
      urlPlaceholder: 'URL (optionnel)',
      quantityAriaLabel: 'Quantité',
      pricePlaceholder: 'Prix € (optionnel)',
      priceAriaLabel: 'Prix en euros',
      targetMonthAriaLabel: 'Mois prévu (optionnel)',
      add: 'Ajouter',
      doneDefault: 'Terminés (0)',
    },
    planning: {
      tabTitle: 'Budget & Prévisions',
      title: 'Budget & Prévisions Achats',
      horizon3: '3 prochains mois',
      horizon6: '6 prochains mois',
      horizon12: '12 prochains mois',
      totalLabel: 'Total prévu sur la période',
      emptyMonth: 'Aucun achat prévu ce mois-ci.',
      unscheduled: '— Non planifié —',
      moveAriaLabel: 'Déplacer {title} vers un autre mois',
    },
    modals: {
      close: 'Fermer',
      newList: {
        title: 'Nouvelle liste',
        nameLabel: 'Nom',
        namePlaceholder: 'Ex. Courses de la semaine',
        typeLabel: 'Type',
        typeShopping: 'Courses',
        typeTodo: 'Tâches',
        submit: 'Créer la liste',
      },
      newHouse: {
        title: 'Nouvelle Maison',
        nameLabel: 'Nom',
        namePlaceholder: 'Ex. Maison des Parents',
        submit: 'Créer la Maison',
      },
      members: {
        title: 'Membres de la Maison',
        invitePlaceholder: 'Email de la personne à inviter',
        inviteSubmit: 'Inviter',
      },
      editItem: {
        title: "Modifier l'article",
        nameLabel: 'Nom',
        urlLabel: 'URL (optionnel)',
        priceLabel: 'Prix (€, optionnel)',
        autoBadge: 'Détecté automatiquement',
        targetMonthLabel: 'Mois prévu (optionnel)',
        submit: 'Enregistrer',
      },
      imagePreview: {
        ariaLabel: "Aperçu de l'image de l'article",
        close: "Fermer l'aperçu",
      },
    },
    undo: {
      cancel: 'Annuler',
      listDeleted: 'Liste « {name} » supprimée',
      itemDeleted: '« {title} » supprimé',
      itemMarkedDone: '« {title} » marqué comme terminé',
      itemMarkedUndone: '« {title} » marqué comme à faire',
    },
  };
  let currentLang = DEFAULT_LANG;
  // True once the very first loadLang() call (the page-load bootstrap, see
  // the IIFE's tail below) has resolved. Guards the trakka:lang-changed
  // dispatch in loadLang() below — see that function for why the very
  // first call must not fire it.
  let hasLoadedOnce = false;

  function detectLang() {
    const stored = localStorage.getItem(LANG_STORAGE_KEY);
    if (stored && SUPPORTED_LANGS.includes(stored)) return stored;
    const nav = (navigator.language || '').slice(0, 2).toLowerCase();
    return SUPPORTED_LANGS.includes(nav) ? nav : DEFAULT_LANG;
  }

  function lookup(key) {
    return key.split('.').reduce((acc, part) => (acc && typeof acc === 'object' ? acc[part] : undefined), translations);
  }

  // True only when `translations` actually has a string at this key path.
  // Used by applyTranslations() below to tell "found" apart from "missing"
  // without duplicating lookup()'s own traversal logic.
  function hasTranslation(key) {
    return typeof lookup(key) === 'string';
  }

  // t() never throws and always returns a string — falling back to the raw
  // key when a translation is missing keeps a broken key visible (rather
  // than blank) for a *dynamic* string built in JS (app.js/list_view.js),
  // which has no other fallback to fall back to. applyTranslations() below
  // is different: every data-i18n* element already ships real, correct
  // fallback text baked into index.html itself (that's what a reader sees
  // before this file's async /locales/*.json fetch ever resolves), so it
  // deliberately does NOT call t() blindly — see hasTranslation() above.
  function t(key, vars) {
    const value = lookup(key);
    let text = typeof value === 'string' ? value : key;
    if (vars) {
      for (const [name, replacement] of Object.entries(vars)) {
        text = text.replace(new RegExp(`\\{${name}\\}`, 'g'), String(replacement));
      }
    }
    return text;
  }

  // Only overwrites an element's existing text/attribute when a real
  // translation was actually found for its key — never with the bare key
  // string t() falls back to for a *dynamic* caller. Without this guard, a
  // client whose service worker is still serving a locale JSON from before
  // a given key existed (SW updates are opt-in via the "Mettre à jour"
  // banner, not automatic — see CLAUDE.md's session-handoff log) would see
  // that literal key ("modals.userSettings.adminConsoleButton" rather than
  // "Console d'Administration") replace the element's perfectly good
  // hardcoded HTML fallback the moment applyTranslations() ran, for as long
  // as the stale locale file keeps being served. Leaving the DOM untouched
  // instead means a missing/stale key just quietly keeps whatever text was
  // already there — the same French fallback every data-i18n element in
  // index.html/templates already carries — rather than ever surfacing a raw
  // key to a real user.
  function applyTranslations(root) {
    const scope = root || document;
    scope.querySelectorAll('[data-i18n]').forEach((el) => {
      const key = el.getAttribute('data-i18n');
      if (!hasTranslation(key)) return;
      const attr = el.getAttribute('data-i18n-attr');
      if (attr) {
        el.setAttribute(attr, t(key));
      } else {
        el.textContent = t(key);
      }
    });
    scope.querySelectorAll('[data-i18n-placeholder]').forEach((el) => {
      const key = el.getAttribute('data-i18n-placeholder');
      if (hasTranslation(key)) el.setAttribute('placeholder', t(key));
    });
    scope.querySelectorAll('[data-i18n-aria-label]').forEach((el) => {
      const key = el.getAttribute('data-i18n-aria-label');
      if (hasTranslation(key)) el.setAttribute('aria-label', t(key));
    });
    document.documentElement.lang = currentLang;
  }

  async function loadLang(lang) {
    const response = await fetch(`/locales/${lang}.json`, { cache: 'no-store' });
    translations = await response.json();
    currentLang = lang;
    applyTranslations();
    // Skip the event on the very first call (the page-load bootstrap at
    // the bottom of this IIFE) — app.js's trakka:lang-changed listener
    // re-renders dynamic, network-driven content (the dashboard grids,
    // notifications) in the new language, which on a fresh load races
    // ahead of app.js's own init() sequence: state.currentHouseId hasn't
    // necessarily been corrected against the live GET /api/v1/houses yet
    // (it may still hold whatever hydrateFromCache() set it to from the
    // IndexedDB mirror, which can reference a house that doesn't belong to
    // the current session at all — e.g. right after switching accounts in
    // the same browser). Firing the dashboard/notifications refresh with
    // that stale id produced a real, user-visible 403 "not a member of
    // this house" banner on the very first paint, milliseconds before
    // init() corrected itself — this fetch here typically resolves in a
    // handful of milliseconds, well before init()'s own /api/v1/houses
    // round-trip. There's nothing to re-render yet on first load anyway:
    // applyTranslations() above already handles the static data-i18n
    // markup, and init()'s own subsequent loadDashboard()/loadNotifications()
    // calls will render everything in the right language once they run.
    if (hasLoadedOnce) {
      document.dispatchEvent(new CustomEvent('trakka:lang-changed', { detail: { lang } }));
    }
    hasLoadedOnce = true;
  }

  async function setLang(lang) {
    if (!SUPPORTED_LANGS.includes(lang) || lang === currentLang) return;
    localStorage.setItem(LANG_STORAGE_KEY, lang);
    await loadLang(lang);
  }

  window.TrakkaI18n = { t, setLang, getLang: () => currentLang, applyTranslations };

  window.TrakkaI18n.ready = loadLang(detectLang());
})();
