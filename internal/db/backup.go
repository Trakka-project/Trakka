package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	sqlite "modernc.org/sqlite"
)

// Sentinel errors PrepareRestoreFile returns for a decrypted backup that
// must not replace the live database — internal/handlers maps each one to a
// 400 with an actionable message rather than a generic 500.
var (
	// ErrRestoreNotTrakka means the file is a readable SQLite database but
	// not a Trakka one (no users table), e.g. an unrelated .db file.
	ErrRestoreNotTrakka = errors.New("the backup is not a Trakka database")
	// ErrRestoreCorrupt means SQLite could not open the file at all, or
	// PRAGMA integrity_check reported damage.
	ErrRestoreCorrupt = errors.New("the backup database is corrupt")
	// ErrRestoreTooNew means the backup was produced by a newer Trakka
	// release, with migrations this binary doesn't know about — restoring
	// it would leave the schema at a version this code can't reason about.
	ErrRestoreTooNew = errors.New("the backup was made by a newer version of Trakka")
)

// LatestSchemaVersion reports the newest migration version this binary
// knows about (see migrate.go).
func LatestSchemaVersion() (int, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	if len(migrations) == 0 {
		return 0, errors.New("no migrations found")
	}
	return migrations[len(migrations)-1].version, nil
}

// SnapshotTo writes a consistent, compacted copy of the live database to
// destPath (which must not exist yet) via VACUUM INTO — the same hot-backup
// statement backupBeforeMigration already relies on. It runs on a second,
// short-lived, read-only connection rather than the shared one in d.conn:
// with the database in WAL mode, a reader on its own connection sees one
// fixed snapshot of the file while writers on the main connection carry on
// untouched, so a backup never stalls ordinary requests for its duration
// (running it on d.conn, capped at one connection, would queue every other
// query behind it instead).
func (d *DB) SnapshotTo(ctx context.Context, destPath string) error {
	conn, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", d.path))
	if err != nil {
		return fmt.Errorf("opening snapshot connection: %w", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)

	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, destPath); err != nil {
		return fmt.Errorf("VACUUM INTO %s: %w", destPath, err)
	}
	return nil
}

// PrepareRestoreFile vets a decrypted backup staged at path before it is
// allowed anywhere near the live database, and brings its schema up to date
// in place, so that RestoreFrom only ever copies in a file this binary
// already fully understands — the live database is never left, even for a
// moment, holding an older schema than the running code expects. It
// returns the schema version the backup was originally made at.
//
// The file is opened with trusted_schema off: its content is authenticated
// (it only decrypts under the instance's backup key, see internal/backup),
// but there is no reason to let any trigger or view in it call a function
// with side effects while its migrations run here.
func PrepareRestoreFile(ctx context.Context, path string, logger *slog.Logger) (int, error) {
	conn, err := sql.Open("sqlite", fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=trusted_schema(OFF)", path,
	))
	if err != nil {
		return 0, fmt.Errorf("opening staged backup: %w", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)

	// integrity_check returns a single "ok" row for a healthy file, or one
	// row per problem found otherwise; a file that isn't SQLite at all
	// fails right here with "file is not a database".
	var integrity string
	if err := conn.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrRestoreCorrupt, err)
	}
	if integrity != "ok" {
		return 0, fmt.Errorf("%w: %s", ErrRestoreCorrupt, integrity)
	}

	var usersTable int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'users'`,
	).Scan(&usersTable); err != nil {
		return 0, fmt.Errorf("inspecting staged backup: %w", err)
	}
	if usersTable == 0 {
		return 0, ErrRestoreNotTrakka
	}

	var version int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("reading staged backup schema version: %w", err)
	}
	latest, err := LatestSchemaVersion()
	if err != nil {
		return 0, err
	}
	if version > latest {
		return 0, fmt.Errorf("%w (schema v%d, this server supports up to v%d)", ErrRestoreTooNew, version, latest)
	}

	if err := migrateSchema(conn, logger, nil); err != nil {
		return 0, fmt.Errorf("migrating staged backup: %w", err)
	}
	if err := ensureDefaultHouse(conn); err != nil {
		return 0, fmt.Errorf("seeding default house in staged backup: %w", err)
	}
	return version, nil
}

// SnapshotBeforeRestore keeps a plaintext safety copy of the live database
// under the same <DB_PATH dir>/backups/ directory backupBeforeMigration
// uses, right before RestoreFrom overwrites it — so a restore of the wrong
// file is itself recoverable by an operator with shell access to the
// volume. Returns the path written.
func (d *DB) SnapshotBeforeRestore(ctx context.Context) (string, error) {
	backupDir := filepath.Join(filepath.Dir(d.path), "backups")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return "", fmt.Errorf("creating backup directory %s: %w", backupDir, err)
	}
	dest := filepath.Join(backupDir, fmt.Sprintf("trakka-pre-restore-%s.db", time.Now().UTC().Format("20060102T150405Z")))
	if err := d.SnapshotTo(ctx, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// restorer is the method modernc.org/sqlite's driver connection exposes
// for SQLite's online backup API run in reverse (sqlite3_backup_init with
// the live connection as the destination). It isn't part of any
// database/sql interface, so it's reached through sql.Conn.Raw below.
type restorer interface {
	NewRestore(srcURI string) (*sqlite.Backup, error)
}

// RestoreFrom replaces the entire content of the live database with the
// SQLite file at srcPath (already vetted and migrated by
// PrepareRestoreFile), using SQLite's online backup API over the one shared
// connection in d.conn. Holding that connection for the duration is what
// makes this safe with no restart: every other query in the process simply
// waits in database/sql's pool until the copy is complete, then sees the
// restored content — there is never a moment where a request can observe a
// half-copied file, and no *sql.DB has to be swapped out from under the
// handlers and background workers that share this one.
func (d *DB) RestoreFrom(ctx context.Context, srcPath string) error {
	c, err := d.conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquiring database connection: %w", err)
	}
	defer func() { _ = c.Close() }()

	err = c.Raw(func(driverConn any) error {
		r, ok := driverConn.(restorer)
		if !ok {
			return errors.New("sqlite driver does not support online restore")
		}
		bk, err := r.NewRestore(fmt.Sprintf("file:%s?mode=ro", srcPath))
		if err != nil {
			return fmt.Errorf("starting restore: %w", err)
		}
		for {
			more, err := bk.Step(-1)
			if err != nil {
				_ = bk.Finish()
				return fmt.Errorf("copying pages: %w", err)
			}
			if !more {
				break
			}
		}
		return bk.Finish()
	})
	if err != nil {
		return err
	}

	// The restored pages carry whatever journal mode the source file was
	// written in; re-assert WAL so the live database keeps the concurrency
	// behavior Open() configured (and SnapshotTo depends on).
	var mode string
	if err := c.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		return fmt.Errorf("re-enabling WAL after restore: %w", err)
	}
	return nil
}
