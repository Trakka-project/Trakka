// Package backup implements Trakka's encrypted, off-site backups: a hot
// SQLite snapshot of the live database (see internal/db.SnapshotTo),
// encrypted server-side with a per-instance key (crypto.go), uploaded to a
// WebDAV folder (webdav.go) either on demand or on an automatic schedule
// (schedule.go), with old copies pruned by a retention count — plus the
// reverse path, restoring the live database from one of those files.
//
// Everything is streamed: neither a backup nor a restore ever holds more
// than one 64 KiB chunk of the database in memory, keeping the process
// within its <20 MB RAM target however large the database grows.
package backup

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// system_settings keys holding the backup configuration. Like every other
// admin-editable setting they live in the database — and so travel inside
// the backups themselves, which is what lets a restore onto a fresh
// instance bring the whole backup setup back with it.
const (
	settingWebDAVURL       = "backup_webdav_url"
	settingWebDAVUsername  = "backup_webdav_username"
	settingWebDAVPassword  = "backup_webdav_password" // #nosec G101 -- settings key name; the value is sealed with the backup key (see sealSecret)
	settingAutoEnabled     = "backup_auto_enabled"
	settingFrequency       = "backup_frequency"
	settingTime            = "backup_time"
	settingWeekday         = "backup_weekday"
	settingRetention       = "backup_retention"
	settingScheduleAnchor  = "backup_schedule_anchor"
	settingKeyAcknowledged = "backup_key_acknowledged"
)

// Limits and defaults.
const (
	defaultFrequency = FrequencyDaily
	defaultTime      = "03:00"
	defaultRetention = 7
	maxRetention     = 365
	maxCredentialLen = 1024

	// backupTimeout bounds one whole backup run (snapshot + upload +
	// retention cleanup).
	backupTimeout = 30 * time.Minute
	// probeTimeout bounds a connection test or a remote listing.
	probeTimeout = 30 * time.Second

	// A failed scheduled run is retried, at most maxScheduledAttempts times
	// per slot in total, no sooner than retryDelay after the previous try —
	// so a WebDAV server that is briefly unreachable at 03:00 doesn't cost
	// a whole day's backup.
	maxScheduledAttempts = 3
	retryDelay           = 30 * time.Minute

	// runsShown is how many past runs Status reports.
	runsShown = 10
)

// Run sources, stored in backup_runs.source.
const (
	SourceManual    = "manual"
	SourceScheduled = "scheduled"
)

// Alert codes, surfaced by Status for the admin console's non-blocking
// warning badge and messages.
const (
	AlertLastBackupFailed = "last_backup_failed"
	AlertNoRecentSuccess  = "no_recent_success"
	AlertKeyNotSaved      = "key_not_saved"
)

// Errors the handlers layer maps to specific HTTP statuses.
var (
	// ErrBusy: a backup or restore is already in progress.
	ErrBusy = errors.New("a backup or restore is already in progress")
	// ErrNotConfigured: no WebDAV URL has been saved yet.
	ErrNotConfigured = errors.New("backups are not configured: save a WebDAV URL first")
	// ErrNoKey: a restore was attempted without supplying a key, on an
	// instance that doesn't have one of its own either.
	ErrNoKey = errors.New("no backup key was supplied and this instance has none")
	// ErrInvalidRemoteName: a restore named a remote file that isn't a
	// Trakka backup file name.
	ErrInvalidRemoteName = errors.New("invalid backup file name")
)

// ValidationError is a configuration problem to show the admin as-is (400).
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// backupFilePattern matches the names this package gives backup files
// (see backupFileName). Retention and listing only ever look at files that
// match it, so anything else in the folder is never touched.
var backupFilePattern = regexp.MustCompile(`^trakka-backup-\d{8}T\d{6}Z\.tkb$`)

func backupFileName(t time.Time) string {
	return "trakka-backup-" + t.UTC().Format("20060102T150405Z") + ".tkb"
}

// timeLayout is the ISO-8601-UTC-with-milliseconds text format every *_at
// column in this schema uses (strftime('%Y-%m-%dT%H:%M:%fZ')).
const timeLayout = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(timeLayout, s)
	return t, err == nil
}

// Options configures a Service.
type Options struct {
	DB *db.DB
	// DBPath is the live database file (DB_PATH). The key file and the
	// staging directory live next to it, on the same volume.
	DBPath   string
	Logger   *slog.Logger
	Location *time.Location
	// AllowPrivateNetworks relaxes the WebDAV dial guard (see dialGuard);
	// BACKUP_WEBDAV_ALLOW_PRIVATE.
	AllowPrivateNetworks bool
}

