package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/shoptemplates"
	"trakka/internal/validate"
)

// List templates — ready-made lists a new one can start from (see
// models.ShoppingTemplate):
//
//   - the built-in ones (internal/shoptemplates: purchase templates for
//     cleaning, travel, moving, back to school, home maintenance; task
//     templates for cleaning chores, moving, back-to-school paperwork),
//     translated, identified by key;
//   - a House's own, saved from one of its lists by a member (POST
//     /api/v1/shopping-templates) and usable by every member, identified by
//     id. They belong to the House like its product catalog does, so only
//     its members read, use or delete them — a list-share recipient can't.
//
// A template only fills lists of its own family (models.ListTemplateFamily):
// a purchase template a shopping, groceries or recurring_shopping list, a
// task template a todo one. POST /api/v1/lists with template_key or
// template_id creates the list with a copy of the template's items, planned
// in a purchase list (see handleListsCreate).

// maxTemplateItems bounds what a saved template (and so a list created
// from it) may hold; maxTemplatesPerHouse bounds how many a House keeps.
const (
	maxTemplateItems     = 500
	maxTemplatesPerHouse = 100
)

// templateLanguage picks the language built-in templates are served in:
// the one the request names when supported (the frontend sends the
// language it currently shows), else the account's own, else the
// instance default.
func (app *Application) templateLanguage(r *http.Request, requested string) string {
	if lang, ok := validate.Language(requested); ok {
		return lang
	}
	if lang, ok := validate.Language(userFromContext(r).Language); ok {
		return lang
	}
	return app.Config.DefaultAppLanguage
}

// handleShoppingTemplatesIndex returns the built-in templates followed by
// the House templates of every House the caller is a member of — only
// ?house_id='s when given, and only those a list of type ?list_type= may
// start from (its family's) when given.
func (app *Application) handleShoppingTemplatesIndex(w http.ResponseWriter, r *http.Request) {
	listType := r.URL.Query().Get("list_type")
	if listType != "" && !models.ValidListTypes[listType] {
		writeError(w, http.StatusBadRequest, "list_type must be one of 'todo', 'shopping', 'groceries', 'recurring_shopping', 'custom'")
		return
	}
	var houseID int64
	if raw := r.URL.Query().Get("house_id"); raw != "" {
		var err error
		houseID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || houseID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid house_id")
			return
		}
		if !app.authorizeHouseAccess(w, r, houseID) {
			return
		}
	}

	templates := shoptemplates.Builtins(app.templateLanguage(r, r.URL.Query().Get("lang")))
	house, err := app.DB.ListShoppingTemplatesForUser(r.Context(), userFromContext(r).ID, houseID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	for _, t := range house {
		shoptemplates.FillTotals(t)
	}
	templates = append(templates, house...)
	if listType != "" {
		family := models.ListTemplateFamily(listType)
		matching := make([]*models.ShoppingTemplate, 0, len(templates))
		for _, t := range templates {
			if models.ListTemplateFamily(t.ListType) == family {
				matching = append(matching, t)
			}
		}
		templates = matching
	}
	writeJSON(w, http.StatusOK, templates)
}

// handleShoppingTemplatesCreate saves one of a House's lists as a House
// template of the list's type: its items, in list order, with their
// quantity and recurrence rule — and, from a purchase list, their aisle and
// as the estimate, the price actually paid when one was entered (the best
// guess for next time), else the item's estimated price. A task or free
// list's template carries no price or aisle (one the list may hold from an
// earlier type would only resurface in a template of the wrong family).
func (app *Application) handleShoppingTemplatesCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ListID int64  `json:"list_id"`
		Name   string `json:"name"`
		Icon   string `json:"icon"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.ListID <= 0 {
		writeError(w, http.StatusBadRequest, "list_id is required")
		return
	}

	list, err := app.DB.GetList(r.Context(), in.ListID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "list_id does not reference an existing list")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	// Reading the list isn't enough: the template becomes the House's.
	if !app.authorizeListAccess(w, r, list, false) || !app.authorizeHouseAccess(w, r, list.HouseID) {
		return
	}

	name := validate.Text(in.Name)
	if name == "" {
		name = list.Name
	}
	if !validate.MaxLen(name, validate.MaxNameLen) {
		writeError(w, http.StatusBadRequest, "name is too long")
		return
	}
	icon := validate.Text(in.Icon)
	if icon == "" {
		icon = list.Icon
	}
	if !validate.MaxLen(icon, validate.MaxIconLen) {
		writeError(w, http.StatusBadRequest, "icon is too long")
		return
	}

	items, err := app.DB.ListItemsByList(r.Context(), list.ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, "the list has no items to save as a template")
		return
	}
	if len(items) > maxTemplateItems {
		writeError(w, http.StatusBadRequest, "the list has too many items to save as a template")
		return
	}
	count, err := app.DB.CountShoppingTemplates(r.Context(), list.HouseID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if count >= maxTemplatesPerHouse {
		writeError(w, http.StatusBadRequest, "this house already has the maximum number of templates")
		return
	}

	purchase := models.IsPurchaseListType(list.Type)
	templateItems := make([]*models.ShoppingTemplateItem, 0, len(items))
	for _, item := range items {
		templateItem := &models.ShoppingTemplateItem{
			Title:          item.Title,
			Quantity:       item.Quantity,
			RecurrenceRule: stringValue(item.RecurrenceRule),
		}
		if purchase {
			templateItem.EstimatedPrice = item.Price
			if item.ActualPrice != nil {
				templateItem.EstimatedPrice = item.ActualPrice
			}
			templateItem.Aisle = stringValue(item.Aisle)
		}
		templateItems = append(templateItems, templateItem)
	}
	template, err := app.DB.CreateShoppingTemplate(r.Context(), list.HouseID, name, icon, list.Type, userFromContext(r).ID, templateItems)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	shoptemplates.FillTotals(template)
	writeJSON(w, http.StatusCreated, template)
}

// handleShoppingTemplatesDelete deletes a House template; any member of its
// House may, like any member may save one.
func (app *Application) handleShoppingTemplatesDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	template, err := app.DB.GetShoppingTemplate(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "template not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeHouseAccess(w, r, template.HouseID) {
		return
	}
	if err := app.DB.DeleteShoppingTemplate(r.Context(), id); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "template not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveListTemplate finds the template a new list of houseID starts
// from: the built-in one named by key (in lang), or the House template id,
// which must be houseID's own. Neither set is no template (nil, true); both
// set, or one that doesn't resolve, writes a 400 and returns false.
func (app *Application) resolveListTemplate(w http.ResponseWriter, r *http.Request, key string, id *int64, houseID int64, lang string) (*models.ShoppingTemplate, bool) {
	switch {
	case key != "" && id != nil:
		writeError(w, http.StatusBadRequest, "send template_key or template_id, not both")
		return nil, false
	case key != "":
		template, found := shoptemplates.Builtin(key, app.templateLanguage(r, lang))
		if !found {
			writeError(w, http.StatusBadRequest, "template_key does not reference a built-in template")
			return nil, false
		}
		return template, true
	case id != nil:
		// The same answer for a missing template and another House's, so
		// the id of a House the caller isn't in reveals nothing.
		template, err := app.DB.GetShoppingTemplate(r.Context(), *id)
		if errors.Is(err, db.ErrNotFound) || (err == nil && template.HouseID != houseID) {
			writeError(w, http.StatusBadRequest, "template_id does not reference a template of this house")
			return nil, false
		} else if err != nil {
			app.serverError(w, r, err)
			return nil, false
		}
		return template, true
	}
	return nil, true
}
