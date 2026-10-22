'use strict';

// The app's connect screen: enter or scan the address of a Trakka server, or pick a recent one.
// It is also the app's sign-in screen (MainActivity shows it when the server asks to sign in):
// the server's SSO in the phone's browser, its password form, a sign-in QR code, or another server. Talks to the app's native TrakkaApp plugin
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
    errorQrNoSignIn: "Ce QR code ne contient pas de code de connexion. Dans Trakka, sur un ordinateur : Paramètres → Intégrations → « Connecter l'application mobile via QR Code ».",
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
    signInTitle: 'Se connecter',
    signInLead: 'à {host}',
    signInLoading: 'Recherche des méthodes de connexion…',
    sso: 'Se connecter avec {provider}',
    passwordSignIn: 'Se connecter avec un e-mail et un mot de passe',
    qrSignIn: 'Se connecter avec un QR code',
    openServerPage: 'Ouvrir la page de connexion du serveur',
    register: 'Créer un compte',
    changeServer: 'Changer de serveur',
    backToSignIn: 'Annuler et revenir à la connexion à {host}',
    ssoInBrowser: 'Connexion en cours dans le navigateur : l\'application reprend la main une fois connecté.',
    ssoFailed: "Impossible d'ouvrir la connexion SSO.",
    signInUnknown: "{host} répond, mais n'indique pas ses méthodes de connexion (code {status}) : ouvrez sa page de connexion.",
    signInUnknownRedirect: "{host} redirige vers {redirect} : une page de connexion le protège peut-être. Ouvrez-la pour vous connecter.",
    signInNone: "{host} ne propose aucune méthode de connexion : ouvrez sa page de connexion.",
    signInError_oidc_failed: 'La connexion via le fournisseur externe a échoué.',
    signInError_oidc_unavailable: "Le fournisseur d'identité ne répond pas pour le moment. Réessayez dans quelques instants.",
    signInError_rate_limited: 'Trop de tentatives. Réessayez dans quelques minutes.',
    signInError_qr_login_failed: 'Ce QR code de connexion a expiré ou a déjà servi : générez-en un nouveau dans Trakka.',
    signInError: 'La connexion a échoué ({code}).',
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
    errorQrNoSignIn: 'This QR code holds no sign-in code. In Trakka, on a computer: Settings → Integrations → “Connect the mobile app with a QR code”.',
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
    signInTitle: 'Sign in',
    signInLead: 'to {host}',
    signInLoading: 'Looking up the sign-in methods…',
    sso: 'Sign in with {provider}',
    passwordSignIn: 'Sign in with an email and a password',
    qrSignIn: 'Sign in with a QR code',
    openServerPage: "Open the server's sign-in page",
    register: 'Create an account',
    changeServer: 'Change server',
    backToSignIn: 'Cancel and go back to signing in to {host}',
    ssoInBrowser: 'Signing in in the browser: the app takes over once you are signed in.',
    ssoFailed: 'Could not start the SSO sign-in.',
    signInUnknown: "{host} answers but does not tell its sign-in methods (status {status}): open its sign-in page.",
    signInUnknownRedirect: '{host} redirects to {redirect}: a sign-in page may protect it. Open it to sign in.',
    signInNone: "{host} offers no sign-in method: open its sign-in page.",
    signInError_oidc_failed: 'Signing in with the external provider failed.',
    signInError_oidc_unavailable: 'The identity provider does not respond right now. Try again in a moment.',
    signInError_rate_limited: 'Too many attempts. Try again in a few minutes.',
    signInError_qr_login_failed: 'This sign-in QR code has expired or was already used: generate a new one in Trakka.',
    signInError: 'Signing in failed ({code}).',
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

// A sign-in QR code from Trakka's settings (static/js/qr-login.js): the server's address, with a
// single-use code in its fragment, "https://trakka.example.com/#trakka-qr-login=<code>". The
// server redeems the code once the app has opened it (MainActivity, internal/handlers/qr_login.go).
const QR_LOGIN_FRAGMENT = 'trakka-qr-login';
const QR_LOGIN_CODE = /^[A-Za-z0-9_-]{20,128}$/;

