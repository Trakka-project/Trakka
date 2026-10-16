package handlers

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"trakka/internal/models"
)

// TestItemPurchaseCycle walks one item through planned → to buy → bought
// (with the price paid) → back to buy, over PATCH, checking the list's
// budget along the way, and the requests that must be refused.
func TestItemPurchaseCycle(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)
	listID := strconv.FormatInt(list.ID, 10)

	var item models.Item
	decodeInto(t, callAs(owner, app.handleItemsCreate, http.MethodPost, "/api/v1/items",
		`{"list_id":`+listID+`,"title":"Peinture","quantity":2,"price":30,"purchase_status":"planned"}`), http.StatusCreated, &item)
	if item.PurchaseStatus != models.PurchasePlanned || item.Done {
		t.Fatalf("created planned: got %q (done %v)", item.PurchaseStatus, item.Done)
	}
	id := strconv.FormatInt(item.ID, 10)
	patch := func(body string, wantStatus int) *models.Item {
		t.Helper()
		var out models.Item
		rec := callAs(owner, app.handleItemsPatch, http.MethodPatch, "/api/v1/items/"+id, body, "id", id)
		if wantStatus != http.StatusOK {
			if rec.Code != wantStatus {
				t.Fatalf("PATCH %s: status %d, want %d (%s)", body, rec.Code, wantStatus, rec.Body.String())
			}
			return nil
		}
		decodeInto(t, rec, http.StatusOK, &out)
		return &out
	}
	budgetOf := func() *models.ListBudget {
		t.Helper()
		var shown models.List
		decodeInto(t, callAs(owner, app.handleListsShow, http.MethodGet, "/api/v1/lists/"+listID, "", "id", listID), http.StatusOK, &shown)
		return shown.Budget
	}

	if got := patch(`{"purchase_status":"to_buy"}`, http.StatusOK); got.PurchaseStatus != models.PurchaseToBuy || got.Done {
		t.Fatalf("to_buy: got %q (done %v)", got.PurchaseStatus, got.Done)
	}

	// A paid price needs the item bought — in this request or before.
	patch(`{"actual_price":31}`, http.StatusBadRequest)
	patch(`{"purchase_status":"to_buy","done":true}`, http.StatusBadRequest)
	patch(`{"purchase_status":"sold"}`, http.StatusBadRequest)
	patch(`{"done":true,"actual_price":-1}`, http.StatusBadRequest)

	// Checked off at 34.50 a unit instead of 30: 9 € over budget.
	got := patch(`{"done":true,"actual_price":34.5}`, http.StatusOK)
	if got.PurchaseStatus != models.PurchaseBought || !got.Done || got.ActualPrice == nil || *got.ActualPrice != 34.5 {
		t.Fatalf("bought: got %q, done %v, actual %v", got.PurchaseStatus, got.Done, got.ActualPrice)
	}
	want := models.ListBudget{EstimatedTotal: 60, SpentTotal: 69, ProjectedTotal: 69, Variance: 9, VarianceStatus: "over"}
	if b := budgetOf(); b == nil || *b != want {
		t.Fatalf("budget after an overrun = %+v, want %+v", b, want)
	}

	// Correcting the price while bought; then clearing it means "cost the
	// estimate", which is on budget.
	if got := patch(`{"actual_price":27.5}`, http.StatusOK); *got.ActualPrice != 27.5 {
		t.Fatalf("actual price not updated: %v", got.ActualPrice)
	}
	if b := budgetOf(); b.Variance != -5 || b.VarianceStatus != "under" {
		t.Fatalf("budget after a saving = %+v", b)
	}
	if got := patch(`{"actual_price":null}`, http.StatusOK); got.ActualPrice != nil {
		t.Fatalf("actual price not cleared: %v", got.ActualPrice)
	}
	if b := budgetOf(); b.SpentTotal != 60 || b.Variance != 0 || b.VarianceStatus != "on_budget" {
		t.Fatalf("budget at the estimate = %+v", b)
	}

	// Unchecking starts a new purchase: to buy, the paid price gone.
	patch(`{"actual_price":40}`, http.StatusOK)
	got = patch(`{"done":false}`, http.StatusOK)
	if got.PurchaseStatus != models.PurchaseToBuy || got.ActualPrice != nil {
		t.Fatalf("unchecked: got %q, actual %v", got.PurchaseStatus, got.ActualPrice)
	}
	if b := budgetOf(); b.SpentTotal != 0 || b.RemainingTotal != 60 || b.Variance != 0 {
		t.Fatalf("budget after unchecking = %+v", b)
	}

	// Planned, then bought directly: buying confirms it, so unchecking
	// later brings it back to buy, not planned.
	patch(`{"purchase_status":"planned"}`, http.StatusOK)
	patch(`{"purchase_status":"bought"}`, http.StatusOK)
	if got := patch(`{"purchase_status":"to_buy"}`, http.StatusOK); got.PurchaseStatus != models.PurchaseToBuy {
		t.Fatalf("after planned → bought → to_buy: %q", got.PurchaseStatus)
	}
}

