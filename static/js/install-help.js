'use strict';

// "How do I install this on my phone?" help modal, opened from the user
// settings panel (settings.js's #user-settings-modal). Three sections, as
// tabs: the Android app (the official trakka.apk attached to every GitHub
// release, docs/MOBILE_BUILD.md), the PWA installed from a browser (with its
// own iOS / Android / desktop sub-tabs), and signing the Android app in with
// a QR code (qr-login.js). The user agent only preselects a section and a
// platform — every tab stays reachable regardless, since sniffing is only
// ever a hint (someone opening Settings from a desktop browser to read the
// steps before trying on their phone, or an in-app browser reporting a
// generic/spoofed UA). Inside the Android app itself the button opening this
// modal is hidden (settings.js's refreshAndroidAppSection).
//
// Shares `userSettingsEls`/`setUserSettingsTab` with settings.js and
// `openQrLoginModal` with qr-login.js (classic-<script>-tags shared scope,
// same convention as every other pair of files in static/js/, all referenced
// lazily from function bodies) so this modal's own close handler can tell
// whether it's being closed while still stacked on top of the settings
// modal — see closeInstallHelpModal below, mirroring spaces.js's
// category-modal-on-top-of-list-modal pattern (and settings.js's own Escape
// handler, updated to defer to this one the same way app.js's new-list modal
// already defers to spaces.js's category one).

const installHelpEls = {
  button: document.getElementById('install-help-button'),
  modal: document.getElementById('install-help-modal'),
  closeButton: document.getElementById('close-install-help-modal-button'),
  sectionTabList: document.getElementById('install-help-tabs'),
  sectionTabs: {
    apk: document.getElementById('install-help-tab-apk'),
    pwa: document.getElementById('install-help-tab-pwa'),
    qr: document.getElementById('install-help-tab-qr'),
  },
  sectionPanels: {
    apk: document.getElementById('install-help-panel-apk'),
    pwa: document.getElementById('install-help-panel-pwa'),
    qr: document.getElementById('install-help-panel-qr'),
  },
  platformTabList: document.getElementById('install-help-platform-tabs'),
  platformTabs: {
    ios: document.getElementById('install-help-tab-ios'),
    android: document.getElementById('install-help-tab-android'),
    desktop: document.getElementById('install-help-tab-desktop'),
  },
  platformPanels: {
    ios: document.getElementById('install-help-steps-ios'),
    android: document.getElementById('install-help-steps-android'),
    desktop: document.getElementById('install-help-steps-desktop'),
  },
  serverAddress: document.getElementById('install-help-server-address'),
  httpsWarning: document.getElementById('install-help-apk-https-warning'),
  goQrButton: document.getElementById('install-help-go-qr-button'),
  showQrButton: document.getElementById('install-help-show-qr-button'),
};

// iPadOS 13+ reports itself as a plain "Macintosh" desktop Safari — the
// touch-points check is the standard way to tell it apart from an actual Mac.
function detectMobileOS() {
  const ua = navigator.userAgent || '';
  const isIOS = /iPad|iPhone|iPod/.test(ua) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  if (isIOS) return 'ios';
  if (/Android/i.test(ua)) return 'android';
  return 'desktop';
}

function setInstallHelpTabButtonState(button, active) {
  button.setAttribute('aria-selected', String(active));
  // Roving tabindex: Tab reaches the selected tab, arrow keys the others.
  button.tabIndex = active ? 0 : -1;
  button.classList.toggle('border-sky-500', active);
  button.classList.toggle('bg-sky-500/10', active);
  button.classList.toggle('text-sky-600', active);
  button.classList.toggle('dark:text-sky-300', active);
  button.classList.toggle('border-slate-200', !active);
  button.classList.toggle('dark:border-slate-700', !active);
  button.classList.toggle('bg-white', !active);
  button.classList.toggle('dark:bg-slate-900', !active);
  button.classList.toggle('text-slate-600', !active);
  button.classList.toggle('dark:text-slate-300', !active);
  button.classList.toggle('hover:bg-slate-200', !active);
  button.classList.toggle('dark:hover:bg-slate-800', !active);
}

