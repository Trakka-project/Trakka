package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"trakka/internal/models"
)

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(row rowScanner) (*models.Item, error) {
	it := &models.Item{}
	var done int
	var priceAuto int
	var isRecurring int
	var isUrgent int
	var price sql.NullFloat64
	var imageURL sql.NullString
	var targetMonth sql.NullString
	var dueDate sql.NullString
	var recurrenceRule sql.NullString
	var recurrenceEndDate sql.NullString
	var recurrenceLeadMinutes sql.NullInt64
	var targetPrice sql.NullFloat64
	var alertOnPriceDrop int
	var labelsJSON string
	if err := row.Scan(&it.ID, &it.ListID, &it.Title, &it.URL, &it.Quantity, &done,
		&it.Position, &it.CreatedAt, &it.UpdatedAt, &price, &priceAuto, &imageURL, &targetMonth,
		&dueDate, &isRecurring, &recurrenceRule, &recurrenceEndDate, &isUrgent, &recurrenceLeadMinutes,
		&targetPrice, &alertOnPriceDrop, &labelsJSON); err != nil {
		return nil, err
	}
	it.Done = done != 0
	if price.Valid {
		it.Price = &price.Float64
	}
	it.PriceAuto = priceAuto != 0
	if imageURL.Valid && imageURL.String != "" {
		s := imageURL.String
		it.ImageURL = &s
	}
	if targetMonth.Valid && targetMonth.String != "" {
		s := targetMonth.String
		it.TargetMonth = &s
	}
	if dueDate.Valid && dueDate.String != "" {
		s := dueDate.String
		it.DueDate = &s
	}
	it.IsRecurring = isRecurring != 0
	if recurrenceRule.Valid && recurrenceRule.String != "" {
		s := recurrenceRule.String
		it.RecurrenceRule = &s
	}
	if recurrenceEndDate.Valid && recurrenceEndDate.String != "" {
		s := recurrenceEndDate.String
		it.RecurrenceEndDate = &s
	}
	it.IsUrgent = isUrgent != 0
	if recurrenceLeadMinutes.Valid {
		n := int(recurrenceLeadMinutes.Int64)
		it.RecurrenceLeadMinutes = &n
	}
	if targetPrice.Valid {
		it.TargetPrice = &targetPrice.Float64
	}
	it.AlertOnPriceDrop = alertOnPriceDrop != 0
	labels := []string{}
	if labelsJSON != "" {
		if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil {
			return nil, fmt.Errorf("decoding labels for item %d: %w", it.ID, err)
		}
		if labels == nil {
			labels = []string{}
		}
	}
	it.Labels = labels
	return it, nil
}

