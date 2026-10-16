package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"trakka/internal/models"
	"trakka/internal/shoptemplates"
)

// callAs runs handler as user on a request with body (may be "") and the
// given {name: value} path values.
func callAs(user *models.User, handler http.HandlerFunc, method, target, body string, pathValues ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for i := 0; i+1 < len(pathValues); i += 2 {
		req.SetPathValue(pathValues[i], pathValues[i+1])
	}
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, dst any) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status %d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
	}
	if dst != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
			t.Fatalf("decoding %s: %v", rec.Body.String(), err)
		}
	}
}

// templatesFixture: an owner with a House and a list, plus an outsider
// with a House of their own.
func templatesFixture(t *testing.T) (*Application, *models.User, *models.House, *models.User, *models.House) {
	t.Helper()
	app := newTestApplication(t)
	ctx := context.Background()
	owner := mustCreateTestUser(t, app, "owner@example.com")
	house, err := app.DB.CreateHouseWithOwner(ctx, "Maison", owner.ID)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	outsider := mustCreateTestUser(t, app, "outsider@example.com")
	otherHouse, err := app.DB.CreateHouseWithOwner(ctx, "Ailleurs", outsider.ID)
	if err != nil {
		t.Fatalf("creating other house: %v", err)
	}
	return app, owner, house, outsider, otherHouse
}

