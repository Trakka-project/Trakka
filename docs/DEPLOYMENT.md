# Deployment

Trakka ships as a single static Go binary in a minimal Alpine image, with a [compose.yml](../compose.yml) that works unchanged under Docker and Podman rootless.

## Configuration (environment variables)

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `DB_PATH` | `/data/trakka.db` | SQLite database file path |
| `STATIC_DIR` | `/app/static` | Directory served as static assets at `/` |
| `TEMPLATES_DIR` | `/app/templates` | Directory containing `login.html` |
| `BASE_URL` | *(empty)* | Externally-visible origin (e.g. `https://trakka.example.com`), used to build the OIDC `redirect_uri`. **Required if OIDC is configured**; the server refuses to start otherwise. |
| `OIDC_ISSUER` | *(empty)* | OIDC provider issuer URL (e.g. `https://auth.example.com`). Set together with the two vars below, or leave all three empty to disable OIDC. |
| `OIDC_CLIENT_ID` | *(empty)* | OIDC client id registered with the provider. |
| `OIDC_CLIENT_SECRET` | *(empty)* | OIDC client secret. Treat as sensitive — pass it as a secret/secret-file in production, not a plain compose env value, the same way you would any other credential. |
| `SESSION_COOKIE_SECURE` | `true` | Whether the session cookie gets the `Secure` attribute. Only set to `false` for plain-HTTP `localhost` testing (see [docs/PWA.md](PWA.md) — the same HTTPS-or-localhost requirement service workers have). |
| `SESSION_TTL_HOURS` | `720` (30 days) | How long a session stays valid after login. |
| `PRICE_CHECK_INTERVAL_HOURS` | `24` | How often the background price-drop scan (see [docs/API.md](API.md#price-alerts)) re-checks every item that has both a `url` and a `price`. Set to `0` (or negative) to disable the periodic scan entirely — the on-demand `POST /api/v1/items/{id}/price-check` endpoint still works regardless. |
| `SCRAPE_INTERVAL` | `12h` | How often the target-price background worker (see [docs/API.md](API.md#price-drop-alerts)) re-scrapes every item with an active `alert_on_price_drop` threshold and applies whatever price it finds — distinct from `PRICE_CHECK_INTERVAL_HOURS` above, which drives the separate accept/reject deal-detection scan. Accepts a plain Go duration (`2h`, `30m`) or a whole number of days with a `d` suffix (`1d`). Set to `0` (or negative) to disable the periodic scan entirely. |
| `INSTANCE_NAME` | `Trakka` | Display name shown in the login page's `<title>` and, once loaded, the SPA header. Overridable at runtime by an admin without a restart (see [docs/API.md](API.md#admin-settings)) — this is only the starting value on a fresh `system_settings` table. |
| `REGISTRATION_OPEN` | `true` | Whether `POST /auth/register` accepts new local accounts. Also overridable at runtime via [Admin settings](API.md#admin-settings), which takes priority once set. |
| `VAPID_PUBLIC_KEY` | *(empty)* | Web Push application-server public key, base64url — see [docs/API.md](API.md#push-notifications). Set together with the two vars below, or leave all three empty to disable Web Push entirely. Generate a fresh pair with `trakka -generate-vapid-keys`. |
| `VAPID_PRIVATE_KEY` | *(empty)* | Web Push application-server private key, base64url. Treat as sensitive, the same as `OIDC_CLIENT_SECRET`. |
| `VAPID_SUBJECT` | *(empty)* | Contact URI sent in every VAPID JWT (a `mailto:` or `https:` URL, e.g. `mailto:ops@example.com`) — some push services use this to reach an operator about a misbehaving sender. |
| `APP_TIMEZONE` | `Europe/Paris` | IANA time zone (e.g. `UTC`, `America/New_York`) task due-date reminders (see [docs/API.md](API.md#reminders)) are interpreted in — a reminder's `HH:MM` time of day is otherwise meaningless, since there is no other per-user or per-instance notion of a time zone anywhere in this app. A single instance-wide setting rather than per-user, matching Trakka's one-household-per-instance design. An unrecognized value falls back to `UTC` with a startup warning rather than failing to start. |
| `NOTIF_DUE_SCAN_INTERVAL_MINUTES` | `30` | How often the due-date reminder scan itself runs — independent of, and normally much finer-grained than, any individual reminder's own offset/time, so a reminder due at e.g. `09:00` is actually caught close to on time. `0` (or negative) disables the periodic scan; only takes effect when Web Push is configured at all. |
| `DEFAULT_APP_LANGUAGE` | `en` | UI language (`fr` or `en`) shown to any account that has never set its own preference from the "Langue" section of the "Paramètres" modal — see [docs/API.md](API.md#get-apiv1me). Applies retroactively to every such account (new and pre-existing) since it's resolved at read time, not baked in at account creation. An unrecognized value falls back to `en`. |
| `BACKUP_WEBDAV_ALLOW_PRIVATE` | `false` | Lets [encrypted WebDAV backups](#encrypted-webdav-backups) reach a WebDAV server on a private, loopback or CGNAT/Tailscale address (`192.168.x.x`, `10.x.x.x`, `100.64.0.0/10`, `127.0.0.1`, `fd00::/8`, …) — the usual self-hosted setup, e.g. a Nextcloud on the same LAN. Off by default: the WebDAV URL is entered at runtime from the admin console, so the backup client applies the same public-addresses-only SSRF guard as the price scraper and Web Push until you, who know this deployment's network, opt in. Link-local addresses (including the `169.254.169.254` cloud metadata endpoint), multicast and unspecified addresses stay blocked even when enabled. |

There is no config file — every setting is an environment variable, set in [compose.yml](../compose.yml) or passed to `docker run` / `podman run`. OIDC is only enabled when `OIDC_ISSUER`, `OIDC_CLIENT_ID`, and `OIDC_CLIENT_SECRET` are **all** set; setting one or two of the three fails startup with a clear error rather than silently half-enabling it. See [docs/API.md](API.md#authentication) for the resulting `/auth/...` endpoints. Web Push is the same all-or-nothing shape: `VAPID_PUBLIC_KEY`/`VAPID_PRIVATE_KEY`/`VAPID_SUBJECT` must all be set together, or all left empty.

**OIDC, registration, and the instance name can all also be changed at runtime**, without touching environment variables or restarting the container, by an admin user through `PATCH /api/v1/admin/settings` (see [docs/API.md](API.md#admin-settings)) — these settings are persisted in the `system_settings` table (see [docs/DATABASE.md](DATABASE.md#system_settings)) and take priority over the environment variables above whenever a value has been set that way. `BASE_URL` is the one exception: it stays environment-only (there was no need to make the externally-visible origin itself admin-editable), so it must still be set as an env var before OIDC can be enabled through the admin panel, exactly as it must be to enable OIDC through `OIDC_*` env vars directly. The very first account ever registered on a fresh instance automatically becomes an admin (see [docs/DATABASE.md](DATABASE.md#users)) — there is no separate seeding step or CLI flag to grant that role.

## Docker

```bash
docker compose up -d --build
```

This builds the image from the [Dockerfile](../Dockerfile), starts `trakka` on `localhost:8080`, and persists data in the `trakka_data` named volume.

`compose.yml` exposes Trakka over plain HTTP, which is fine for `localhost` testing but **not enough for the PWA/offline features on a real phone**: browsers only allow service worker registration in a secure context (`https://`, or exactly `http://localhost`). To install Trakka and get offline support on an actual iOS/Android device, put a TLS-terminating reverse proxy (Caddy, Traefik, nginx + Let's Encrypt, etc.) in front of port `8080` — see [docs/PWA.md](PWA.md).

Equivalent manual `docker run`:

```bash
docker build -t trakka:latest .
docker run -d --name trakka \
  -p 8080:8080 \
  -v trakka_data:/data \
  trakka:latest
```

## Podman (rootless)

The same `compose.yml` works with `podman-compose`:

```bash
podman-compose up -d
```

Or without compose:

```bash
podman build -t trakka:latest .
podman run -d --name trakka \
  -p 8080:8080 \
  -v trakka_data:/data \
  trakka:latest
```

No `--userns=keep-id` or extra rootless flags are needed for normal operation: the container process runs as a **fixed numeric UID:GID (`10001:10001`)**, set in the Dockerfile (`adduser -u 10001 ... && USER 10001:10001`) rather than a symbolic `USER trakka`. A numeric UID is what makes ownership of the `/data` volume behave predictably under Podman's user-namespace remapping — the UID inside the container maps to a UID in your subuid range on the host, and stays consistent across `docker run` and `podman run` alike.

## Image build

Multi-stage [Dockerfile](../Dockerfile):

1. **Build stage** — `golang:1.27.0-alpine`, compiles `./cmd/server` with `CGO_ENABLED=0` to a fully static, stripped binary (`-ldflags="-s -w"`); `modernc.org/sqlite` is pure Go, so this works with no `libsqlite3`. This stage also creates the `/etc/passwd`/`/etc/group` entries for UID/GID `10001` and an empty, correctly-owned `/data` directory, since neither can be created in the shell-less runtime stage below.
2. **Runtime stage** — [`gcr.io/distroless/static-debian12`](https://github.com/GoogleContainerTools/distroless), not Alpine: no shell, no package manager, nothing beyond CA certificates and the binary itself. Only the compiled `trakka` binary, `static/`, `templates/`, and the passwd/group/`/data` entries prepared in the build stage are copied in; `USER 10001:10001` (the same fixed non-root UID as before) is set explicitly rather than relying on the image's own built-in `nonroot` user (which is UID `65532`), to keep the Podman rootless-mapping behavior described below unchanged.

The Go version in the build stage and the `go` directive in [go.mod](../go.mod) are intentionally kept in lockstep (both `1.27.0`) — see [CLAUDE.md](../CLAUDE.md) if you need to bump the SQLite driver or Go version. The distroless runtime tag is not part of that lockstep (it tracks Debian 12 rebuilds, not a Go version); pin it to a digest before relying on it for production reproducibility.

### When the build can't download Go modules

Go modules are downloaded into their own layer (`RUN go mod download`, re-run only when `go.mod`/`go.sum` change). That step needs to reach `proxy.golang.org`. If it fails with an error like `lookup proxy.golang.org: i/o timeout`, work through these fixes in order:

1. **Build with the host's network.** This is the usual cause on a Linux laptop running `systemd-resolved`. `/etc/resolv.conf` points at the local stub `127.0.0.53`, which a build container on Docker's bridge network can't use, so Docker falls back to public resolvers (`8.8.8.8`) that a VPN such as Tailscale, a hotspot, or a filtered network may block. With `--network=host`, the build steps use the host's own resolver:

   ```bash
   docker build --network=host -t trakka:latest .
   ```

   To fix it permanently for every container instead, give the Docker daemon a reachable DNS server in `/etc/docker/daemon.json` (`{"dns": ["<your DNS server>"]}`, listed by `resolvectl dns`), then restart Docker.
2. **Use a reachable module mirror**, if `proxy.golang.org` itself is blocked: `--build-arg GOPROXY=https://goproxy.io` (or your organization's Athens/Artifactory proxy). `GOPROXY=direct` won't work in this image: it fetches from each module's own repository and the Alpine build image has no `git`.
3. **Build fully offline from vendored modules.** Run `go mod vendor` somewhere that does have access (it writes `vendor/`), then pass the directory as a named build context:

   ```bash
   go mod vendor
   docker build --build-context vendor=./vendor -t trakka:latest .
   ```

   The Dockerfile's otherwise empty `vendor` stage is replaced by that directory, `go mod download` is skipped, and `go build` compiles from `vendor/` without any network access. `vendor/` is in `.dockerignore`, so an ordinary build never uploads it. It isn't committed to the repository either, so re-run `go mod vendor` after any dependency change, or delete `vendor/` once you no longer need it: while it exists, local `go build`/`go test` also use it instead of the module cache. `--build-context` needs BuildKit (Docker 23+) or Podman 4.4+.

The compile step itself uses a BuildKit cache mount for Go's build cache, so rebuilds after a source change are incremental. The module download deliberately does *not* use a cache mount: CI's GitHub Actions layer cache (`cache-from: type=gha`) only stores layers, so keeping modules in a layer is what spares each CI build a fresh download.

Distroless ships no `tzdata`, unlike the old Alpine runtime image (which had it installed via `apk add`). This is harmless here: the app never calls `time.LoadLocation`, and every timestamp it stores or logs is UTC by convention (see [CLAUDE.md](../CLAUDE.md)) — with no zoneinfo database and no `TZ` set, Go's `time.Local` simply behaves as UTC. `compose.yml` no longer sets `TZ`, since distroless would silently ignore it anyway.

## Encrypted WebDAV backups

Trakka can back its whole database up, encrypted, to any WebDAV folder — Nextcloud/ownCloud, Synology, a NAS, `rclone serve webdav`, Apache `mod_dav`… — on demand or on a schedule, and restore from those backups. Everything is configured from the admin console's **Sauvegardes** tab; the only deploy-time setting is `BACKUP_WEBDAV_ALLOW_PRIVATE` above. Endpoint reference: [docs/API.md](API.md#admin-backups).

### Setting it up

1. **Create a dedicated folder** on the WebDAV server, one per Trakka instance (old backups are pruned by file name, so two instances sharing a folder would delete each other's). For Nextcloud, also create an **app password** (Settings → Security → "Create new app password") rather than using your account password; the folder URL looks like `https://cloud.example.com/remote.php/dav/files/<user>/trakka-backups/`.
2. If the WebDAV server is on your LAN or tailnet, set `BACKUP_WEBDAV_ALLOW_PRIVATE=true` on the `trakka` service and restart it.
3. In the admin console → **Sauvegardes**: enter the folder URL, username and password, click **Tester la connexion WebDAV** (it checks the credentials, that the URL is a folder, and that a file can actually be written there), then pick a schedule — every 12 hours, every night at a given time, or weekly on a given day and time, all in `APP_TIMEZONE` — and how many backups to keep, and **Enregistrer**.
4. **Download the encryption key** (**Télécharger la clé**) and store it off the server — a password manager, an offline copy. The console keeps a warning up until you do. **Without this key, no backup can be restored**; there is no recovery mechanism, by design.
5. Click **Sauvegarder maintenant** once to confirm everything works end to end.

A backup is a consistent hot snapshot of the live database (SQLite `VACUUM INTO`, on its own read-only connection, so the app keeps serving and writing meanwhile), encrypted on the server with AES-256-GCM before it leaves, and streamed to `trakka-backup-<UTC timestamp>.tkb` in the folder; then everything beyond the retention count is deleted. Neither backup nor restore ever holds more than a 64 KiB chunk of the database in memory, whatever its size. A failed scheduled backup is retried twice (30 minutes apart); a slot missed while Trakka was down runs right after it starts again.

**Alerts.** If an automatic backup fails, or no backup has succeeded for too long (3 days, or 9 for a weekly schedule), or the key has never been downloaded, admins see a small amber dot on the header's settings button and a ⚠️ on the admin console button, with the explanation at the top of the Sauvegardes tab. It never blocks anything; every attempt is also logged (visible in the console's Logs tab).

### What lives where

| File (in the `/data` volume) | What it is |
|---|---|
| `backup.key` | The instance's backup encryption key (`0600`). Deliberately **not** inside the database, so it never travels inside the backups it protects. Losing the volume loses this copy too — hence step 4 above. |
| `trakka.db` → `system_settings` | The WebDAV URL, username, schedule, retention — and the WebDAV password, encrypted with a subkey of `backup.key` (never returned by the API). These are part of every backup, so a restore brings the whole backup setup back with it. |
| `backups/trakka-pre-restore-<ts>.db` | A plaintext safety copy of the database, taken right before each restore (next to the existing automatic pre-migration copies). Not pruned automatically: delete old ones yourself. |
| `backups/staging/` | Temporary files during a backup/restore (a snapshot is as large as the database — this is on the data volume rather than the small in-memory `/tmp`). Emptied after each operation and at startup. |

### Restoring (including after losing the server)

**From the admin console** — to roll back on a running instance, or on a **brand new instance** after a disaster:

1. On a new instance, deploy Trakka as usual with an empty volume and create the first account (the first account on an instance is automatically an admin — keep `REGISTRATION_OPEN` at its default `true` for this).
2. Admin console → **Sauvegardes** → **Import / Restauration**: either upload the `.tkb` file, or — if the WebDAV server is still there — save its connection settings first and pick the backup from **Sauvegardes disponibles sur le serveur WebDAV**. Provide the key (paste it, or upload the `.key` file) and confirm.
3. Trakka decrypts and verifies the backup, checks it, migrates it to the running version if it is older, keeps a safety copy of the current database, then swaps the content in place — no restart. Anything wrong (wrong key, modified or truncated file, not a Trakka backup, made by a *newer* Trakka) is refused before the live data is touched.
4. Accounts, sessions and settings are now the backup's: sign in again with the restored accounts. If the key you supplied differs from the instance's own, it becomes the instance key (the previous one is kept as `backup.key.pre-restore-<ts>`), so the restored backup settings keep working.

If you go through a reverse proxy, allow request bodies as large as your backups (nginx's `client_max_body_size` defaults to 1 MB) and a long enough timeout for the upload; Trakka itself accepts up to 1 GiB and waits up to 15 minutes for this one request.

**Offline, from the command line** — when the web UI isn't an option (no admin account can sign in, the IdP of an OIDC-only instance is gone, …): `trakka -decrypt-backup` turns a `.tkb` back into a plain SQLite file and installs the key next to it, with no server running. With Compose, before starting the new instance (the output must not exist yet — it refuses to overwrite a database):

```bash
mkdir restore && cp trakka-backup-20260929T010000Z.tkb trakka-backup-key-3f2a-91c0.key restore/
chmod -R a+rX restore
docker compose run --rm --no-deps -v "$PWD/restore:/restore:ro" trakka \
  -decrypt-backup /restore/trakka-backup-20260929T010000Z.tkb \
  -backup-key /restore/trakka-backup-key-3f2a-91c0.key \
  -decrypt-output /data/trakka.db
docker compose up -d
```

The container runs as UID `10001`, so both files must be readable by it — browsers often save downloads as owner-only, hence the `chmod` — and delete the `restore/` folder once done. `-backup-key` also accepts the key text itself instead of a file. The same works with `podman-compose run`, or `go run ./cmd/server -decrypt-backup …` outside a container. Trakka then starts on the restored database, migrating it if it came from an older release.

## Healthcheck

The image's `HEALTHCHECK` runs `trakka -healthcheck`, which performs an in-process HTTP GET against its own `/healthz` and exits `0`/`1` accordingly — no `curl` or `wget` is installed in the runtime image. `compose.yml` declares the same check under `services.trakka.healthcheck` so `docker compose ps` / `podman-compose ps` reflect container health.

## Optional CalDAV sync (Radicale)

A lightweight [Radicale](https://radicale.org/) service is defined in `compose.yml` but gated behind the `calendar` [Compose profile](https://docs.docker.com/compose/profiles/), so it is **not** started by `docker compose up` alone:

```bash
docker compose --profile calendar up -d
# or
podman-compose --profile calendar up -d
```

It listens on `5232` and persists its data/config in the `radicale_data` and `radicale_config` named volumes, on the same `trakka_net` bridge network as `trakka`. This is intended as an optional companion for calendar-based sync of task lists — Trakka's own API does not talk to Radicale directly; wiring that integration up (e.g. exporting to-do lists as `.ics`/CalDAV) is a separate, not-yet-implemented piece of work.

## Networking

Both services sit on a single explicit bridge network, `trakka_net`, defined in `compose.yml`. This keeps them addressable by service name (`trakka`, `radicale`) for any future inter-service calls, without exposing anything beyond the ports explicitly published (`8080` for Trakka, `5232` for Radicale).

## Persistence

| Volume | Mounted at | Contains |
|---|---|---|
| `trakka_data` | `/data` (in `trakka`) | `trakka.db` (+ WAL/SHM sidecar files), `backup.key` and `backups/` (see [Encrypted WebDAV backups](#what-lives-where)) |
| `radicale_data` | `/data` (in `radicale`) | CalDAV collections |
| `radicale_config` | `/config` (in `radicale`) | Radicale configuration |

All three are named Docker/Podman volumes (not bind mounts), which sidesteps host-side UID/permission mismatches that bind mounts commonly hit under rootless Podman.

## Security posture

- Non-root, fixed UID/GID (`10001:10001`) in both services, set both in the Dockerfile (`USER`) and again explicitly in `compose.yml` (`user:`) for defense in depth.
- Distroless runtime image for `trakka` (no shell, no package manager) — see [Image build](#image-build) above.
- `read_only: true` on the `trakka` service: the root filesystem is mounted read-only, with `/data` (the named volume) and `/tmp` (an in-memory `tmpfs`, capped at 16MB) as the only writable paths. Nothing in the app writes anywhere else — the SQLite file (plus its `-wal`/`-shm` siblings) lives entirely under `/data`.
- `cap_drop: [ALL]` on the `trakka` service: a plain HTTP server backed by SQLite needs no Linux capabilities, not even `NET_BIND_SERVICE` (it binds the unprivileged port `8080`).
- `security_opt: no-new-privileges:true` on both services in `compose.yml`.
- No host networking.
- Every HTTP response (API and static) carries `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, a strict `Content-Security-Policy` (`default-src 'self'`, no `unsafe-inline`), and `Referrer-Policy: no-referrer` — see [docs/API.md](API.md) and `internal/handlers/middleware.go`.
- All SQL is parameterized (no string-built queries); any user-supplied URL is validated to be an absolute `http://`/`https://` URL before it's ever stored or rendered.
- Off-site backups are encrypted on the server before upload (AES-256-GCM, authenticated per 64 KiB chunk, see [Encrypted WebDAV backups](#encrypted-webdav-backups)); the WebDAV credentials are stored encrypted and never returned by the API; the WebDAV client never follows redirects (which would resend the credentials) and goes through the same SSRF dial guard as the scraper and Web Push.

`radicale` (the optional CalDAV companion, gated behind the `calendar` profile) is a third-party image and is intentionally left out of the `read_only`/`cap_drop` hardening above — it wasn't built with a read-only root filesystem in mind, and hardening it is out of scope for Trakka itself.

## PWA / offline support

See [docs/PWA.md](PWA.md) for how `static/sw.js`, `static/js/db.js`, and `static/manifest.json` make Trakka installable and usable offline on iOS, iPadOS, and Android — including the HTTPS requirement above, and how offline-created data gets reconciled once the network returns.
