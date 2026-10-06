// Command server is Trakka's entry point: it loads configuration, opens
// the database, wires up the HTTP handler, and runs the server with a
// graceful shutdown path that closes the database connection cleanly.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"trakka/internal/auth"
	"trakka/internal/backup"
	"trakka/internal/config"
	"trakka/internal/db"
	"trakka/internal/handlers"
	"trakka/internal/logbuffer"
	"trakka/internal/settings"
	"trakka/internal/webpush"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit (used by HEALTHCHECK)")
	generateVAPIDKeys := flag.Bool("generate-vapid-keys", false, "print a fresh VAPID key pair for VAPID_PUBLIC_KEY/VAPID_PRIVATE_KEY and exit")
	decryptBackup := flag.String("decrypt-backup", "", "decrypt this encrypted .tkb backup into a plain SQLite file (see -backup-key, -decrypt-output) and exit")
	backupKey := flag.String("backup-key", "", "with -decrypt-backup: the backup key, or the path to a downloaded .key file")
	decryptOutput := flag.String("decrypt-output", "", "with -decrypt-backup: where to write the decrypted SQLite database (must not exist)")
	flag.Parse()

	if *generateVAPIDKeys {
		os.Exit(runGenerateVAPIDKeys())
	}
	if *decryptBackup != "" {
		os.Exit(runDecryptBackup(*decryptBackup, *backupKey, *decryptOutput))
	}

	cfg := config.Load()

	if *healthcheck {
		os.Exit(probeHealthz(cfg.Port))
	}

	// logHandler wraps the ordinary stdout JSON handler with an in-memory
	// ring buffer (internal/logbuffer) so the admin console's "Logs" panel
	// (GET /api/v1/admin/logs) can surface recent activity without this app
	// needing a log file or a database table for it — see logbuffer's own
	// package doc for why. Capacity of 500 is generous for "what's happening
	// right now" at the scale this app targets while staying a small,
	// bounded amount of memory regardless of how long the process has run.
	var logLevel slog.Level
	logLevelErr := logLevel.UnmarshalText([]byte(cfg.LogLevel))
	if logLevelErr != nil {
		logLevel = slog.LevelInfo
	}
	logHandler := logbuffer.NewHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}), 500)
	logger := slog.New(logHandler)
	if logLevelErr != nil {
		logger.Warn("unrecognized LOG_LEVEL, using info", "value", cfg.LogLevel)
	}

	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	database, err := db.Open(cfg.DBPath, logger)
	if err != nil {
		logger.Error("opening database", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("closing database", "error", err)
		}
	}()

	resolvedSettings, err := settings.Resolve(context.Background(), database, cfg)
	if err != nil {
		logger.Error("resolving system settings", "error", err)
		os.Exit(1)
	}

	var oidcClient *auth.OIDCClient
	if resolvedSettings.OIDCEnabled {
		switch {
		case resolvedSettings.OIDCIssuer == "" || resolvedSettings.OIDCClientID == "" || resolvedSettings.OIDCClientSecret == "":
			logger.Warn("oidc is marked enabled but incompletely configured; starting with OIDC disabled until fixed via PATCH /api/v1/admin/settings")
		case cfg.BaseURL == "":
			logger.Warn("oidc is enabled but BASE_URL is not set; starting with OIDC disabled")
		default:
			// Unlike the env-var-only OIDC config this replaces (which still
			// fails startup outright via cfg.Validate() below on a broken
			// all-or-nothing env setup), a DB-driven OIDC config that was valid
			// when an admin saved it can still fail discovery later purely
			// because the IdP is temporarily unreachable. Crashing local-auth
			// availability along with it on every restart until the IdP comes
			// back would be worse than starting up with OIDC login simply
			// unavailable — so this logs and continues rather than exiting.
			oidcClient, err = auth.NewOIDCClient(context.Background(), resolvedSettings.OIDCIssuer, resolvedSettings.OIDCClientID, resolvedSettings.OIDCClientSecret, cfg.BaseURL+"/auth/oidc/callback")
			if err != nil {
				logger.Error("oidc discovery failed at startup; starting with OIDC disabled", "error", err)
				oidcClient = nil
			}
		}
	}
	authService := auth.NewService(database, oidcClient, cfg.SessionTTL, cfg.SessionCookieSecure)

	// Resolved once at startup, the same graceful-degrade-with-warning
	// posture as a broken OIDC discovery above: reminder times of day (see
	// models.User.ReminderDefaultTime, models.Item.ReminderTime) are
	// meaningless without a time zone to interpret them in, but a typo'd
	// APP_TIMEZONE shouldn't take the whole instance down.
	location, err := time.LoadLocation(cfg.AppTimeZone)
	if err != nil {
		logger.Warn("unrecognized APP_TIMEZONE; falling back to UTC", "app_timezone", cfg.AppTimeZone, "error", err)
		location = time.UTC
	}

	loginTemplate, err := template.ParseFiles(filepath.Join(cfg.TemplatesDir, "login.html"))
	if err != nil {
		logger.Error("parsing login template", "error", err)
		os.Exit(1)
	}

	backups := backup.New(backup.Options{
		DB:                   database,
		DBPath:               cfg.DBPath,
		Logger:               logger,
		Location:             location,
		AllowPrivateNetworks: cfg.BackupWebDAVAllowPrivate,
	})

	app := &handlers.Application{
		DB:            database,
		StaticDir:     cfg.StaticDir,
		Logger:        logger,
		Auth:          authService,
		LoginTemplate: loginTemplate,
		Config:        cfg,
		LogBuffer:     logHandler,
		Location:      location,
		Backups:       backups,
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// The periodic price scans run detached from any request, on their own
	// cancelable context, the same "never r.Context()" reasoning
	// scrapeProductInfo already follows — canceled only on shutdown, below.
	// Unlike the due-reminder scan they never depend on push being
	// configured: a price alert always reaches the in-app inbox (see
	// handlers.notifyPriceChange), push or not.
	priceScanCtx, cancelPriceScan := context.WithCancel(context.Background())
	defer cancelPriceScan()
	if cfg.PriceCheckInterval > 0 {
		go runPriceAlertScanLoop(priceScanCtx, app, cfg.PriceCheckInterval, logger)
	}

	// The target-price scan (SCRAPE_INTERVAL) runs the same per-item check
	// as the scan above (handlers.trackItemPrice), on the items with an
	// active alert_on_price_drop threshold only — which the scan above
	// leaves out — and more often, since a user waiting on a target price
	// wants to know soon.
	if cfg.TargetPriceScrapeInterval > 0 {
		go runTargetPriceScanLoop(priceScanCtx, app, cfg.TargetPriceScrapeInterval, logger)
	}

	// The task due-date reminder scan (Web Push "Use Case 2") shares the
	// same detached-context/immediate-first-run pattern as the price scan
	// above, and is gated on both a positive scan interval and push
	// actually being configured — with no VAPID keys, RunDueReminderScan
	// would just no-op on every tick, so there is no reason to run it at all.
	if cfg.NotifDueScanInterval > 0 && cfg.PushEnabled() {
		go runDueReminderScanLoop(priceScanCtx, app, cfg.NotifDueScanInterval, logger)
	}

	// Recurring tasks come back for their next occurrence on their own
	// fixed one-minute cadence, whether or not push is configured: it is
	// part of the task lifecycle, not a notification (see
	// handlers.Application.RunNextOccurrenceScan).
	go runNextOccurrenceLoop(priceScanCtx, app)

	// Expired sessions are swept on the same detached-context pattern as the
	// price scan above: nothing deleted them before, so the table grew for
	// the life of the instance (see db.DeleteExpiredSessions).
	go runSessionCleanupLoop(priceScanCtx, database, logger)

	// The backup scheduler always runs, on the same detached context: it
	// is a cheap once-a-minute settings read that does nothing until an
	// admin switches automatic backups on from the console.
	go runBackupSchedulerLoop(priceScanCtx, backups)

	serverErrs := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", srv.Addr, "db_path", cfg.DBPath, "static_dir", cfg.StaticDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrs <- err
			return
		}
		serverErrs <- nil
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErrs:
		if err != nil {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}

	case <-stop:
		logger.Info("shutdown signal received")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
		// database.Close() runs via the deferred call above once main
		// returns, after in-flight requests have drained.
	}
}

