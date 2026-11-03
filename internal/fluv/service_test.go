package fluv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
)

const testToken = "fluv_trk_abcdefghijklmnopqrstuvwxyz"

// fluvPurchaseRequest is the body Fluv's handleTrakkaPurchase decodes
// (Fluv's internal/httpapi/handlers_trakka.go), field for field — kept
// apart from purchaseBody so that a field Trakka sends and Fluv doesn't
// know fails here, as it would there.
type fluvPurchaseRequest struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Store         string `json:"store"`
	Quantity      int    `json:"quantity"`
	AmountMinor   int64  `json:"amount_minor"`
	ShippingMinor int64  `json:"shipping_amount_minor"`
	Currency      string `json:"currency"`
	Date          string `json:"date"`
	AccountID     string `json:"account_id"`
	CategoryID    string `json:"category_id"`
	BudgetType    string `json:"budget_type"`
}

// fakeFluv answers like Fluv's /api/v1/integrations/trakka/purchases: the
// link token in Authorization: Bearer, strict JSON (unknown fields
// refused), the same id twice recorded once, DELETE of an unknown id 404.
type fakeFluv struct {
	mu        sync.Mutex
	token     string
	purchases map[string]purchaseBody
	log       []string
	// failWith, when set, answers every request with this status.
	failWith int
	// metadata is what GET metadataPath answers; nil: 404, like a Fluv
	// older than the endpoint. lang is the Accept-Language it last got.
	metadata any
	lang     string
}

func (f *fakeFluv) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	problem := func(status int, code string) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code})
	}
	if f.failWith != 0 {
		f.log = append(f.log, "FAIL "+r.Method)
		problem(f.failWith, "forced")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		problem(http.StatusUnauthorized, "unauthenticated")
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == purchasesPath:
		if r.Header.Get("Content-Type") != "application/json" {
			problem(http.StatusUnsupportedMediaType, "unsupported_media_type")
			return
		}
		var req fluvPurchaseRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			problem(http.StatusBadRequest, "invalid_json")
			return
		}
		switch req.BudgetType {
		case "", "need", "needs", "want", "wants":
		default:
			problem(http.StatusUnprocessableEntity, "invalid")
			return
		}
		if req.ShippingMinor < 0 {
			problem(http.StatusUnprocessableEntity, "invalid")
			return
		}
		p := purchaseBody{
			ID: req.ID, Title: req.Title, Store: req.Store, Quantity: req.Quantity, AmountMinor: req.AmountMinor, Date: req.Date,
			ShippingMinor: req.ShippingMinor, AccountID: req.AccountID, CategoryID: req.CategoryID, BudgetType: req.BudgetType,
		}
		f.log = append(f.log, "POST "+p.ID)
		if _, ok := f.purchases[p.ID]; ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		f.purchases[p.ID] = p
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, purchasesPath+"/"):
		id := strings.TrimPrefix(r.URL.Path, purchasesPath+"/")
		f.log = append(f.log, "DELETE "+id)
		if _, ok := f.purchases[id]; !ok {
			problem(http.StatusNotFound, "not_found")
			return
		}
		delete(f.purchases, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == metadataPath && f.metadata != nil:
		f.log = append(f.log, "GET metadata")
		f.lang = r.Header.Get("Accept-Language")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.metadata)
	default:
		problem(http.StatusNotFound, "not_found")
	}
}

func (f *fakeFluv) snapshot() (map[string]purchaseBody, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]purchaseBody, len(f.purchases))
	for k, v := range f.purchases {
		out[k] = v
	}
	return out, append([]string(nil), f.log...)
}

// fakeSealer stands in for backup.Service: reversible, and never the
// plaintext as is.
type fakeSealer struct{}

func (fakeSealer) SealSecret(purpose, plaintext string) (string, error) {
	return "sealed:" + purpose + ":" + reverse(plaintext), nil
}

