package db

import (
	"database/sql"
	"fmt"

	"trakka/internal/catalog"
)

// applySeedHouseProductsMigration is migration 31: it fills the
// house_products catalog that migration 30 created from the items already
// on every House's "Courses" (groceries) lists, and gives those items a
// first aisle guess — so autocomplete, favorites and the "Par rayon" view
// have the household's history to work with from the first day rather than
// only from items added after the upgrade.
//
// It runs as Go rather than SQL because a product is identified by
// catalog.Key(name) (accent and punctuation folding SQLite's ASCII-only
// lower() can't do) and the aisle guess is catalog.GuessAisle; the catalog
// keys the running server computes must match the ones seeded here, or
// every seeded product would be duplicated the first time it's added again.
//
// One done item counts as one purchase, dated by its updated_at (the last
// time it changed, normally its check-off): only an approximation, but the
// first real check-offs after the upgrade refine it. Items keep their
// updated_at, so the PWA's offline mirror accepts the aisle the next time it
// reads the list (db.js isStaleWrite only refuses strictly older rows).
func applySeedHouseProductsMigration(conn *sql.DB) error {
	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("beginning house_products seed transaction: %w", err)
	}
	defer tx.Rollback()

	seeds, itemAisles, err := readProductSeeds(tx)
	if err != nil {
		return err
	}
	for _, s := range seeds {
		if _, err := tx.Exec(`
			INSERT INTO house_products (house_id, name, name_key, aisle, add_count, purchase_count,
			                            first_purchased_at, last_purchased_at, last_added_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.houseID, s.name, s.key, nullIfEmpty(catalog.GuessAisle(s.name)), s.addCount, s.purchaseCount,
			nullIfEmpty(s.firstPurchase), nullIfEmpty(s.lastPurchase), nullIfEmpty(s.lastAdded),
		); err != nil {
			return fmt.Errorf("seeding house product %q: %w", s.name, err)
		}
	}
	for id, aisle := range itemAisles {
		if _, err := tx.Exec(`UPDATE items SET aisle = ? WHERE id = ?`, aisle, id); err != nil {
			return fmt.Errorf("setting aisle of item %d: %w", id, err)
		}
	}

	if err := setSchemaVersion(tx, 31); err != nil {
		return err
	}
	return tx.Commit()
}

// productSeed is one house_products row migration 31 inserts.
type productSeed struct {
	houseID                     int64
	key, name                   string
	addCount, purchaseCount     int
	firstPurchase, lastPurchase string
	lastAdded                   string
}

// readProductSeeds reads every groceries item without an aisle and returns
// the catalog rows they add up to (in first-seen order) and each item's
// guessed aisle. The cursor is closed before the caller writes, on the
// transaction's one connection.
func readProductSeeds(tx *sql.Tx) ([]*productSeed, map[int64]string, error) {
	rows, err := tx.Query(`
		SELECT items.id, lists.house_id, items.title, items.done, items.created_at, items.updated_at
		FROM items JOIN lists ON lists.id = items.list_id
		WHERE lists.type = 'groceries' AND lists.house_id IS NOT NULL AND items.aisle IS NULL
		ORDER BY items.created_at, items.id`)
	if err != nil {
		return nil, nil, fmt.Errorf("reading groceries items: %w", err)
	}
	defer rows.Close()

	type seedKey struct {
		houseID int64
		key     string
	}
	byKey := map[seedKey]*productSeed{}
	var seeds []*productSeed
	itemAisles := map[int64]string{}
	for rows.Next() {
		var (
			id, houseID          int64
			title                string
			done                 int
			createdAt, updatedAt string
		)
		if err := rows.Scan(&id, &houseID, &title, &done, &createdAt, &updatedAt); err != nil {
			return nil, nil, fmt.Errorf("scanning groceries item: %w", err)
		}
		if aisle := catalog.GuessAisle(title); aisle != "" {
			itemAisles[id] = aisle
		}
		key := catalog.Key(title)
		if key == "" {
			continue
		}
		s, ok := byKey[seedKey{houseID, key}]
		if !ok {
			s = &productSeed{houseID: houseID, key: key}
			byKey[seedKey{houseID, key}] = s
			seeds = append(seeds, s)
		}
		// Rows come oldest first, so the last spelling seen is the latest.
		s.name = title
		s.addCount++
		s.lastAdded = createdAt
		if done != 0 {
			s.purchaseCount++
			if s.firstPurchase == "" || updatedAt < s.firstPurchase {
				s.firstPurchase = updatedAt
			}
			if updatedAt > s.lastPurchase {
				s.lastPurchase = updatedAt
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterating groceries items: %w", err)
	}
	return seeds, itemAisles, nil
}

// nullIfEmpty maps "" to a SQL NULL, for optional TEXT columns.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
