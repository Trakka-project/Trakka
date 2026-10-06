package scraper

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// dealabsThreadAttr builds one search result the way Dealabs server-renders
// it: a single-quoted data-vue3 attribute holding the component's JSON.
func dealabsThreadAttr(id, slug, title, extra string) string {
	return fmt.Sprintf(`<div data-vue3='{"name":"ThreadMainListItemNormalizer","props":{"thread":{"threadId":"%s","titleSlug":"%s","title":"%s","type":"Deal","status":"Activated","deletedAt":null%s}}}'></div>`,
		id, slug, title, extra)
}

func TestParseDealabsDeals(t *testing.T) {
	page := `<html><body>` +
		`<div data-vue3='{"name":"TagsCarousel","props":{"results":[]}}'></div>` +
		dealabsThreadAttr("3420521", "console-nintendo-switch-2", "Console Nintendo Switch 2",
			`,"isExpired":false,"isLocal":false,"price":419,"merchant":{"merchantName":"Amazon"}`) +
		dealabsThreadAttr("1", "expired", "Expired deal", `,"isExpired":true,"price":10`) +
		dealabsThreadAttr("2", "local", "Local deal", `,"isExpired":false,"isLocal":true,"price":10`) +
		dealabsThreadAttr("3", "free", "No price", `,"isExpired":false,"price":0`) +
		dealabsThreadAttr("4", "null-price", "Null price", `,"isExpired":false,"price":null`) +
		dealabsThreadAttr("5", "Bad Slug!", "Bad slug", `,"isExpired":false,"price":10`) +
		dealabsThreadAttr("x6", "bad-id", "Bad id", `,"isExpired":false,"price":10`) +
		`<div data-vue3='{"name":"ThreadMainListItemNormalizer","props":{"thread":{"threadId":"7","titleSlug":"voucher","title":"Code promo","type":"Voucher","status":"Activated","isExpired":false,"price":5}}}'></div>` +
		`<div data-vue3='{"name":"ThreadMainListItemNormalizer","props":{"thread":{"threadId":"8","titleSlug":"deleted","title":"Deleted","type":"Deal","status":"Activated","isExpired":false,"deletedAt":1790000000,"price":5}}}'></div>` +
		dealabsThreadAttr("9", "casque-l-ecoute-9", `Casque d\u0027écoute Sony`, `,"isExpired":false,"price":144.67`) +
		`<div data-vue3='not json'></div>` +
		`</body></html>`

	deals, err := parseDealabsDeals(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parseDealabsDeals: %v", err)
	}
	if len(deals) != 2 {
		t.Fatalf("got %d deals, want 2: %+v", len(deals), deals)
	}
	want := Deal{Title: "Console Nintendo Switch 2", Price: 419, URL: "https://www.dealabs.com/bons-plans/console-nintendo-switch-2-3420521", Merchant: "Amazon"}
	if deals[0] != want {
		t.Errorf("deals[0] = %+v, want %+v", deals[0], want)
	}
	if deals[1].Title != "Casque d'écoute Sony" || deals[1].Price != 144.67 || deals[1].Merchant != "" {
		t.Errorf("deals[1] = %+v", deals[1])
	}
}

func TestDealSearchQuery(t *testing.T) {
	tests := []struct {
		title string
		want  string
		ok    bool
	}{
		{"Sony WH-1000XM5", "sony wh 1000xm5", true},
		{"Dyson V15", "dyson v15", true},
		{"Nintendo Switch OLED", "nintendo switch oled", true},
		{"iPhone 15", "iphone 15", true},
		{"Casque à réduction de bruit", "casque reduction bruit", true},
		{"Casque Sony", "", false},
		{"Lait", "", false},
		{"  ", "", false},
		{"Pâtes", "", false},
		{"Casque Bluetooth sans Fil Sony WH-1000XM5 - à réduction de Bruit, Argenté", "casque bluetooth fil sony wh 1000xm5", true},
	}
	for _, tt := range tests {
		got, ok := DealSearchQuery(tt.title)
		if got != tt.want || ok != tt.ok {
			t.Errorf("DealSearchQuery(%q) = %q, %v; want %q, %v", tt.title, got, ok, tt.want, tt.ok)
		}
	}
}

