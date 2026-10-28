package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// FluvIntegration is a user's connection to their Fluv (migration 41,
// internal/fluv). TokenSealed is the Fluv token sealed with the instance
// key — never the token itself, which internal/fluv alone opens, right
// before sending.
type FluvIntegration struct {
	UserID      int64
	EndpointURL string
	// PinnedIPs is the comma-separated list of the only addresses the
	// purchases are ever sent to (see internal/fluv's pinned dialer).
	PinnedIPs   string
	TokenSealed string
	Enabled     bool
	CreatedAt   string
	UpdatedAt   string
	// LastSuccessAt/LastErrorCode/LastErrorAt describe the latest exchange
	// with Fluv, "" when there was none.
	LastSuccessAt string
	LastErrorCode string
	LastErrorAt   string
}

// FluvPurchase is one row of the fluv_purchases outbox: one time an item
// was bought, as Fluv should hold it (Desired) and as it is known to hold
// it (Synced). See migration 41.
type FluvPurchase struct {
	ID            int64
	UserID        int64
	ItemID        *int64
	Ref           string
	Title         string
	Store         string
	Quantity      int
	AmountMinor   int64
	PurchasedAt   string
	PurchasedOn   string
	Desired       string
	Synced        string
	Failed        bool
	Attempts      int
	NextAttemptAt string
	LastStatus    int
	LastErrorCode string
}

// Values of FluvPurchase.Desired and FluvPurchase.Synced.
const (
	FluvDesiredRecord = "record"
	FluvDesiredCancel = "cancel"

	FluvSyncedNone      = "none"
	FluvSyncedRecorded  = "recorded"
	FluvSyncedCancelled = "cancelled"
)

// fluvPendingCondition selects the outbox rows still to send — the same
// expression as idx_fluv_purchases_due's WHERE clause, so SQLite can use
// that partial index.
const fluvPendingCondition = `failed = 0 AND NOT ((desired = 'record' AND synced = 'recorded') OR (desired = 'cancel' AND synced = 'cancelled'))`

const fluvPurchaseColumns = `id, user_id, item_id, ref, title, store, quantity, amount_minor, purchased_at, purchased_on,
	desired, synced, failed, attempts, next_attempt_at, COALESCE(last_status, 0), COALESCE(last_error_code, '')`

func scanFluvPurchase(row interface{ Scan(...any) error }) (*FluvPurchase, error) {
	p := &FluvPurchase{}
	var itemID sql.NullInt64
	var failed int
	if err := row.Scan(&p.ID, &p.UserID, &itemID, &p.Ref, &p.Title, &p.Store, &p.Quantity, &p.AmountMinor,
		&p.PurchasedAt, &p.PurchasedOn, &p.Desired, &p.Synced, &failed, &p.Attempts, &p.NextAttemptAt,
		&p.LastStatus, &p.LastErrorCode); err != nil {
		return nil, err
	}
	if itemID.Valid {
		id := itemID.Int64
		p.ItemID = &id
	}
	p.Failed = failed != 0
	return p, nil
}

// GetFluvIntegration returns userID's Fluv connection, or ErrNotFound.
func (d *DB) GetFluvIntegration(ctx context.Context, userID int64) (*FluvIntegration, error) {
	f := &FluvIntegration{}
	var enabled int
	err := d.conn.QueryRowContext(ctx,
		`SELECT user_id, endpoint_url, pinned_ips, token_sealed, enabled, created_at, updated_at,
		        COALESCE(last_success_at, ''), COALESCE(last_error_code, ''), COALESCE(last_error_at, '')
		 FROM fluv_integrations WHERE user_id = ?`, userID,
	).Scan(&f.UserID, &f.EndpointURL, &f.PinnedIPs, &f.TokenSealed, &enabled, &f.CreatedAt, &f.UpdatedAt,
		&f.LastSuccessAt, &f.LastErrorCode, &f.LastErrorAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying Fluv integration of user %d: %w", userID, err)
	}
	f.Enabled = enabled != 0
	return f, nil
}

