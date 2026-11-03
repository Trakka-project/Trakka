package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"trakka/internal/backup"
	"trakka/internal/db"
	"trakka/internal/fluv"
	"trakka/internal/models"
)

const (
	fluvTestToken = "fluv_trk_abcdefghijklmnopqrstuvwxyz"
	fluvTestPath  = "/api/v1/integrations/trakka/purchases"
)

// fluvReceiver records the purchases Fluv's endpoint is sent, checking the
// token like Fluv does.
type fluvReceiver struct {
	mu        sync.Mutex
	purchases map[string]map[string]any
}

func (f *fluvReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+fluvTestToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == fluvTestPath:
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.purchases[p["id"].(string)] = p
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, fluvTestPath+"/"):
		id := strings.TrimPrefix(r.URL.Path, fluvTestPath+"/")
		if _, ok := f.purchases[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.purchases, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fluvReceiver) received() map[string]map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]map[string]any{}
	for k, v := range f.purchases {
		out[k] = v
	}
	return out
}

type fluvTestEnv struct {
	app      *Application
	receiver *fluvReceiver
	endpoint string
	dbPath   string
	clock    time.Time
}

// newFluvTestEnv is an Application with the Fluv integration wired as in
// cmd/server — the token sealed by a real backup.Service — allowed to
// reach a loopback receiver.
func newFluvTestEnv(t *testing.T) *fluvTestEnv {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbPath := filepath.Join(t.TempDir(), "trakka.db")
	d, err := db.Open(dbPath, logger)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	receiver := &fluvReceiver{purchases: map[string]map[string]any{}}
	srv := httptest.NewServer(receiver)
	t.Cleanup(srv.Close)

	env := &fluvTestEnv{receiver: receiver, endpoint: srv.URL + fluvTestPath, dbPath: dbPath, clock: time.Now()}
	backups := backup.New(backup.Options{DB: d, DBPath: dbPath, Logger: logger, Location: time.UTC})
	env.app = &Application{DB: d, Logger: logger, Backups: backups, Fluv: fluv.New(fluv.Options{
		DB: d, Sealer: backups, Logger: logger, Location: time.UTC,
		Policy: fluv.Policy{Allowed: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}},
		Now:    func() time.Time { return env.clock },
	})}
	return env
}

