# Radicale (CalDAV) integration

Trakka's [`compose.yml`](../compose.yml) ships an optional [Radicale](https://radicale.org/) sidecar: a small CalDAV server that runs next to Trakka. This guide explains what it does, how to secure it, how to connect calendar and task apps to it, and how to subscribe to it from Nextcloud.

> **Read this first: Trakka does not sync with Radicale yet.**
> Trakka's to-do lists live in Trakka's own SQLite database. Trakka never writes to Radicale and never reads from it, and Trakka has no CalDAV settings, credentials or `.ics` export of its own. A two-way bridge between Trakka tasks and CalDAV is planned but not built (see [`.claude/status.md`](../.claude/status.md)).
> Today, the sidecar is a separate CalDAV server that you get "for free" with the Trakka stack. You can use it for calendars and task lists you want in standard CalDAV apps. When the bridge exists, it will use this same server, so setting it up now is not wasted.

## Contents

1. [How Radicale fits next to Trakka](#how-radicale-fits-next-to-trakka)
2. [Start the sidecar](#start-the-sidecar)
3. [Secure it before first use](#secure-it-before-first-use)
4. [Create calendars and task lists](#create-calendars-and-task-lists)
5. [Finding your URLs and credentials](#finding-your-urls-and-credentials)
6. [Connecting CalDAV clients](#connecting-caldav-clients) (Thunderbird, Apple Calendar & Reminders, DAVx⁵)
7. [Subscribing from Nextcloud](#subscribing-from-nextcloud)
8. [Backups](#backups)
9. [Troubleshooting](#troubleshooting)

## How Radicale fits next to Trakka

```
                ┌─────────────── trakka_net (bridge network) ───────────────┐
 browser/PWA ──►│ trakka    :8080  REST API + PWA    vol: trakka_data       │
                │                                                           │
 CalDAV apps ──►│ radicale  :5232  CalDAV + .ics     vol: radicale_data     │
 (Thunderbird,  │ (profile: calendar)                     radicale_config   │
  Apple, DAVx⁵, └───────────────────────────────────────────────────────────┘
  Nextcloud)            no link between the two services today
```

| | Trakka | Radicale sidecar |
|---|---|---|
| Image | Built from this repo | `docker.io/tomsquest/docker-radicale` (third-party, Radicale 3.x) |
| Started by | `docker compose up -d` | Only with `--profile calendar` |
| Port | `8080` | `5232` |
| Accounts | Trakka users (local or OIDC) | Its own users, in an `htpasswd` file. **Not** shared with Trakka |
| Data | `trakka_data` volume | `radicale_data` (collections) and `radicale_config` (config, users, rights) |
| Protocol | JSON REST (`/api/v1/...`) | CalDAV / WebDAV, plus `.ics` export of each collection |

Both services share the `trakka_net` network, so a future bridge can reach Radicale at `http://radicale:5232` without exposing anything extra. For more on the Compose setup, see [Deployment → Optional CalDAV sync](DEPLOYMENT.md#optional-caldav-sync-radicale).

## Start the sidecar

```bash
docker compose --profile calendar up -d
# or
podman-compose --profile calendar up -d
```

> ⚠️ **Do not leave it like this.** The image's default config is `[auth] type = none`, and `compose.yml` publishes port `5232` on every interface. Until you finish the next section, anyone who can reach port `5232` can read, create and delete calendars on it. If you have already started it, stop it (`docker compose stop radicale`) and do the next section first.

All commands below use `docker compose`. With Podman, replace it with `podman-compose`, and replace `docker exec` with `podman exec`. The sidecar's container is named `radicale` in both.

## Secure it before first use

This section turns on password authentication, keeps passwords hashed with bcrypt, and stops Radicale from being exposed directly. Do these steps in order: Radicale **refuses to start** if `htpasswd` auth is enabled but the users file doesn't exist yet.

### 1. Create the first user

This runs the sidecar image once, uses its built-in Python and `bcrypt` to hash the password, and appends the result to `/config/users` in the `radicale_config` volume. The password is typed at a hidden prompt, so it never ends up in your shell history.

```bash
docker compose --profile calendar run --rm --entrypoint /venv/bin/python radicale -c '
import bcrypt, getpass
user = input("Username: ")
pw = getpass.getpass("Password: ").encode()
open("/config/users", "a").write(f"{user}:{bcrypt.hashpw(pw, bcrypt.gensalt()).decode()}\n")
'
```

Radicale usernames are separate from Trakka usernames. Using the same name in both is a convenience, not a link: the two passwords stay independent.

### 2. Turn on authentication

In `compose.yml`, add an `environment:` block to the `radicale` service. At startup, the image copies every `RADICALE_CONFIG_<SECTION>_<KEY>` variable into its config file:

```yaml
  radicale:
    image: docker.io/tomsquest/docker-radicale:latest
    # ...
    environment:
      RADICALE_CONFIG_AUTH_TYPE: htpasswd
      RADICALE_CONFIG_AUTH_HTPASSWD_FILENAME: /config/users
      RADICALE_CONFIG_AUTH_HTPASSWD_ENCRYPTION: bcrypt
```

### 3. Stop exposing it to the network

Change the `radicale` service's port mapping so that only the host itself can reach it:

```yaml
    ports:
      - "127.0.0.1:5232:5232"
```

Then put a TLS reverse proxy in front of it, ideally on its own subdomain (Radicale doesn't like being served from a sub-path unless the proxy sends `X-Script-Name`). With [Caddy](https://caddyserver.com/), for example:

```
cal.example.com {
    reverse_proxy 127.0.0.1:5232
}
```

HTTPS is not optional in practice: CalDAV sends your password with every request (HTTP Basic auth), and Apple devices want a trusted certificate anyway. On a trusted LAN, or over Tailscale/WireGuard, you can skip the proxy and keep `"5232:5232"`, but you then rely on the network alone to protect the password in transit.

### 4. Apply and check

```bash
docker compose --profile calendar up -d radicale

curl -s -o /dev/null -w '%{http_code}\n' -X PROPFIND http://127.0.0.1:5232/alice/                          # 401 = auth is on
curl -s -o /dev/null -w '%{http_code}\n' -X PROPFIND -H 'Depth: 0' -u alice http://127.0.0.1:5232/alice/  # 207 after typing the password
```

Changing the `environment:` block is enough for `up -d` to recreate the container. You don't need `--force-recreate`.

> Note: the variables are written into `/config/config` in the `radicale_config` volume, and they stay there. Deleting a variable from `compose.yml` later does **not** remove its line from the config file. Either edit `/config/config` directly or set the variable to the value you want.

### Managing users later

Users added or removed while Radicale is running take effect right away; no restart is needed.

```bash
# Add a user (same hashing snippet as above, run inside the running container)
docker exec -it radicale /venv/bin/python -c '
import bcrypt, getpass
user = input("Username: ")
pw = getpass.getpass("Password: ").encode()
open("/config/users", "a").write(f"{user}:{bcrypt.hashpw(pw, bcrypt.gensalt()).decode()}\n")
'

# List users
docker exec radicale cut -d: -f1 /config/users

# Remove a user (to reset a password: remove, then add again)
docker exec radicale sed -i '/^alice:/d' /config/users
```

## Create calendars and task lists

The easiest way is Radicale's built-in web UI at `https://cal.example.com/.web/` (or `http://127.0.0.1:5232/.web/`):

1. Log in with a Radicale user.
2. Choose **Create new addressbook or calendar**.
3. Give it a title and choose a **type**:
   - For a to-do list, pick a type that includes **tasks**. Clients then show it as a task or reminders list.
   - For dates, pick **calendar**.
   - A collection can hold both.
4. After you create it, the UI shows the collection's **URL**. You'll need it for Nextcloud.

Most clients (Thunderbird, Apple, DAVx⁵) can also create collections themselves once connected, so the web UI is optional.

## Finding your URLs and credentials

**None of this comes from Trakka.** Trakka has no CalDAV settings, no credentials screen and no API field for Radicale. Everything is on the sidecar side:

| What | Where it comes from | Example |
|---|---|---|
| Server (base) URL | Your reverse proxy hostname, or the host and port `5232` | `https://cal.example.com/` |
| Principal (home) URL | Base URL + username + `/` | `https://cal.example.com/alice/` |
| Collection URL | Shown by the web UI; also `<principal URL><collection id>/` | `https://cal.example.com/alice/3f1c…/` |
| `.ics` export of a collection | A plain `GET` on the collection URL. Returns `text/calendar` with every event/task in it | same as above |
| Username | `/config/users` in the sidecar (`docker exec radicale cut -d: -f1 /config/users`) | `alice` |
| Password | Whatever was typed when the user was created. It is stored as a bcrypt hash and **cannot be read back**. Reset it instead (see [Managing users later](#managing-users-later)) | — |

Clients that support auto-discovery need only the **base URL**. They use `/.well-known/caldav` and the principal to find every collection on their own. Clients that don't need the **collection URL**.

## Connecting CalDAV clients

In every client below, connect with a **Radicale** username and password, not your Trakka login.

### Thunderbird (desktop)

1. Open the **Calendar** tab, then **New Calendar…** (the `+` next to the calendar list).
2. Choose **On the Network**.
3. **Username:** `alice`. **Location:** `https://cal.example.com/` (the base URL). Click **Find Calendars**.
4. Enter the password when prompted, then tick the collections you want and click **Subscribe**.
5. Collections that support tasks appear in the **Tasks** tab as well as the calendar.

If discovery fails, put a single collection URL in **Location** instead.

### Apple Calendar & Reminders (macOS, iPhone, iPad)

Apple uses one CalDAV account for both apps: event collections show in **Calendar**, and task collections show as lists in **Reminders**.

**macOS:**

1. **Calendar → Settings → Accounts → `+`** (or **Calendar → Add Account…**). Choose **Other CalDAV Account…**.
2. **Account Type:** *Manual*. Fill in username, password and **Server Address:** `cal.example.com`.
3. Click **Sign In**, then enable Calendars and/or Reminders for the account.

**iOS / iPadOS:**

1. **Settings → Apps → Calendar → Calendar Accounts → Add Account → Other → Add CalDAV Account**. On iOS 17 and earlier, start from **Settings → Calendar → Accounts**.
2. **Server:** `cal.example.com`, then the username and password. Tap **Next**.
3. If it can't verify, open **Advanced Settings**:
   - **Use SSL:** on
   - **Port:** `443`
   - **Account URL:** `https://cal.example.com/alice/`

Apple devices expect HTTPS with a publicly trusted certificate. A self-signed certificate or plain HTTP on `:5232` usually fails or keeps prompting.

### DAVx⁵ (Android, including GrapheneOS)

DAVx⁵ syncs CalDAV into Android's calendar and task storage, so any calendar app can show the events. Tasks need a separate tasks app: [jtx Board](https://jtx.techbee.at/), [Tasks.org](https://tasks.org/) or OpenTasks. Install one **before** adding the account, or DAVx⁵ will offer to install one.

1. Install DAVx⁵ ([F-Droid](https://f-droid.org/packages/at.bitfire.davdroid/) or Google Play).
2. Tap **`+` → Login with URL and user name**.
3. **Base URL:** `https://cal.example.com/`. Then enter the username and password, and tap **Login**.
4. Name the account (any label; it shows up in calendar apps) and tap **Create account**.
5. In the **CalDAV** tab, tick the collections to sync, then pull down to sync.
6. Optionally, set the sync interval in the account settings. The default is fine for most uses.

For a server on your LAN that only speaks HTTP, `http://192.168.x.y:5232/` works too, but DAVx⁵ will warn that the password travels in clear text.

## Subscribing from Nextcloud

Nextcloud can't add an external CalDAV account the way Thunderbird or DAVx⁵ can. What it supports is a **read-only subscription** to an `.ics` URL. Radicale serves one for every collection: a plain `GET` on the collection URL. So the steps are: make a read-only Radicale account just for Nextcloud, then subscribe Nextcloud to the collection's URL.

**What to expect:**

- **Read-only.** Nextcloud shows the subscription's events, but you can't edit them there. Changes are made in Radicale (or in any client connected to it) and come back into Nextcloud on its next refresh.
- **Not live.** By default, Nextcloud refreshes subscriptions once a day. Step 3 shortens that.
- **Events, not tasks.** The Nextcloud **Tasks** app doesn't list subscribed calendars, so to-dos from a task-only collection won't appear there. Use subscriptions for event calendars. To get tasks into Nextcloud, see [Tasks in Nextcloud](#tasks-in-nextcloud) below.

### 1. Create a read-only account for Nextcloud (on the Radicale host)

Nextcloud stores the subscription URL, password included, in its database, and the Nextcloud user can see it in the calendar's settings. So don't reuse a real account here. Create one that can only **read** the collections you subscribe to.

Pick a password made only of **letters and digits**. Nextcloud reads the credentials straight out of the URL and doesn't percent-decode them, so `@ : / % #` and similar characters break the login.

```bash
openssl rand -hex 24          # a suitable password
```

Create the user `nextcloud` with that password, using the snippet from [Managing users later](#managing-users-later). Then write a rights file. The first matching section wins, and the last three sections reproduce Radicale's default "each user owns only their own collections" behaviour for everyone else:

```bash
docker exec -i radicale sh -c 'cat > /config/rights' <<'EOF'
# Read-only feed for Nextcloud: one section per subscribed collection.
[nextcloud-feed-family]
user: nextcloud
collection: alice/3f1c0d2e-family-calendar
permissions: r

# Everyone else: same as Radicale's default "owner_only".
[root]
user: .+
collection:
permissions: R

[principal]
user: .+
collection: {user}
permissions: RW

[calendars]
user: .+
collection: {user}/[^/]+
permissions: rw
EOF
```

`collection:` is the collection's path **without** the leading or trailing slash: the part of its URL after the host. To expose more collections, add more `[nextcloud-feed-…]` sections. The `user:` and `collection:` values are regular expressions.

Turn the rights file on by adding two lines to the `radicale` service's `environment:` block in `compose.yml`, then apply:

```yaml
      RADICALE_CONFIG_RIGHTS_TYPE: from_file
      RADICALE_CONFIG_RIGHTS_FILE: /config/rights
```

```bash
docker compose --profile calendar up -d radicale
```

Check: the `nextcloud` user must be able to read the feed but not change it:

```bash
F=https://cal.example.com/alice/3f1c0d2e-family-calendar/
curl -s -o /dev/null -w '%{http_code}\n' -u nextcloud "$F"                         # 200
curl -s -o /dev/null -w '%{http_code}\n' -u nextcloud -X DELETE "${F}anything.ics"  # 403
```

You can edit the rights file later without restarting Radicale.

### 2. If Nextcloud reaches Radicale over a private address (Nextcloud admin)

Nextcloud refuses to fetch subscriptions from private, loopback or LAN addresses unless an admin allows it. You need this if the URL resolves to `192.168.x.x`, `10.x.x.x`, a Tailscale `100.x.y.z`, `127.0.0.1`, and so on:

```bash
sudo -E -u www-data php occ config:app:set dav webcalAllowLocalAccess --value yes
```

You don't need it when Radicale is behind a public hostname like `https://cal.example.com/`. If Nextcloud runs in Docker, run the same `occ` command inside its container, for example `docker exec -u www-data nextcloud php occ …`.

### 3. Optional: refresh more often than once a day (Nextcloud admin)

Radicale doesn't advertise a refresh interval, so Nextcloud falls back to its own default of one day. To refresh every hour instead (the value is an ISO 8601 duration):

```bash
sudo -E -u www-data php occ config:app:set dav calendarSubscriptionRefreshRate --value "PT1H"
```

### 4. Add the subscription (any Nextcloud user)

1. Open the **Calendar** app.
2. In the left sidebar, open the **`+` New calendar** menu and choose **New subscription from link (read-only)**. The exact wording varies slightly between Calendar app versions.
3. Paste the collection URL with the `nextcloud` credentials in it:

   ```
   https://nextcloud:<password>@cal.example.com/alice/3f1c0d2e-family-calendar/
   ```

   Use `https://`. If you paste a `webcal://` link, Nextcloud fetches it over HTTPS. If Radicale is plain HTTP on your LAN, type the URL as `http://nextcloud:<password>@192.168.1.20:5232/alice/…/` explicitly.
4. Confirm. The calendar appears under **Subscriptions** with the name Radicale gave it. You can rename it, change its color, or hide it like any other calendar.

To remove it, open the subscription's `⋯` menu and choose **Unsubscribe**. To revoke Nextcloud's access on the Radicale side, delete its `[nextcloud-feed-…]` section from `/config/rights` or remove the `nextcloud` user.

### Tasks in Nextcloud

Because subscriptions don't feed the Tasks app, there are only partial options for getting Radicale to-dos into Nextcloud:

- **One-time copy.** Download the collection (`curl -u alice -o tasks.ics https://cal.example.com/alice/<id>/`), then import it with **Calendar → Settings → Import calendar** into a Nextcloud calendar that supports tasks. This makes a copy that stays in Nextcloud and doesn't sync back.
- **Use both from one device.** Add both servers to DAVx⁵ (Radicale and Nextcloud are each one account). Your tasks app then shows both sets of lists side by side, but nothing syncs between the two servers.

## Backups

Trakka's [encrypted WebDAV backups](DEPLOYMENT.md#encrypted-webdav-backups) cover **only Trakka's database** (`trakka_data`). Your Radicale calendars, users and rights live in the `radicale_data` and `radicale_config` volumes and are **not** included. Back them up separately, for example with a file-level copy of both volumes:

```bash
docker run --rm -v trakka_radicale_data:/d:ro -v trakka_radicale_config:/c:ro -v "$PWD":/out \
  docker.io/library/alpine tar czf /out/radicale-$(date +%F).tgz -C / d c
```

Compose prefixes volume names with the project name (the directory name by default, so `trakka_` here). Run `docker volume ls` (or `podman volume ls`) to see the exact names on your machine.

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| The `radicale` container keeps restarting; logs show `Failed to load htpasswd file '/config/users'` | Auth was enabled before any user existed. Create a user with the `compose run` command in [step 1](#1-create-the-first-user), then run `up -d` again. |
| Anyone can open `http://host:5232/.web/` and create calendars without a password | Auth isn't on. Check `docker exec radicale grep -A3 '^\[auth\]' /config/config`; it should show `type = htpasswd`. |
| `401` with a password you're sure is right | The password contains characters your client mangles (Nextcloud: see [step 1](#1-create-a-read-only-account-for-nextcloud-on-the-radicale-host)). Otherwise the user isn't in `/config/users`; check with `cut -d: -f1`. |
| A collection returns `403` for a user | The rights file doesn't give that user access. Remember that the first matching section wins. Check `/config/rights` and the `collection:` path, which has no leading or trailing slash. |
| Nextcloud: "An error occurred, unable to create the calendar" or a subscription that stays empty | Radicale is on a private address: see [step 2](#2-if-nextcloud-reaches-radicale-over-a-private-address-nextcloud-admin). Otherwise the credentials in the URL are wrong, or Nextcloud can't resolve or reach the hostname from the server itself. Test with `curl` **from the Nextcloud host**. |
| Nextcloud shows old data | Subscriptions refresh once a day by default. See [step 3](#3-optional-refresh-more-often-than-once-a-day-nextcloud-admin). |
| iPhone/iPad: "Cannot connect using SSL" | Apple wants HTTPS with a trusted certificate. Put Radicale behind a reverse proxy with a real certificate (see [step 3](#3-stop-exposing-it-to-the-network)). |
| DAVx⁵ syncs events but no tasks | No tasks app is installed, or the collection doesn't include tasks. Install jtx Board or Tasks.org, then refresh the collection list in DAVx⁵. |
| Trakka's to-do lists don't show up in any CalDAV client | Expected: Trakka doesn't sync with Radicale yet. See the note at the [top of this page](#radicale-caldav-integration). |
