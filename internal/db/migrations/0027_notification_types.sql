-- Migration 27: the rest of the per-type notification preferences started in
-- migration 26 — Paramètres' "Types de notifications" matrix, settable via
-- PATCH /api/v1/me (see models.User):
--
--   collaborator_actions_enabled  someone else checks or unchecks an item on a
--                                 list you can access (notifyListChange)
--   item_additions_enabled        someone else adds an item to such a list
--   list_sharing_enabled          a list or Space is shared with you, or you
--                                 are invited to a House
--
-- All on by default: the first two were sent to everyone before this
-- migration, and the third is a rare, directly relevant notice. One column
-- per type, like reminders_enabled and vibrate_on_notification, so the
-- recipient queries filter in SQL (db.ListNotificationRecipientsFor).
ALTER TABLE users ADD COLUMN collaborator_actions_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN item_additions_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN list_sharing_enabled INTEGER NOT NULL DEFAULT 1;

-- The overdue summary now lists every task past its due date, not only
-- yesterday's: forget the day already handled under the old rule, so today's
-- summary goes out with the new one.
UPDATE users SET overdue_tasks_summary_sent_on = NULL;
