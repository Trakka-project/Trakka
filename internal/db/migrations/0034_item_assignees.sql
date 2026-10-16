-- Migration 34 ("Task assignments & member views"): the House members a
-- task (any item) is assigned to — one, several or none.
--
-- A join table rather than an items.assigned_user_id column, because a
-- chore can be shared by several people ("Thomas & Sarah"), and rather than
-- a JSON array like items.labels, because an assignee is a reference to a
-- users row the database must keep valid: ON DELETE CASCADE on both sides
-- drops the assignment with the item or with the account. Leaving a House
-- (internal/db.RemoveHouseMember) removes that person's assignments on the
-- House's items in the same transaction — an assignee is always a current
-- member of the item's House, which the handlers check on every write.
--
-- A recurring task keeps the same items row from one occurrence to the
-- next (see internal/db.ActivateNextOccurrence), so its assignees carry
-- over to every occurrence with no copying.
--
-- The primary key (item_id, user_id) serves the per-item lookup every item
-- read runs (itemSelectColumns' assignees subquery); idx_item_assignees_user
-- serves the per-person deletes.
CREATE TABLE item_assignees (
    item_id    INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (item_id, user_id)
);

CREATE INDEX idx_item_assignees_user ON item_assignees (user_id);
