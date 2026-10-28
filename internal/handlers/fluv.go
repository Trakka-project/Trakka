package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"trakka/internal/db"
	"trakka/internal/fluv"
	"trakka/internal/models"
)

// Sending purchases to Fluv (internal/fluv, docs/FLUV.md): each user
// connects their own Fluv from Paramètres → Intégrations, through these
// routes — all behind RequireSession and requireSameOriginWrite, and none
// on apiTokenRoutes: an API token can't read or change where a user's
// purchases go. They answer 404 when the operator turned the feature off
// (FLUV_INTEGRATION_ENABLED=false, app.Fluv nil).

// The connection test sends a request to the user's Fluv, so it is rate
// limited per account like the other outbound lookups.
const (
	fluvTestWindow = 5 * time.Minute
	fluvTestMax    = 10
)

func (app *Application) fluvTestLimiter() *rateLimiter {
	app.fluvTestLimiterOnce.Do(func() { app.fluvTestLimiterVal = newRateLimiter(fluvTestWindow) })
	return app.fluvTestLimiterVal
}

// fluvAvailable writes the 404 of a disabled feature and reports false.
func (app *Application) fluvAvailable(w http.ResponseWriter) bool {
	if app.Fluv == nil {
		writeError(w, http.StatusNotFound, "the Fluv integration is not enabled on this server")
		return false
	}
	return true
}

// handleFluvShow answers GET /api/v1/integrations/fluv: the caller's
// connection, never the token.
func (app *Application) handleFluvShow(w http.ResponseWriter, r *http.Request) {
	if !app.fluvAvailable(w) {
		return
	}
	settings, err := app.Fluv.Get(r.Context(), userFromContext(r).ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// handleFluvSave answers PUT /api/v1/integrations/fluv: creates or replaces
// the caller's connection. A refused setting is a 400 with a code.
func (app *Application) handleFluvSave(w http.ResponseWriter, r *http.Request) {
	if !app.fluvAvailable(w) {
		return
	}
	var in fluv.Update
	if !decodeJSON(w, r, &in) {
		return
	}
	settings, err := app.Fluv.Save(r.Context(), userFromContext(r).ID, in)
	var verr *fluv.ValidationError
	if errors.As(err, &verr) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": verr.Message, "code": verr.Code, "field": verr.Field})
		return
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// handleFluvDelete answers DELETE /api/v1/integrations/fluv: disconnects
// Fluv. Idempotent.
func (app *Application) handleFluvDelete(w http.ResponseWriter, r *http.Request) {
	if !app.fluvAvailable(w) {
		return
	}
	if err := app.Fluv.Delete(r.Context(), userFromContext(r).ID); err != nil && !errors.Is(err, db.ErrNotFound) {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleFluvTest answers POST /api/v1/integrations/fluv/test. Fluv's own
// refusals are a 200 with ok false and a code, never a 5xx, which sw.js and
// apiRequest would take for Trakka itself being unreachable (the same
// convention as the backup connection test).
func (app *Application) handleFluvTest(w http.ResponseWriter, r *http.Request) {
	if !app.fluvAvailable(w) {
		return
	}
	user := userFromContext(r)
	if !app.fluvTestLimiter().allow(strconv.FormatInt(user.ID, 10), fluvTestMax) {
		writeError(w, http.StatusTooManyRequests, "too many connection tests; try again in a few minutes")
		return
	}
	result, err := app.Fluv.Test(r.Context(), user.ID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Fluv is not connected")
		return
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleFluvRetry answers POST /api/v1/integrations/fluv/retry: the
// caller's failed purchases are sent again.
func (app *Application) handleFluvRetry(w http.ResponseWriter, r *http.Request) {
	if !app.fluvAvailable(w) {
		return
	}
	n, err := app.Fluv.Retry(r.Context(), userFromContext(r).ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"retried": n})
}

// reportPurchase tells internal/fluv about an item a signed-in user (or
// one of their API tokens) just changed from before to after: checked off,
// un-checked, or repriced while bought (see fluv.Service.ItemChanged). It
// never fails the request — the item change has already been saved — and
// only queues: the purchase is sent in the background. Public link
// visitors' changes don't come through here: they aren't anyone's
// purchase.
func (app *Application) reportPurchase(r *http.Request, before, after *models.Item) {
	if app.Fluv == nil || (!before.Done && !after.Done) {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	list, err := app.DB.GetList(ctx, after.ListID)
	if err != nil {
		app.Logger.Error("reading list for Fluv", "list_id", after.ListID, "error", err)
		return
	}
	app.Fluv.ItemChanged(ctx, userFromContext(r).ID, list, before, after)
}
