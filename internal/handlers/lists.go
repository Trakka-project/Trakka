package handlers

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// handleListsIndex lists the caller's lists. ?shared_with_me=true switches
// to a different, mutually exclusive mode: every List reachable only via a
// list_shares/space_shares grant (see db.ListSharedListsForUser) rather than
// the caller's own House-scoped lists — house_id/type filters don't apply
// to that mode, mirroring how ?house_id= itself is optional below.
// ?pinned_house_spaces=true is a third, equally exclusive mode: every List
// the caller reaches purely through a space_house_pins pin on one of its
// Houses' own Spaces (db.ListPinnedHouseSpaceLists) — deliberately not
// folded into ?shared_with_me=true's own query, since
// ListSharedListsForUser's House-membership exclusion would filter every
// one of these rows straight back out (see that method's own comment). This
// is what lets a pinned House Space's lists show up on the caller's
// dashboard even while a *different* House they also belong to is the one
// currently selected — see the "Pinning shared lists (and shared Spaces)"
// bullet in CLAUDE.md.
//
// ?scope=all is a fourth mode, for integrations (the Home Assistant one,
// docs/HOME_ASSISTANT.md) that want every list the caller can open in one
// request: the House lists of the default mode (all Houses) followed by the
// shared_with_me ones — the three access paths AccessLevelForList combines,
// with no duplicates since ListSharedListsForUser leaves House lists out.
// Every list then says how it's reached: access_source "house_member"
// (as in the pinned_house_spaces mode) with access_permission "write" for
// a House list, the share's own source and permission otherwise. type applies, house_id doesn't.
//
// Two options apply to every mode: ?name= keeps the lists with that name
// (compared case-insensitively), and ?include=items embeds each list's
// items, as GET /api/v1/lists/{id} does.
func (app *Application) handleListsIndex(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	includeItems := false
	if include := query.Get("include"); include == "items" {
		includeItems = true
	} else if include != "" {
		writeError(w, http.StatusBadRequest, "include must be 'items'")
		return
	}
	nameFilter := validate.Text(query.Get("name"))

	lists, ok := app.listsForIndex(w, r)
	if !ok {
		return
	}
	if nameFilter != "" {
		lists = slices.DeleteFunc(lists, func(l *models.List) bool { return !strings.EqualFold(l.Name, nameFilter) })
	}
	if includeItems {
		// One query per list: SQLite answers each from the items(list_id)
		// index in-process, and the lists one person can open stay few.
		for _, l := range lists {
			items, err := app.DB.ListItemsByList(r.Context(), l.ID)
			if err != nil {
				app.serverError(w, r, err)
				return
			}
			l.Items = items
		}
	}
	writeJSON(w, http.StatusOK, lists)
}

// listsForIndex returns the lists of handleListsIndex's mode, before its
// name and include options. On failure it writes the response itself and
// returns ok=false.
func (app *Application) listsForIndex(w http.ResponseWriter, r *http.Request) ([]*models.List, bool) {
	query := r.URL.Query()
	userID := userFromContext(r).ID
	if query.Get("shared_with_me") == "true" {
		lists, err := app.DB.ListSharedListsForUser(r.Context(), userID)
		if err != nil {
			app.serverError(w, r, err)
			return nil, false
		}
		return lists, true
	}
	if query.Get("pinned_house_spaces") == "true" {
		lists, err := app.DB.ListPinnedHouseSpaceLists(r.Context(), userID)
		if err != nil {
			app.serverError(w, r, err)
			return nil, false
		}
		return lists, true
	}

	typeFilter := query.Get("type")
	if typeFilter != "" && !models.ValidListTypes[typeFilter] {
		writeError(w, http.StatusBadRequest, "invalid type filter")
		return nil, false
	}

	switch scope := query.Get("scope"); scope {
	case "":
	case "all":
		if query.Get("house_id") != "" {
			writeError(w, http.StatusBadRequest, "house_id can't be combined with scope=all")
			return nil, false
		}
		lists, err := app.DB.ListListsForUser(r.Context(), userID, typeFilter, 0)
		if err != nil {
			app.serverError(w, r, err)
			return nil, false
		}
		for _, l := range lists {
			l.AccessSource = "house_member"
			l.AccessPermission = "write"
		}
		shared, err := app.DB.ListSharedListsForUser(r.Context(), userID)
		if err != nil {
			app.serverError(w, r, err)
			return nil, false
		}
		for _, l := range shared {
			if typeFilter == "" || l.Type == typeFilter {
				lists = append(lists, l)
			}
		}
		return lists, true
	default:
		writeError(w, http.StatusBadRequest, "scope must be 'all'")
		return nil, false
	}

	var houseID int64
	if houseIDStr := query.Get("house_id"); houseIDStr != "" {
		var err error
		houseID, err = strconv.ParseInt(houseIDStr, 10, 64)
		if err != nil || houseID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid house_id")
			return nil, false
		}
		if !app.authorizeHouseAccess(w, r, houseID) {
			return nil, false
		}
	}

	lists, err := app.DB.ListListsForUser(r.Context(), userID, typeFilter, houseID)
	if err != nil {
		app.serverError(w, r, err)
		return nil, false
	}
	return lists, true
}

