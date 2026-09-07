package db

import (
	"context"
	"fmt"

	"trakka/internal/models"
)

// ListItemsForTargetPriceScan returns every not-done item with a url and an
// active price-drop threshold (alert_on_price_drop = 1, target_price set) —
// the population internal/handlers.RunTargetPriceScan re-scrapes on each
// periodic pass. Unlike ListItemsForPriceScan (the separate price_alerts
// accept/reject scan), an item's own current price is not required here — a
// freshly threshold-tagged item may not have one yet, and this scan's job is
// exactly to go find it. Items belonging to a custom (freeform notes) list
// are excluded via the lists join, the same reasoning ListItemsForPriceScan
// documents: the UI never lets a custom-list item carry a url/price/target
// price, so this only matters for one created directly through the API.
func (d *DB) ListItemsForTargetPriceScan(ctx context.Context) ([]*models.Item, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT items.id, items.list_id, items.title, items.url, items.quantity, items.done, items.position, items.created_at, items.updated_at,
		 items.price, items.price_auto, items.image_url, items.target_month,
		 items.due_date, items.is_recurring, items.recurrence_rule, items.recurrence_end_date, items.is_urgent, items.recurrence_lead_minutes,
		 items.target_price, items.alert_on_price_drop, items.labels
		 FROM items
		 JOIN lists ON lists.id = items.list_id
		 WHERE items.done = 0 AND items.url IS NOT NULL AND items.url != ''
		   AND items.alert_on_price_drop = 1 AND items.target_price IS NOT NULL
		   AND lists.type != 'custom'`)
	if err != nil {
		return nil, fmt.Errorf("querying items for target price scan: %w", err)
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

// UpdateItemPriceFromScan applies a freshly re-scraped price to an item as
// part of the target-price background worker (RunTargetPriceScan), marking
// it auto-detected the same as UpdateItemPriceIfMissing does. Unlike that
// method, this one is expected to fire even when the item already has a
// price (re-checking an existing price is the whole point of this scan), so
// instead of guarding on "price IS NULL" it guards on "price IS <the exact
// value the caller read just before scraping>" — a compare-and-swap using
// SQLite's NULL-safe IS operator, which handles "was nil" and "was some
// float" with the same statement, no branch needed. This is what stops a
// scrape that took a while to finish from clobbering a price the user (or a
// different scan) changed in the meantime, or a price for a since-changed
// url. Returns whether the row was actually updated: false is an expected,
// silent outcome (lost the race, the item was deleted, or its url changed
// since oldPrice/url were read) — not an error — and callers should treat it
// as "nothing to report", the same way UpdateItemPriceIfMissing's callers
// already do with a 0-rows-affected result.
func (d *DB) UpdateItemPriceFromScan(ctx context.Context, id int64, url string, oldPrice *float64, newPrice float64) (bool, error) {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE items SET price = ?, price_auto = 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND url = ? AND price IS ?`,
		newPrice, id, url, oldPrice)
	if err != nil {
		return false, fmt.Errorf("saving rescanned price for item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reading rows affected for item %d: %w", id, err)
	}
	return n > 0, nil
}
