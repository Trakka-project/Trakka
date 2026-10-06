'use strict';

// Task reminders inside Trakka's Android app (android/, docs/MOBILE_BUILD.md), as local
// notifications scheduled on the phone. The app's WebView has no Web Push, so instead of the
// server pushing a reminder when it is due, the app asks the server ahead of time which reminders
// are coming (GET /api/v1/reminders/upcoming: when each one fires, computed server-side like the
// push, and its text) and hands them to Android through Capacitor's LocalNotifications plugin.
// Android then shows each one at its minute, network or not, even after a restart.
//
// Everything here is behind localNotificationsPlugin(): in a browser, or in a version of the app
// without the plugin, nothing runs, and Web Push (push.js) stays the only reminder mechanism.
//
// Keeping the schedule right:
//   - syncLocalReminders replaces the whole schedule with the server's answer. It runs at
//     startup, after any task or list change (debounced), when the offline queue has synced,
//     and when the app comes back to the foreground. Offline, it does nothing: what is scheduled
//     stays.
//   - noteLocalReminderWrite (called by app.js's apiRequest for every write, queued offline or
//     not) cancels a task's reminder right away when it is checked off or deleted, and a list's
//     when the list is deleted — without waiting for the server, which offline it can't reach.
//     Such a reminder stays "suppressed" until the server stops returning it, so that a sync
//     racing the offline queue can't bring it back; unchecking the task restores it, offline too.
//     Suppression is per occurrence (the reminder's exact time): checking off a recurring task
//     cancels this occurrence's reminder, not the next one's, which the server also returns.
//   - A recurring task checked off offline only gets its next occurrence's reminder at the next
//     sync: when it is due is the server's to compute.
//   - The daily overdue-tasks summary (Paramètres' "Rappel des tâches en retard / non
//     effectuées": every task past its due date still open) comes with the same answer, as its next one or none, and is scheduled under its own
//     notification id. It lists tasks by id, so checking them all off here cancels it at once,
//     offline too, and unchecking one brings it back. Its text is the server's, so checking off
//     only some of them leaves it as it is until the next sync.
//   - The account's notification preferences (PATCH /me) decide what the server returns: turning
//     task reminders or the summary off also cancels them here at once, without waiting for it.
//
// Shares `state`, `t`, `API_BASE` and handleNotificationClickMessage with app.js, the same
// classic-<script>-tags shared-scope pattern as every other frontend file.

const LOCAL_REMINDERS_ENABLED_KEY = 'trakka:localReminders';
const LOCAL_REMINDERS_SUPPRESSED_KEY = 'trakka:localRemindersSuppressed';
// What was last scheduled, as the server described it: the plugin's own getPending doesn't give
// a notification back in a form that can be scheduled again.
const LOCAL_REMINDERS_SCHEDULED_KEY = 'trakka:localRemindersScheduled';
// { summary, done }: the overdue-tasks summary last scheduled, as GET /reminders/upcoming returned
// it, and which of its tasks were checked off here since.
const LOCAL_OVERDUE_SUMMARY_KEY = 'trakka:localOverdueSummaryScheduled';
// The summary's notification id: a task's reminder uses the task's id, and a household's tasks
// never get anywhere near this one.
const OVERDUE_SUMMARY_NOTIFICATION_ID = 2000000000;
// The channels android/native/.../ReminderChannels.java creates.
const REMINDER_CHANNEL_VIBRATING = 'trakka_reminders';
const REMINDER_CHANNEL_QUIET = 'trakka_reminders_quiet';
// A suppression the server never confirms (a queued write that failed for good) expires.
const LOCAL_REMINDER_SUPPRESSION_TTL_MS = 7 * 24 * 60 * 60 * 1000;
const LOCAL_REMINDER_WRITE_SYNC_DELAY_MS = 1500;
const LOCAL_REMINDER_RESUME_SYNC_INTERVAL_MS = 60 * 1000;

function localNotificationsPlugin() {
  const cap = window.Capacitor;
  const plugin = cap && cap.Plugins && cap.Plugins.LocalNotifications;
  return plugin && typeof plugin.schedule === 'function' ? plugin : null;
}

// The user's choice on this phone (Paramètres), off until they turn it on: turning it on is
// what asks Android for the notification permission.
function isLocalRemindersEnabled() {
  try {
    return localStorage.getItem(LOCAL_REMINDERS_ENABLED_KEY) === '1';
  } catch {
    return false;
  }
}

function setLocalRemindersEnabled(enabled) {
  try {
    if (enabled) {
      localStorage.setItem(LOCAL_REMINDERS_ENABLED_KEY, '1');
    } else {
      localStorage.removeItem(LOCAL_REMINDERS_ENABLED_KEY);
    }
  } catch {
    // Storage denied: the switch won't survive a restart, nothing else to do.
  }
}

