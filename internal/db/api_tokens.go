package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// APIToken describes one of a user's API tokens without the token itself,
// which is never stored (only its hash is — see migration 35).
type APIToken struct {
	ID        int64
	Name      string
	CreatedAt string
	// LastUsedAt is when the token last authenticated a request, to the
	// minute (see UseAPIToken), "" if never.
	LastUsedAt string
}

// apiTokenUseResolution is how stale last_used_at must be before
// UseAPIToken writes it again: an integration polling every 30 seconds
// would otherwise turn every read into a write.
const apiTokenUseResolution = "-60 seconds"

// CreateAPIToken stores a new token for userID under name, keyed by
// tokenHash (the SHA-256 hash of the raw token — never the token itself).
func (d *DB) CreateAPIToken(ctx context.Context, userID int64, name, tokenHash string) (*APIToken, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO api_tokens (user_id, name, token_hash) VALUES (?, ?, ?)`,
		userID, name, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("inserting API token for user %d: %w", userID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("reading new API token id: %w", err)
	}
	t := &APIToken{}
	err = d.conn.QueryRowContext(ctx,
		`SELECT id, name, created_at, COALESCE(last_used_at, '') FROM api_tokens WHERE id = ?`, id,
	).Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt)
	if err != nil {
		return nil, fmt.Errorf("reading back API token %d: %w", id, err)
	}
	return t, nil
}

// ListAPITokens returns userID's tokens, oldest first.
func (d *DB) ListAPITokens(ctx context.Context, userID int64) ([]*APIToken, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT id, name, created_at, COALESCE(last_used_at, '') FROM api_tokens
		 WHERE user_id = ? ORDER BY id ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("querying API tokens for user %d: %w", userID, err)
	}
	defer rows.Close()

	tokens := []*APIToken{}
	for rows.Next() {
		t := &APIToken{}
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scanning API token row: %w", err)
		}
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating API token rows: %w", err)
	}
	return tokens, nil
}

// CountAPITokens returns how many tokens userID has.
func (d *DB) CountAPITokens(ctx context.Context, userID int64) (int, error) {
	var n int
	if err := d.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_tokens WHERE user_id = ?`, userID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting API tokens for user %d: %w", userID, err)
	}
	return n, nil
}

// DeleteAPIToken revokes token id, which stops working at once. Returns
// ErrNotFound if userID has no such token — including another user's.
func (d *DB) DeleteAPIToken(ctx context.Context, userID, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("deleting API token %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected deleting API token: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UseAPIToken resolves a token hash to its user and records the use in
// last_used_at, at most once per apiTokenUseResolution. Returns ErrNotFound
// for an unknown hash — including a revoked token or one removed with its
// user.
func (d *DB) UseAPIToken(ctx context.Context, tokenHash string) (int64, error) {
	var id, userID int64
	err := d.conn.QueryRowContext(ctx,
		`SELECT id, user_id FROM api_tokens WHERE token_hash = ?`, tokenHash,
	).Scan(&id, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("querying API token: %w", err)
	}
	if _, err := d.conn.ExecContext(ctx, `
		UPDATE api_tokens SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND (last_used_at IS NULL OR last_used_at < strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))`,
		id, apiTokenUseResolution); err != nil {
		return 0, fmt.Errorf("recording API token %d use: %w", id, err)
	}
	return userID, nil
}
