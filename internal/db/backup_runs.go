package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"trakka/internal/models"
)

// backupRunsKept caps how many backup_runs rows are retained — the admin
// console only ever shows the most recent handful, and the alert logic only
// needs to look back to the last success. Pruned on every insert.
const backupRunsKept = 100

const backupRunColumns = `id, source, status, started_at, finished_at, file_name, size_bytes, error`

func scanBackupRun(row interface{ Scan(dest ...any) error }) (models.BackupRun, error) {
	var r models.BackupRun
	err := row.Scan(&r.ID, &r.Source, &r.Status, &r.StartedAt, &r.FinishedAt, &r.FileName, &r.SizeBytes, &r.Error)
	return r, err
}

// InsertBackupRun records one finished backup attempt and prunes the table
// back down to backupRunsKept rows.
func (d *DB) InsertBackupRun(ctx context.Context, run models.BackupRun) (models.BackupRun, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO backup_runs (source, status, started_at, finished_at, file_name, size_bytes, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.Source, run.Status, run.StartedAt, run.FinishedAt, run.FileName, run.SizeBytes, run.Error,
	)
	if err != nil {
		return models.BackupRun{}, fmt.Errorf("inserting backup run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.BackupRun{}, fmt.Errorf("reading backup run id: %w", err)
	}
	run.ID = id

	if _, err := d.conn.ExecContext(ctx,
		`DELETE FROM backup_runs WHERE id NOT IN (SELECT id FROM backup_runs ORDER BY started_at DESC, id DESC LIMIT ?)`,
		backupRunsKept,
	); err != nil {
		return run, fmt.Errorf("pruning backup runs: %w", err)
	}
	return run, nil
}

// ListBackupRuns returns the most recent backup attempts, newest first.
func (d *DB) ListBackupRuns(ctx context.Context, limit int) ([]models.BackupRun, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT `+backupRunColumns+` FROM backup_runs ORDER BY started_at DESC, id DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying backup runs: %w", err)
	}
	defer rows.Close()

	runs := []models.BackupRun{}
	for rows.Next() {
		r, err := scanBackupRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning backup run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// ListBackupRunsSince returns every attempt from source that started at or
// after since (same text format as started_at), newest first — the
// scheduler's "have I already handled this slot" check.
func (d *DB) ListBackupRunsSince(ctx context.Context, source, since string) ([]models.BackupRun, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT `+backupRunColumns+` FROM backup_runs WHERE source = ? AND started_at >= ? ORDER BY started_at DESC, id DESC`,
		source, since,
	)
	if err != nil {
		return nil, fmt.Errorf("querying backup runs: %w", err)
	}
	defer rows.Close()

	runs := []models.BackupRun{}
	for rows.Next() {
		r, err := scanBackupRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning backup run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// LatestSuccessfulBackupRun returns the most recent successful attempt of
// either source, or ErrNotFound if there has never been one.
func (d *DB) LatestSuccessfulBackupRun(ctx context.Context) (models.BackupRun, error) {
	r, err := scanBackupRun(d.conn.QueryRowContext(ctx,
		`SELECT `+backupRunColumns+` FROM backup_runs WHERE status = 'success' ORDER BY started_at DESC, id DESC LIMIT 1`,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return models.BackupRun{}, ErrNotFound
	}
	if err != nil {
		return models.BackupRun{}, fmt.Errorf("querying latest successful backup run: %w", err)
	}
	return r, nil
}