func TestDealMatches(t *testing.T) {
	tests := []struct {
		query, title string
		want         bool
	}{
		{"sony wh 1000xm5", "Casque sans fil Sony WH-1000XM5 - Réduction de bruit active", true},
		{"sony wh 1000xm5", "Casque Sans Fil Sony WH-1000XM6 - Noir", false},
		{"sony wh1000xm5", "Casque Sony WH-1000XM5", true},
		{"casque reduction bruit", "Casque à réduction de bruit", true},
		{"nintendo switch 2", "Console Nintendo Switch 2", true},
		{"nintendo switch 2", "Console Nintendo Switch OLED", false},
		// A short word must be a word of its own, not part of another one.
		{"tv 55", "Téléviseur 55 pouces", false},
	}
	for _, tt := range tests {
		if got := dealMatches(tt.query, tt.title); got != tt.want {
			t.Errorf("dealMatches(%q, %q) = %v, want %v", tt.query, tt.title, got, tt.want)
		}
	}
}

func TestBestDeal(t *testing.T) {
	deals := []Deal{
		{Title: "Pikmin 4 sur Nintendo Switch 2", Price: 51.99},  // matches, but far too cheap to be the console
		{Title: "Console Nintendo Switch 2", Price: 459},         // matches
		{Title: "Console Nintendo Switch 2 - Bleu", Price: 419},  // matches, cheapest
		{Title: "Console Nintendo Switch OLED", Price: 299},      // different product
		{Title: "Console Nintendo Switch 2 + Mario", Price: 509}, // dearer than now
	}
	best, ok := BestDeal(deals, "nintendo switch 2", 469)
	if !ok || best.Price != 419 {
		t.Fatalf("BestDeal = %+v, %v; want the 419 deal", best, ok)
	}
	if _, ok := BestDeal(deals, "nintendo switch 2", 419); ok {
		t.Error("BestDeal proposed a deal no cheaper than the current price")
	}
	if _, ok := BestDeal(nil, "nintendo switch 2", 469); ok {
		t.Error("BestDeal found a deal in an empty list")
	}
}

func TestDealabsDealState(t *testing.T) {
	page := func(thread string) []byte {
		return []byte(`<html><script>var modulesConfig = {"a":1};
window.__INITIAL_STATE__ = {"settings":{},"threadDetail":` + thread + `,"comments":[]};
window.__RESPONSE_DATA__ = {};</script><div data-vue3='{"name":"ThreadMainListItemNormalizer","props":{"thread":{"price":1}}}'></div></html>`)
	}
	live, ok := dealabsDealState(page(`{"threadId":"1","type":"Deal","status":"Activated","isExpired":false,"deletedAt":null,"price":419}`))
	if !ok || !live.live() || *live.Price != 419 {
		t.Fatalf("live deal = %+v, %v", live, ok)
	}
	expired, ok := dealabsDealState(page(`{"threadId":"1","type":"Deal","status":"Activated","isExpired":true,"deletedAt":null,"price":190.58}`))
	if !ok || expired.live() {
		t.Fatalf("expired deal read as live: %+v", expired)
	}
	if _, ok := dealabsDealState([]byte(`<html>no state</html>`)); ok {
		t.Error("found a deal on a page without state")
	}
	if _, ok := dealabsDealState(page(`null`)); ok {
		t.Error("found a deal on a page without threadDetail")
	}
}

func TestIsDealabsDealURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://www.dealabs.com/bons-plans/console-nintendo-switch-2-3420521": true,
		"https://WWW.DEALABS.COM/bons-plans/x-1":                               true,
		"https://www.dealabs.com/search?q=switch":                              false,
		"https://www.amazon.fr/bons-plans/x-1":                                 false,
	} {
		u, _ := url.Parse(raw)
		if got := isDealabsDealURL(u); got != want {
			t.Errorf("isDealabsDealURL(%q) = %v, want %v", raw, got, want)
		}
	}
}
