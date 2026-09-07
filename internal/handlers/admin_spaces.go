package handlers

import (
	"errors"
	"net/http"

	"trakka/internal/db"
)

// handleAdminSpacesList returns every Space (custom_categories row) on the
// instance, across every user, with its owner's identity and how many
// lists currently reference it — the admin "Espaces" panel's data source.
func (app *Application) handleAdminSpacesList(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) {
		return
	}
	categories, err := app.DB.ListAllCustomCategoriesWithOwners(r.Context())
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, categories)
}

// handleAdminSpacesDelete removes any user's Space, regardless of
// ownership — the admin override of DELETE /api/v1/custom-categories/{id},
// which is scoped to the caller's own categories. Any list referencing it
// has custom_category_id reset to NULL (ON DELETE SET NULL) rather than
// being deleted itself, the same as the owner-initiated delete.
func (app *Application) handleAdminSpacesDelete(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := app.DB.DeleteCustomCategory(r.Context(), id); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "custom category not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
