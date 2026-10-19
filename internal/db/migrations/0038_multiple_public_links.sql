-- Migration 38: several public share links per list. Until now list_id was
-- list_public_links' primary key, so a list had at most one link; a House
-- may want one per audience instead ("Famille" with full_edit, "Invités"
-- with deposit_only...), each revoked on its own. Each link gets its own id
-- and an optional label naming it (1 to 50 characters, NULL for none; the
-- handler trims it and refuses longer ones before this CHECK ever sees it).
--
-- Migration 37 has already brought the table to migration 36's final shape
-- on every database, so a plain rebuild is enough: every existing link is
-- carried over (in creation order, so ids follow it), token and settings
-- included. Nothing references list_public_links, so dropping and renaming
-- needs none of migration 6's foreign key precautions.
CREATE TABLE list_public_links_v38 (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    list_id      INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    share_token  TEXT    NOT NULL UNIQUE,
    label        TEXT    CHECK (label IS NULL OR length(label) BETWEEN 1 AND 50),
    access_mode  TEXT    NOT NULL CHECK (access_mode IN ('read', 'add_only', 'deposit_only', 'full_edit')),
    is_enabled   INTEGER NOT NULL DEFAULT 1 CHECK (is_enabled IN (0, 1)),
    created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at   TEXT,
    last_used_at TEXT
);

INSERT INTO list_public_links_v38 (list_id, share_token, access_mode, is_enabled, created_by, created_at, expires_at, last_used_at)
SELECT list_id, share_token, access_mode, is_enabled, created_by, created_at, expires_at, last_used_at
FROM list_public_links
ORDER BY created_at, list_id;

DROP TABLE list_public_links;
ALTER TABLE list_public_links_v38 RENAME TO list_public_links;

CREATE INDEX idx_list_public_links_list ON list_public_links (list_id);
