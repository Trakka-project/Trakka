# Testing the Android app (APK)

A manual checklist for verifying a built `trakka.apk` on a phone or an emulator before handing it out. Copy it into an issue or a PR and tick the boxes as you go. How the app works, how to build it and every error message's cause are in [MOBILE_BUILD.md](MOBILE_BUILD.md).

The connect screen and the launcher shortcut follow the phone's language, and Trakka's own pages follow Trakka's language setting. Strings are quoted in English below. The French ones are direct translations ("Change server" / "Changer de serveur", "Try again" / "Réessayer"...).

## Before you start

You need:

- **The APK**: `android/out/trakka.apk` from `make build-apk-capacitor`, or `trakka.apk` from a [release](https://github.com/Trakka-project/Trakka/releases/latest).
- **A Trakka server over HTTPS** whose certificate the phone trusts (a public authority, or [your own authority installed on the phone](MOBILE_BUILD.md#a-server-with-a-private-certificate-authority)), and an account on it. Below, `<your-instance>` is its address, e.g. `trakka.example.com`.
- **Optional**: a second Trakka server, or the same server under a second host name (each host name counts as a separate server for the app), for the multi-server checks. An OIDC provider or an authentication proxy, for the sign-in redirect checks.
- **A QR code of the server's address**, on a computer screen or printed: `qrencode -t ansiutf8 'https://<your-instance>'`.

On an emulator (Android 7.0 / API 24 or newer), prefer an AOSP image without Google services, which is closer to GrapheneOS. The emulator must trust the server's certificate just as a phone would. Scanning needs a camera that sees a real code (`emulator -camera-back webcam0`), so the QR checks are best done on a phone.

To start over from a fresh install at any point (servers and sessions forgotten, camera permission reset):

```bash
adb shell pm clear io.github.trakka_project.app
```

## 1. Installation and permissions

- [ ] `adb install -r android/out/trakka.apk` prints `Success`. Alternatively, copy the APK to the phone, open it from the Files app, allow that app to install unknown apps, then revoke that permission (Settings → Apps → Special app access → Install unknown apps).
- [ ] The launcher shows **Trakka** with its icon.
- [ ] First launch: a splash screen with the Trakka icon, then the connect screen ("Connect the app"). **No permission dialog appears**: Network is granted at install, the camera is only asked on the first scan, and notifications only when you turn on task reminders (section 4).
- [ ] Settings → Apps → Trakka → Permissions: Camera not allowed. On GrapheneOS, Network allowed.
- [ ] *GrapheneOS*: turn the app's **Network** permission off, then try to connect: "Server not found: … or the phone has no network access." Turn it back on.

> `INSTALL_FAILED_UPDATE_INCOMPATIBLE` (adb) or "App not installed" means the installed app is signed with another key, e.g. a release APK over your own build. Uninstall it first: `adb uninstall io.github.trakka_project.app`.

## 2. Connecting to a server

### Wrong addresses

Type each address, tap **Connect**, and check the message under the field. After each error, the field stays editable and **Connect** can be tapped again: there is no separate retry button on this form.

- [ ] *(empty)* → "Enter your server's address."
- [ ] `not a server` → "This address is not valid."
- [ ] `http://<your-instance>` → "The app only connects over HTTPS: use an https:// address."
- [ ] `trakka-test.invalid` → "Server not found: trakka-test.invalid does not exist, or the phone has no network access."
- [ ] `https://self-signed.badssl.com` (or `https://expired.badssl.com`) → "This phone does not trust the HTTPS certificate of self-signed.badssl.com."
- [ ] `https://<your-instance>:9` (a closed port) → "<your-instance>:9 does not respond.", or "… does not respond (timed out)." after 10 s if a firewall drops the connection.
- [ ] `example.com` (answers, but isn't Trakka) → "This server answers (status …) but does not look like Trakka.", and a **Connect anyway** button appears. Leave it for [section 3](#changing-server).

### The right address

- [ ] `<your-instance>`, without `https://` → "Checking…", "Connecting…", then Trakka's sign-in page, full screen.
- [ ] Sign in → Trakka's Dashboard.

### Server unreachable, and "Try again"

This screen only appears **before you sign in**. Once you are signed in, Trakka's service worker caches the app and an outage shows Trakka in offline mode instead (see the last check).

- [ ] Run `adb shell pm clear io.github.trakka_project.app`, connect to `<your-instance>`, and **don't sign in**.
- [ ] Make the server unreachable (stop it, or turn on airplane mode), swipe the app away from recents and reopen it → the connect screen with "Could not open https://<your-instance>." followed by the reason ("… does not respond.", "… answered with an error (502)." behind a reverse proxy, "Server not found…" in airplane mode), and a **Try again** button.
- [ ] **Try again** while the server is still down → the same message again.
- [ ] Bring the server back, then **Try again** → the sign-in page.
- [ ] Signed in, make the server unreachable again → Trakka still opens, shows "Offline", and changes made meanwhile sync once the server is back.

### QR code scanner

- [ ] First tap on **Scan a QR code** → Android asks for the camera permission.
- [ ] **Deny** → back on the connect screen: "Camera access denied: allow it in the app's Android settings to scan a QR code."
- [ ] Allow it (from Android's prompt on the next tap, or from Settings → Apps → Trakka → Permissions) → a full-screen camera view, "Point the camera at the QR code of your Trakka server", and a **Close** button.
- [ ] **Close**, or the back button → back on the connect screen, with no error.
- [ ] Scan the server's code → the address fills in, then the app checks it and connects by itself.
- [ ] Scan a code of a Trakka page rather than the server's root (`qrencode -t ansiutf8 'https://<your-instance>/?list=1'`) → connects to `https://<your-instance>`.
- [ ] Scan a code that isn't an address (`qrencode -t ansiutf8 'not a server'`) → "This QR code does not contain a server address."
- [ ] Scan a **printed** code with the phone's real camera (never tried on a real device yet).

## 3. Core app behavior

### Full screen

- [ ] No address bar or browser toolbar, in portrait and landscape.
- [ ] Trakka's header is fully visible below the status bar (no overlap with the clock or icons), and nothing at the bottom is hidden behind the navigation bar or gesture handle. This hasn't been verified yet on Vanadium's current WebView, which draws the page under the status bar.
- [ ] The recent-apps screen shows Trakka as its own app.

### Back button

Use Android's back button or back gesture.

- [ ] With a dialog open (e.g. Settings) → back closes the dialog, and the app stays open.
- [ ] Inside a list → back returns to the Dashboard. With an item's sheet open inside the list, the first back closes the sheet and the second returns to the Dashboard.
- [ ] On the Dashboard → back leaves the app. Reopening it shows the same screen, still signed in.
- [ ] Right after signing in → back leaves the app instead of returning to the sign-in page.
- [ ] On the first-run connect screen (no server yet) → back leaves the app.

### Session persistence

- [ ] Swipe the app away from recents and reopen it → straight into Trakka, still signed in, with no connect screen.
- [ ] Same after Settings → Apps → Trakka → **Force stop**, and after rebooting the phone.
- [ ] **Log out** in Trakka → the sign-in page, still inside the app, and the server is kept.
- [ ] *Optional, updates*: build with a higher `ANDROID_VERSION_CODE` and the same key, then run `adb install -r` → the server and session are kept, and Settings shows the new version.

### Changing server

- [ ] Trakka → Settings shows an **App server** section with the server's address and "Android app <version>". The browser help "How do I install the app on my phone?" is gone.
- [ ] **Change server** → the connect screen, with **Recent servers** (the current one marked "current") and "Cancel and go back to <your-instance>".
- [ ] "Cancel and go back to …" → back in Trakka, still signed in. The back button does the same.
- [ ] Connect to a second server and sign in. Then **Change server** → tap the first server under **Recent servers** → it opens at once (no check) and you are still signed in there. Each server keeps its own session.
- [ ] Long-press the app icon → **Change server** → the connect screen, both when the app is closed and when it's in the background.
- [ ] Connect to `example.com` with **Connect anyway** → the site shows full screen, with no Trakka Settings to leave it. Long-press the icon → **Change server** → the connect screen: the shortcut is the way out of a server whose pages are broken.
- [ ] Remove `example.com` from **Recent servers** with its **×** button.

## 4. Hardware and PWA integrations

### Haptic feedback

Make sure vibration isn't turned off in Android's Settings → Sound & vibration.

- [ ] Long-press an item in a list → selection mode, with a short vibration.
- [ ] Long-press a list on the Dashboard → its actions sheet, with a short vibration.
- [ ] Swipe an item fully right (done) or left (delete) → a short tick when the action triggers.

### Theme

The picker is in Trakka → Settings: Light / Dark / System.

- [ ] **Dark** → Trakka turns dark, and the status and navigation bars match, with light, readable icons.
- [ ] **Light** → Trakka turns light, with dark, readable bar icons.
- [ ] **System**, then switch the phone's dark theme from Quick Settings while the app is open → Trakka follows immediately, without restarting.
- [ ] The connect screen follows the phone's theme. The QR scanner is always dark.

With an Android System WebView older than 140, the bars keep the phone's theme rather than Trakka's. That's expected.

### Task reminders (local notifications)

How they work: [MOBILE_BUILD.md, Notifications](MOBILE_BUILD.md#task-reminders-local-notifications). For a quick test, set the account's default to "At the exact due time" (Settings → Task notifications), or give each task below its own reminder time, a few minutes ahead. With USB debugging, `adb shell dumpsys alarm | grep -A3 io.github.trakka_project.app` lists the alarms the app holds (one per scheduled reminder).

- [ ] Settings → **Task notifications**: the switch reads **"Local reminders (works offline)"**, enabled and off, with the vibration option below it. No "not supported" message.
- [ ] Turn it on → Android asks to allow notifications (Android 13+) → **Allow** → the switch stays on. Close and reopen Settings: still on.
- [ ] *Permission refused*: `adb shell pm clear io.github.trakka_project.app` (or a fresh install), sign in, turn it on, **Don't allow** → the switch goes back off with "Trakka's notifications are blocked on this phone…". Allow notifications in Settings → Apps → Trakka → Notifications, then turn it on again.
- [ ] Create a task due today at a time 3 minutes from now, reminded at the due time. Leave the app (home screen) and wait: at that minute, a notification **"<task>"**, "🔔 Échéance aujourd'hui à HH:MM — <list>" (always in French, like push reminders), with the Trakka icon in the status bar, and a short double vibration.
- [ ] Tap it → the app opens on that task's list. Do it once with the app in the background, and once after swiping the app away from recent apps.
- [ ] **Offline**: create another task reminded 5 minutes from now, wait a few seconds (the app schedules it), then turn on **airplane mode** and close the app. At that minute the notification still comes.
- [ ] Back online, create a task reminded 4 minutes from now and wait a few seconds. Turn airplane mode on, check the task off, and leave the app. **No notification** comes for it: checking off cancels the reminder on the phone at once.
- [ ] Before its time comes, turn airplane mode off and uncheck the task: its reminder is scheduled again (`dumpsys alarm`). Then delete the task: no notification comes.
- [ ] *Recurring task*: a daily task due today with a reminder, checked off → its reminder for tomorrow is scheduled (it shows in `dumpsys alarm`, or wait for it).
- [ ] *Another device*: from a browser, give one of your tasks a reminder a few minutes ahead. Open the app online (or bring it back to the foreground), leave it → the reminder comes on the phone.
- [ ] *Vibration*: in Settings, uncheck "Enable notification vibrations" and **Save**, then schedule a task 2 minutes ahead → the notification comes silently, without vibration. Android's settings for the app now list two channels, "Task reminders" and "Task reminders (silent)".
- [ ] *After a restart*: schedule a reminder 5 minutes ahead, restart the phone and don't open the app → the notification still comes.
- [ ] Turn the switch off → `dumpsys alarm` no longer lists the app's reminders, and none comes.
- [ ] *In a browser* (not the app), Settings → Task notifications still reads "Enable push notifications" and works as before.

### Links and sign-in redirects

- [ ] An item with a product link → tapping the link opens it in the phone's browser, not in the app. Going back returns to Trakka unchanged.
- [ ] *If OIDC is configured*: "Se connecter avec <provider>" → the provider's page opens in a **browser tab over the app** (a Custom Tab: Vanadium on GrapheneOS), not in the app itself. After signing in there, the tab closes by itself and you're in Trakka in the app, and the back button then leaves the app rather than returning to the sign-in page. Opening Trakka in the browser afterwards shows its sign-in page: the session is the app's only.
- [ ] *If the provider uses passkeys or security keys* (Authentik's WebAuthn stages): in that browser tab, enrolling or using a passkey works, with no "Error creating credential" ([why it used to fail](MOBILE_BUILD.md#sso-oidc-passkeys-and-security-keys)).
- [ ] *SSO, cancelled*: tap "Se connecter avec <provider>", then close the browser tab with its ✕ → back on Trakka's sign-in page in the app; tapping the button again works.
- [ ] *SSO, failed*: sign in at the provider with an identity Trakka refuses (registration closed, or an email already used by another account) → back in the app, on Trakka's sign-in page with the error message.
- [ ] *Behind an authentication proxy* (Authelia, Authentik...): connecting shows "This server answers but redirects to <portal>: a sign-in page may protect it, or it is not Trakka." **Connect anyway** → the portal's sign-in page inside the app, then Trakka.

## Expected limitations (not failures)

- Only task reminders come as notifications in the app: an item added to or checked off in a shared list, or a price drop, doesn't notify the phone ([why](MOBILE_BUILD.md#other-notifications-web-push)).
- A task created or rescheduled while offline gets its reminder only once the phone is back online.
- File downloads do nothing, e.g. the admin console's "Download the key (.key)". Use a browser for those.
- Web pages never get the camera, microphone or location. The camera is only for the QR scanner.

## Reporting

With each report, include the device and Android version, the WebView version (`adb shell dumpsys webviewupdate | grep -i 'current webview'`, or Settings → Apps → Android System WebView / Vanadium System WebView), the APK's version and origin (your build or a release), the server's Trakka version, and the failed checks with screenshots. To capture the app's log while reproducing a failure:

```bash
adb logcat --pid="$(adb shell pidof io.github.trakka_project.app)"
```
