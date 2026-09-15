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
}

// ListItemsForDueReminderScan returns every not-done item with a due date,
// an enabled reminder, and a due date that hasn't already had a reminder
// sent for it (see MarkDueReminderSent). Excluding an already-reminded-for
// due date here, rather than in Go after fetching everything, keeps the
// periodic scan cheap regardless of how many items an instance accumulates,
// and re-arms itself automatically the moment due_date changes for any
// reason (a recurring item advancing to its next occurrence on completion,
// or a manual edit) — due_reminder_sent_for simply stops matching due_date,
// with no explicit "clear the flag" step needed anywhere else in the
// codebase. A row whose reminder_offset_days/reminder_time are
// unexpectedly NULL (which the write path should never produce while
// reminder_enabled is true) is skipped rather than returned with a
// meaningless zero value — see the caller's own defensive check.
func (d *DB) ListItemsForDueReminderScan(ctx context.Context) ([]*DueReminderCandidate, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT id, list_id, title, due_date, reminder_offset_days, reminder_time
		FROM items
		WHERE done = 0 AND due_date IS NOT NULL AND reminder_enabled = 1
		  AND (due_reminder_sent_for IS NULL OR due_reminder_sent_for != due_date)`)
	if err != nil {
		return nil, fmt.Errorf("querying items for due reminder scan: %w", err)
	}
	defer rows.Close()

	candidates := []*DueReminderCandidate{}
	for rows.Next() {
		c := &DueReminderCandidate{}
		var offsetDays sql.NullInt64
		var timeOfDay sql.NullString
		if err := rows.Scan(&c.ItemID, &c.ListID, &c.Title, &c.DueDate, &offsetDays, &timeOfDay); err != nil {
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
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating due reminder candidate rows: %w", err)
	}
	return candidates, nil
}

// MarkDueReminderSent records that a reminder has been sent for itemID's
// due date exactly as given — internal/handlers.RunDueReminderScan calls
// this right after a successful send so the same due date isn't
// re-notified on the next scan tick. Storing the due_date value itself
// (rather than a plain boolean/timestamp flag) is what makes the "re-arm on
// due_date change" behavior described on ListItemsForDueReminderScan above
// automatic: once the item's due_date changes for any reason, this stored
// value simply stops matching it. A no-op (not an error) if the item no
// longer exists — the scan already has its own snapshot of the row and
// there is nothing left to update.
func (d *DB) MarkDueReminderSent(ctx context.Context, itemID int64, dueDate string) error {
	if _, err := d.conn.ExecContext(ctx,
		`UPDATE items SET due_reminder_sent_for = ? WHERE id = ?`, dueDate, itemID); err != nil {
		return fmt.Errorf("marking due reminder sent for item %d: %w", itemID, err)
	}
	return nil
}
