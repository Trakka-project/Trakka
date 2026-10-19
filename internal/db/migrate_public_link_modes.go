package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// listPublicLinksDDL is list_public_links as migration 36 defines it today,
// under a name of the caller's choosing.
const listPublicLinksDDL = `CREATE TABLE %s (
    list_id      INTEGER PRIMARY KEY REFERENCES lists(id) ON DELETE CASCADE,
    share_token  TEXT    NOT NULL UNIQUE,
    access_mode  TEXT    NOT NULL CHECK (access_mode IN ('read', 'add_only', 'deposit_only', 'full_edit')),
    is_enabled   INTEGER NOT NULL DEFAULT 1 CHECK (is_enabled IN (0, 1)),
    created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ','now')),
    expires_at   TEXT,
    last_used_at TEXT
)`

// applyPublicLinkModesMigration is migration 37: it brings list_public_links
// to the schema migration 36 has today, on a database that applied one of
// the two earlier drafts of migration 36 written while the feature was being
// built (on its branch, before it shipped) — which the version engine,
// seeing user_version 36, would otherwise never touch again:
//
//   - access_mode limited to 'read'/'write' (the four modes 'read',
//     'add_only', 'deposit_only', 'full_edit' came later): every 'write' link
//     becomes 'full_edit', what 'write' allowed, and keeps its token;
//   - and, in the very first draft, token_hash (a SHA-256 hash) instead of
//     share_token (the token itself, so the share modal can show the link
//     again): those links can't be carried over — a hash doesn't give the
//     token back — so they are dropped, and their lists' members generate a
//     new link from the share modal.
//
// A database that applied migration 36 as it is now already has the right
// table: only the version is recorded. Nothing references list_public_links,
// so rebuilding it is a plain create-copy-drop-rename in one transaction,
// with none of migration 6's foreign key precautions.
func applyPublicLinkModesMigration(conn *sql.DB) error {
	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("beginning list_public_links repair transaction: %w", err)
	}
	defer tx.Rollback()

	current, err := publicLinksTableState(tx)
	if err != nil {
		return err
	}
	if !current.upToDate() {
		if err := rebuildPublicLinksTable(tx, current); err != nil {
			return err
		}
	}

	if err := setSchemaVersion(tx, 37); err != nil {
		return err
	}
	return tx.Commit()
}

// publicLinksTable describes list_public_links as a database actually has it.
type publicLinksTable struct {
	createSQL     string
	hasShareToken bool
}

// upToDate reports whether the table already allows the four access modes
// and stores the token itself.
func (t publicLinksTable) upToDate() bool {
	return t.hasShareToken && strings.Contains(t.createSQL, "'deposit_only'") && strings.Contains(t.createSQL, "'full_edit'")
}

func publicLinksTableState(tx *sql.Tx) (publicLinksTable, error) {
	var t publicLinksTable
	err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'list_public_links'`).Scan(&t.createSQL)
	if errors.Is(err, sql.ErrNoRows) {
		return t, errors.New("list_public_links is missing: migration 36 should have created it")
	}
	if err != nil {
		return t, fmt.Errorf("reading list_public_links' schema: %w", err)
	}
	rows, err := tx.Query(`SELECT name FROM pragma_table_info('list_public_links')`)
	if err != nil {
		return t, fmt.Errorf("reading list_public_links' columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return t, fmt.Errorf("scanning list_public_links' columns: %w", err)
		}
		if name == "share_token" {
			t.hasShareToken = true
		}
	}
	if err := rows.Err(); err != nil {
		return t, fmt.Errorf("iterating list_public_links' columns: %w", err)
	}
	return t, nil
}

// rebuildPublicLinksTable replaces list_public_links with the current
// schema, carrying over every link whose token it has (see
// applyPublicLinkModesMigration).
func rebuildPublicLinksTable(tx *sql.Tx, current publicLinksTable) error {
	const staging = "list_public_links_v37"
	if _, err := tx.Exec(fmt.Sprintf(listPublicLinksDDL, staging)); err != nil {
		return fmt.Errorf("creating %s: %w", staging, err)
	}
	if current.hasShareToken {
		// 'write' is the old name of 'full_edit'; 'read' kept its meaning.
		if _, err := tx.Exec(`
			INSERT INTO ` + staging + ` (list_id, share_token, access_mode, is_enabled, created_by, created_at, expires_at, last_used_at)
			SELECT list_id, share_token,
			       CASE access_mode WHEN 'write' THEN 'full_edit' WHEN 'read' THEN 'read'
			                        WHEN 'add_only' THEN 'add_only' WHEN 'deposit_only' THEN 'deposit_only'
			                        WHEN 'full_edit' THEN 'full_edit' ELSE 'read' END,
			       is_enabled, created_by, created_at, expires_at, last_used_at
			FROM list_public_links`); err != nil {
			return fmt.Errorf("copying public links into %s: %w", staging, err)
		}
	}
	if _, err := tx.Exec(`DROP TABLE list_public_links`); err != nil {
		return fmt.Errorf("dropping the old list_public_links: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE ` + staging + ` RENAME TO list_public_links`); err != nil {
		return fmt.Errorf("renaming %s: %w", staging, err)
	}
	return nil
}
