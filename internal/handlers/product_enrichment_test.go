package handlers

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"trakka/internal/catalog"
	"trakka/internal/models"
	"trakka/internal/scraper"
)

// Open Food Facts results like the ones its search really answers (see
// the comments of pickSearchMatch).
var (
	offWater = []scraper.SearchProduct{
		{Barcode: "6111035000058", Name: "Eau minérale naturelle", Brands: "Sidi Ali", NutriScore: "A", NovaGroup: 1,
			Categories: []string{"en:beverages", "en:waters", "en:mineral-waters"}, ImageURL: "https://images.openfoodfacts.org/sidi.jpg"},
		{Barcode: "3274080005003", Name: "isabelle", Brands: "Cristaline", NutriScore: "A", Categories: []string{"en:beverages", "en:waters"}},
	}
	offKerCadelac = []scraper.SearchProduct{
		{Name: "Le Moelleux", Brands: "Ker Cadélac", NutriScore: "D", NovaGroup: 4, Categories: []string{"en:snacks", "en:sweet-snacks", "en:biscuits-and-cakes"}},
		{Name: "Madeleines extra moelleuses nature", Brands: "Ker Cadélac", NutriScore: "D", NovaGroup: 4, Categories: []string{"en:biscuits-and-cakes"}},
		{Name: "Cake Marbré", Brands: "Ker Cadélac", NutriScore: "E", NovaGroup: 4, Categories: []string{"en:biscuits-and-cakes"}},
	}
)

func TestPickSearchMatch(t *testing.T) {
	cristaline := []scraper.SearchProduct{
		{Name: "isabelle", Brands: "Cristaline", NutriScore: "A", Categories: []string{"en:waters"}},
		{Name: "Eau de source", Brands: "Cristaline", NutriScore: "A", Categories: []string{"en:waters"}},
		{Name: "Pétillante", Brands: "Cristaline", Categories: []string{"en:waters"}},
	}
	nutella := []scraper.SearchProduct{{Name: "Nutella", Brands: "Ferrero", NutriScore: "E",
		Categories: []string{"en:sweet-spreads"}, ImageURL: "https://images.openfoodfacts.org/nutella.jpg"}}
	for _, tc := range []struct {
		query   string
		results []scraper.SearchProduct
		want    productEnrichment
	}{
		// A generic name: aisle and grade, but no picture of one brand's bottle.
		{"Eau", offWater, productEnrichment{Aisle: catalog.AisleDrinks, NutriScore: "A", NovaGroup: 1, MatchedName: "Eau minérale naturelle"}},
		// A brand: its aisle; its products disagree on the grade, but agree
		// on the NOVA group.
		{"ker-cadelac", offKerCadelac, productEnrichment{Aisle: catalog.AisleBreakfast, NovaGroup: 4}},
		// A brand whose products agree.
		{"Cristaline", cristaline, productEnrichment{Aisle: catalog.AisleDrinks, NutriScore: "A"}},
		// A word starting a name's word matches; a close name gets the image.
		{"madeleine", offKerCadelac, productEnrichment{Aisle: catalog.AisleBreakfast, NutriScore: "D", NovaGroup: 4, MatchedName: "Madeleines extra moelleuses nature"}},
		{"nutella", nutella, productEnrichment{Aisle: catalog.AisleBreakfast, NutriScore: "E", ImageURL: "https://images.openfoodfacts.org/nutella.jpg", MatchedName: "Nutella"}},
		// Nothing relevant: Open Food Facts' ranking alone isn't trusted.
		{"Prince", offWater, productEnrichment{}},
		{"eau", nil, productEnrichment{}},
		{"  ", offWater, productEnrichment{}},
	} {
		if got := pickSearchMatch(tc.query, tc.results); got != tc.want {
			t.Errorf("pickSearchMatch(%q) = %+v, want %+v", tc.query, got, tc.want)
		}
	}
	// An image URL that isn't plain http(s) is never kept.
	bad := []scraper.SearchProduct{{Name: "Nutella", ImageURL: "javascript:alert(1)"}}
	if got := pickSearchMatch("Nutella", bad); got.ImageURL != "" {
		t.Fatalf("unsafe image kept: %q", got.ImageURL)
	}
}

// fakeProductSearch records the searches made and answers from results.
type fakeProductSearch struct {
	mu      sync.Mutex
	asked   []string
	results map[string][]scraper.SearchProduct
	err     error
}

func (f *fakeProductSearch) hook(_ context.Context, terms, _ string) ([]scraper.SearchProduct, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, terms)
	return f.results[terms], f.err
}

func (f *fakeProductSearch) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

