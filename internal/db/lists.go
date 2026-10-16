package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"trakka/internal/budget"
	"trakka/internal/models"
)

// listSelect is shared by every read below. The LEFT JOIN embeds the
// attached custom category's own columns (all NULL when
// lists.custom_category_id is NULL, or the category was deleted out from
// under a stale reference — impossible here since deleting a category sets
// custom_category_id back to NULL via ON DELETE SET NULL, but scanListRow
// handles it defensively regardless) so a single query can return a list
// with its category embedded, matching the API's documented shape for
// GET /api/v1/lists and GET /api/v1/lists/{id}. The trailing correlated
// subquery computes total_amount — the sum of price * quantity across every
// still-unchecked item in the list that has a price set (items.done = 0) —
// so the dashboard/Espaces cards can show a list's "reste à dépenser"
// without a separate per-item fetch; see models.List.TotalAmount. A done
// item is deliberately excluded here, the same as the list detail view's own
// "Reste à dépenser" figure (updateFinanceSummary/lineTotal in
// static/js/list_view.js) — this field is that same figure, just computed
// once in SQL instead of re-derived per card from a full item fetch.
// Quantity is defensively floored at 1 the same way lineTotal already does,
// even though internal/handlers/items.go never actually persists a quantity
// <= 0. SUM() over zero matching rows (no unchecked priced items at all)
// returns SQL NULL, which is exactly the "nothing to show" signal
// TotalAmount's nil case relies on — no COALESCE needed.
//
// The two subqueries after it are the rest of the list's budget (see
// models.List.Budget and internal/budget.Sums, which scanListRow builds it
// from): bought_estimated, the estimate of every bought (done) item, and
// spent, what those cost — actual_price when one was entered, the estimate
// otherwise. total_amount is the third sum, the estimate of what is left to
// buy. All three NULL means no item has any price at all: no budget.
const listSelect = `
	SELECT lists.id, lists.house_id, lists.name, lists.type, lists.icon, lists.created_at, lists.updated_at, lists.custom_category_id,
	       cc.id, cc.user_id, cc.name, cc.icon, cc.color, cc.position, cc.created_at,
	       (SELECT SUM(items.price * CASE WHEN items.quantity > 0 THEN items.quantity ELSE 1 END)
	          FROM items WHERE items.list_id = lists.id AND items.price IS NOT NULL AND items.done = 0) AS total_amount,
	       (SELECT SUM(items.price * CASE WHEN items.quantity > 0 THEN items.quantity ELSE 1 END)
	          FROM items WHERE items.list_id = lists.id AND items.price IS NOT NULL AND items.done = 1) AS bought_estimated,
	       (SELECT SUM(COALESCE(items.actual_price, items.price) * CASE WHEN items.quantity > 0 THEN items.quantity ELSE 1 END)
	          FROM items WHERE items.list_id = lists.id AND COALESCE(items.actual_price, items.price) IS NOT NULL AND items.done = 1) AS spent
	FROM lists LEFT JOIN custom_categories cc ON cc.id = lists.custom_category_id`

func scanListRow(row rowScanner) (*models.List, error) {
	l := &models.List{}
	var customCategoryID sql.NullInt64
	var catID sql.NullInt64
	var catUserID sql.NullInt64
	var catName, catIcon, catColor, catCreatedAt sql.NullString
	var catPosition sql.NullInt64
	var totalAmount, boughtEstimated, spent sql.NullFloat64
	if err := row.Scan(&l.ID, &l.HouseID, &l.Name, &l.Type, &l.Icon, &l.CreatedAt, &l.UpdatedAt, &customCategoryID,
		&catID, &catUserID, &catName, &catIcon, &catColor, &catPosition, &catCreatedAt, &totalAmount, &boughtEstimated, &spent); err != nil {
		return nil, err
	}
	if customCategoryID.Valid {
		id := customCategoryID.Int64
		l.CustomCategoryID = &id
	}
	if catID.Valid {
		l.CustomCategory = &models.CustomCategory{
			ID:        catID.Int64,
			UserID:    catUserID.Int64,
			Name:      catName.String,
			Icon:      catIcon.String,
			Color:     catColor.String,
			Position:  int(catPosition.Int64),
			CreatedAt: catCreatedAt.String,
		}
	}
	if totalAmount.Valid {
		amount := totalAmount.Float64
		l.TotalAmount = &amount
	}
	if totalAmount.Valid || boughtEstimated.Valid || spent.Valid {
		l.Budget = listBudget(budget.Sums{
			RemainingEstimated: totalAmount.Float64,
			BoughtEstimated:    boughtEstimated.Float64,
			Spent:              spent.Float64,
		})
	}
	return l, nil
}