// handleListsCreate creates a list, empty or — with template_key (a
// built-in template, served in lang) or template_id (one of the House's own)
// — filled with a copy of a template's items, all planned in a purchase
// list (see shopping_templates.go). With a template, an empty name, type or
// icon takes the template's, and a type outside the template's family
// (models.ListTemplateFamily) is a 400.
func (app *Application) handleListsCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name             string `json:"name"`
		Type             string `json:"type"`
		HouseID          int64  `json:"house_id"`
		CustomCategoryID *int64 `json:"custom_category_id"`
		Icon             string `json:"icon"`
		TemplateKey      string `json:"template_key"`
		TemplateID       *int64 `json:"template_id"`
		Lang             string `json:"lang"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	usesTemplate := in.TemplateKey != "" || in.TemplateID != nil

	in.Name = validate.Text(in.Name)
	if in.Name == "" && !usesTemplate {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !validate.MaxLen(in.Name, validate.MaxNameLen) {
		writeError(w, http.StatusBadRequest, "name is too long")
		return
	}
	if in.Type == "" && !usesTemplate {
		in.Type = "shopping"
	}
	if in.Type != "" && !models.ValidListTypes[in.Type] {
		writeError(w, http.StatusBadRequest, "type must be one of 'todo', 'shopping', 'groceries', 'recurring_shopping', 'custom'")
		return
	}
	in.Icon = validate.Text(in.Icon)
	if !validate.MaxLen(in.Icon, validate.MaxIconLen) {
		writeError(w, http.StatusBadRequest, "icon is too long")
		return
	}
	if in.HouseID <= 0 {
		writeError(w, http.StatusBadRequest, "house_id is required")
		return
	}
	if _, err := app.DB.GetHouse(r.Context(), in.HouseID); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "house_id does not reference an existing house")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeHouseAccess(w, r, in.HouseID) {
		return
	}
	if !app.validateCustomCategoryOwnership(w, r, in.CustomCategoryID) {
		return
	}
	template, ok := app.resolveListTemplate(w, r, in.TemplateKey, in.TemplateID, in.HouseID, in.Lang)
	if !ok {
		return
	}

	var list *models.List
	var err error
	if template == nil {
		list, err = app.DB.CreateList(r.Context(), in.Name, in.Type, in.HouseID, in.CustomCategoryID, in.Icon)
	} else {
		if in.Name == "" {
			in.Name = template.Name
		}
		if in.Type == "" {
			in.Type = template.ListType
		}
		if models.ListTemplateFamily(in.Type) != models.ListTemplateFamily(template.ListType) {
			writeError(w, http.StatusBadRequest, "type does not match the template's list type")
			return
		}
		if in.Icon == "" {
			in.Icon = template.Icon
		}
		list, err = app.DB.CreateListFromTemplate(r.Context(), in.Name, in.Type, in.HouseID, in.CustomCategoryID, in.Icon, template.Items, models.IsPurchaseListType(in.Type))
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, list)
}

// validateCustomCategoryOwnership writes a 400 and returns false if id is
// non-nil but doesn't reference a category owned by the caller (or is
// non-positive). A nil id (meaning "leave unattached"/"dissociate") is
// always valid and returns true without a lookup.
func (app *Application) validateCustomCategoryOwnership(w http.ResponseWriter, r *http.Request, id *int64) bool {
	if id == nil {
		return true
	}
	if *id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid custom_category_id")
		return false
	}
	if _, err := app.DB.GetCustomCategoryForUser(r.Context(), *id, userFromContext(r).ID); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "custom_category_id does not reference a category you own")
		return false
	} else if err != nil {
		app.serverError(w, r, err)
		return false
	}
	return true
}

func (app *Application) handleListsShow(w http.ResponseWriter, r *http.Request) {
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
	if !app.authorizeListAccess(w, r, list, false) {
		return
	}

	items, err := app.DB.ListItemsByList(r.Context(), id)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	list.Items = items

	writeJSON(w, http.StatusOK, list)
}

func (app *Application) handleListsUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	existing, err := app.DB.GetList(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	// Renaming/retyping a list counts as "editing" it, so a write-level
	// List/Space share is enough here — unlike deleting it outright
	// (handleListsDelete below), which stays House-membership-only.
	if !app.authorizeListAccess(w, r, existing, true) {
		return
	}
	// Re-categorizing it is NOT ordinary editing, though: a list's
	// custom_category_id decides which Space grants access to it
	// (db.AccessLevelForList), so whoever can change it can hand the list to
	// a Space of their own and then share that Space with anyone — which
	// would route straight around handleListShareCreate's deliberate "only
	// the list's real House members can extend access to it" rule, and would
	// equally let them revoke everyone else's Space-derived access by
	// detaching it. Changing it therefore requires actual House membership,
	// exactly like deleting the list. A caller who is merely a write-share
	// holder can still rename/retype/re-icon the list; they just cannot move
	// it between Spaces.
	isHouseMember, err := app.DB.UserCanAccessHouse(r.Context(), userFromContext(r).ID, existing.HouseID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	var in struct {
		Name             string `json:"name"`
		Type             string `json:"type"`
		CustomCategoryID *int64 `json:"custom_category_id"`
		Icon             string `json:"icon"`
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
	if in.Type == "" {
		in.Type = "shopping"
	}
	if !models.ValidListTypes[in.Type] {
		writeError(w, http.StatusBadRequest, "type must be one of 'todo', 'shopping', 'groceries', 'recurring_shopping', 'custom'")
		return
	}
	in.Icon = validate.Text(in.Icon)
	if !validate.MaxLen(in.Icon, validate.MaxIconLen) {
		writeError(w, http.StatusBadRequest, "icon is too long")
		return
	}
	if !isHouseMember && !sameCategoryID(in.CustomCategoryID, existing.CustomCategoryID) {
		writeError(w, http.StatusForbidden, "only a member of this list's house can change its space")
		return
	}
	if !app.validateCustomCategoryOwnership(w, r, in.CustomCategoryID) {
		return
	}

	list, err := app.DB.UpdateList(r.Context(), id, in.Name, in.Type, in.CustomCategoryID, in.Icon)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !sameCategoryID(in.CustomCategoryID, existing.CustomCategoryID) {
		// Out of its old Space, the list no longer reaches that Space's
		// share recipients (see assignees.go).
		app.pruneAssignees(r, func(scope db.AssigneeScope) (int, error) {
			return app.DB.PruneListAssignees(r.Context(), scope, id)
		})
	}
	writeJSON(w, http.StatusOK, list)
}

func (app *Application) handleListsDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	existing, err := app.DB.GetList(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	// Deliberately House-membership-only, unlike handleListsUpdate above: a
	// write-level List/Space share lets someone edit a list and its items,
	// not destroy the list itself (and everyone else's items in it).
	if !app.authorizeHouseAccess(w, r, existing.HouseID) {
		return
	}

	if err := app.DB.DeleteList(r.Context(), id); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sameCategoryID reports whether two optional custom_category_id values
// refer to the same category (both unset counts as the same). Used by
// handleListsUpdate to tell an actual re-categorization apart from a
// request that merely round-trips the value it already had.
func sameCategoryID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
