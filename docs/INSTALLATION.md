# Installing Trakka on your phone

Trakka is a Progressive Web App (PWA): there is no App Store or Play Store listing to install from. Instead, you open Trakka's URL once in your phone's browser and add it to your home screen from there — the icon that results behaves like any other installed app (its own icon, full-screen, no browser address bar) and keeps working offline, see [docs/PWA.md](PWA.md).

The same steps are also available in-app: open **Settings → "How do I install the app on my phone?"** for a shorter version of this guide that automatically shows the right platform's steps.

## Before you start

- **The link must be served over HTTPS** (or be exactly `http://localhost` on the same device) — this is a browser requirement for installable PWAs, not a Trakka-specific limitation. If your Trakka instance is only reachable over plain HTTP from your phone (e.g. `http://192.168.1.20:8080`), none of the steps below will offer to install anything; ask whoever runs your Trakka instance to put it behind HTTPS first. See [docs/PWA.md](PWA.md#requirement-https) and [docs/DEPLOYMENT.md](DEPLOYMENT.md) for the operator-facing side of this.
- Log in once from the browser before installing — the installed icon reopens the same session/PWA, it doesn't create a separate one.

## iOS and iPadOS (Safari)

Adding a site to the Home Screen on iOS/iPadOS only works from **Safari** — Chrome, Firefox, and other browsers on iOS all use Apple's WebKit engine under the hood but don't expose the same "Add to Home Screen" action, since Apple reserves it for Safari's own share sheet.

1. Open Trakka's link in **Safari**.
2. Tap the **Share** icon (the square with an arrow pointing up ⎘) in the toolbar — on iPhone it's at the bottom of the screen; on iPad it's in the top toolbar.
3. Scroll down the share sheet's list of actions and tap **"Add to Home Screen"**.
4. Confirm (or edit) the name Safari suggests, then tap **"Add"** in the top-right corner.

Trakka's icon now appears on your home screen and launches full-screen, with no Safari address bar — see [docs/PWA.md](PWA.md#iosipados-safari-whats-different) for what's different about iOS specifically (no install banner, no Background Sync, why the offline queue still works anyway).

## Android (Chrome, Firefox, Brave, ...)

1. Open Trakka's link in **Chrome** (or your preferred mobile browser).
2. Tap the **⋮** menu in the top-right corner.
3. Select **"Install app"** (Chrome may also show this as a banner or an install icon in the address bar without needing the menu at all) — if your browser doesn't offer that exact wording, look for **"Add to Home screen"** instead; both do the same thing.
4. Confirm the installation.

Unlike iOS, Android/Chrome can also flush queued offline changes in the background even when Trakka isn't open, via the Background Sync API — see [docs/PWA.md](PWA.md#iosipados-safari-whats-different) for the comparison.

### Android app (APK)

There is also an Android app, built by whoever runs your Trakka instance (see [docs/MOBILE_BUILD.md](MOBILE_BUILD.md)). If they give you a `trakka.apk` file, open it on your phone and allow the installation. On first launch, enter your Trakka server's address, or scan its QR code, then sign in: the app shows Trakka full screen, as a real app with its own icon, and Paramètres → "Changer de serveur" switches it to another server. It is the better choice on browsers that can only add a home-screen shortcut, such as Vanadium on GrapheneOS. One thing it can't do yet is receive push notifications; see [Notifications](MOBILE_BUILD.md#notifications).

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| No "Add to Home Screen"/"Install app" option appears at all | The site isn't served over HTTPS (or isn't `localhost`) — see "Before you start" above. |
| On iPhone/iPad, the option is missing from the share sheet | You're not using Safari — switch to it, since no other iOS browser exposes this action. |
| The installed icon opens a blank or outdated page | The service worker may still be fetching the app shell for the first time — reopen it once more while online, then it works offline too. |
| A new version doesn't seem to appear after a redeploy | Trakka shows an in-app "a new version is available" banner with an update button once it detects one — reopening the app is usually enough to trigger the check; see the Cache versioning section of [docs/PWA.md](PWA.md#cache-versioning). |

## See also

- [docs/PWA.md](PWA.md) — the technical design behind installability and offline support (service worker, manifest, IndexedDB sync queue).
- [docs/DEPLOYMENT.md](DEPLOYMENT.md) — for whoever runs a Trakka instance: why HTTPS matters and how to configure it.