func TestFluvSettingsLifecycle(t *testing.T) {
	env := newFluvTestEnv(t)
	app := env.app
	user := mustCreateTestUser(t, app, "buyer@example.com")

	var got fluv.Settings
	decodeInto(t, callAs(user, app.handleFluvShow, http.MethodGet, "/api/v1/integrations/fluv", ""), http.StatusOK, &got)
	if got.Configured || !got.OperatorAllowlist {
		t.Fatalf("before saving: %+v", got)
	}

	refusals := []struct{ body, code string }{
		{`{"endpoint_url":"` + env.endpoint + `"}`, "required"},
		{`{"endpoint_url":"` + env.endpoint + `","token":"trk_nope"}`, "invalid_token"},
		{`{"endpoint_url":"javascript:alert(1)","token":"` + fluvTestToken + `"}`, "invalid_url"},
		{`{"endpoint_url":"http://10.0.0.1/api/v1/integrations/trakka/purchases","token":"` + fluvTestToken + `"}`, "address_not_allowed"},
		{`{"endpoint_url":"http://169.254.169.254/api/v1/integrations/trakka/purchases","token":"` + fluvTestToken + `"}`, "address_not_allowed"},
		// The address is locked automatically: the old manual field is refused.
		{`{"endpoint_url":"` + env.endpoint + `","pinned_ips":["127.0.0.1"],"token":"` + fluvTestToken + `"}`, ""},
	}
	for _, c := range refusals {
		rec := callAs(user, app.handleFluvSave, http.MethodPut, "/api/v1/integrations/fluv", c.body)
		var body map[string]string
		decodeInto(t, rec, http.StatusBadRequest, &body)
		if body["code"] != c.code {
			t.Errorf("PUT %s: code %q, want %q", c.body, body["code"], c.code)
		}
	}

	decodeInto(t, callAs(user, app.handleFluvSave, http.MethodPut, "/api/v1/integrations/fluv",
		`{"endpoint_url":"`+env.endpoint+`","token":"`+fluvTestToken+`"}`), http.StatusOK, &got)
	if !got.Configured || !got.Enabled || !got.TokenSet || got.EndpointURL != env.endpoint || len(got.PinnedIPs) != 1 {
		t.Fatalf("after saving: %+v", got)
	}
	rec := callAs(user, app.handleFluvShow, http.MethodGet, "/api/v1/integrations/fluv", "")
	if strings.Contains(rec.Body.String(), fluvTestToken) {
		t.Fatal("GET returned the token")
	}
	// Sealed with the instance key, which lives next to the database, not
	// in it.
	integ, err := app.DB.GetFluvIntegration(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(integ.TokenSealed, fluvTestToken) || !strings.HasPrefix(integ.TokenSealed, "v1:") {
		t.Fatalf("stored token %q is not sealed", integ.TokenSealed)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(env.dbPath), backup.KeyFileName)); err != nil {
		t.Fatalf("instance key file: %v", err)
	}

	var test fluv.TestResult
	decodeInto(t, callAs(user, app.handleFluvTest, http.MethodPost, "/api/v1/integrations/fluv/test", ""), http.StatusOK, &test)
	if !test.OK {
		t.Fatalf("connection test = %+v", test)
	}

	// This receiver is a Fluv older than the metadata endpoint: a 200 with
	// ok false, for the page to keep its text fields.
	var meta fluv.Metadata
	decodeInto(t, callAs(user, app.handleFluvMetadata, http.MethodGet, "/api/v1/integrations/fluv/metadata?lang=en", ""), http.StatusOK, &meta)
	if meta.OK || meta.Status != http.StatusNotFound || meta.Accounts == nil {
		t.Fatalf("metadata from an older Fluv = %+v", meta)
	}

	// Saving without a token keeps the stored one.
	decodeInto(t, callAs(user, app.handleFluvSave, http.MethodPut, "/api/v1/integrations/fluv",
		`{"endpoint_url":"`+env.endpoint+`","enabled":false}`), http.StatusOK, &got)
	if got.Enabled || !got.TokenSet {
		t.Fatalf("after pausing: %+v", got)
	}

	if rec := callAs(user, app.handleFluvDelete, http.MethodDelete, "/api/v1/integrations/fluv", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", rec.Code)
	}
	decodeInto(t, callAs(user, app.handleFluvShow, http.MethodGet, "/api/v1/integrations/fluv", ""), http.StatusOK, &got)
	if got.Configured {
		t.Fatalf("after DELETE: %+v", got)
	}
	if rec := callAs(user, app.handleFluvMetadata, http.MethodGet, "/api/v1/integrations/fluv/metadata", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("metadata after DELETE: %d, want 404", rec.Code)
	}
	if rec := callAs(user, app.handleFluvDelete, http.MethodDelete, "/api/v1/integrations/fluv", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("second DELETE: %d", rec.Code)
	}
}

func TestFluvRoutesOffWhenDisabled(t *testing.T) {
	app := newTestApplication(t)
	user := mustCreateTestUser(t, app, "buyer@example.com")
	for _, h := range []http.HandlerFunc{app.handleFluvShow, app.handleFluvSave, app.handleFluvDelete, app.handleFluvTest, app.handleFluvRetry,
		app.handleFluvMetadata, app.handleFluvListDefaultsShow, app.handleFluvListDefaultsSave, app.handleFluvItemShow, app.handleFluvItemSave} {
		if rec := callAs(user, h, http.MethodGet, "/api/v1/integrations/fluv", "{}"); rec.Code != http.StatusNotFound {
			t.Errorf("status %d with the integration off, want 404", rec.Code)
		}
	}
}

