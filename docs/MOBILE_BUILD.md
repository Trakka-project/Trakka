# Building the Android app (APK)

Trakka's Android app works with any Trakka server: the first time it opens, it asks for the server's address, typed in or scanned from a QR code, then shows that server's Trakka full screen, like an installed app. It is built with [Capacitor](https://capacitorjs.com/): a native Android shell around a WebView. The same APK serves every self-hosted instance, the server needs no change or extra configuration, and the PWA inside keeps updating itself from the server.

This page covers what the app does, building it, and installing it, including on GrapheneOS. Each Trakka release also comes with a ready-made, signed `trakka.apk`, built by GitHub Actions ([Releases built by GitHub Actions](#releases-built-by-github-actions)). To install the PWA straight from a browser instead, see [INSTALLATION.md](INSTALLATION.md).

## How the app works

- **Connecting.** A connect screen, bundled in the app, takes the server's address (`trakka.example.com`, `https://trakka.home.lan:8443`...) or scans a QR code containing it, and lists the servers used recently. Before switching, the app checks that the address answers over HTTPS with a certificate the phone trusts, and that it is Trakka; otherwise it says what is wrong ("Server not found", "certificate not trusted", "does not look like Trakka", with a way to connect anyway, for a server behind a sign-in portal).
- **Using Trakka.** The app then loads the server's pages straight from the server, as a browser would, with their own security headers, full screen and with no address bar, whatever the server. Trakka's SSO button signs in through the phone's browser and comes back to the app ([SSO](#sso-oidc-passkeys-and-security-keys)); an authentication proxy's portal stays in the app; a link to another site, such as a product page, opens in the browser. Android's back button closes Trakka's dialogs, then leaves the app.
- **Changing server.** In Trakka's Paramètres, the "Serveur de l'application" section shows the server and offers "Changer de serveur", which opens the connect screen ("Annuler" goes back). A long press on the app icon offers "Changer de serveur" too, which works even when the server's pages are broken or come from a Trakka older than this section. When the server cannot be opened, the app shows the connect screen with the reason and a "Réessayer" button. Each server keeps its own session: switching back to a server you used finds you signed in.

## What works, and what doesn't yet

| | In the app |
|---|---|
| Trakka's interface, offline mode | As in the installed PWA: the service worker and the offline queue run in the app's WebView |
| Look | Full screen, no address bar, light or dark like Trakka, the status and navigation bars matching |
| Haptic feedback | Yes (the app holds `VIBRATE`) |
| File uploads (`<input type="file">`) | Yes |
| **Push notifications** | **Not yet.** Android's WebView has neither Web Push nor the Notifications API, so Paramètres greys out "Activer les notifications push", says it isn't supported in the mobile app yet and points to the PWA in a browser, and hides the vibration option. See [Notifications](#notifications) |
| File downloads | No: the WebView ignores them. The admin console's "Télécharger la clé" (backups) has to be done from a browser |
| Camera, microphone, location for web pages | Never: the app refuses them, the camera is only for its own QR code scanner |
| SSO (OIDC) with passkeys or security keys | Yes, through the browser: see [SSO](#sso-oidc-passkeys-and-security-keys). Needs a Trakka server at least as recent as the app |
| WebAuthn on an authentication proxy's portal | No: the portal shows in the app's WebView, where WebAuthn is unavailable. See [SSO](#sso-oidc-passkeys-and-security-keys) |

The app's WebView is Android System WebView: on GrapheneOS, Vanadium's. It shares nothing with the browser: you sign in once in the app (an SSO sign-in goes through the browser, but the Trakka session it opens is the app's).

## SSO (OIDC), passkeys and security keys

An identity provider such as Authentik, Keycloak or Authelia may ask for a passkey, a fingerprint or a security key, through the browser's WebAuthn API. Android's WebView, which shows Trakka in the app, cannot do that for them: it only runs WebAuthn for sites tied to the app by [Digital Asset Links](https://developer.android.com/training/app-links/verify-android-applinks) (an `assetlinks.json` naming the app's signing key on each provider's domain) or for apps registered as browsers. An app meant for any self-hosted server, signed with each builder's own key, can be neither. In the WebView, the provider fails: Authentik says "Error creating credential".

So Trakka's "Se connecter avec …" button, in the app, runs the sign-in in the phone's browser instead, in a Custom Tab (the browser's own tab, shown over the app; Vanadium on GrapheneOS), where WebAuthn works as on any website and where you may already be signed in to the provider:

1. Tapping the button opens the server's `/auth/oidc/login` in a Custom Tab. You sign in to the provider there, passkey included.
2. Trakka then sends the browser back to the app (`io.github.trakka-project.app://sso`), with a single-use code. Android closes the tab and returns to the app.
3. The app redeems the code from its WebView, which opens your Trakka session in the app. The browser holds no Trakka session.

The code alone is worth nothing: redeeming it also takes a secret the app keeps (the PKCE principle), so another app declaring the same link cannot use it. The flow is described in [API.md](API.md#sso-sign-in-from-the-android-app).

What it takes:

- **Nothing on the provider's side.** Its redirect URI stays `https://<trakka>/auth/oidc/callback`: the browser, not the app, follows it.
- **Nothing on the server's side but its version.** The single Trakka container handles it; the code waits in its memory for up to 2 minutes. A Trakka server older than this feature has no browser sign-in: its button runs the sign-in in the app's WebView as before, which works with providers that don't ask for WebAuthn.
- **A browser on the phone.** Without one, the button falls back to the WebView. A browser without Custom Tabs opens the page as a normal tab, and the sign-in still comes back to the app.

**Authentication proxies** (Authentik's or Authelia's forward auth in front of Trakka) are not covered: their portal stands in front of every Trakka page, so there is no Trakka page to hand the sign-in to the browser, and its session cookie would stay in the browser anyway. Their portal shows in the app's WebView, where WebAuthn fails. Give such a portal a sign-in method other than WebAuthn for the app (password plus TOTP, for instance), or protect Trakka with its own OIDC sign-in rather than with the proxy.

## Notifications

Trakka sends standard Web Push messages, which a WebView cannot receive: the browser normally does that part, and the app has no browser. Push in the app therefore needs a native receiver. The planned way is [UnifiedPush](https://unifiedpush.org/developers/intro), which works without Google on GrapheneOS: the app registers with a distributor installed on the phone (ntfy, for instance), generates the encryption keys, and gets an endpoint. UnifiedPush's push servers speak Web Push (RFC 8030), with RFC 8291 encryption and VAPID, which is exactly what Trakka's server sends, and `POST /api/v1/push/subscribe` accepts any `https://` endpoint: the app could register itself there and display the messages, without a server change. One limit: Trakka's Web Push sender only connects to public addresses (its SSRF guard), so the distributor's push server must be one (ntfy.sh, or a self-hosted ntfy reachable from the internet), not a LAN or Tailscale address. That receiver is not built yet; the app already declares `POST_NOTIFICATIONS` for it, which Android 13+ only asks the user about once the app requests it.

Until then, for push on the phone, use the PWA from a browser that delivers Web Push ([INSTALLATION.md](INSTALLATION.md)); on GrapheneOS, Vanadium doesn't.

## Requirements

- **Podman or Docker.** The build runs in a container image ([android/Dockerfile](../android/Dockerfile)) holding Node.js, JDK 21 and the Android SDK, all pinned, so nothing else needs installing. The first build downloads about 2 GB (the image, then Gradle and the Android libraries, cached in `~/.cache/trakka-apk/`); later builds take well under a minute. Building the image **accepts the [Android SDK License Agreement](https://developer.android.com/studio/terms) on your behalf**.
- **For using the app: Trakka served over HTTPS**, with a certificate the phone trusts: from a public authority (Let's Encrypt...), or from your own certificate authority installed on the phone ([see below](#a-server-with-a-private-certificate-authority)). Any host name or port works; plain HTTP doesn't, since Trakka's service worker and session cookie need HTTPS anyway ([DEPLOYMENT.md](DEPLOYMENT.md#docker)).

## Building

```bash
make apk-keystore          # once: creates the signing key
make build-apk-capacitor   # android/out/trakka.apk
```

`make build-apk` is the same target under a shorter name.

### Settings

None is required: the APK has no server address built in. To change one, copy [android/apk.env.example](../android/apk.env.example) to `android/apk.env` (git-ignored), or set it in the environment or on the `make` command line (which win over the file).

| Variable | Default | Description |
|---|---|---|
| `ANDROID_VERSION_CODE` | `1` | Version code of the app. Android only installs an APK over the existing app if its code is not lower: raise it for every APK you hand out |
| `ANDROID_VERSION_NAME` | `1.0.0` | Version shown in Android's settings and in Trakka's Paramètres. It is the app's version, not Trakka's |
| `ANDROID_KEYSTORE` / `ANDROID_KEY_ALIAS` | `signing/trakka.keystore` / `trakka` | Signing key, relative to `android/` (keep it there: it is the directory mounted into the build container) |
| `ANDROID_KEYSTORE_PASSWORD` / `ANDROID_KEY_PASSWORD` | read from `android/signing/keystore.env` | Passwords of the key |

`make` variables, for the container itself: `CONTAINER_ENGINE` (`podman` when installed, `docker` otherwise), `APK_RUN_FLAGS` and `APK_IMAGE_BUILD_FLAGS` (extra flags for `run` and `build`, such as `--network=host`), `APK_USER_FLAGS` (how the build runs as you, see [Troubleshooting](#troubleshooting)) and `APK_CACHE`.

### Behind `make build-apk-capacitor`

[android/build.mjs](../android/build.mjs) runs in the builder image, with only `android/` mounted (a local `.env` or `trakka.db` never enters a container that runs npm packages and Gradle plugins). It:

1. installs the Capacitor packages pinned by `android/package-lock.json` (`npm ci`, skipped while the lockfile is unchanged);
2. runs `cap sync android`, which copies the connect screen (`android/www/`) and `capacitor.config.json` into the native project (`android/native/`);
3. runs Gradle's `assembleRelease`, signing with your key, whose passwords go through the environment rather than the command line;
4. checks the result with `aapt2` and `apksigner` (package, permissions, signature) and copies it to `android/out/trakka.apk`.

## The signing key

`make apk-keystore` creates `android/signing/trakka.keystore` (PKCS12, RSA 4096, valid for 10,000 days, alias `trakka`) and stores its random password in `android/signing/keystore.env`, both readable by you only. It refuses to replace an existing key. The equivalent by hand:

```bash
keytool -genkeypair -keystore android/signing/trakka.keystore -storetype PKCS12 \
  -alias trakka -keyalg RSA -keysize 4096 -validity 10000 -dname CN=Trakka
```

It is a self-generated key rather than one managed by an app store, but it becomes the key of your app as soon as you install it: Android only installs an update over the app if it is signed with the same key. So:

- **Back up `android/signing/`** (both files), for example in your password manager.
- **Never commit it or share it.** `.gitignore` excludes `android/signing/`, `*.keystore` and `*.jks`; `.dockerignore` keeps the whole `android/` directory out of the server image.
- **To use a key of your own**, put it under `android/` and set `ANDROID_KEYSTORE`, `ANDROID_KEY_ALIAS` and the passwords.
- **If the key is lost**, uninstall the app from the phones, delete `android/signing/`, and run `make apk-keystore` and `make build-apk-capacitor` again.

## Releases built by GitHub Actions

[`.github/workflows/build-apk.yml`](../.github/workflows/build-apk.yml) builds the APK the same way (`make build-apk-capacitor`, with Docker) and attaches it to Trakka's GitHub releases, so that nobody has to build it to install it: [latest release](https://github.com/Trakka-project/Trakka/releases/latest), or directly <https://github.com/Trakka-project/Trakka/releases/latest/download/trakka.apk>.

- **Publishing a release** (from a `vX.Y.Z` tag) builds `trakka.apk`, signed with the release key, and attaches it with `trakka.apk.sha256` and a build provenance attestation. The app's version comes from the tag: `v1.3.0` gives version name `1.3.0` and version code `1003000`, so each release installs over the previous one. The run's page sums up the package, version, signing certificate and checksum.
- **Running the workflow by hand** (Actions → Android APK → Run workflow): from a tag, it rebuilds and re-attaches that release's APK, for instance after fixing the secrets; from a branch, it builds a development APK (version code 1, so that any release installs over it), downloadable from the run's page for 14 days.
- Releases made before the app existed (v1.2.0 and older) have no `android/`, and no APK.

### Setting up the secrets

The workflow signs with the key `make apk-keystore` created, stored in repository secrets (Settings → Secrets and variables → Actions → New repository secret):

| Secret | Value |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | The keystore file in base64, on one line: `base64 -w0 android/signing/trakka.keystore` (on macOS, `base64 -i android/signing/trakka.keystore`) |
| `ANDROID_KEYSTORE_PASSWORD` | The `ANDROID_KEYSTORE_PASSWORD` value from `android/signing/keystore.env` |
| `ANDROID_KEY_PASSWORD` | The `ANDROID_KEY_PASSWORD` value from the same file. Optional for a key made by `make apk-keystore`, where it is the same password |

With the [GitHub CLI](https://cli.github.com/), from the repository's root, without the values ever showing on screen or landing in the shell history:

```bash
base64 -w0 android/signing/trakka.keystore | gh secret set ANDROID_KEYSTORE_BASE64
grep '^ANDROID_KEYSTORE_PASSWORD=' android/signing/keystore.env | cut -d= -f2- | tr -d '\n' | gh secret set ANDROID_KEYSTORE_PASSWORD
grep '^ANDROID_KEY_PASSWORD=' android/signing/keystore.env | cut -d= -f2- | tr -d '\n' | gh secret set ANDROID_KEY_PASSWORD
```

A key alias other than `trakka` goes in the repository *variable* `ANDROID_KEY_ALIAS` (it is not secret). Without the secrets, the workflow stops with an error naming the missing one.

The secrets are the release key: whoever can change this repository's workflows could use them, and GitHub never shows a secret again, so keep your own backup of `android/signing/`. To restrict them further, you can move them to a GitHub environment that only `v*` tags may use, and add `environment:` with its name to the workflow's job (development builds from branches then lose access to them).

### Checking a downloaded APK

```bash
sha256sum -c trakka.apk.sha256
gh attestation verify trakka.apk --repo Trakka-project/Trakka
```

The attestation proves the APK was built by this repository's workflow, from the release's commit. To check who signed it, compare its certificate (`apksigner verify --print-certs trakka.apk`, or [AppVerifier](https://github.com/soupslurpr/AppVerifier) on GrapheneOS) with the "signed by" fingerprint on the release's workflow run.

An APK you build yourself is signed with your own key: Android won't install it over the release's APK, or the other way round, without uninstalling first.

## Installing on the phone

Download `trakka.apk` on the phone from the [latest release](https://github.com/Trakka-project/Trakka/releases/latest), or copy the one you built (`android/out/trakka.apk`) to it (USB file transfer, a synced folder, an email to yourself), and open it from the Files app. Android asks you to allow that app to install unknown apps: allow it, install, then you can revoke that permission (Settings → Apps → Special app access → Install unknown apps). With USB debugging enabled, `adb install -r android/out/trakka.apk` from a computer does the same.

**On GrapheneOS** the steps are the same, and nothing more is required: no sandboxed Google Play, no app store. Keep the app's "Network" permission on (GrapheneOS grants it at install unless you turned that default off): the app does its own networking. The camera permission is only asked for the first time you scan a QR code.

Open the app, give it your server's address or scan its QR code, then sign in to Trakka as usual.

To check a new APK on a phone or an emulator before handing it out, follow the checklist in [TESTING_APK.md](TESTING_APK.md).

### A QR code for your server

Any QR code containing the server's address works, for instance printed for the family. From a terminal:

```bash
qrencode -t ansiutf8 'https://trakka.example.com'
```

or any QR code generator. The app reads the address only, so a code containing the address of any Trakka page works too.

### A server with a private certificate authority

If your server's certificate comes from your own certificate authority (a home network, Caddy's internal CA, step-ca...), install that CA's certificate on the phone: Settings → Security & privacy → More security settings → Encryption & credentials → Install a certificate → CA certificate (the wording varies between Android versions). The app trusts the authorities installed this way, as Chrome does, which most apps don't.

### Updating

New Trakka versions need no new APK: the app always shows the server's current Trakka, which updates itself. Rebuild the APK only for a new version of the app itself: raise `ANDROID_VERSION_CODE`, run `make build-apk-capacitor`, and install the new APK over the old one. Signed with the same key, it updates in place and keeps your servers and sessions.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| "The app only connects over HTTPS" | The address starts with `http://`: serve Trakka over HTTPS ([DEPLOYMENT.md](DEPLOYMENT.md#docker)) |
| "This phone does not trust the HTTPS certificate" | A self-signed certificate, a private authority not installed on the phone ([above](#a-server-with-a-private-certificate-authority)), an expired certificate, or one for another host name |
| "Server not found" | A typo in the address, or a name the phone cannot resolve (a LAN-only name, a VPN that is off) |
| "This server answers but does not look like Trakka" | A wrong address, or a sign-in portal in front of Trakka (Authelia, Authentik...): "Connect anyway" |
| The app opens on the connect screen with "Could not open…" | The server is down or unreachable: "Try again" once it is back |
| A page is broken and Paramètres can't be reached | Long press on the app icon → "Change server" |
| "App not installed" when updating | The new APK isn't signed with the key of the installed app: use the original key, or uninstall the app first |
| "Error creating credential" (Authentik), or another WebAuthn error, on the provider's page inside the app | The sign-in runs in the WebView: the server predates the browser sign-in (update Trakka), or the provider is an authentication proxy's portal. See [SSO](#sso-oidc-passkeys-and-security-keys) |
| After an SSO sign-in, the browser stays open instead of returning to the app, or Trakka's sign-in page shows "La connexion via le fournisseur externe a échoué" | The sign-in took longer than 10 minutes, or the app was reinstalled during it: tap the button again. If the browser asks which app to open the link with, pick Trakka |
| Trakka says push notifications aren't supported | Expected for now: see [Notifications](#notifications) |
| The "Android APK" workflow stops on a missing secret | The repository secrets aren't set up: see [Setting up the secrets](#setting-up-the-secrets), then run the workflow by hand from the release's tag |
| The workflow fails at "Attach the APK to the release" | It was run by hand from a tag that has no GitHub release yet: publish the release, which runs the workflow by itself |
| The build can't resolve or download anything | The container has no working DNS (common with `systemd-resolved` or a VPN, see [DEPLOYMENT.md](DEPLOYMENT.md#when-the-build-cant-download-go-modules)): `make build-apk-capacitor APK_RUN_FLAGS=--network=host APK_IMAGE_BUILD_FLAGS=--network=host` |
| `permission denied` writing under `android/`, with rootless Docker | Run as the container's root, which is you there: `make build-apk-capacitor APK_USER_FLAGS=` |

## Security

- **HTTPS only.** Clear-text traffic is refused (`res/xml/network_security_config.xml`). Trusted certificates: the system's, and those the user installed.
- **The server's headers apply.** The app loads Trakka's pages straight from the server, Content-Security-Policy included. Capacitor would proxy them, dropping those headers, on a WebView too old to inject its bridge otherwise; the app refuses to run on such a WebView instead.
- **What the server's pages can do.** As in any Capacitor app, the pages of the configured server, and only them, reach the app's native bridge: Capacitor's own plugins, and the app's `TrakkaApp`, of which they may only read the server's address, open the connect screen, and start an SSO sign-in in the browser on that same server. Switching to another server, scanning or listing servers is refused unless the connect screen is showing, so a page cannot send the app elsewhere. Camera, microphone and location requests from pages are refused. Identity-provider pages reached by a redirect get no bridge. Connect the app only to servers you trust as much as an installed app.
- **SSO through the browser.** The deep link that ends a sign-in carries a code that only opens a session together with a verifier the app keeps in its private storage, and that the server forgets after one attempt or 2 minutes. The app ignores such a link unless it started a sign-in on the server it shows, less than 10 minutes earlier.
- **No backups** of the app's data, whose WebView cookies hold session tokens (`allowBackup="false"`, `res/xml/data_extraction_rules.xml`).
- **No Google services.** The app contains no Google Play services, Firebase, or analytics; its QR code scanner is CameraX with ZXing, decoding on the device.

## Maintenance

- **Layout.** [android/README.md](../android/README.md) lists what is where. `android/native/` is an ordinary Android Studio project: open it after `npm ci` and `npx cap sync android` in `android/` (or `npx cap open android`). After editing `android/www/`, `npx cap sync android` copies it into the project; `make build-apk-capacitor` does that by itself.
- **The release workflow** ([.github/workflows/build-apk.yml](../.github/workflows/build-apk.yml)) follows the same rules as `ci.yml`: no permission beyond those its job declares, every action pinned to a full commit SHA ([.claude/ci-security.md](../.claude/ci-security.md)). It runs the same `make build-apk-capacitor`, so whatever builds locally builds there.
- **Upgrading Capacitor**: change the three exact versions in `android/package.json`, run `npm install` in `android/`, check the Android Gradle Plugin, SDK and JDK versions the new release expects against `android/native/variables.gradle`, `android/native/build.gradle` and the versions pinned in [android/Dockerfile](../android/Dockerfile), then build. The SDK in the builder image is read-only, so a missing package fails the build instead of being downloaded during it.
- **The lockfile is part of CI's checks**: Trivy's filesystem scan fails on any fixable HIGH or CRITICAL advisory in `android/package-lock.json` ([.claude/ci-security.md](../.claude/ci-security.md)).
- **The Android command-line tools stay at 21.0, on purpose**: they only install the SDK packages while the image builds. From 22.0 on they bring Google's "Android CLI", which the `sdkmanager` of 23.0 delegates to; it downloads a runtime of its own and uploads usage metrics by default.
- **Status**: built and verified end to end on an Android 16 emulator (AOSP image, no Google services, WebView 133): the connect screen and each of its errors, the switch to a real Trakka over HTTPS through a user-installed authority, signing in, the Paramètres section, changing server and coming back, the launcher shortcut, the QR code scanner (camera permission refused and granted, the decoding itself checked separately), an SSO-style redirect staying in the app and an external link opening the browser, the back button, light and dark, French and English, and the native bridge refusing a server page's attempts to switch servers. Not yet tried on a real GrapheneOS phone: scanning a printed code with a real camera, and Vanadium's current WebView, which lays the page out under the status bar (the emulator's older WebView pads it instead). [TESTING_APK.md](TESTING_APK.md) is the checklist for that pass. The SSO sign-in through the browser was added later, and checked on a GrapheneOS phone against Authentik.
