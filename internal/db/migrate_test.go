package db

import (
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLoadMigrationsSequential(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("expected at least one migration")
	}
	for i, m := range migrations {
		if m.version != i+1 {
			t.Fatalf("migration at index %d has version %d, want %d", i, m.version, i+1)
		}
	}
	// Migration 6 (widening lists.type's CHECK constraint) is the one
	// documented exception that must run as Go code, not an embedded .sql
	// file — see migrate_list_types.go.
	if migrations[5].name != "list_types_widen" {
		t.Fatalf("expected migration 6 to be list_types_widen, got %q", migrations[5].name)
	}
}

// TestMigrateFreshDatabase confirms a brand new database, migrated from
// scratch, ends up fully up to date and with a schema that actually works —
// including the lists.type CHECK constraint having already been widened by
// migration 6 by the time this returns, since a fresh install applies every
// migration in order rather than starting from the pre-widened shape.
func TestMigrateFreshDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "trakka.db")
	d, err := Open(dbPath, discardLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var version int
	if err := d.conn.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("reading user_version: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	latest := migrations[len(migrations)-1].version
	if version != latest {
		t.Fatalf("user_version = %d, want %d (latest migration)", version, latest)
	}

	for _, table := range []string{
		"houses", "lists", "items", "users", "sessions", "house_members",
		"price_alerts", "custom_categories", "system_settings", "space_shares", "list_shares",
		"backup_runs",
	} {
		var name string
		err := d.conn.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("expected table %q to exist after migration: %v", table, err)
		}
	}

	// A default house should have been seeded by ensureDefaultHouse, run
	// right after migrate() inside Open().
	var houseCount int
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM houses`).Scan(&houseCount); err != nil {
		t.Fatalf("counting houses: %v", err)
	}
	if houseCount == 0 {
		t.Fatal("expected ensureDefaultHouse to have seeded at least one house")
	}

	// The lists.type CHECK constraint must already be the widened set from
	// migration 6, not the original ('shopping', 'todo') pair from
	// migration 1 — proves the migrations actually ran in order rather than
	// e.g. migration 6 being skipped.
	if _, err := d.conn.Exec(`INSERT INTO lists (name, type, house_id) VALUES ('Test', 'groceries', (SELECT id FROM houses LIMIT 1))`); err != nil {
		t.Fatalf("inserting a 'groceries' list should succeed once migration 6 has widened the CHECK constraint: %v", err)
	}
}

// TestMigrateAdoptsPreExistingDatabase simulates a database that was
// already in full use before this migration system existed, with a schema
// shape already exactly at legacySchemaVersion (nothing left for the old
// unversioned code to have added), but PRAGMA user_version still at its
// default of 0 (nothing has ever set it). Open() must adopt it at
// legacySchemaVersion without attempting to re-run any migration SQL up to
// that point — which would otherwise fail outright (e.g. "duplicate column
// name") — and, critically, without touching any existing data. This test
// only exercises the pure-adoption step in isolation (no migration newer
// than legacySchemaVersion pending); see
// TestMigrateAdoptsLegacyDatabaseWithPendingMigrations for the realistic
// case this tree is actually in today, where adoption is immediately
// followed by real migration SQL for everything newer.
func TestMigrateAdoptsPreExistingDatabase(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	latest := migrations[len(migrations)-1].version
	if latest > legacySchemaVersion {
		t.Skip("this tree has migrations newer than legacySchemaVersion, so adoption always falls through to applying them — see TestMigrateAdoptsLegacyDatabaseWithPendingMigrations")
	}

	dbPath := filepath.Join(t.TempDir(), "trakka.db")
	seedLegacyDatabase(t, dbPath)

	d, err := Open(dbPath, discardLogger())
	if err != nil {
		t.Fatalf("Open on a pre-existing database should adopt it, not fail: %v", err)
	}
	defer d.Close()

	var version int
	if err := d.conn.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("reading user_version: %v", err)
	}
	if version != legacySchemaVersion {
		t.Fatalf("user_version = %d, want %d (adopted at legacySchemaVersion)", version, legacySchemaVersion)
	}

	var name string
	if err := d.conn.QueryRow(`SELECT name FROM houses WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("pre-existing house row should survive adoption: %v", err)
	}
	if name != "Pre-existing House" {
		t.Fatalf("house name = %q, want unchanged %q", name, "Pre-existing House")
	}

	// No backup should have been taken for this path: nothing pending
	// means nothing about adoption modifies the schema.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dbPath), "backups")); !os.IsNotExist(err) {
		t.Fatalf("expected no backups/ directory when nothing was pending, stat error: %v", err)
	}
}