// TestFluvRoutesClosedToAPITokens: an API token (Home Assistant) must not
// read or redirect where a user's purchases go.
func TestFluvRoutesClosedToAPITokens(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/integrations/fluv"},
		{http.MethodPut, "/api/v1/integrations/fluv"},
		{http.MethodDelete, "/api/v1/integrations/fluv"},
		{http.MethodPost, "/api/v1/integrations/fluv/test"},
		{http.MethodPost, "/api/v1/integrations/fluv/retry"},
		{http.MethodGet, "/api/v1/integrations/fluv/metadata"},
		{http.MethodGet, "/api/v1/integrations/fluv/lists/1"},
		{http.MethodPut, "/api/v1/integrations/fluv/lists/1"},
		{http.MethodGet, "/api/v1/integrations/fluv/items/1"},
		{http.MethodPut, "/api/v1/integrations/fluv/items/1"},
	} {
		if apiTokenAllows(httptest.NewRequest(route.method, route.path, nil)) {
			t.Errorf("%s %s is open to API tokens", route.method, route.path)
		}
	}
}

// TestFluvCheckOffSendsThePurchase follows an item checked off and then
// un-checked over PATCH, as the PWA does, to the Fluv receiver.
func TestFluvCheckOffSendsThePurchase(t *testing.T) {
	env := newFluvTestEnv(t)
	app := env.app
	owner, list := itemTestFixture(t, app)
	decodeInto(t, callAs(owner, app.handleFluvSave, http.MethodPut, "/api/v1/integrations/fluv",
		`{"endpoint_url":"`+env.endpoint+`","token":"`+fluvTestToken+`"}`), http.StatusOK, nil)

	var item models.Item
	decodeInto(t, callAs(owner, app.handleItemsCreate, http.MethodPost, "/api/v1/items",
		`{"list_id":`+strconv.FormatInt(list.ID, 10)+`,"title":"Café","quantity":3,"price":4.5}`), http.StatusCreated, &item)
	id := strconv.FormatInt(item.ID, 10)
	patch := func(body string) {
		t.Helper()
		decodeInto(t, callAs(owner, app.handleItemsPatch, http.MethodPatch, "/api/v1/items/"+id, body, "id", id), http.StatusOK, nil)
	}

	patch(`{"title":"Café moulu"}`) // not a purchase
	patch(`{"done":true,"purchase_status":"bought","actual_price":4.2}`)
	env.clock = env.clock.Add(time.Minute)
	app.Fluv.SendDue(t.Context())
	got := env.receiver.received()
	if len(got) != 1 {
		t.Fatalf("Fluv received %v, want one purchase", got)
	}
	for _, p := range got {
		if p["title"] != "Café moulu" || p["store"] != "Courses" || p["quantity"] != float64(3) || p["amount_minor"] != float64(1260) {
			t.Errorf("purchase = %v", p)
		}
		if p["date"] != env.clock.Add(-time.Minute).UTC().Format(time.DateOnly) {
			t.Errorf("date = %v", p["date"])
		}
	}

	patch(`{"done":false}`)
	app.Fluv.SendDue(t.Context())
	if got := env.receiver.received(); len(got) != 0 {
		t.Fatalf("still in Fluv after un-checking: %v", got)
	}
}

