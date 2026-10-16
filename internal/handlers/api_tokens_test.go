package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"trakka/internal/auth"
	"trakka/internal/db"
	"trakka/internal/models"
)

// tokenTestEnv drives the full router (Routes(): RequireSession, the CSRF
// check, the real mux) the way the PWA does — with a session cookie — and
// the way the Home Assistant integration does — with a bearer token.
type tokenTestEnv struct {
	app     *Application
	handler http.Handler
}

func newTokenTestEnv(t *testing.T) *tokenTestEnv {
	t.Helper()
	app := newTestApplication(t)
	app.Auth = auth.NewService(app.DB, nil, time.Hour, false)
	return &tokenTestEnv{app: app, handler: app.Routes()}
}

// sessionCookie signs user in and returns the cookie the browser would hold.
func (e *tokenTestEnv) sessionCookie(t *testing.T, user *models.User) *http.Cookie {
	t.Helper()
	raw, _, err := e.app.Auth.CreateSession(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: auth.SessionCookieName, Value: raw}
}

// do sends one request through the router. authorization is the raw
// Authorization header ("" for none); cookie may be nil.
func (e *tokenTestEnv) do(method, target, body, authorization string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// bearer sends a request authenticated by token alone.
func (e *tokenTestEnv) bearer(method, target, body, token string) *httptest.ResponseRecorder {
	return e.do(method, target, body, "Bearer "+token, nil)
}

// createToken creates an API token for the cookie's user, through the API.
func (e *tokenTestEnv) createToken(t *testing.T, cookie *http.Cookie, name string) apiTokenResponse {
	t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/api-tokens", `{"name":`+strconv.Quote(name)+`}`, "", cookie)
	var got apiTokenResponse
	decodeInto(t, rec, http.StatusCreated, &got)
	return got
}

func (e *tokenTestEnv) listTokens(t *testing.T, cookie *http.Cookie) []apiTokenResponse {
	t.Helper()
	rec := e.do(http.MethodGet, "/api/v1/api-tokens", "", "", cookie)
	if strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("listing must never return a token: %s", rec.Body.String())
	}
	var got []apiTokenResponse
	decodeInto(t, rec, http.StatusOK, &got)
	return got
}

