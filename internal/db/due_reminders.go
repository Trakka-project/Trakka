package db

import (
	"context"
	"database/sql"
	"fmt"
)

// DueReminderCandidate is one not-done item with a due date and an active
// reminder that hasn't triggered a push yet for its *current* due date —
// see internal/handlers.RunDueReminderScan. Deliberately not models.Item:
// due_reminder_sent_for is pure internal bookkeeping, never part of the
// API-facing Item shape, so this scan has no reason to pay for
// scanning/allocating every other Item field it doesn't need. Unlike the
// recurring-only reminder this superseded, this applies to any item with a
// due date, not only a recurring one.
type DueReminderCandidate struct {
	ItemID int64
	ListID int64
	Title  string
	// DueDate is the calendar date (YYYY-MM-DD) the reminder is computed
	// from.
	DueDate string
	// OffsetDays/TimeOfDay are the item's own, already-resolved reminder
	// timing (see models.Item.ReminderOffsetDays/ReminderTime) — always
	// concrete by the time a row reaches this scan, since
	// internal/handlers resolves "use the default" to the acting user's
	// current reminder default at write time rather than leaving it to be
	// re-derived here (see that resolution logic's own doc comment for why:
	// a shared list's item can be visible to multiple users with different
	// personal defaults, so there is no single well-defined "resolve live"
	// moment).
	OffsetDays int
	TimeOfDay  string
	// DueTime is the item's optional due time (HH:MM), "" when it has none.
	// With AtDueTime set, the reminder fires at DueDate + DueTime instead of
	// using OffsetDays/TimeOfDay (see models.Item.ReminderAtDueTime).
	DueTime   string
	AtDueTime bool
}

// Key is the due_reminder_sent_for value this candidate's reminder is
// recorded under once sent — the Go counterpart of dueReminderKeyExpr.
func (c *DueReminderCandidate) Key() string {
	if c.DueTime == "" {
		return c.DueDate
	}
	return c.DueDate + "T" + c.DueTime
}

// ListItemsForDueReminderScan returns every not-done item with a due date,
// an enabled reminder, and a due date/time that hasn't already had a
// reminder sent for it (see MarkDueReminderSent). Excluding an
// already-reminded-for due date here, rather than in Go after fetching
// everything, keeps the periodic scan cheap regardless of how many items an
// instance accumulates, and re-arms itself automatically the moment
// due_date or due_time changes for any reason (a recurring item starting
// its next occurrence, or a manual edit) — due_reminder_sent_for simply
// stops matching dueReminderKeyExpr, with no explicit "clear the flag" step
// needed anywhere else in the codebase. Served by the partial index from
// migration 23 (idx_items_pending_reminder), since the scan runs every
// minute by default. A row whose reminder_offset_days/reminder_time are
// unexpectedly NULL (which the write path should never produce while
// reminder_enabled is true) is skipped rather than returned with a
// meaningless zero value — see the caller's own defensive check.
func (d *DB) ListItemsForDueReminderScan(ctx context.Context) ([]*DueReminderCandidate, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT items.id, items.list_id, items.title, items.due_date, items.reminder_offset_days, items.reminder_time,
		       COALESCE(items.due_time, ''), items.reminder_at_due_time
		FROM items
		WHERE items.done = 0 AND items.due_date IS NOT NULL AND items.reminder_enabled = 1
		  AND (items.due_reminder_sent_for IS NULL OR items.due_reminder_sent_for != `+dueReminderKeyExpr+`)`)
	if err != nil {
		return nil, fmt.Errorf("querying items for due reminder scan: %w", err)
	}
	defer rows.Close()

	candidates := []*DueReminderCandidate{}
	for rows.Next() {
		c := &DueReminderCandidate{}
		var offsetDays sql.NullInt64
		var timeOfDay sql.NullString
		var atDueTime int
		if err := rows.Scan(&c.ItemID, &c.ListID, &c.Title, &c.DueDate, &offsetDays, &timeOfDay, &c.DueTime, &atDueTime); err != nil {
			return nil, fmt.Errorf("scanning due reminder candidate row: %w", err)
		}
		if !offsetDays.Valid || !timeOfDay.Valid || timeOfDay.String == "" {
			// Shouldn't happen through the ordinary write path (see the
			// type doc comment above) — skip defensively rather than
			// notifying with a meaningless offset/time.
			continue
		}
		c.OffsetDays = int(offsetDays.Int64)
		c.TimeOfDay = timeOfDay.String
		c.AtDueTime = atDueTime != 0
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating due reminder candidate rows: %w", err)
	}
	return candidates, nil
}

// MarkDueReminderSent records that a reminder has been sent for itemID's
// due date/time key exactly as given (DueReminderCandidate.Key) —
// internal/handlers.RunDueReminderScan calls this right after a successful
// send so the same due date isn't re-notified on the next scan tick, and
// stamps notification_sent_at with the current time. Storing the key itself
// (rather than a plain boolean flag) is what makes the "re-arm on due date
// change" behavior described on ListItemsForDueReminderScan above
// automatic: once the item's due_date/due_time changes for any reason, this
// stored value simply stops matching it. A no-op (not an error) if the item
// no longer exists — the scan already has its own snapshot of the row and
// there is nothing left to update.
func (d *DB) MarkDueReminderSent(ctx context.Context, itemID int64, key string) error {
	if _, err := d.conn.ExecContext(ctx,
		`UPDATE items SET due_reminder_sent_for = ?, notification_sent_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		key, itemID); err != nil {
		return fmt.Errorf("marking due reminder sent for item %d: %w", itemID, err)
	}
	return nil
}