// TestFluvListDefaultsAndShipping: a user's Fluv defaults for a list and an
// item's shipping fee, set over the API, reach Fluv with the purchase.
func TestFluvListDefaultsAndShipping(t *testing.T) {
	env := newFluvTestEnv(t)
	app := env.app
	owner, list := itemTestFixture(t, app)
	listID := strconv.FormatInt(list.ID, 10)
	path := "/api/v1/integrations/fluv/lists/" + listID
	const account = "0192f3a4-5b6c-7d8e-9f01-23456789abcd"

	var view struct {
		AccountID  string `json:"account_id"`
		CategoryID string `json:"category_id"`
		BudgetType string `json:"budget_type"`
		Configured bool   `json:"configured"`
	}
	decodeInto(t, callAs(owner, app.handleFluvListDefaultsShow, http.MethodGet, path, "", "id", listID), http.StatusOK, &view)
	if view.Configured || view.AccountID != "" || view.BudgetType != "" {
		t.Fatalf("before anything is set: %+v", view)
	}
	decodeInto(t, callAs(owner, app.handleFluvSave, http.MethodPut, "/api/v1/integrations/fluv",
		`{"endpoint_url":"`+env.endpoint+`","token":"`+fluvTestToken+`"}`), http.StatusOK, nil)

	var refusal map[string]string
	decodeInto(t, callAs(owner, app.handleFluvListDefaultsSave, http.MethodPut, path, `{"budget_type":"luxury"}`, "id", listID), http.StatusBadRequest, &refusal)
	if refusal["code"] != "invalid_budget_type" || refusal["field"] != "budget_type" {
		t.Fatalf("refusal = %v", refusal)
	}
	decodeInto(t, callAs(owner, app.handleFluvListDefaultsSave, http.MethodPut, path,
		`{"account_id":"`+account+`","budget_type":"need"}`, "id", listID), http.StatusOK, &view)
	if !view.Configured || view.AccountID != account || view.BudgetType != "need" || view.CategoryID != "" {
		t.Fatalf("saved: %+v", view)
	}

	// Someone who can't see the list can't read or set anything about it.
	stranger := mustCreateTestUser(t, app, "stranger@example.com")
	if rec := callAs(stranger, app.handleFluvListDefaultsSave, http.MethodPut, path, `{}`, "id", listID); rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
		t.Errorf("stranger's PUT: status %d", rec.Code)
	}

	var item models.Item
	decodeInto(t, callAs(owner, app.handleItemsCreate, http.MethodPost, "/api/v1/items",
		`{"list_id":`+listID+`,"title":"Casque","price":79.9}`), http.StatusCreated, &item)
	id := strconv.FormatInt(item.ID, 10)
	if rec := callAs(owner, app.handleItemsPatch, http.MethodPatch, "/api/v1/items/"+id, `{"shipping_price":-1}`, "id", id); rec.Code != http.StatusBadRequest {
		t.Errorf("negative shipping_price: status %d, want 400", rec.Code)
	}
	decodeInto(t, callAs(owner, app.handleItemsPatch, http.MethodPatch, "/api/v1/items/"+id,
		`{"shipping_price":5.99}`, "id", id), http.StatusOK, &item)
	if item.ShippingPrice == nil || *item.ShippingPrice != 5.99 {
		t.Fatalf("shipping_price = %v, want 5.99", item.ShippingPrice)
	}
	decodeInto(t, callAs(owner, app.handleItemsPatch, http.MethodPatch, "/api/v1/items/"+id,
		`{"done":true}`, "id", id), http.StatusOK, &item)
	if item.ShippingPrice == nil {
		t.Fatal("a PATCH without shipping_price cleared it")
	}
	env.clock = env.clock.Add(time.Minute)
	app.Fluv.SendDue(t.Context())
	got := env.receiver.received()
	if len(got) != 1 {
		t.Fatalf("Fluv received %v, want one purchase", got)
	}
	for _, p := range got {
		if p["amount_minor"] != float64(7990) || p["shipping_amount_minor"] != float64(599) ||
			p["account_id"] != account || p["budget_type"] != "need" {
			t.Errorf("purchase = %v", p)
		}
		if _, sent := p["category_id"]; sent {
			t.Errorf("an unset category_id was sent: %v", p)
		}
	}

	// A PUT that doesn't know the field leaves it; null clears it.
	decodeInto(t, callAs(owner, app.handleItemsUpdate, http.MethodPut, "/api/v1/items/"+id,
		`{"title":"Casque","price":79.9,"done":true}`, "id", id), http.StatusOK, &item)
	if item.ShippingPrice == nil {
		t.Fatal("a PUT without shipping_price cleared it")
	}
	var cleared models.Item
	decodeInto(t, callAs(owner, app.handleItemsPatch, http.MethodPatch, "/api/v1/items/"+id,
		`{"shipping_price":null}`, "id", id), http.StatusOK, &cleared)
	if cleared.ShippingPrice != nil {
		t.Fatalf("shipping_price = %v after null", *cleared.ShippingPrice)
	}
}

