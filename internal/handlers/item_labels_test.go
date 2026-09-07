package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestHandleItemsCreateLabels covers handleItemsCreate: labels sent on
// creation are cleaned (internal/validate.Labels — trimmed, deduplicated
// case-insensitively) and persisted, and an item created without any labels
// reports an empty, non-nil array rather than omitting the field.
func TestHandleItemsCreateLabels(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)

	t.Run("labels are cleaned and persisted", func(t *testing.T) {
		body := `{"list_id":` + strconv.FormatInt(list.ID, 10) + `,"title":"Yaourts","labels":["  Bio  ","Promo","bio"]}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/items", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsCreate(rec, req)

		item := decodeItemResponse(t, rec)
		if !reflect.DeepEqual(item.Labels, []string{"Bio", "Promo"}) {
			t.Fatalf("expected labels [Bio Promo], got %v", item.Labels)
		}
	})

	t.Run("no labels field yields an empty, non-nil array", func(t *testing.T) {
		body := `{"list_id":` + strconv.FormatInt(list.ID, 10) + `,"title":"Pain"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/items", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsCreate(rec, req)

		item := decodeItemResponse(t, rec)
		if item.Labels == nil || len(item.Labels) != 0 {
			t.Fatalf("expected an empty, non-nil label set, got %#v", item.Labels)
		}
	})

	t.Run("a label over the length ceiling is rejected", func(t *testing.T) {
		body := `{"list_id":` + strconv.FormatInt(list.ID, 10) + `,"title":"Chaise","labels":["` + strings.Repeat("a", 31) + `"]}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/items", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsCreate(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestHandleItemsUpdateLabels covers handleItemsUpdate (PUT): a full replace
// resets labels back to empty when the field is omitted, matching how
// type/custom_category_id/icon already behave on PUT.
func TestHandleItemsUpdateLabels(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)

	item, err := app.DB.CreateItem(context.Background(), list.ID, "Fromage", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	if _, err := app.DB.SetItemLabels(context.Background(), item.ID, []string{"Bio"}); err != nil {
		t.Fatalf("SetItemLabels: %v", err)
	}
	idStr := strconv.FormatInt(item.ID, 10)

	t.Run("PUT with labels replaces the set", func(t *testing.T) {
		body := `{"title":"Fromage","quantity":1,"labels":["Promo","Local"]}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/items/"+idStr, strings.NewReader(body))
		req.SetPathValue("id", idStr)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsUpdate(rec, req)

		updated := decodeItemResponse(t, rec)
		if !reflect.DeepEqual(updated.Labels, []string{"Promo", "Local"}) {
			t.Fatalf("expected labels [Promo Local], got %v", updated.Labels)
		}
	})

	t.Run("PUT omitting labels resets it to empty", func(t *testing.T) {
		body := `{"title":"Fromage","quantity":1}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/items/"+idStr, strings.NewReader(body))
		req.SetPathValue("id", idStr)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsUpdate(rec, req)

		updated := decodeItemResponse(t, rec)
		if updated.Labels == nil || len(updated.Labels) != 0 {
			t.Fatalf("expected an empty, non-nil label set after an omitted PUT, got %#v", updated.Labels)
		}
	})
}

// TestHandleItemsPatchLabels covers handleItemsPatch: labels are only
// touched when the field is actually present in the request body — an
// unrelated PATCH (e.g. toggling done) must leave a previously set label set
// untouched.
func TestHandleItemsPatchLabels(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)

	item, err := app.DB.CreateItem(context.Background(), list.ID, "Oeufs", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	idStr := strconv.FormatInt(item.ID, 10)

	t.Run("PATCH with labels sets them", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/items/"+idStr, strings.NewReader(`{"labels":["Bio"]}`))
		req.SetPathValue("id", idStr)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsPatch(rec, req)

		updated := decodeItemResponse(t, rec)
		if !reflect.DeepEqual(updated.Labels, []string{"Bio"}) {
			t.Fatalf("expected labels [Bio], got %v", updated.Labels)
		}
	})

	t.Run("PATCH without labels leaves the set untouched", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/items/"+idStr, strings.NewReader(`{"quantity":2}`))
		req.SetPathValue("id", idStr)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsPatch(rec, req)

		updated := decodeItemResponse(t, rec)
		if !reflect.DeepEqual(updated.Labels, []string{"Bio"}) {
			t.Fatalf("expected labels to remain [Bio], got %v", updated.Labels)
		}
	})

	t.Run("PATCH with an explicit empty array clears the set", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/items/"+idStr, strings.NewReader(`{"labels":[]}`))
		req.SetPathValue("id", idStr)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
		rec := httptest.NewRecorder()
		app.handleItemsPatch(rec, req)

		updated := decodeItemResponse(t, rec)
		if updated.Labels == nil || len(updated.Labels) != 0 {
			t.Fatalf("expected an empty, non-nil label set, got %#v", updated.Labels)
		}
	})
}