// SaveFluvIntegration creates or replaces userID's Fluv connection. The
// status of the latest exchange is reset: it described the previous
// settings.
func (d *DB) SaveFluvIntegration(ctx context.Context, userID int64, endpointURL, pinnedIPs, tokenSealed string, enabled bool) error {
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO fluv_integrations (user_id, endpoint_url, pinned_ips, token_sealed, enabled)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET
			endpoint_url = excluded.endpoint_url, pinned_ips = excluded.pinned_ips,
			token_sealed = excluded.token_sealed, enabled = excluded.enabled,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			last_success_at = NULL, last_error_code = NULL, last_error_at = NULL`,
		userID, endpointURL, pinnedIPs, tokenSealed, boolToInt(enabled))
	if err != nil {
		return fmt.Errorf("saving Fluv integration of user %d: %w", userID, err)
	}
	return nil
}

// DeleteFluvIntegration removes userID's Fluv connection and their outbox:
// what was not sent yet never will be. Purchases Fluv already recorded stay
// there. Returns ErrNotFound if there was no connection.
func (d *DB) DeleteFluvIntegration(ctx context.Context, userID int64) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning Fluv integration removal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM fluv_integrations WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("deleting Fluv integration of user %d: %w", userID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected deleting Fluv integration: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fluv_purchases WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("deleting Fluv outbox of user %d: %w", userID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing Fluv integration removal: %w", err)
	}
	return nil
}

// SetFluvPinnedIPs moves userID's IP lock to pinnedIPs (comma-separated),
// after a connection test found Fluv there.
func (d *DB) SetFluvPinnedIPs(ctx context.Context, userID int64, pinnedIPs string) error {
	if _, err := d.conn.ExecContext(ctx, `
		UPDATE fluv_integrations SET pinned_ips = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE user_id = ?`, pinnedIPs, userID); err != nil {
		return fmt.Errorf("re-locking Fluv integration of user %d: %w", userID, err)
	}
	return nil
}

// RecordFluvExchange stores the outcome of the latest exchange with
// userID's Fluv: a success when code is "", else that error code.
func (d *DB) RecordFluvExchange(ctx context.Context, userID int64, code string) error {
	query := `UPDATE fluv_integrations SET last_success_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
	          last_error_code = NULL, last_error_at = NULL WHERE user_id = ?`
	args := []any{userID}
	if code != "" {
		query = `UPDATE fluv_integrations SET last_error_code = ?,
		         last_error_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE user_id = ?`
		args = []any{code, userID}
	}
	if _, err := d.conn.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("recording Fluv exchange of user %d: %w", userID, err)
	}
	return nil
}

// InsertFluvPurchase queues p (ID, Desired, Synced and the attempt fields
// are ignored: a new row is always a pending record), first due at
// notBefore.
func (d *DB) InsertFluvPurchase(ctx context.Context, p FluvPurchase, notBefore time.Time) error {
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO fluv_purchases (user_id, item_id, ref, title, store, quantity, amount_minor,
		                            purchased_at, purchased_on, next_attempt_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.UserID, p.ItemID, p.Ref, p.Title, p.Store, p.Quantity, p.AmountMinor,
		p.PurchasedAt, p.PurchasedOn, notBefore.UTC().Format(timeFormat))
	if err != nil {
		return fmt.Errorf("queueing Fluv purchase for item %v: %w", p.ItemID, err)
	}
	return nil
}

// ActiveFluvPurchaseForItem returns the latest purchase of itemID Fluv
// should hold (desired = record), or ErrNotFound.
func (d *DB) ActiveFluvPurchaseForItem(ctx context.Context, itemID int64) (*FluvPurchase, error) {
	p, err := scanFluvPurchase(d.conn.QueryRowContext(ctx,
		`SELECT `+fluvPurchaseColumns+` FROM fluv_purchases
		 WHERE item_id = ? AND desired = 'record' ORDER BY id DESC LIMIT 1`, itemID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying Fluv purchase of item %d: %w", itemID, err)
	}
	return p, nil
}

// RepriceUnsentFluvPurchase corrects a purchase that was never sent and
// isn't being sent (the paid price typed right after checking the item
// off), reporting whether it could: once a row has been on the wire, the
// correction must be a cancellation plus a new purchase instead.
func (d *DB) RepriceUnsentFluvPurchase(ctx context.Context, id int64, title string, quantity int, amountMinor int64) (bool, error) {
	res, err := d.conn.ExecContext(ctx, `
		UPDATE fluv_purchases SET title = ?, quantity = ?, amount_minor = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND desired = 'record' AND synced = 'none' AND attempts = 0 AND in_flight = 0`,
		title, quantity, amountMinor, id)
	if err != nil {
		return false, fmt.Errorf("repricing Fluv purchase %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reading rows affected repricing Fluv purchase: %w", err)
	}
	return n > 0, nil
}

// CancelFluvPurchase asks for purchase id to be taken back out of Fluv. A
// row that was never sent is settled on the spot; any other is queued for
// a cancellation, due at once — including one whose record attempts
// failed, since a timed-out attempt may still have reached Fluv.
func (d *DB) CancelFluvPurchase(ctx context.Context, id int64) error {
	now := time.Now().UTC().Format(timeFormat)
	_, err := d.conn.ExecContext(ctx, `
		UPDATE fluv_purchases SET
			synced = CASE WHEN synced = 'none' AND attempts = 0 AND in_flight = 0 THEN 'cancelled' ELSE synced END,
			desired = 'cancel', failed = 0, attempts = 0, next_attempt_at = ?, last_error_code = NULL,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND desired = 'record'`, now, id)
	if err != nil {
		return fmt.Errorf("cancelling Fluv purchase %d: %w", id, err)
	}
	return nil
}

// detachFluvPurchases unlinks itemID's purchases from it, when the item is
// un-checked by the server itself — a recurring item coming back for its
// next occurrence, a Courses product added again — rather than by a person:
// those purchases happened, and un-checking or repricing the item from now
// on is about the next one.
func (d *DB) detachFluvPurchases(ctx context.Context, itemID int64) error {
	if _, err := d.conn.ExecContext(ctx, `UPDATE fluv_purchases SET item_id = NULL WHERE item_id = ?`, itemID); err != nil {
		return fmt.Errorf("detaching Fluv purchases of item %d: %w", itemID, err)
	}
	return nil
}

// ClaimNextFluvPurchase marks the next due purchase of an enabled
// connection in flight and returns it, or ErrNotFound when none is due.
func (d *DB) ClaimNextFluvPurchase(ctx context.Context, now time.Time) (*FluvPurchase, error) {
	p, err := scanFluvPurchase(d.conn.QueryRowContext(ctx, `
		UPDATE fluv_purchases SET in_flight = 1
		WHERE id = (
			SELECT id FROM fluv_purchases
			WHERE `+fluvPendingCondition+` AND in_flight = 0 AND next_attempt_at <= ?
			  AND user_id IN (SELECT user_id FROM fluv_integrations WHERE enabled = 1)
			ORDER BY next_attempt_at, id LIMIT 1)
		RETURNING `+fluvPurchaseColumns, now.UTC().Format(timeFormat)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("claiming next Fluv purchase: %w", err)
	}
	return p, nil
}

// FluvAttempt is the outcome of sending a purchase, for FinishFluvAttempt.
type FluvAttempt struct {
	// Acted is the Desired value the attempt carried out.
	Acted string
	// Synced is what Fluv is now known to hold.
	Synced string
	// Failed flags the purchase as needing the user (no automatic retry).
	Failed      bool
	Attempts    int
	NextAttempt time.Time
	Status      int
	ErrorCode   string
}

// FinishFluvAttempt records the outcome of the attempt on purchase id and
// releases it. If the purchase was cancelled while in flight, the attempt
// counters start over and the cancellation is due at once.
func (d *DB) FinishFluvAttempt(ctx context.Context, id int64, a FluvAttempt) error {
	now := time.Now().UTC().Format(timeFormat)
	var code any
	if a.ErrorCode != "" {
		code = a.ErrorCode
	}
	_, err := d.conn.ExecContext(ctx, `
		UPDATE fluv_purchases SET in_flight = 0, synced = ?, last_status = ?, last_error_code = ?,
			failed = CASE WHEN desired = ? THEN ? ELSE 0 END,
			attempts = CASE WHEN desired = ? THEN ? ELSE 0 END,
			next_attempt_at = CASE WHEN desired = ? THEN ? ELSE ? END,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`,
		a.Synced, a.Status, code,
		a.Acted, boolToInt(a.Failed),
		a.Acted, a.Attempts,
		a.Acted, a.NextAttempt.UTC().Format(timeFormat), now,
		id)
	if err != nil {
		return fmt.Errorf("recording Fluv attempt on purchase %d: %w", id, err)
	}
	return nil
}

// ReleaseFluvPurchases clears every in-flight mark — at startup, for rows a
// previous process was sending when it stopped. Sending one again is safe:
// Fluv ignores a purchase it already has.
func (d *DB) ReleaseFluvPurchases(ctx context.Context) error {
	if _, err := d.conn.ExecContext(ctx, `UPDATE fluv_purchases SET in_flight = 0 WHERE in_flight = 1`); err != nil {
		return fmt.Errorf("releasing in-flight Fluv purchases: %w", err)
	}
	return nil
}

// RetryFailedFluvPurchases makes userID's failed purchases due again at
// once, with fresh attempt counters, and returns how many there were.
func (d *DB) RetryFailedFluvPurchases(ctx context.Context, userID int64) (int64, error) {
	res, err := d.conn.ExecContext(ctx, `
		UPDATE fluv_purchases SET failed = 0, attempts = 0, next_attempt_at = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE user_id = ? AND failed = 1`, time.Now().UTC().Format(timeFormat), userID)
	if err != nil {
		return 0, fmt.Errorf("retrying failed Fluv purchases of user %d: %w", userID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reading rows affected retrying Fluv purchases: %w", err)
	}
	return n, nil
}

// FluvQueueStats counts userID's purchases still to send and those that
// failed.
func (d *DB) FluvQueueStats(ctx context.Context, userID int64) (pending, failed int, err error) {
	err = d.conn.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN `+fluvPendingCondition+` THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(failed), 0)
		FROM fluv_purchases WHERE user_id = ?`, userID,
	).Scan(&pending, &failed)
	if err != nil {
		return 0, 0, fmt.Errorf("counting Fluv purchases of user %d: %w", userID, err)
	}
	return pending, failed, nil
}

// PruneFluvPurchases deletes the settled purchases last changed before
// cutoff — only kept so that un-checking or repricing an item can still
// reach the purchase in Fluv — and returns how many it deleted.
func (d *DB) PruneFluvPurchases(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := d.conn.ExecContext(ctx, `
		DELETE FROM fluv_purchases
		WHERE failed = 0 AND in_flight = 0 AND updated_at < ?
		  AND ((desired = 'record' AND synced = 'recorded') OR (desired = 'cancel' AND synced = 'cancelled'))`,
		cutoff.UTC().Format(timeFormat))
	if err != nil {
		return 0, fmt.Errorf("pruning Fluv purchases: %w", err)
	}
	return res.RowsAffected()
}