// Service owns the backup configuration, the in-memory "operation in
// progress" state, and every backup/restore operation. Safe for concurrent
// use; at most one backup or restore runs at a time.
type Service struct {
	db           *db.DB
	logger       *slog.Logger
	loc          *time.Location
	allowPrivate bool
	keys         *keyStore
	stagingDir   string
	now          func() time.Time

	mu        sync.Mutex
	busyKind  string // "", "backup" or "restore"
	busySrc   string
	busySince time.Time
}

// New builds a Service and clears any staging files a previous process
// left behind (a crash or restart mid-backup).
func New(opts Options) *Service {
	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}
	dir := filepath.Dir(opts.DBPath)
	s := &Service{
		db:           opts.DB,
		logger:       opts.Logger,
		loc:          loc,
		allowPrivate: opts.AllowPrivateNetworks,
		keys:         &keyStore{path: filepath.Join(dir, KeyFileName)},
		// On the data volume, not /tmp: the container's /tmp is a small
		// in-memory tmpfs, and a database snapshot is as large as the
		// database itself.
		stagingDir: filepath.Join(dir, "backups", "staging"),
		now:        time.Now,
	}
	if err := os.RemoveAll(s.stagingDir); err != nil {
		s.logger.Warn("clearing backup staging directory", "error", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// Config is the stored backup configuration as the admin console sees it.
// The WebDAV password is write-only — only whether one is set is exposed,
// the same pattern as the OIDC client secret in the admin settings.
type Config struct {
	WebDAVURL         string `json:"webdav_url"`
	WebDAVUsername    string `json:"webdav_username"`
	WebDAVPasswordSet bool   `json:"webdav_password_set"`
	AutoEnabled       bool   `json:"auto_enabled"`
	Frequency         string `json:"frequency"`
	Time              string `json:"time"`
	Weekday           int    `json:"weekday"`
	Retention         int    `json:"retention"`

	sealedPassword  string
	anchor          time.Time
	keyAcknowledged string
}

func (c Config) configured() bool { return c.WebDAVURL != "" }

func (c Config) schedule() schedule {
	t, err := time.Parse("15:04", c.Time)
	if err != nil {
		t, _ = time.Parse("15:04", defaultTime)
	}
	return schedule{Frequency: c.Frequency, Hour: t.Hour(), Minute: t.Minute(), Weekday: time.Weekday(c.Weekday)}
}

func (s *Service) loadConfig(ctx context.Context) (Config, error) {
	stored, err := s.db.GetAllSettings(ctx)
	if err != nil {
		return Config{}, err
	}
	c := Config{
		WebDAVURL:       stored[settingWebDAVURL],
		WebDAVUsername:  stored[settingWebDAVUsername],
		sealedPassword:  stored[settingWebDAVPassword],
		AutoEnabled:     stored[settingAutoEnabled] == "true",
		Frequency:       stored[settingFrequency],
		Time:            stored[settingTime],
		Retention:       defaultRetention,
		keyAcknowledged: stored[settingKeyAcknowledged],
	}
	c.WebDAVPasswordSet = c.sealedPassword != ""
	if c.Frequency != FrequencyEvery12h && c.Frequency != FrequencyWeekly {
		c.Frequency = defaultFrequency
	}
	if _, err := time.Parse("15:04", c.Time); err != nil {
		c.Time = defaultTime
	}
	if n, err := strconv.Atoi(stored[settingWeekday]); err == nil && n >= 0 && n <= 6 {
		c.Weekday = n
	}
	if n, err := strconv.Atoi(stored[settingRetention]); err == nil && n >= 1 && n <= maxRetention {
		c.Retention = n
	}
	if t, ok := parseTime(stored[settingScheduleAnchor]); ok {
		c.anchor = t
	}
	return c, nil
}

// ConfigUpdate is a full replacement of the backup configuration.
// WebDAVPassword is only applied when non-empty (empty = keep the stored
// one), since the stored password is never sent back to the form.
type ConfigUpdate struct {
	WebDAVURL      string
	WebDAVUsername string
	WebDAVPassword string
	AutoEnabled    bool
	Frequency      string
	Time           string
	Weekday        int
	Retention      int
}

func (u *ConfigUpdate) normalize() error {
	if u.WebDAVURL != "" {
		parsed, err := parseWebDAVURL(u.WebDAVURL)
		if err != nil {
			return &ValidationError{err.Error()}
		}
		u.WebDAVURL = parsed.String()
	}
	if !validate.MaxLen(u.WebDAVUsername, maxCredentialLen) || !validate.MaxLen(u.WebDAVPassword, maxCredentialLen) {
		return &ValidationError{"the WebDAV username or password is too long"}
	}
	switch u.Frequency {
	case FrequencyEvery12h, FrequencyDaily, FrequencyWeekly:
	default:
		return &ValidationError{"frequency must be one of every_12h, daily, weekly"}
	}
	t, err := validate.TimeOfDay(u.Time)
	if err != nil || t == "" {
		return &ValidationError{"time must be in HH:MM (24h) format"}
	}
	u.Time = t
	if u.Weekday < 0 || u.Weekday > 6 {
		return &ValidationError{"weekday must be between 0 (Sunday) and 6 (Saturday)"}
	}
	if u.Retention < 1 || u.Retention > maxRetention {
		return &ValidationError{fmt.Sprintf("retention must be between 1 and %d backups", maxRetention)}
	}
	if u.AutoEnabled && u.WebDAVURL == "" {
		return &ValidationError{"a WebDAV URL is required to enable automatic backups"}
	}
	return nil
}

// SaveConfig validates and stores a new configuration. The instance key is
// generated here, the first time a WebDAV URL is saved, so that it can be
// downloaded before the first backup ever runs.
func (s *Service) SaveConfig(ctx context.Context, u ConfigUpdate) error {
	if err := u.normalize(); err != nil {
		return err
	}
	cur, err := s.loadConfig(ctx)
	if err != nil {
		return err
	}
	now := s.now()

	updates := map[string]string{
		settingWebDAVURL:      u.WebDAVURL,
		settingWebDAVUsername: u.WebDAVUsername,
		settingAutoEnabled:    strconv.FormatBool(u.AutoEnabled),
		settingFrequency:      u.Frequency,
		settingTime:           u.Time,
		settingWeekday:        strconv.Itoa(u.Weekday),
		settingRetention:      strconv.Itoa(u.Retention),
	}

	// Clearing the URL is how backups are switched off entirely; the stored
	// credentials go with it rather than lingering unused.
	if u.WebDAVURL == "" {
		updates[settingWebDAVPassword] = ""
	} else {
		key, err := s.keys.loadOrCreate(now)
		if err != nil {
			return err
		}
		if u.WebDAVPassword != "" {
			sealed, err := sealSecret(key, u.WebDAVPassword)
			if err != nil {
				return err
			}
			updates[settingWebDAVPassword] = sealed
		}
	}

	// (Re)arm the schedule from now whenever automatic backups are switched
	// on or their timing changes, so that a slot that already went by today
	// isn't treated as missed and run immediately — the first automatic
	// backup is the next slot from here on.
	scheduleChanged := u.Frequency != cur.Frequency || u.Time != cur.Time || u.Weekday != cur.Weekday
	if u.AutoEnabled && (!cur.AutoEnabled || scheduleChanged || cur.anchor.IsZero()) {
		updates[settingScheduleAnchor] = formatTime(now)
	}

	return s.db.SetSettings(ctx, updates)
}

// ---------------------------------------------------------------------------
// Status & alerts
// ---------------------------------------------------------------------------

// KeyInfo describes the instance key without revealing it.
type KeyInfo struct {
	Exists      bool   `json:"exists"`
	Fingerprint string `json:"fingerprint"`
	// Saved reports whether the admin has exported this exact key (by
	// fingerprint) at least once — until then, the console keeps warning
	// that backups made with it would be unrecoverable if the server's
	// own copy were lost.
	Saved bool `json:"saved"`
}

// Running describes the operation in progress, if any.
type Running struct {
	Kind   string `json:"kind"`   // "backup" or "restore"
	Source string `json:"source"` // for a backup: "manual" or "scheduled"
	Since  string `json:"since"`
}

// Alert is one non-blocking warning for the admin console.
type Alert struct {
	Code string `json:"code"`
	// At is the relevant moment: the failed run's start for
	// last_backup_failed, the last success (empty if never) for
	// no_recent_success.
	At    string `json:"at,omitempty"`
	Error string `json:"error,omitempty"`
	Days  int    `json:"days,omitempty"`
}

// Status is everything the admin console's "Sauvegardes" tab shows.
type Status struct {
	Config               Config             `json:"config"`
	Key                  KeyInfo            `json:"key"`
	Running              *Running           `json:"running"`
	LastRun              *models.BackupRun  `json:"last_run"`
	LastSuccessAt        string             `json:"last_success_at,omitempty"`
	NextRunAt            string             `json:"next_run_at,omitempty"`
	Runs                 []models.BackupRun `json:"runs"`
	Alerts               []Alert            `json:"alerts"`
	AllowPrivateNetworks bool               `json:"allow_private_networks"`
	TimeZone             string             `json:"time_zone"`
}

// Status reports the configuration, key, current operation, recent history
// and any alerts. Local only — it never contacts the WebDAV server, so the
// admin console can call it freely (e.g. to decide whether to show its
// warning badge).
func (s *Service) Status(ctx context.Context) (Status, error) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return Status{}, err
	}
	st := Status{
		Config:               cfg,
		Alerts:               []Alert{},
		AllowPrivateNetworks: s.allowPrivate,
		TimeZone:             s.loc.String(),
	}

	key, ok, err := s.keys.load()
	if err != nil {
		return Status{}, err
	}
	if ok {
		st.Key = KeyInfo{Exists: true, Fingerprint: key.Fingerprint(), Saved: cfg.keyAcknowledged == key.Fingerprint()}
	}

	s.mu.Lock()
	if s.busyKind != "" {
		st.Running = &Running{Kind: s.busyKind, Source: s.busySrc, Since: formatTime(s.busySince)}
	}
	s.mu.Unlock()

	st.Runs, err = s.db.ListBackupRuns(ctx, runsShown)
	if err != nil {
		return Status{}, err
	}
	if len(st.Runs) > 0 {
		st.LastRun = &st.Runs[0]
	}
	var lastSuccess time.Time
	if run, err := s.db.LatestSuccessfulBackupRun(ctx); err == nil {
		st.LastSuccessAt = run.StartedAt
		lastSuccess, _ = parseTime(run.StartedAt)
	} else if !errors.Is(err, db.ErrNotFound) {
		return Status{}, err
	}

	now := s.now()
	if cfg.AutoEnabled && cfg.configured() {
		sched := cfg.schedule()
		st.NextRunAt = formatTime(sched.nextSlot(now, s.loc))

		// A scheduled run failed and nothing has succeeded since.
		for _, run := range st.Runs {
			if run.Status == "success" {
				break
			}
			if run.Source == SourceScheduled {
				st.Alerts = append(st.Alerts, Alert{Code: AlertLastBackupFailed, At: run.StartedAt, Error: run.Error})
				break
			}
		}

		// No success for too long, counted from the last success or, if
		// more recent (or there never was one), from when the schedule was
		// armed — so switching automatic backups on doesn't raise this alert
		// before the schedule has had a chance to run.
		since := lastSuccess
		if cfg.anchor.After(since) {
			since = cfg.anchor
		}
		if !since.IsZero() && now.Sub(since) > sched.staleAfter() {
			st.Alerts = append(st.Alerts, Alert{
				Code: AlertNoRecentSuccess,
				At:   st.LastSuccessAt,
				Days: int(now.Sub(since).Hours() / 24),
			})
		}
	}
	if cfg.configured() && st.Key.Exists && !st.Key.Saved {
		st.Alerts = append(st.Alerts, Alert{Code: AlertKeyNotSaved})
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// Key export
// ---------------------------------------------------------------------------

// ExportedKey is what the "download the key" action returns.
type ExportedKey struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	FileName    string `json:"file_name"`
	FileContent string `json:"file_content"`
}

// ExportKey returns the instance key (generating it if it doesn't exist
// yet) and records that the admin has now saved this exact key, which
// clears the key_not_saved alert.
func (s *Service) ExportKey(ctx context.Context) (ExportedKey, error) {
	now := s.now()
	key, err := s.keys.loadOrCreate(now)
	if err != nil {
		return ExportedKey{}, err
	}
	if err := s.db.SetSettings(ctx, map[string]string{settingKeyAcknowledged: key.Fingerprint()}); err != nil {
		return ExportedKey{}, err
	}
	return ExportedKey{
		Key:         key.String(),
		Fingerprint: key.Fingerprint(),
		FileName:    "trakka-backup-key-" + key.Fingerprint()[:9] + ".key",
		FileContent: KeyFileContent(key, now),
	}, nil
}

// CurrentKey returns the instance key, if one exists.
func (s *Service) CurrentKey() (Key, bool, error) {
	return s.keys.load()
}

// ---------------------------------------------------------------------------
// Busy state
// ---------------------------------------------------------------------------

func (s *Service) acquire(kind, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busyKind != "" {
		return ErrBusy
	}
	s.busyKind, s.busySrc, s.busySince = kind, source, s.now()
	return nil
}

func (s *Service) release() {
	s.mu.Lock()
	s.busyKind, s.busySrc, s.busySince = "", "", time.Time{}
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Connection test & remote listing
// ---------------------------------------------------------------------------

// ConnectionSettings is an unsaved WebDAV configuration to test. An empty
// Password means "the stored one".
type ConnectionSettings struct {
	URL      string
	Username string
	Password string
}

// TestResult is a connection test's outcome. A failed test is a normal
// result (OK false, with Code/Error), not an error return.
type TestResult struct {
	OK          bool   `json:"ok"`
	Writable    bool   `json:"writable"`
	BackupCount int    `json:"backup_count"`
	Code        string `json:"code,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (s *Service) clientFor(ctx context.Context, conn ConnectionSettings) (*webdavClient, error) {
	password := conn.Password
	if password == "" {
		cfg, err := s.loadConfig(ctx)
		if err != nil {
			return nil, err
		}
		if cfg.sealedPassword != "" {
			key, ok, err := s.keys.load()
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, ErrSecretUnreadable
			}
			if password, err = openSecret(key, cfg.sealedPassword); err != nil {
				return nil, err
			}
		}
	}
	client, err := newWebDAVClient(conn.URL, conn.Username, password, s.allowPrivate)
	if err != nil {
		return nil, &ValidationError{err.Error()}
	}
	return client, nil
}

func (s *Service) configuredClient(ctx context.Context) (*webdavClient, Config, error) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return nil, Config{}, err
	}
	if !cfg.configured() {
		return nil, cfg, ErrNotConfigured
	}
	client, err := s.clientFor(ctx, ConnectionSettings{URL: cfg.WebDAVURL, Username: cfg.WebDAVUsername})
	return client, cfg, err
}

// TestConnection checks that conn reaches a WebDAV folder, that the
// credentials are accepted, and that a file can actually be written and
// deleted there — a read-only share would otherwise pass a PROPFIND and
// only fail at the first real backup.
func (s *Service) TestConnection(ctx context.Context, conn ConnectionSettings) (TestResult, error) {
	client, err := s.clientFor(ctx, conn)
	if errors.Is(err, ErrSecretUnreadable) {
		return TestResult{Code: CodePasswordUnreadable, Error: err.Error()}, nil
	}
	if err != nil {
		return TestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	fail := func(err error) (TestResult, error) {
		return TestResult{Code: ErrorCode(err), Error: err.Error()}, nil
	}
	if err := client.probe(ctx); err != nil {
		return fail(err)
	}
	files, err := client.list(ctx)
	if err != nil {
		return fail(err)
	}

	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return TestResult{}, err
	}
	probeName := "trakka-write-test-" + hex.EncodeToString(suffix) + ".tmp"
	const probeBody = "Trakka backup write test — safe to delete.\n"
	if err := client.put(ctx, probeName, strings.NewReader(probeBody), int64(len(probeBody))); err != nil {
		res, _ := fail(err)
		res.BackupCount = countBackups(files)
		return res, nil
	}
	if err := client.delete(ctx, probeName); err != nil {
		s.logger.Warn("backup connection test: could not delete write-test file", "file", probeName, "error", err)
	}
	return TestResult{OK: true, Writable: true, BackupCount: countBackups(files)}, nil
}

func countBackups(files []remoteFile) int {
	n := 0
	for _, f := range files {
		if backupFilePattern.MatchString(f.Name) {
			n++
		}
	}
	return n
}

// RemoteBackup is one backup file found in the WebDAV folder.
type RemoteBackup struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified,omitempty"`
}

// ListRemote lists the backup files in the configured WebDAV folder,
// newest first.
func (s *Service) ListRemote(ctx context.Context) ([]RemoteBackup, error) {
	client, _, err := s.configuredClient(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	files, err := client.list(ctx)
	if err != nil {
		return nil, err
	}
	backups := sortedBackups(files)
	out := make([]RemoteBackup, 0, len(backups))
	for _, f := range backups {
		rb := RemoteBackup{Name: f.Name, Size: f.Size}
		if !f.Modified.IsZero() {
			rb.Modified = formatTime(f.Modified)
		}
		out = append(out, rb)
	}
	return out, nil
}

// sortedBackups keeps only backup files, newest first — by name, which
// embeds the UTC creation time, rather than by the server's modification
// date, which a copy or sync tool may have rewritten.
func sortedBackups(files []remoteFile) []remoteFile {
	var backups []remoteFile
	for _, f := range files {
		if backupFilePattern.MatchString(f.Name) {
			backups = append(backups, f)
		}
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].Name > backups[j].Name })
	return backups
}

// ---------------------------------------------------------------------------
// Backup
// ---------------------------------------------------------------------------

// StartManualBackup launches an immediate backup in the background and
// returns right away — ErrNotConfigured or ErrBusy if it can't start. The
// outcome is recorded in backup_runs and visible through Status.
func (s *Service) StartManualBackup(ctx context.Context) error {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.configured() {
		return ErrNotConfigured
	}
	if err := s.acquire("backup", SourceManual); err != nil {
		return err
	}
	// Not canceled with the triggering request, which ends as soon as the
	// 202 is written — the same "never r.Context() for background work"
	// rule the scraper's lookups follow; bounded by backupTimeout instead.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backupTimeout)
	go func() {
		defer s.release()
		defer cancel()
		s.runBackup(runCtx, SourceManual)
	}()
	return nil
}

// RunScheduledIfDue runs an automatic backup if one is due — called every
// minute by the scheduler loop in cmd/server. A slot is due once its time
// has passed, if it came after the schedule was armed, no attempt for it
// has succeeded yet, and it hasn't used up its retries.
func (s *Service) RunScheduledIfDue(ctx context.Context) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		s.logger.Error("backup scheduler: loading configuration", "error", err)
		return
	}
	if !cfg.AutoEnabled || !cfg.configured() {
		return
	}
	now := s.now()
	slot := cfg.schedule().latestSlot(now, s.loc)
	if slot.IsZero() || !slot.After(cfg.anchor) {
		return
	}
	attempts, err := s.db.ListBackupRunsSince(ctx, SourceScheduled, formatTime(slot))
	if err != nil {
		s.logger.Error("backup scheduler: reading past runs", "error", err)
		return
	}
	for _, a := range attempts {
		if a.Status == "success" {
			return
		}
	}
	if len(attempts) >= maxScheduledAttempts {
		return
	}
	if len(attempts) > 0 {
		if last, ok := parseTime(attempts[0].StartedAt); ok && now.Sub(last) < retryDelay {
			return
		}
	}

	if err := s.acquire("backup", SourceScheduled); err != nil {
		return // a manual backup or a restore is running; try again next tick
	}
	defer s.release()
	runCtx, cancel := context.WithTimeout(ctx, backupTimeout)
	defer cancel()
	s.runBackup(runCtx, SourceScheduled)
}

