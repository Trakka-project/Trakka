package handlers

import (
	"context"
	"time"
)

// targetPriceScanDelay is the pause between two consecutive items' checks
// within a single RunTargetPriceScan or RunPriceAlertScan pass — deliberate rate limiting so a
// house tracking many items doesn't hit a merchant's site (Amazon in
// particular) with a burst of near-simultaneous requests, which risks the
// server's IP getting throttled or blocked outright. This is a politeness
// delay between requests to hosts this app doesn't control, unrelated to
// internal/scraper's own fetchTimeout (the bound on a single request).
const targetPriceScanDelay = 5 * time.Second

// RunTargetPriceScan re-checks every item with an active price-drop
// threshold (db.ListItemsForTargetPriceScan: alert_on_price_drop = true, a
// url, and a target_price set) through trackItemPrice — the same check
// RunPriceAlertScan runs on every other tracked item, just more often
// (SCRAPE_INTERVAL), since a user waiting on a target price wants to know
// soon. trackItemPrice applies whatever current price it finds and fires
// checkPriceDropAlert on a false→true transition exactly as a manual price
// edit does through the item handlers. Called on a timer from
// cmd/server/main.go (runTargetPriceScanLoop); also safe to call directly
// for an immediate, whole-catalog on-demand scan. targetPriceScanDelay is
// paused between items (not after the last one), so a full pass takes at
// least len(items) * ~5s — a large catalog should lean on a longer
// SCRAPE_INTERVAL rather than a shorter per-item delay.
func (app *Application) RunTargetPriceScan(ctx context.Context) {
	items, err := app.DB.ListItemsForTargetPriceScan(ctx)
	if err != nil {
		app.Logger.Error("listing items for target price scan", "error", err)
		return
	}
	app.runPriceTracking(ctx, "target price scan", items)
}

// sleepOrCanceled pauses for d, returning true the moment ctx is canceled
// instead of waiting out the rest of d — the shared cancelable-sleep helper
// for the pacing delay between two consecutive items within a scan, so a
// slow multi-item scan still responds to shutdown promptly rather than
// finishing its current delay first.
func sleepOrCanceled(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}
