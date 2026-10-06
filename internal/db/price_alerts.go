package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"trakka/internal/models"
)

// priceAlertSelect is shared by every read below; it always joins in the
// item's title and list_id, since every caller needs at least one of them
// (display, or authorizeItemAccess's need for a list_id).
const priceAlertSelect = `
	SELECT price_alerts.id, price_alerts.item_id, items.title, items.list_id,
	       price_alerts.original_price, price_alerts.found_price, price_alerts.source_url,
	       items.url IS NOT price_alerts.source_url,
	       price_alerts.status, price_alerts.created_at
	FROM price_alerts JOIN items ON items.id = price_alerts.item_id`

func scanPriceAlert(row rowScanner) (*models.PriceAlert, error) {
	a := &models.PriceAlert{}
	if err := row.Scan(&a.ID, &a.ItemID, &a.ItemTitle, &a.ListID,
		&a.OriginalPrice, &a.FoundPrice, &a.SourceURL, &a.ChangesURL, &a.Status, &a.CreatedAt); err != nil {
		return nil, err
	}
	return a, nil
}

// CreatePriceAlertIfNonePending records a lower price found for itemID at
// sourceURL, unless a pending alert for that item already exists. The
// periodic price scan (internal/handlers.RunPriceAlertScan) re-checks every
// eligible item on every run, so without this guard the same price drop
// would spawn a fresh alert every single time the scan ran before the
// existing one was accepted or rejected. Silently does nothing in that
// case — the same "expected, not an error" no-op convention as
// UpdateItemPriceIfMissing/UpdateItemImageIfMissing. Reports whether an
// alert was actually created, so the caller notifies about a new deal once
// rather than on every scan that sees it again.
func (d *DB) CreatePriceAlertIfNonePending(ctx context.Context, itemID int64, originalPrice, foundPrice float64, sourceURL string) (bool, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO price_alerts (item_id, original_price, found_price, source_url)
		 SELECT ?, ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM price_alerts WHERE item_id = ? AND status = 'pending')`,
		itemID, originalPrice, foundPrice, sourceURL, itemID)
	if err != nil {
		return false, fmt.Errorf("recording price alert for item %d: %w", itemID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reading rows affected recording price alert for item %d: %w", itemID, err)
	}
	return n > 0, nil
}

// HasPriceAlertForSource reports whether itemID already has an alert of any
// status for sourceURL at foundPrice — so a deal the user already rejected
// (or accepted) is not proposed again on every scan that still finds it.
func (d *DB) HasPriceAlertForSource(ctx context.Context, itemID int64, sourceURL string, foundPrice float64) (bool, error) {
	var n int
	if err := d.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM price_alerts WHERE item_id = ? AND source_url = ? AND found_price = ?`,
		itemID, sourceURL, foundPrice).Scan(&n); err != nil {
		return false, fmt.Errorf("checking price alerts of item %d for %s: %w", itemID, sourceURL, err)
	}
	return n > 0, nil
}

// GetPriceAlert fetches a single alert by id. Returns ErrNotFound if no
// such alert exists.
func (d *DB) GetPriceAlert(ctx context.Context, id int64) (*models.PriceAlert, error) {
	row := d.conn.QueryRowContext(ctx, priceAlertSelect+` WHERE price_alerts.id = ?`, id)
	alert, err := scanPriceAlert(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying price alert %d: %w", id, err)
	}
	return alert, nil
}

// GetPendingPriceAlertForItem returns itemID's pending alert, if it has
// one. Returns ErrNotFound when the item currently has no pending alert —
// a normal outcome (no better price known right now), not a failure; used
// by the on-demand price-check endpoint to report what, if anything, the
// check just found.
func (d *DB) GetPendingPriceAlertForItem(ctx context.Context, itemID int64) (*models.PriceAlert, error) {
	row := d.conn.QueryRowContext(ctx,
		priceAlertSelect+` WHERE price_alerts.item_id = ? AND price_alerts.status = 'pending'`, itemID)
	alert, err := scanPriceAlert(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying pending price alert for item %d: %w", itemID, err)
	}
	return alert, nil
}

