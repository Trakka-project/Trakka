'use strict';

// The app's connect screen: enter or scan the address of a Trakka server, or pick a recent one.
// Talks to the app's native TrakkaApp plugin
// (native/app/src/main/java/io/github/trakka_project/app/TrakkaAppPlugin.java) through the bridge
// Capacitor injects into this page. The DOM is only built with textContent, as in Trakka itself.

const STRINGS = {
  fr: {
    title: "Connecter l'application",
    lead: "Indiquez l'adresse de votre serveur Trakka, ou scannez son QR code.",
    urlLabel: 'Adresse du serveur',
    urlPlaceholder: 'https://trakka.exemple.fr',
    connect: 'Se connecter',
    checking: 'Vérification…',
    connecting: 'Connexion…',
    connectAnyway: 'Se connecter quand même',
    or: 'ou',
    scan: 'Scanner un QR code',
    recentTitle: 'Serveurs récents',
    current: 'actuel',
    forget: 'Oublier {host}',
    cancel: 'Annuler et revenir à {host}',
    retry: 'Réessayer',
    loadError: "Impossible d'ouvrir {server}.",
    errorEmpty: "Saisissez l'adresse de votre serveur.",
    errorInvalid: "Cette adresse n'est pas valide.",
    errorQrInvalid: "Ce QR code ne contient pas d'adresse de serveur.",
    errorHttps: "L'application ne se connecte qu'en HTTPS : utilisez une adresse en https://.",
    errorUnknownHost: "Serveur introuvable : {host} n'existe pas, ou le téléphone n'a pas accès au réseau.",
    errorTls: "Le certificat HTTPS de {host} n'est pas reconnu par ce téléphone.",
    errorTimeout: '{host} ne répond pas (délai dépassé).',
    errorUnreachable: '{host} ne répond pas.',
    errorNetwork: 'Erreur réseau en contactant {host}.',
    errorHttp: '{host} a répondu par une erreur ({status}).',
    errorWebView: 'Le composant « Android System WebView » de ce téléphone est trop ancien : mettez-le à jour.',
    notTrakkaRedirect: "Ce serveur répond, mais redirige vers {host} : une page de connexion le protège peut-être, ou ce n'est pas Trakka.",
    notTrakkaStatus: 'Ce serveur répond (code {status}), mais ne ressemble pas à Trakka.',
    cameraDenied: "Accès à la caméra refusé : autorisez-le dans les réglages Android de l'application pour scanner un QR code.",
    noBridge: "Cette page ne fonctionne que dans l'application Android Trakka.",
  },
  en: {
    title: 'Connect the app',
    lead: 'Enter the address of your Trakka server, or scan its QR code.',
    urlLabel: 'Server address',
    urlPlaceholder: 'https://trakka.example.com',
    connect: 'Connect',
    checking: 'Checking…',
    connecting: 'Connecting…',
    connectAnyway: 'Connect anyway',
    or: 'or',
    scan: 'Scan a QR code',
    recentTitle: 'Recent servers',
    current: 'current',
    forget: 'Forget {host}',
    cancel: 'Cancel and go back to {host}',
    retry: 'Try again',
    loadError: 'Could not open {server}.',
    errorEmpty: "Enter your server's address.",
    errorInvalid: 'This address is not valid.',
    errorQrInvalid: 'This QR code does not contain a server address.',
    errorHttps: 'The app only connects over HTTPS: use an https:// address.',
    errorUnknownHost: 'Server not found: {host} does not exist, or the phone has no network access.',
    errorTls: 'This phone does not trust the HTTPS certificate of {host}.',
    errorTimeout: '{host} does not respond (timed out).',
    errorUnreachable: '{host} does not respond.',
    errorNetwork: 'Network error while contacting {host}.',
    errorHttp: '{host} answered with an error ({status}).',
    errorWebView: "This phone's “Android System WebView” is too old: update it.",
    notTrakkaRedirect: 'This server answers but redirects to {host}: a sign-in page may protect it, or it is not Trakka.',
    notTrakkaStatus: 'This server answers (status {status}) but does not look like Trakka.',
    cameraDenied: "Camera access denied: allow it in the app's Android settings to scan a QR code.",
    noBridge: 'This page only works in the Trakka Android app.',
  },
};

// Reasons from the native side (ServerStore.normalize, ServerProbe, MainActivity) to messages.
const REASON_KEYS = {
  empty: 'errorEmpty',
  invalid: 'errorInvalid',
  https_required: 'errorHttps',
  unknown_host: 'errorUnknownHost',
  tls: 'errorTls',
  timeout: 'errorTimeout',
  unreachable: 'errorUnreachable',
  network: 'errorNetwork',
  http: 'errorHttp',
  webview_outdated: 'errorWebView',
};

const lang = (navigator.language || '').toLowerCase().startsWith('fr') ? 'fr' : 'en';

function t(key, params = {}) {
  let text = STRINGS[lang][key] || STRINGS.en[key] || key;
  for (const [name, value] of Object.entries(params)) {
    text = text.replaceAll(`{${name}}`, String(value));
  }
  return text;
}

function hostOf(server) {
  try {
    return new URL(server).host;
  } catch {
    return server;
  }
}

const app = window.Capacitor && window.Capacitor.Plugins && window.Capacitor.Plugins.TrakkaApp;

