package handlers

import (
	"errors"
	"net/http"

	"trakka/internal/db"
	"trakka/internal/validate"
)

// handleCustomCategoriesIndex lists the caller's own custom categories —
// unlike houses/lists, a category has no shared "membership": it's always
// scoped to the authenticated user, never to a house. ?shared_with_me=true
// switches to a different, mutually exclusive mode mirroring
// handleListsIndex's own: every Space the caller doesn't own but can still
// see (db.ListSpacesVisibleToUser) — either via an explicit space_shares
// grant, or simply because they're a member of a House that uses the Space
// on at least one of its lists (AccessSource "house_member" — see
// db.spaceAccessibleViaHouse) — rather than the caller's own categories.
// This is what lets a Space someone shared with the caller, or a Space
// merely visible through shared House membership (and, once pinned via
// PATCH /api/v1/custom-categories/{id}/share/pin, its lists), show up in
// their own "Espaces" tab at all.
func (app *Application) handleCustomCategoriesIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("shared_with_me") == "true" {
		categories, err := app.DB.ListSpacesVisibleToUser(r.Context(), userFromContext(r).ID)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, categories)
		return
	}

	categories, err := app.DB.ListCustomCategoriesForUser(r.Context(), userFromContext(r).ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, categories)
}

func (app *Application) handleCustomCategoriesCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Icon     string `json:"icon"`
		Color    string `json:"color"`
		Position int    `json:"position"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	in.Name = validate.Text(in.Name)
	if in.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !validate.MaxLen(in.Name, validate.MaxNameLen) {
		writeError(w, http.StatusBadRequest, "name is too long")
		return
	}
	in.Icon = validate.Text(in.Icon)
	if !validate.MaxLen(in.Icon, validate.MaxIconLen) {
		writeError(w, http.StatusBadRequest, "icon is too long")
		return
	}
	cleanColor, err := validate.Color(in.Color)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	category, err := app.DB.CreateCustomCategory(r.Context(), userFromContext(r).ID, in.Name, in.Icon, cleanColor, in.Position)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, category)
}

// handleCustomCategoriesUpdate is a full replace, scoped to the caller's
// own categories — db.UpdateCustomCategoryForUser's WHERE ... AND user_id
// guard is what makes a category owned by someone else come back as
// ErrNotFound here rather than succeeding or leaking a 403 that would
// confirm its existence.
func (app *Application) handleCustomCategoriesUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var in struct {
		Name     string `json:"name"`
		Icon     string `json:"icon"`
		Color    string `json:"color"`
		Position int    `json:"position"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	in.Name = validate.Text(in.Name)
	if in.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !validate.MaxLen(in.Name, validate.MaxNameLen) {
		writeError(w, http.StatusBadRequest, "name is too long")
		return
	}
	in.Icon = validate.Text(in.Icon)
	if !validate.MaxLen(in.Icon, validate.MaxIconLen) {
		writeError(w, http.StatusBadRequest, "icon is too long")
		return
	}
	cleanColor, err := validate.Color(in.Color)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	category, err := app.DB.UpdateCustomCategoryForUser(r.Context(), id, userFromContext(r).ID, in.Name, in.Icon, cleanColor, in.Position)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "custom category not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, category)
}

func (app *Application) handleCustomCategoriesDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := app.DB.DeleteCustomCategoryForUser(r.Context(), id, userFromContext(r).ID); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "custom category not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	// Its lists left it (ON DELETE SET NULL), and with it its share
	// recipients' access — the database no longer knows which lists, hence
	// the whole prune (see assignees.go).
	app.pruneAssignees(r, func(scope db.AssigneeScope) (int, error) {
		return app.DB.PruneAssignees(r.Context(), scope)
	})
	w.WriteHeader(http.StatusNoContent)
}