// Selects `selected` among a tab group's {name: button} and {name: panel}.
function selectInstallHelpTab(tabs, panels, selected) {
  for (const [name, button] of Object.entries(tabs)) {
    const active = name === selected;
    setInstallHelpTabButtonState(button, active);
    panels[name].hidden = !active;
  }
}

function setInstallHelpSection(section) {
  selectInstallHelpTab(installHelpEls.sectionTabs, installHelpEls.sectionPanels, section);
}

function setInstallHelpPlatform(platform) {
  selectInstallHelpTab(installHelpEls.platformTabs, installHelpEls.platformPanels, platform);
}

// Arrow keys move between a group's tabs, as on any role="tablist" (same keys
// as settings.js's tab list).
function bindInstallHelpTabKeys(tabList, tabs, select) {
  tabList.addEventListener('keydown', (event) => {
    const names = Object.keys(tabs);
    const current = names.findIndex((name) => tabs[name] === document.activeElement);
    if (current === -1) return;
    let next;
    if (event.key === 'ArrowRight') next = (current + 1) % names.length;
    else if (event.key === 'ArrowLeft') next = (current - 1 + names.length) % names.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = names.length - 1;
    else return;
    event.preventDefault();
    select(names[next]);
    tabs[names[next]].focus();
  });
}

function openInstallHelpModal() {
  const os = detectMobileOS();
  // An iPhone can't run the APK: start it on the PWA. Android and desktop
  // start on the app, the desktop reader most likely preparing their phone.
  setInstallHelpSection(os === 'ios' ? 'pwa' : 'apk');
  setInstallHelpPlatform(os);
  // The address to type in the app's connect screen; the app only connects
  // over HTTPS (android/www/connect.js).
  installHelpEls.serverAddress.textContent = window.location.origin;
  installHelpEls.httpsWarning.hidden = window.location.protocol === 'https:';
  installHelpEls.modal.hidden = false;
  TrakkaScrollLock.lock();
}

function closeInstallHelpModal() {
  installHelpEls.modal.hidden = true;
  // Opened from on top of the settings modal — only release the shared body
  // scroll-lock if that one isn't still open behind it.
  if (userSettingsEls.modal.hidden) {
    TrakkaScrollLock.unlock();
  }
}

// "Afficher mon QR code de connexion": swaps this modal for qr-login.js's,
// with the settings behind it moved to the Intégrations tab, so closing the
// QR code lands where the steps said it lives.
function openInstallHelpQrLogin() {
  closeInstallHelpModal();
  setUserSettingsTab('integrations');
  openQrLoginModal();
}

installHelpEls.button.addEventListener('click', openInstallHelpModal);
installHelpEls.closeButton.addEventListener('click', closeInstallHelpModal);
installHelpEls.modal.addEventListener('click', (event) => {
  if (event.target === installHelpEls.modal) closeInstallHelpModal();
});
for (const [name, button] of Object.entries(installHelpEls.sectionTabs)) {
  button.addEventListener('click', () => setInstallHelpSection(name));
}
for (const [name, button] of Object.entries(installHelpEls.platformTabs)) {
  button.addEventListener('click', () => setInstallHelpPlatform(name));
}
bindInstallHelpTabKeys(installHelpEls.sectionTabList, installHelpEls.sectionTabs, setInstallHelpSection);
bindInstallHelpTabKeys(installHelpEls.platformTabList, installHelpEls.platformTabs, setInstallHelpPlatform);
installHelpEls.goQrButton.addEventListener('click', () => {
  setInstallHelpSection('qr');
  installHelpEls.sectionTabs.qr.focus();
});
installHelpEls.showQrButton.addEventListener('click', openInstallHelpQrLogin);
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !installHelpEls.modal.hidden) closeInstallHelpModal();
});
