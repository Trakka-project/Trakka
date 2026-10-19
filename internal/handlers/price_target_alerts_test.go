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
)

// TestPriceAlertConditionPure exercises priceAlertCondition directly: it
// should only ever hold when every one of its four inputs (opted in, a
// threshold set, a price present, and that price at or below the
// threshold) is true at once.
func TestPriceAlertConditionPure(t *testing.T) {
	price, target, higher := 10.0, 15.0, 20.0
	cases := []struct {
		name string
		item *models.Item
		want bool
	}{
		{"all conditions met, price under threshold", &models.Item{AlertOnPriceDrop: true, TargetPrice: &target, Price: &price}, true},
		{"price exactly at threshold still counts", &models.Item{AlertOnPriceDrop: true, TargetPrice: &target, Price: &target}, true},
		{"alert disabled", &models.Item{AlertOnPriceDrop: false, TargetPrice: &target, Price: &price}, false},
		{"no threshold set", &models.Item{AlertOnPriceDrop: true, TargetPrice: nil, Price: &price}, false},
		{"no price at all", &models.Item{AlertOnPriceDrop: true, TargetPrice: &target, Price: nil}, false},
		{"price still above threshold", &models.Item{AlertOnPriceDrop: true, TargetPrice: &target, Price: &higher}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := priceAlertCondition(c.item); got != c.want {
				t.Fatalf("priceAlertCondition() = %v, want %v", got, c.want)
			}
		})
	}
}

// itemTestFixture creates a House (with an owner), a shopping List inside
// it, and returns both alongside the owner user — the minimal setup every
// test below needs before it can create/update an item through the
// handlers directly (bypassing Routes()'s mux/middleware, the same pattern
// TestHandleListsReorder and the shares_test.go tests already use).
func itemTestFixture(t *testing.T, app *Application) (*models.User, *models.List) {
	t.Helper()
	ctx := context.Background()
	owner := mustCreateTestUser(t, app, "owner@example.com")
	house, err := app.DB.CreateHouseWithOwner(ctx, "Maison Test", owner.ID)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := app.DB.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}
	return owner, list
}

func decodeItemResponse(t *testing.T, rec *httptest.ResponseRecorder) *models.Item {
	t.Helper()
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	var item models.Item
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("decoding response: %v (body: %s)", err, rec.Body.String())
	}
	return &item
}

