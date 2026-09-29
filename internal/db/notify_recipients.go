package db

import (
	"context"
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
	rows, err := d.conn.QueryContext(ctx, `
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
		WHERE l2.id = ? AND ss.shared_with_user_id != ?`,
		listID, excludeUserID, listID, excludeUserID, listID, excludeUserID)
	if err != nil {
		return nil, fmt.Errorf("querying notification recipients for list %d: %w", listID, err)
	}
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
