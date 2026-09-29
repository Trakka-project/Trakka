// Package db owns all SQLite access: connection setup, versioned schema
// migrations, and parameterized queries for lists and items. No other
// package imports database/sql directly.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGO
)

// DB wraps a *sql.DB configured for Trakka's usage pattern.
type DB struct {
	conn *sql.DB
	// path is the database file Open was given — needed by SnapshotTo
	// (backup.go), which reads the file through a second, read-only
	// connection of its own rather than the shared one above.
	path string
}

// Open opens (creating if necessary) the SQLite database at path, applies
// pragmas suited for a single-writer embedded workload, and brings the
// schema up to date via the versioned migration engine in migrate.go (see
// "Evolving the schema" in docs/DATABASE.md for the full design). logger is
// used only to record which migrations ran, if any — the common case, an
// already up-to-date database, logs nothing here.
func Open(path string, logger *slog.Logger) (*DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
		path,
	)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}

	// SQLite allows only one writer at a time, and the pure-Go driver gains
	// nothing from pooling multiple readers here; a single shared
	// connection avoids SQLITE_BUSY errors under concurrent requests.
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(0)

	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("pinging sqlite: %w", err)
	}

	if err := migrate(conn, path, logger); err != nil {
		return nil, fmt.Errorf("migrating schema: %w", err)
	}

	if err := ensureDefaultHouse(conn); err != nil {
		return nil, fmt.Errorf("seeding default house: %w", err)
	}

	return &DB{conn: conn, path: path}, nil
}

// ensureDefaultHouse guarantees at least one house always exists and that
// every list belongs to one, so that "house_id" can be treated as required
// at the application level even though the column itself is nullable (added
// via ALTER TABLE, which cannot retroactively enforce NOT NULL). It creates
// a default house named "Maison Principale" the first time no house exists
// at all (a fresh database, or an existing one migrating from before houses
// existed), and reassigns any list still missing a house_id — e.g. rows
// created before this migration — to the oldest house. Runs on every
// startup; a no-op once every list already has a house.
func ensureDefaultHouse(conn *sql.DB) error {
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM houses`).Scan(&count); err != nil {
		return fmt.Errorf("counting houses: %w", err)
	}
	if count == 0 {
		if _, err := conn.Exec(`INSERT INTO houses (name) VALUES ('Maison Principale')`); err != nil {
			return fmt.Errorf("inserting default house: %w", err)
		}
	}

	if _, err := conn.Exec(
		`UPDATE lists SET house_id = (SELECT id FROM houses ORDER BY id ASC LIMIT 1) WHERE house_id IS NULL`,
	); err != nil {
		return fmt.Errorf("backfilling lists.house_id: %w", err)
	}
	return nil
}

// Close releases the underlying database connection. Safe to call once
// during graceful shutdown.
func (d *DB) Close() error {
	return d.conn.Close()
}

// Ping checks that the database is reachable; used by the /healthz handler.
func (d *DB) Ping(ctx context.Context) error {
	return d.conn.PingContext(ctx)
}
