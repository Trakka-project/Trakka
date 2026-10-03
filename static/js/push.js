'use strict';

// Web Push opt-in: the "Activer les notifications push" toggle inside
// #user-settings-modal (static/index.html), wired to GET
// /api/v1/push/vapid-public-key and POST/DELETE /api/v1/push/subscribe
// (internal/handlers/push.go) plus the browser's own Notification/
// PushManager APIs. Shares `state`, `apiRequest`, `showError`/`hideError`,
// `t`, `selectList` with app.js/list_view.js/settings.js — same
// classic-<script>-tags shared-scope pattern as every other frontend file.
//
// refreshPushToggleUI is called from settings.js's openUserSettingsModal
// (every time the modal opens, not just once) since permission/subscription
// state can change outside the app at any time — the user revoking
// notification access via the browser's own site settings, most notably —
// and the toggle has to reflect reality rather than whatever it last showed.

const pushEls = {
  toggle: document.getElementById('user-settings-push-toggle'),
  status: document.getElementById('user-settings-push-status'),
  vibrateRow: document.getElementById('user-settings-vibrate-row'),
};

function isPushSupported() {
  return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
}

// Converts a base64url-encoded VAPID public key (as
// GET /api/v1/push/vapid-public-key returns it) into the raw Uint8Array
// PushManager.subscribe's applicationServerKey option expects — the
// standard conversion every Web Push integration needs, since the Push API
// itself has no base64 convenience of its own.
function urlBase64ToUint8Array(base64String) {
  const padding = '='.repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(base64);
  const bytes = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) {
    bytes[i] = raw.charCodeAt(i);
  }
  return bytes;
}

// Cached for the page's lifetime — the VAPID public key never changes
// without a server restart, and both refreshPushToggleUI and enablePush
// need it.
let vapidKeyPromise = null;

function fetchVapidPublicKey() {
  if (!vapidKeyPromise) {
    vapidKeyPromise = apiRequest('/push/vapid-public-key').catch((err) => {
      vapidKeyPromise = null; // let a later call retry after a transient failure
      throw err;
    });
  }
  return vapidKeyPromise;
}

async function getExistingPushSubscription() {
  if (!isPushSupported()) return null;
  const registration = await navigator.serviceWorker.ready;
  return registration.pushManager.getSubscription();
}

// Sets the toggle's checked/disabled state and the small status line below
// it to match reality: unsupported browser, push not configured on this
// instance, permission previously denied (which only the user can undo, via
// their own browser's site settings — this app can't re-prompt), or the
// ordinary on/off state of an actual browser subscription.
//
// The vibration setting only shapes push notifications, so it is hidden where
// this device can't get any (unsupported, or push not configured on the
// instance). Hidden, its checkbox keeps the account's value, which saving the
// form sends back unchanged: it still applies to the user's other devices.
async function refreshPushToggleUI() {
  if (!pushEls.toggle) return;
  pushEls.status.hidden = true;
  pushEls.status.textContent = '';
  if (pushEls.vibrateRow) pushEls.vibrateRow.hidden = false;

  if (!isPushSupported()) {
    pushEls.toggle.checked = false;
    pushEls.toggle.disabled = true;
    // Android's WebView, which the Android app (android/) shows Trakka in,
    // has no Web Push: tell the user where reminders do work rather than
    // blaming "this browser" they never chose (androidAppPlugin: settings.js).
    pushEls.status.textContent = t(androidAppPlugin()
      ? 'modals.userSettings.pushUnsupportedAndroidApp'
      : 'modals.userSettings.pushUnsupported');
    pushEls.status.hidden = false;
    if (pushEls.vibrateRow) pushEls.vibrateRow.hidden = true;
    return;
  }

  let vapid;
  try {
    vapid = await fetchVapidPublicKey();
  } catch {
    vapid = { enabled: false };
  }
  if (!vapid.enabled) {
    pushEls.toggle.checked = false;
    pushEls.toggle.disabled = true;
    pushEls.status.textContent = t('modals.userSettings.pushNotConfigured');
    pushEls.status.hidden = false;
    if (pushEls.vibrateRow) pushEls.vibrateRow.hidden = true;
    return;
  }

  if (Notification.permission === 'denied') {
    pushEls.toggle.checked = false;
    pushEls.toggle.disabled = true;
    pushEls.status.textContent = t('modals.userSettings.pushPermissionDenied');
    pushEls.status.hidden = false;
    return;
  }

  pushEls.toggle.disabled = false;
  const subscription = await getExistingPushSubscription();
  pushEls.toggle.checked = !!subscription;
}

// Requests notification permission (the browser's own native prompt —
// nothing here can skip or pre-empt it) and, once granted, subscribes via
// PushManager and registers the subscription with the backend.
async function enablePush() {
  const vapid = await fetchVapidPublicKey();
  if (!vapid.enabled) {
    throw new Error(t('modals.userSettings.pushNotConfigured'));
  }

  const permission = await Notification.requestPermission();
  if (permission !== 'granted') {
    throw new Error(t('modals.userSettings.pushPermissionDenied'));
  }

  const registration = await navigator.serviceWorker.ready;
  let subscription = await registration.pushManager.getSubscription();
  if (!subscription) {
    subscription = await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(vapid.public_key),
    });
  }

  const json = subscription.toJSON();
  await apiRequest('/push/subscribe', {
    method: 'POST',
    body: JSON.stringify({ endpoint: json.endpoint, keys: json.keys }),
  });
}

// Unsubscribes both locally (the browser stops being able to receive a push
// for this subscription at all) and on the backend (so the now-dead
// endpoint isn't kept around and tried on the next notification).
async function disablePush() {
  const subscription = await getExistingPushSubscription();
  if (!subscription) return;
  const endpoint = subscription.endpoint;
  await subscription.unsubscribe();
  try {
    await apiRequest('/push/subscribe', { method: 'DELETE', body: JSON.stringify({ endpoint }) });
  } catch {
    // The subscription is already gone client-side regardless — a failure
    // reaching the server here just means a stale row lingers until the
    // push service itself eventually reports it gone (see
    // internal/handlers.sendToUsers' ErrSubscriptionGone cleanup), which
    // isn't worth surfacing as an error for a toggle the user just turned
    // off.
  }
}

if (pushEls.toggle) {
  pushEls.toggle.addEventListener('change', async () => {
    hideError();
    pushEls.status.hidden = true;
    const wantEnabled = pushEls.toggle.checked;
    pushEls.toggle.disabled = true;
    try {
      if (wantEnabled) {
        await enablePush();
      } else {
        await disablePush();
      }
    } catch (err) {
      pushEls.toggle.checked = !wantEnabled;
      if (!isNetworkError(err)) showError(err.message);
    } finally {
      pushEls.toggle.disabled = false;
    }
  });
}
