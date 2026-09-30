package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleMeResolvesDefaultLanguage exercises resolveUserLanguage: an
// account that never set its own language preference (users.language still
// "") must come back from GET /api/v1/me carrying the instance's configured
// DEFAULT_APP_LANGUAGE, not an empty string.
func TestHandleMeResolvesDefaultLanguage(t *testing.T) {
	app := newTestApplication(t)
	app.Config.DefaultAppLanguage = "en"

	user := mustCreateTestUser(t, app, "default-lang@example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.handleMe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleMe: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Language string `json:"language"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Language != "en" {
		t.Fatalf("expected the instance default %q for an account with no preference, got %q", "en", got.Language)
	}
}

// TestHandleMeUpdateSetsLanguage exercises PATCH /api/v1/me's language field:
// a valid choice is persisted and echoed back verbatim (no default
// substitution needed once the account has an explicit preference), and an
// unsupported value is rejected with 400 rather than silently stored.
func TestHandleMeUpdateSetsLanguage(t *testing.T) {
	app := newTestApplication(t)
	app.Config.DefaultAppLanguage = "en"

	user := mustCreateTestUser(t, app, "patch-lang@example.com")

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/me", strings.NewReader(`{"language":"fr"}`))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	app.handleMeUpdate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleMeUpdate: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Language string `json:"language"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Language != "fr" {
		t.Fatalf("expected the persisted choice %q, got %q", "fr", got.Language)
	}

	reloaded, err := app.DB.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("reloading user: %v", err)
	}
	if reloaded.Language != "fr" {
		t.Fatalf("expected users.language to persist as %q, got %q", "fr", reloaded.Language)
	}

	badReq := httptest.NewRequest(http.MethodPatch, "/api/v1/me", strings.NewReader(`{"language":"de"}`))
	badReq = badReq.WithContext(context.WithValue(badReq.Context(), userContextKey, user))
	badRec := httptest.NewRecorder()
	app.handleMeUpdate(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unsupported language, got %d %s", badRec.Code, badRec.Body.String())
	}

	// The rejected request must not have overwritten the earlier, valid
	// choice.
	stillFrench, err := app.DB.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("reloading user: %v", err)
	}
	if stillFrench.Language != "fr" {
		t.Fatalf("expected the rejected PATCH to leave language as %q, got %q", "fr", stillFrench.Language)
	}
}

// TestHandleMeUpdateReminderAtDueTime covers the "at the exact due time"
// reminder preset: it is saved with the offset/time pair, the pair alone
// resets it to false, and it can't be sent without the pair.
func TestHandleMeUpdateReminderAtDueTime(t *testing.T) {
	app := newTestApplication(t)
	user := mustCreateTestUser(t, app, "reminder@example.com")

	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/me", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
		rec := httptest.NewRecorder()
		app.handleMeUpdate(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) (atDueTime bool, timeOfDay string) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("handleMeUpdate: %d %s", rec.Code, rec.Body.String())
		}
		var got struct {
			AtDueTime bool   `json:"reminder_default_at_due_time"`
			Time      string `json:"reminder_default_time"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		return got.AtDueTime, got.Time
	}

	if atDueTime, timeOfDay := decode(patch(`{"reminder_default_offset_days":0,"reminder_default_time":"08:30","reminder_default_at_due_time":true}`)); !atDueTime || timeOfDay != "08:30" {
		t.Fatalf("got at_due_time=%v time=%q, want true/08:30", atDueTime, timeOfDay)
	}
	if atDueTime, _ := decode(patch(`{"reminder_default_offset_days":1,"reminder_default_time":"20:00"}`)); atDueTime {
		t.Fatal("expected the offset/time pair alone to reset the at-due-time preset")
	}
	if rec := patch(`{"reminder_default_at_due_time":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without the offset/time pair, got %d", rec.Code)
	}
}

// TestHandleMeUpdateVibrateOnNotification covers PATCH /api/v1/me's
// vibrate_on_notification field: on by default, persisted when turned off,
// and left untouched by a PATCH that doesn't mention it.
func TestHandleMeUpdateVibrateOnNotification(t *testing.T) {
	app := newTestApplication(t)
	user := mustCreateTestUser(t, app, "vibrate@example.com")
	if !user.VibrateOnNotification {
		t.Fatalf("expected vibrate_on_notification to default to true")
	}

	patch := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/me", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
		rec := httptest.NewRecorder()
		app.handleMeUpdate(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("handleMeUpdate(%s): %d %s", body, rec.Code, rec.Body.String())
		}
	}

	patch(`{"vibrate_on_notification":false}`)
	patch(`{"keep_last_page":false}`)

	reloaded, err := app.DB.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("reloading user: %v", err)
	}
	if reloaded.VibrateOnNotification {
		t.Fatalf("expected vibrate_on_notification to persist as false")
	}
}