func (fakeSealer) OpenSecret(purpose, sealed string) (string, error) {
	rest, ok := strings.CutPrefix(sealed, "sealed:"+purpose+":")
	if !ok {
		return "", errors.New("unreadable")
	}
	return reverse(rest), nil
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

type testEnv struct {
	svc      *Service
	db       *db.DB
	fluv     *fakeFluv
	endpoint string
	clock    time.Time
	user     *models.User
	list     *models.List
	item     *models.Item
}

func (e *testEnv) advance(d time.Duration) { e.clock = e.clock.Add(d) }

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := db.Open(filepath.Join(t.TempDir(), "trakka.db"), logger)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	fake := &fakeFluv{token: testToken, purchases: map[string]purchaseBody{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	hash := "x"
	user, err := d.CreateUser(ctx, "buyer@example.com", &hash, nil, nil, "Buyer")
	if err != nil {
		t.Fatal(err)
	}
	house, err := d.CreateHouseWithOwner(ctx, "Maison", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := d.CreateList(ctx, "Lidl", "groceries", house.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	item, err := d.CreateItem(ctx, list.ID, "Lait", nil, 2, floatPtr(1.1), false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	env := &testEnv{db: d, fluv: fake, endpoint: srv.URL + purchasesPath, clock: time.Now(), user: user, list: list, item: item}
	env.svc = New(Options{
		DB: d, Sealer: fakeSealer{}, Logger: logger,
		Location: time.UTC,
		Policy:   Policy{Allowed: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}},
		Now:      func() time.Time { return env.clock },
	})
	return env
}

func (e *testEnv) connect(t *testing.T) {
	t.Helper()
	if _, err := e.svc.Save(context.Background(), e.user.ID, Update{EndpointURL: e.endpoint, Token: testToken}); err != nil {
		t.Fatalf("saving the connection: %v", err)
	}
}

func bought(item *models.Item, done bool) *models.Item {
	c := *item
	c.Done = done
	return &c
}

// tick checks e.item off (or un-checks it), as the handlers report it.
func (e *testEnv) tick(done bool) {
	e.svc.ItemChanged(context.Background(), e.user.ID, e.list, bought(e.item, !done), bought(e.item, done))
}

func (e *testEnv) settings(t *testing.T) Settings {
	t.Helper()
	s, err := e.svc.Get(context.Background(), e.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPurchaseRecordedThenCancelled(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.connect(t)

	env.tick(true)
	env.svc.SendDue(ctx)
	if _, log := env.fluv.snapshot(); len(log) != 0 {
		t.Fatalf("sent before the settle delay: %v", log)
	}
	if s := env.settings(t); s.Pending != 1 {
		t.Fatalf("pending = %d, want 1", s.Pending)
	}

	env.advance(settleDelay)
	env.svc.SendDue(ctx)
	purchases, _ := env.fluv.snapshot()
	if len(purchases) != 1 {
		t.Fatalf("Fluv holds %d purchases, want 1", len(purchases))
	}
	for id, p := range purchases {
		want := purchaseBody{ID: id, Title: "Lait", Store: "Lidl", Quantity: 2, AmountMinor: 220, Date: env.clock.Add(-settleDelay).UTC().Format(time.DateOnly)}
		if p != want {
			t.Errorf("recorded %+v, want %+v", p, want)
		}
		if !strings.HasPrefix(id, "trakka-") {
			t.Errorf("reference %q", id)
		}
	}
	if s := env.settings(t); s.Pending != 0 || s.Failed != 0 || s.LastSuccessAt == nil {
		t.Fatalf("after delivery: %+v", s)
	}

	// Sending it again (a crash after the POST, before it was recorded)
	// changes nothing in Fluv.
	if err := env.db.ReleaseFluvPurchases(ctx); err != nil {
		t.Fatal(err)
	}

	env.tick(false)
	env.svc.SendDue(ctx)
	purchases, log := env.fluv.snapshot()
	if len(purchases) != 0 {
		t.Fatalf("un-checked item still in Fluv: %v (log %v)", purchases, log)
	}
	if len(log) != 2 || !strings.HasPrefix(log[1], "DELETE trakka-") {
		t.Fatalf("exchanges = %v, want a POST then a DELETE", log)
	}
}

func TestQuickUncheckAndRepriceNeverReachFluv(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.connect(t)

	// Checked off by mistake and un-checked at once: nothing is sent.
	env.tick(true)
	env.tick(false)
	env.advance(settleDelay)
	env.svc.SendDue(ctx)
	if _, log := env.fluv.snapshot(); len(log) != 0 {
		t.Fatalf("a purchase taken back within the settle delay was sent: %v", log)
	}

	// Checked off, then the price paid typed: one purchase, at that price.
	env.tick(true)
	paid := bought(env.item, true)
	paid.ActualPrice = floatPtr(0.95)
	env.svc.ItemChanged(ctx, env.user.ID, env.list, bought(env.item, true), paid)
	env.advance(settleDelay)
	env.svc.SendDue(ctx)
	purchases, log := env.fluv.snapshot()
	if len(log) != 1 || len(purchases) != 1 {
		t.Fatalf("exchanges %v, want a single POST", log)
	}
	for _, p := range purchases {
		if p.AmountMinor != 190 {
			t.Errorf("amount = %d, want 190 (2 × 0.95)", p.AmountMinor)
		}
	}
}

func TestRepriceAfterDeliveryReplacesThePurchase(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.connect(t)
	env.tick(true)
	env.advance(settleDelay)
	env.svc.SendDue(ctx)
	before, _ := env.fluv.snapshot()

	env.advance(48 * time.Hour)
	paid := bought(env.item, true)
	paid.ActualPrice = floatPtr(2)
	env.svc.ItemChanged(ctx, env.user.ID, env.list, bought(env.item, true), paid)
	env.advance(settleDelay)
	env.svc.SendDue(ctx)

	after, log := env.fluv.snapshot()
	if len(after) != 1 || len(log) != 3 {
		t.Fatalf("Fluv holds %v after %v, want one replaced purchase", after, log)
	}
	for id, p := range after {
		if _, old := before[id]; old {
			t.Error("the old reference was kept")
		}
		var original purchaseBody
		for _, o := range before {
			original = o
		}
		if p.AmountMinor != 400 || p.Date != original.Date {
			t.Errorf("replacement %+v: want 400 cents on the original date %s", p, original.Date)
		}
	}
}

func TestFailuresAreClassified(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.connect(t)

	// Transient: retried with a backoff, then delivered.
	env.fluv.failWith = http.StatusServiceUnavailable
	env.tick(true)
	env.advance(settleDelay)
	env.svc.SendDue(ctx)
	if s := env.settings(t); s.Pending != 1 || s.Failed != 0 || s.LastErrorCode != "forced" {
		t.Fatalf("after a 503: %+v", s)
	}
	env.fluv.failWith = 0
	env.advance(30 * time.Second)
	env.svc.SendDue(ctx)
	if purchases, _ := env.fluv.snapshot(); len(purchases) != 0 {
		t.Fatal("retried before the backoff")
	}
	env.advance(time.Minute)
	env.svc.SendDue(ctx)
	if purchases, _ := env.fluv.snapshot(); len(purchases) != 1 {
		t.Fatal("not delivered after the backoff")
	}

	// Permanent: a revoked token waits for the user.
	env.fluv.mu.Lock()
	env.fluv.token = "fluv_trk_zzzzzzzzzzzzzzzzzzzzzzzzzz"
	env.fluv.mu.Unlock()
	env.tick(false)
	env.svc.SendDue(ctx)
	s := env.settings(t)
	if s.Failed != 1 || s.LastErrorCode != "unauthenticated" {
		t.Fatalf("after a 401: %+v", s)
	}
	integ, err := env.db.GetFluvIntegration(ctx, env.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastError := integ.LastErrorAt
	env.advance(maxBackoff * 2)
	env.svc.SendDue(ctx)
	if integ, _ := env.db.GetFluvIntegration(ctx, env.user.ID); integ.LastErrorAt != lastError {
		t.Fatal("a failed purchase was retried on its own")
	}
	// A new token, saved: the failed cancellation goes through.
	if _, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: env.endpoint, Token: "fluv_trk_zzzzzzzzzzzzzzzzzzzzzzzzzz"}); err != nil {
		t.Fatal(err)
	}
	env.svc.SendDue(ctx)
	if purchases, _ := env.fluv.snapshot(); len(purchases) != 0 {
		t.Fatal("cancellation not delivered after the token was fixed")
	}
	if s := env.settings(t); s.Failed != 0 || s.Pending != 0 {
		t.Fatalf("after the fix: %+v", s)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	var leaked bool
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != purchasesPath {
			leaked = true
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	if _, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: redirector.URL + purchasesPath, Token: testToken}); err != nil {
		t.Fatal(err)
	}
	env.tick(true)
	env.advance(settleDelay)
	env.svc.SendDue(ctx)
	if leaked {
		t.Fatal("the redirect was followed, with the token")
	}
	if s := env.settings(t); s.Failed != 1 || s.LastErrorCode != "redirect_refused" {
		t.Fatalf("after a redirect: %+v", s)
	}
}

func TestOnlyConnectedBuyersOnPurchaseLists(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	env.tick(true) // not connected
	if s := env.settings(t); s.Configured || s.Pending != 0 {
		t.Fatalf("queued without a connection: %+v", s)
	}

	env.connect(t)
	disabled := false
	if _, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: env.endpoint, Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	env.tick(true)
	if s := env.settings(t); !s.TokenSet || s.Enabled || s.Pending != 0 {
		t.Fatalf("queued while paused: %+v", s)
	}

	if _, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: env.endpoint}); err != nil {
		t.Fatal(err)
	}
	todo := *env.list
	todo.Type = "todo"
	env.svc.ItemChanged(ctx, env.user.ID, &todo, bought(env.item, false), bought(env.item, true))
	noPrice := bought(env.item, true)
	noPrice.Price = nil
	env.svc.ItemChanged(ctx, env.user.ID, env.list, bought(noPrice, false), noPrice)
	if s := env.settings(t); s.Pending != 0 {
		t.Fatalf("queued a task or an item without a price: %+v", s)
	}

	integ, err := env.db.GetFluvIntegration(ctx, env.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(integ.TokenSealed, testToken) {
		t.Fatal("the token is stored as is")
	}
}

func TestServerUncheckDetachesThePurchase(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.connect(t)
	env.tick(true)
	env.advance(settleDelay)
	env.svc.SendDue(ctx)

	// The product is added again on the Courses list: the item comes back
	// to buy, but the purchase happened.
	if _, err := env.db.ReactivateItem(ctx, env.item.ID, 1, nil, false); err != nil {
		t.Fatal(err)
	}
	env.tick(false)
	env.svc.SendDue(ctx)
	if purchases, log := env.fluv.snapshot(); len(purchases) != 1 || len(log) != 1 {
		t.Fatalf("a server-side un-check cancelled the purchase: %v", log)
	}
}

func TestTestConnection(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if _, err := env.svc.Test(ctx, env.user.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("test without a connection: %v", err)
	}
	env.connect(t)
	if got, err := env.svc.Test(ctx, env.user.ID); err != nil || !got.OK {
		t.Fatalf("test = %+v, %v; want ok", got, err)
	}
	env.fluv.failWith = http.StatusForbidden
	if got, _ := env.svc.Test(ctx, env.user.ID); got.OK || got.Status != http.StatusForbidden {
		t.Fatalf("test against a refusing Fluv = %+v", got)
	}
	if purchases, _ := env.fluv.snapshot(); len(purchases) != 0 {
		t.Fatal("the test recorded something")
	}

	// Nothing listens on the pinned address any more.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	if _, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: "http://" + addr + purchasesPath}); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.svc.Test(ctx, env.user.ID); got.OK || got.Code != "unreachable" {
		t.Fatalf("test against nothing = %+v", got)
	}
}

// TestAutomaticIPLock: saving resolves the host once and locks it; sending
// never resolves; a test re-locks only where Fluv accepts it.
func TestAutomaticIPLock(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(strings.TrimSuffix(env.endpoint, purchasesPath), "http://"))
	resolver := &fakeResolver{answers: map[string][]netip.Addr{"fluv.lan": addrs("127.0.0.1")}}
	env.svc.resolve = resolver.resolve
	env.svc.policy = Policy{Allowed: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	endpoint := "http://fluv.lan:" + port + purchasesPath

	got, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: endpoint, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.PinnedIPs, ",") != "127.0.0.1" || resolver.calls != 1 {
		t.Fatalf("after saving: pins %v, %d lookups", got.PinnedIPs, resolver.calls)
	}

	// The DNS record now points elsewhere (or the resolver is poisoned):
	// purchases still go to the locked address, without any lookup.
	resolver.answers["fluv.lan"] = addrs("127.0.0.2")
	env.tick(true)
	env.advance(settleDelay)
	env.svc.SendDue(ctx)
	if purchases, _ := env.fluv.snapshot(); len(purchases) != 1 {
		t.Fatal("purchase not delivered to the locked address")
	}
	if resolver.calls != 1 {
		t.Fatalf("sending resolved the host (%d lookups)", resolver.calls)
	}

	// A test resolves again; nothing answers at the new address, so the
	// lock stays where Fluv is.
	res, err := env.svc.Test(ctx, env.user.ID)
	if err != nil || res.OK || res.Relocked {
		t.Fatalf("test against the moved record = %+v, %v", res, err)
	}
	if s := env.settings(t); strings.Join(s.PinnedIPs, ",") != "127.0.0.1" {
		t.Fatalf("a failed test moved the lock to %v", s.PinnedIPs)
	}

	// Fluv really moved (127.0.0.2 is loopback too: the test server
	// answers there): the test re-locks.
	ln, err := net.Listen("tcp", "127.0.0.2:"+port)
	if err != nil {
		t.Skipf("127.0.0.2 not usable here: %v", err)
	}
	moved := &http.Server{Handler: env.fluv, ReadHeaderTimeout: time.Second}
	go func() { _ = moved.Serve(ln) }()
	t.Cleanup(func() { _ = moved.Close() })
	res, err = env.svc.Test(ctx, env.user.ID)
	if err != nil || !res.OK || !res.Relocked || strings.Join(res.PinnedIPs, ",") != "127.0.0.2" {
		t.Fatalf("test after Fluv moved = %+v, %v", res, err)
	}
	if s := env.settings(t); strings.Join(s.PinnedIPs, ",") != "127.0.0.2" {
		t.Fatalf("lock not moved: %v", s.PinnedIPs)
	}
	res, _ = env.svc.Test(ctx, env.user.ID)
	if !res.OK || res.Relocked {
		t.Fatalf("a test with an unchanged record = %+v", res)
	}
}

func TestListDefaultsAndShippingAreSent(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.connect(t)
	const account, category = "0192f3a4-5b6c-7d8e-9f01-23456789abcd", "0192f3a4-5b6c-7d8e-9f01-23456789abce"

	// Refused values never reach the table.
	for _, bad := range []ListDefaults{
		{AccountID: "checking"},
		{CategoryID: "0192f3a4-5b6c-7d8e-9f01-23456789abcz"},
		{BudgetType: "luxury"},
	} {
		var verr *ValidationError
		if _, err := env.svc.SaveListDefaults(ctx, env.user.ID, env.list.ID, bad); !errors.As(err, &verr) {
			t.Errorf("SaveListDefaults(%+v) = %v, want a ValidationError", bad, err)
		}
	}
	saved, err := env.svc.SaveListDefaults(ctx, env.user.ID, env.list.ID,
		ListDefaults{AccountID: " " + strings.ToUpper(account) + " ", CategoryID: category, BudgetType: "wants"})
	if err != nil {
		t.Fatal(err)
	}
	if saved != (ListDefaults{AccountID: account, CategoryID: category, BudgetType: "want"}) {
		t.Fatalf("saved %+v, want canonical ids and \"want\"", saved)
	}
	if got, _ := env.svc.ListDefaults(ctx, env.user.ID, env.list.ID); got != saved {
		t.Fatalf("ListDefaults = %+v, want %+v", got, saved)
	}

	env.item.ShippingPrice = floatPtr(4.99)
	env.tick(true)
	env.advance(settleDelay)
	env.svc.SendDue(ctx)
	purchases, _ := env.fluv.snapshot()
	var first purchaseBody
	for _, p := range purchases {
		first = p
	}
	if len(purchases) != 1 || first.AccountID != account || first.CategoryID != category || first.BudgetType != "want" ||
		first.ShippingMinor != 499 || first.AmountMinor != 220 {
		t.Fatalf("Fluv holds %+v, want the list's defaults and 499 cents of shipping", purchases)
	}

	// Changing the defaults afterwards doesn't move the purchase: a
	// correction is filed like the original, with the new shipping fee.
	if _, err := env.svc.SaveListDefaults(ctx, env.user.ID, env.list.ID, ListDefaults{BudgetType: "need"}); err != nil {
		t.Fatal(err)
	}
	cheaper := bought(env.item, true)
	cheaper.ShippingPrice = nil
	env.svc.ItemChanged(ctx, env.user.ID, env.list, bought(env.item, true), cheaper)
	env.advance(settleDelay)
	env.svc.SendDue(ctx)
	purchases, log := env.fluv.snapshot()
	if len(purchases) != 1 || len(log) != 3 {
		t.Fatalf("Fluv holds %v after %v, want the purchase replaced", purchases, log)
	}
	for id, p := range purchases {
		if id == first.ID || p.ShippingMinor != 0 || p.AccountID != account || p.BudgetType != "want" {
			t.Errorf("replacement %+v: want a new reference, no shipping, filed like the original", p)
		}
	}

	// Cleared defaults: nothing optional is sent — what a Fluv that
	// predates these fields still accepts.
	if _, err := env.svc.SaveListDefaults(ctx, env.user.ID, env.list.ID, ListDefaults{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.svc.ListDefaults(ctx, env.user.ID, env.list.ID); got != (ListDefaults{}) {
		t.Fatalf("cleared defaults read back as %+v", got)
	}
	other, err := env.db.CreateItem(ctx, env.list.ID, "Pain", nil, 1, floatPtr(1.5), false, 1, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	env.svc.ItemChanged(ctx, env.user.ID, env.list, bought(other, false), bought(other, true))
	env.advance(settleDelay)
	env.svc.SendDue(ctx)
	purchases, _ = env.fluv.snapshot()
	for _, p := range purchases {
		if p.Title == "Pain" && (p.AccountID != "" || p.CategoryID != "" || p.BudgetType != "" || p.ShippingMinor != 0) {
			t.Errorf("purchase without defaults sent %+v", p)
		}
	}
	body, err := json.Marshal(purchaseBody{ID: "x", Title: "Pain", Quantity: 1, AmountMinor: 150, Date: "2026-11-03"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"account_id", "category_id", "budget_type", "shipping_amount_minor"} {
		if strings.Contains(string(body), field) {
			t.Errorf("unset %s is sent: %s", field, body)
		}
	}
}

// TestBuyerFilesOrSkipsOnePurchase: the check-off toast's "Options" — the
// buyer files their purchase in another Fluv account (in place before it
// is sent, cancelled and sent again after), takes it out of Fluv while the
// item stays bought, then sends it again; nobody else can.
func TestBuyerFilesOrSkipsOnePurchase(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.connect(t)
	const first, second = "0192f3a4-5b6c-7d8e-9f01-23456789abcd", "0192f3a4-5b6c-7d8e-9f01-23456789abce"
	item := bought(env.item, true)
	send := func() map[string]purchaseBody {
		t.Helper()
		env.advance(settleDelay)
		env.svc.SendDue(ctx)
		purchases, _ := env.fluv.snapshot()
		return purchases
	}
	set := func(u ItemPurchaseUpdate) ItemPurchase {
		t.Helper()
		view, err := env.svc.SetItemPurchase(ctx, env.user.ID, env.list, item, u)
		if err != nil {
			t.Fatalf("SetItemPurchase(%+v): %v", u, err)
		}
		return view
	}
	only := func(purchases map[string]purchaseBody) purchaseBody {
		t.Helper()
		if len(purchases) != 1 {
			t.Fatalf("Fluv holds %v, want one purchase", purchases)
		}
		for _, p := range purchases {
			return p
		}
		return purchaseBody{}
	}

	if view, err := env.svc.ItemPurchase(ctx, env.user.ID, env.item); err != nil || view != (ItemPurchase{}) {
		t.Fatalf("before the check-off: %+v, %v", view, err)
	}
	env.tick(true)
	if view, err := env.svc.ItemPurchase(ctx, env.user.ID, item); err != nil || view != (ItemPurchase{Queued: true}) {
		t.Fatalf("checked off: %+v, %v", view, err)
	}

	// Before it is sent: changed in place, one POST.
	if view := set(ItemPurchaseUpdate{AccountID: strings.ToUpper(first)}); view != (ItemPurchase{Queued: true, AccountID: first}) {
		t.Fatalf("account chosen: %+v", view)
	}
	sent := only(send())
	if _, log := env.fluv.snapshot(); sent.AccountID != first || len(log) != 1 {
		t.Fatalf("Fluv holds %+v after %v", sent, log)
	}

	// After: cancelled and sent again, on the same day.
	if view := set(ItemPurchaseUpdate{AccountID: second}); !view.Queued || view.AccountID != second {
		t.Fatalf("account changed after sending: %+v", view)
	}
	moved := only(send())
	if moved.ID == sent.ID || moved.AccountID != second || moved.Date != sent.Date {
		t.Fatalf("moved to %+v, from %+v", moved, sent)
	}

	// Skipped: taken out of Fluv, and a correction doesn't send it again.
	if view := set(ItemPurchaseUpdate{AccountID: second, Skip: true}); view != (ItemPurchase{Skipped: true, AccountID: second}) {
		t.Fatalf("skipped: %+v", view)
	}
	if purchases := send(); len(purchases) != 0 {
		t.Fatalf("Fluv still holds %v", purchases)
	}
	unpriced := bought(env.item, true)
	unpriced.Price = nil
	env.svc.ItemChanged(ctx, env.user.ID, env.list, item, unpriced)
	env.svc.ItemChanged(ctx, env.user.ID, env.list, unpriced, item)
	if purchases := send(); len(purchases) != 0 {
		t.Fatalf("a correction sent the skipped purchase: %v", purchases)
	}

	// Sent again, in the account chosen meanwhile.
	set(ItemPurchaseUpdate{AccountID: first, Skip: true})
	if view := set(ItemPurchaseUpdate{AccountID: first}); !view.Queued || view.AccountID != first {
		t.Fatalf("sent again: %+v", view)
	}
	if again := only(send()); again.AccountID != first || again.Date != sent.Date {
		t.Fatalf("sent again as %+v", again)
	}

	// Un-checking ends the skip: the next purchase is sent as usual.
	set(ItemPurchaseUpdate{Skip: true})
	env.tick(false)
	env.tick(true)
	if next := only(send()); next.AccountID != "" {
		t.Fatalf("next purchase %+v, want Fluv's own account", next)
	}

	// Nobody else's, and only ids of Fluv's shape.
	hash := "x"
	other, err := env.db.CreateUser(ctx, "other@example.com", &hash, nil, nil, "Other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.SetItemPurchase(ctx, other.ID, env.list, item, ItemPurchaseUpdate{Skip: true}); !errors.Is(err, ErrNoPurchase) {
		t.Fatalf("another user: %v, want ErrNoPurchase", err)
	}
	if view, err := env.svc.ItemPurchase(ctx, other.ID, item); err != nil || view != (ItemPurchase{}) {
		t.Fatalf("another user sees %+v, %v", view, err)
	}
	var verr *ValidationError
	if _, err := env.svc.SetItemPurchase(ctx, env.user.ID, env.list, item, ItemPurchaseUpdate{AccountID: "checking"}); !errors.As(err, &verr) {
		t.Fatalf("malformed account: %v, want a ValidationError", err)
	}
	if _, err := env.svc.SetItemPurchase(ctx, env.user.ID, env.list, env.item, ItemPurchaseUpdate{Skip: true}); !errors.Is(err, ErrNoPurchase) {
		t.Fatalf("item not bought: %v, want ErrNoPurchase", err)
	}
}

// TestMetadata: the accounts and categories are read with the token from
// the pinned address, cleaned, cached a while; a Fluv without the endpoint
// or a bad answer is ok false, never an error.
func TestMetadata(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if _, err := env.svc.Metadata(ctx, env.user.ID, "fr"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("metadata without a connection: %v", err)
	}
	env.connect(t)

	const (
		cash    = "0b0e5a8e-1c1d-4e7a-9f3a-7d2b8c9e0f11"
		bank    = "1c1f6b9f-2d2e-4f8b-8a4b-8e3c9d0f1a22"
		grocery = "2d207cab-3e3f-4a9c-9b5c-9f4d0e1a2b33"
	)
	env.fluv.metadata = map[string]any{
		"default_account_id": strings.ToUpper(cash),
		"accounts": []map[string]any{
			{"id": cash, "name": "Portefeuille", "kind": "cash"},
			{"id": bank, "name": "  Compte\u202e courant\x07 ", "kind": "Checking!"},
			{"id": cash, "name": "Duplicate"},
			{"id": "not-a-uuid", "name": "Broken"},
		},
		"categories": []map[string]any{
			{"id": grocery, "name": "Groceries", "emoji": "🛒"},
			{"id": "", "name": "No id"},
		},
	}
	got, err := env.svc.Metadata(ctx, env.user.ID, "EN")
	if err != nil || !got.OK {
		t.Fatalf("metadata = %+v, %v; want ok", got, err)
	}
	want := Metadata{
		OK: true, DefaultAccountID: cash,
		Accounts:   []MetadataAccount{{ID: cash, Name: "Portefeuille", Kind: "cash"}, {ID: bank, Name: "Compte courant"}},
		Categories: []MetadataCategory{{ID: grocery, Name: "Groceries", Emoji: "🛒"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %+v\nwant       %+v", got, want)
	}
	if env.fluv.lang != "en" {
		t.Fatalf("Accept-Language = %q, want en", env.fluv.lang)
	}

	countGets := func() int {
		_, log := env.fluv.snapshot()
		n := 0
		for _, l := range log {
			if l == "GET metadata" {
				n++
			}
		}
		return n
	}
	// Cached per language a while, then asked again.
	_, _ = env.svc.Metadata(ctx, env.user.ID, "en")
	if n := countGets(); n != 1 {
		t.Fatalf("%d requests, want the answer reused", n)
	}
	_, _ = env.svc.Metadata(ctx, env.user.ID, "de") // French
	if n := countGets(); n != 2 || env.fluv.lang != "fr" {
		t.Fatalf("%d requests in %q, want a second one in fr", n, env.fluv.lang)
	}
	env.advance(metadataTTL)
	_, _ = env.svc.Metadata(ctx, env.user.ID, "en")
	if n := countGets(); n != 3 {
		t.Fatalf("%d requests, want the answer asked again once stale", n)
	}

	// Saving the connection forgets the cache. A Fluv older than the
	// endpoint answers 404.
	env.fluv.metadata = nil
	env.connect(t)
	if got, err := env.svc.Metadata(ctx, env.user.ID, "en"); err != nil || got.OK || got.Status != http.StatusNotFound || got.Code != "not_found" {
		t.Fatalf("metadata from an older Fluv = %+v, %v", got, err)
	}

	// An answer too large to be names and ids is refused whole.
	env.fluv.metadata = map[string]any{"accounts": []any{}, "categories": []any{}, "padding": strings.Repeat("x", maxMetadataBody)}
	env.connect(t)
	if got, _ := env.svc.Metadata(ctx, env.user.ID, "en"); got.OK || got.Code != "invalid_response" {
		t.Fatalf("metadata too large = %+v", got)
	}

	// A refused token.
	env.fluv.token = "fluv_trk_otherotherotherotherotherot"
	env.connect(t)
	if got, _ := env.svc.Metadata(ctx, env.user.ID, "en"); got.OK || got.Status != http.StatusUnauthorized {
		t.Fatalf("metadata with a refused token = %+v", got)
	}
	if settings := env.settings(t); settings.LastErrorCode != "" {
		t.Fatalf("a metadata failure marked the connection failing: %q", settings.LastErrorCode)
	}
}

func TestCleanLabel(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"  Courses ", 80, "Courses"},
		{"a\u202eb\u2066c\x00d", 80, "abcd"},
		{"👨\u200d👩\u200d👧", 8, "👨\u200d👩\u200d👧"},
		{"abcdef", 3, "abc"},
		{"\xff\xfeok", 80, "ok"},
	} {
		if got := cleanLabel(c.in, c.max); got != c.want {
			t.Errorf("cleanLabel(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