// TestCreateListFromBuiltinTemplate: POST /api/v1/lists with template_key
// creates the list with every template item — estimate as price, aisle,
// quantity, order — planned, and its budget is the template's total.
func TestCreateListFromBuiltinTemplate(t *testing.T) {
	app, owner, house, _, _ := templatesFixture(t)
	houseID := strconv.FormatInt(house.ID, 10)

	var list models.List
	decodeInto(t, callAs(owner, app.handleListsCreate, http.MethodPost, "/api/v1/lists",
		`{"house_id":`+houseID+`,"name":"","template_key":"back_to_school","lang":"en"}`), http.StatusCreated, &list)

	tmpl, _ := shoptemplates.Builtin(shoptemplates.KeyBackToSchool, "en")
	if list.Name != "Back to School" || list.Type != "groceries" || list.Icon != "🎒" {
		t.Fatalf("name/type/icon should default to the template's, got %q %q %q", list.Name, list.Type, list.Icon)
	}
	if list.Budget == nil || list.Budget.EstimatedTotal != tmpl.EstimatedTotal || list.Budget.SpentTotal != 0 || list.Budget.Variance != 0 {
		t.Fatalf("budget = %+v, want estimated %v and nothing spent", list.Budget, tmpl.EstimatedTotal)
	}
	items, err := app.DB.ListItemsByList(context.Background(), list.ID)
	if err != nil {
		t.Fatalf("ListItemsByList: %v", err)
	}
	if len(items) != len(tmpl.Items) {
		t.Fatalf("got %d items, want %d", len(items), len(tmpl.Items))
	}
	for i, item := range items {
		want := tmpl.Items[i]
		if item.Title != want.Title || item.Quantity != want.Quantity || *item.Price != *want.EstimatedPrice ||
			stringValue(item.Aisle) != want.Aisle || item.PurchaseStatus != models.PurchasePlanned {
			t.Errorf("item %d = %q ×%d %v %v %q, want %+v planned", i, item.Title, item.Quantity, *item.Price, stringValue(item.Aisle), item.PurchaseStatus, *want)
		}
	}

	// Explicit name/type/icon win over the template's.
	decodeInto(t, callAs(owner, app.handleListsCreate, http.MethodPost, "/api/v1/lists",
		`{"house_id":`+houseID+`,"name":"Fournitures Léa","type":"shopping","icon":"✏️","template_key":"back_to_school"}`), http.StatusCreated, &list)
	if list.Name != "Fournitures Léa" || list.Type != "shopping" || list.Icon != "✏️" {
		t.Fatalf("explicit fields were overridden: %+v", list)
	}

	for name, body := range map[string]string{
		"unknown key":  `{"house_id":` + houseID + `,"name":"x","template_key":"nope"}`,
		"key and id":   `{"house_id":` + houseID + `,"name":"x","template_key":"travel","template_id":1}`,
		"no template":  `{"house_id":` + houseID + `,"name":""}`,
		"invalid type": `{"house_id":` + houseID + `,"type":"nope","template_key":"travel"}`,
	} {
		if rec := callAs(owner, app.handleListsCreate, http.MethodPost, "/api/v1/lists", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

// TestHouseTemplateLifecycle: a member saves a list as a House template
// (paid prices becoming the estimates), the House's members see and use it,
// nobody else can, and a member deletes it.
func TestHouseTemplateLifecycle(t *testing.T) {
	app, owner, house, outsider, otherHouse := templatesFixture(t)
	ctx := context.Background()
	list, err := app.DB.CreateList(ctx, "Courses du samedi", "groceries", house.ID, nil, "🧺")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	listID := strconv.FormatInt(list.ID, 10)
	price := 2.0
	milk, err := app.DB.CreateItem(ctx, list.ID, "Lait", nil, 6, &price, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := app.DB.CreateItem(ctx, list.ID, "Pain", nil, 1, nil, false, 1, nil, nil, nil, nil, false, nil, nil, false); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	// Milk was bought at 1.80: that becomes the estimate for next time.
	decodeInto(t, callAs(owner, app.handleItemsPatch, http.MethodPatch, "/api/v1/items/x",
		`{"purchase_status":"bought","actual_price":1.8}`, "id", strconv.FormatInt(milk.ID, 10)), http.StatusOK, nil)

	// A share recipient may read the list, but the template would be the
	// House's: refused.
	if _, err := app.DB.CreateOrUpdateListShare(ctx, list.ID, outsider.ID, "write"); err != nil {
		t.Fatalf("sharing list: %v", err)
	}
	if rec := callAs(outsider, app.handleShoppingTemplatesCreate, http.MethodPost, "/api/v1/shopping-templates",
		`{"list_id":`+listID+`}`); rec.Code != http.StatusForbidden {
		t.Fatalf("share recipient saving a template: status %d, want 403", rec.Code)
	}

	var tmpl models.ShoppingTemplate
	decodeInto(t, callAs(owner, app.handleShoppingTemplatesCreate, http.MethodPost, "/api/v1/shopping-templates",
		`{"list_id":`+listID+`,"name":"  Samedi  "}`), http.StatusCreated, &tmpl)
	if tmpl.ID == 0 || tmpl.Builtin || tmpl.HouseID != house.ID || tmpl.Name != "Samedi" || tmpl.Icon != "🧺" || tmpl.ListType != "groceries" {
		t.Fatalf("unexpected template %+v", tmpl)
	}
	if tmpl.ItemCount != 2 || tmpl.EstimatedTotal != 10.8 || *tmpl.Items[0].EstimatedPrice != 1.8 || tmpl.Items[1].EstimatedPrice != nil {
		t.Fatalf("items/totals = %d, %v, %+v", tmpl.ItemCount, tmpl.EstimatedTotal, tmpl.Items)
	}
	tmplID := strconv.FormatInt(tmpl.ID, 10)

	// Members list it after the built-ins; the outsider only gets the
	// built-ins, and can't ask for this House's.
	var listed []models.ShoppingTemplate
	decodeInto(t, callAs(owner, app.handleShoppingTemplatesIndex, http.MethodGet,
		"/api/v1/shopping-templates?lang=fr&house_id="+strconv.FormatInt(house.ID, 10), ""), http.StatusOK, &listed)
	builtins := len(shoptemplates.Builtins("fr"))
	if len(listed) != builtins+1 || !listed[0].Builtin || listed[0].Name != "Ménage & Entretien" || listed[builtins].ID != tmpl.ID {
		t.Fatalf("owner's templates = %+v", listed)
	}
	decodeInto(t, callAs(outsider, app.handleShoppingTemplatesIndex, http.MethodGet, "/api/v1/shopping-templates?lang=en", ""), http.StatusOK, &listed)
	if len(listed) != builtins || listed[0].Name != "Cleaning & Upkeep" {
		t.Fatalf("outsider's templates = %+v", listed)
	}
	if rec := callAs(outsider, app.handleShoppingTemplatesIndex, http.MethodGet,
		"/api/v1/shopping-templates?house_id="+strconv.FormatInt(house.ID, 10), ""); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider asking for the House's templates: status %d, want 403", rec.Code)
	}

	// Only lists of the template's own House can start from it.
	if rec := callAs(outsider, app.handleListsCreate, http.MethodPost, "/api/v1/lists",
		`{"house_id":`+strconv.FormatInt(otherHouse.ID, 10)+`,"template_id":`+tmplID+`}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("using another House's template: status %d, want 400", rec.Code)
	}
	var fromTemplate models.List
	decodeInto(t, callAs(owner, app.handleListsCreate, http.MethodPost, "/api/v1/lists",
		`{"house_id":`+strconv.FormatInt(house.ID, 10)+`,"template_id":`+tmplID+`}`), http.StatusCreated, &fromTemplate)
	if fromTemplate.Name != "Samedi" || fromTemplate.Budget == nil || fromTemplate.Budget.EstimatedTotal != 10.8 {
		t.Fatalf("list from House template = %+v (budget %+v)", fromTemplate, fromTemplate.Budget)
	}

	// Deleting: not by an outsider, then by a member.
	if rec := callAs(outsider, app.handleShoppingTemplatesDelete, http.MethodDelete, "/", "", "id", tmplID); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider deleting: status %d, want 403", rec.Code)
	}
	decodeInto(t, callAs(owner, app.handleShoppingTemplatesDelete, http.MethodDelete, "/", "", "id", tmplID), http.StatusNoContent, nil)
	if rec := callAs(owner, app.handleShoppingTemplatesDelete, http.MethodDelete, "/", "", "id", tmplID); rec.Code != http.StatusNotFound {
		t.Fatalf("deleting twice: status %d, want 404", rec.Code)
	}
}

func TestSaveTemplateRefusesAnEmptyList(t *testing.T) {
	app, owner, house, _, _ := templatesFixture(t)
	list, err := app.DB.CreateList(context.Background(), "Vide", "groceries", house.ID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	if rec := callAs(owner, app.handleShoppingTemplatesCreate, http.MethodPost, "/api/v1/shopping-templates",
		`{"list_id":`+strconv.FormatInt(list.ID, 10)+`}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

// TestTaskTemplates: task templates fill todo lists only — tasks with their
// recurrence, no price, not planned — GET ?list_type= returns the templates
// of that type's family, and a todo list saved as a template keeps its
// recurrence but not a stray price.
func TestTaskTemplates(t *testing.T) {
	app, owner, house, _, _ := templatesFixture(t)
	ctx := context.Background()
	houseID := strconv.FormatInt(house.ID, 10)
	keys := func(target string) []string {
		t.Helper()
		var templates []models.ShoppingTemplate
		decodeInto(t, callAs(owner, app.handleShoppingTemplatesIndex, http.MethodGet, target, ""), http.StatusOK, &templates)
		out := []string{}
		for _, tmpl := range templates {
			if tmpl.Builtin {
				out = append(out, tmpl.Key)
			} else {
				out = append(out, "house:"+tmpl.Name)
			}
		}
		return out
	}
	tasks := strings.Join([]string{shoptemplates.KeyCleaningTasks, shoptemplates.KeyMovingTasks, shoptemplates.KeyBackToSchoolTasks}, ",")
	purchases := strings.Join([]string{shoptemplates.KeyCleaning, shoptemplates.KeyTravel, shoptemplates.KeyMoving,
		shoptemplates.KeyBackToSchool, shoptemplates.KeyHomeMaintenance}, ",")
	for target, want := range map[string]string{
		"/api/v1/shopping-templates?list_type=todo":               tasks,
		"/api/v1/shopping-templates?list_type=groceries":          purchases,
		"/api/v1/shopping-templates?list_type=recurring_shopping": purchases,
		"/api/v1/shopping-templates?list_type=custom":             "",
		"/api/v1/shopping-templates":                              purchases + "," + tasks,
	} {
		if got := strings.Join(keys(target), ","); got != want {
			t.Errorf("GET %s = %s, want %s", target, got, want)
		}
	}
	if rec := callAs(owner, app.handleShoppingTemplatesIndex, http.MethodGet, "/api/v1/shopping-templates?list_type=nope", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown list_type: status %d, want 400", rec.Code)
	}

	var list models.List
	decodeInto(t, callAs(owner, app.handleListsCreate, http.MethodPost, "/api/v1/lists",
		`{"house_id":`+houseID+`,"template_key":"cleaning_tasks","lang":"fr"}`), http.StatusCreated, &list)
	tmpl, _ := shoptemplates.Builtin(shoptemplates.KeyCleaningTasks, "fr")
	if list.Name != tmpl.Name || list.Type != "todo" || list.Icon != "🧹" || list.Budget != nil {
		t.Fatalf("list from a task template = %+v (budget %+v)", list, list.Budget)
	}
	items, err := app.DB.ListItemsByList(ctx, list.ID)
	if err != nil || len(items) != len(tmpl.Items) {
		t.Fatalf("ListItemsByList = %d items, %v; want %d", len(items), err, len(tmpl.Items))
	}
	for i, item := range items {
		if item.Title != tmpl.Items[i].Title || stringValue(item.RecurrenceRule) != tmpl.Items[i].RecurrenceRule || !item.IsRecurring ||
			item.PurchaseStatus != models.PurchaseToBuy || item.Price != nil || item.DueDate != nil {
			t.Errorf("task %d = %+v, want %+v recurring, not planned", i, item, *tmpl.Items[i])
		}
	}

	// A template never fills a list of another family.
	for name, body := range map[string]string{
		"task template, purchase list": `{"house_id":` + houseID + `,"type":"groceries","template_key":"moving_tasks"}`,
		"task template, free list":     `{"house_id":` + houseID + `,"type":"custom","template_key":"moving_tasks"}`,
		"purchase template, todo list": `{"house_id":` + houseID + `,"type":"todo","template_key":"moving"}`,
	} {
		if rec := callAs(owner, app.handleListsCreate, http.MethodPost, "/api/v1/lists", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}

	// Saving a todo list: recurrence kept, a price it carries from an
	// earlier type dropped.
	todo, err := app.DB.CreateList(ctx, "Routine", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	rule, price := "FREQ=WEEKLY", 3.5
	if _, err := app.DB.CreateItem(ctx, todo.ID, "Sortir les poubelles", nil, 1, &price, false, 0, nil, nil, &rule, nil, false, nil, nil, false); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	var saved models.ShoppingTemplate
	decodeInto(t, callAs(owner, app.handleShoppingTemplatesCreate, http.MethodPost, "/api/v1/shopping-templates",
		`{"list_id":`+strconv.FormatInt(todo.ID, 10)+`}`), http.StatusCreated, &saved)
	if saved.ListType != "todo" || saved.EstimatedTotal != 0 || len(saved.Items) != 1 ||
		saved.Items[0].RecurrenceRule != rule || saved.Items[0].EstimatedPrice != nil {
		t.Fatalf("saved task template = %+v / %+v", saved, saved.Items)
	}
	if got := strings.Join(keys("/api/v1/shopping-templates?list_type=todo&house_id="+houseID), ","); got != tasks+",house:Routine" {
		t.Errorf("task templates with the House's = %s", got)
	}
}
