-- Migration 24: a per-user preference for whether Web Push notifications
-- vibrate the device. internal/handlers.sendToUsers reads it for each
-- subscription's owner and only then adds a `vibrate` pattern to the
-- payload; static/sw.js shows a notification without one as silent, as it
-- always did before this migration. Defaults to enabled (1), per the
-- feature's own "on by default" spec.
ALTER TABLE users ADD COLUMN vibrate_on_notification INTEGER NOT NULL DEFAULT 1;
