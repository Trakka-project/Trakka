package handlers

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"trakka/internal/logbuffer"
	"trakka/internal/models"
)

// TestHandleAdminLogsListRequiresAdminAndNilSafe confirms the gate, that a
// nil LogBuffer (e.g. an Application built without one, as most other
// handler tests do) is handled gracefully rather than panicking, and that
// a populated buffer round-trips through the endpoint most-recent-first.
func TestHandleAdminLogsListRequiresAdminAndNilSafe(t *testing.T) {
	app := newTestApplication(t)
	admin := mustCreateTestUser(t, app, "admin@example.com")

	nonAdminReq := withUser(httptest.NewRequest(http.MethodGet, "/api/v1/admin/logs", nil), &models.User{ID: 999, IsAdmin: false})
	rec := httptest.NewRecorder()
	app.handleAdminLogsList(rec, nonAdminReq)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-admin, got %d: %s", rec.Code, rec.Body.String())
	}

	// app.LogBuffer is nil here (newTestApplication doesn't set one) — must
	// not panic, and must report an empty list.
	adminReq := withUser(httptest.NewRequest(http.MethodGet, "/api/v1/admin/logs", nil), admin)
	rec = httptest.NewRecorder()
	app.handleAdminLogsList(rec, adminReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with a nil LogBuffer, got %d: %s", rec.Code, rec.Body.String())
	}
	var entries []logbuffer.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries with a nil LogBuffer, got %d", len(entries))
	}

	// Now with a real, populated buffer.
	h := logbuffer.NewHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil), 10)
	logger := slog.New(h)
	logger.Info("first")
	logger.Warn("second")
	app.LogBuffer = h

	adminReq = withUser(httptest.NewRequest(http.MethodGet, "/api/v1/admin/logs", nil), admin)
	rec = httptest.NewRecorder()
	app.handleAdminLogsList(rec, adminReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(entries) != 2 || entries[0].Message != "second" {
		t.Fatalf("expected 2 entries, most recent first, got %+v", entries)
	}
}