func enrichmentFixture(t *testing.T, showScores bool) (*Application, *models.User, *models.List, *fakeProductSearch) {
	t.Helper()
	app := newTestApplication(t)
	app.Config.ProductLookupEnabled = true
	search := &fakeProductSearch{results: map[string][]scraper.SearchProduct{"eau": offWater, "ker cadelac": offKerCadelac}}
	app.productSearchHook = search.hook
	owner, list := quickListFixture(t, app)
	if showScores {
		on := true
		updated, err := app.DB.UpdateUserNutritionScoreDisplay(context.Background(), owner.ID, &on, nil)
		if err != nil {
			t.Fatal(err)
		}
		owner = updated
	}
	return app, owner, list, search
}

// waitForItem polls item id until cond holds (the enrichment runs in the
// background), failing after two seconds.
func waitForItem(t *testing.T, app *Application, id int64, cond func(*models.Item) bool) *models.Item {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		item, err := app.DB.GetItem(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if cond(item) {
			return item
		}
		if time.Now().After(deadline) {
			t.Fatalf("item %d never reached the expected state: %+v", id, item)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForProduct polls the House catalog's product keyed key until cond
// holds: the background job writes it just after the item.
func waitForProduct(t *testing.T, app *Application, houseID int64, key string, cond func(*models.Product) bool) *models.Product {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		product, err := app.DB.GetProductByKey(context.Background(), houseID, key)
		if err == nil && cond(product) {
			return product
		}
		if time.Now().After(deadline) {
			t.Fatalf("product %q never reached the expected state: %+v (%v)", key, product, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// "Eau" typed by hand: "Boissons" straight away from the keyword
// dictionary, and the Nutri-Score from Open Food Facts in the background
// for a user who shows the scores.
func TestManualAddEauGetsDrinksAndNutriScore(t *testing.T) {
	app, owner, list, search := enrichmentFixture(t, true)

	created := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Eau"`)))
	if stringValue(created.Aisle) != catalog.AisleDrinks {
		t.Fatalf("aisle = %q, want drinks at once (keyword dictionary)", stringValue(created.Aisle))
	}
	if created.EnrichmentStatus != "pending" {
		t.Fatalf("enrichment_status = %q, want pending", created.EnrichmentStatus)
	}
	item := waitForItem(t, app, created.ID, func(it *models.Item) bool { return it.NutriScore != nil })
	wantScores(t, item, "A", -1)
	if item.NovaGroup == nil || *item.NovaGroup != 1 {
		t.Fatalf("nova_group = %v, want 1 from the search", item.NovaGroup)
	}
	if stringValue(item.Aisle) != catalog.AisleDrinks || item.ImageURL != nil {
		t.Fatalf("aisle %q, image %v: the search must not change a known aisle, nor picture one brand for a generic name", stringValue(item.Aisle), item.ImageURL)
	}
	product := waitForProduct(t, app, list.HouseID, "eau", func(p *models.Product) bool { return p.NutriScore != "" })
	if product.NutriScore != "A" || product.Aisle != catalog.AisleDrinks || product.NovaGroup == nil || *product.NovaGroup != 1 {
		t.Fatalf("catalog product = %+v", product)
	}

	// Added again elsewhere: the catalog answers, no second search.
	other, _ := app.DB.CreateList(context.Background(), "Courses 2", "groceries", list.HouseID, nil, "")
	again := decodeItemResponse(t, postItem(app, owner, itemBody(other.ID, `"title":"eau"`)))
	wantScores(t, again, "A", -1)
	if again.NovaGroup == nil || *again.NovaGroup != 1 {
		t.Fatalf("inherited nova_group = %v, want 1", again.NovaGroup)
	}
	if again.EnrichmentStatus != "" || search.count() != 1 {
		t.Fatalf("enrichment_status %q after %d searches: the catalog comes first", again.EnrichmentStatus, search.count())
	}
}

// A user who doesn't show the scores: "Eau" already has its aisle, so
// there's nothing to search for.
func TestManualAddSkipsSearchWhenNothingIsMissing(t *testing.T) {
	app, owner, list, search := enrichmentFixture(t, false)
	created := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Eau"`)))
	if created.EnrichmentStatus != "" {
		t.Fatalf("enrichment_status = %q, want none", created.EnrichmentStatus)
	}
	// Scanned (Open Food Facts already asked), or the lookup turned off:
	// no search either.
	decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"ker-cadelac","barcode":"3259421340012"`)))
	app.Config.ProductLookupEnabled = false
	decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Prince"`)))
	time.Sleep(50 * time.Millisecond)
	if n := search.count(); n != 0 {
		t.Fatalf("%d searches, want none", n)
	}
}

// A brand the dictionary doesn't know gets its aisle from Open Food Facts
// (and the catalog learns it); the household's own choice comes first
// next time.
func TestManualAddBrandGetsAisleFromSearch(t *testing.T) {
	app, owner, list, _ := enrichmentFixture(t, false)
	ctx := context.Background()

	created := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"ker-cadelac"`)))
	if created.Aisle != nil || created.EnrichmentStatus != "pending" {
		t.Fatalf("created %q / %q, want no aisle yet and a pending enrichment", stringValue(created.Aisle), created.EnrichmentStatus)
	}
	item := waitForItem(t, app, created.ID, func(it *models.Item) bool { return it.Aisle != nil })
	if stringValue(item.Aisle) != catalog.AisleBreakfast || item.NutriScore != nil {
		t.Fatalf("aisle %q, nutri %v; want breakfast and no grade (not asked for)", stringValue(item.Aisle), item.NutriScore)
	}
	waitForProduct(t, app, list.HouseID, "ker cadelac", func(p *models.Product) bool { return p.Aisle == catalog.AisleBreakfast })

	// The household files it elsewhere: that wins from then on.
	patchItemJSON(t, app, owner, item.ID, `{"aisle":"pantry"}`)
	other, _ := app.DB.CreateList(ctx, "Courses 2", "groceries", list.HouseID, nil, "")
	again := decodeItemResponse(t, postItem(app, owner, itemBody(other.ID, `"title":"Ker Cadélac"`)))
	if stringValue(again.Aisle) != catalog.AislePantry || again.EnrichmentStatus != "" {
		t.Fatalf("re-added: aisle %q, enrichment %q", stringValue(again.Aisle), again.EnrichmentStatus)
	}
}

// What the search finds only fills gaps: while it ran, someone filed the
// item under another aisle — the grade it found is saved, the aisle kept.
func TestEnrichmentNeverOverridesAChoiceMadeMeanwhile(t *testing.T) {
	app, owner, list, search := enrichmentFixture(t, true)
	search.results["cristaline"] = []scraper.SearchProduct{
		{Name: "isabelle", Brands: "Cristaline", NutriScore: "A", Categories: []string{"en:waters"}},
		{Name: "Eau de source", Brands: "Cristaline", NutriScore: "A", Categories: []string{"en:waters"}},
	}
	release := make(chan struct{})
	app.productSearchHook = func(ctx context.Context, terms, lang string) ([]scraper.SearchProduct, error) {
		<-release
		return search.hook(ctx, terms, lang)
	}
	created := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Cristaline"`)))
	if created.Aisle != nil || created.EnrichmentStatus != "pending" {
		t.Fatalf("created %q / %q", stringValue(created.Aisle), created.EnrichmentStatus)
	}
	patchItemJSON(t, app, owner, created.ID, `{"aisle":"pantry"}`)
	close(release)
	item := waitForItem(t, app, created.ID, func(it *models.Item) bool { return it.NutriScore != nil })
	if stringValue(item.Aisle) != catalog.AislePantry {
		t.Fatalf("aisle %q, want the user's pantry kept", stringValue(item.Aisle))
	}
	wantScores(t, item, "A", -1)
}

