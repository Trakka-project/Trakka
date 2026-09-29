package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migration is one versioned, sequential schema change, tracked via
// SQLite's built-in PRAGMA user_version (an integer stored in the database
// file's own header — no extra table needed to record it). apply begins and
// commits (or rolls back) its own transaction, including bumping
// user_version on success, so a crash or error partway through a migration
// can never leave the schema and its recorded version disagreeing with each
// other.
type migration struct {
	version int
	name    string
	apply   func(conn *sql.DB) error
}

var migrationFilePattern = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// goMigrations holds the versions that must run as Go code rather than a
// plain embedded .sql file. Currently just migration 6 — see
// migrate_list_types.go for why widening a CHECK constraint can't be
// expressed as a single script under the generic sqlMigration wrapper
// below. Adding a *new* migration normally needs nothing here: just drop a
// new NNNN_description.sql file in internal/db/migrations/.
var goMigrations = map[int]func(conn *sql.DB) error{
	6: applyListTypesWidenMigration,
}

// loadMigrations returns every migration in ascending version order,
// sourced from the embedded .sql files in internal/db/migrations/ plus the
// Go-coded exceptions in goMigrations. It fails loudly on anything that
// looks like a mistake (a misnamed file, a duplicate or non-sequential
// version) rather than silently skipping it — a gap or reordering here
// would be a schema bug waiting to happen.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("reading embedded migrations directory: %w", err)
	}

	byVersion := make(map[int]migration, len(entries)+len(goMigrations))

	for _, entry := range entries {
		match := migrationFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("migration file %q doesn't match the NNNN_name.sql naming convention", entry.Name())
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("parsing version from migration file %q: %w", entry.Name(), err)
		}
		if _, isGo := goMigrations[version]; isGo {
			return nil, fmt.Errorf("version %d has both a Go migration and a migration file (%q) — pick one", version, entry.Name())
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading migration file %q: %w", entry.Name(), err)
		}
		byVersion[version] = migration{version: version, name: match[2], apply: sqlMigration(version, string(sqlBytes))}
	}

	for version, apply := range goMigrations {
		if _, exists := byVersion[version]; exists {
			return nil, fmt.Errorf("duplicate migration version %d", version)
		}
		byVersion[version] = migration{version: version, name: "list_types_widen", apply: apply}
	}

	versions := make([]int, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	migrations := make([]migration, 0, len(versions))
	for i, v := range versions {
		if v != i+1 {
			return nil, fmt.Errorf("migration versions must be sequential starting at 1 with no gaps; found version %d out of order", v)
		}
		migrations = append(migrations, byVersion[v])
	}
	return migrations, nil
}

// sqlMigration builds the apply function for an ordinary embedded .sql
// migration: run its whole script and record the new version, both inside
// one transaction. Unlike the old addColumnIfMissing()-guarded approach
// this replaces, the script itself needs no existence checks (no
// `IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`) — versioning already
// guarantees it runs at most once per database, ever.
func sqlMigration(version int, sqlText string) func(conn *sql.DB) error {
	return func(conn *sql.DB) error {
		tx, err := conn.Begin()
		if err != nil {
			return fmt.Errorf("beginning transaction: %w", err)
		}
		defer tx.Rollback()

		if _, err := tx.Exec(sqlText); err != nil {
			return err
		}
		if err := setSchemaVersion(tx, version); err != nil {
			return err
		}
		return tx.Commit()
	}
}

// setSchemaVersion is a thin fmt.Sprintf wrapper, not a parameterized
// statement: SQLite's PRAGMA syntax doesn't accept a `?` bound parameter
// for the value, so unlike every other statement in this package this one
// can't use a placeholder. version is always an int derived from this
// package's own embedded migration filenames (matched against
// migrationFilePattern above), never user input, so this is the same
// "hard-coded call-site constant" safety reasoning the old
// addColumnIfMissing() used for table/column identifiers, just applied to
// an integer instead of a name.
func setSchemaVersion(tx *sql.Tx, version int) error {
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("recording schema version %d: %w", version, err)
	}
	return nil
}

// legacySchemaVersion is the fixed version a pre-existing, pre-engine
// database (see hasExistingSchema) is adopted at — never "whatever the
// latest migration happens to be today". Migrations 1 through
// legacySchemaVersion reconstruct exactly what the old, unversioned
// schema.sql + addColumnIfMissing() code already applied to any database
// that ever started successfully (see the package doc comment above
// loadMigrations' sibling functions for that history); every migration
// numbered higher is a genuine post-engine schema change that such a
// database has never seen, no matter how long ago it predates the engine or
// how many migrations have since been added to the source tree. This must
// never be bumped — it's a historical fact about the moment the versioned
// engine replaced the old system, not a moving target. Getting this wrong
// silently strands a legacy database missing every migration after it,
// while user_version claims it's fully up to date — exactly the bug this
// constant exists to prevent (discovered live: a database last touched by
// pre-engine code, first opened by an engine-aware binary long after
// migrations 10-14 already existed in the tree, jumped straight to
// user_version=14 without ever running 10-14's SQL).
const legacySchemaVersion = 9