// TestHandleItemsCreatePriceAlertTrigger covers handleItemsCreate: an item
// created already at or below its own target price, with the alert opted
// in, reports price_alert_triggered on the very same response — there is
// no "before" state for a brand-new item, so this is necessarily always a
// false→true transition (see checkPriceDropAlert's wasActive contract).
func TestHandleItemsCreatePriceAlertTrigger(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)

	t.Run("price already under threshold triggers on creation", func(t *testing.T) {
		body := `{"list_id":` + strconv.FormatInt(list.ID, 10) + `,"title":"Casque audio","price":15,"target_price":20,"alert_on_price_drop":true}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/items", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsCreate(rec, req)

		item := decodeItemResponse(t, rec)
		if !item.PriceAlertTriggered {
			t.Fatal("expected price_alert_triggered on the creation response")
		}
	})

	t.Run("price above threshold does not trigger", func(t *testing.T) {
		body := `{"list_id":` + strconv.FormatInt(list.ID, 10) + `,"title":"Chaise","price":80,"target_price":20,"alert_on_price_drop":true}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/items", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsCreate(rec, req)

		item := decodeItemResponse(t, rec)
		if item.PriceAlertTriggered {
			t.Fatal("did not expect price_alert_triggered")
		}
	})

	t.Run("alert not opted in does not trigger even under threshold", func(t *testing.T) {
		body := `{"list_id":` + strconv.FormatInt(list.ID, 10) + `,"title":"Lampe","price":5,"target_price":20}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/items", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsCreate(rec, req)

		item := decodeItemResponse(t, rec)
		if item.PriceAlertTriggered {
			t.Fatal("did not expect price_alert_triggered when alert_on_price_drop is false")
		}
	})
}

// TestHandleItemsUpdatePriceAlertTrigger covers handleItemsUpdate (PUT): a
// price drop that newly crosses the threshold triggers once, and a repeat
// PUT that leaves the item still under the same threshold does not
// re-trigger — proving this is a false→true transition, not a level check
// re-evaluated on every request.
func TestHandleItemsUpdatePriceAlertTrigger(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)

	item, err := app.DB.CreateItem(context.Background(), list.ID, "Casque audio", nil, 1, floatPtr(50), false, 0, nil, nil, nil, nil, false, nil, floatPtr(20), true)
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	idStr := strconv.FormatInt(item.ID, 10)

	putItem := func(price float64) *models.Item {
		t.Helper()
		body := `{"title":"Casque audio","quantity":1,"price":` + strconv.FormatFloat(price, 'f', -1, 64) + `,"target_price":20,"alert_on_price_drop":true}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/items/"+idStr, strings.NewReader(body))
		req.SetPathValue("id", idStr)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsUpdate(rec, req)
		return decodeItemResponse(t, rec)
	}

	t.Run("dropping the price below the threshold triggers", func(t *testing.T) {
		updated := putItem(15)
		if !updated.PriceAlertTriggered {
			t.Fatal("expected price_alert_triggered when the price newly crosses the threshold")
		}
	})

	t.Run("a repeat update still under the same threshold does not re-trigger", func(t *testing.T) {
		updated := putItem(10)
		if updated.PriceAlertTriggered {
			t.Fatal("did not expect price_alert_triggered again while the condition was already active")
		}
	})
}

// TestHandleItemsPatchPriceAlertTrigger covers handleItemsPatch: toggling
// alert_on_price_drop on for an item whose price is already under a
// pre-existing target price triggers, even though the price itself didn't
// change in this request — the transition here is in the opt-in flag, not
// the price.
func TestHandleItemsPatchPriceAlertTrigger(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)

	item, err := app.DB.CreateItem(context.Background(), list.ID, "Casque audio", nil, 1, floatPtr(15), false, 0, nil, nil, nil, nil, false, nil, floatPtr(20), false)
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	idStr := strconv.FormatInt(item.ID, 10)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/items/"+idStr, strings.NewReader(`{"alert_on_price_drop":true}`))
	req.SetPathValue("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
	rec := httptest.NewRecorder()
	app.handleItemsPatch(rec, req)

	updated := decodeItemResponse(t, rec)
	if !updated.PriceAlertTriggered {
		t.Fatal("expected price_alert_triggered when opting into an already-satisfied threshold")
	}

	t.Run("patching an unrelated field afterward does not re-trigger", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/items/"+idStr, strings.NewReader(`{"quantity":2}`))
		req.SetPathValue("id", idStr)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsPatch(rec, req)

		again := decodeItemResponse(t, rec)
		if again.PriceAlertTriggered {
			t.Fatal("did not expect price_alert_triggered on an unrelated field edit")
		}
	})
}

func floatPtr(f float64) *float64 { return &f }

// TestCheckPriceDropAlertPushOpensTheItem: the "Bonne affaire" push sent
// when an item's price newly reaches its threshold opens that item in its
// list.
func TestCheckPriceDropAlertPushOpensTheItem(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	_, list := itemTestFixture(t, app)
	item, err := app.DB.CreateItem(context.Background(), list.ID, "Casque audio", nil, 1, floatPtr(15), false, 0, nil, nil, nil, nil, false, nil, floatPtr(20), true)
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	if !app.checkPriceDropAlert(item, false) {
		t.Fatal("expected the alert to trigger")
	}
	want := "/?list=" + strconv.FormatInt(list.ID, 10) + "&item=" + strconv.FormatInt(item.ID, 10)
	if got := pushes.next(t).payload.URL; got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
}
