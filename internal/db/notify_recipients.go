package db

import (
	"context"
	"database/sql"
	"fmt"
)

// ListNotificationRecipients returns the id of every user who currently has
// access to listID — every member of its House, plus anyone it's been
// individually shared with (list_shares), plus anyone its parent Space has
// been shared with (space_shares) if it has one — excluding excludeUserID
// (the user whose own action triggered the notification, if any; pass 0
// when there is no single actor to exclude, as no real user ever has that
// id). This mirrors the three access sources AccessLevelForList already
// combines for a single user, just inverted: "who can see this list" rather
// than "can this one user see this list". Used by
// internal/handlers.notifyListChange/RunDueReminderScan to address a Web
// Push notification — a plain UNION (deduplicating automatically) is enough
// here, unlike AccessLevelForList, since no caller needs to know *what
// level* of access each recipient holds, only that they should be told
// about the change at all.
func (d *DB) ListNotificationRecipients(ctx context.Context, listID, excludeUserID int64) ([]int64, error) {
	rows, err := d.conn.QueryContext(ctx, listRecipientsQuery,
		listID, excludeUserID, listID, excludeUserID, listID, excludeUserID)
	if err != nil {
		return nil, fmt.Errorf("querying notification recipients for list %d: %w", listID, err)
	}
	return scanRecipientIDs(rows)
}

// listRecipientsQuery is ListNotificationRecipients' query: its parameters
// are (listID, excludeUserID) three times over, one pair per access source.
const listRecipientsQuery = `
		SELECT hm.user_id
		FROM house_members hm
		JOIN lists l ON l.house_id = hm.house_id
		WHERE l.id = ? AND hm.user_id != ?
		UNION
		SELECT ls.shared_with_user_id
		FROM list_shares ls
		WHERE ls.list_id = ? AND ls.shared_with_user_id != ?
		UNION
		SELECT ss.shared_with_user_id
		FROM space_shares ss
		JOIN lists l2 ON l2.custom_category_id = ss.custom_category_id
		WHERE l2.id = ? AND ss.shared_with_user_id != ?`

// NotificationKind is a type of notification a user can turn off in
// Paramètres ("Types de notifications"), each backed by one users column —
// see wantsNotificationExpr.
type NotificationKind string

const (
	NotifyTaskReminders       NotificationKind = "task_reminders"       // users.reminders_enabled
	NotifyCollaboratorActions NotificationKind = "collaborator_actions" // users.collaborator_actions_enabled
	NotifyItemAdditions       NotificationKind = "item_additions"       // users.item_additions_enabled
	NotifyListSharing         NotificationKind = "list_sharing"         // users.list_sharing_enabled
)

// wantsNotificationExpr is true for a users row that wants the kind bound to
// its one parameter. The kind is a bound value like any other (an unknown
// one matches nobody), so the query text never varies with it.
const wantsNotificationExpr = `CASE ?
		  WHEN 'task_reminders' THEN reminders_enabled
		  WHEN 'collaborator_actions' THEN collaborator_actions_enabled
		  WHEN 'item_additions' THEN item_additions_enabled
		  WHEN 'list_sharing' THEN list_sharing_enabled
		  ELSE 0 END = 1`

// ListNotificationRecipientsFor is ListNotificationRecipients narrowed to the
// users who want this kind of notification — the audience of every push
// about a list: checkItemForDueReminder (NotifyTaskReminders, nobody
// excluded) and notifyListChange (an item added or checked/unchecked by
// excludeUserID).
func (d *DB) ListNotificationRecipientsFor(ctx context.Context, listID, excludeUserID int64, kind NotificationKind) ([]int64, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT id FROM users WHERE `+wantsNotificationExpr+` AND id IN (`+listRecipientsQuery+`)`,
		string(kind), listID, excludeUserID, listID, excludeUserID, listID, excludeUserID)
	if err != nil {
		return nil, fmt.Errorf("querying %s recipients for list %d: %w", kind, listID, err)
	}
	return scanRecipientIDs(rows)
}

// UserWantsNotification reports whether userID wants this kind of
// notification (false for an unknown user).
func (d *DB) UserWantsNotification(ctx context.Context, userID int64, kind NotificationKind) (bool, error) {
	var n int
	if err := d.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE id = ? AND `+wantsNotificationExpr, userID, string(kind)).Scan(&n); err != nil {
		return false, fmt.Errorf("checking user %d's %s preference: %w", userID, kind, err)
	}
	return n > 0, nil
}

// scanRecipientIDs reads a single-column result of user ids, closing rows.
func scanRecipientIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning notification recipient row: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating notification recipient rows: %w", err)
	}
	return ids, nil
}