// runBackup performs one backup and records its outcome. Must be called
// with the busy state held.
func (s *Service) runBackup(ctx context.Context, source string) models.BackupRun {
	started := s.now()
	run := models.BackupRun{Source: source, StartedAt: formatTime(started)}

	name, size, retentionErr, err := s.performBackup(ctx, started)
	run.FinishedAt = formatTime(s.now())
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		s.logger.Error("backup failed", "source", source, "error", err)
	} else {
		run.Status = "success"
		run.FileName = name
		run.SizeBytes = size
		if retentionErr != nil {
			// The backup itself is safely uploaded; only pruning older ones
			// failed. Recorded on the (successful) run so the admin can see
			// the folder isn't being cleaned up.
			run.Error = "retention cleanup failed: " + retentionErr.Error()
			s.logger.Warn("backup retention cleanup failed", "error", retentionErr)
		}
		s.logger.Info("backup uploaded", "source", source, "file", name, "bytes", size)
	}

	// Recorded on a fresh context: ctx may be the very deadline that just
	// failed the run, and the failure still needs recording.
	recordCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stored, err := s.db.InsertBackupRun(recordCtx, run)
	if err != nil {
		s.logger.Error("recording backup run", "error", err)
		return run
	}
	return stored
}

func (s *Service) performBackup(ctx context.Context, started time.Time) (name string, size int64, retentionErr, err error) {
	client, cfg, err := s.configuredClient(ctx)
	if err != nil {
		return "", 0, nil, err
	}
	key, err := s.keys.loadOrCreate(started)
	if err != nil {
		return "", 0, nil, err
	}

	if err := os.MkdirAll(s.stagingDir, 0o700); err != nil {
		return "", 0, nil, fmt.Errorf("creating staging directory: %w", err)
	}
	snapshot := filepath.Join(s.stagingDir, "snapshot-"+started.UTC().Format("20060102T150405.000Z")+".db")
	defer func() { _ = os.Remove(snapshot) }()
	if err := s.db.SnapshotTo(ctx, snapshot); err != nil {
		return "", 0, nil, fmt.Errorf("snapshotting database: %w", err)
	}

	f, err := os.Open(snapshot) // #nosec G304 -- path built above inside the staging directory
	if err != nil {
		return "", 0, nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", 0, nil, err
	}

	// Encrypt straight into the upload through a pipe: the ciphertext is
	// never written to disk, and EncryptedSize gives the exact
	// Content-Length up front.
	name = backupFileName(started)
	size = EncryptedSize(info.Size())
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(Encrypt(pw, f, key))
	}()
	err = client.put(ctx, name, pr, size)
	// Unblocks the encrypting goroutine if the upload stopped reading early.
	_ = pr.CloseWithError(errors.New("upload finished"))
	if err != nil {
		return "", 0, nil, err
	}

	return name, size, s.applyRetention(ctx, client, cfg.Retention), nil
}