// {url, signInCode}: what a scanned QR code holds, signInCode null for a plain server address.
function parseScan(text) {
  const value = String(text || '').trim();
  try {
    const url = new URL(value);
    const code = new URLSearchParams(url.hash.slice(1)).get(QR_LOGIN_FRAGMENT);
    if (code && QR_LOGIN_CODE.test(code)) {
      return { url: url.origin, signInCode: code };
    }
  } catch {
    // Not a full URL: an address such as "trakka.example.com", which the app completes.
  }
  return { url: value, signInCode: null };
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
  title: document.getElementById('screen-title'),
  lead: document.getElementById('screen-lead'),
  signIn: document.getElementById('signin-section'),
  signInMessage: document.getElementById('signin-message'),
  sso: document.getElementById('sso-button'),
  signInSeparator: document.getElementById('signin-separator'),
  password: document.getElementById('password-button'),
  qrSignIn: document.getElementById('qr-signin-button'),
  serverPage: document.getElementById('server-page-button'),
  signInRetry: document.getElementById('signin-retry-button'),
  register: document.getElementById('register-button'),
  changeServer: document.getElementById('change-server-button'),
  backToSignIn: document.getElementById('back-to-signin-button'),
};

// A server that answers but is not recognizably Trakka, waiting for "Se connecter quand même",
// with the sign-in code of the QR code it came from, if any.
let unconfirmedServer = null;
let unconfirmedSignInCode = null;
// Set once the app is switching to a server: the page is about to go away.
let leaving = false;
// {server, error?} when the server asked to sign in: the screen then starts as a sign-in screen.
let signIn = null;

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

// signInCode, from a sign-in QR code, is redeemed by the server once the app has opened it.
async function connect(server, { signInCode = null, show = showMessage } = {}) {
  leaving = true;
  setBusy(true, t('connecting'));
  try {
    // The app then restarts on that server.
    await app.connect(signInCode ? { url: server, qrLoginCode: signInCode } : { url: server });
  } catch (err) {
    leaving = false;
    setBusy(false);
    show(reasonText(err.code, { host: hostOf(server) }), 'error');
  }
}

