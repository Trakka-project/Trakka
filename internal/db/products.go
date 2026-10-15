package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"trakka/internal/models"
)

// A House's product catalog (house_products, migration 30): what the
// household has added to its "Courses" lists and checked off them. Every
// method is scoped by house_id; callers (internal/handlers/products.go and
// the item handlers) check House membership first. A product is identified
// within its House by its name key, internal/catalog.Key(name), which the
// caller computes: this package never normalizes names itself.

const productSelectColumns = `house_products.id, house_products.house_id, house_products.name, house_products.aisle,
	house_products.is_favorite, house_products.add_count, house_products.purchase_count,
	house_products.first_purchased_at, house_products.last_purchased_at,
	house_products.created_at, house_products.updated_at`

func scanProduct(row rowScanner) (*models.Product, error) {
	p := &models.Product{}
	var aisle, firstPurchase, lastPurchase sql.NullString
	var isFavorite int
	if err := row.Scan(&p.ID, &p.HouseID, &p.Name, &aisle, &isFavorite, &p.AddCount, &p.PurchaseCount,
		&firstPurchase, &lastPurchase, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Aisle = aisle.String
	p.IsFavorite = isFavorite != 0
	p.FirstPurchasedAt = nullStringPtr(firstPurchase)
	p.LastPurchasedAt = nullStringPtr(lastPurchase)
	return p, nil
}

func (d *DB) queryProducts(ctx context.Context, query string, args ...any) ([]*models.Product, error) {
	rows, err := d.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying products: %w", err)
	}
	defer rows.Close()
	products := []*models.Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning product row: %w", err)
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating product rows: %w", err)
	}
	return products, nil
}

func (d *DB) queryProduct(ctx context.Context, query string, args ...any) (*models.Product, error) {
	p, err := scanProduct(d.conn.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying product: %w", err)
	}
	return p, nil
}

// ProductAddition describes a product being added to one of a House's
// "Courses" lists, for RecordProductAdded.
type ProductAddition struct {
	HouseID int64
	// Name is the spelling just typed; Key is catalog.Key(Name).
	Name string
	Key  string
	// Aisle is the item's aisle ("" for none). AisleExplicit says whether
	// the user chose it: an explicit aisle becomes the product's new usual
	// one, while a guessed or inherited one only fills a gap.
	Aisle         string
	AisleExplicit bool
	// Barcode, when not "", was scanned for this product: it is filed under
	// it (house_product_barcodes), moving it off whichever other product of
	// the House it was filed under before.
	Barcode string
}

// RecordProductAdded notes in the House's catalog that a product was added
// to a list: the product is created on first sight, otherwise its add
// count and last-added date move on and its name takes the latest spelling.
func (d *DB) RecordProductAdded(ctx context.Context, p ProductAddition) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning product addition: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	now := time.Now().UTC().Format(timeFormat)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO house_products (house_id, name, name_key, aisle, add_count, last_added_at)
		 VALUES (?, ?, ?, ?, 1, ?)
		 ON CONFLICT (house_id, name_key) DO UPDATE SET
		   name = excluded.name,
		   add_count = add_count + 1,
		   last_added_at = excluded.last_added_at,
		   aisle = CASE WHEN ? THEN excluded.aisle ELSE COALESCE(aisle, excluded.aisle) END,
		   updated_at = excluded.last_added_at`,
		p.HouseID, p.Name, p.Key, nullIfEmpty(p.Aisle), now,
		p.AisleExplicit && p.Aisle != "",
	); err != nil {
		return fmt.Errorf("recording product addition: %w", err)
	}
	if p.Barcode != "" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO house_product_barcodes (house_id, barcode, product_id)
			 VALUES (?, ?, (SELECT id FROM house_products WHERE house_id = ? AND name_key = ?))
			 ON CONFLICT (house_id, barcode) DO UPDATE SET product_id = excluded.product_id`,
			p.HouseID, p.Barcode, p.HouseID, p.Key,
		); err != nil {
			return fmt.Errorf("filing barcode: %w", err)
		}
	}
	return tx.Commit()
}