// applyRetention deletes every backup beyond the newest `keep` ones.
func (s *Service) applyRetention(ctx context.Context, client *webdavClient, keep int) error {
	files, err := client.list(ctx)
	if err != nil {
		return err
	}
	backups := sortedBackups(files)
	var firstErr error
	for i := keep; i < len(backups); i++ {
		if err := client.delete(ctx, backups[i].Name); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		s.logger.Info("deleted old backup (retention)", "file", backups[i].Name)
	}
	return firstErr
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

// RestoreResult describes a completed restore.
type RestoreResult struct {
	// FromSchemaVersion is the schema version the backup was taken at
	// (migrated to the current one during the restore).
	FromSchemaVersion int `json:"from_schema_version"`
	// SafetyCopy is the file name, under <DB_PATH dir>/backups/, of the
	// plaintext copy of the database as it was just before the restore.
	SafetyCopy string `json:"safety_copy"`
	// KeyAdopted reports that the supplied key differed from the
	// instance's own and replaced it (see keyStore.replace).
	KeyAdopted bool `json:"key_adopted"`
}

// StagedUpload is an uploaded (still encrypted) backup kept in the staging
// directory until it is restored or discarded. Deliberately opaque: callers
// never see or handle its filesystem path.
type StagedUpload struct {
	path string
}

// Discard removes the staged file; a no-op once it has been restored.
func (u *StagedUpload) Discard() {
	if u != nil {
		_ = os.Remove(u.path)
	}
}

// StageUpload writes an uploaded backup file into the staging directory —
// the multipart form's fields may arrive in any order, so the file has to
// be kept until the key is known. The caller must either pass the result
// to RestoreFromUpload or Discard it.
func (s *Service) StageUpload(src io.Reader) (*StagedUpload, error) {
	if err := os.MkdirAll(s.stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating staging directory: %w", err)
	}
	f, err := os.CreateTemp(s.stagingDir, "upload-*.tkb")
	if err != nil {
		return nil, err
	}
	u := &StagedUpload{path: f.Name()}
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		u.Discard()
		return nil, err
	}
	if err := f.Close(); err != nil {
		u.Discard()
		return nil, err
	}
	return u, nil
}

