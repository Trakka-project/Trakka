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
	"strings"
	"sync"
	"testing"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
)

const testToken = "fluv_trk_abcdefghijklmnopqrstuvwxyz"

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
		var p purchaseBody
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			problem(http.StatusBadRequest, "invalid_json")
			return
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
	if _, err := e.svc.Save(context.Background(), e.user.ID, Update{EndpointURL: e.endpoint, PinnedIPs: []string{"127.0.0.1"}, Token: testToken}); err != nil {
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
	if _, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: env.endpoint, PinnedIPs: []string{"127.0.0.1"}, Token: "fluv_trk_zzzzzzzzzzzzzzzzzzzzzzzzzz"}); err != nil {
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
	if _, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: redirector.URL + purchasesPath, PinnedIPs: []string{"127.0.0.1"}, Token: testToken}); err != nil {
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
	if _, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: env.endpoint, PinnedIPs: []string{"127.0.0.1"}, Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	env.tick(true)
	if s := env.settings(t); !s.TokenSet || s.Enabled || s.Pending != 0 {
		t.Fatalf("queued while paused: %+v", s)
	}

	if _, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: env.endpoint, PinnedIPs: []string{"127.0.0.1"}}); err != nil {
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
	if _, err := env.svc.Save(ctx, env.user.ID, Update{EndpointURL: "http://" + addr + purchasesPath, PinnedIPs: []string{"127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.svc.Test(ctx, env.user.ID); got.OK || got.Code != "unreachable" {
		t.Fatalf("test against nothing = %+v", got)
	}
}
