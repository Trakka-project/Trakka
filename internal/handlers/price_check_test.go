package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"trakka/internal/models"
	"trakka/internal/scraper"
)

// fakeProductPage serves the price checks a product page whose price the
// test changes between two checks — or a fetch failure — through
// Application.productPageHook.
type fakeProductPage struct {
	mu      sync.Mutex
	price   *float64
	expired bool
	err     error
	fetches int
}

func (f *fakeProductPage) set(price float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.price, f.err = &price, nil
}

func (f *fakeProductPage) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeProductPage) hook(context.Context, string) (*scraper.ProductInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if f.err != nil {
		return nil, f.err
	}
	info := &scraper.ProductInfo{Title: "Produit", DealExpired: f.expired}
	if f.price != nil && !f.expired {
		p := *f.price
		info.Price = &p
	}
	return info, nil
}

type priceCheckResponse struct {
	Alert *models.PriceAlert `json:"alert"`
	Item  *models.Item       `json:"item"`
	Check struct {
		Status        string   `json:"status"`
		ObservedPrice *float64 `json:"observed_price"`
		Error         string   `json:"error"`
	} `json:"check"`
}

func postPriceCheck(t *testing.T, app *Application, user *models.User, itemID int64) priceCheckResponse {
	t.Helper()
	id := strconv.FormatInt(itemID, 10)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/items/"+id+"/price-check", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	app.handleItemsPriceCheck(rec, withUser(req, user))
	if rec.Code != http.StatusOK {
		t.Fatalf("price check: %d %s", rec.Code, rec.Body.String())
	}
	var out priceCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestManualPriceCheckFollowsChangingPage: two "Vérifier le prix
// maintenant" in a row on an item whose price was typed in, the page's
// price changing in between. Each check must apply the page's price —
// the user asked for it — record it in the history, keep the movement for
// the ▲/▼ indicator, and report what it read.
func TestManualPriceCheckFollowsChangingPage(t *testing.T) {
	app := newTestApplication(t)
	recordPushes(app)
	page := &fakeProductPage{}
	app.productPageHook = page.hook
	ctx := context.Background()
	owner, _, item := trackedItemFixture(t, app, false) // typed in: 100

	page.set(120)
	out := postPriceCheck(t, app, owner, item.ID)
	if out.Check.Status != "ok" || out.Check.ObservedPrice == nil || *out.Check.ObservedPrice != 120 {
		t.Fatalf("first check reported %+v", out.Check)
	}
	if out.Item.Price == nil || *out.Item.Price != 120 || !out.Item.PriceAuto ||
		out.Item.PreviousPrice == nil || *out.Item.PreviousPrice != 100 || out.Item.PriceChangedAt == nil {
		t.Fatalf("after the first check the item is %+v", out.Item)
	}

	page.set(95)
	out = postPriceCheck(t, app, owner, item.ID)
	if out.Check.Status != "ok" || *out.Check.ObservedPrice != 95 {
		t.Fatalf("second check reported %+v", out.Check)
	}
	if *out.Item.Price != 95 || *out.Item.PreviousPrice != 120 {
		t.Fatalf("after the second check the item is %+v", out.Item)
	}
	history, err := app.DB.ListPriceHistory(ctx, item.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Price != 120 || history[1].Price != 95 {
		t.Fatalf("history = %+v, want 120 then 95", history)
	}
	if page.fetches != 2 {
		t.Fatalf("the page was fetched %d times, want once per check", page.fetches)
	}

	// The same price again: nothing moves, and it says so.
	out = postPriceCheck(t, app, owner, item.ID)
	if out.Check.Status != "ok" || *out.Check.ObservedPrice != 95 || *out.Item.Price != 95 {
		t.Fatalf("unchanged check: %+v, item %+v", out.Check, out.Item)
	}
	if history, _ := app.DB.ListPriceHistory(ctx, item.ID, 10); len(history) != 2 {
		t.Fatalf("an unchanged price was recorded again: %+v", history)
	}
}

// TestManualPriceCheckReportsFailures: a page that can't be read, carries
// no price, or is an expired deal is never reported as an unchanged price.
func TestManualPriceCheckReportsFailures(t *testing.T) {
	app := newTestApplication(t)
	recordPushes(app)
	page := &fakeProductPage{}
	app.productPageHook = page.hook
	owner, _, item := trackedItemFixture(t, app, true)

	page.fail(errors.New("fetching https://shop.example.com/product: unexpected status 403"))
	out := postPriceCheck(t, app, owner, item.ID)
	if out.Check.Status != "unreachable" || out.Check.Error == "" || out.Check.ObservedPrice != nil {
		t.Fatalf("blocked page reported %+v", out.Check)
	}
	if *out.Item.Price != 100 {
		t.Fatalf("a failed check changed the price: %+v", out.Item)
	}

	page.mu.Lock()
	page.err, page.price = nil, nil
	page.mu.Unlock()
	if out := postPriceCheck(t, app, owner, item.ID); out.Check.Status != "no_price" {
		t.Fatalf("page without a price reported %+v", out.Check)
	}

	page.mu.Lock()
	page.expired = true
	page.mu.Unlock()
	if out := postPriceCheck(t, app, owner, item.ID); out.Check.Status != "deal_expired" {
		t.Fatalf("expired deal reported %+v", out.Check)
	}
}