// A failed search changes nothing and isn't cached; an answer — even
// "nothing relevant" — is.
func TestProductSearchCacheAndFailures(t *testing.T) {
	app, _, _, search := enrichmentFixture(t, false)
	ctx := context.Background()

	search.err = errors.New("503 Page temporarily unavailable")
	if _, ok := app.productSearch(ctx, "prince", "fr"); ok {
		t.Fatal("a failed search reported an answer")
	}
	search.err = nil
	for range 3 {
		if found, ok := app.productSearch(ctx, "prince", "fr"); !ok || found != (productEnrichment{}) {
			t.Fatalf("prince: %+v, %v", found, ok)
		}
	}
	if n := search.count(); n != 2 {
		t.Fatalf("%d searches, want 2 (the failure, then one cached answer)", n)
	}
}

// The instance-wide bucket stays under Open Food Facts' limit.
func TestProductSearchRateLimit(t *testing.T) {
	app, _, _, search := enrichmentFixture(t, false)
	answered := 0
	for i := range productSearchRateLimit + 3 {
		if _, ok := app.productSearch(context.Background(), "produit "+strconv.Itoa(i), "fr"); ok {
			answered++
		}
	}
	if answered != productSearchRateLimit || search.count() != productSearchRateLimit {
		t.Fatalf("%d answered, %d searches; want %d", answered, search.count(), productSearchRateLimit)
	}
}