function readReminderMap(key) {
  try {
    const parsed = JSON.parse(localStorage.getItem(key) || '{}');
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

function writeReminderMap(key, map) {
  try {
    if (Object.keys(map).length === 0) {
      localStorage.removeItem(key);
    } else {
      localStorage.setItem(key, JSON.stringify(map));
    }
  } catch {
    // Storage denied: at worst, the next sync brings a cancelled reminder back until the server
    // catches up (as soon as the offline queue syncs), or an undo can't restore one offline.
  }
}

// { [itemId]: { since, reminder } }: reminders cancelled here that the server may not know about
// yet, as GET /reminders/upcoming returned them, to restore if the change is undone.
const readSuppressedReminders = () => readReminderMap(LOCAL_REMINDERS_SUPPRESSED_KEY);
const writeSuppressedReminders = (map) => writeReminderMap(LOCAL_REMINDERS_SUPPRESSED_KEY, map);
// { [itemId]: reminder }, as GET /reminders/upcoming returned it.
const readScheduledReminders = () => readReminderMap(LOCAL_REMINDERS_SCHEDULED_KEY);
const writeScheduledReminders = (map) => writeReminderMap(LOCAL_REMINDERS_SCHEDULED_KEY, map);

function readOverdueSummary() {
  try {
    const entry = JSON.parse(localStorage.getItem(LOCAL_OVERDUE_SUMMARY_KEY) || 'null');
    return entry && entry.summary && Array.isArray(entry.summary.item_ids) && Array.isArray(entry.done) ? entry : null;
  } catch {
    return null;
  }
}

function writeOverdueSummary(entry) {
  try {
    if (entry) {
      localStorage.setItem(LOCAL_OVERDUE_SUMMARY_KEY, JSON.stringify(entry));
    } else {
      localStorage.removeItem(LOCAL_OVERDUE_SUMMARY_KEY);
    }
  } catch {
    // Storage denied: checking the summary's tasks off offline won't cancel it.
  }
}

// Whether the summary still lists a task not checked off here.
function overdueSummaryHasOpenTasks(entry) {
  return entry.summary.item_ids.some((id) => !entry.done.includes(id));
}

function reminderChannelId() {
  return state.currentUser && state.currentUser.vibrate_on_notification === false
    ? REMINDER_CHANNEL_QUIET
    : REMINDER_CHANNEL_VIBRATING;
}

function toLocalNotification(reminder) {
  return {
    id: reminder.item_id,
    title: reminder.title,
    body: reminder.body,
    channelId: reminderChannelId(),
    // allowWhileIdle: fires on time even while the phone dozes.
    schedule: { at: new Date(reminder.remind_at), allowWhileIdle: true },
    autoCancel: true,
    extra: { url: reminder.url },
  };
}

function toSummaryNotification(summary) {
  return {
    id: OVERDUE_SUMMARY_NOTIFICATION_ID,
    title: summary.title,
    body: summary.body,
    channelId: reminderChannelId(),
    schedule: { at: new Date(summary.remind_at), allowWhileIdle: true },
    autoCancel: true,
    extra: { url: summary.url },
  };
}

async function cancelAllLocalReminders(plugin) {
  const { notifications } = await plugin.getPending();
  if (Array.isArray(notifications) && notifications.length > 0) {
    await plugin.cancel({ notifications: notifications.map((n) => ({ id: n.id })) });
  }
  writeScheduledReminders({});
  writeOverdueSummary(null);
}

// The server's reminders still to come and next overdue-tasks summary (or null), as
// { reminders, overdueSummary }, or null when they can't be had right now (offline, or an answer
// sw.js made up from its offline mirror, which has none for this).
async function fetchUpcomingReminders() {
  if (!navigator.onLine) return null;
  try {
    const response = await fetch(`${API_BASE}/reminders/upcoming`, { credentials: 'same-origin', cache: 'no-store' });
    if (!response.ok || response.headers.get('X-Trakka-Offline') === 'true') return null;
    const body = await response.json();
    if (!Array.isArray(body.reminders)) return null;
    const summary = body.overdue_summary;
    const overdueSummary = summary && Array.isArray(summary.item_ids) && summary.item_ids.length > 0 ? summary : null;
    return { reminders: body.reminders, overdueSummary };
  } catch {
    return null;
  }
}

let localReminderSyncRunning = false;
let localReminderSyncAgain = false;
let lastLocalReminderSyncAt = 0;

async function syncLocalReminders() {
  const plugin = localNotificationsPlugin();
  if (!plugin || !isLocalRemindersEnabled()) return;
  if (localReminderSyncRunning) {
    localReminderSyncAgain = true;
    return;
  }
  localReminderSyncRunning = true;
  try {
    do {
      localReminderSyncAgain = false;
      lastLocalReminderSyncAt = Date.now();
      const permission = await plugin.checkPermissions();
      if (permission.display !== 'granted') return;
      const upcoming = await fetchUpcomingReminders();
      if (!upcoming) return;
      const { reminders } = upcoming;

      // A suppression ends once the server no longer returns that occurrence.
      const isSuppressed = (r, suppressed) => {
        const entry = suppressed[String(r.item_id)];
        return !!entry && entry.reminder.remind_at === r.remind_at;
      };
      const suppressed = readSuppressedReminders();
      for (const [itemId, entry] of Object.entries(suppressed)) {
        const stillReturned = reminders.some((r) => String(r.item_id) === itemId && isSuppressed(r, suppressed));
        if (!stillReturned || Date.now() - entry.since > LOCAL_REMINDER_SUPPRESSION_TTL_MS) {
          delete suppressed[itemId];
        }
      }
      writeSuppressedReminders(suppressed);

      const now = Date.now();
      const wanted = reminders.filter((r) => !isSuppressed(r, suppressed) && Date.parse(r.remind_at) > now);
      const summary = upcoming.overdueSummary && Date.parse(upcoming.overdueSummary.remind_at) > now
        ? upcoming.overdueSummary
        : null;
      // Tasks checked off here stay checked for the same summary, as long as the server still
      // lists them: the same race with the offline queue as the suppressions above.
      const previousSummary = readOverdueSummary();
      const summaryEntry = summary && {
        summary,
        done: previousSummary && previousSummary.summary.remind_at === summary.remind_at
          ? previousSummary.done.filter((id) => summary.item_ids.includes(id))
          : [],
      };
      const notifications = wanted.map(toLocalNotification);
      if (summaryEntry && overdueSummaryHasOpenTasks(summaryEntry)) notifications.push(toSummaryNotification(summary));

      await cancelAllLocalReminders(plugin);
      if (notifications.length > 0) await plugin.schedule({ notifications });
      writeScheduledReminders(Object.fromEntries(wanted.map((r) => [String(r.item_id), r])));
      writeOverdueSummary(summaryEntry);
    } while (localReminderSyncAgain);
  } catch (err) {
    console.warn('Rappels locaux non synchronisés :', err);
  } finally {
    localReminderSyncRunning = false;
  }
}

let localReminderSyncTimer = null;

function scheduleLocalReminderSync(delay = LOCAL_REMINDER_WRITE_SYNC_DELAY_MS) {
  if (!localNotificationsPlugin() || !isLocalRemindersEnabled()) return;
  clearTimeout(localReminderSyncTimer);
  localReminderSyncTimer = setTimeout(syncLocalReminders, delay);
}

// Cancels the reminders of these tasks now, and keeps them from coming back until the server
// confirms (see the top of this file).
async function suppressLocalReminders(plugin, itemIds) {
  const scheduled = readScheduledReminders();
  const ids = itemIds.map(String).filter((id) => scheduled[id]);
  if (ids.length === 0) return;
  await plugin.cancel({ notifications: ids.map((id) => ({ id: Number(id) })) });
  const suppressed = readSuppressedReminders();
  for (const id of ids) {
    suppressed[id] = { since: Date.now(), reminder: scheduled[id] };
    delete scheduled[id];
  }
  writeScheduledReminders(scheduled);
  writeSuppressedReminders(suppressed);
}

async function restoreItemReminder(plugin, itemId) {
  const id = String(itemId);
  const suppressed = readSuppressedReminders();
  const entry = suppressed[id];
  if (!entry) return;
  delete suppressed[id];
  writeSuppressedReminders(suppressed);
  const reminder = entry.reminder;
  if (Date.parse(reminder.remind_at) > Date.now()) {
    await plugin.schedule({ notifications: [toLocalNotification(reminder)] });
    writeScheduledReminders({ ...readScheduledReminders(), [id]: reminder });
  }
}

// Records tasks checked off (done) or unchecked here against the scheduled overdue-tasks summary:
// it is cancelled once none of its tasks is left, and comes back when one is unchecked.
async function markOverdueSummaryTasks(plugin, itemIds, done) {
  const entry = readOverdueSummary();
  if (!entry) return;
  const ids = itemIds.filter((id) => entry.summary.item_ids.includes(id));
  if (ids.length === 0) return;
  const hadOpenTasks = overdueSummaryHasOpenTasks(entry);
  entry.done = done
    ? [...new Set([...entry.done, ...ids])]
    : entry.done.filter((id) => !ids.includes(id));
  writeOverdueSummary(entry);
  const hasOpenTasks = overdueSummaryHasOpenTasks(entry);
  if (hadOpenTasks && !hasOpenTasks) {
    await plugin.cancel({ notifications: [{ id: OVERDUE_SUMMARY_NOTIFICATION_ID }] });
  } else if (!hadOpenTasks && hasOpenTasks && Date.parse(entry.summary.remind_at) > Date.now()) {
    await plugin.schedule({ notifications: [toSummaryNotification(entry.summary)] });
  }
}

// Turning a notification type off in Paramètres cancels what it had scheduled here right away; the
// sync that follows (once the PATCH has reached the server) agrees with it.
async function applyNotificationPreferences(plugin, prefs) {
  if (prefs.reminders_enabled === false) {
    const ids = Object.keys(readScheduledReminders());
    if (ids.length > 0) await plugin.cancel({ notifications: ids.map((id) => ({ id: Number(id) })) });
    writeScheduledReminders({});
    writeSuppressedReminders({});
  }
  if (prefs.overdue_tasks_summary_enabled === false && readOverdueSummary()) {
    await plugin.cancel({ notifications: [{ id: OVERDUE_SUMMARY_NOTIFICATION_ID }] });
    writeOverdueSummary(null);
  }
}

// Called by apiRequest (app.js) after every write it sends, queued offline or not.
function noteLocalReminderWrite(method, path, rawBody) {
  const plugin = localNotificationsPlugin();
  if (!plugin || !isLocalRemindersEnabled()) return;
  const itemMatch = /^\/items\/(\d+)$/.exec(path);
  const listMatch = /^\/lists\/(\d+)$/.exec(path);
  // Creating a task (POST /items) or changing a list only need the sync below; so does Paramètres
  // (PATCH /me: the vibration setting picks the channel), apart from turning a notification type
  // off.
  if (!itemMatch && !listMatch && path !== '/items' && path !== '/me') return;

  let body = null;
  try {
    body = typeof rawBody === 'string' ? JSON.parse(rawBody) : null;
  } catch {
    body = null;
  }

  let local = Promise.resolve();
  if (itemMatch) {
    const itemId = Number(itemMatch[1]);
    if (method === 'DELETE' || (body && body.done === true)) {
      local = suppressLocalReminders(plugin, [itemId]).then(() => markOverdueSummaryTasks(plugin, [itemId], true));
    } else if (body && body.done === false) {
      local = restoreItemReminder(plugin, itemId).then(() => markOverdueSummaryTasks(plugin, [itemId], false));
    }
  } else if (path === '/me' && body) {
    local = applyNotificationPreferences(plugin, body);
  } else if (listMatch && method === 'DELETE') {
    const listId = Number(listMatch[1]);
    const ids = Object.values(readScheduledReminders())
      .filter((r) => r.list_id === listId)
      .map((r) => r.item_id);
    local = suppressLocalReminders(plugin, ids);
  }
  local
    .catch((err) => console.warn('Rappel local non mis à jour :', err))
    .finally(() => scheduleLocalReminderSync());
}

// Turned on from Paramètres: asks for the notification permission (Android 13+ shows its
// prompt) and schedules. Resolves to false, leaving it off, when the permission is refused.
async function enableLocalReminders() {
  const plugin = localNotificationsPlugin();
  if (!plugin) return false;
  let permission = await plugin.checkPermissions();
  if (permission.display !== 'granted') {
    permission = await plugin.requestPermissions();
  }
  if (permission.display !== 'granted') return false;
  setLocalRemindersEnabled(true);
  await syncLocalReminders();
  return true;
}

async function disableLocalReminders() {
  setLocalRemindersEnabled(false);
  writeSuppressedReminders({});
  const plugin = localNotificationsPlugin();
  if (plugin) await cancelAllLocalReminders(plugin);
}

// 'granted', 'denied' or 'prompt', for Paramètres.
async function localRemindersPermission() {
  const plugin = localNotificationsPlugin();
  if (!plugin) return 'denied';
  try {
    return (await plugin.checkPermissions()).display;
  } catch {
    return 'denied';
  }
}

let localRemindersStarted = false;

// Called once by app.js's init, after the dashboard and any deep link: from then on, tapping a
// reminder opens its list, and the schedule follows the server.
function startLocalReminders() {
  const plugin = localNotificationsPlugin();
  if (!plugin || localRemindersStarted) return;
  localRemindersStarted = true;

  // Also delivers the tap that launched the app, which the plugin keeps until a listener exists.
  plugin.addListener('localNotificationActionPerformed', (event) => {
    const extra = event && event.notification && event.notification.extra;
    if (extra && typeof extra.url === 'string') handleNotificationClickMessage(extra.url);
  });
  window.addEventListener('trakka:sync-complete', () => scheduleLocalReminderSync(0));
  window.addEventListener('online', () => scheduleLocalReminderSync(3000));
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && Date.now() - lastLocalReminderSyncAt > LOCAL_REMINDER_RESUME_SYNC_INTERVAL_MS) {
      scheduleLocalReminderSync(0);
    }
  });
  syncLocalReminders();
}
