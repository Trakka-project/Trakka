'use strict';

// "How do I install this on my phone?" help modal, opened from the user
// settings panel (settings.js's #user-settings-modal). Detects iOS vs
// Android from the user agent purely to preselect a tab — both tabs stay
// reachable regardless, since sniffing is only ever a hint (someone opening
// Settings from a desktop browser to read the steps before trying on their
// phone, or an in-app browser reporting a generic/spoofed UA).
//
// Shares `userSettingsEls` with settings.js (classic-<script>-tags shared
// scope, same convention as every other pair of files in static/js/) so this
// modal's own close handler can tell whether it's being closed while still
// stacked on top of the settings modal — see closeInstallHelpModal below,
// mirroring spaces.js's category-modal-on-top-of-list-modal pattern (and
// settings.js's own Escape handler, updated to defer to this one the same
// way app.js's new-list modal already defers to spaces.js's category one).

const installHelpEls = {
  button: document.getElementById('install-help-button'),
  modal: document.getElementById('install-help-modal'),
  closeButton: document.getElementById('close-install-help-modal-button'),
  tabIOS: document.getElementById('install-help-tab-ios'),
  tabAndroid: document.getElementById('install-help-tab-android'),
  stepsIOS: document.getElementById('install-help-steps-ios'),
  stepsAndroid: document.getElementById('install-help-steps-android'),
};

// iPadOS 13+ reports itself as a plain "Macintosh" desktop Safari — the
// touch-points check is the standard way to tell it apart from an actual Mac.
function detectMobileOS() {
  const ua = navigator.userAgent || '';
  const isIOS = /iPad|iPhone|iPod/.test(ua) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  if (isIOS) return 'ios';
  if (/Android/i.test(ua)) return 'android';
  return 'ios'; // desktop/unknown: show the iOS tab first, Android is one tap away
}

function setInstallHelpTabButtonState(button, active) {
  button.setAttribute('aria-selected', String(active));
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
}

function setInstallHelpTab(os) {
  const isIOS = os !== 'android';
  setInstallHelpTabButtonState(installHelpEls.tabIOS, isIOS);
  setInstallHelpTabButtonState(installHelpEls.tabAndroid, !isIOS);
  installHelpEls.stepsIOS.hidden = !isIOS;
  installHelpEls.stepsAndroid.hidden = isIOS;
}

function openInstallHelpModal() {
  setInstallHelpTab(detectMobileOS());
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

installHelpEls.button.addEventListener('click', openInstallHelpModal);
installHelpEls.closeButton.addEventListener('click', closeInstallHelpModal);
installHelpEls.modal.addEventListener('click', (event) => {
  if (event.target === installHelpEls.modal) closeInstallHelpModal();
});
installHelpEls.tabIOS.addEventListener('click', () => setInstallHelpTab('ios'));
installHelpEls.tabAndroid.addEventListener('click', () => setInstallHelpTab('android'));
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !installHelpEls.modal.hidden) closeInstallHelpModal();
});
