'use strict';

// Inside Trakka's Android app (android/, docs/MOBILE_BUILD.md), the SSO button signs in through
// the phone's browser rather than the app's WebView, where an identity provider asking for a
// passkey or a security key fails: the app's native TrakkaApp plugin opens the flow in a Custom
// Tab and brings the session back (internal/handlers/oidc_app.go). In a browser, or in a version
// of the app without signInWithBrowser, the link works as a plain link.
(function () {
  const link = document.getElementById('oidc-login-link');
  const plugins = window.Capacitor && window.Capacitor.Plugins;
  const plugin = plugins && plugins.TrakkaApp;
  // The bridge exposes exactly the methods the app's native plugin has: an older app has no
  // signInWithBrowser at all, and calling it would throw before the fallback below could run.
  if (!link || !plugin || typeof plugin.signInWithBrowser !== 'function') return;

  link.addEventListener('click', (event) => {
    event.preventDefault();
    plugin.signInWithBrowser().catch(() => {
      window.location.href = link.href;
    });
  });
})();
