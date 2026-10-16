-- Migration 32: the purchase cycle with budget tracking, and list templates
-- (see internal/handlers/shopping_templates.go and internal/budget).
--
-- An item's purchase status is planned → to buy → bought. "Bought" is
-- items.done, unchanged and still what every other feature (recurrence,
-- reminders, the offline mirror) reads; is_planned splits the items not
-- bought yet into "planned" (added while estimating, not confirmed as
-- needed) and "to buy". The API reports the three as purchase_status
-- (models.PurchaseStatusOf). is_planned is cleared when an item is bought,
-- so a checked-then-unchecked item comes back as "to buy".
ALTER TABLE items ADD COLUMN is_planned INTEGER NOT NULL DEFAULT 0;

-- actual_price is what one unit actually cost, entered when the item is
-- bought — a unit price like items.price, which stays the estimate it is
-- compared with. NULL means "cost what was estimated". Only a bought item
-- carries one: every write that takes an item out of "bought" clears it
-- (internal/handlers, db.ReactivateItem, db.ActivateNextOccurrence), since
-- the next purchase of it is a new one.
ALTER TABLE items ADD COLUMN actual_price REAL;

-- shopping_templates are a House's own list templates ("Modèle personnalisé
-- de la maison"), saved from one of its lists. The built-in templates
-- (cleaning, travel, moving, back to school, home maintenance) are not
-- stored here: they live in internal/shoptemplates, translated, so a new
-- version ships with the code. list_type is the type a list created from
-- the template gets by default (models.ValidListTypes).
CREATE TABLE shopping_templates (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    house_id   INTEGER NOT NULL REFERENCES houses(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    icon       TEXT NOT NULL DEFAULT '',
    list_type  TEXT NOT NULL DEFAULT 'groceries',
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX idx_shopping_templates_house ON shopping_templates(house_id);

-- shopping_template_items are a template's items, in list order: what a
-- list created from it starts with. estimated_price is a unit price, copied
-- into the new item's price; aisle is one of internal/catalog.Aisles or
-- NULL.
CREATE TABLE shopping_template_items (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id     INTEGER NOT NULL REFERENCES shopping_templates(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    quantity        INTEGER NOT NULL DEFAULT 1,
    estimated_price REAL,
    aisle           TEXT,
    position        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_shopping_template_items_template ON shopping_template_items(template_id, position);
