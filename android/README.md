# Android app (Capacitor)

An installable Android app for Trakka that works with any Trakka server: on first launch it asks for the server's address (typed, or scanned from a QR code), then opens that server's PWA full screen. The server can be changed at any time from Trakka's Paramètres, or with a long press on the app icon. Built with [Capacitor](https://capacitorjs.com/); the full guide, including what changes on GrapheneOS, is [docs/MOBILE_BUILD.md](../docs/MOBILE_BUILD.md).

```bash
make apk-keystore          # once: create the signing key
make build-apk-capacitor   # android/out/trakka.apk
```

| Path | What it is |
|---|---|
| `www/` | The connect screen bundled in the app (server address, QR code, recent servers) |
| `native/` | The Android Studio project: `MainActivity` (connect screen or server), `TrakkaAppPlugin` (the native API pages call), `QrScanActivity` (CameraX + ZXing), `BrowserSignIn` (SSO sign-in through the browser) |
| `capacitor.config.json`, `package.json` | Capacitor's configuration and the pinned Capacitor packages |
| `build.mjs` | The build script (`keystore`, `build`), run by the root [Makefile](../Makefile) inside the builder image |
| `Dockerfile` | Builder image: Node.js, JDK 21 and the Android SDK, all pinned |
| `apk.env.example` | Settings; copy it to `apk.env` |
| `signing/` | Your signing key and its passwords. Git-ignored: **back it up** |
| `out/` | The resulting `trakka.apk`. Git-ignored |
