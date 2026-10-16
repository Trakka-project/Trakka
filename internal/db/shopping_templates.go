package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"trakka/internal/models"
)

// shoppingTemplateColumns is the column list scanShoppingTemplate expects.
const shoppingTemplateColumns = `t.id, t.house_id, t.name, t.icon, t.list_type, t.created_by, t.created_at`

func scanShoppingTemplate(row rowScanner) (*models.ShoppingTemplate, error) {
	t := &models.ShoppingTemplate{Items: []*models.ShoppingTemplateItem{}}
	var createdBy sql.NullInt64
	if err := row.Scan(&t.ID, &t.HouseID, &t.Name, &t.Icon, &t.ListType, &createdBy, &t.CreatedAt); err != nil {
		return nil, err
	}
	if createdBy.Valid {
		id := createdBy.Int64
		t.CreatedBy = &id
	}
	return t, nil
}

// ListShoppingTemplatesForUser returns the House templates (migration 32)
// of every House userID is a member of — only houseID's when it is > 0 —
// with their items, newest first. A House the user isn't a member of
// silently yields nothing; the caller authorizes houseID separately.
func (d *DB) ListShoppingTemplatesForUser(ctx context.Context, userID, houseID int64) ([]*models.ShoppingTemplate, error) {
	query := `SELECT ` + shoppingTemplateColumns + `
		FROM shopping_templates t
		JOIN house_members hm ON hm.house_id = t.house_id AND hm.user_id = ?`
	args := []any{userID}
	if houseID > 0 {
		query += ` WHERE t.house_id = ?`
		args = append(args, houseID)
	}
	query += ` ORDER BY t.created_at DESC, t.id DESC`

	rows, err := d.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying shopping templates for user %d: %w", userID, err)
	}
	defer rows.Close()

	templates := []*models.ShoppingTemplate{}
	byID := map[int64]*models.ShoppingTemplate{}
	for rows.Next() {
		t, err := scanShoppingTemplate(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning shopping template row: %w", err)
		}
		templates = append(templates, t)
		byID[t.ID] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating shopping template rows: %w", err)
	}
	if len(templates) == 0 {
		return templates, nil
	}

	// Every template's items in one query rather than one per template.
	itemQuery := `SELECT ti.template_id, ti.title, ti.quantity, ti.estimated_price, ti.aisle, ti.recurrence_rule
		FROM shopping_template_items ti
		JOIN shopping_templates t ON t.id = ti.template_id
		JOIN house_members hm ON hm.house_id = t.house_id AND hm.user_id = ?`
	if houseID > 0 {
		itemQuery += ` WHERE t.house_id = ?`
	}
	itemQuery += ` ORDER BY ti.template_id, ti.position, ti.id`
	itemRows, err := d.conn.QueryContext(ctx, itemQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("querying shopping template items for user %d: %w", userID, err)
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var templateID int64
		item, err := scanShoppingTemplateItem(itemRows, &templateID)
		if err != nil {
			return nil, fmt.Errorf("scanning shopping template item row: %w", err)
		}
		if t := byID[templateID]; t != nil {
			t.Items = append(t.Items, item)
		}
	}
	if err := itemRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating shopping template item rows: %w", err)
	}
	return templates, nil
}

func scanShoppingTemplateItem(row rowScanner, templateID *int64) (*models.ShoppingTemplateItem, error) {
	item := &models.ShoppingTemplateItem{}
	var price sql.NullFloat64
	var aisle, rule sql.NullString
	if err := row.Scan(templateID, &item.Title, &item.Quantity, &price, &aisle, &rule); err != nil {
		return nil, err
	}
	if price.Valid {
		item.EstimatedPrice = &price.Float64
	}
	item.Aisle = aisle.String
	item.RecurrenceRule = rule.String
	return item, nil
}

