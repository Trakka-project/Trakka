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
	flag.Parse()

	if *generateVAPIDKeys {
		os.Exit(runGenerateVAPIDKeys())
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
	logHandler := logbuffer.NewHandler(slog.NewJSONHandler(os.Stdout, nil), 500)
	logger := slog.New(logHandler)

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

	loginTemplate, err := template.ParseFiles(filepath.Join(cfg.TemplatesDir, "login.html"))
	if err != nil {
		logger.Error("parsing login template", "error", err)
		os.Exit(1)
	}

	app := &handlers.Application{
		DB:            database,
		StaticDir:     cfg.StaticDir,
		Logger:        logger,
		Auth:          authService,
		LoginTemplate: loginTemplate,
		Config:        cfg,
		LogBuffer:     logHandler,
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// The periodic price-drop scan runs detached from any request, on its
	// own cancelable context, the same "never r.Context()" reasoning
	// scrapeProductInfo already follows — canceled only on shutdown, below.
	priceScanCtx, cancelPriceScan := context.WithCancel(context.Background())
	defer cancelPriceScan()
	if cfg.PriceCheckInterval > 0 {
		go runPriceAlertScanLoop(priceScanCtx, app, cfg.PriceCheckInterval, logger)
	}

	// The target-price scan (SCRAPE_INTERVAL) shares the same detached-context
	// pattern — it's a distinct feature from the price-drop scan above (see
	// config.TargetPriceScrapeInterval's doc comment for how they differ):
	// this one re-scrapes only items with an active alert_on_price_drop
	// threshold and writes items.price directly, with no accept/reject step.
	if cfg.TargetPriceScrapeInterval > 0 {
		go runTargetPriceScanLoop(priceScanCtx, app, cfg.TargetPriceScrapeInterval, logger)
	}

	// The recurring-task due-date reminder scan (Web Push "Use Case 2")
	// shares the same detached-context/immediate-first-run pattern as the
	// price scan above, and is gated on both a positive scan interval and
	// push actually being configured — with no VAPID keys, RunRecurringDueScan
	// would just no-op on every tick, so there is no reason to run it at all.
	if cfg.NotifRecurringScanInterval > 0 && cfg.PushEnabled() {
		go runRecurringNotifyScanLoop(priceScanCtx, app, cfg.NotifRecurringScanInterval, logger)
	}

	// Expired sessions are swept on the same detached-context pattern as the
	// price scan above: nothing deleted them before, so the table grew for
	// the life of the instance (see db.DeleteExpiredSessions).
	go runSessionCleanupLoop(priceScanCtx, database, logger)

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

// runPriceAlertScanLoop periodically re-checks every eligible item's price
// for a better deal (see handlers.Application.RunPriceAlertScan), stopping
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

// runRecurringNotifyScanLoop periodically checks every recurring item's due
// date against its (instance-default or per-item) lead time and sends a
// reminder push once it falls within it (see
// handlers.Application.RunRecurringDueScan), stopping once ctx is canceled
// during shutdown. Runs an initial scan immediately, same reasoning as
// runPriceAlertScanLoop: a freshly deployed instance shouldn't sit with a
// backlog of overdue reminders for up to a full NOTIF_RECURRING_SCAN_INTERVAL_MINUTES
// before its first check.
func runRecurringNotifyScanLoop(ctx context.Context, app *handlers.Application, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Info("starting periodic recurring due-date notification scan", "interval", interval)
	app.RunRecurringDueScan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			app.RunRecurringDueScan(ctx)
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