// runPriceAlertScanLoop periodically re-checks every tracked item's price —
// following its page, recording its history, looking for a better deal
// (see handlers.Application.RunPriceAlertScan) — stopping
// once ctx is canceled during shutdown. It runs an initial scan right away
// rather than waiting a full interval for the first one, so a freshly
// deployed instance doesn't sit with an empty notification center for up to
// PRICE_CHECK_INTERVAL_HOURS before its first check. Runs in its own
// goroutine (see the call site in main), so this scan itself never delays
// server startup.
func runPriceAlertScanLoop(ctx context.Context, app *handlers.Application, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Info("starting periodic price alert scan", "interval", interval)
	app.RunPriceAlertScan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			app.RunPriceAlertScan(ctx)
		}
	}
}

// runTargetPriceScanLoop periodically re-scrapes every item with an active
// price-drop threshold and applies whatever price it finds (see
// handlers.Application.RunTargetPriceScan), stopping once ctx is canceled
// during shutdown. Runs an initial scan immediately, same reasoning as
// runPriceAlertScanLoop: a freshly deployed instance shouldn't sit with a
// stale price on a tracked item for up to a full SCRAPE_INTERVAL before its
// first check.
func runTargetPriceScanLoop(ctx context.Context, app *handlers.Application, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Info("starting periodic target price scan", "interval", interval)
	app.RunTargetPriceScan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			app.RunTargetPriceScan(ctx)
		}
	}
}

