package db

import (
	"context"
	"fmt"
)

// OverdueSummaryUser is one account the daily overdue-tasks summary scan
// still has to handle today (see ListUsersAwaitingOverdueSummary).
type OverdueSummaryUser struct {
	UserID int64
	// Time is the user's own overdue_tasks_summary_time (HH:MM).
	Time string
}

// ListUsersAwaitingOverdueSummary returns every user with the daily
// overdue-tasks summary turned on (users.overdue_tasks_summary_enabled)
// whose summary hasn't been handled yet for today (a YYYY-MM-DD local date,
// see MarkOverdueSummarySent) — internal/handlers.RunOverdueSummaryScan then
// decides which of them have reached their own time of day.
func (d *DB) ListUsersAwaitingOverdueSummary(ctx context.Context, today string) ([]*OverdueSummaryUser, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT id, overdue_tasks_summary_time
		FROM users
		WHERE overdue_tasks_summary_enabled = 1
		  AND (overdue_tasks_summary_sent_on IS NULL OR overdue_tasks_summary_sent_on != ?)`,
		today)
	if err != nil {
		return nil, fmt.Errorf("querying users awaiting the overdue summary: %w", err)
	}
	defer rows.Close()

	users := []*OverdueSummaryUser{}
	for rows.Next() {
		u := &OverdueSummaryUser{}
		if err := rows.Scan(&u.UserID, &u.Time); err != nil {
			return nil, fmt.Errorf("scanning overdue summary user row: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating overdue summary user rows: %w", err)
	}
	return users, nil
}

// MarkOverdueSummarySent records that userID's summary for the local date
// day (YYYY-MM-DD) has been handled — sent, or found to have nothing to
// report — so the scan skips them until the next day.
func (d *DB) MarkOverdueSummarySent(ctx context.Context, userID int64, day string) error {
	if _, err := d.conn.ExecContext(ctx,
		`UPDATE users SET overdue_tasks_summary_sent_on = ? WHERE id = ?`, day, userID); err != nil {
		return fmt.Errorf("marking overdue summary sent for user %d: %w", userID, err)
	}
	return nil
}

// OverdueTask is one task listed by the overdue-tasks summary.
type OverdueTask struct {
	ItemID   int64
	ListID   int64
	ListName string
	Title    string
	// DueDate is the task's due date (YYYY-MM-DD), always before the day the
	// summary is for.
	DueDate string
}

// ListOverdueTasksForUser returns the not-done tasks due before the date
// `before` (YYYY-MM-DD, the day the summary is for) on the lists userID can
// access — the three access sources ListActiveRemindersForUser also checks —
// oldest first (then by due time, tasks without one first, list and id).
// Every task past its due date, however long ago: an overdue task keeps
// being summarized every day until it is checked off or rescheduled. A done
// recurring task is excluded like any other done task — its due date is the
// occurrence already completed. A task's own reminder setting doesn't matter
// here: the summary isn't that task's reminder.
func (d *DB) ListOverdueTasksForUser(ctx context.Context, userID int64, before string) ([]*OverdueTask, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT items.id, items.list_id, l.name, items.title, items.due_date
		FROM items
		JOIN lists l ON l.id = items.list_id
		WHERE items.done = 0 AND items.due_date IS NOT NULL AND items.due_date < ?
		  AND (
		    EXISTS (SELECT 1 FROM house_members hm WHERE hm.house_id = l.house_id AND hm.user_id = ?)
		    OR EXISTS (SELECT 1 FROM list_shares ls WHERE ls.list_id = l.id AND ls.shared_with_user_id = ?)
		    OR EXISTS (SELECT 1 FROM space_shares ss WHERE ss.custom_category_id = l.custom_category_id AND ss.shared_with_user_id = ?)
		  )
		ORDER BY items.due_date, COALESCE(items.due_time, ''), l.name, items.id`,
		before, userID, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("querying overdue tasks for user %d: %w", userID, err)
	}
	defer rows.Close()

	tasks := []*OverdueTask{}
	for rows.Next() {
		t := &OverdueTask{}
		if err := rows.Scan(&t.ItemID, &t.ListID, &t.ListName, &t.Title, &t.DueDate); err != nil {
			return nil, fmt.Errorf("scanning overdue task row: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating overdue task rows: %w", err)
	}
	return tasks, nil
}