// RecordProductPurchased notes in the House's catalog that a product was
// checked off a list at at: one more purchase, the latest one at at, and
// the first one too if it had none. A product checked off before the
// catalog knew it (an item older than migration 30, or added through a list
// type that doesn't feed the catalog before being retyped) is created.
// aisle only fills a gap.
func (d *DB) RecordProductPurchased(ctx context.Context, houseID int64, name, key, aisle string, at time.Time) error {
	stamp := at.UTC().Format(timeFormat)
	if _, err := d.conn.ExecContext(ctx,
		`INSERT INTO house_products (house_id, name, name_key, aisle, purchase_count, first_purchased_at, last_purchased_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)
		 ON CONFLICT (house_id, name_key) DO UPDATE SET
		   purchase_count = purchase_count + 1,
		   first_purchased_at = COALESCE(first_purchased_at, excluded.first_purchased_at),
		   last_purchased_at = excluded.last_purchased_at,
		   aisle = COALESCE(aisle, excluded.aisle),
		   updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		houseID, name, key, nullIfEmpty(aisle), stamp, stamp,
	); err != nil {
		return fmt.Errorf("recording product purchase: %w", err)
	}
	return nil
}

// SetProductAisle makes aisle ("" clears it) the House's usual aisle for
// the product keyed key — what someone just filed an item under — creating
// the product if the catalog doesn't know it yet.
func (d *DB) SetProductAisle(ctx context.Context, houseID int64, name, key, aisle string) error {
	if _, err := d.conn.ExecContext(ctx,
		`INSERT INTO house_products (house_id, name, name_key, aisle) VALUES (?, ?, ?, ?)
		 ON CONFLICT (house_id, name_key) DO UPDATE SET
		   aisle = excluded.aisle,
		   updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		houseID, name, key, nullIfEmpty(aisle),
	); err != nil {
		return fmt.Errorf("setting product aisle: %w", err)
	}
	return nil
}

