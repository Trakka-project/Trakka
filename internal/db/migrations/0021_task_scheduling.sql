-- Migration 21: task scheduling — an optional due time, a recurring task's
-- "completed, waiting for its next occurrence" state, and an "at the exact
-- due time" reminder mode.
--
-- items.due_time (HH:MM, 24h, nullable) is an optional wall-clock time for
-- due_date; meaningless (and always cleared by internal/handlers) without a
-- due_date. It is what the new "at the exact due time" reminder mode fires
-- at, and part of the reminder dedup key (see due_reminder_sent_for below).
--
-- items.next_due_date (YYYY-MM-DD, nullable) replaces the old "un-check the
-- item and advance due_date on completion" behavior of recurring items:
-- checking one off now leaves it done (in the list's "Terminés" section)
-- with next_due_date set to its next occurrence, and
-- internal/handlers.RunNextOccurrenceScan brings it back (done = 0,
-- due_date = next_due_date, next_due_date = NULL) once that occurrence
-- starts. NULL on a done recurring item means its series has ended
-- (recurrence_end_date reached). Existing rows need no backfill: under the
-- old behavior a recurring item was only ever left done once its series had
-- ended, which is exactly what NULL means here.
--
-- items.notification_sent_at (ISO-8601 UTC, nullable) records when the due
-- reminder was last pushed. due_reminder_sent_for (migration 15) stays the
-- dedup key; it now holds due_date, or due_date || 'T' || due_time when the
-- item has a due time, so moving a task to another time re-arms it. Rows
-- already marked with a plain due_date keep matching, since existing items
-- have no due_time.
--
-- items.reminder_at_due_time / users.reminder_default_at_due_time select
-- the "at the exact due time" reminder mode. An item with that mode but no
-- due_time falls back to its reminder_offset_days/reminder_time, which the
-- write path still resolves as before.
ALTER TABLE items ADD COLUMN due_time TEXT;
ALTER TABLE items ADD COLUMN next_due_date TEXT;
ALTER TABLE items ADD COLUMN notification_sent_at TEXT;
ALTER TABLE items ADD COLUMN reminder_at_due_time INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN reminder_default_at_due_time INTEGER NOT NULL DEFAULT 0;

-- RunNextOccurrenceScan runs every minute and only ever looks at done items
-- waiting for a next occurrence.
CREATE INDEX idx_items_next_due_date ON items(next_due_date) WHERE next_due_date IS NOT NULL;
