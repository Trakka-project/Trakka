-- Migration 42: what Fluv's purchase endpoint learned to take besides the
-- amount — the account, category and need/want a purchase is filed under,
-- and its shipping fee (internal/fluv, docs/FLUV.md).
--
-- items.shipping_price: what delivering the item cost, in euros, for the
-- whole line (not per unit, unlike price/actual_price). NULL: none. Fluv
-- records it as a second expense beside the item's.
ALTER TABLE items ADD COLUMN shipping_price REAL;

-- fluv_list_defaults: how a user's purchases from a list are filed in their
-- Fluv. Per user and list, not per list: a shared list's members each send
-- to their own Fluv, whose account and category ids mean nothing to
-- another's. '' means "Fluv decides": the account linked to Trakka, the
-- category its rules find, the need/want that category implies.
CREATE TABLE fluv_list_defaults (
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    list_id     INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    account_id  TEXT    NOT NULL DEFAULT '',
    category_id TEXT    NOT NULL DEFAULT '',
    budget_type TEXT    NOT NULL DEFAULT '' CHECK (budget_type IN ('', 'need', 'want')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (user_id, list_id)
);

CREATE INDEX idx_fluv_list_defaults_list ON fluv_list_defaults (list_id);

-- The outbox keeps what a purchase was sent with, taken when it was queued:
-- changing a list's defaults later doesn't move what Fluv already holds.
ALTER TABLE fluv_purchases ADD COLUMN account_id TEXT NOT NULL DEFAULT '';
ALTER TABLE fluv_purchases ADD COLUMN category_id TEXT NOT NULL DEFAULT '';
ALTER TABLE fluv_purchases ADD COLUMN budget_type TEXT NOT NULL DEFAULT '';
ALTER TABLE fluv_purchases ADD COLUMN shipping_minor INTEGER NOT NULL DEFAULT 0 CHECK (shipping_minor >= 0);