// GetShoppingTemplate fetches one House template with its items. Returns
// ErrNotFound if no such template exists.
func (d *DB) GetShoppingTemplate(ctx context.Context, id int64) (*models.ShoppingTemplate, error) {
	t, err := scanShoppingTemplate(d.conn.QueryRowContext(ctx,
		`SELECT `+shoppingTemplateColumns+` FROM shopping_templates t WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying shopping template %d: %w", id, err)
	}

	rows, err := d.conn.QueryContext(ctx,
		`SELECT template_id, title, quantity, estimated_price, aisle, recurrence_rule
		 FROM shopping_template_items WHERE template_id = ? ORDER BY position, id`, id)
	if err != nil {
		return nil, fmt.Errorf("querying items of shopping template %d: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var templateID int64
		item, err := scanShoppingTemplateItem(rows, &templateID)
		if err != nil {
			return nil, fmt.Errorf("scanning shopping template item row: %w", err)
		}
		t.Items = append(t.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating shopping template item rows: %w", err)
	}
	return t, nil
}

// CreateShoppingTemplate stores a House template with its items, in order,
// in one transaction, and returns it. The caller (internal/handlers) has
// already validated every field.
func (d *DB) CreateShoppingTemplate(ctx context.Context, houseID int64, name, icon, listType string, createdBy int64, items []*models.ShoppingTemplateItem) (*models.ShoppingTemplate, error) {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning shopping template transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	res, err := tx.ExecContext(ctx,
		`INSERT INTO shopping_templates (house_id, name, icon, list_type, created_by) VALUES (?, ?, ?, ?, ?)`,
		houseID, name, icon, listType, createdBy)
	if err != nil {
		return nil, fmt.Errorf("inserting shopping template: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("reading inserted shopping template id: %w", err)
	}
	for position, item := range items {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO shopping_template_items (template_id, title, quantity, estimated_price, aisle, recurrence_rule, position)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, item.Title, item.Quantity, item.EstimatedPrice, nullableText(item.Aisle), nullableText(item.RecurrenceRule), position); err != nil {
			return nil, fmt.Errorf("inserting item %d of shopping template %d: %w", position, id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing shopping template %d: %w", id, err)
	}
	return d.GetShoppingTemplate(ctx, id)
}

// CountShoppingTemplates returns how many templates houseID has saved.
func (d *DB) CountShoppingTemplates(ctx context.Context, houseID int64) (int, error) {
	var n int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM shopping_templates WHERE house_id = ?`, houseID).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting shopping templates of house %d: %w", houseID, err)
	}
	return n, nil
}

// DeleteShoppingTemplate removes a House template (its items cascade).
// Returns ErrNotFound if no such template exists.
func (d *DB) DeleteShoppingTemplate(ctx context.Context, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM shopping_templates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting shopping template %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected for shopping template %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateListFromTemplate creates a list (like CreateList) together with a
// copy of a template's items, in one transaction, so a failure never leaves
// a half-filled list behind. Each item gets the template item's title,
// quantity, estimated price (as its price), aisle and recurrence rule, in
// order, planned when planned is true (see migration 32), and no due date or
// reminder — what POST /api/v1/items gives an item created without one. A
// recurring one's series starts when it is first done (migration 33).
func (d *DB) CreateListFromTemplate(ctx context.Context, name, listType string, houseID int64, customCategoryID *int64, icon string, items []*models.ShoppingTemplateItem, planned bool) (*models.List, error) {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning list-from-template transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	res, err := tx.ExecContext(ctx,
		`INSERT INTO lists (name, type, house_id, custom_category_id, icon) VALUES (?, ?, ?, ?, ?)`,
		name, listType, houseID, customCategoryID, icon)
	if err != nil {
		return nil, fmt.Errorf("inserting list: %w", err)
	}
	listID, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("reading inserted list id: %w", err)
	}
	for position, item := range items {
		quantity := item.Quantity
		if quantity <= 0 {
			quantity = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO items (list_id, title, quantity, price, position, aisle, is_recurring, recurrence_rule, is_planned, reminder_enabled)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			listID, item.Title, quantity, item.EstimatedPrice, position, nullableText(item.Aisle),
			boolToInt(item.RecurrenceRule != ""), nullableText(item.RecurrenceRule), boolToInt(planned)); err != nil {
			return nil, fmt.Errorf("inserting item %d of list %d: %w", position, listID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing list %d: %w", listID, err)
	}
	return d.GetList(ctx, listID)
}

// nullableText maps "" to SQL NULL for an optional TEXT column.
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