// migrate brings the database's schema up to the latest known version. On
// every startup:
//
//  1. A brand new, empty database applies every migration in order, from a
//     blank slate — this reproduces the real historical schema evolution
//     one step at a time, ending in the same shape as any other case below.
//  2. A database that predates this migration system (PRAGMA user_version
//     is still 0, SQLite's default for a file that's never had it set, but
//     tables already exist) is adopted at legacySchemaVersion without
//     running any migration SQL for versions up to that point — see
//     hasExistingSchema for why that much is always safe. Anything newer
//     than legacySchemaVersion is a real post-engine migration this
//     database has genuinely never run, so it falls through to case 3
//     below in the very same call, exactly like an already-versioned
//     database that's simply behind.
//  3. A database at a known version below latest (whether just adopted in
//     case 2 or already-versioned to begin with) applies only the
//     migrations newer than its current version, preceded by a hot backup
//     (see backupBeforeMigration) — the ordinary "pull a new release" path
//     this system exists for.
func migrate(conn *sql.DB, dbPath string, logger *slog.Logger) error {
	return migrateSchema(conn, logger, func(fromVersion, toVersion int) error {
		return backupBeforeMigration(conn, dbPath, fromVersion, toVersion, logger)
	})
}

// migrateSchema is migrate's engine, with the pre-migration backup step
// injected rather than hard-wired: the live database always passes
// backupBeforeMigration (via migrate above), while a decrypted backup
// staged for restore (see PrepareRestoreFile) passes nil — that file is
// already a disposable copy, and snapshotting it into /data/backups/ would
// only leave a stray duplicate of old data behind.
func migrateSchema(conn *sql.DB, logger *slog.Logger, backup func(fromVersion, toVersion int) error) error {
	migrations, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("loading migrations: %w", err)
	}
	if len(migrations) == 0 {
		return fmt.Errorf("no migrations found — internal/db/migrations must not be empty")
	}
	latest := migrations[len(migrations)-1].version

	var current int
	if err := conn.QueryRow(`PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}

	if current == 0 {
		legacy, err := hasExistingSchema(conn)
		if err != nil {
			return fmt.Errorf("checking for a pre-existing schema: %w", err)
		}
		if legacy {
			if _, err := conn.Exec(fmt.Sprintf("PRAGMA user_version = %d", legacySchemaVersion)); err != nil {
				return fmt.Errorf("adopting pre-existing database at schema version %d: %w", legacySchemaVersion, err)
			}
			current = legacySchemaVersion
			logger.Info("adopted pre-existing database into versioned schema migrations", "version", legacySchemaVersion)
		}
	}

	if current >= latest {
		return nil
	}

	if current > 0 && backup != nil {
		if err := backup(current, latest); err != nil {
			return fmt.Errorf("backing up database before migration: %w", err)
		}
	}

	for _, m := range migrations[current:] {
		if err := m.apply(conn); err != nil {
			return fmt.Errorf("applying migration %04d_%s: %w", m.version, m.name, err)
		}
		logger.Info("applied database migration", "version", m.version, "name", m.name)
	}
	return nil
}

// hasExistingSchema reports whether the database already has at least one
// user table — i.e. this file was already in use before the migration
// system existed, as opposed to a genuinely fresh, empty database.
// sqlite_sequence is excluded: SQLite creates that table itself the first
// time any AUTOINCREMENT table is populated, so its presence alone says
// nothing about whether Trakka's own tables exist yet.
//
// Every startup of the old, unversioned code applied the equivalent of
// every migration in this package unconditionally — idempotent
// `CREATE TABLE/INDEX IF NOT EXISTS` plus addColumnIfMissing()-guarded
// `ALTER TABLE` — and Open() always returned an error rather than continue
// past any failure, so any database that has ever successfully started
// already has the full schema. Re-running e.g. an `ALTER TABLE ADD COLUMN`
// for a column that's already there would fail outright ("duplicate column
// name"), so this case must be detected and skipped, not replayed.
func hasExistingSchema(conn *sql.DB) (bool, error) {
	var count int
	err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// backupBeforeMigration snapshots the live database to a `backups/`
// directory next to dbPath (e.g. /data/backups/ for the default
// DB_PATH=/data/trakka.db) before applying any pending migration to a
// database that might already hold real data. `VACUUM INTO` — a single
// ordinary SQL statement, supported since SQLite 3.27 — produces a
// consistent, compacted snapshot of the live database into a brand new
// file; it's SQLite's own documented hot-backup mechanism, safe to run
// against a database that's open and in WAL mode, and needs nothing beyond
// database/sql (no CGO backup API, no extra dependency). It cannot run
// inside an explicit transaction, so it must happen here, before any
// migration below opens one.
func backupBeforeMigration(conn *sql.DB, dbPath string, fromVersion, toVersion int, logger *slog.Logger) error {
	backupDir := filepath.Join(filepath.Dir(dbPath), "backups")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return fmt.Errorf("creating backup directory %s: %w", backupDir, err)
	}

	backupPath := filepath.Join(backupDir, fmt.Sprintf(
		"trakka-v%d-to-v%d-%s.db", fromVersion, toVersion, time.Now().UTC().Format("20060102T150405Z"),
	))

	// VACUUM INTO's target does accept a bound parameter (verified against
	// modernc.org/sqlite directly), so this stays a normal parameterized
	// statement despite building a filesystem path — no string-building
	// into the SQL text itself, the same rule every other query in this
	// package follows.
	if _, err := conn.Exec(`VACUUM INTO ?`, backupPath); err != nil {
		return fmt.Errorf("VACUUM INTO %s: %w", backupPath, err)
	}

	logger.Info("backed up database before schema migration", "path", backupPath, "from_version", fromVersion, "to_version", toVersion)
	return nil
}
