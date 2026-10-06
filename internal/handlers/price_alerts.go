package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// priceCheckTimeout bounds a single item's own-page fetch during a periodic
// price scan or an on-demand check — long enough for a normal product
// page, short enough that one slow/unresponsive site can't stall the whole
// scan (or an on-demand request a user is actively waiting on)
// indefinitely.
const priceCheckTimeout = 10 * time.Second

// RunPriceAlertScan runs trackItemPrice over every tracked item without an
// active target price (db.ListItemsForPriceScan — not done, has a url;
// target-price items are RunTargetPriceScan's): their price history, the
// price itself when it follows the page, price change alerts, and better
// deals proposed as price_alerts rows. Called on a timer from
// cmd/server/main.go (PRICE_CHECK_INTERVAL_HOURS), whether or not push
// notifications are configured — an alert always reaches the in-app inbox
// (see notifyPriceChange). Also prunes that inbox's old entries. Best-effort
// throughout: one item's check failing (network error, or a genuine
// internal/db error) is logged and never stops the rest of the scan.
func (app *Application) RunPriceAlertScan(ctx context.Context) {
	if n, err := app.DB.PrunePriceNotifications(ctx, priceNotificationReadRetentionDays, priceNotificationRetentionDays); err != nil {
		app.Logger.Error("pruning price notifications", "error", err)
	} else if n > 0 {
		app.Logger.Info("pruned price notifications", "count", n)
	}

	items, err := app.DB.ListItemsForPriceScan(ctx)
	if err != nil {
		app.Logger.Error("listing items for price alert scan", "error", err)
		return
	}
	app.runPriceTracking(ctx, "price alert scan", items)
}

// handlePriceAlertsIndex lists price alerts for a house, filterable by
// status — the notification bell calls this with ?status=pending. house_id
// is required, mirroring handleItemsIndex's ?list_id requirement, since a
// price alert only makes sense scoped to a house the caller is a member of.
func (app *Application) handlePriceAlertsIndex(w http.ResponseWriter, r *http.Request) {
	houseIDStr := r.URL.Query().Get("house_id")
	if houseIDStr == "" {
		writeError(w, http.StatusBadRequest, "house_id query parameter is required")
		return
	}
	houseID, err := strconv.ParseInt(houseIDStr, 10, 64)
	if err != nil || houseID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid house_id")
		return
	}
	if !app.authorizeHouseAccess(w, r, houseID) {
		return
	}

	status := r.URL.Query().Get("status")
	if status != "" && !models.ValidPriceAlertStatuses[status] {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}

	alerts, err := app.DB.ListPriceAlertsByHouse(r.Context(), houseID, status)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, alerts)
}

// handlePriceAlertsUpdate resolves a pending alert: {"status": "accepted"}
// applies its found_price to the item and makes its source_url the item's
// url (see db.AcceptPriceAlert), {"status": "rejected"} just dismisses it. Once resolved an alert can never be re-actioned — see
// db.AcceptPriceAlert/RejectPriceAlert's shared "WHERE status = 'pending'"
// guard — so a repeat call (e.g. a double click) reports 409 rather than
// silently doing nothing or erroring as "not found".
func (app *Application) handlePriceAlertsUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var in struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Status != "accepted" && in.Status != "rejected" {
		writeError(w, http.StatusBadRequest, "status must be \"accepted\" or \"rejected\"")
		return
	}

	alert, err := app.DB.GetPriceAlert(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "price alert not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeItemAccess(w, r, alert.ListID, true) {
		return
	}
	// Accepting makes source_url the item's url (db.AcceptPriceAlert). It
	// was built by this server (the item's own url, or a Dealabs deal page),
	// but it is still re-checked like any url an item can carry.
	if in.Status == "accepted" {
		if clean, err := validate.URL(alert.SourceURL); err != nil || clean == "" {
			writeError(w, http.StatusConflict, "price alert source is not a valid url")
			return
		}
	}

	var updated *models.PriceAlert
	if in.Status == "accepted" {
		updated, err = app.DB.AcceptPriceAlert(r.Context(), id)
	} else {
		updated, err = app.DB.RejectPriceAlert(r.Context(), id)
	}
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusConflict, "price alert was already resolved")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	// Accepting an alert is itself a price update "via le scraper" (the
	// original deal-detection scan), so it's checked against the item's own
	// target-price threshold the same way a manual/scraped price change is —
	// see checkPriceDropAlert. This endpoint's response shape is the
	// PriceAlert, not the Item, so there's no PriceAlertTriggered field to
	// surface an in-app toast through here; the push notification still
	// fires regardless, best-effort, and errors fetching the item to check
	// are logged rather than failing an already-successful accept.
	if in.Status == "accepted" {
		if item, itemErr := app.DB.GetItem(r.Context(), alert.ItemID); itemErr == nil {
			wasActive := priceAlertCondition(&models.Item{Price: &alert.OriginalPrice, TargetPrice: item.TargetPrice, AlertOnPriceDrop: item.AlertOnPriceDrop})
			app.checkPriceDropAlert(item, wasActive)
		} else {
			app.Logger.Error("loading item after accepting price alert", "item_id", alert.ItemID, "error", itemErr)
		}
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleItemsPriceCheck runs an immediate, synchronous trackItemPrice for
// one item ("Vérifier le prix maintenant", the "à la demande" counterpart
// to the periodic scans) as a manual check — the item's price becomes its
// page's, even a typed-in one — and reports the item as it stands
// afterward, what the check read on the page (check: status, observed
// price, or why the page couldn't be read), and whatever pending alert
// exists for it: a freshly created one, one from an earlier check that's
// still pending, or null.
// Bounded by priceCheckTimeout for the item's page plus dealSearchTimeout
// for the deal search.
func (app *Application) handleItemsPriceCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	item, err := app.DB.GetItem(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeItemAccess(w, r, item.ListID, true) {
		return
	}
	if item.URL == nil || *item.URL == "" {
		writeError(w, http.StatusBadRequest, "item has no url to check")
		return
	}

	_, outcome, err := app.trackItemPrice(r.Context(), item, true)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	updated, err := app.DB.GetItem(r.Context(), item.ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	var alert *models.PriceAlert
	alert, err = app.DB.GetPendingPriceAlertForItem(r.Context(), item.ID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert": alert, "item": updated, "check": outcome})
}
