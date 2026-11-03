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

// handleFluvMetadata answers GET /api/v1/integrations/fluv/metadata?lang=…:
// the caller's Fluv accounts and expense categories, read from their Fluv
// with the stored token, for the menus of the list modal and the purchase
// sheet. Fluv failing (an older Fluv without the endpoint included) is a
// 200 with ok false and a code, like the connection test; the page then
// keeps its text fields. 404 when Fluv is not connected.
func (app *Application) handleFluvMetadata(w http.ResponseWriter, r *http.Request) {
	if !app.fluvAvailable(w) {
		return
	}
	m, err := app.Fluv.Metadata(r.Context(), userFromContext(r).ID, r.URL.Query().Get("lang"))
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Fluv is not connected")
		return
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// fluvListDefaultsView is what GET/PUT /api/v1/integrations/fluv/lists/{id}
// answer: the caller's defaults for the list, and whether they connected a
// Fluv at all (the list modal only offers the defaults then).
type fluvListDefaultsView struct {
	fluv.ListDefaults
	Configured bool `json:"configured"`
}

// loadFluvList reads the purchase list a /api/v1/integrations/fluv/lists/{id}
// request is about, writing the error and reporting false when the caller
// may not see it — or when it is not a purchase list (nothing on it is ever
// sent to Fluv). Only read access is needed: the defaults are the caller's,
// about their own Fluv, not a change to the list.
func (app *Application) loadFluvList(w http.ResponseWriter, r *http.Request) (*models.List, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	list, err := app.DB.GetList(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return nil, false
	} else if err != nil {
		app.serverError(w, r, err)
		return nil, false
	}
	if !app.authorizeListAccess(w, r, list, false) {
		return nil, false
	}
	if !models.IsPurchaseListType(list.Type) {
		writeError(w, http.StatusBadRequest, "only purchase lists send their items to Fluv")
		return nil, false
	}
	return list, true
}

func (app *Application) fluvListDefaultsView(w http.ResponseWriter, r *http.Request, defaults fluv.ListDefaults) {
	settings, err := app.Fluv.Get(r.Context(), userFromContext(r).ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, fluvListDefaultsView{ListDefaults: defaults, Configured: settings.Configured})
}

// handleFluvListDefaultsShow answers GET /api/v1/integrations/fluv/lists/{id}:
// how the caller's purchases from the list are filed in their Fluv.
func (app *Application) handleFluvListDefaultsShow(w http.ResponseWriter, r *http.Request) {
	if !app.fluvAvailable(w) {
		return
	}
	list, ok := app.loadFluvList(w, r)
	if !ok {
		return
	}
	defaults, err := app.Fluv.ListDefaults(r.Context(), userFromContext(r).ID, list.ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.fluvListDefaultsView(w, r, defaults)
}

// handleFluvListDefaultsSave answers PUT /api/v1/integrations/fluv/lists/{id}:
// replaces them (all three fields; "" leaves one to Fluv). A refused value
// is a 400 with a code, like handleFluvSave.
func (app *Application) handleFluvListDefaultsSave(w http.ResponseWriter, r *http.Request) {
	if !app.fluvAvailable(w) {
		return
	}
	list, ok := app.loadFluvList(w, r)
	if !ok {
		return
	}
	var in fluv.ListDefaults
	if !decodeJSON(w, r, &in) {
		return
	}
	defaults, err := app.Fluv.SaveListDefaults(r.Context(), userFromContext(r).ID, list.ID, in)
	var verr *fluv.ValidationError
	if errors.As(err, &verr) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": verr.Message, "code": verr.Code, "field": verr.Field})
		return
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.fluvListDefaultsView(w, r, defaults)
}

// loadFluvItem reads the item a /api/v1/integrations/fluv/items/{id}
// request is about, and its list, writing the error and reporting false
// when the caller may not see it. Read access is enough: what changes is
// the caller's own purchase, in their own Fluv, not the item.
func (app *Application) loadFluvItem(w http.ResponseWriter, r *http.Request) (*models.List, *models.Item, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, nil, false
	}
	item, err := app.DB.GetItem(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return nil, nil, false
	} else if err != nil {
		app.serverError(w, r, err)
		return nil, nil, false
	}
	list, err := app.DB.GetList(r.Context(), item.ListID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return nil, nil, false
	} else if err != nil {
		app.serverError(w, r, err)
		return nil, nil, false
	}
	if !app.authorizeListAccess(w, r, list, false) {
		return nil, nil, false
	}
	return list, item, true
}

// handleFluvItemShow answers GET /api/v1/integrations/fluv/items/{id}: how
// the caller's purchase of the item stands in their Fluv — all false when
// nothing of theirs is sent for it.
func (app *Application) handleFluvItemShow(w http.ResponseWriter, r *http.Request) {
	if !app.fluvAvailable(w) {
		return
	}
	_, item, ok := app.loadFluvItem(w, r)
	if !ok {
		return
	}
	view, err := app.Fluv.ItemPurchase(r.Context(), userFromContext(r).ID, item)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleFluvItemSave answers PUT /api/v1/integrations/fluv/items/{id}: the
// caller's purchase of the item filed in another Fluv account, taken out of
// Fluv ("skip": true) or sent again. 404 "no_purchase" when they have none
// to change.
func (app *Application) handleFluvItemSave(w http.ResponseWriter, r *http.Request) {
	if !app.fluvAvailable(w) {
		return
	}
	list, item, ok := app.loadFluvItem(w, r)
	if !ok {
		return
	}
	var in fluv.ItemPurchaseUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	view, err := app.Fluv.SetItemPurchase(r.Context(), userFromContext(r).ID, list, item, in)
	var verr *fluv.ValidationError
	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": verr.Message, "code": verr.Code, "field": verr.Field})
	case errors.Is(err, fluv.ErrNoPurchase):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "you have no purchase of this item to change", "code": "no_purchase"})
	case err != nil:
		app.serverError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, view)
	}
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
