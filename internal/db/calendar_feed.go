package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CalendarFeedToken describes a user's calendar feed link without the
// token itself, which is never stored (only its hash is — see migration 25).
type CalendarFeedToken struct {
	CreatedAt string
	// LastUsedAt is when a calendar app last fetched the feed, "" if never.
	LastUsedAt string
}

// SetCalendarFeedToken stores tokenHash as userID's feed token, replacing
// any previous one (which stops working at once) and resetting its
// timestamps.
func (d *DB) SetCalendarFeedToken(ctx context.Context, userID int64, tokenHash string) (*CalendarFeedToken, error) {
	if _, err := d.conn.ExecContext(ctx, `
		INSERT INTO calendar_feed_tokens (user_id, token_hash) VALUES (?, ?)
		ON CONFLICT (user_id) DO UPDATE SET token_hash = excluded.token_hash,
		  created_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), last_used_at = NULL`,
		userID, tokenHash); err != nil {
		return nil, fmt.Errorf("upserting calendar feed token for user %d: %w", userID, err)
	}
	return d.GetCalendarFeedToken(ctx, userID)
}

// GetCalendarFeedToken returns userID's feed token metadata, or ErrNotFound
// if they have no feed link.
func (d *DB) GetCalendarFeedToken(ctx context.Context, userID int64) (*CalendarFeedToken, error) {
	t := &CalendarFeedToken{}
	err := d.conn.QueryRowContext(ctx,
		`SELECT created_at, COALESCE(last_used_at, '') FROM calendar_feed_tokens WHERE user_id = ?`, userID,
	).Scan(&t.CreatedAt, &t.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying calendar feed token for user %d: %w", userID, err)
	}
	return t, nil
}

// DeleteCalendarFeedToken removes userID's feed token, so the link stops
// working. Returns ErrNotFound if they had none.
func (d *DB) DeleteCalendarFeedToken(ctx context.Context, userID int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM calendar_feed_tokens WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("deleting calendar feed token for user %d: %w", userID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected deleting calendar feed token: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UseCalendarFeedToken resolves a feed token hash to its user and records
// the fetch in last_used_at. Returns ErrNotFound for an unknown hash —
// including one replaced by SetCalendarFeedToken or removed with its user.
func (d *DB) UseCalendarFeedToken(ctx context.Context, tokenHash string) (int64, error) {
	var userID int64
	err := d.conn.QueryRowContext(ctx,
		`SELECT user_id FROM calendar_feed_tokens WHERE token_hash = ?`, tokenHash,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("querying calendar feed token: %w", err)
	}
	if _, err := d.conn.ExecContext(ctx,
		`UPDATE calendar_feed_tokens SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE user_id = ?`,
		userID); err != nil {
		return 0, fmt.Errorf("recording calendar feed use for user %d: %w", userID, err)
	}
	return userID, nil
}

// CalendarFeedItem is one dated task for a user's calendar feed.
type CalendarFeedItem struct {
	ItemID   int64
	ListID   int64
	ListName string
	Title    string
	// Date (YYYY-MM-DD) is the occurrence shown: the due date of a task
	// still to do, or the next occurrence (next_due_date) of a done
	// recurring task.
	Date string
	// DueTime is the optional HH:MM due time, "" when the task has none.
	DueTime string
	// RecurrenceRule is the canonical RRULE subset (see internal/recurrence),
	// "" for a task that doesn't repeat; RecurrenceEndDate is "" for a series
	// with no end.
	RecurrenceRule    string
	RecurrenceEndDate string
	// The reminder's resolved timing, as on models.Item. HasReminder is false
	// when the reminder is off or its timing is incomplete.
	HasReminder bool
	OffsetDays  int
	TimeOfDay   string
	AtDueTime   bool
	UpdatedAt   string
}

// ListCalendarFeedItems returns every dated task userID would see in the
// app: tasks still to do that have a due date, and the next occurrence of
// done recurring tasks, on every list the user can access through any of
// the three sources AccessLevelForList combines (house membership, a list
// share, a space share). Done non-recurring tasks, and recurring ones whose
// series has ended, are left out. Ordered by date, then id, so the feed's
// output is stable between fetches.
func (d *DB) ListCalendarFeedItems(ctx context.Context, userID int64) ([]*CalendarFeedItem, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT items.id, items.list_id, l.name, items.title,
		       CASE WHEN items.done = 0 THEN items.due_date ELSE items.next_due_date END AS feed_date,
		       COALESCE(items.due_time, ''), COALESCE(items.recurrence_rule, ''), COALESCE(items.recurrence_end_date, ''),
		       items.reminder_enabled, items.reminder_offset_days, items.reminder_time, items.reminder_at_due_time,
		       items.updated_at
		FROM items
		JOIN lists l ON l.id = items.list_id
		WHERE ((items.done = 0 AND items.due_date IS NOT NULL)
		       OR (items.done = 1 AND items.next_due_date IS NOT NULL))
		  AND (
		    EXISTS (SELECT 1 FROM house_members hm WHERE hm.house_id = l.house_id AND hm.user_id = ?)
		    OR EXISTS (SELECT 1 FROM list_shares ls WHERE ls.list_id = l.id AND ls.shared_with_user_id = ?)
		    OR EXISTS (SELECT 1 FROM space_shares ss WHERE ss.custom_category_id = l.custom_category_id AND ss.shared_with_user_id = ?)
		  )
		ORDER BY feed_date, items.id`,
		userID, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("querying calendar feed items for user %d: %w", userID, err)
	}
	defer rows.Close()

	items := []*CalendarFeedItem{}
	for rows.Next() {
		it := &CalendarFeedItem{}
		var reminderEnabled, atDueTime int
		var offsetDays sql.NullInt64
		var timeOfDay sql.NullString
		if err := rows.Scan(&it.ItemID, &it.ListID, &it.ListName, &it.Title, &it.Date, &it.DueTime,
			&it.RecurrenceRule, &it.RecurrenceEndDate, &reminderEnabled, &offsetDays, &timeOfDay, &atDueTime,
			&it.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning calendar feed item row: %w", err)
		}
		it.AtDueTime = atDueTime != 0
		// Same defensive skip as ListItemsForDueReminderScan: an enabled
		// reminder without a resolved offset/time has no moment to show.
		if reminderEnabled != 0 && offsetDays.Valid && timeOfDay.Valid && timeOfDay.String != "" {
			it.HasReminder = true
			it.OffsetDays = int(offsetDays.Int64)
			it.TimeOfDay = timeOfDay.String
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating calendar feed item rows: %w", err)
	}
	return items, nil
}
