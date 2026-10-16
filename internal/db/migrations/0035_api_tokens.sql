-- Migration 35: personal API tokens, for integrations that call the JSON
-- API with no browser session — the Home Assistant integration first
-- (custom_components/trakka, docs/HOME_ASSISTANT.md). A token is sent as
-- "Authorization: Bearer trk_..." and RequireSession resolves it to its
-- user, but only on the list and item routes an integration needs (see
-- internal/handlers/api_tokens.go's apiTokenRoutes): never the account,
-- sharing, admin or token-management routes.
--
-- Several rows per user (one per integration, each named by the user so it
-- can be told apart and revoked on its own), unlike calendar_feed_tokens'
-- single row. Only the SHA-256 hash of the token is stored, exactly like
-- sessions.id, so a database leak or a backup copy never hands out working
-- tokens; the token itself is shown once, when it is created.
-- last_used_at is bumped at most once a minute by the requests the token
-- authenticates, so the settings screen can show whether an integration is
-- actually polling without a write on every request.
CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    token_hash   TEXT    NOT NULL UNIQUE,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_used_at TEXT
);

CREATE INDEX idx_api_tokens_user ON api_tokens (user_id);
