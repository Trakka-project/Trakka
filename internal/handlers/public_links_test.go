package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"trakka/internal/auth"
	"trakka/internal/db"
	"trakka/internal/models"
)

// publicLinkFixture: a House owner with a list holding one item, driven
// through the full router — the owner with a session cookie, visitors with
// nothing at all. exec reaches the database directly, for the states no
// route sets any more (a suspended link, an expiry already past).
type publicLinkFixture struct {
	*tokenTestEnv
	dbPath string
	owner  *models.User
	cookie *http.Cookie
	list   *models.List
	item   *models.Item
}

func newPublicLinkFixture(t *testing.T, listType string) *publicLinkFixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbPath := filepath.Join(t.TempDir(), "trakka.db")
	d, err := db.Open(dbPath, logger)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	app := &Application{DB: d, Logger: logger}
	app.Auth = auth.NewService(app.DB, nil, time.Hour, false)
	env := &tokenTestEnv{app: app, handler: app.Routes()}

	ctx := context.Background()
	owner := mustCreateTestUser(t, app, "owner@example.com")
	house, err := app.DB.CreateHouseWithOwner(ctx, "Maison", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := app.DB.CreateList(ctx, "Courses du samedi", listType, house.ID, nil, "🛒")
	if err != nil {
		t.Fatal(err)
	}
	return &publicLinkFixture{
		tokenTestEnv: env,
		dbPath:       dbPath,
		owner:        owner,
		cookie:       env.sessionCookie(t, owner),
		list:         list,
		item:         mustCreateItem(t, app, list.ID, "Lait"),
	}
}

