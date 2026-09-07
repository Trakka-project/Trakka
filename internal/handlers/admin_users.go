package handlers

import (
	"errors"
	"net/http"

	"trakka/internal/db"
)

// handleAdminUsersList returns every account on the instance — the admin
// "Utilisateurs" panel's data source. Gated behind authorizeAdmin like
// every other /api/v1/admin/... endpoint.
func (app *Application) handleAdminUsersList(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) {
		return
	}
	users, err := app.DB.ListAllUsers(r.Context())
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

type adminUserUpdate struct {
	IsAdmin *bool `json:"is_admin"`
}

// handleAdminUsersUpdate currently only supports toggling is_admin — the
// one attribute of another user's account an admin has any business
// changing from this panel (email/password/display name stay
// self-service, via the ordinary /me and /auth/... endpoints).
//
// Demoting the very last remaining admin is refused (400): per
// db.CreateUser's own doc comment, the only way an instance ever gets an
// admin at all is being the first account ever created — there is no
// separate seeding mechanism or CLI to grant the role back, so losing the
// last one would permanently lock every future admin action out of the
// instance.
func (app *Application) handleAdminUsersUpdate(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var in adminUserUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.IsAdmin == nil {
		writeError(w, http.StatusBadRequest, "is_admin is required")
		return
	}

	if !*in.IsAdmin {
		target, err := app.DB.GetUser(r.Context(), id)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		} else if err != nil {
			app.serverError(w, r, err)
			return
		}
		if target.IsAdmin {
			count, err := app.DB.CountAdmins(r.Context())
			if err != nil {
				app.serverError(w, r, err)
				return
			}
			if count <= 1 {
				writeError(w, http.StatusBadRequest, "cannot remove the last remaining admin")
				return
			}
		}
	}

	user, err := app.DB.SetUserAdmin(r.Context(), id, *in.IsAdmin)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// handleAdminUsersDelete permanently deletes an account (see db.DeleteUser
// for what cascades). Two safeguards beyond the plain admin gate: an admin
// can't delete their own account from this panel (self-service account
// deletion, if ever added, belongs on /me — not here, and deleting the
// account you're currently authenticated as mid-session would be
// confusing at best), and the last remaining admin can't be deleted, for
// the same "no way to grant the role back" reasoning handleAdminUsersUpdate
// documents.
func (app *Application) handleAdminUsersDelete(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if id == userFromContext(r).ID {
		writeError(w, http.StatusBadRequest, "cannot delete your own account from the admin console")
		return
	}

	target, err := app.DB.GetUser(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if target.IsAdmin {
		count, err := app.DB.CountAdmins(r.Context())
		if err != nil {
			app.serverError(w, r, err)
			return
		}
		if count <= 1 {
			writeError(w, http.StatusBadRequest, "cannot delete the last remaining admin")
			return
		}
	}

	if err := app.DB.DeleteUser(r.Context(), id); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
