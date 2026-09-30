package db

import (
	"context"
	"fmt"
	"strings"

	"trakka/internal/models"
)

func scanPushSubscription(row rowScanner) (*models.PushSubscription, error) {
	s := &models.PushSubscription{}
	if err := row.Scan(&s.ID, &s.UserID, &s.Endpoint, &s.P256dh, &s.Auth, &s.UserAgent, &s.CreatedAt); err != nil {
		return nil, err
	}
	return s, nil
}

// CreatePushSubscription registers a Web Push subscription for userID,
// upserting on the table's (user_id, endpoint) UNIQUE index so a browser
// re-subscribing the same endpoint (a key rotation, or simply re-enabling
// push after having granted it once already) refreshes the stored keys
// rather than erroring.
func (d *DB) CreatePushSubscription(ctx context.Context, userID int64, endpoint, p256dh, auth, userAgent string) (*models.PushSubscription, error) {
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, user_agent) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, user_agent = excluded.user_agent
	`, userID, endpoint, p256dh, auth, userAgent)
	if err != nil {
		return nil, fmt.Errorf("upserting push subscription: %w", err)
	}
	row := d.conn.QueryRowContext(ctx,
		`SELECT id, user_id, endpoint, p256dh, auth, user_agent, created_at FROM push_subscriptions WHERE user_id = ? AND endpoint = ?`,
		userID, endpoint)
	sub, err := scanPushSubscription(row)
	if err != nil {
		return nil, fmt.Errorf("querying upserted push subscription: %w", err)
	}
	return sub, nil
}

// DeletePushSubscription removes one subscription by (user_id, endpoint) —
// an explicit client-initiated unsubscribe. Returns ErrNotFound if userID
// has no subscription for endpoint.
func (d *DB) DeletePushSubscription(ctx context.Context, userID int64, endpoint string) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint)
	if err != nil {
		return fmt.Errorf("deleting push subscription: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected for push subscription delete: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePushSubscriptionByID removes one subscription outright, regardless
// of owner — used by internal/handlers.sendToUsers to clean up a
// subscription a push service reported as permanently gone (404/410),
// where the caller only has the row's own id in hand, not the user id it
// belongs to. A no-op (not an error) if the row is already gone.
func (d *DB) DeletePushSubscriptionByID(ctx context.Context, id int64) error {
	if _, err := d.conn.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting push subscription %d: %w", id, err)
	}
	return nil
}

// ListPushSubscriptionsForUsers returns every subscription belonging to any
// of userIDs — the batch form internal/handlers.sendToUsers uses to notify
// every recipient of a list change or a recurring-task reminder in one
// query rather than one per user. The query's placeholder count is built
// from len(userIDs) (a structural detail, not a value), while every actual
// id still passes through as a bound parameter — the standard Go idiom for
// a variable-length IN clause, not a departure from this package's
// "parameters only, never concatenate a value into the SQL text" rule.
// Joins users so each subscription also carries its owner's
// vibrate_on_notification preference (models.PushSubscription.Vibrate).
func (d *DB) ListPushSubscriptionsForUsers(ctx context.Context, userIDs []int64) ([]*models.PushSubscription, error) {
	if len(userIDs) == 0 {
		return []*models.PushSubscription{}, nil
	}
	placeholders := make([]string, len(userIDs))
	args := make([]any, len(userIDs))
	for i, id := range userIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf( // #nosec G201 -- only the placeholder count (a structural detail derived from len(userIDs)) is interpolated; every actual id is still bound as a query parameter below, never concatenated into the SQL text
		`SELECT s.id, s.user_id, s.endpoint, s.p256dh, s.auth, s.user_agent, s.created_at, u.vibrate_on_notification
		 FROM push_subscriptions s JOIN users u ON u.id = s.user_id WHERE s.user_id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	rows, err := d.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying push subscriptions for users: %w", err)
	}
	defer rows.Close()

	subs := []*models.PushSubscription{}
	for rows.Next() {
		s := &models.PushSubscription{}
		var vibrate int
		if err := rows.Scan(&s.ID, &s.UserID, &s.Endpoint, &s.P256dh, &s.Auth, &s.UserAgent, &s.CreatedAt, &vibrate); err != nil {
			return nil, fmt.Errorf("scanning push subscription row: %w", err)
		}
		s.Vibrate = vibrate != 0
		subs = append(subs, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating push subscription rows: %w", err)
	}
	return subs, nil
}
