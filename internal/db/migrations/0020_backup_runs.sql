-- Migration 20 ("Encrypted WebDAV backups"): one row per finished backup
-- attempt — manual ("Sauvegarder maintenant") or scheduled — backing the
-- admin console's "Sauvegardes" history and its non-blocking failure/
-- staleness alerts (see internal/backup). A run is only ever inserted once
-- it has finished, never while it is in progress: the snapshot a run
-- uploads is taken from this very database, so an "in progress" row would
-- otherwise be captured inside the backup itself and resurface, stuck
-- forever "running", after that backup is restored. The in-progress state
-- lives in memory instead (internal/backup.Service). The backup
-- configuration itself (WebDAV URL/credentials, schedule, retention) lives
-- in system_settings, like every other admin-editable setting.
CREATE TABLE backup_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source      TEXT NOT NULL CHECK (source IN ('manual', 'scheduled')),
    status      TEXT NOT NULL CHECK (status IN ('success', 'failed')),
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    file_name   TEXT NOT NULL DEFAULT '',
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    error       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_backup_runs_started_at ON backup_runs (started_at);
