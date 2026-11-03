-- Migration 41: sending purchases to Fluv, the personal finance app
-- (internal/fluv, docs/FLUV.md). When a user checks an item off a purchase
-- list, Trakka reports it to their Fluv, which records it as an expense;
-- un-checking it takes it back.
--
-- fluv_integrations: one row per user who connected their Fluv. The URL is
-- Fluv's purchase endpoint, as Fluv's settings show it; pinned_ips is the
-- comma-separated list of the only addresses Trakka will ever connect to for
-- it (the IP lock: the URL's host name is never resolved). The token is the
-- one Fluv made for its link with Trakka — an outbound credential, so it
-- cannot be hashed like Trakka's own tokens: it is sealed (AES-256-GCM) with
-- a subkey of the instance key file, which lives outside the database, so a
-- copy of the database alone never holds a working Fluv token.
CREATE TABLE fluv_integrations (
    user_id         INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    endpoint_url    TEXT    NOT NULL,
    pinned_ips      TEXT    NOT NULL,
    token_sealed    TEXT    NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_success_at TEXT,
    last_error_code TEXT,
    last_error_at   TEXT
);

-- fluv_purchases: the outbox. One row per time an item was bought, under
-- the reference (ref) Fluv knows it by. desired is what Fluv should hold
-- ('record': the expense, 'cancel': nothing), synced what it is known to
-- hold ('none', 'recorded', 'cancelled'); the background sender works on the
-- rows where the two disagree, retrying with a backoff, and flags failed
-- the ones that need the user (a revoked token, an address Fluv refuses).
-- A row only ever goes from record to cancel: checking the item off again
-- makes a new row with a new ref. in_flight marks the row being sent, so a
-- handler never rewrites what is on the wire. item_id is SET NULL when the
-- item is deleted: deleting a bought item is tidying up, not a refund.
CREATE TABLE fluv_purchases (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_id         INTEGER REFERENCES items(id) ON DELETE SET NULL,
    ref             TEXT    NOT NULL UNIQUE,
    title           TEXT    NOT NULL,
    store           TEXT    NOT NULL DEFAULT '',
    quantity        INTEGER NOT NULL,
    amount_minor    INTEGER NOT NULL CHECK (amount_minor > 0),
    purchased_at    TEXT    NOT NULL,
    purchased_on    TEXT    NOT NULL,
    desired         TEXT    NOT NULL DEFAULT 'record' CHECK (desired IN ('record', 'cancel')),
    synced          TEXT    NOT NULL DEFAULT 'none' CHECK (synced IN ('none', 'recorded', 'cancelled')),
    failed          INTEGER NOT NULL DEFAULT 0 CHECK (failed IN (0, 1)),
    in_flight       INTEGER NOT NULL DEFAULT 0 CHECK (in_flight IN (0, 1)),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT    NOT NULL,
    last_status     INTEGER,
    last_error_code TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX idx_fluv_purchases_item ON fluv_purchases (item_id);
CREATE INDEX idx_fluv_purchases_user ON fluv_purchases (user_id);
-- The sender's queue: only the rows still to send.
CREATE INDEX idx_fluv_purchases_due ON fluv_purchases (next_attempt_at)
    WHERE failed = 0 AND NOT ((desired = 'record' AND synced = 'recorded') OR (desired = 'cancel' AND synced = 'cancelled'));
