package db

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"trakka/internal/models"
)

// TestSnapshotAndRestoreRoundTrip drives the full hot-backup/live-restore
// cycle the encrypted-backup feature is built on: snapshot a live database,
// keep writing to it, then restore the snapshot over the still-open
// connection and confirm the post-snapshot write is gone, the pre-snapshot
// data is back, and the connection is still usable (and still in WAL mode)
// with no reopen.
func TestSnapshotAndRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := openTestDB(t)

	before := "before@example.com"
	hash := "x"
	if _, err := d.CreateUser(ctx, before, &hash, nil, nil, before); err != nil {
		t.Fatalf("creating user: %v", err)
	}

	snap := filepath.Join(t.TempDir(), "snap.db")
	if err := d.SnapshotTo(ctx, snap); err != nil {
		t.Fatalf("SnapshotTo: %v", err)
	}

	after := "after@example.com"
	if _, err := d.CreateUser(ctx, after, &hash, nil, nil, after); err != nil {
		t.Fatalf("creating user after snapshot: %v", err)
	}

	if _, err := PrepareRestoreFile(ctx, snap, logger); err != nil {
		t.Fatalf("PrepareRestoreFile: %v", err)
	}
	if err := d.RestoreFrom(ctx, snap); err != nil {
		t.Fatalf("RestoreFrom: %v", err)
	}

	if _, err := d.GetUserByEmail(ctx, before); err != nil {
		t.Fatalf("expected pre-snapshot user to survive the restore: %v", err)
	}
	if _, err := d.GetUserByEmail(ctx, after); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected post-snapshot user to be gone after restore, got err=%v", err)
	}

	var mode string
	if err := d.conn.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("reading journal mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode after restore = %q, want wal", mode)
	}

	// The connection must still accept writes after the restore.
	if _, err := d.CreateUser(ctx, "later@example.com", &hash, nil, nil, "later"); err != nil {
		t.Fatalf("writing after restore: %v", err)
	}
}

// TestPrepareRestoreFileMigratesOlderSchema confirms a backup taken at an
// older schema version is brought up to date on the staged copy, before it
// ever reaches the live database.
func TestPrepareRestoreFileMigratesOlderSchema(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := openTestDB(t)

	snap := filepath.Join(t.TempDir(), "old.db")
	if err := d.SnapshotTo(ctx, snap); err != nil {
		t.Fatalf("SnapshotTo: %v", err)
	}
	// Simulate a backup from before migration 20 by dropping its table and
	// rewinding user_version on the staged copy.
	old, err := Open(snap, logger)
	if err != nil {
		t.Fatalf("opening snapshot: %v", err)
	}
	if _, err := old.conn.Exec(`DROP TABLE backup_runs`); err != nil {
		t.Fatalf("dropping backup_runs: %v", err)
	}
	if _, err := old.conn.Exec(`PRAGMA user_version = 19`); err != nil {
		t.Fatalf("rewinding user_version: %v", err)
	}
	_ = old.Close()

	from, err := PrepareRestoreFile(ctx, snap, logger)
	if err != nil {
		t.Fatalf("PrepareRestoreFile: %v", err)
	}
	if from != 19 {
		t.Fatalf("reported original version %d, want 19", from)
	}
	if err := d.RestoreFrom(ctx, snap); err != nil {
		t.Fatalf("RestoreFrom: %v", err)
	}
	if _, err := d.InsertBackupRun(ctx, models.BackupRun{
		Source: "manual", Status: "success", StartedAt: "2026-01-01T00:00:00.000Z", FinishedAt: "2026-01-01T00:00:01.000Z",
	}); err != nil {
		t.Fatalf("backup_runs should exist again after restoring a migrated v19 backup: %v", err)
	}
}

// TestPrepareRestoreFileRejects covers the three refusal cases: not SQLite
// at all, SQLite but not Trakka, and a schema newer than this binary.
func TestPrepareRestoreFileRejects(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()

	garbage := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(garbage, []byte("definitely not a sqlite file, just some bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareRestoreFile(ctx, garbage, logger); !errors.Is(err, ErrRestoreCorrupt) {
		t.Fatalf("garbage file: got %v, want ErrRestoreCorrupt", err)
	}

	other := filepath.Join(dir, "other.db")
	o, err := sql.Open("sqlite", "file:"+other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = o.Close()
	if _, err := PrepareRestoreFile(ctx, other, logger); !errors.Is(err, ErrRestoreNotTrakka) {
		t.Fatalf("non-Trakka database: got %v, want ErrRestoreNotTrakka", err)
	}

	newer := filepath.Join(dir, "newer.db")
	n, err := Open(newer, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.conn.Exec(`PRAGMA user_version = 9999`); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	if _, err := PrepareRestoreFile(ctx, newer, logger); !errors.Is(err, ErrRestoreTooNew) {
		t.Fatalf("newer schema: got %v, want ErrRestoreTooNew", err)
	}
}

// TestBackupRunsQueries covers insert/list ordering, the since-filter the
// scheduler uses, and the latest-success lookup.
func TestBackupRunsQueries(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if _, err := d.LatestSuccessfulBackupRun(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty table: got %v, want ErrNotFound", err)
	}

	for _, r := range []models.BackupRun{
		{Source: "scheduled", Status: "success", StartedAt: "2026-09-27T03:00:00.000Z", FinishedAt: "2026-09-27T03:00:02.000Z"},
		{Source: "manual", Status: "failed", StartedAt: "2026-09-28T10:00:00.000Z", FinishedAt: "2026-09-28T10:00:01.000Z", Error: "boom"},
		{Source: "scheduled", Status: "failed", StartedAt: "2026-09-29T03:00:00.000Z", FinishedAt: "2026-09-29T03:00:01.000Z", Error: "boom"},
	} {
		if _, err := d.InsertBackupRun(ctx, r); err != nil {
			t.Fatalf("InsertBackupRun: %v", err)
		}
	}

	runs, err := d.ListBackupRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].StartedAt != "2026-09-29T03:00:00.000Z" {
		t.Fatalf("expected 3 runs newest first, got %+v", runs)
	}

	since, err := d.ListBackupRunsSince(ctx, "scheduled", "2026-09-28T00:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(since) != 1 || since[0].Status != "failed" {
		t.Fatalf("expected only the 09-29 scheduled run, got %+v", since)
	}

	last, err := d.LatestSuccessfulBackupRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if last.StartedAt != "2026-09-27T03:00:00.000Z" {
		t.Fatalf("latest success = %+v", last)
	}
}