const els = {
  form: document.getElementById('connect-form'),
  url: document.getElementById('server-url'),
  message: document.getElementById('form-message'),
  connect: document.getElementById('connect-button'),
  connectAnyway: document.getElementById('connect-anyway-button'),
  scan: document.getElementById('scan-button'),
  loadError: document.getElementById('load-error'),
  loadErrorText: document.getElementById('load-error-text'),
  retry: document.getElementById('retry-button'),
  recentSection: document.getElementById('recent-section'),
  recentList: document.getElementById('recent-list'),
  cancel: document.getElementById('cancel-button'),
};

// A server that answers but is not recognizably Trakka, waiting for "Se connecter quand même".
let unconfirmedServer = null;
// Set once the app is switching to a server: the page is about to go away.
let leaving = false;

function setBusy(busy, label) {
  for (const control of document.querySelectorAll('button, input')) {
    control.disabled = busy;
  }
  els.connect.textContent = label || t('connect');
}

function showMessage(text, kind) {
  els.message.textContent = text || '';
  els.message.dataset.kind = kind || '';
  els.message.hidden = !text;
}

function reasonText(reason, params) {
  return t(REASON_KEYS[reason] || 'errorInvalid', params);
}

async function connect(server) {
  leaving = true;
  setBusy(true, t('connecting'));
  try {
    // The app then restarts on that server.
    await app.connect({ url: server });
  } catch (err) {
    leaving = false;
    setBusy(false);
    showMessage(reasonText(err.code, { host: hostOf(server) }), 'error');
  }
}

async function checkAndConnect(url, { scanned = false } = {}) {
  unconfirmedServer = null;
  els.connectAnyway.hidden = true;
  showMessage('');
  setBusy(true, t('checking'));
  try {
    const result = await app.checkServer({ url });
    if (!result.reachable) {
      showMessage(reasonText(result.reason, { host: hostOf(result.server) }), 'error');
    } else if (result.trakka) {
      await connect(result.server);
    } else {
      unconfirmedServer = result.server;
      showMessage(
        result.redirectHost
          ? t('notTrakkaRedirect', { host: result.redirectHost })
          : t('notTrakkaStatus', { status: result.status }),
        'warning',
      );
      els.connectAnyway.hidden = false;
    }
  } catch (err) {
    showMessage(scanned && err.code === 'invalid' ? t('errorQrInvalid') : reasonText(err.code), 'error');
  } finally {
    if (!leaving) {
      setBusy(false);
    }
  }
}

function renderRecent(recent, current) {
  const items = recent.map((server) => {
    const host = hostOf(server);
    const item = document.createElement('li');

    const open = document.createElement('button');
    open.type = 'button';
    open.className = 'recent-open';
    const name = document.createElement('span');
    name.className = 'recent-host';
    name.textContent = host;
    if (server === current) {
      const badge = document.createElement('span');
      badge.className = 'recent-current';
      badge.textContent = t('current');
      name.append(badge);
    }
    const address = document.createElement('span');
    address.className = 'recent-url';
    address.textContent = server;
    open.append(name, address);
    // A server already used once: no need to check it again.
    open.addEventListener('click', () => connect(server));

    const forget = document.createElement('button');
    forget.type = 'button';
    forget.className = 'recent-forget';
    forget.textContent = '×';
    forget.setAttribute('aria-label', t('forget', { host }));
    forget.addEventListener('click', async () => {
      await app.forgetServer({ url: server });
      await refresh();
    });

    item.append(open, forget);
    return item;
  });
  els.recentList.replaceChildren(...items);
  els.recentSection.hidden = items.length === 0;
}

async function refresh() {
  const state = await app.getConnectState();
  renderRecent(state.recent || [], state.server);
  els.cancel.hidden = !state.canCancel;
  if (state.canCancel) {
    els.cancel.textContent = t('cancel', { host: hostOf(state.server) });
  }
  const error = state.error;
  els.loadError.hidden = !error;
  if (error) {
    els.loadErrorText.textContent = `${t('loadError', { server: error.server })} ${reasonText(error.reason, {
      host: error.host || hostOf(error.server),
      status: error.status,
    })}`;
  }
}

function applyTranslations() {
  document.documentElement.lang = lang;
  for (const el of document.querySelectorAll('[data-i18n]')) {
    el.textContent = t(el.dataset.i18n);
  }
  els.url.placeholder = t('urlPlaceholder');
}

applyTranslations();

if (!app) {
  setBusy(true);
  showMessage(t('noBridge'), 'error');
} else {
  els.form.addEventListener('submit', (event) => {
    event.preventDefault();
    checkAndConnect(els.url.value);
  });
  els.connectAnyway.addEventListener('click', () => {
    if (unconfirmedServer) {
      connect(unconfirmedServer);
    }
  });
  els.scan.addEventListener('click', async () => {
    try {
      const { text } = await app.scanQrCode();
      els.url.value = text.trim();
      await checkAndConnect(els.url.value, { scanned: true });
    } catch (err) {
      if (err.code === 'camera_denied') {
        showMessage(t('cameraDenied'), 'error');
      }
    }
  });
  els.retry.addEventListener('click', () => app.reopenServer());
  els.cancel.addEventListener('click', () => app.reopenServer());
  refresh();
}
