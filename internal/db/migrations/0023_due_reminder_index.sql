-- Migration 23: a partial index for internal/db.ListItemsForDueReminderScan.
--
-- RunDueReminderScan runs every minute by default since migration 21's "at
-- the exact due time" reminder mode, and without this its query was a full
-- scan of items on every tick. The index holds only the rows that scan can
-- return (not done, reminder enabled, with a due date), so it costs next to
-- nothing to maintain, the same trade-off as idx_items_next_due_date.
-- Schema only: no row changes. IF NOT EXISTS keeps it idempotent, like
-- migration 22's rewrites.
CREATE INDEX IF NOT EXISTS idx_items_pending_reminder ON items(due_date)
WHERE done = 0 AND reminder_enabled = 1 AND due_date IS NOT NULL;