// RestoreFromUpload restores the live database from a staged upload,
// discarding the staged file afterward either way.
func (s *Service) RestoreFromUpload(ctx context.Context, u *StagedUpload, key Key) (RestoreResult, error) {
	defer u.Discard()
	if err := s.acquire("restore", ""); err != nil {
		return RestoreResult{}, err
	}
	defer s.release()

	f, err := os.Open(u.path) // #nosec G304 -- created by StageUpload via os.CreateTemp inside the staging directory
	if err != nil {
		return RestoreResult{}, err
	}
	defer func() { _ = f.Close() }()
	return s.restore(ctx, f, key)
}

// RestoreFromRemote downloads the named backup from the configured WebDAV
// folder and restores the live database from it.
func (s *Service) RestoreFromRemote(ctx context.Context, name string, key Key) (RestoreResult, error) {
	if !backupFilePattern.MatchString(name) {
		return RestoreResult{}, ErrInvalidRemoteName
	}
	if err := s.acquire("restore", ""); err != nil {
		return RestoreResult{}, err
	}
	defer s.release()

	client, _, err := s.configuredClient(ctx)
	if err != nil {
		return RestoreResult{}, err
	}
	body, err := client.get(ctx, name)
	if err != nil {
		return RestoreResult{}, err
	}
	defer body.Close()
	return s.restore(ctx, body, key)
}

