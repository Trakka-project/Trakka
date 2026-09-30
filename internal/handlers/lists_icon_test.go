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

// TestListIconRoundTripsComplexEmoji checks that an icon chosen in the emoji
// picker — here a ZWJ sequence carrying two skin-tone modifiers, the longest
// kind the picker offers — survives create and update byte for byte, through
// validation and SQLite, and comes back unchanged from a fresh read.
func TestListIconRoundTripsComplexEmoji(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()

	owner := mustCreateTestUser(t, app, "owner@example.com")
	house, err := app.DB.CreateHouseWithOwner(ctx, "Maison", owner.ID)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}

	const created = "\U0001F469\U0001F3FB\u200d❤\ufe0f\u200d\U0001F48B\u200d\U0001F468\U0001F3FC"
	const updated = "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F"

	body, err := json.Marshal(map[string]any{"name": "Courses", "type": "shopping", "icon": created, "house_id": house.ID})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/lists", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
	rec := httptest.NewRecorder()
	app.handleListsCreate(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var list models.List
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if list.Icon != created {
		t.Fatalf("create: icon came back as %+q, want %+q", list.Icon, created)
	}

	idStr := strconv.FormatInt(list.ID, 10)
	body, err = json.Marshal(map[string]any{"name": "Courses", "type": "shopping", "icon": updated})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPut, "/api/v1/lists/"+idStr, strings.NewReader(string(body)))
	req.SetPathValue("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
	rec = httptest.NewRecorder()
	app.handleListsUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	stored, err := app.DB.GetList(ctx, list.ID)
	if err != nil {
		t.Fatalf("reading list back: %v", err)
	}
	if stored.Icon != updated {
		t.Fatalf("update: stored icon is %+q, want %+q", stored.Icon, updated)
	}
}
