package handlers

import (
	"errors"
	"net/http"

	"trakka/internal/db"
)

const (
	// priceNotificationsLimit is how many entries GET
	// /api/v1/price-notifications returns: the 🔔 drawer shows recent
	// alerts, not an archive.
	priceNotificationsLimit = 50
	// priceHistoryLimit is how many observed prices GET
	// /api/v1/items/{id}/price-history returns.
	priceHistoryLimit = 100
	// maxPriceNotificationIDs bounds one mark-read request.
	maxPriceNotificationIDs = 200
)

// handlePriceNotificationsIndex lists the signed-in user's in-app price
// alerts (see notifyPriceChange), newest first, read or not — the 🔔 drawer
// counts the unread ones for its badge. Scoped to the caller only, and to
// items on lists they can still access (db.ListPriceNotifications).
func (app *Application) handlePriceNotificationsIndex(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	notifications, err := app.DB.ListPriceNotifications(r.Context(), user.ID, priceNotificationsLimit)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, notifications)
}

// handlePriceNotificationsRead marks the caller's own price notifications
// read: {"ids": [...]} for some, an empty body object ({}) for all of them.
// An id belonging to someone else is ignored, never an error, so the answer
// can't reveal which ids exist.
func (app *Application) handlePriceNotificationsRead(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.IDs) > maxPriceNotificationIDs {
		writeError(w, http.StatusBadRequest, "too many ids")
		return
	}
	user := userFromContext(r)
	if err := app.DB.MarkPriceNotificationsRead(r.Context(), user.ID, in.IDs); err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleItemsPriceHistory returns the prices observed on an item's own page
// over time (db.RecordPriceObservation), oldest first. Read access to the
// item's list is enough.
func (app *Application) handleItemsPriceHistory(w http.ResponseWriter, r *http.Request) {
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
	if !app.authorizeItemAccess(w, r, item.ListID, false) {
		return
	}
	history, err := app.DB.ListPriceHistory(r.Context(), id, priceHistoryLimit)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, history)
}