// UpsertProduct creates the product keyed key in houseID's catalog if it
// isn't there yet (named name), and applies the given changes: isFavorite
// when non-nil, aisle when non-nil ("" clears it). An existing product
// keeps its own name. Returns the product.
func (d *DB) UpsertProduct(ctx context.Context, houseID int64, name, key string, isFavorite *bool, aisle *string) (*models.Product, error) {
	var favorite any
	if isFavorite != nil {
		favorite = boolToInt(*isFavorite)
	}
	if _, err := d.conn.ExecContext(ctx,
		`INSERT INTO house_products (house_id, name, name_key, is_favorite, aisle) VALUES (?, ?, ?, COALESCE(?, 0), ?)
		 ON CONFLICT (house_id, name_key) DO UPDATE SET
		   is_favorite = COALESCE(?, is_favorite),
		   aisle = CASE WHEN ? THEN excluded.aisle ELSE aisle END,
		   updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		houseID, name, key, favorite, nullIfEmpty(stringValueOrEmpty(aisle)),
		favorite, aisle != nil,
	); err != nil {
		return nil, fmt.Errorf("upserting product: %w", err)
	}
	return d.GetProductByKey(ctx, houseID, key)
}

// UpdateProduct applies the given changes to product id of houseID: each
// field is left alone when nil; aisle "" clears it. Returns ErrNotFound if
// houseID has no such product.
func (d *DB) UpdateProduct(ctx context.Context, houseID, id int64, isFavorite *bool, aisle *string) (*models.Product, error) {
	var favorite any
	if isFavorite != nil {
		favorite = boolToInt(*isFavorite)
	}
	res, err := d.conn.ExecContext(ctx,
		`UPDATE house_products SET
		   is_favorite = COALESCE(?, is_favorite),
		   aisle = CASE WHEN ? THEN ? ELSE aisle END,
		   updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND house_id = ?`,
		favorite, aisle != nil, nullIfEmpty(stringValueOrEmpty(aisle)), id, houseID,
	)
	if err != nil {
		return nil, fmt.Errorf("updating product %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected for product %d: %w", id, err)
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return d.GetProduct(ctx, houseID, id)
}

// DeleteProduct removes product id from houseID's catalog ("forget this
// product": it stops being suggested until it is added again), with the
// barcodes filed under it. Items on lists are untouched. Returns ErrNotFound if houseID has no such product.
func (d *DB) DeleteProduct(ctx context.Context, houseID, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM house_products WHERE id = ? AND house_id = ?`, id, houseID)
	if err != nil {
		return fmt.Errorf("deleting product %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected for product %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetProduct returns product id of houseID, or ErrNotFound — also when the
// product exists but belongs to another House.
func (d *DB) GetProduct(ctx context.Context, houseID, id int64) (*models.Product, error) {
	return d.queryProduct(ctx, `SELECT `+productSelectColumns+` FROM house_products WHERE id = ? AND house_id = ?`, id, houseID)
}

// GetProductByKey returns houseID's product keyed key, or ErrNotFound.
func (d *DB) GetProductByKey(ctx context.Context, houseID int64, key string) (*models.Product, error) {
	return d.queryProduct(ctx, `SELECT `+productSelectColumns+` FROM house_products WHERE house_id = ? AND name_key = ?`, houseID, key)
}

// GetProductByBarcode returns houseID's product filed under barcode (as
// internal/validate.Barcode normalizes it), or ErrNotFound.
func (d *DB) GetProductByBarcode(ctx context.Context, houseID int64, barcode string) (*models.Product, error) {
	return d.queryProduct(ctx,
		`SELECT `+productSelectColumns+` FROM house_product_barcodes
		 JOIN house_products ON house_products.id = house_product_barcodes.product_id
		 WHERE house_product_barcodes.house_id = ? AND house_product_barcodes.barcode = ?`, houseID, barcode)
}

// SearchProducts returns up to limit of houseID's products whose key
// contains keyQuery (itself a catalog.Key, so already lowercase and
// unaccented), for autocomplete: names starting with it first, then names
// with a word starting with it, then the rest; within each, favorites,
// then the most added/bought, then alphabetical.
func (d *DB) SearchProducts(ctx context.Context, houseID int64, keyQuery string, limit int) ([]*models.Product, error) {
	if keyQuery == "" {
		return []*models.Product{}, nil
	}
	escaped := likeEscaper.Replace(keyQuery)
	return d.queryProducts(ctx,
		`SELECT `+productSelectColumns+` FROM house_products
		 WHERE house_id = ? AND name_key LIKE ? ESCAPE '\'
		 ORDER BY name_key LIKE ? ESCAPE '\' DESC,
		          (' ' || name_key) LIKE ? ESCAPE '\' DESC,
		          is_favorite DESC, (add_count + purchase_count) DESC, name_key
		 LIMIT ?`,
		houseID, "%"+escaped+"%", escaped+"%", "% "+escaped+"%", limit)
}

// ListFavoriteProducts returns houseID's favorite products, alphabetically.
func (d *DB) ListFavoriteProducts(ctx context.Context, houseID int64) ([]*models.Product, error) {
	return d.queryProducts(ctx,
		`SELECT `+productSelectColumns+` FROM house_products
		 WHERE house_id = ? AND is_favorite = 1 ORDER BY name_key`, houseID)
}

// ListPurchasedProducts returns houseID's products checked off at least
// minPurchases times (with both purchase dates known): the candidates for
// "time to buy it again" suggestions, ranked by the caller
// (catalog.RankRepurchases).
func (d *DB) ListPurchasedProducts(ctx context.Context, houseID int64, minPurchases int) ([]*models.Product, error) {
	return d.queryProducts(ctx,
		`SELECT `+productSelectColumns+` FROM house_products
		 WHERE house_id = ? AND purchase_count >= ?
		   AND first_purchased_at IS NOT NULL AND last_purchased_at IS NOT NULL
		 ORDER BY name_key`, houseID, minPurchases)
}

// stringValueOrEmpty dereferences an optional string, "" for nil.
func stringValueOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
