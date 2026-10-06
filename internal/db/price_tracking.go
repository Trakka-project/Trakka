package db

import (
	"context"
	"fmt"
	"slices"

	"trakka/internal/models"
)

// RecordPriceObservation appends price to itemID's price_history, unless it
// equals the last price recorded for that item — the history only grows
// when the price actually moves, however often the background scans look.
// Reports whether a row was added.
func (d *DB) RecordPriceObservation(ctx context.Context, itemID int64, price float64) (bool, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO price_history (item_id, price)
		 SELECT ?, ?
		 WHERE (SELECT price FROM price_history WHERE item_id = ? ORDER BY recorded_at DESC, id DESC LIMIT 1) IS NOT ?`,
		itemID, price, itemID, price)
	if err != nil {
		return false, fmt.Errorf("recording observed price for item %d: %w", itemID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reading rows affected recording price for item %d: %w", itemID, err)
	}
	return n > 0, nil
}

// ListPriceHistory returns itemID's last limit observed prices, oldest
// first. An item never observed returns an empty slice.
func (d *DB) ListPriceHistory(ctx context.Context, itemID int64, limit int) ([]models.PricePoint, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT price, recorded_at FROM price_history WHERE item_id = ?
		 ORDER BY recorded_at DESC, id DESC LIMIT ?`, itemID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying price history for item %d: %w", itemID, err)
	}
	defer rows.Close()

	points := []models.PricePoint{}
	for rows.Next() {
		var p models.PricePoint
		if err := rows.Scan(&p.Price, &p.RecordedAt); err != nil {
			return nil, fmt.Errorf("scanning price history row: %w", err)
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating price history rows: %w", err)
	}
	slices.Reverse(points)
	return points, nil
}

// Price notification kinds — see models.PriceNotification.Kind.
const (
	PriceNotificationDrop     = "drop"
	PriceNotificationIncrease = "increase"
	PriceNotificationDeal     = "deal"
	PriceNotificationExpired  = "expired"
)

// HasPriceNotification reports whether anyone was already told kind about
// itemID for sourceURL — so a deal that expired is announced once, not on
// every scan that finds it still expired.
func (d *DB) HasPriceNotification(ctx context.Context, itemID int64, kind, sourceURL string) (bool, error) {
	var n int
	if err := d.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM price_notifications WHERE item_id = ? AND kind = ? AND source_url = ?`,
		itemID, kind, sourceURL).Scan(&n); err != nil {
		return false, fmt.Errorf("checking %s notifications of item %d: %w", kind, itemID, err)
	}
	return n > 0, nil
}

// CreatePriceNotifications adds one unread entry to the in-app price alert
// inbox of each of userIDs, in a single transaction.
func (d *DB) CreatePriceNotifications(ctx context.Context, userIDs []int64, itemID int64, kind string, oldPrice, newPrice float64, sourceURL *string) error {
	if len(userIDs) == 0 {
		return nil
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning price notification transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	for _, userID := range userIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO price_notifications (user_id, item_id, kind, old_price, new_price, source_url) VALUES (?, ?, ?, ?, ?, ?)`,
			userID, itemID, kind, oldPrice, newPrice, sourceURL); err != nil {
			return fmt.Errorf("adding price notification for user %d: %w", userID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing price notifications: %w", err)
	}
	return nil
}

// ListPriceNotifications returns userID's newest limit price notifications,
// read or not, newest first — only those of a kind the user currently wants
// (Paramètres → "Alertes de prix": the master switch, then drops — which
// include better deals — or increases; an expired deal under the master
// switch alone), since every entry is recorded whatever the preferences
// (see internal/handlers.notifyPriceChange), and only for items on lists
// the user can still access (the three access sources
// ListOverdueTasksForUser also checks), so losing access to a list also
// hides what was queued about it.
func (d *DB) ListPriceNotifications(ctx context.Context, userID int64, limit int) ([]*models.PriceNotification, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT pn.id, pn.item_id, items.title, items.list_id, pn.kind, pn.old_price, pn.new_price,
		       pn.source_url, pn.created_at, pn.read_at
		FROM price_notifications pn
		JOIN users u ON u.id = pn.user_id
		JOIN items ON items.id = pn.item_id
		JOIN lists l ON l.id = items.list_id
		WHERE pn.user_id = ?
		  AND u.price_alerts_enabled = 1
		  AND CASE pn.kind
		        WHEN 'increase' THEN u.price_increase_alerts_enabled
		        WHEN 'expired' THEN 1
		        ELSE u.price_drop_alerts_enabled
		      END = 1
		  AND (
		    EXISTS (SELECT 1 FROM house_members hm WHERE hm.house_id = l.house_id AND hm.user_id = pn.user_id)
		    OR EXISTS (SELECT 1 FROM list_shares ls WHERE ls.list_id = l.id AND ls.shared_with_user_id = pn.user_id)
		    OR EXISTS (SELECT 1 FROM space_shares ss WHERE ss.custom_category_id = l.custom_category_id AND ss.shared_with_user_id = pn.user_id)
		  )
		ORDER BY pn.created_at DESC, pn.id DESC
		LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying price notifications for user %d: %w", userID, err)
	}
	defer rows.Close()

	notifications := []*models.PriceNotification{}
	for rows.Next() {
		n := &models.PriceNotification{}
		if err := rows.Scan(&n.ID, &n.ItemID, &n.ItemTitle, &n.ListID, &n.Kind, &n.OldPrice, &n.NewPrice,
			&n.SourceURL, &n.CreatedAt, &n.ReadAt); err != nil {
			return nil, fmt.Errorf("scanning price notification row: %w", err)
		}
		notifications = append(notifications, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating price notification rows: %w", err)
	}
	return notifications, nil
}

// MarkPriceNotificationsRead marks userID's own unread price notifications
// read: those in ids, or every one of them when ids is empty. An id that
// isn't userID's is silently ignored.
func (d *DB) MarkPriceNotificationsRead(ctx context.Context, userID int64, ids []int64) error {
	const markRead = `UPDATE price_notifications SET read_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE user_id = ? AND read_at IS NULL`
	if len(ids) == 0 {
		if _, err := d.conn.ExecContext(ctx, markRead, userID); err != nil {
			return fmt.Errorf("marking price notifications of user %d read: %w", userID, err)
		}
		return nil
	}

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning price notification read transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, markRead+` AND id = ?`, userID, id); err != nil {
			return fmt.Errorf("marking price notification %d read: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing price notification read: %w", err)
	}
	return nil
}

// PrunePriceNotifications deletes read price notifications older than
// readDays days and every one older than allDays days, so the inbox table
// doesn't grow forever. Returns how many rows were deleted.
func (d *DB) PrunePriceNotifications(ctx context.Context, readDays, allDays int) (int64, error) {
	res, err := d.conn.ExecContext(ctx,
		`DELETE FROM price_notifications
		 WHERE (read_at IS NOT NULL AND created_at < strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))
		    OR created_at < strftime('%Y-%m-%dT%H:%M:%fZ','now', ?)`,
		fmt.Sprintf("-%d days", readDays), fmt.Sprintf("-%d days", allDays))
	if err != nil {
		return 0, fmt.Errorf("pruning price notifications: %w", err)
	}
	return res.RowsAffected()
}