// seedLegacyDatabase builds a database at exactly legacySchemaVersion's
// shape (by actually running migrations 1..legacySchemaVersion, the same
// SQL a real database would have accumulated) and then resets
// PRAGMA user_version back to 0 — simulating a database whose schema was
// brought fully up to date by the old, unversioned schema.sql +
// addColumnIfMissing() code, which never touched user_version at all.
func seedLegacyDatabase(t *testing.T, dbPath string) {
	t.Helper()

	seed, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("opening seed connection: %v", err)
	}
	defer seed.Close()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range migrations {
		if m.version > legacySchemaVersion {
			break
		}
		if err := m.apply(seed); err != nil {
			t.Fatalf("seeding migration %04d_%s: %v", m.version, m.name, err)
		}
	}

	if _, err := seed.Exec(`INSERT INTO houses (id, name) VALUES (1, 'Pre-existing House')`); err != nil {
		t.Fatalf("seeding a house row: %v", err)
	}
	if _, err := seed.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("resetting user_version to simulate pre-engine code: %v", err)
	}
}

// TestMigrateAdoptsLegacyDatabaseWithPendingMigrations is a direct
// regression test for a real bug found live: a database last touched by
// pre-engine code (schema shape frozen at legacySchemaVersion) had its
// first-ever run under an engine-aware binary happen well after migrations
// newer than legacySchemaVersion already existed in the source tree.
// hasExistingSchema's adoption used to stamp such a database at
// migrations[len-1].version (whatever "latest" was *today*) and return
// immediately — skipping every migration between legacySchemaVersion and
// latest without ever running their SQL, while user_version claimed the
// database was fully current. This test builds exactly that scenario and
// asserts the database ends up actually current, not just labeled as such.
func TestMigrateAdoptsLegacyDatabaseWithPendingMigrations(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	latest := migrations[len(migrations)-1].version
	if latest <= legacySchemaVersion {
		t.Skip("no migrations newer than legacySchemaVersion exist yet in this tree — nothing to regress against")
	}

	dbPath := filepath.Join(t.TempDir(), "trakka.db")
	seedLegacyDatabase(t, dbPath)

	d, err := Open(dbPath, discardLogger())
	if err != nil {
		t.Fatalf("Open on a legacy database with pending migrations should succeed: %v", err)
	}
	defer d.Close()

	var version int
	if err := d.conn.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("reading user_version: %v", err)
	}
	if version != latest {
		t.Fatalf("user_version = %d, want %d — migrations after legacySchemaVersion were never actually applied", version, latest)
	}

	// users.keep_last_page comes from migration 10, strictly after
	// legacySchemaVersion (9) — its presence is direct proof the pending
	// migrations actually ran, not just that user_version was bumped.
	if _, err := d.conn.Exec(`SELECT keep_last_page FROM users LIMIT 1`); err != nil {
		t.Fatalf("expected users.keep_last_page to exist after adopting a legacy database with pending migrations: %v", err)
	}

	var name string
	if err := d.conn.QueryRow(`SELECT name FROM houses WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("pre-existing house row should survive adoption + pending migrations: %v", err)
	}
	if name != "Pre-existing House" {
		t.Fatalf("house name = %q, want unchanged %q", name, "Pre-existing House")
	}

	// Unlike a pure adoption with nothing pending, this path does apply
	// real migration SQL to a database that might hold real data, so it
	// must be preceded by a safety backup.
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(dbPath), "backups"))
	if err != nil {
		t.Fatalf("expected a backups/ directory since pending migrations were actually applied: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one backup file")
	}
}

// TestBackupBeforeMigrationCreatesSnapshot exercises backupBeforeMigration
// directly: it should create backups/<name>.db next to dbPath, and that
// snapshot should contain the data that existed at backup time.
func TestBackupBeforeMigrationCreatesSnapshot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "trakka.db")
	conn, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("opening connection: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("creating table: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO t (v) VALUES ('before-migration')`); err != nil {
		t.Fatalf("inserting row: %v", err)
	}

	if err := backupBeforeMigration(conn, dbPath, 3, 4, discardLogger()); err != nil {
		t.Fatalf("backupBeforeMigration: %v", err)
	}

	backupDir := filepath.Join(filepath.Dir(dbPath), "backups")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("reading backup directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one backup file, got %d", len(entries))
	}

	backupConn, err := sql.Open("sqlite", "file:"+filepath.Join(backupDir, entries[0].Name())+"?mode=ro")
	if err != nil {
		t.Fatalf("opening backup file: %v", err)
	}
	defer backupConn.Close()

	var v string
	if err := backupConn.QueryRow(`SELECT v FROM t WHERE id = 1`).Scan(&v); err != nil {
		t.Fatalf("reading row from backup: %v", err)
	}
	if v != "before-migration" {
		t.Fatalf("backup row v = %q, want %q", v, "before-migration")
	}
}

func TestHasExistingSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "trakka.db")
	conn, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("opening connection: %v", err)
	}
	defer conn.Close()

	legacy, err := hasExistingSchema(conn)
	if err != nil {
		t.Fatalf("hasExistingSchema on empty db: %v", err)
	}
	if legacy {
		t.Fatal("a genuinely empty database should not be reported as having an existing schema")
	}

	if _, err := conn.Exec(`CREATE TABLE anything (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("creating table: %v", err)
	}
	legacy, err = hasExistingSchema(conn)
	if err != nil {
		t.Fatalf("hasExistingSchema after creating a table: %v", err)
	}
	if !legacy {
		t.Fatal("a database with a user table should be reported as having an existing schema")
	}
}