// runDueReminderScanLoop periodically checks every item's due date against
// its own (already-resolved) reminder offset/time and sends a push once
// that moment is reached (see handlers.Application.RunDueReminderScan), and
// sends each user's daily overdue-tasks summary once their summary time has
// passed (handlers.Application.RunOverdueSummaryScan), stopping once ctx is
// canceled during shutdown. Runs an initial scan
// immediately, same reasoning as runPriceAlertScanLoop: a freshly deployed
// instance shouldn't sit with a backlog of overdue reminders for up to a
// full NOTIF_DUE_SCAN_INTERVAL_MINUTES before its first check.
func runDueReminderScanLoop(ctx context.Context, app *handlers.Application, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Info("starting periodic due-date reminder scan", "interval", interval)
	app.RunDueReminderScan(ctx)
	app.RunOverdueSummaryScan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			app.RunDueReminderScan(ctx)
			app.RunOverdueSummaryScan(ctx)
		}
	}
}

// nextOccurrenceScanInterval is how often done recurring tasks are checked
// for a next occurrence that has started. One minute keeps a task's return
// close to its start of day or reminder moment; the scan reads a partial
// index of done recurring items only, so it costs next to nothing.
const nextOccurrenceScanInterval = time.Minute

// runNextOccurrenceLoop brings recurring tasks back for their next
// occurrence (see handlers.Application.RunNextOccurrenceScan), stopping once
// ctx is canceled during shutdown. Runs once immediately so occurrences
// that started while the server was down come back at startup.
func runNextOccurrenceLoop(ctx context.Context, app *handlers.Application) {
	ticker := time.NewTicker(nextOccurrenceScanInterval)
	defer ticker.Stop()

	app.RunNextOccurrenceScan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			app.RunNextOccurrenceScan(ctx)
		}
	}
}

// sessionCleanupInterval is how often expired session rows are swept. Hourly
// is far more often than strictly necessary for correctness — an expired
// session is already rejected by GetSessionByHash's own WHERE clause, so this
// is purely housekeeping — but cheap enough (one indexed DELETE) that a tighter
// interval costs nothing and keeps the table proportional to live sessions.
const sessionCleanupInterval = time.Hour

