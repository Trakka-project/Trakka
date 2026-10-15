-- Migration 30: quick shopping lists — store aisles on items, and a
-- per-House product catalog that remembers what the household buys (see
-- internal/handlers/products.go and internal/catalog).
--
-- items.aisle is one of internal/catalog.Aisles ("produce", "dairy", ...),
-- or NULL when none is known. The list view can group a list by it, in the
-- order a supermarket walk passes the aisles. Kept on the item (not only on
-- the catalog product) so one list can file a product differently from the
-- household's usual choice without changing it for everyone.
ALTER TABLE items ADD COLUMN aisle TEXT;

-- house_products is the household's catalog: one row per distinct product
-- name ever added to one of the House's "Courses" (groceries) lists. Rows
-- outlive the items they came from — the catalog is the history that
-- autocomplete, favorites and "time to buy it again" suggestions read.
--
--   name               the latest spelling someone typed, shown as is
--   name_key           internal/catalog.Key(name): lowercase, unaccented,
--                      punctuation folded — what two names are compared by.
--                      Computed in Go, never in SQL (SQLite's lower() only
--                      folds ASCII), hence migration 31 seeding in Go too.
--   aisle              the household's aisle for it (learned from the latest
--                      explicit choice, or a first guess)
--   is_favorite        pinned to the quick-add panel by a member
--   add_count          how many times it was added to a list
--   purchase_count     how many times it was checked off a list
--   first/last_purchased_at  the first and latest check-off (UTC ISO-8601),
--                      which give its average buying interval
CREATE TABLE house_products (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    house_id           INTEGER NOT NULL REFERENCES houses(id) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    name_key           TEXT NOT NULL,
    aisle              TEXT,
    is_favorite        INTEGER NOT NULL DEFAULT 0,
    add_count          INTEGER NOT NULL DEFAULT 0,
    purchase_count     INTEGER NOT NULL DEFAULT 0,
    first_purchased_at TEXT,
    last_purchased_at  TEXT,
    last_added_at      TEXT,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (house_id, name_key)
);

CREATE INDEX idx_house_products_favorite ON house_products(house_id) WHERE is_favorite = 1;

-- house_product_barcodes files scanned EAN/UPC codes (digits only, a
-- 12-digit UPC-A stored as its 13-digit EAN form, see
-- internal/validate.Barcode) under a catalog product: several codes per
-- product (the 1 L bottle and the pack of six are both "Lait"), one product
-- per code within a House — scanning a known code and adding it under
-- another name moves it there.
CREATE TABLE house_product_barcodes (
    house_id   INTEGER NOT NULL REFERENCES houses(id) ON DELETE CASCADE,
    barcode    TEXT NOT NULL,
    product_id INTEGER NOT NULL REFERENCES house_products(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (house_id, barcode)
);

CREATE INDEX idx_house_product_barcodes_product ON house_product_barcodes(product_id);
