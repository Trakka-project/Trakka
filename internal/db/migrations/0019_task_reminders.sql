-- Migration 19: configurable, Google-Tasks-style due-date reminders for any
-- item (not just recurring ones), replacing the old "recurring items only"
-- lead-time reminder from migration 15.
--
-- users.reminder_default_offset_days / reminder_default_time are a
-- per-account default for "when should I be reminded before a task's due
-- date", expressed as a number of whole days before due_date plus a
-- wall-clock time of day (HH:MM, 24h) rather than a plain duration — this is
-- what lets it represent "the same day at 09:00" (0, '09:00', the default
-- below), "the evening before at 20:00" (1, '20:00'), or any custom
-- combination, with no separate "mode" column: the frontend derives which
-- named preset to show from these two values instead of storing a redundant
-- label. See internal/handlers.RunDueReminderScan.
ALTER TABLE users ADD COLUMN reminder_default_offset_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN reminder_default_time TEXT NOT NULL DEFAULT '09:00';

-- items.reminder_enabled/reminder_offset_days/reminder_time are the
-- per-item counterpart, superseding recurrence_lead_minutes (migration 15,
-- left in place but no longer read by any scan — it was never wired to any
-- frontend control). reminder_enabled defaults to on, matching this
-- feature's "any due-dated task reminds you unless you turn it off"
-- behavior; reminder_offset_days/reminder_time are nullable at the SQL
-- level but always resolved to concrete values by internal/handlers before
-- being written whenever reminder_enabled ends up true — see
-- internal/handlers.handleItemsCreate/Update/Patch's reminder-resolution
-- logic, which substitutes the acting user's own current
-- reminder_default_offset_days/_time the moment a request asks to use "the
-- default" rather than an explicit override. This resolve-once-at-write-time
-- design (rather than re-deriving live from a user's default on every scan)
-- is deliberate: a shared list's task is visible to multiple users who could
-- each have a different personal default, so there is no single well-defined
-- "notify every recipient at their own preferred time" moment — resolving
-- once keeps the existing single-moment, single-fan-out notification design
-- intact. due_reminder_sent_for (migration 15) is reused unchanged: it
-- already re-arms itself whenever due_date changes, which is exactly what a
-- generalized, non-recurring-only scan needs too.
ALTER TABLE items ADD COLUMN reminder_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE items ADD COLUMN reminder_offset_days INTEGER;
ALTER TABLE items ADD COLUMN reminder_time TEXT;
