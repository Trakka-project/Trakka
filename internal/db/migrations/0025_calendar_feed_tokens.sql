-- Migration 25: personal calendar feed tokens. Each user can generate one
-- secret link (GET /api/v1/calendar/feed.ics?token=...) that a calendar
-- app (Nextcloud, Google Calendar, Apple Calendar, Thunderbird) subscribes
-- to without going through a login form or SSO — see
-- internal/handlers/calendar_feed.go and docs/CALENDAR_EXPORT.md.
--
-- One row per user at most (user_id is the primary key): generating a new
-- link replaces the token, which is how a leaked link is revoked. Only the
-- SHA-256 hash of the token is stored, exactly like sessions.id, so a
-- database leak or a backup copy never hands out working feed links.
-- last_used_at is bumped by every successful feed fetch, so the settings
-- screen can show whether a calendar app is actually syncing.
CREATE TABLE calendar_feed_tokens (
    user_id      INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_used_at TEXT
);