// exec runs one statement on the fixture's database, on a connection of its
// own (the driver is the one internal/db registers).
func (f *publicLinkFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	conn, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (f *publicLinkFixture) linksURL() string {
	return "/api/v1/lists/" + strconv.FormatInt(f.list.ID, 10) + "/public-links"
}

func (f *publicLinkFixture) linkURL(linkID int64) string {
	return f.linksURL() + "/" + strconv.FormatInt(linkID, 10)
}

// setLink creates a public link on the list as the owner, from body.
func (f *publicLinkFixture) setLink(t *testing.T, body string, wantStatus int) publicLinkResponse {
	t.Helper()
	var got publicLinkResponse
	decodeInto(t, f.do(http.MethodPost, f.linksURL(), body, "", f.cookie), wantStatus, &got)
	return got
}

// links returns the list's public links, as the owner.
func (f *publicLinkFixture) links(t *testing.T) []publicLinkResponse {
	t.Helper()
	var got []publicLinkResponse
	decodeInto(t, f.do(http.MethodGet, f.linksURL(), "", "", f.cookie), http.StatusOK, &got)
	return got
}

// visitor sends an anonymous request: no cookie, no Authorization header.
func (f *publicLinkFixture) visitor(method, target, body string) *httptest.ResponseRecorder {
	return f.do(method, target, body, "", nil)
}

func publicListURL(token string) string { return "/api/v1/public/lists/" + token }

func publicItemURL(token string, itemID int64) string {
	return publicListURL(token) + "/items/" + strconv.FormatInt(itemID, 10)
}

func TestPublicLinkReadOnlyAccess(t *testing.T) {
	f := newPublicLinkFixture(t, "shopping")
	created := f.setLink(t, `{"access_mode":"read"}`, http.StatusCreated)
	if created.Token == "" || created.Path != "/public/lists/"+created.Token || created.AccessMode != "read" || !created.IsEnabled || created.ID == 0 || created.Label != nil {
		t.Fatalf("unexpected create response %+v", created)
	}

	rec := f.visitor(http.MethodGet, publicListURL(created.Token), "")
	var got publicList
	decodeInto(t, rec, http.StatusOK, &got)
	if got.Name != "Courses du samedi" || got.Icon != "🛒" || got.AccessMode != "read" || len(got.Items) != 1 || got.Items[0].Title != "Lait" {
		t.Fatalf("unexpected public list %+v", got)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if robots := rec.Header().Get("X-Robots-Tag"); !strings.Contains(robots, "noindex") {
		t.Errorf("X-Robots-Tag = %q, want noindex", robots)
	}
	// Nothing about the House, its members or the item's private fields.
	for _, field := range []string{`"house_id"`, `"assignees"`, `"list_id"`, `"url"`, `"price"`, "owner@example.com"} {
		if strings.Contains(rec.Body.String(), field) {
			t.Errorf("public response leaks %s: %s", field, rec.Body.String())
		}
	}

	// Every write through a read-only link is refused, and changes nothing.
	if rec := f.visitor(http.MethodPatch, publicItemURL(created.Token, f.item.ID), `{"done":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("PATCH through a read link: got %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if rec := f.visitor(http.MethodPost, publicListURL(created.Token)+"/items", `{"title":"Pain"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("POST through a read link: got %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	items, err := f.app.DB.ListItemsByList(context.Background(), f.list.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Done {
		t.Fatalf("a refused write changed the list: %+v", items)
	}
}

func TestPublicLinkWriteAccess(t *testing.T) {
	f := newPublicLinkFixture(t, "shopping")
	token := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated).Token

	var checked publicItem
	decodeInto(t, f.visitor(http.MethodPatch, publicItemURL(token, f.item.ID), `{"done":true}`), http.StatusOK, &checked)
	if !checked.Done || checked.ID != f.item.ID {
		t.Fatalf("unexpected PATCH response %+v", checked)
	}
	var renamed publicItem
	decodeInto(t, f.visitor(http.MethodPatch, publicItemURL(token, f.item.ID), `{"title":"Lait demi-écrémé","quantity":3,"done":false}`), http.StatusOK, &renamed)
	if renamed.Done || renamed.Title != "Lait demi-écrémé" || renamed.Quantity != 3 {
		t.Fatalf("unexpected PATCH response %+v", renamed)
	}

	var added publicItem
	decodeInto(t, f.visitor(http.MethodPost, publicListURL(token)+"/items", `{"title":"  Pain  ","quantity":2}`), http.StatusCreated, &added)
	if added.Title != "Pain" || added.Quantity != 2 || added.Done {
		t.Fatalf("unexpected POST response %+v", added)
	}
	stored, err := f.app.DB.GetItem(context.Background(), added.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ListID != f.list.ID || stored.ReminderEnabled || stored.URL != nil {
		t.Fatalf("unexpected stored item %+v", stored)
	}

	// The visitor's view reflects both changes.
	var got publicList
	decodeInto(t, f.visitor(http.MethodGet, publicListURL(token), ""), http.StatusOK, &got)
	if len(got.Items) != 2 || got.AccessMode != "full_edit" {
		t.Fatalf("unexpected public list %+v", got)
	}

	// Bad input is a 400, not a write.
	for _, c := range []struct{ method, target, body string }{
		{http.MethodPost, publicListURL(token) + "/items", `{"title":"   "}`},
		{http.MethodPost, publicListURL(token) + "/items", `{"title":"x","quantity":1000001}`},
		{http.MethodPatch, publicItemURL(token, f.item.ID), `{}`},
		{http.MethodPatch, publicItemURL(token, f.item.ID), `{"title":""}`},
		{http.MethodPatch, publicItemURL(token, f.item.ID), `{"quantity":0}`},
		{http.MethodPatch, publicListURL(token) + "/items/abc", `{"done":true}`},
	} {
		if rec := f.visitor(c.method, c.target, c.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s %s: got %d, want 400 (%s)", c.method, c.target, c.body, rec.Code, rec.Body.String())
		}
	}
	// Fields a visitor may not touch are not part of the request at all.
	if rec := f.visitor(http.MethodPatch, publicItemURL(token, f.item.ID), `{"url":"https://example.com"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH with url: got %d, want 400", rec.Code)
	}
}

func TestPublicLinkWriteCannotReachAnotherList(t *testing.T) {
	f := newPublicLinkFixture(t, "shopping")
	token := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated).Token
	other, err := f.app.DB.CreateList(context.Background(), "Autre", "shopping", f.list.HouseID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	foreign := mustCreateItem(t, f.app, other.ID, "Secret")

	if rec := f.visitor(http.MethodPatch, publicItemURL(token, foreign.ID), `{"done":true}`); rec.Code != http.StatusNotFound {
		t.Fatalf("PATCH of another list's item: got %d, want 404", rec.Code)
	}
	if rec := f.visitor(http.MethodPatch, publicItemURL(token, 999999), `{"done":true}`); rec.Code != http.StatusNotFound {
		t.Fatalf("PATCH of a missing item: got %d, want 404", rec.Code)
	}
	if got, err := f.app.DB.GetItem(context.Background(), foreign.ID); err != nil || got.Done {
		t.Fatalf("the other list's item changed: %+v (%v)", got, err)
	}
}

func TestPublicLinkCrossSiteWriteIsRejected(t *testing.T) {
	f := newPublicLinkFixture(t, "shopping")
	token := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated).Token

	req := httptest.NewRequest(http.MethodPatch, publicItemURL(token, f.item.ID), strings.NewReader(`{"done":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site PATCH: got %d, want 403", rec.Code)
	}
}

func TestPublicLinkWriteLimits(t *testing.T) {
	f := newPublicLinkFixture(t, "todo")
	token := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated).Token

	for i := range publicWriteRateLimit {
		if rec := f.visitor(http.MethodPatch, publicItemURL(token, f.item.ID), `{"done":`+strconv.FormatBool(i%2 == 0)+`}`); rec.Code != http.StatusOK {
			t.Fatalf("write %d: got %d (%s)", i, rec.Code, rec.Body.String())
		}
	}
	if rec := f.visitor(http.MethodPatch, publicItemURL(token, f.item.ID), `{"done":true}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("write past the budget: got %d, want 429", rec.Code)
	}
	// Reads are not budgeted.
	if rec := f.visitor(http.MethodGet, publicListURL(token), ""); rec.Code != http.StatusOK {
		t.Fatalf("read past the write budget: got %d, want 200", rec.Code)
	}
	// The budget is the link's: the list's other link still has its own.
	other := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated).Token
	if rec := f.visitor(http.MethodPatch, publicItemURL(other, f.item.ID), `{"done":false}`); rec.Code != http.StatusOK {
		t.Fatalf("another link of the same list: got %d, want 200", rec.Code)
	}
}

func TestPublicLinkItemCap(t *testing.T) {
	f := newPublicLinkFixture(t, "todo")
	token := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated).Token
	for i := 1; i < maxPublicListItems; i++ { // the fixture's item is the first
		mustCreateItem(t, f.app, f.list.ID, "Tâche "+strconv.Itoa(i))
	}
	if rec := f.visitor(http.MethodPost, publicListURL(token)+"/items", `{"title":"Une de trop"}`); rec.Code != http.StatusConflict {
		t.Fatalf("add to a full list: got %d, want 409", rec.Code)
	}
}

func TestPublicLinkOnCoursesListMergesDuplicates(t *testing.T) {
	f := newPublicLinkFixture(t, "groceries")
	token := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated).Token

	var merged publicItem
	decodeInto(t, f.visitor(http.MethodPost, publicListURL(token)+"/items", `{"title":"lait","quantity":2}`), http.StatusOK, &merged)
	if merged.ID != f.item.ID || merged.Quantity != 3 {
		t.Fatalf("adding a product already on a Courses list must update it, got %+v", merged)
	}
}

func TestPublicListPage(t *testing.T) {
	f := newPublicLinkFixture(t, "shopping")
	f.app.StaticDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(f.app.StaticDir, "public-list.html"), []byte("<!doctype html><title>Liste partagée</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	token := f.setLink(t, `{"access_mode":"read"}`, http.StatusCreated).Token

	rec := f.visitor(http.MethodGet, "/public/lists/"+token, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Liste partagée") {
		t.Fatalf("page: got %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if robots := rec.Header().Get("X-Robots-Tag"); !strings.Contains(robots, "noindex") {
		t.Errorf("X-Robots-Tag = %q, want noindex", robots)
	}
	if rec := f.visitor(http.MethodGet, "/public/lists/bad$token", ""); rec.Code != http.StatusNotFound {
		t.Errorf("malformed token page: got %d, want 404", rec.Code)
	}
}

func TestLogPathRedactsPublicLinkTokens(t *testing.T) {
	for path, want := range map[string]string{
		"/api/v1/public/lists/s3cr3t":          "/api/v1/public/lists/{token}",
		"/api/v1/public/lists/s3cr3t/items":    "/api/v1/public/lists/{token}/items",
		"/api/v1/public/lists/s3cr3t/items/42": "/api/v1/public/lists/{token}/items/42",
		"/public/lists/s3cr3t":                 "/public/lists/{token}",
		"/api/v1/lists/7/public-link":          "/api/v1/lists/7/public-link",
		"/api/v1/calendar/feed.ics":            "/api/v1/calendar/feed.ics",
	} {
		if got := logPath(httptest.NewRequest(http.MethodGet, path, nil)); got != want {
			t.Errorf("logPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// Sanity check on the JSON shape the public page parses.
func TestPublicLinkResponseShape(t *testing.T) {
	f := newPublicLinkFixture(t, "shopping")
	token := f.setLink(t, `{"access_mode":"read"}`, http.StatusCreated).Token
	var raw map[string]json.RawMessage
	decodeInto(t, f.visitor(http.MethodGet, publicListURL(token), ""), http.StatusOK, &raw)
	for _, key := range []string{"name", "type", "access_mode", "expires_at", "items"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing %q in %v", key, raw)
		}
	}
}

// Every mode against every visitor action: what it allows answers like
// full_edit, what it doesn't is a 403 that changes nothing.
func TestPublicLinkAccessModes(t *testing.T) {
	cases := []struct {
		mode               string
		seesItems          bool
		addStatus, patchSt int
	}{
		{"read", true, http.StatusForbidden, http.StatusForbidden},
		{"add_only", true, http.StatusCreated, http.StatusForbidden},
		{"deposit_only", false, http.StatusCreated, http.StatusForbidden},
		{"full_edit", true, http.StatusCreated, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			f := newPublicLinkFixture(t, "shopping")
			token := f.setLink(t, `{"access_mode":"`+c.mode+`"}`, http.StatusCreated).Token

			var got publicList
			decodeInto(t, f.visitor(http.MethodGet, publicListURL(token), ""), http.StatusOK, &got)
			if got.AccessMode != c.mode || got.Name != "Courses du samedi" || got.Icon != "🛒" {
				t.Fatalf("unexpected list %+v", got)
			}
			if c.seesItems != (len(got.Items) == 1) || got.Items == nil {
				t.Fatalf("items: got %+v, want visible=%v (and never null)", got.Items, c.seesItems)
			}

			if rec := f.visitor(http.MethodPost, publicListURL(token)+"/items", `{"title":"Pain"}`); rec.Code != c.addStatus {
				t.Errorf("POST: got %d, want %d (%s)", rec.Code, c.addStatus, rec.Body.String())
			}
			if rec := f.visitor(http.MethodPatch, publicItemURL(token, f.item.ID), `{"done":true}`); rec.Code != c.patchSt {
				t.Errorf("PATCH: got %d, want %d (%s)", rec.Code, c.patchSt, rec.Body.String())
			}

			items, err := f.app.DB.ListItemsByList(context.Background(), f.list.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantItems := 1
			if c.addStatus == http.StatusCreated {
				wantItems = 2
			}
			if len(items) != wantItems {
				t.Errorf("the list holds %d items, want %d", len(items), wantItems)
			}
			for _, it := range items {
				if it.ID == f.item.ID && it.Done != (c.patchSt == http.StatusOK) {
					t.Errorf("the existing item's done = %v after the PATCH", it.Done)
				}
			}
		})
	}
}

// add_only: the visitor sees the list and adds to it, but every change to
// an existing item is refused — title and quantity as much as done.
func TestPublicLinkAddOnlyCannotTouchExistingItems(t *testing.T) {
	f := newPublicLinkFixture(t, "shopping")
	token := f.setLink(t, `{"access_mode":"add_only"}`, http.StatusCreated).Token

	var added publicItem
	decodeInto(t, f.visitor(http.MethodPost, publicListURL(token)+"/items", `{"title":"Pain","quantity":2}`), http.StatusCreated, &added)
	if added.Title != "Pain" || added.Quantity != 2 || added.ID == 0 {
		t.Fatalf("an add_only visitor gets the item back, got %+v", added)
	}
	for _, body := range []string{`{"done":true}`, `{"title":"Autre"}`, `{"quantity":9}`} {
		for _, id := range []int64{f.item.ID, added.ID, 999999} {
			if rec := f.visitor(http.MethodPatch, publicItemURL(token, id), body); rec.Code != http.StatusForbidden {
				t.Errorf("PATCH %d %s: got %d, want 403", id, body, rec.Code)
			}
		}
	}
	stored, err := f.app.DB.GetItem(context.Background(), f.item.ID)
	if err != nil || stored.Done || stored.Title != "Lait" || stored.Quantity != 1 {
		t.Fatalf("a refused PATCH changed the item: %+v (%v)", stored, err)
	}
}

// deposit_only: the visitor never learns what the list holds — not from the
// GET, not from an addition's reply (whether it created an item or, on a
// Courses list, updated one already there), not from a PATCH's status.
func TestPublicLinkDepositOnlyRevealsNothing(t *testing.T) {
	f := newPublicLinkFixture(t, "groceries")
	token := f.setLink(t, `{"access_mode":"deposit_only"}`, http.StatusCreated).Token

	rec := f.visitor(http.MethodGet, publicListURL(token), "")
	var got publicList
	decodeInto(t, rec, http.StatusOK, &got)
	if len(got.Items) != 0 || strings.Contains(rec.Body.String(), "Lait") {
		t.Fatalf("a deposit_only GET must not show the items: %s", rec.Body.String())
	}

	// "lait" is already on the list (merged), "Beurre" is new: same reply.
	for _, title := range []string{"lait", "Beurre"} {
		rec := f.visitor(http.MethodPost, publicListURL(token)+"/items", `{"title":"`+title+`"}`)
		if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != `{"added":true}` {
			t.Errorf("deposit of %q: got %d %s, want 201 {\"added\":true}", title, rec.Code, rec.Body.String())
		}
	}
	// An existing item and a made-up id answer the same 403.
	for _, id := range []int64{f.item.ID, 999999} {
		if rec := f.visitor(http.MethodPatch, publicItemURL(token, id), `{"done":true}`); rec.Code != http.StatusForbidden {
			t.Errorf("PATCH %d: got %d, want 403", id, rec.Code)
		}
	}

	items, err := f.app.DB.ListItemsByList(context.Background(), f.list.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("both deposits must reach the list (one merged), got %d items", len(items))
	}
	for _, it := range items {
		if it.Title == "Lait" && it.Quantity != 2 {
			t.Errorf("the deposited duplicate must merge into the existing item, got quantity %d", it.Quantity)
		}
	}
}

func TestPublicLinkRejectsUnknownDisabledAndRevokedTokens(t *testing.T) {
	f := newPublicLinkFixture(t, "todo")

	assertGone := func(t *testing.T, token, why string) {
		t.Helper()
		for _, c := range []struct{ method, target, body string }{
			{http.MethodGet, publicListURL(token), ""},
			{http.MethodPatch, publicItemURL(token, f.item.ID), `{"done":true}`},
			{http.MethodPost, publicListURL(token) + "/items", `{"title":"Pain"}`},
		} {
			if rec := f.visitor(c.method, c.target, c.body); rec.Code != http.StatusNotFound {
				t.Errorf("%s (%s %s): got %d, want 404 (%s)", why, c.method, c.target, rec.Code, rec.Body.String())
			}
		}
	}
	assertWorks := func(t *testing.T, token, why string) {
		t.Helper()
		if rec := f.visitor(http.MethodGet, publicListURL(token), ""); rec.Code != http.StatusOK {
			t.Errorf("%s: got %d, want 200", why, rec.Code)
		}
	}

	assertGone(t, "made-up-token", "unknown token")
	assertGone(t, strings.Repeat("a", maxPublicLinkTokenLength+1), "overlong token")
	assertGone(t, "bad%24token", "token outside the base64url alphabet")

	kept := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated)
	suspended := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated)
	revoked := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated)
	for _, l := range []publicLinkResponse{kept, suspended, revoked} {
		assertWorks(t, l.Token, "fresh link")
	}

	// Suspended (only the database sets it now): gone, the others untouched.
	f.exec(t, `UPDATE list_public_links SET is_enabled = 0 WHERE id = ?`, suspended.ID)
	assertGone(t, suspended.Token, "suspended link")

	// Revoked: gone for good; deleting it again is a 404.
	if rec := f.do(http.MethodDelete, f.linkURL(revoked.ID), "", "", f.cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: got %d, want 204", rec.Code)
	}
	assertGone(t, revoked.Token, "revoked link")
	if rec := f.do(http.MethodDelete, f.linkURL(revoked.ID), "", "", f.cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE: got %d, want 404", rec.Code)
	}
	if rec := f.do(http.MethodDelete, f.linksURL()+"/abc", "", "", f.cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("DELETE of a malformed id: got %d, want 400", rec.Code)
	}

	assertWorks(t, kept.Token, "the link nobody touched")
	got := f.links(t)
	if len(got) != 2 || got[0].ID != kept.ID || got[1].ID != suspended.ID || got[1].IsEnabled || !got[0].IsEnabled {
		t.Fatalf("the remaining links, oldest first, suspended flagged: %+v", got)
	}
}

func TestPublicLinkExpiry(t *testing.T) {
	f := newPublicLinkFixture(t, "shopping")

	for _, body := range []string{
		`{"access_mode":"read","expires_at":"2001-01-01T00:00:00Z"}`,
		`{"access_mode":"read","expires_at":"demain"}`,
		`{"access_mode":"admin"}`,
		`{"access_mode":"write"}`, // the old binary mode is not a mode any more
		`{"access_mode":"read","regenerate":true}`,
		`{}`,
	} {
		if rec := f.do(http.MethodPost, f.linksURL(), body, "", f.cookie); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s: got %d, want 400", body, rec.Code)
		}
	}

	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	created := f.setLink(t, `{"access_mode":"read","expires_at":"`+future+`"}`, http.StatusCreated)
	if created.ExpiresAt == nil || *created.ExpiresAt != future || created.Expired {
		t.Fatalf("unexpected expiry in %+v", created)
	}
	never := f.setLink(t, `{"access_mode":"read","expires_at":null}`, http.StatusCreated)
	if never.ExpiresAt != nil {
		t.Fatalf("a null expires_at means never, got %+v", never)
	}
	var got publicList
	decodeInto(t, f.visitor(http.MethodGet, publicListURL(created.Token), ""), http.StatusOK, &got)
	if got.ExpiresAt == nil || *got.ExpiresAt != future {
		t.Fatalf("the visitor must see when the link expires, got %+v", got)
	}

	// Past its expiry date, the link is gone, like an unknown one — and
	// only that link.
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	f.exec(t, `UPDATE list_public_links SET expires_at = ? WHERE id = ?`, past, created.ID)
	if rec := f.visitor(http.MethodGet, publicListURL(created.Token), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("expired link: got %d, want 404", rec.Code)
	}
	if rec := f.visitor(http.MethodGet, publicListURL(never.Token), ""); rec.Code != http.StatusOK {
		t.Fatalf("the list's other link must keep working: got %d", rec.Code)
	}
	links := f.links(t)
	if len(links) != 2 || !links[0].Expired || links[0].Token != created.Token || links[1].Expired {
		t.Fatalf("the expired link must still be listed, flagged: %+v", links)
	}
}

// Several links on one list: each its own mode, label and token; revoking
// one leaves the others working exactly as before.
func TestMultiplePublicLinksAreIndependent(t *testing.T) {
	f := newPublicLinkFixture(t, "shopping")
	family := f.setLink(t, `{"access_mode":"full_edit","label":"Famille"}`, http.StatusCreated)
	guests := f.setLink(t, `{"access_mode":"deposit_only","label":"  Invités  "}`, http.StatusCreated)
	workers := f.setLink(t, `{"access_mode":"read","label":"Artisans"}`, http.StatusCreated)
	unnamed := f.setLink(t, `{"access_mode":"add_only","label":"   "}`, http.StatusCreated)

	if family.Label == nil || *family.Label != "Famille" || guests.Label == nil || *guests.Label != "Invités" || unnamed.Label != nil {
		t.Fatalf("labels are trimmed, and blank means none: %+v %+v %+v", family, guests, unnamed)
	}
	tokens := map[string]bool{}
	for _, l := range []publicLinkResponse{family, guests, workers, unnamed} {
		if tokens[l.Token] || l.Path != "/public/lists/"+l.Token {
			t.Fatalf("each link has its own token and path: %+v", l)
		}
		tokens[l.Token] = true
	}

	listed := f.links(t)
	if len(listed) != 4 || listed[0].ID != family.ID || listed[3].ID != unnamed.ID || listed[1].Token != guests.Token {
		t.Fatalf("GET lists every link, oldest first, with its token: %+v", listed)
	}

	// Each link answers in its own mode.
	itemsSeen := func(token string) int {
		var got publicList
		decodeInto(t, f.visitor(http.MethodGet, publicListURL(token), ""), http.StatusOK, &got)
		return len(got.Items)
	}
	if itemsSeen(family.Token) != 1 || itemsSeen(guests.Token) != 0 || itemsSeen(workers.Token) != 1 {
		t.Fatal("full_edit and read links see the item, the deposit_only one doesn't")
	}
	if rec := f.visitor(http.MethodPatch, publicItemURL(workers.Token, f.item.ID), `{"done":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("the read link must not edit: %d", rec.Code)
	}
	if rec := f.visitor(http.MethodPatch, publicItemURL(family.Token, f.item.ID), `{"done":true}`); rec.Code != http.StatusOK {
		t.Fatalf("the full_edit link must edit: %d", rec.Code)
	}

	// Revoking the family link touches nothing else.
	if rec := f.do(http.MethodDelete, f.linkURL(family.ID), "", "", f.cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: got %d", rec.Code)
	}
	if rec := f.visitor(http.MethodGet, publicListURL(family.Token), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("the revoked link: got %d, want 404", rec.Code)
	}
	for _, l := range []publicLinkResponse{guests, workers, unnamed} {
		var got publicList
		decodeInto(t, f.visitor(http.MethodGet, publicListURL(l.Token), ""), http.StatusOK, &got)
		if got.AccessMode != l.AccessMode {
			t.Fatalf("link %d must keep its mode %s, got %s", l.ID, l.AccessMode, got.AccessMode)
		}
	}
	if rec := f.visitor(http.MethodPost, publicListURL(guests.Token)+"/items", `{"title":"Thermos"}`); rec.Code != http.StatusCreated {
		t.Fatalf("the deposit link must still take deposits: %d", rec.Code)
	}
	if got := f.links(t); len(got) != 3 {
		t.Fatalf("three links left, got %+v", got)
	}

	// Another list's link can't be revoked through this list.
	other, err := f.app.DB.CreateList(context.Background(), "Autre", "todo", f.list.HouseID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var otherLink publicLinkResponse
	decodeInto(t, f.do(http.MethodPost, "/api/v1/lists/"+strconv.FormatInt(other.ID, 10)+"/public-links", `{"access_mode":"read"}`, "", f.cookie), http.StatusCreated, &otherLink)
	if rec := f.do(http.MethodDelete, f.linkURL(otherLink.ID), "", "", f.cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE of another list's link through this list: got %d, want 404", rec.Code)
	}
	if rec := f.visitor(http.MethodGet, publicListURL(otherLink.Token), ""); rec.Code != http.StatusOK {
		t.Fatalf("the other list's link must be untouched: %d", rec.Code)
	}
}

func TestPublicLinkLabelAndCountLimits(t *testing.T) {
	f := newPublicLinkFixture(t, "todo")
	fifty := strings.Repeat("é", maxPublicLinkLabelLength)
	if got := f.setLink(t, `{"access_mode":"read","label":"`+fifty+`"}`, http.StatusCreated); got.Label == nil || *got.Label != fifty {
		t.Fatalf("50 characters (not bytes) is a valid label, got %+v", got)
	}
	if rec := f.do(http.MethodPost, f.linksURL(), `{"access_mode":"read","label":"`+fifty+`x"}`, "", f.cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("51 characters: got %d, want 400", rec.Code)
	}
	for i := 1; i < maxPublicLinksPerList; i++ {
		f.setLink(t, `{"access_mode":"read"}`, http.StatusCreated)
	}
	if rec := f.do(http.MethodPost, f.linksURL(), `{"access_mode":"read"}`, "", f.cookie); rec.Code != http.StatusConflict {
		t.Fatalf("link %d: got %d, want 409", maxPublicLinksPerList+1, rec.Code)
	}
	// Revoking one makes room again.
	if rec := f.do(http.MethodDelete, f.linkURL(f.links(t)[0].ID), "", "", f.cookie); rec.Code != http.StatusNoContent {
		t.Fatal("DELETE failed")
	}
	f.setLink(t, `{"access_mode":"read"}`, http.StatusCreated)
}

func TestPublicLinkManagementRequiresHouseMembership(t *testing.T) {
	f := newPublicLinkFixture(t, "shopping")
	ctx := context.Background()
	link := f.setLink(t, `{"access_mode":"full_edit"}`, http.StatusCreated)
	stranger := mustCreateTestUser(t, f.app, "stranger@example.com")
	recipient := mustCreateTestUser(t, f.app, "recipient@example.com")
	if _, err := f.app.DB.CreateOrUpdateListShare(ctx, f.list.ID, recipient.ID, "write"); err != nil {
		t.Fatal(err)
	}

	requests := []struct{ method, target, body string }{
		{http.MethodGet, f.linksURL(), ""},
		{http.MethodPost, f.linksURL(), `{"access_mode":"full_edit"}`},
		{http.MethodDelete, f.linkURL(link.ID), ""},
	}
	for _, who := range []*models.User{stranger, recipient} {
		cookie := f.sessionCookie(t, who)
		for _, req := range requests {
			rec := f.do(req.method, req.target, req.body, "", cookie)
			if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), link.Token) {
				t.Errorf("%s %s by %s: got %d, want a 403 without the link", req.method, req.target, who.Email, rec.Code)
			}
		}
	}
	apiToken := f.createToken(t, f.cookie, "Home Assistant").Token
	for _, req := range requests {
		if rec := f.do(req.method, req.target, req.body, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with no session: got %d, want 401", req.method, rec.Code)
		}
		// An API token opens list reads, but never these routes.
		if rec := f.bearer(req.method, req.target, req.body, apiToken); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), link.Token) {
			t.Errorf("%s with an API token: got %d, want 403", req.method, rec.Code)
		}
	}
	if rec := f.do(http.MethodGet, "/api/v1/lists/999999/public-links", "", "", f.cookie); rec.Code != http.StatusNotFound {
		t.Errorf("missing list: got %d, want 404", rec.Code)
	}
	// Nothing was created or revoked by the refused requests.
	if got := f.links(t); len(got) != 1 || got[0].ID != link.ID {
		t.Fatalf("refused requests changed the links: %+v", got)
	}
	if rec := f.visitor(http.MethodGet, publicListURL(link.Token), ""); rec.Code != http.StatusOK {
		t.Fatalf("the link must still work: %d", rec.Code)
	}
}