// listBudget is internal/budget.Summarize in the API's shape.
func listBudget(sums budget.Sums) *models.ListBudget {
	summary := budget.Summarize(sums)
	return &models.ListBudget{
		EstimatedTotal: summary.EstimatedTotal,
		SpentTotal:     summary.SpentTotal,
		RemainingTotal: summary.RemainingTotal,
		ProjectedTotal: summary.ProjectedTotal,
		Variance:       summary.Variance,
		VarianceStatus: summary.Status,
	}
}

// ListListsForUser returns lists belonging to houses userID is a member
// of, newest first. If typeFilter is non-empty, it restricts the result to
// lists of that type. If houseID is > 0, it further restricts the result
// to that one house (the caller should authorize access to it separately;
// this method silently returns nothing for a house the user isn't in).
func (d *DB) ListListsForUser(ctx context.Context, userID int64, typeFilter string, houseID int64) ([]*models.List, error) {
	query := listSelect + ` JOIN house_members hm ON hm.house_id = lists.house_id AND hm.user_id = ?`
	conditions := []string{}
	args := []any{userID}
	if typeFilter != "" {
		conditions = append(conditions, `lists.type = ?`)
		args = append(args, typeFilter)
	}
	if houseID > 0 {
		conditions = append(conditions, `lists.house_id = ?`)
		args = append(args, houseID)
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY lists.created_at DESC`

	rows, err := d.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying lists: %w", err)
	}
	defer rows.Close()

	lists := []*models.List{}
	for rows.Next() {
		l, err := scanListRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning list row: %w", err)
		}
		lists = append(lists, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating list rows: %w", err)
	}
	return lists, nil
}

// CreateList inserts a new list under houseID and returns the persisted
// row. customCategoryID optionally attaches it to one of the caller's
// CustomCategory "spaces" (nil leaves it unattached) — the caller
// (internal/handlers) is expected to have already validated that it
// references a category the requesting user owns. icon is a freeform
// display icon (typically an emoji); "" means none was set.
func (d *DB) CreateList(ctx context.Context, name, listType string, houseID int64, customCategoryID *int64, icon string) (*models.List, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO lists (name, type, house_id, custom_category_id, icon) VALUES (?, ?, ?, ?, ?)`,
		name, listType, houseID, customCategoryID, icon)
	if err != nil {
		return nil, fmt.Errorf("inserting list: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("reading inserted list id: %w", err)
	}
	return d.GetList(ctx, id)
}

// GetList fetches a single list by id (without its items), with its
// attached custom category embedded if any. Returns ErrNotFound if no such
// list exists.
func (d *DB) GetList(ctx context.Context, id int64) (*models.List, error) {
	row := d.conn.QueryRowContext(ctx, listSelect+` WHERE lists.id = ?`, id)
	l, err := scanListRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying list %d: %w", id, err)
	}
	return l, nil
}

// UpdateList replaces a list's name, type, icon, and custom category
// association (nil dissociates it). Returns ErrNotFound if no such list
// exists.
func (d *DB) UpdateList(ctx context.Context, id int64, name, listType string, customCategoryID *int64, icon string) (*models.List, error) {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE lists SET name = ?, type = ?, custom_category_id = ?, icon = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		name, listType, customCategoryID, icon, id)
	if err != nil {
		return nil, fmt.Errorf("updating list %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected for list %d: %w", id, err)
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return d.GetList(ctx, id)
}

// DeleteList removes a list (its items cascade via ON DELETE CASCADE).
// Returns ErrNotFound if no such list exists.
func (d *DB) DeleteList(ctx context.Context, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM lists WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting list %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected for list %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