// TestFluvItemPurchaseOptions: the check-off toast's "Options" over the
// API — the buyer reads their purchase, files it in another account, takes
// it out of Fluv; a House member who didn't buy it has nothing to change.
func TestFluvItemPurchaseOptions(t *testing.T) {
	env := newFluvTestEnv(t)
	app := env.app
	owner, list := itemTestFixture(t, app)
	listID := strconv.FormatInt(list.ID, 10)
	const account = "0192f3a4-5b6c-7d8e-9f01-23456789abcd"
	decodeInto(t, callAs(owner, app.handleFluvSave, http.MethodPut, "/api/v1/integrations/fluv",
		`{"endpoint_url":"`+env.endpoint+`","token":"`+fluvTestToken+`"}`), http.StatusOK, nil)

	var item models.Item
	decodeInto(t, callAs(owner, app.handleItemsCreate, http.MethodPost, "/api/v1/items",
		`{"list_id":`+listID+`,"title":"Casque","price":79.9}`), http.StatusCreated, &item)
	id := strconv.FormatInt(item.ID, 10)
	path := "/api/v1/integrations/fluv/items/" + id

	var view map[string]any
	decodeInto(t, callAs(owner, app.handleFluvItemShow, http.MethodGet, path, "", "id", id), http.StatusOK, &view)
	if view["queued"] != false || view["skipped"] != false {
		t.Fatalf("not bought yet: %v", view)
	}
	var refusal map[string]string
	decodeInto(t, callAs(owner, app.handleFluvItemSave, http.MethodPut, path, `{"skip":true}`, "id", id), http.StatusNotFound, &refusal)
	if refusal["code"] != "no_purchase" {
		t.Fatalf("refusal = %v", refusal)
	}

	decodeInto(t, callAs(owner, app.handleItemsPatch, http.MethodPatch, "/api/v1/items/"+id, `{"done":true}`, "id", id), http.StatusOK, &item)
	decodeInto(t, callAs(owner, app.handleFluvItemShow, http.MethodGet, path, "", "id", id), http.StatusOK, &view)
	if view["queued"] != true || view["sent"] != false || view["account_id"] != "" {
		t.Fatalf("checked off: %v", view)
	}
	decodeInto(t, callAs(owner, app.handleFluvItemSave, http.MethodPut, path, `{"account_id":"nope"}`, "id", id), http.StatusBadRequest, &refusal)
	if refusal["code"] != "invalid_fluv_id" || refusal["field"] != "account_id" {
		t.Fatalf("refusal = %v", refusal)
	}
	decodeInto(t, callAs(owner, app.handleFluvItemSave, http.MethodPut, path, `{"account_id":"`+account+`"}`, "id", id), http.StatusOK, &view)
	if view["queued"] != true || view["account_id"] != account {
		t.Fatalf("account chosen: %v", view)
	}
	env.clock = env.clock.Add(time.Minute)
	app.Fluv.SendDue(t.Context())
	got := env.receiver.received()
	if len(got) != 1 {
		t.Fatalf("Fluv received %v, want one purchase", got)
	}
	for _, p := range got {
		if p["account_id"] != account {
			t.Errorf("purchase = %v", p)
		}
	}

	decodeInto(t, callAs(owner, app.handleFluvItemSave, http.MethodPut, path, `{"account_id":"`+account+`","skip":true}`, "id", id), http.StatusOK, &view)
	if view["skipped"] != true || view["queued"] != false {
		t.Fatalf("skipped: %v", view)
	}
	app.Fluv.SendDue(t.Context())
	if got := env.receiver.received(); len(got) != 0 {
		t.Fatalf("still in Fluv after skipping: %v", got)
	}

	// Someone who can't see the list can't read anything about it.
	stranger := mustCreateTestUser(t, app, "stranger@example.com")
	if rec := callAs(stranger, app.handleFluvItemShow, http.MethodGet, path, "", "id", id); rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
		t.Errorf("stranger's GET: status %d", rec.Code)
	}
}
