-- Migration 28: price tracking — what the background price scans observe
-- over time, the last movement of each item's own price, per-user price
-- alert preferences, and an in-app inbox for price changes (see
-- internal/handlers/price_tracking.go).
--
-- price_history is every distinct price observed on an item's own url: the
-- scrape that fills a new item's price in, then each background scan pass
-- whose price differs from the item's last recorded one
-- (db.RecordPriceObservation). It records what the page showed, whether
-- or not that price was applied to the item (a manually entered price is
-- never overwritten by a scan).
CREATE TABLE price_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    item_id     INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    price       REAL NOT NULL,
    recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX idx_price_history_item ON price_history(item_id, recorded_at);

-- previous_price/price_changed_at describe the last time a background scan
-- moved items.price (db.UpdateItemPriceFromScan): the price before, and
-- when. The list view colours a recent drop green and a recent increase
-- red from these two. A manual price or url edit clears both (db.UpdateItem),
-- since the user's own edit is not a movement at the store.
ALTER TABLE items ADD COLUMN previous_price REAL;
ALTER TABLE items ADD COLUMN price_changed_at TEXT;

-- Paramètres → "Alertes de prix", settable via PATCH /api/v1/me:
--
--   price_alerts_enabled             master switch: off silences every price
--                                    alert (drops, increases, target price,
--                                    better deals). Scans keep running.
--   price_drop_alerts_enabled        an item's price went down, or a lower
--                                    price was found elsewhere
--   price_increase_alerts_enabled    an item's price went up
--   price_change_indicators_enabled  colour recently moved prices in lists
--
-- Drops and indicators are on by default; increases are off, since every
-- price tracked would otherwise notify in both directions.
ALTER TABLE users ADD COLUMN price_alerts_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN price_drop_alerts_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN price_increase_alerts_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN price_change_indicators_enabled INTEGER NOT NULL DEFAULT 1;

-- price_notifications is the in-app inbox for price changes, one row per
-- recipient: written for everyone who wants that kind of alert, whether or
-- not they have a push subscription, so turning push off (or never turning
-- it on) still leaves the alert waiting in the 🔔 drawer.
-- kind: 'drop'/'increase' (the item's price moved), 'deal' (a lower price
-- was found elsewhere and is waiting in price_alerts to be accepted),
-- 'expired' (the Dealabs deal the item's url points to has expired).
CREATE TABLE price_notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_id    INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('drop', 'increase', 'deal', 'expired')),
    old_price  REAL NOT NULL,
    new_price  REAL NOT NULL,
    source_url TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    read_at    TEXT
);

CREATE INDEX idx_price_notifications_user ON price_notifications(user_id, read_at);
CREATE INDEX idx_price_notifications_item ON price_notifications(item_id);
