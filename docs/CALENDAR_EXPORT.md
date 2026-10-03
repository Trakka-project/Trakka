# Calendar export (iCalendar / WebCAL)

Every Trakka user can subscribe their calendar app to their own tasks. Trakka generates a personal, secret link to an iCalendar (`.ics`) feed, and any app that can subscribe to a calendar by URL shows the tasks there: Nextcloud, Google Calendar, Apple Calendar, Thunderbird, and Android through ICSx⁵. The feed is built into Trakka itself. There is no extra service to run, no second set of passwords, and nothing for an administrator to set up per user.

This replaces the optional Radicale CalDAV sidecar that `compose.yml` used to ship. That sidecar never synced with Trakka. If you ran it, see [Migrating from the Radicale sidecar](#migrating-from-the-radicale-sidecar).

1. [What ends up in your calendar](#what-ends-up-in-your-calendar)
2. [Getting your link](#getting-your-link)
3. [Subscribing](#subscribing) (Nextcloud, Google Calendar, Apple Calendar, Thunderbird, Android)
4. [Server requirements](#server-requirements) (administrators)
5. [Security](#security)
6. [Limitations](#limitations)
7. [Migrating from the Radicale sidecar](#migrating-from-the-radicale-sidecar)
8. [Troubleshooting](#troubleshooting)

## What ends up in your calendar

The feed contains every task with a due date that you can see in Trakka: on your houses' lists, and on lists and Spaces shared with you. Each task is one calendar event.

| In Trakka | In the calendar |
|---|---|
| Task with a due date, no time | All-day event on that date |
| Task with a due date and a time | 30-minute event starting at that time, in the instance's time zone (`APP_TIMEZONE`) |
| Recurring task | Repeating event, ending on the recurrence end date if there is one |
| Recurring task you checked off | Its next occurrence, still repeating |
| Reminder ("la veille à 20:00", "à l'heure de l'échéance", …) | An alarm at the same moment, on every occurrence |
| List name | Event category and description, plus a link back to the list when `BASE_URL` is set |
| Task without a due date, or a non-recurring task already done | Not in the feed |

The subscription is **read-only**. Check off, edit and delete tasks in Trakka, and the calendar follows at its next refresh. Changes made in the calendar app don't come back to Trakka.

Trakka suggests a one-hour refresh to calendar apps. Each app decides on its own schedule:

| App | Refresh |
|---|---|
| Nextcloud | Every hour (Nextcloud follows the interval the feed advertises), unless the admin set a fixed `calendarSubscriptionRefreshRate` |
| Google Calendar | Every few hours, up to about a day. Nothing can make it faster |
| Apple Calendar | Set per subscription (**Auto-refresh**). Pick "Every hour" |
| Thunderbird | Set per calendar (**Refresh calendar every…**) |

## Getting your link

1. In Trakka, open **Paramètres** (the gear in the header) and scroll to **Export de calendrier (CalDAV / WebCAL)**.
2. Tap **Générer mon lien**.
3. Trakka shows two forms of the same link. The `webcal://` link suits Apple Calendar, Nextcloud and Thunderbird. The `https://` link suits Google Calendar, which doesn't accept `webcal://` everywhere. Copy the one your app needs with **Copier l'URL**. On an iPhone, iPad or Mac, **S'abonner sur cet appareil** opens the calendar app directly.

**The link is only shown once.** Trakka keeps only a fingerprint (hash) of it, the same way it stores login sessions, so it can't show it again later. When you reopen Paramètres, the section says whether a link is active and when a calendar app last fetched it. Use that line to check that a subscription is working.

If you lose the link, or want to add another device later, tap **Régénérer le lien** (then tap again to confirm). This creates a new link and **the old one stops working immediately**, so update every app that used it. **Désactiver le lien** turns the feed off entirely.

## Subscribing

### Nextcloud

1. Open the **Calendar** app.
2. In the left sidebar, open the **`+` New calendar** menu and choose **New subscription from link (read-only)**. The exact wording varies slightly between Calendar app versions.
3. Paste the `webcal://` link and confirm.

The calendar appears under **Subscriptions**. You can rename it, change its color or hide it like any other calendar. To remove it, open its `⋯` menu and choose **Unsubscribe**.

Nextcloud fetches the feed **from the Nextcloud server**, not from your browser, so that server must be able to reach Trakka. If Trakka's address is private (a `192.168.x.x`, `10.x.x.x` or Tailscale `100.x.y.z` address, `127.0.0.1`, …), a Nextcloud admin has to allow it once:

```bash
sudo -E -u www-data php occ config:app:set dav webcalAllowLocalAccess --value yes
```

If Nextcloud runs in Docker, run the same `occ` command inside its container, for example `docker exec -u www-data nextcloud php occ …`.

Subscriptions show up in Nextcloud's **Calendar** app only. The **Tasks** app doesn't list subscribed calendars, which is one reason the feed uses events rather than to-dos (see [Limitations](#limitations)).

### Google Calendar

Google only lets you add a subscription from the website on a computer, not from the mobile app. The subscription then shows up on your phone too.

1. Open [calendar.google.com](https://calendar.google.com/).
2. In the left sidebar, click **`+`** next to **Other calendars**, then **From URL**.
3. Paste the `https://` link and click **Add calendar**.

Google fetches the feed **from Google's servers**, so Trakka must be reachable from the internet over HTTPS with a valid certificate. A LAN-only or VPN-only instance can't be subscribed to from Google Calendar. Google also ignores the alarms of subscribed calendars and refreshes them only every few hours.

### Apple Calendar

- **iPhone or iPad:** tap **S'abonner sur cet appareil** in Trakka. Or go to **Settings → Apps → Calendar → Calendar Accounts → Add Account → Other → Add Subscribed Calendar** and paste the `webcal://` link. On iOS 17 and earlier, start from **Settings → Calendar → Accounts**.
- **Mac:** in Calendar, choose **File → New Calendar Subscription…**, paste the `webcal://` link, then set **Auto-refresh** to "Every hour".

Apple Calendar drops the alarms of a subscription by default. To keep Trakka's reminders as calendar alerts, untick **Remove: Alerts** in the subscription's settings. If you already get Trakka's push notifications, you may prefer to leave them off and avoid double reminders.

Apple wants HTTPS with a trusted certificate. Over plain HTTP, the subscription may fail.

### Thunderbird

1. In the **Calendar** tab, click **`+` New Calendar…** (or **File → New → Calendar…**).
2. Choose **On the Network**, paste the `https://` link as the location, and continue.
3. Thunderbird detects an iCalendar subscription. Give it a name and finish.

Thunderbird shows the alarms. To avoid double reminders with Trakka's push notifications, untick **Show Reminders** in the calendar's properties.

### Android

The Google Calendar app on Android can't add a subscription by URL. Either add it once from the Google Calendar website (see above; it then appears on your phone), or install [ICSx⁵](https://icsx5.bitfire.at/) (free, open source), which subscribes to an `.ics` link and puts its events in Android's own calendar storage, where any calendar app can show them. In ICSx⁵, tap **`+`**, paste the `https://` link, and pick a sync interval.

## Server requirements

This section is for whoever runs the Trakka instance. The feed works with the default `compose.yml`, with no extra service or setting, but a few deployment details matter:

- **`BASE_URL`** ([deployment variables](DEPLOYMENT.md)): optional, but recommended. With it, every event links back to its list in Trakka, and every event's unique ID is tied to your public host name. Without it, events carry no link and their IDs use whatever host name the calendar app used to reach Trakka. That works, but if the same link were reached under two different host names, an app would see two sets of events.
- **Reachability.** Calendar apps fetch the feed themselves, in the background. Google Calendar fetches it from Google's servers and Nextcloud from your Nextcloud server, so they need to reach Trakka at the address in the link. The link uses the address you opened Trakka with when you generated it.
- **Reverse proxy.** The feed is `GET /api/v1/calendar/feed.ics?token=…`. Proxies forward query strings by default. Don't add a rule that strips them or caches `/api/v1/`. Trakka already sends `Cache-Control: no-store`.
- **Time zone.** Timed tasks are written in `APP_TIMEZONE`, the same zone reminders already use, with a `VTIMEZONE` that Trakka generates from the zone's current rules. A zone with unusual rules (more than two changes a year) falls back to UTC times, which stay correct but can drift by an hour across a daylight saving change for a recurring task.
- **Logs.** The request log records the path only (`/api/v1/calendar/feed.ics`), never the query string, so the token doesn't end up in Trakka's logs. Your reverse proxy's access log probably does record it. Treat that log as sensitive, or configure the proxy not to log query strings for this path.

Nothing here touches the database beyond one row per user who has a link (the `calendar_feed_tokens` table, [DATABASE.md](DATABASE.md#calendar_feed_tokens)). The endpoints are documented in [API.md](API.md#calendar-feed).

## Security

The link **is** the credential. A calendar app polling in the background can't go through a login page or an SSO redirect, so the feed is the one part of Trakka's API that isn't behind the session cookie. Anyone who has the link can read the feed. Here is what limits that:

- **Read-only, and only the feed.** The token opens `feed.ics` and nothing else: not the rest of the API, not the web app, not any write. It shows the same tasks you see, with their titles, dates, list names and reminders. It never includes prices, product links, labels or anything about other users' accounts.
- **Unguessable.** 32 random bytes from the operating system's secure generator, the same strength as a login session.
- **Not stored.** Only its SHA-256 hash is in the database. A leaked database file or backup doesn't contain working links.
- **Revocable.** Regenerating or disabling the link kills the old one at once. Deleting a user deletes their link with them.
- **Not leaked by the browser.** Every Trakka response carries `Referrer-Policy: no-referrer`. The service worker never stores the feed or queues the link's management actions for later.
- **Unknown links get `404`,** not `401`. A `401` would make Apple Calendar ask for a password that can never work.

Share the link the way you would share a password. If you posted it somewhere by mistake, regenerate it.

## Limitations

- **One-way.** Changes in the calendar app never reach Trakka.
- **Events, not to-dos.** The feed has no `VTODO` items. Google Calendar and Apple Calendar ignore to-dos in subscriptions, and Nextcloud's Tasks app doesn't show subscriptions, so events are the only form every app displays. As a result, checking off a task removes it from the calendar (or, for a recurring task, moves it to its next occurrence) instead of showing it as done.
- **Monthly tasks on the 29th–31st, and yearly tasks on February 29.** Trakka moves such a task to the last day of shorter months, while the iCalendar standard skips those months. To avoid showing wrong dates, the feed shows only the task's next occurrence instead of the whole series.
- **Overdue recurring tasks** show their past occurrences in the calendar. Trakka itself skips the missed occurrences once you check the task off.
- **One link per user.** It covers every list you can see. There is no per-list link.
- **The section's name says CalDAV,** but the feed is an iCalendar subscription (sometimes called WebCAL), not a CalDAV server. Don't add it as a "CalDAV account": use each app's "subscribe" or "from URL" option, as above.

## Migrating from the Radicale sidecar

Earlier versions of `compose.yml` defined an optional `radicale` service behind the `calendar` profile. It was a separate CalDAV server: Trakka never wrote to it or read from it. It has been removed in favor of the feed above.

If you never started it with `--profile calendar`, there is nothing to do.

If you did:

1. **Keep what you stored in it.** If you used Radicale for your own calendars, export them while the container still runs. In Radicale's web UI (`/.web/`), download each collection as `.ics`, then import it into the app you're moving to. Or keep a file-level copy of both volumes:

   ```bash
   docker run --rm -v trakka_radicale_data:/d:ro -v trakka_radicale_config:/c:ro -v "$PWD":/out \
     docker.io/library/alpine tar czf /out/radicale-$(date +%F).tgz -C / d c
   ```

   Compose prefixes volume names with the project name (the directory name by default, so `trakka_` here). Run `docker volume ls` (or `podman volume ls`) to see the exact names.

2. **Stop and remove the container.** After updating `compose.yml`:

   ```bash
   docker compose up -d --remove-orphans
   ```

   With Podman, remove it by hand: `podman stop radicale && podman rm radicale`.

3. **Remove the volumes** once you no longer need them. This deletes their data for good:

   ```bash
   docker volume rm trakka_radicale_data trakka_radicale_config
   ```

4. **Clean up around it.** Remove any reverse-proxy site, DNS name or firewall rule you created for port `5232`. Remove the Radicale account from each CalDAV client (Thunderbird, Apple, DAVx⁵) and the subscription from Nextcloud.

Then generate your Trakka link and subscribe as described above.

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| The app says the calendar can't be found, or the feed returns `404` | The link was regenerated or disabled, or was copied incompletely (it ends with a long `token=…`). Generate a new link and subscribe again. |
| Paramètres says "pas encore utilisé par une application" long after subscribing | The app has never reached Trakka. Open the `https://` link in a browser **on the machine that fetches it** (the Nextcloud server for Nextcloud; for Google, any machine outside your network): it should download a `trakka.ics` file. |
| Nextcloud: "An error occurred, unable to create the calendar", or a subscription that stays empty | Trakka is on a private address: see [Nextcloud](#nextcloud) for `webcalAllowLocalAccess`. Otherwise Nextcloud can't resolve or reach the host name from the server itself. Test with `curl` **from the Nextcloud host**. |
| Google Calendar: "Could not fetch the URL" | Trakka isn't reachable from the internet over HTTPS with a valid certificate, or the `webcal://` link was used. Use the `https://` link. |
| Google Calendar shows old data | Google refreshes subscriptions every few hours at best. There is no way to force it. |
| iPhone/iPad: "Cannot connect using SSL" or the subscription fails | Apple wants HTTPS with a trusted certificate. Put Trakka behind a reverse proxy with a real certificate. |
| No reminders in the calendar | Apple drops alerts from subscriptions by default (untick **Remove: Alerts**). Google never shows alerts for subscriptions. Tasks with their reminder turned off have none. |
| Reminders twice | You get both Trakka's push notification and the calendar's alert. Turn the alerts off in the calendar app, or push notifications off in Trakka. |
| A timed task is one hour off | `APP_TIMEZONE` isn't the zone you expect, or the calendar app shows another zone. Check the server's `APP_TIMEZONE` and the app's time zone setting. |
| The same task shows twice | The calendar is subscribed twice (for example once through each link form, or through two host names). Remove one subscription, and set `BASE_URL`. |
| A monthly task on the 31st doesn't repeat in the calendar | Expected: see [Limitations](#limitations). |
