package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"trakka/internal/db"
	"trakka/internal/models"
)

// Task assignments (models.Item.Assignees, migration 34): POST/PUT/PATCH
// /api/v1/items take assignee_ids, the user ids the item is assigned to.
// Who those may be depends on the deployment (config.Config.SaaSMode, see
// db.AssigneeScope):
//
//   - SaaS mode: the members of the list's House, and only a member may
//     send assignee_ids — accounts on the instance don't know each other,
//     so a list-share recipient sees the assignees on the items but never
//     the House roster, and can't change them (403);
//   - self-hosted: anyone who can open the list — its House's members, the
//     recipients of a share of the list or of its Space — and anyone who
//     may edit the list's items may assign them. A household that keeps
//     personal lists out of a shared House, and shares a Space ("Maison")
//     or a list instead, can still split its chores.
//
// An id outside that set is a 400. Both checks run before the request
// writes anything, so it is never half-applied. GET
// /api/v1/lists/{id}/assignable-users lists the set, for the pickers.
//
// The set shrinks when someone leaves the House, a share is revoked or the
// list moves to another Space: whoever falls out of it is unassigned right
// away (pruneAssignees), and at startup (db.PruneAssignees), which also
// covers a change of SAAS_MODE.

var (
	errInvalidAssigneeID = errors.New("assignee_ids must be positive user ids")
	errTooManyAssignees  = fmt.Errorf("an item can be assigned to at most %d people", models.MaxItemAssignees)
	errAssignNotMember   = errors.New("only members of the list's house can assign tasks")
)

// assigneeScope is the db.AssigneeScope this deployment assigns tasks
// within.
func (app *Application) assigneeScope() db.AssigneeScope {
	return db.AssigneeScopeFor(app.Config.SaaSMode)
}

// cleanAssigneeIDs validates the assignee_ids of a request and drops
// duplicates, keeping the order the ids were first given in.
func cleanAssigneeIDs(ids []int64) ([]int64, error) {
	seen := make(map[int64]bool, len(ids))
	clean := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, errInvalidAssigneeID
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	if len(clean) > models.MaxItemAssignees {
		return nil, errTooManyAssignees
	}
	return clean, nil
}

// canAssignIn reports whether the caller, who may already edit list's
// items, may also send assignee_ids on them: in SaaS mode only a member of
// the list's House may; self-hosted, editing is enough.
func (app *Application) canAssignIn(r *http.Request, list *models.List) (bool, error) {
	if app.assigneeScope() == db.AssigneesListAudience {
		return true, nil
	}
	return app.DB.UserCanAccessHouse(r.Context(), userFromContext(r).ID, list.HouseID)
}

// authorizeAssignees writes the response and returns false unless the
// caller, who may edit list's items, may assign them to ids: they may
// assign at all (403 otherwise, see canAssignIn) and every id is one of
// the list's assignable users (400 otherwise).
func (app *Application) authorizeAssignees(w http.ResponseWriter, r *http.Request, list *models.List, ids []int64) bool {
	allowed, err := app.canAssignIn(r, list)
	if err != nil {
		app.serverError(w, r, err)
		return false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, errAssignNotMember.Error())
		return false
	}
	ok, err := app.DB.AreAssignable(r.Context(), list.ID, app.assigneeScope(), ids)
	if err != nil {
		app.serverError(w, r, err)
		return false
	}
	if !ok {
		writeError(w, http.StatusBadRequest, db.ErrInvalidAssignee.Error())
		return false
	}
	return true
}

// authorizeItemAssignees is authorizeAssignees for an existing item, whose
// list the item handlers only know by id.
func (app *Application) authorizeItemAssignees(w http.ResponseWriter, r *http.Request, item *models.Item, ids []int64) bool {
	list, err := app.DB.GetList(r.Context(), item.ListID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return false
	} else if err != nil {
		app.serverError(w, r, err)
		return false
	}
	return app.authorizeAssignees(w, r, list, ids)
}

// saveAssignees writes an item's assignees once the request has been
// authorized (authorizeAssignees). Someone who lost access in the meantime
// still gets the 400 (db.SetItemAssignees checks again). Returns nil after
// writing an error response.
func (app *Application) saveAssignees(w http.ResponseWriter, r *http.Request, itemID int64, ids []int64) *models.Item {
	item, err := app.DB.SetItemAssignees(r.Context(), itemID, ids, app.assigneeScope())
	if errors.Is(err, db.ErrInvalidAssignee) {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil
	} else if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return nil
	} else if err != nil {
		app.serverError(w, r, err)
		return nil
	}
	return item
}

// sameAssignees reports whether item is assigned to exactly ids, whatever
// their order — the "nothing to write" check of PUT/PATCH.
func sameAssignees(item *models.Item, ids []int64) bool {
	if len(item.Assignees) != len(ids) {
		return false
	}
	current := make(map[int64]bool, len(item.Assignees))
	for _, a := range item.Assignees {
		current[a.UserID] = true
	}
	for _, id := range ids {
		if !current[id] {
			return false
		}
	}
	return true
}

// handleListAssignableUsers serves GET /api/v1/lists/{id}/assignable-users:
// who the list's items can be assigned to, and whether the caller may
// assign them (models.AssignableUsers). Anyone who can read the list may
// ask; in SaaS mode the users are only listed for someone who may assign
// (a House member), since the House roster is the House's own business.
func (app *Application) handleListAssignableUsers(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	list, err := app.DB.GetList(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	level, err := app.DB.AccessLevelForList(r.Context(), userFromContext(r).ID, list)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if level == "" {
		writeError(w, http.StatusForbidden, "no access to this list")
		return
	}

	out := models.AssignableUsers{Users: []models.AssignableUser{}}
	allowed, err := app.canAssignIn(r, list)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	out.CanAssign = allowed && level == "write"
	if allowed {
		if out.Users, err = app.DB.AssignableUsers(r.Context(), list.ID, app.assigneeScope()); err != nil {
			app.serverError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// pruneAssignees runs one of the db.Prune*Assignees after a change that
// can narrow who a list's tasks may be assigned to (a member leaving a
// House, a share revoked, a list moved to another Space). The change
// itself has already succeeded, so a failure here is only logged: the
// assignments it would have removed go at the next startup
// (db.PruneAssignees).
func (app *Application) pruneAssignees(r *http.Request, prune func(db.AssigneeScope) (int, error)) {
	if _, err := prune(app.assigneeScope()); err != nil {
		app.Logger.Error("unassigning users who lost access to a list", "error", err, "path", logPath(r))
	}
}
