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

func withUser(req *http.Request, user *models.User) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), userContextKey, user))
}

// TestHandleAdminUsersListRequiresAdmin confirms the endpoint is gated
// behind IsAdmin like every other /api/v1/admin/... route, and that a
// genuine admin gets every account back.
func TestHandleAdminUsersListRequiresAdmin(t *testing.T) {
	app := newTestApplication(t)
	admin := mustCreateTestUser(t, app, "admin@example.com") // first user created: admin
	_ = mustCreateTestUser(t, app, "member@example.com")

	nonAdminReq := withUser(httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil), &models.User{ID: 999, IsAdmin: false})
	rec := httptest.NewRecorder()
	app.handleAdminUsersList(rec, nonAdminReq)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-admin, got %d: %s", rec.Code, rec.Body.String())
	}

	adminReq := withUser(httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil), admin)
	rec = httptest.NewRecorder()
	app.handleAdminUsersList(rec, adminReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin, got %d: %s", rec.Code, rec.Body.String())
	}
	var users []models.User
	if err := json.Unmarshal(rec.Body.Bytes(), &users); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
}

// TestHandleAdminUsersUpdatePromoteAndDemote exercises the is_admin toggle,
// and confirms demoting the last remaining admin is refused.
func TestHandleAdminUsersUpdatePromoteAndDemote(t *testing.T) {
	app := newTestApplication(t)
	admin := mustCreateTestUser(t, app, "admin@example.com")
	member := mustCreateTestUser(t, app, "member@example.com")

	// Promote member.
	memberIDStr := strconv.FormatInt(member.ID, 10)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/users/"+memberIDStr, strings.NewReader(`{"is_admin":true}`))
	req.SetPathValue("id", memberIDStr)
	req = withUser(req, admin)
	rec := httptest.NewRecorder()
	app.handleAdminUsersUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 promoting member, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated models.User
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !updated.IsAdmin {
		t.Fatal("expected member to now be an admin")
	}

	// Demote the original admin: fine, since there are now 2 admins.
	adminIDStr := strconv.FormatInt(admin.ID, 10)
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/admin/users/"+adminIDStr, strings.NewReader(`{"is_admin":false}`))
	req.SetPathValue("id", adminIDStr)
	req = withUser(req, admin)
	rec = httptest.NewRecorder()
	app.handleAdminUsersUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 demoting the original admin (a second admin exists), got %d: %s", rec.Code, rec.Body.String())
	}

	// Now member is the sole admin — demoting them must be refused.
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/admin/users/"+memberIDStr, strings.NewReader(`{"is_admin":false}`))
	req.SetPathValue("id", memberIDStr)
	req = withUser(req, admin) // caller's own admin status is irrelevant to this check
	rec = httptest.NewRecorder()
	app.handleAdminUsersUpdate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 demoting the last remaining admin, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminUsersDeleteSafeguards exercises the two refusals specific
// to this endpoint: deleting your own account, and deleting the last
// remaining admin.
func TestHandleAdminUsersDeleteSafeguards(t *testing.T) {
	app := newTestApplication(t)
	admin := mustCreateTestUser(t, app, "admin@example.com")
	member := mustCreateTestUser(t, app, "member@example.com")

	// Cannot delete your own account from here.
	adminIDStr := strconv.FormatInt(admin.ID, 10)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/users/"+adminIDStr, nil)
	req.SetPathValue("id", adminIDStr)
	req = withUser(req, admin)
	rec := httptest.NewRecorder()
	app.handleAdminUsersDelete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 self-deleting, got %d: %s", rec.Code, rec.Body.String())
	}

	// admin is the only admin in the database. Since any real caller
	// reaching this handler must itself be an admin (authorizeAdmin), and
	// the database has exactly one, a genuine non-self "delete the last
	// admin" request is only reachable if the caller IS that admin — i.e.
	// it collapses into the self-delete case already covered above. To
	// still exercise the safeguard independently of that check, this uses
	// a synthetic caller object (id different from admin's, IsAdmin forced
	// true) the same way this test file bypasses RequireSession/real
	// session-backed auth entirely — see newTestApplication's own doc
	// comment on why handler tests call methods directly.
	other := mustCreateTestUser(t, app, "other@example.com")
	syntheticCaller := *other
	syntheticCaller.IsAdmin = true
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/users/"+adminIDStr, nil)
	req.SetPathValue("id", adminIDStr)
	req = withUser(req, &syntheticCaller)
	rec = httptest.NewRecorder()
	app.handleAdminUsersDelete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 deleting the last remaining admin, got %d: %s", rec.Code, rec.Body.String())
	}

	// Deleting a non-admin member is fine.
	memberIDStr := strconv.FormatInt(member.ID, 10)
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/users/"+memberIDStr, nil)
	req.SetPathValue("id", memberIDStr)
	req = withUser(req, admin)
	rec = httptest.NewRecorder()
	app.handleAdminUsersDelete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting a non-admin member, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := app.DB.GetUser(context.Background(), member.ID); err == nil {
		t.Fatal("expected the deleted member to be gone")
	}

	// Deleting an already-gone id is a 404, not a repeat success.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/users/"+memberIDStr, nil)
	req.SetPathValue("id", memberIDStr)
	req = withUser(req, admin)
	rec = httptest.NewRecorder()
	app.handleAdminUsersDelete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 re-deleting an already-deleted user, got %d: %s", rec.Code, rec.Body.String())
	}
}