// ListItemsByList returns all items of a list, ordered for display. Returns
// an empty slice (not an error) if the list has no items or does not exist;
// callers that need existence checked should call GetList first.
func (d *DB) ListItemsByList(ctx context.Context, listID int64) ([]*models.Item, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT id, list_id, title, url, quantity, done, position, created_at, updated_at, price, price_auto, image_url, target_month,
		 due_date, is_recurring, recurrence_rule, recurrence_end_date, is_urgent, recurrence_lead_minutes, target_price, alert_on_price_drop, labels
		 FROM items WHERE list_id = ? ORDER BY position ASC, id ASC`, listID)
	if err != nil {
		return nil, fmt.Errorf("querying items for list %d: %w", listID, err)
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

// CreateItem inserts a new item under listID and returns the persisted row.
// priceAuto should always be false here — a freshly created item's price
// (even if nil) always comes directly from the request, never from the
// scraper (see scrapePrice in internal/handlers, which runs only after the
// item already exists). targetMonth is the planned purchase month
// (YYYY-MM, validated by internal/validate.Month) or nil if not scheduled.
// dueDate (YYYY-MM-DD) and recurrenceEndDate (YYYY-MM-DD) are validated by
// internal/validate.Date; recurrenceRule is validated by
// internal/validate.Recurrence. is_recurring is never a separate argument —
// it's stored as simply whether recurrenceRule is non-nil, so the two can
// never disagree. isUrgent is a plain user-set flag, independent of every
// other field here — see models.Item.IsUrgent. recurrenceLeadMinutes
// optionally overrides the instance-wide NOTIF_RECURRING_TASK_LEAD_TIME for
// this item alone (see models.Item.RecurrenceLeadMinutes); nil leaves it
// unset, meaning "use the instance default".
// targetPrice/alertOnPriceDrop back the "notify me when the price drops"
// feature (see models.Item.TargetPrice/AlertOnPriceDrop); this method only
// stores whatever the caller passed in, the actual threshold comparison and
// notification live in internal/handlers.checkPriceDropAlert.
func (d *DB) CreateItem(ctx context.Context, listID int64, title string, url *string, quantity int, price *float64, priceAuto bool, position int, targetMonth, dueDate, recurrenceRule, recurrenceEndDate *string, isUrgent bool, recurrenceLeadMinutes *int, targetPrice *float64, alertOnPriceDrop bool) (*models.Item, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO items (list_id, title, url, quantity, price, price_auto, position, target_month, due_date, is_recurring, recurrence_rule, recurrence_end_date, is_urgent, recurrence_lead_minutes, target_price, alert_on_price_drop)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		listID, title, url, quantity, price, boolToInt(priceAuto), position, targetMonth, dueDate, boolToInt(recurrenceRule != nil), recurrenceRule, recurrenceEndDate, boolToInt(isUrgent), recurrenceLeadMinutes, targetPrice, boolToInt(alertOnPriceDrop))
	if err != nil {
		return nil, fmt.Errorf("inserting item: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("reading inserted item id: %w", err)
	}
	return d.GetItem(ctx, id)
}

// GetItem fetches a single item by id. Returns ErrNotFound if no such item
// exists.
func (d *DB) GetItem(ctx context.Context, id int64) (*models.Item, error) {
	row := d.conn.QueryRowContext(ctx,
		`SELECT id, list_id, title, url, quantity, done, position, created_at, updated_at, price, price_auto, image_url, target_month,
		 due_date, is_recurring, recurrence_rule, recurrence_end_date, is_urgent, recurrence_lead_minutes, target_price, alert_on_price_drop, labels
		 FROM items WHERE id = ?`, id)
	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying item %d: %w", id, err)
	}
	return item, nil
}

// UpdateItem replaces every mutable field of an item. priceAuto should be
// false for every caller except the PATCH handler preserving an existing
// scraped price it didn't touch (see internal/handlers/items.go); any
// caller that supplies price directly from the request is setting it
// manually, even when the value happens to be nil. imageURL should be the
// item's existing image_url when url isn't changing, or nil when it is —
// a scraped image is tied to the url it was found on, so a url change
// invalidates it the same way a manual price invalidates price_auto (see
// internal/handlers/items.go, which computes this before calling in).
// targetMonth is the planned purchase month (YYYY-MM) or nil to leave the
// item unscheduled. dueDate/recurrenceRule/recurrenceEndDate follow the
// same validation/is_recurring-derivation rules as CreateItem — callers
// (internal/handlers) are expected to have already run a recurring item's
// completion through applyRecurrenceCompletion before calling this, so
// done/dueDate here already reflect any auto-advance. isUrgent is a plain
// user-set flag, independent of every other field here — see
// models.Item.IsUrgent. recurrenceLeadMinutes follows the same
// "nil means use the instance default" convention as CreateItem. Returns
// ErrNotFound if no such item exists.
// targetPrice/alertOnPriceDrop follow the same pass-through convention as
// CreateItem — see its doc comment.
func (d *DB) UpdateItem(ctx context.Context, id int64, title string, url *string, quantity int, price *float64, priceAuto bool, imageURL *string, done bool, position int, targetMonth, dueDate, recurrenceRule, recurrenceEndDate *string, isUrgent bool, recurrenceLeadMinutes *int, targetPrice *float64, alertOnPriceDrop bool) (*models.Item, error) {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE items SET title = ?, url = ?, quantity = ?, price = ?, price_auto = ?, image_url = ?, done = ?, position = ?, target_month = ?,
		 due_date = ?, is_recurring = ?, recurrence_rule = ?, recurrence_end_date = ?, is_urgent = ?, recurrence_lead_minutes = ?,
		 target_price = ?, alert_on_price_drop = ?,
		 updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		title, url, quantity, price, boolToInt(priceAuto), imageURL, boolToInt(done), position, targetMonth,
		dueDate, boolToInt(recurrenceRule != nil), recurrenceRule, recurrenceEndDate, boolToInt(isUrgent), recurrenceLeadMinutes,
		targetPrice, boolToInt(alertOnPriceDrop), id)
	if err != nil {
		return nil, fmt.Errorf("updating item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected for item %d: %w", id, err)
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return d.GetItem(ctx, id)
}

// UpdateItemPriceIfMissing sets an item's price and marks it
// auto-detected, but only if the item still has no price and its url still
// matches the one that was scraped. internal/handlers' background price
// lookup (kicked off after an item is created or its url changes) uses
// this rather than UpdateItem so a slow scrape can never clobber a price
// the user typed in, or a price for a since-changed url, while it was in
// flight. Rows affected == 0 (item deleted, price set manually meanwhile,
// or url changed meanwhile) is an expected, silent no-op — there is
// nothing to report as an error.
func (d *DB) UpdateItemPriceIfMissing(ctx context.Context, id int64, url string, price float64) error {
	_, err := d.conn.ExecContext(ctx,
		`UPDATE items SET price = ?, price_auto = 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND url = ? AND price IS NULL`,
		price, id, url)
	if err != nil {
		return fmt.Errorf("saving scraped price for item %d: %w", id, err)
	}
	return nil
}

// UpdateItemImageIfMissing sets an item's image_url, but only if the item
// still has no image and its url still matches the one that was scraped —
// the same race guard as UpdateItemPriceIfMissing, and for the same reason:
// internal/handlers' background product lookup calls this rather than
// UpdateItem so a slow scrape can never overwrite an image for a
// since-changed url. Rows affected == 0 (item deleted, image already set,
// or url changed meanwhile) is an expected, silent no-op.
func (d *DB) UpdateItemImageIfMissing(ctx context.Context, id int64, url string, imageURL string) error {
	_, err := d.conn.ExecContext(ctx,
		`UPDATE items SET image_url = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND url = ? AND (image_url IS NULL OR image_url = '')`,
		imageURL, id, url)
	if err != nil {
		return fmt.Errorf("saving scraped image for item %d: %w", id, err)
	}
	return nil
}

// ReorderItems assigns each item in itemIDs a new position matching its
// index in the slice (0-based), so ListItemsByList/GetList's own `ORDER BY
// position ASC, id ASC` reflects the new order immediately afterward.
// itemIDs must be exactly a permutation of listID's current items — every
// current item named once, nothing else — or ErrInvalidReorder is returned
// without writing anything; a partial list would leave the omitted items'
// positions ambiguous relative to the reordered ones, and silently
// accepting an id from a different list would let a caller who only has
// write access to listID reorder items out from under a list they don't
// control. Runs as a single transaction so a failure partway through can
// never leave positions in a mixed old/new state. Returns the list's items
// in their new order (equivalent to a fresh ListItemsByList call).
func (d *DB) ReorderItems(ctx context.Context, listID int64, itemIDs []int64) ([]*models.Item, error) {
	current, err := d.ListItemsByList(ctx, listID)
	if err != nil {
		return nil, err
	}
	if len(itemIDs) != len(current) {
		return nil, ErrInvalidReorder
	}
	currentIDs := make(map[int64]bool, len(current))
	for _, item := range current {
		currentIDs[item.ID] = true
	}
	seen := make(map[int64]bool, len(itemIDs))
	for _, id := range itemIDs {
		if seen[id] || !currentIDs[id] {
			return nil, ErrInvalidReorder
		}
		seen[id] = true
	}

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning reorder transaction for list %d: %w", listID, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	for position, id := range itemIDs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE items SET position = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ? AND list_id = ?`,
			position, id, listID,
		); err != nil {
			return nil, fmt.Errorf("updating position for item %d: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing reorder transaction for list %d: %w", listID, err)
	}

	return d.ListItemsByList(ctx, listID)
}

// SetItemLabels replaces an item's full label set (see models.Item.Labels)
// and returns the updated row. Kept as its own method rather than folded
// into CreateItem/UpdateItem's already-long parameter list — the same
// reasoning ReorderItems already established for a per-item concern that
// doesn't need to ride along with every other field write. labels must
// already be cleaned by the caller (internal/validate.Labels); this method
// only persists whatever it's given, replacing nil with an empty slice so
// the stored JSON is always a valid array, never "null". Returns
// ErrNotFound if no such item exists.
func (d *DB) SetItemLabels(ctx context.Context, id int64, labels []string) (*models.Item, error) {
	if labels == nil {
		labels = []string{}
	}
	encoded, err := json.Marshal(labels)
	if err != nil {
		return nil, fmt.Errorf("encoding labels for item %d: %w", id, err)
	}
	res, err := d.conn.ExecContext(ctx,
		`UPDATE items SET labels = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		string(encoded), id)
	if err != nil {
		return nil, fmt.Errorf("updating labels for item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected for item %d: %w", id, err)
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return d.GetItem(ctx, id)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DeleteItem removes an item. Returns ErrNotFound if no such item exists.
func (d *DB) DeleteItem(ctx context.Context, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected for item %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