// restore decrypts src into a staged plaintext file, vets and migrates it,
// keeps a safety copy of the current database, then copies the staged file
// over the live database. Must be called with the busy state held.
func (s *Service) restore(ctx context.Context, src io.Reader, key Key) (RestoreResult, error) {
	now := s.now()
	if err := os.MkdirAll(s.stagingDir, 0o700); err != nil {
		return RestoreResult{}, fmt.Errorf("creating staging directory: %w", err)
	}
	plain := filepath.Join(s.stagingDir, "restore-"+now.UTC().Format("20060102T150405.000Z")+".db")
	defer func() {
		for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
			_ = os.Remove(plain + suffix)
		}
	}()

	if err := decryptToFile(plain, src, key); err != nil {
		return RestoreResult{}, err
	}
	fromVersion, err := db.PrepareRestoreFile(ctx, plain, s.logger)
	if err != nil {
		return RestoreResult{}, err
	}
	safety, err := s.db.SnapshotBeforeRestore(ctx)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("keeping a safety copy of the current database: %w", err)
	}
	priorRuns, err := s.db.ListBackupRuns(ctx, carriedRunsLimit)
	if err != nil {
		return RestoreResult{}, err
	}
	if err := s.db.RestoreFrom(ctx, plain); err != nil {
		return RestoreResult{}, fmt.Errorf("restoring database: %w", err)
	}
	// From here on the restore itself has succeeded; the steps below only
	// realign the backup setup with the restored data, so they are logged
	// rather than reported as a failed restore.
	result := RestoreResult{FromSchemaVersion: fromVersion, SafetyCopy: filepath.Base(safety)}

	// The restored database's own backup settings (and its sealed WebDAV
	// password) belong with the key it was encrypted with, so that key
	// becomes the instance key.
	current, ok, err := s.keys.load()
	if err != nil || !ok || current != key {
		if err := s.keys.replace(key, now); err != nil {
			s.logger.Error("adopting the restored backup's key", "error", err)
		} else {
			result.KeyAdopted = true
		}
	}

	s.carryOverRuns(ctx, priorRuns)

	// The admin just supplied this key, so they have it; and staleness is
	// measured afresh from now, since the restored backup_runs history
	// predates the restore.
	if err := s.db.SetSettings(ctx, map[string]string{
		settingKeyAcknowledged: key.Fingerprint(),
		settingScheduleAnchor:  formatTime(now),
	}); err != nil {
		s.logger.Error("updating backup settings after restore", "error", err)
	}
	return result, nil
}

