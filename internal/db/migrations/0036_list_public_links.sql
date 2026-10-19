-- Migration 36: public share links for lists. A House member can make one
-- secret link per list (/public/lists/<token>) that opens it to anyone who
-- has the link, with no Trakka account, in one of four access modes:
-- 'read' (view only), 'add_only' (view and add), 'deposit_only' (add without
-- seeing the list: a wish list or idea box) and 'full_edit' (view, add,
-- check, uncheck, rename, change quantities) — see
-- internal/handlers/public_links.go and docs/API.md#public-share-links.
--
-- One row per list at most (list_id is the primary key): regenerating the
-- link replaces the token, which is how a leaked link is revoked without
-- turning sharing off. Unlike sessions, calendar_feed_tokens and api_tokens,
-- the token is stored as is, not hashed: the owner chose to be able to copy
-- the link again from the share modal at any time, which a hash can't give
-- back. A database leak or a backup copy therefore hands out working public
-- links (never more than the links themselves open). is_enabled suspends a
-- link without forgetting it; expires_at (UTC, RFC 3339, NULL for never) ends it on its own. Either
-- way the public routes then answer 404, the same as for a token that never
-- existed. last_used_at is bumped at most once a minute by the public
-- routes, so the share modal can show whether anyone is using the link.
CREATE TABLE list_public_links (
    list_id      INTEGER PRIMARY KEY REFERENCES lists(id) ON DELETE CASCADE,
    share_token  TEXT    NOT NULL UNIQUE,
    access_mode  TEXT    NOT NULL CHECK (access_mode IN ('read', 'add_only', 'deposit_only', 'full_edit')),
    is_enabled   INTEGER NOT NULL DEFAULT 1 CHECK (is_enabled IN (0, 1)),
    created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at   TEXT,
    last_used_at TEXT
);
