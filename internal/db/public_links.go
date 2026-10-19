package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ListPublicLink describes one of a list's public share links — a list can
// have several, one per audience (migration 38). Its token is stored as is,
// so the share modal can show the link again (see migration 36).
type ListPublicLink struct {
	ID     int64
	ListID int64
	Token  string
	// Label names the link in the share modal ("Famille", "Invités"...), ""
	// for none.
	Label string
	// AccessMode is "read", "add_only", "deposit_only" or "full_edit" (see
	// handlers.publicModeAllows).
	AccessMode string
	Enabled    bool
	CreatedAt  string
	// ExpiresAt is when the link stops working (UTC, RFC 3339), "" for never.
	ExpiresAt string
	// LastUsedAt is when the link last opened the list, to the minute (see
	// UseListPublicLink), "" if never.
	LastUsedAt string
}

// publicLinkUseResolution is how stale last_used_at must be before
// UseListPublicLink writes it again: an open public page polls for other
// people's changes, which would otherwise turn every read into a write.
const publicLinkUseResolution = "-60 seconds"

const publicLinkColumns = `id, list_id, share_token, COALESCE(label, ''), access_mode, is_enabled, created_at,
	COALESCE(expires_at, ''), COALESCE(last_used_at, '')`

func scanListPublicLink(row rowScanner) (*ListPublicLink, error) {
	l := &ListPublicLink{}
	var enabled int
	if err := row.Scan(&l.ID, &l.ListID, &l.Token, &l.Label, &l.AccessMode, &enabled, &l.CreatedAt, &l.ExpiresAt, &l.LastUsedAt); err != nil {
		return nil, err
	}
	l.Enabled = enabled != 0
	return l, nil
}

// CreateListPublicLink adds a public link to listID, next to any it already
// has. label is nil for none, expiresAt nil for a link that never expires.
func (d *DB) CreateListPublicLink(ctx context.Context, listID int64, token string, label *string, accessMode string, expiresAt *string, createdBy int64) (*ListPublicLink, error) {
	res, err := d.conn.ExecContext(ctx, `
		INSERT INTO list_public_links (list_id, share_token, label, access_mode, created_by, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		listID, token, label, accessMode, createdBy, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("inserting public link for list %d: %w", listID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("reading new public link id: %w", err)
	}
	return d.GetListPublicLink(ctx, listID, id)
}

// GetListPublicLink returns listID's public link linkID, or ErrNotFound if
// the list has no such link (including another list's link).
func (d *DB) GetListPublicLink(ctx context.Context, listID, linkID int64) (*ListPublicLink, error) {
	l, err := scanListPublicLink(d.conn.QueryRowContext(ctx,
		`SELECT `+publicLinkColumns+` FROM list_public_links WHERE id = ? AND list_id = ?`, linkID, listID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying public link %d of list %d: %w", linkID, listID, err)
	}
	return l, nil
}

// ListListPublicLinks returns every public link of listID, oldest first.
func (d *DB) ListListPublicLinks(ctx context.Context, listID int64) ([]*ListPublicLink, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT `+publicLinkColumns+` FROM list_public_links WHERE list_id = ? ORDER BY id ASC`, listID)
	if err != nil {
		return nil, fmt.Errorf("querying public links of list %d: %w", listID, err)
	}
	defer rows.Close()

	links := []*ListPublicLink{}
	for rows.Next() {
		l, err := scanListPublicLink(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning public link row: %w", err)
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating public link rows: %w", err)
	}
	return links, nil
}

// CountListPublicLinks returns how many public links listID has.
func (d *DB) CountListPublicLinks(ctx context.Context, listID int64) (int, error) {
	var n int
	if err := d.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM list_public_links WHERE list_id = ?`, listID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting public links of list %d: %w", listID, err)
	}
	return n, nil
}

// DeleteListPublicLink removes listID's public link linkID, so its token
// stops working for good; the list's other links are untouched. Returns
// ErrNotFound if the list has no such link (including another list's link).
func (d *DB) DeleteListPublicLink(ctx context.Context, listID, linkID int64) error {
	res, err := d.conn.ExecContext(ctx,
		`DELETE FROM list_public_links WHERE id = ? AND list_id = ?`, linkID, listID)
	if err != nil {
		return fmt.Errorf("deleting public link %d of list %d: %w", linkID, listID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected deleting public link: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UseListPublicLink resolves a public link token to its link and records the
// use in last_used_at (at most once a minute). Returns ErrNotFound for an
// unknown token — including a revoked one, or one removed with its list. A
// disabled or expired link is still returned, as is: whether it may open the
// list is the caller's call (see handlers.resolvePublicLink). A disabled one
// records no use.
func (d *DB) UseListPublicLink(ctx context.Context, token string) (*ListPublicLink, error) {
	l, err := scanListPublicLink(d.conn.QueryRowContext(ctx,
		`SELECT `+publicLinkColumns+` FROM list_public_links WHERE share_token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying public link: %w", err)
	}
	if !l.Enabled {
		return l, nil
	}
	if _, err := d.conn.ExecContext(ctx,
		`UPDATE list_public_links SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND (last_used_at IS NULL OR last_used_at < strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))`,
		l.ID, publicLinkUseResolution); err != nil {
		return nil, fmt.Errorf("recording use of public link %d: %w", l.ID, err)
	}
	return l, nil
}

// CountListItems returns how many items listID holds.
func (d *DB) CountListItems(ctx context.Context, listID int64) (int, error) {
	var n int
	if err := d.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM items WHERE list_id = ?`, listID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting items of list %d: %w", listID, err)
	}
	return n, nil
}
