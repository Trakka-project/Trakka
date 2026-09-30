package db

import (
	"context"
	"database/sql"
	"fmt"
)

// NextOccurrenceCandidate is a done recurring item waiting for its next
// occurrence (next_due_date set) — see internal/handlers.RunNextOccurrenceScan,
// which decides from these fields when that occurrence starts. Like
// DueReminderCandidate it is deliberately not models.Item: the scan runs
// every minute and needs only these few columns.
type NextOccurrenceCandidate struct {
	ItemID      int64
	NextDueDate string
	// DueTime is the item's due time, "" when it has none. The next
	// occurrence keeps the same time of day.
	DueTime         string
	ReminderEnabled bool
	// OffsetDays/TimeOfDay are nil when the reminder was never resolved
	// (reminder disabled).
	OffsetDays *int
	TimeOfDay  *string
	AtDueTime  bool
}

// ListItemsAwaitingNextOccurrence returns every done item with a
// next_due_date (see models.Item.NextDueDate). Served by the partial index
// from migration 21, so it stays cheap however many items an instance has.
func (d *DB) ListItemsAwaitingNextOccurrence(ctx context.Context) ([]*NextOccurrenceCandidate, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT id, next_due_date, COALESCE(due_time, ''), reminder_enabled, reminder_offset_days, reminder_time, reminder_at_due_time
		FROM items
		WHERE done = 1 AND next_due_date IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("querying items awaiting their next occurrence: %w", err)
	}
	defer rows.Close()

	candidates := []*NextOccurrenceCandidate{}
	for rows.Next() {
		c := &NextOccurrenceCandidate{}
		var reminderEnabled, atDueTime int
		var offsetDays sql.NullInt64
		var timeOfDay sql.NullString
		if err := rows.Scan(&c.ItemID, &c.NextDueDate, &c.DueTime, &reminderEnabled, &offsetDays, &timeOfDay, &atDueTime); err != nil {
			return nil, fmt.Errorf("scanning next occurrence candidate row: %w", err)
		}
		c.ReminderEnabled = reminderEnabled != 0
		c.AtDueTime = atDueTime != 0
		c.OffsetDays = nullIntPtr(offsetDays)
		c.TimeOfDay = nullStringPtr(timeOfDay)
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating next occurrence candidate rows: %w", err)
	}
	return candidates, nil
}

// ActivateNextOccurrence brings a done recurring item back for its next
// occurrence: done = 0, due_date = next_due_date, next_due_date = NULL.
// Guarded like a compare-and-swap on the snapshot the scan read (still
// done, next_due_date still nextDueDate), so a user who un-checked or
// edited the item in the meantime is never overridden. Reports whether the
// row was actually updated. The due date change re-arms the reminder by
// itself (see ListItemsForDueReminderScan).
func (d *DB) ActivateNextOccurrence(ctx context.Context, itemID int64, nextDueDate string) (bool, error) {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE items SET done = 0, due_date = next_due_date, next_due_date = NULL,
		 updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND done = 1 AND next_due_date = ?`,
		itemID, nextDueDate)
	if err != nil {
		return false, fmt.Errorf("activating next occurrence of item %d: %w", itemID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reading rows affected for item %d: %w", itemID, err)
	}
	return n > 0, nil
}