// ListPriceAlertsByHouse returns every alert for items belonging to
// houseID, newest first. If status is non-empty it restricts the result to
// that one status (the notification bell always passes "pending"); empty
// returns every status.
func (d *DB) ListPriceAlertsByHouse(ctx context.Context, houseID int64, status string) ([]*models.PriceAlert, error) {
	query := priceAlertSelect + ` JOIN lists ON lists.id = items.list_id WHERE lists.house_id = ?`
	args := []any{houseID}
	if status != "" {
		query += ` AND price_alerts.status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY price_alerts.created_at DESC`

	rows, err := d.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying price alerts for house %d: %w", houseID, err)
	}
	defer rows.Close()

	alerts := []*models.PriceAlert{}
	for rows.Next() {
		a, err := scanPriceAlert(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning price alert row: %w", err)
		}
		alerts = append(alerts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating price alert rows: %w", err)
	}
	return alerts, nil
}

// AcceptPriceAlert applies a pending alert's found price to its item and
// marks the alert accepted, atomically. The item's url becomes the alert's
// source_url — a no-op for a lower price found on the item's own page, a
// switch to the deal page for one found on Dealabs — and its price follows
// that page from then on (price_auto = 1), so the background scans track
// the price the user chose rather than the one they turned down. The found
// price starts the new url's price history, and previous_price/
// price_changed_at are cleared: the user's own decision is not a movement
// at the store. image_url is kept: it is still the same product. A partial
// failure here — the item updated but the alert left "pending", or vice
// versa — would otherwise let the same alert be actioned twice, or silently
// fail to apply the price it claims to have applied; this is the same
// reasoning CreateHouseWithOwner uses for its own transaction. sourceURL
// must have passed internal/validate.URL (the handler re-checks it). Returns
// ErrNotFound if the alert doesn't exist or was already actioned (not
// "pending").
func (d *DB) AcceptPriceAlert(ctx context.Context, id int64) (*models.PriceAlert, error) {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning price alert acceptance transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	var itemID int64
	var foundPrice float64
	var sourceURL string
	err = tx.QueryRowContext(ctx,
		`SELECT item_id, found_price, source_url FROM price_alerts WHERE id = ? AND status = 'pending'`, id,
	).Scan(&itemID, &foundPrice, &sourceURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying price alert %d: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE items SET price = ?, price_auto = 1, url = ?, previous_price = NULL, price_changed_at = NULL,
		 updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		foundPrice, sourceURL, itemID,
	); err != nil {
		return nil, fmt.Errorf("applying accepted price to item %d: %w", itemID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO price_history (item_id, price)
		 SELECT ?, ?
		 WHERE (SELECT price FROM price_history WHERE item_id = ? ORDER BY recorded_at DESC, id DESC LIMIT 1) IS NOT ?`,
		itemID, foundPrice, itemID, foundPrice,
	); err != nil {
		return nil, fmt.Errorf("recording accepted price for item %d: %w", itemID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE price_alerts SET status = 'accepted' WHERE id = ?`, id,
	); err != nil {
		return nil, fmt.Errorf("marking price alert %d accepted: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing price alert acceptance: %w", err)
	}
	return d.GetPriceAlert(ctx, id)
}

// RejectPriceAlert marks a pending alert rejected without touching its
// item. Returns ErrNotFound if the alert doesn't exist or was already
// actioned (not "pending") — the same guard AcceptPriceAlert uses, so an
// alert can never be actioned twice regardless of which action is taken
// first.
func (d *DB) RejectPriceAlert(ctx context.Context, id int64) (*models.PriceAlert, error) {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE price_alerts SET status = 'rejected' WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return nil, fmt.Errorf("rejecting price alert %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected for price alert %d: %w", id, err)
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return d.GetPriceAlert(ctx, id)
}

// ListItemsForPriceScan returns every not-done item that has a url and no
// active target price — the population internal/handlers.RunPriceAlertScan
// tracks on each periodic pass. Items with an active target price
// (alert_on_price_drop = 1 and target_price set) are left out: they belong
// to the more frequent RunTargetPriceScan (ListItemsForTargetPriceScan),
// which runs the same per-item check, so no item is fetched by both. An
// item without a price yet is included, so a price the initial lookup
// missed is still filled in later. A done item's price isn't worth chasing
// anymore. Items belonging to a `custom`
// (freeform notes) list are excluded via the lists join: the UI never lets
// a custom-list item carry a url/price (see FIELD_VISIBILITY_BY_TYPE in
// static/js/list_view.js), so this only ever matters for one created
// directly through the API, but the price-drop notification center is
// documented as strictly ignoring custom lists regardless of how the data
// got there.
func (d *DB) ListItemsForPriceScan(ctx context.Context) ([]*models.Item, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT `+itemSelectColumns+`
		 FROM items
		 JOIN lists ON lists.id = items.list_id
		 WHERE items.done = 0 AND items.url IS NOT NULL AND items.url != '' AND lists.type != 'custom'
		   AND NOT (items.alert_on_price_drop = 1 AND items.target_price IS NOT NULL)`)
	if err != nil {
		return nil, fmt.Errorf("querying items for price scan: %w", err)
	}
	defer rows.Close()

	items := []*models.Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning item row: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating item rows: %w", err)
	}
	return items, nil
}
