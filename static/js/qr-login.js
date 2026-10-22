'use strict';

// "Connecter l'application mobile via QR Code" (Paramètres → Intégrations):
// a modal, on top of #user-settings-modal, showing a single-use code that
// signs Trakka's Android app in to this account. POST
// /api/v1/auth/qr-code/generate (internal/handlers/qr_login.go) issues the
// code; the QR code carries <this origin>/#trakka-qr-login=<code>, which the
// app's connect and sign-in screens recognize (android/www/connect.js): the
// app opens this server and redeems the code, which opens its session.
//
// The code lives three minutes (the response's expires_in, counted down
// from when it arrived, so a computer with a wrong clock still shows the
// right time) and serves once. Once it has expired the QR code is removed,
// not just covered, so it can't be scanned for a sure failure; "Générer un
// nouveau QR code" asks for another, which also cancels the previous one on
// the server. Closing the modal forgets the code.
//
// Hidden inside the Android app itself: the phone showing the code is not
// the one that would scan it. Never queued offline: sw.js lets
// /api/v1/auth/... go straight to the network. Shares `apiRequest`, `t`,
// `userSettingsEls` and settings.js's `androidAppPlugin` with the other page
// scripts (classic-<script>-tags shared scope); refreshQrLoginSection is
// called from settings.js's openUserSettingsModal.

const qrLoginEls = {
  section: document.getElementById('qr-login-section'),
  openButton: document.getElementById('qr-login-open-button'),
  modal: document.getElementById('qr-login-modal'),
  closeButton: document.getElementById('close-qr-login-modal-button'),
  code: document.getElementById('qr-login-code'),
  expired: document.getElementById('qr-login-expired'),
  countdown: document.getElementById('qr-login-countdown'),
  status: document.getElementById('qr-login-status'),
  error: document.getElementById('qr-login-error'),
  regenerateButton: document.getElementById('qr-login-regenerate-button'),
};

// The URL fragment the app looks for (android/www/connect.js's
// QR_LOGIN_FRAGMENT). A fragment is never sent to a server, so a phone's
// camera app opening the link leaves the code out of every log.
const QR_LOGIN_FRAGMENT = 'trakka-qr-login';

let qrLoginTimer = null;
// performance.now() at which the code shown expires; 0 when none is shown.
let qrLoginDeadline = 0;
// Bumped by every request and by closing the modal: a response that arrives
// after either belongs to a code nobody is looking at any more.
let qrLoginGeneration = 0;

function qrLoginUrl(token) {
  return `${window.location.origin}/#${QR_LOGIN_FRAGMENT}=${encodeURIComponent(token)}`;
}

function refreshQrLoginSection() {
  qrLoginEls.section.hidden = Boolean(androidAppPlugin());
}

function stopQrLoginCountdown() {
  if (qrLoginTimer) clearInterval(qrLoginTimer);
  qrLoginTimer = null;
  qrLoginDeadline = 0;
}

function clearQrLoginCode() {
  stopQrLoginCountdown();
  qrLoginEls.code.replaceChildren();
  qrLoginEls.expired.hidden = true;
  qrLoginEls.countdown.textContent = '';
  qrLoginEls.status.textContent = '';
}

function expireQrLoginCode() {
  stopQrLoginCountdown();
  qrLoginEls.code.replaceChildren();
  qrLoginEls.expired.hidden = false;
  qrLoginEls.countdown.textContent = t('modals.qrLogin.expired');
  qrLoginEls.status.textContent = t('modals.qrLogin.expired');
}

function renderQrLoginCountdown() {
  if (!qrLoginDeadline) return;
  const remaining = Math.ceil((qrLoginDeadline - performance.now()) / 1000);
  if (remaining <= 0) {
    expireQrLoginCode();
    return;
  }
  const minutes = Math.floor(remaining / 60);
  const seconds = String(remaining % 60).padStart(2, '0');
  qrLoginEls.countdown.textContent = t('modals.qrLogin.expiresIn', { time: `${minutes}:${seconds}` });
}

async function generateQrLoginCode() {
  const generation = ++qrLoginGeneration;
  clearQrLoginCode();
  qrLoginEls.error.hidden = true;
  qrLoginEls.regenerateButton.disabled = true;
  qrLoginEls.countdown.textContent = t('modals.qrLogin.loading');
  try {
    const created = await apiRequest('/auth/qr-code/generate', { method: 'POST' });
    if (generation !== qrLoginGeneration) return;
    const svg = TrakkaQR.toSvg(qrLoginUrl(created.token), { title: t('modals.qrLogin.codeLabel') });
    svg.setAttribute('class', 'block h-full w-full');
    qrLoginEls.code.replaceChildren(svg);
    qrLoginDeadline = performance.now() + created.expires_in * 1000;
    renderQrLoginCountdown();
    qrLoginTimer = setInterval(renderQrLoginCountdown, 1000);
  } catch (err) {
    if (generation !== qrLoginGeneration) return;
    qrLoginEls.countdown.textContent = '';
    qrLoginEls.error.textContent = err.isNetworkError ? t('modals.qrLogin.networkRequired') : err.message;
    qrLoginEls.error.hidden = false;
  } finally {
    if (generation === qrLoginGeneration) qrLoginEls.regenerateButton.disabled = false;
  }
}

function openQrLoginModal() {
  qrLoginEls.modal.hidden = false;
  TrakkaScrollLock.lock();
  generateQrLoginCode();
}

function closeQrLoginModal() {
  qrLoginGeneration++;
  clearQrLoginCode();
  qrLoginEls.error.hidden = true;
  qrLoginEls.regenerateButton.disabled = false;
  qrLoginEls.modal.hidden = true;
  // Opened from on top of the settings modal: release the shared body
  // scroll-lock only if that one isn't still open behind it (install-help.js
  // does the same).
  if (userSettingsEls.modal.hidden) {
    TrakkaScrollLock.unlock();
  }
}

qrLoginEls.openButton.addEventListener('click', openQrLoginModal);
qrLoginEls.closeButton.addEventListener('click', closeQrLoginModal);
qrLoginEls.regenerateButton.addEventListener('click', generateQrLoginCode);
qrLoginEls.modal.addEventListener('click', (event) => {
  if (event.target === qrLoginEls.modal) closeQrLoginModal();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !qrLoginEls.modal.hidden) closeQrLoginModal();
});

// The countdown is set from script, not data-i18n.
document.addEventListener('trakka:lang-changed', () => {
  if (!qrLoginEls.modal.hidden && qrLoginDeadline) renderQrLoginCountdown();
  else if (!qrLoginEls.expired.hidden) qrLoginEls.countdown.textContent = t('modals.qrLogin.expired');
});