func TestAPITokenLifecycle(t *testing.T) {
	e := newTokenTestEnv(t)
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	cookie := e.sessionCookie(t, alice)

	if got := e.listTokens(t, cookie); len(got) != 0 {
		t.Fatalf("a new user has no token, got %+v", got)
	}

	created := e.createToken(t, cookie, "  Home Assistant  ")
	if !strings.HasPrefix(created.Token, auth.APITokenPrefix) || len(created.Token) != len(auth.APITokenPrefix)+43 {
		t.Fatalf("unexpected token %q", created.Token)
	}
	if created.Name != "Home Assistant" || created.CreatedAt == "" || created.LastUsedAt != nil {
		t.Fatalf("unexpected create response %+v", created)
	}
	if listed := e.listTokens(t, cookie); len(listed) != 1 || listed[0].ID != created.ID || listed[0].LastUsedAt != nil {
		t.Fatalf("want the new token, never used, got %+v", listed)
	}

	// The token authenticates as its user, and its use is recorded.
	rec := e.bearer(http.MethodGet, "/api/v1/me", "", created.Token)
	var me models.User
	decodeInto(t, rec, http.StatusOK, &me)
	if me.ID != alice.ID {
		t.Fatalf("token authenticated as user %d, want %d", me.ID, alice.ID)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if listed := e.listTokens(t, cookie); listed[0].LastUsedAt == nil {
		t.Error("last_used_at not recorded after a token request")
	}

	// Revoking it stops it at once.
	if rec := e.do(http.MethodDelete, "/api/v1/api-tokens/"+strconv.FormatInt(created.ID, 10), "", "", cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.bearer(http.MethodGet, "/api/v1/me", "", created.Token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: got %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer ") {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	if rec := e.do(http.MethodDelete, "/api/v1/api-tokens/"+strconv.FormatInt(created.ID, 10), "", "", cookie); rec.Code != http.StatusNotFound {
		t.Errorf("second revoke: got %d, want 404", rec.Code)
	}

	// Someone else can neither see nor revoke Alice's tokens.
	kept := e.createToken(t, cookie, "Script")
	bobCookie := e.sessionCookie(t, mustCreateTestUser(t, e.app, "bob@example.com"))
	if got := e.listTokens(t, bobCookie); len(got) != 0 {
		t.Errorf("Bob sees %+v", got)
	}
	if rec := e.do(http.MethodDelete, "/api/v1/api-tokens/"+strconv.FormatInt(kept.ID, 10), "", "", bobCookie); rec.Code != http.StatusNotFound {
		t.Errorf("Bob revoking Alice's token: got %d, want 404", rec.Code)
	}
	if rec := e.bearer(http.MethodGet, "/api/v1/me", "", kept.Token); rec.Code != http.StatusOK {
		t.Errorf("Alice's token stopped working after Bob's attempt: %d", rec.Code)
	}

	// Managing tokens needs a session.
	if rec := e.do(http.MethodGet, "/api/v1/api-tokens", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("listing without a session: got %d, want 401", rec.Code)
	}
}

func TestAPITokenCreateValidation(t *testing.T) {
	e := newTokenTestEnv(t)
	cookie := e.sessionCookie(t, mustCreateTestUser(t, e.app, "alice@example.com"))

	for name, body := range map[string]string{
		"missing name":  `{}`,
		"blank name":    `{"name":"  \u0007 "}`,
		"too long":      `{"name":"` + strings.Repeat("x", 201) + `"}`,
		"unknown field": `{"name":"x","scope":"admin"}`,
	} {
		if rec := e.do(http.MethodPost, "/api/v1/api-tokens", body, "", cookie); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}

	for i := range maxAPITokensPerUser {
		e.createToken(t, cookie, "token "+strconv.Itoa(i))
	}
	if rec := e.do(http.MethodPost, "/api/v1/api-tokens", `{"name":"one too many"}`, "", cookie); rec.Code != http.StatusConflict {
		t.Errorf("token #%d: got %d, want 409", maxAPITokensPerUser+1, rec.Code)
	}
}

// TestAPITokenScope: a token opens apiTokenRoutes and nothing else, even
// for an admin, and in particular never the routes that manage tokens.
func TestAPITokenScope(t *testing.T) {
	e := newTokenTestEnv(t)
	admin := mustCreateTestUser(t, e.app, "admin@example.com") // the first account is an admin
	if !admin.IsAdmin {
		t.Fatal("test setup: the first user should be an admin")
	}
	token := e.createToken(t, e.sessionCookie(t, admin), "HA").Token
	house, err := e.app.DB.CreateHouseWithOwner(context.Background(), "Maison", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := e.app.DB.CreateList(context.Background(), "Courses", "groceries", house.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	listPath := "/api/v1/lists/" + strconv.FormatInt(list.ID, 10)

	for _, tc := range []struct{ method, target, body string }{
		{http.MethodGet, "/api/v1/api-tokens", ""},
		{http.MethodPost, "/api/v1/api-tokens", `{"name":"escalation"}`},
		{http.MethodDelete, "/api/v1/api-tokens/1", ""},
		{http.MethodPatch, "/api/v1/me", `{"language":"en"}`},
		{http.MethodGet, "/api/v1/houses", ""},
		{http.MethodPost, "/api/v1/lists", `{"name":"x","house_id":1}`},
		{http.MethodPut, listPath, `{"name":"renamed"}`},
		{http.MethodDelete, listPath, ""},
		{http.MethodPost, listPath + "/share", `{"email":"x@example.com","permission":"write"}`},
		{http.MethodGet, "/api/v1/admin/users", ""},
		{http.MethodPatch, "/api/v1/admin/settings", `{}`},
		{http.MethodPost, "/api/v1/calendar/feed-token", ""},
		{http.MethodPost, "/api/v1/push/subscribe", `{}`},
		{http.MethodGet, "/api/v1/users/search?q=al", ""},
	} {
		rec := e.bearer(tc.method, tc.target, tc.body, token)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not available to API tokens") {
			t.Errorf("%s %s with a token: got %d %s, want 403", tc.method, tc.target, rec.Code, rec.Body.String())
		}
	}
	if got, _ := e.app.DB.CountAPITokens(context.Background(), admin.ID); got != 1 {
		t.Errorf("a token minted another token: %d tokens", got)
	}
	if got, _ := e.app.DB.GetList(context.Background(), list.ID); got == nil || got.Name != "Courses" {
		t.Errorf("a token changed the list: %+v", got)
	}
}

// TestAPITokenAllows checks the allowlist matcher on its own, including
// paths the router cleans: an unclean path counts as the route it leads to.
// (Through Routes() the outer mux answers such a path with a redirect
// before RequireSession even runs.)
func TestAPITokenAllows(t *testing.T) {
	for _, tc := range []struct {
		method, target string
		want           bool
	}{
		{http.MethodGet, "/api/v1/me", true},
		{http.MethodHead, "/api/v1/me", true},
		{http.MethodGet, "/api/v1/lists/3", true},
		{http.MethodGet, "/api/v1/items?list_id=3", true},
		{http.MethodDelete, "/api/v1/items/3", true},
		{http.MethodGet, "/api/v1//me", true},
		{http.MethodDelete, "/api/v1/lists/3", false},
		{http.MethodGet, "/api/v1/lists/", false},
		{http.MethodPost, "/api/v1/items/3/price-check", false},
		{http.MethodGet, "/api/v1/lists/../admin/users", false},
		{http.MethodGet, "/api/v1/items/../api-tokens", false},
	} {
		if got := apiTokenAllows(httptest.NewRequest(tc.method, tc.target, nil)); got != tc.want {
			t.Errorf("%s %s: allowed = %v, want %v", tc.method, tc.target, got, tc.want)
		}
	}
}

// TestAPITokenRoutesExist keeps apiTokenRoutes in step with Routes(): each
// pattern must reach a real handler (a JSON answer or a 204), not the
// router's plain-text 404/405.
func TestAPITokenRoutesExist(t *testing.T) {
	e := newTokenTestEnv(t)
	user := mustCreateTestUser(t, e.app, "alice@example.com")
	token := e.createToken(t, e.sessionCookie(t, user), "HA").Token
	house, err := e.app.DB.CreateHouseWithOwner(context.Background(), "Maison", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := e.app.DB.CreateList(context.Background(), "Tâches", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	item := mustCreateItem(t, e.app, list.ID, "Arroser")

	ids := map[string]int64{"/api/v1/lists/": list.ID, "/api/v1/items/": item.ID}
	bodies := map[string]string{
		"PUT /api/v1/lists/{id}/reorder": `{"item_ids":[` + strconv.FormatInt(item.ID, 10) + `]}`,
		"POST /api/v1/items":             `{"list_id":` + strconv.FormatInt(list.ID, 10) + `,"title":"Tondre"}`,
		"PUT /api/v1/items/{id}":         `{"title":"Arroser","quantity":1}`,
		"PATCH /api/v1/items/{id}":       `{"done":true}`,
	}
	for _, pattern := range apiTokenRoutes {
		method, path, _ := strings.Cut(pattern, " ")
		for prefix, id := range ids {
			if strings.HasPrefix(path, prefix+"{id}") {
				path = strings.Replace(path, "{id}", strconv.FormatInt(id, 10), 1)
			}
		}
		if method == http.MethodGet && path == "/api/v1/items" {
			path += "?list_id=" + strconv.FormatInt(list.ID, 10)
		}
		rec := e.bearer(method, path, bodies[pattern], token)
		isJSON := strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json")
		if rec.Code == http.StatusForbidden || (!isJSON && rec.Code != http.StatusNoContent) || rec.Code >= 400 {
			t.Errorf("%s (as %s %s): got %d %s", pattern, method, path, rec.Code, rec.Body.String())
		}
	}
}

// TestAuthorizationHeaderFallsThrough: only a Trakka token takes over from
// the session cookie. An authenticating reverse proxy in front of the PWA
// may add its own Authorization header (Basic, or oauth2-proxy's forwarded
// id_token) to every request, which must not break the browser session.
func TestAuthorizationHeaderFallsThrough(t *testing.T) {
	e := newTokenTestEnv(t)
	cookie := e.sessionCookie(t, mustCreateTestUser(t, e.app, "alice@example.com"))

	for _, header := range []string{
		"Basic YWxpY2U6c2VjcmV0",
		"Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2ln",
		"Bearer",
		"trk_notbearer",
	} {
		if rec := e.do(http.MethodGet, "/api/v1/me", "", header, cookie); rec.Code != http.StatusOK {
			t.Errorf("Authorization %q with a valid session: got %d, want 200", header, rec.Code)
		}
		if rec := e.do(http.MethodGet, "/api/v1/me", "", header, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q alone: got %d, want 401", header, rec.Code)
		}
	}

	// A Trakka token that doesn't resolve is refused even beside a valid
	// session: the caller chose the token.
	for _, token := range []string{"trk_unknown", "trk_" + strings.Repeat("a", 200)} {
		if rec := e.do(http.MethodGet, "/api/v1/me", "", "bearer "+token, cookie); rec.Code != http.StatusUnauthorized {
			t.Errorf("unknown token %.12q… with a session: got %d, want 401", token, rec.Code)
		}
	}
}

// TestAPITokenIntegrationEndpoints walks the calls the Home Assistant
// integration makes, with a token: one poll of every list the user can open
// with their items, then adding (with aisle and price), finding by name,
// renaming, checking off, scheduling, reordering and deleting items — and
// the access rules those calls keep (a read-only share, someone else's
// list).
func TestAPITokenIntegrationEndpoints(t *testing.T) {
	e := newTokenTestEnv(t)
	ctx := context.Background()
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	bob := mustCreateTestUser(t, e.app, "bob@example.com")
	carol := mustCreateTestUser(t, e.app, "carol@example.com")
	token := e.createToken(t, e.sessionCookie(t, alice), "Home Assistant").Token

	aliceHouse, err := e.app.DB.CreateHouseWithOwner(ctx, "Maison", alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	groceries, _ := e.app.DB.CreateList(ctx, "Courses", "groceries", aliceHouse.ID, nil, "🛒")
	chores, _ := e.app.DB.CreateList(ctx, "Tâches", "todo", aliceHouse.ID, nil, "")
	mustCreateItem(t, e.app, chores.ID, "Arroser")
	bobHouse, _ := e.app.DB.CreateHouseWithOwner(ctx, "Chez Bob", bob.ID)
	bobList, _ := e.app.DB.CreateList(ctx, "Bricolage", "shopping", bobHouse.ID, nil, "")
	if _, err := e.app.DB.CreateOrUpdateListShare(ctx, bobList.ID, alice.ID, "read"); err != nil {
		t.Fatal(err)
	}
	carolHouse, _ := e.app.DB.CreateHouseWithOwner(ctx, "Chez Carol", carol.ID)
	carolList, _ := e.app.DB.CreateList(ctx, "Privé", "todo", carolHouse.ID, nil, "")

	// One poll: every list Alice can open, how she reaches it, with items.
	lists := decodeAs[[]models.List](t, e.bearer(http.MethodGet, "/api/v1/lists?scope=all&include=items", "", token), http.StatusOK)
	byID := map[int64]models.List{}
	for _, l := range lists {
		byID[l.ID] = l
	}
	if len(lists) != 3 || byID[carolList.ID].ID != 0 {
		t.Fatalf("want Alice's two lists and Bob's shared one, got %+v", lists)
	}
	if l := byID[groceries.ID]; l.AccessSource != "house_member" || l.AccessPermission != "write" || l.Icon != "🛒" {
		t.Errorf("House list: %+v", l)
	}
	if l := byID[bobList.ID]; l.AccessSource != "list_share" || l.AccessPermission != "read" {
		t.Errorf("shared list: %+v", l)
	}
	if l := byID[chores.ID]; len(l.Items) != 1 || l.Items[0].Title != "Arroser" {
		t.Errorf("items not embedded: %+v", l.Items)
	}

	// Filters: by name (any case), by type; bad options are refused.
	lists = decodeAs[[]models.List](t, e.bearer(http.MethodGet, "/api/v1/lists?scope=all&name=COURSES", "", token), http.StatusOK)
	if len(lists) != 1 || lists[0].ID != groceries.ID || lists[0].Items != nil {
		t.Errorf("name=COURSES: %+v", lists)
	}
	lists = decodeAs[[]models.List](t, e.bearer(http.MethodGet, "/api/v1/lists?scope=all&type=shopping", "", token), http.StatusOK)
	if len(lists) != 1 || lists[0].ID != bobList.ID {
		t.Errorf("type=shopping: %+v", lists)
	}
	lists = decodeAs[[]models.List](t, e.bearer(http.MethodGet, "/api/v1/lists?name=t%C3%A2ches", "", token), http.StatusOK)
	if len(lists) != 1 || lists[0].ID != chores.ID || lists[0].AccessSource != "" {
		t.Errorf("name filter in the default mode: %+v", lists)
	}
	for _, query := range []string{"scope=everything", "scope=all&house_id=1", "include=shares", "scope=all&type=bogus"} {
		if rec := e.bearer(http.MethodGet, "/api/v1/lists?"+query, "", token); rec.Code != http.StatusBadRequest {
			t.Errorf("?%s: got %d, want 400", query, rec.Code)
		}
	}

	// Add an item with an aisle, a price and a quantity.
	milk := decodeAs[models.Item](t, e.bearer(http.MethodPost, "/api/v1/items",
		`{"list_id":`+strconv.FormatInt(groceries.ID, 10)+`,"title":"Lait","quantity":2,"aisle":"dairy","price":1.15}`, token),
		http.StatusCreated)
	if milk.Quantity != 2 || milk.Aisle == nil || *milk.Aisle != "dairy" || milk.Price == nil || *milk.Price != 1.15 || milk.Done {
		t.Fatalf("created item: %+v", milk)
	}
	itemPath := "/api/v1/items/" + strconv.FormatInt(milk.ID, 10)

	// Find it by name.
	found := decodeAs[[]models.Item](t, e.bearer(http.MethodGet, "/api/v1/items?list_id="+strconv.FormatInt(groceries.ID, 10)+"&title=lait", "", token), http.StatusOK)
	if len(found) != 1 || found[0].ID != milk.ID {
		t.Errorf("title=lait: %+v", found)
	}
	found = decodeAs[[]models.Item](t, e.bearer(http.MethodGet, "/api/v1/items?list_id="+strconv.FormatInt(groceries.ID, 10)+"&title=Beurre", "", token), http.StatusOK)
	if len(found) != 0 {
		t.Errorf("title=Beurre: %+v", found)
	}

	// Rename, then check off (on a purchase list, that's "bought").
	updated := decodeAs[models.Item](t, e.bearer(http.MethodPatch, itemPath, `{"title":"Lait demi-écrémé"}`, token), http.StatusOK)
	if updated.Title != "Lait demi-écrémé" || updated.Done {
		t.Errorf("renamed: %+v", updated)
	}
	updated = decodeAs[models.Item](t, e.bearer(http.MethodPatch, itemPath, `{"done":true}`, token), http.StatusOK)
	if !updated.Done || updated.PurchaseStatus != models.PurchaseBought {
		t.Errorf("checked off: %+v", updated)
	}
	updated = decodeAs[models.Item](t, e.bearer(http.MethodPatch, itemPath, `{"done":false}`, token), http.StatusOK)
	if updated.Done {
		t.Errorf("unchecked: %+v", updated)
	}

	// A task's due date and time, then clearing them.
	task := decodeAs[models.Item](t, e.bearer(http.MethodPost, "/api/v1/items", `{"list_id":`+strconv.FormatInt(chores.ID, 10)+`,"title":"Sortir les poubelles","due_date":"2026-10-20","due_time":"19:30"}`, token), http.StatusCreated)
	if task.DueDate == nil || *task.DueDate != "2026-10-20" || task.DueTime == nil || *task.DueTime != "19:30" {
		t.Fatalf("scheduled task: %+v", task)
	}
	cleared := decodeAs[models.Item](t, e.bearer(http.MethodPatch, "/api/v1/items/"+strconv.FormatInt(task.ID, 10), `{"due_date":""}`, token), http.StatusOK)
	if cleared.DueDate != nil || cleared.DueTime != nil {
		t.Errorf("clearing the due date: %+v", cleared)
	}

	// Reorder the task list: the new task first.
	items, _ := e.app.DB.ListItemsByList(ctx, chores.ID)
	order := `{"item_ids":[` + strconv.FormatInt(task.ID, 10) + `,` + strconv.FormatInt(items[0].ID, 10) + `]}`
	reordered := decodeAs[[]models.Item](t, e.bearer(http.MethodPut, "/api/v1/lists/"+strconv.FormatInt(chores.ID, 10)+"/reorder", order, token), http.StatusOK)
	if len(reordered) != 2 || reordered[0].ID != task.ID {
		t.Errorf("reorder: %+v", reordered)
	}

	// Delete.
	if rec := e.bearer(http.MethodDelete, itemPath, "", token); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.bearer(http.MethodGet, itemPath, "", token); rec.Code != http.StatusNotFound {
		t.Errorf("deleted item: got %d, want 404", rec.Code)
	}

	// The token keeps its user's access rules: Bob's list is read-only to
	// Alice, Carol's isn't hers at all.
	if rec := e.bearer(http.MethodPost, "/api/v1/items", `{"list_id":`+strconv.FormatInt(bobList.ID, 10)+`,"title":"Vis"}`, token); rec.Code != http.StatusForbidden {
		t.Errorf("adding to a read-only share: got %d, want 403", rec.Code)
	}
	if rec := e.bearer(http.MethodGet, "/api/v1/lists/"+strconv.FormatInt(carolList.ID, 10), "", token); rec.Code != http.StatusForbidden {
		t.Errorf("reading someone else's list: got %d, want 403", rec.Code)
	}

	// The instance's time zone, for interpreting due_time.
	cfg := decodeAs[instanceConfig](t, e.bearer(http.MethodGet, "/api/v1/config", "", token), http.StatusOK)
	if cfg.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC (no Location set)", cfg.Timezone)
	}
}

// decodeAs decodes rec's body into a fresh T: decoding into a reused
// variable would keep the previous value's fields the new JSON omits.
func decodeAs[T any](t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) T {
	t.Helper()
	var v T
	decodeInto(t, rec, wantStatus, &v)
	return v
}

func mustCreateItem(t *testing.T, app *Application, listID int64, title string) *models.Item {
	t.Helper()
	item, err := app.DB.CreateItem(context.Background(), listID, title, nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating item %q: %v", title, err)
	}
	return item
}

// Sanity check on the JSON shape the integration parses.
func TestAPITokenResponseShape(t *testing.T) {
	raw, err := json.Marshal(newAPITokenResponse(&db.APIToken{ID: 7, Name: "HA", CreatedAt: "2026-10-16T10:00:00.000Z"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"id":7,"name":"HA","created_at":"2026-10-16T10:00:00.000Z","last_used_at":null}` {
		t.Errorf("unexpected JSON %s", got)
	}
}