// carriedRunsLimit bounds how much pre-restore history carryOverRuns
// re-inserts (the table keeps no more than about this many rows anyway).
const carriedRunsLimit = 100

// carryOverRuns re-inserts, into the just-restored database, the backup
// runs the previous database knew about that are newer than anything in
// the restored history. A backup never contains its own run (that row is
// only written once the upload has finished — see migration 20), so
// without this the history would lose the very backup that was restored,
// and every run after it.
func (s *Service) carryOverRuns(ctx context.Context, prior []models.BackupRun) {
	restored, err := s.db.ListBackupRuns(ctx, 1)
	if err != nil {
		s.logger.Error("reading restored backup history", "error", err)
		return
	}
	latest := ""
	if len(restored) > 0 {
		latest = restored[0].StartedAt
	}
	for i := len(prior) - 1; i >= 0; i-- { // oldest first
		if prior[i].StartedAt <= latest {
			continue
		}
		run := prior[i]
		run.ID = 0
		if _, err := s.db.InsertBackupRun(ctx, run); err != nil {
			s.logger.Error("carrying backup history over the restore", "error", err)
			return
		}
	}
}

func decryptToFile(dest string, src io.Reader, key Key) error {
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- path built by restore inside the staging directory
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, chunkSize)
	if err := Decrypt(w, src, key); err != nil {
		_ = f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
