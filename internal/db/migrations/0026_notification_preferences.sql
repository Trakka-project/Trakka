-- Migration 26: per-user notification preferences — which kinds of
-- notification an account receives, set from the "Paramètres" modal and
-- PATCH /api/v1/me (see models.User.RemindersEnabled/
-- OverdueTasksSummaryEnabled/OverdueTasksSummaryTime).
--
-- reminders_enabled gates the existing per-task due reminders for this
-- user: internal/handlers.checkItemForDueReminder leaves them out of the
-- push fan-out (db.ListReminderRecipients) and GET /api/v1/reminders/upcoming
-- returns none, so the Android app cancels the ones it scheduled. On by
-- default, so existing accounts keep getting what they got before.
--
-- overdue_tasks_summary_enabled/_time are the new daily summary: once a day,
-- at overdue_tasks_summary_time (HH:MM, in APP_TIMEZONE), one notification
-- listing the tasks due the day before that are still not checked off
-- (internal/handlers.RunOverdueSummaryScan). Off by default: it is a new,
-- daily notification nobody has asked for yet.
--
-- overdue_tasks_summary_sent_on is pure bookkeeping, never part of the API
-- shape: the local date (YYYY-MM-DD) of the last day the scan handled for
-- this user, so the summary goes out at most once a day.
ALTER TABLE users ADD COLUMN reminders_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN overdue_tasks_summary_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN overdue_tasks_summary_time TEXT NOT NULL DEFAULT '08:00';
ALTER TABLE users ADD COLUMN overdue_tasks_summary_sent_on TEXT;