async function checkAndConnect(url, { scanned = false, signInCode = null } = {}) {
  unconfirmedServer = null;
  unconfirmedSignInCode = null;
  els.connectAnyway.hidden = true;
  showMessage('');
  setBusy(true, t('checking'));
  try {
    const result = await app.checkServer({ url });
    if (!result.reachable) {
      showMessage(reasonText(result.reason, { host: hostOf(result.server) }), 'error');
    } else if (result.trakka) {
      await connect(result.server, { signInCode });
    } else {
      unconfirmedServer = result.server;
      unconfirmedSignInCode = signInCode;
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

function showSignInMessage(text, kind) {
  els.signInMessage.textContent = text || '';
  els.signInMessage.dataset.kind = kind || '';
  els.signInMessage.hidden = !text;
}

function signInErrorText(code) {
  const key = `signInError_${code}`;
  return STRINGS[lang][key] || STRINGS.en[key] ? t(key) : t('signInError', { code });
}

// Sign-in screen, or the connect form reached from it through "Changer de serveur".
function showSignInView(signingIn) {
  const host = hostOf(signIn.server);
  els.title.textContent = signingIn ? t('signInTitle') : t('title');
  els.lead.textContent = signingIn ? t('signInLead', { host }) : t('lead');
  els.signIn.hidden = !signingIn;
  els.changeServer.hidden = !signingIn;
  els.form.hidden = signingIn;
  els.recentSection.hidden = signingIn || els.recentList.children.length === 0;
  els.backToSignIn.hidden = signingIn;
  els.backToSignIn.textContent = t('backToSignIn', { host });
}

async function loadSignInMethods() {
  const host = hostOf(signIn.server);
  for (const el of [els.sso, els.signInSeparator, els.password, els.serverPage, els.signInRetry, els.register]) {
    el.hidden = true;
  }
  showSignInMessage(signIn.error ? signInErrorText(signIn.error) : t('signInLoading'), signIn.error ? 'error' : '');
  let methods;
  try {
    methods = await app.getSignInMethods();
  } catch {
    methods = { reachable: false, reason: 'network' };
  }
  const notices = signIn.error ? [signInErrorText(signIn.error)] : [];
  if (!methods.reachable) {
    notices.push(reasonText(methods.reason, { host }));
    els.signInRetry.hidden = false;
  } else if (!methods.known) {
    notices.push(
      methods.redirectHost
        ? t('signInUnknownRedirect', { host, redirect: methods.redirectHost })
        : t('signInUnknown', { host, status: methods.status }),
    );
    els.serverPage.hidden = false;
  } else {
    els.sso.textContent = t('sso', { provider: methods.providerName || 'SSO' });
    els.sso.hidden = !methods.oidc;
    // Shown only when the server allows passwords: an SSO-only server has no password form.
    els.password.hidden = !methods.password;
    els.signInSeparator.hidden = !(methods.oidc && methods.password);
    els.register.hidden = !methods.registration;
    if (!methods.oidc && !methods.password) {
      notices.push(t('signInNone', { host }));
      els.serverPage.hidden = false;
    }
  }
  showSignInMessage(notices.join(' '), notices.length ? 'error' : '');
}

// "Se connecter avec un QR code" on the sign-in screen: only a sign-in QR code makes sense there.
async function signInWithQrCode() {
  let scan;
  try {
    scan = parseScan((await app.scanQrCode()).text);
  } catch (err) {
    if (err.code === 'camera_denied') {
      showSignInMessage(t('cameraDenied'), 'error');
    }
    return;
  }
  if (!scan.signInCode) {
    showSignInMessage(t('errorQrNoSignIn'), 'error');
    return;
  }
  // Usually this server's code; another server's opens that server instead.
  await connect(scan.url, { signInCode: scan.signInCode, show: showSignInMessage });
}

async function signInWithSso() {
  try {
    await app.signInWithBrowser();
    showSignInMessage(t('ssoInBrowser'), 'info');
  } catch (err) {
    if (err.code === 'no_browser') {
      // No browser on the phone: the server's SSO sign-in in the app's WebView, as a fallback.
      await openServerSignIn('sso');
    } else {
      showSignInMessage(t('ssoFailed'), 'error');
    }
  }
}

async function openServerSignIn(page) {
  leaving = true;
  setBusy(true);
  try {
    await app.openServerSignIn({ page });
  } catch {
    leaving = false;
    setBusy(false);
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
  els.recentSection.hidden = items.length === 0 || (signIn !== null && !els.signIn.hidden);
}

async function refresh() {
  const state = await app.getConnectState();
  const wasSigningIn = signIn !== null;
  signIn = state.signIn || null;
  if (wasSigningIn && !signIn) {
    // Its server was just forgotten: back to a plain connect screen.
    applyTranslations();
    els.signIn.hidden = true;
    els.changeServer.hidden = true;
    els.backToSignIn.hidden = true;
    els.form.hidden = false;
  }
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
      connect(unconfirmedServer, { signInCode: unconfirmedSignInCode });
    }
  });
  els.scan.addEventListener('click', async () => {
    try {
      const scan = parseScan((await app.scanQrCode()).text);
      // The address only: the sign-in code stays out of the field.
      els.url.value = scan.url;
      await checkAndConnect(scan.url, { scanned: true, signInCode: scan.signInCode });
    } catch (err) {
      if (err.code === 'camera_denied') {
        showMessage(t('cameraDenied'), 'error');
      }
    }
  });
  els.retry.addEventListener('click', () => app.reopenServer());
  els.cancel.addEventListener('click', () => app.reopenServer());
  els.sso.addEventListener('click', signInWithSso);
  els.password.addEventListener('click', () => openServerSignIn('password'));
  els.qrSignIn.addEventListener('click', signInWithQrCode);
  els.serverPage.addEventListener('click', () => openServerSignIn('password'));
  els.register.addEventListener('click', () => openServerSignIn('register'));
  els.signInRetry.addEventListener('click', () => {
    signIn = { server: signIn.server };
    loadSignInMethods();
  });
  els.changeServer.addEventListener('click', () => {
    showSignInView(false);
    els.url.focus();
  });
  els.backToSignIn.addEventListener('click', () => {
    showMessage('');
    showSignInView(true);
  });
  refresh().then(() => {
    if (signIn) {
      showSignInView(true);
      loadSignInMethods();
    }
  });
}
