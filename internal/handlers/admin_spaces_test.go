package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"trakka/internal/db"
)

// TestHandleAdminSpacesListRequiresAdmin confirms the gate and that every
// user's Space (not just the caller's own) comes back.
func TestHandleAdminSpacesListRequiresAdmin(t *testing.T) {
	app := newTestApplication(t)
	admin := mustCreateTestUser(t, app, "admin@example.com")
	owner := mustCreateTestUser(t, app, "owner@example.com")
	ctx := context.Background()

	if _, err := app.DB.CreateCustomCategory(ctx, owner.ID, "Vacances", "🏖️", "#3366ff", 0); err != nil {
		t.Fatalf("creating category: %v", err)
	}

	nonAdminReq := withUser(httptest.NewRequest(http.MethodGet, "/api/v1/admin/spaces", nil), owner)
	rec := httptest.NewRecorder()
	app.handleAdminSpacesList(rec, nonAdminReq)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-admin, got %d: %s", rec.Code, rec.Body.String())
	}

	adminReq := withUser(httptest.NewRequest(http.MethodGet, "/api/v1/admin/spaces", nil), admin)
	rec = httptest.NewRecorder()
	app.handleAdminSpacesList(rec, adminReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin, got %d: %s", rec.Code, rec.Body.String())
	}
	var rows []db.AdminCustomCategoryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(rows) != 1 || rows[0].OwnerEmail != "owner@example.com" {
		t.Fatalf("expected 1 category owned by owner@example.com, got %+v", rows)
	}
}

// TestHandleAdminSpacesDeleteBypassesOwnership confirms an admin can delete
// a Space they don't own — the whole point of the endpoint, distinct from
// DELETE /api/v1/custom-categories/{id} which is owner-scoped.
func TestHandleAdminSpacesDeleteBypassesOwnership(t *testing.T) {
	app := newTestApplication(t)
	admin := mustCreateTestUser(t, app, "admin@example.com")
	owner := mustCreateTestUser(t, app, "owner@example.com")
	ctx := context.Background()

	category, err := app.DB.CreateCustomCategory(ctx, owner.ID, "Vacances", "🏖️", "#3366ff", 0)
	if err != nil {
		t.Fatalf("creating category: %v", err)
	}

	idStr := strconv.FormatInt(category.ID, 10)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/spaces/"+idStr, nil)
	req.SetPathValue("id", idStr)
	req = withUser(req, admin)
	rec := httptest.NewRecorder()
	app.handleAdminSpacesDelete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := app.DB.GetCustomCategory(ctx, category.ID); err == nil {
		t.Fatal("expected the category to be gone")
	}

	// Repeat delete is a 404.
	rec = httptest.NewRecorder()
	app.handleAdminSpacesDelete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 re-deleting an already-deleted category, got %d: %s", rec.Code, rec.Body.String())
	}
}