// A new item is planned or to buy; "bought" is reached by checking it off.
func TestItemCreatePurchaseStatus(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)
	listID := strconv.FormatInt(list.ID, 10)

	var item models.Item
	decodeInto(t, callAs(owner, app.handleItemsCreate, http.MethodPost, "/api/v1/items",
		`{"list_id":`+listID+`,"title":"Vis"}`), http.StatusCreated, &item)
	if item.PurchaseStatus != models.PurchaseToBuy {
		t.Fatalf("default status %q, want to_buy", item.PurchaseStatus)
	}
	for _, body := range []string{
		`{"list_id":` + listID + `,"title":"Vis","purchase_status":"bought"}`,
		`{"list_id":` + listID + `,"title":"Vis","purchase_status":"nope"}`,
		`{"list_id":` + listID + `,"title":"Vis","actual_price":3}`,
	} {
		if rec := callAs(owner, app.handleItemsCreate, http.MethodPost, "/api/v1/items", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, rec.Code)
		}
	}
}

// PUT is a full replace: purchase_status, when sent, decides done; when
// omitted the item is bought or to buy per done, and an omitted
// actual_price is cleared.
func TestItemUpdatePurchaseStatus(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)
	price := 4.0
	created, err := app.DB.CreateItem(context.Background(), list.ID, "Ruban", nil, 1, &price, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	id := strconv.FormatInt(created.ID, 10)
	put := func(body string, wantStatus int) *models.Item {
		t.Helper()
		rec := callAs(owner, app.handleItemsUpdate, http.MethodPut, "/api/v1/items/"+id, body, "id", id)
		if wantStatus != http.StatusOK {
			if rec.Code != wantStatus {
				t.Fatalf("PUT %s: status %d, want %d (%s)", body, rec.Code, wantStatus, rec.Body.String())
			}
			return nil
		}
		var out models.Item
		decodeInto(t, rec, http.StatusOK, &out)
		return &out
	}

	if got := put(`{"title":"Ruban","price":4,"purchase_status":"planned"}`, http.StatusOK); got.PurchaseStatus != models.PurchasePlanned {
		t.Fatalf("PUT planned: %q", got.PurchaseStatus)
	}
	got := put(`{"title":"Ruban","price":4,"purchase_status":"bought","actual_price":4.5}`, http.StatusOK)
	if !got.Done || got.PurchaseStatus != models.PurchaseBought || *got.ActualPrice != 4.5 {
		t.Fatalf("PUT bought: done %v, %q, %v", got.Done, got.PurchaseStatus, got.ActualPrice)
	}
	if got := put(`{"title":"Ruban","price":4,"done":true}`, http.StatusOK); got.ActualPrice != nil || got.PurchaseStatus != models.PurchaseBought {
		t.Fatalf("PUT omitting actual_price should clear it: %v (%q)", got.ActualPrice, got.PurchaseStatus)
	}
	put(`{"title":"Ruban","price":4,"actual_price":5}`, http.StatusBadRequest)
	put(`{"title":"Ruban","price":4,"purchase_status":"nope"}`, http.StatusBadRequest)
}