// runSessionCleanupLoop periodically deletes expired sessions, stopping once
// ctx is canceled during shutdown. Like the price scan, it runs once
// immediately so a long-lived instance restarted after downtime clears its
// backlog straight away instead of carrying it for another full interval.
func runSessionCleanupLoop(ctx context.Context, database *db.DB, logger *slog.Logger) {
	sweep := func() {
		n, err := database.DeleteExpiredSessions(ctx)
		if err != nil {
			logger.Error("cleaning up expired sessions", "error", err)
			return
		}
		if n > 0 {
			logger.Info("cleaned up expired sessions", "deleted", n)
		}
	}

	ticker := time.NewTicker(sessionCleanupInterval)
	defer ticker.Stop()
	sweep()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// backupSchedulerTick is how often the backup scheduler checks whether an
// automatic backup is due. The schedule itself is expressed in whole
// minutes (HH:MM), so checking more often would gain nothing.
const backupSchedulerTick = time.Minute

// runBackupSchedulerLoop asks the backup service, once a minute, to run an
// automatic backup if one is due (see backup.Service.RunScheduledIfDue for
// the due/retry rules), stopping once ctx is canceled during shutdown. The
// first check is immediate, so a slot missed while the server was down is
// caught up right after startup rather than a minute later.
func runBackupSchedulerLoop(ctx context.Context, backups *backup.Service) {
	ticker := time.NewTicker(backupSchedulerTick)
	defer ticker.Stop()

	backups.RunScheduledIfDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			backups.RunScheduledIfDue(ctx)
		}
	}
}

// runDecryptBackup implements `trakka -decrypt-backup`: the disaster-
// recovery path that needs no running server, no admin account and no web
// UI — it turns an encrypted .tkb backup back into a plain SQLite file the
// operator can put in place at DB_PATH before starting Trakka (see
// docs/DEPLOYMENT.md). Like -generate-vapid-keys it runs before any
// configuration is loaded. keyArg is either the key itself or the path to
// a downloaded .key file.
func runDecryptBackup(inPath, keyArg, outPath string) int {
	if keyArg == "" || outPath == "" {
		fmt.Fprintln(os.Stderr, "-decrypt-backup needs both -backup-key and -decrypt-output")
		return 2
	}
	keyText := keyArg
	if data, err := os.ReadFile(keyArg); err == nil { // #nosec G304 -- operator-supplied path on the operator's own command line
		keyText = string(data)
	}
	key, err := backup.ParseKey(keyText)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reading backup key:", err)
		return 1
	}

	in, err := os.Open(inPath) // #nosec G304 -- operator-supplied path on the operator's own command line
	if err != nil {
		fmt.Fprintln(os.Stderr, "opening backup:", err)
		return 1
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- operator-supplied path on the operator's own command line
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating output file:", err)
		return 1
	}
	if err := backup.Decrypt(out, in, key); err != nil {
		_ = out.Close()
		_ = os.Remove(outPath)
		fmt.Fprintln(os.Stderr, "decrypting backup:", err)
		return 1
	}
	if err := out.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "writing output file:", err)
		return 1
	}
	fmt.Printf("decrypted %s into %s (key %s)\n", inPath, outPath, key.Fingerprint())

	// The decrypted database's own backup settings — its encrypted WebDAV
	// password in particular — were sealed with this same key, so it is
	// installed as the instance key next to the output (if none is there
	// yet) for the database to come up fully working at that location.
	installed, err := backup.InstallKey(filepath.Dir(outPath), key, time.Now())
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "warning: could not install the backup key next to the database:", err)
	case installed:
		fmt.Printf("installed the backup key as %s\n", filepath.Join(filepath.Dir(outPath), backup.KeyFileName))
	default:
		fmt.Printf("note: %s already exists and was left untouched; if it isn't key %s, re-enter the WebDAV password in the admin console after starting\n",
			filepath.Join(filepath.Dir(outPath), backup.KeyFileName), key.Fingerprint())
	}
	return 0
}

// runGenerateVAPIDKeys implements `trakka -generate-vapid-keys`: a one-time
// setup convenience for an operator standing up Web Push, printing a fresh
// key pair to stdout in exactly the form VAPID_PUBLIC_KEY/VAPID_PRIVATE_KEY
// expect. Deliberately runs before config.Load()/cfg.Validate() — it needs
// no configuration at all, and must work even on a completely unconfigured
// instance (that is the point of it).
func runGenerateVAPIDKeys() int {
	keys, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generating VAPID key pair:", err)
		return 1
	}
	fmt.Printf("VAPID_PUBLIC_KEY=%s\n", keys.PublicKeyB64)
	fmt.Printf("VAPID_PRIVATE_KEY=%s\n", keys.PrivateKeyB64)
	return 0
}

// probeHealthz is invoked as `trakka -healthcheck` from the container
// HEALTHCHECK. Doing it in-process avoids shipping curl/wget in the
// runtime image, keeping the image (and its attack surface) smaller.
func probeHealthz(port string) int {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
