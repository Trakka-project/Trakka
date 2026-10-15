package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"trakka/internal/catalog"
	"trakka/internal/models"
	"trakka/internal/scraper"
)

// quickListFixture creates a House (with an owner) holding one "Courses"
// (groceries) list — the list type quick shopping applies to.
func quickListFixture(t *testing.T, app *Application) (*models.User, *models.List) {
	t.Helper()
	ctx := context.Background()
	owner := mustCreateTestUser(t, app, "owner@example.com")
	house, err := app.DB.CreateHouseWithOwner(ctx, "Maison Test", owner.ID)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := app.DB.CreateList(ctx, "Courses", "groceries", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}
	return owner, list
}

// postItem sends POST /api/v1/items from user and returns the recorder.
func postItem(app *Application, user *models.User, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/items", strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.handleItemsCreate(rec, withUser(req, user))
	return rec
}

func itemBody(listID int64, fields string) string {
	body := `{"list_id":` + strconv.FormatInt(listID, 10)
	if fields != "" {
		body += "," + fields
	}
	return body + "}"
}

func listItems(t *testing.T, app *Application, listID int64) []*models.Item {
	t.Helper()
	items, err := app.DB.ListItemsByList(context.Background(), listID)
	if err != nil {
		t.Fatalf("ListItemsByList: %v", err)
	}
	return items
}

// houseRequest calls handler for a /api/v1/houses/{id}/products... route,
// as user, with the given path values.
func houseRequest(app *Application, user *models.User, handler http.HandlerFunc, method, target, body string, pathValues map[string]string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	handler(rec, withUser(req, user))
	return rec
}

func houseID(list *models.List) map[string]string {
	return map[string]string{"id": strconv.FormatInt(list.HouseID, 10)}
}

// TestQuickListMergesDuplicateIntoActiveItem: adding a product already
// active on a Courses list, under another spelling, adds to its quantity
// instead of creating a second item.
func TestQuickListMergesDuplicateIntoActiveItem(t *testing.T) {
	app := newTestApplication(t)
	owner, list := quickListFixture(t, app)

	rec := postItem(app, owner, itemBody(list.ID, `"title":"Lait demi-écrémé","quantity":2`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first add: %d %s", rec.Code, rec.Body.String())
	}
	first := decodeItemResponse(t, rec)
	if first.Merged {
		t.Fatal("a new item is not merged")
	}

	rec = postItem(app, owner, itemBody(list.ID, `"title":"  LAIT demi ecreme ","quantity":3`))
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate add: %d %s, want 200", rec.Code, rec.Body.String())
	}
	merged := decodeItemResponse(t, rec)
	if !merged.Merged || merged.ID != first.ID || merged.Quantity != 5 || merged.Done {
		t.Fatalf("merged item %+v, want id %d, quantity 5, active", merged, first.ID)
	}
	if merged.Title != "Lait demi-écrémé" {
		t.Fatalf("title changed to %q", merged.Title)
	}
	if items := listItems(t, app, list.ID); len(items) != 1 {
		t.Fatalf("list holds %d items, want 1", len(items))
	}
}

// TestQuickListReactivatesDoneItem: adding a product that was already
// checked off brings that item back to the active list with the newly
// requested quantity, rather than a second item.
func TestQuickListReactivatesDoneItem(t *testing.T) {
	app := newTestApplication(t)
	owner, list := quickListFixture(t, app)

	created := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Œufs","quantity":6`)))
	done := patchItemJSON(t, app, owner, created.ID, `{"done":true}`)
	if !done.Done {
		t.Fatal("check-off didn't stick")
	}

	rec := postItem(app, owner, itemBody(list.ID, `"title":"oeufs","quantity":12,"is_urgent":true`))
	if rec.Code != http.StatusOK {
		t.Fatalf("re-add: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeItemResponse(t, rec)
	if !got.Merged || got.ID != created.ID || got.Done || got.Quantity != 12 || !got.IsUrgent {
		t.Fatalf("reactivated %+v, want id %d active with quantity 12, urgent", got, created.ID)
	}
	if items := listItems(t, app, list.ID); len(items) != 1 || items[0].Done {
		t.Fatalf("list after re-add: %+v", items)
	}
}

// TestQuickListPrefersActiveDuplicate: with an active and a done copy of
// the same product already present (a list from before the rule), the
// active one is the one updated.
func TestQuickListPrefersActiveDuplicate(t *testing.T) {
	app := newTestApplication(t)
	owner, list := quickListFixture(t, app)
	ctx := context.Background()
	doneCopy, _ := app.DB.CreateItem(ctx, list.ID, "Pain", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if _, err := app.DB.UpdateItem(ctx, doneCopy.ID, "Pain", nil, 1, nil, false, nil, true, 0, nil, nil, nil, nil, false, nil, nil, false); err != nil {
		t.Fatalf("checking off: %v", err)
	}
	activeCopy, _ := app.DB.CreateItem(ctx, list.ID, "pain", nil, 2, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)

	got := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"PAIN"`)))
	if got.ID != activeCopy.ID || got.Quantity != 3 {
		t.Fatalf("updated %+v, want the active copy (%d) at quantity 3", got, activeCopy.ID)
	}
}

// TestOtherListTypesKeepDuplicates: the rule is specific to Courses lists.
func TestOtherListTypesKeepDuplicates(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app) // a "shopping" (Achats ponctuels) list
	for range 2 {
		if rec := postItem(app, owner, itemBody(list.ID, `"title":"Câble USB-C"`)); rec.Code != http.StatusCreated {
			t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
		}
	}
	if items := listItems(t, app, list.ID); len(items) != 2 {
		t.Fatalf("shopping list holds %d items, want 2", len(items))
	}
	// No guessed aisle there either, and nothing in the House catalog.
	items := listItems(t, app, list.ID)
	if items[0].Aisle != nil {
		t.Fatalf("guessed aisle %q on a shopping list", *items[0].Aisle)
	}
	if _, err := app.DB.GetProductByKey(context.Background(), list.HouseID, "cable usb c"); err == nil {
		t.Fatal("a shopping list item reached the catalog")
	}
}

// TestQuickListConcurrentAddsMakeOneItem: simultaneous adds of the same
// product (two members in the shop, an offline queue replaying) still end
// as one item carrying every requested unit.
func TestQuickListConcurrentAddsMakeOneItem(t *testing.T) {
	app := newTestApplication(t)
	owner, list := quickListFixture(t, app)

	const adds = 8
	var wg sync.WaitGroup
	codes := make(chan int, adds)
	for range adds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- postItem(app, owner, itemBody(list.ID, `"title":"Bananes"`)).Code
		}()
	}
	wg.Wait()
	close(codes)
	created := 0
	for code := range codes {
		if code == http.StatusCreated {
			created++
		} else if code != http.StatusOK {
			t.Fatalf("unexpected status %d", code)
		}
	}
	items := listItems(t, app, list.ID)
	if created != 1 || len(items) != 1 || items[0].Quantity != adds {
		t.Fatalf("%d created, items %+v; want one item of quantity %d", created, items, adds)
	}
}

// TestQuickListAisles: a new item gets its aisle from the request, else the
// household's usual aisle for it, else a keyword guess; an aisle chosen on
// an item is learned for next time.
func TestQuickListAisles(t *testing.T) {
	app := newTestApplication(t)
	owner, list := quickListFixture(t, app)
	ctx := context.Background()

	guessed := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Tomates cerises"`)))
	if guessed.Aisle == nil || *guessed.Aisle != catalog.AisleProduce {
		t.Fatalf("guessed aisle %v, want produce", guessed.Aisle)
	}
	explicit := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Gâteau d'anniversaire","aisle":"bakery"`)))
	if explicit.Aisle == nil || *explicit.Aisle != catalog.AisleBakery {
		t.Fatalf("explicit aisle %v", explicit.Aisle)
	}
	unknown := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Bidule"`)))
	if unknown.Aisle != nil {
		t.Fatalf("unknown product got aisle %q", *unknown.Aisle)
	}

	// Filing "Bidule" under household teaches the catalog...
	patchItemJSON(t, app, owner, unknown.ID, `{"aisle":"household"}`)
	if p, err := app.DB.GetProductByKey(ctx, list.HouseID, "bidule"); err != nil || p.Aisle != catalog.AisleHousehold {
		t.Fatalf("catalog after the edit: %+v, %v", p, err)
	}
	// ...so the next "bidule", on another Courses list of the House, lands there.
	other, _ := app.DB.CreateList(ctx, "Courses du week-end", "groceries", list.HouseID, nil, "")
	again := decodeItemResponse(t, postItem(app, owner, itemBody(other.ID, `"title":"bidule"`)))
	if again.Aisle == nil || *again.Aisle != catalog.AisleHousehold {
		t.Fatalf("learned aisle not applied: %v", again.Aisle)
	}

	// A hint (Open Food Facts' aisle for a scanned product) beats a guess
	// from the name, never the household's own choice.
	hinted := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Tomates séchées","aisle_hint":"pantry"`)))
	if hinted.Aisle == nil || *hinted.Aisle != catalog.AislePantry {
		t.Fatalf("hinted aisle %v, want pantry over the produce guess", hinted.Aisle)
	}
	learnedOverHint := decodeItemResponse(t, postItem(app, owner, itemBody(other.ID, `"title":"BIDULE","aisle_hint":"frozen"`)))
	if learnedOverHint.Aisle == nil || *learnedOverHint.Aisle != catalog.AisleHousehold {
		t.Fatalf("hint overrode the learned aisle: %v", learnedOverHint.Aisle)
	}

	// Clearing with "" and refusing unknown aisles.
	if cleared := patchItemJSON(t, app, owner, again.ID, `{"aisle":""}`); cleared.Aisle != nil {
		t.Fatalf("aisle not cleared: %q", *cleared.Aisle)
	}
	if rec := postItem(app, owner, itemBody(list.ID, `"title":"Truc","aisle":"rayon-inconnu"`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown aisle accepted: %d", rec.Code)
	}
}

// TestQuickListFeedsCatalog: additions and check-offs are counted, and a
// barcode sent with an addition is filed under the product.
func TestQuickListFeedsCatalog(t *testing.T) {
	app := newTestApplication(t)
	owner, list := quickListFixture(t, app)
	ctx := context.Background()

	item := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Nutella","barcode":"3017620422003"`)))
	patchItemJSON(t, app, owner, item.ID, `{"done":true}`)
	postItem(app, owner, itemBody(list.ID, `"title":"nutella"`))

	p, err := app.DB.GetProductByKey(ctx, list.HouseID, "nutella")
	if err != nil {
		t.Fatalf("GetProductByKey: %v", err)
	}
	if p.AddCount != 2 || p.PurchaseCount != 1 || p.LastPurchasedAt == nil {
		t.Fatalf("catalog entry %+v, want 2 additions and 1 purchase", p)
	}
	if byCode, err := app.DB.GetProductByBarcode(ctx, list.HouseID, "3017620422003"); err != nil || byCode.ID != p.ID {
		t.Fatalf("barcode lookup: %+v, %v", byCode, err)
	}
	if rec := postItem(app, owner, itemBody(list.ID, `"title":"Truc","barcode":"12ab"`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid barcode accepted: %d", rec.Code)
	}
}

func TestProductsSearchFavoritesAndForget(t *testing.T) {
	app := newTestApplication(t)
	owner, list := quickListFixture(t, app)
	for _, title := range []string{"Lait", "Lait de coco", "Pain au lait", "Salade"} {
		postItem(app, owner, itemBody(list.ID, `"title":"`+title+`"`))
	}

	rec := houseRequest(app, owner, app.handleProductsIndex, http.MethodGet, "/x?q=LAI&limit=2", "", houseID(list))
	var found []models.Product
	if err := json.Unmarshal(rec.Body.Bytes(), &found); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
	}
	if len(found) != 2 || found[0].Name != "Lait" || found[1].Name != "Lait de coco" {
		t.Fatalf("search results %+v", found)
	}

	// Star by name (the list view only knows the item's title).
	rec = houseRequest(app, owner, app.handleProductsUpsert, http.MethodPost, "/x", `{"name":"pain au LAIT","is_favorite":true}`, houseID(list))
	var starred models.Product
	if err := json.Unmarshal(rec.Body.Bytes(), &starred); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("upsert: %d %s", rec.Code, rec.Body.String())
	}
	if !starred.IsFavorite || starred.Name != "Pain au lait" {
		t.Fatalf("starred %+v, want the existing product, now a favorite", starred)
	}
	rec = houseRequest(app, owner, app.handleProductsIndex, http.MethodGet, "/x?favorites=true", "", houseID(list))
	if !strings.Contains(rec.Body.String(), `"Pain au lait"`) {
		t.Fatalf("favorites: %s", rec.Body.String())
	}

	ids := houseID(list)
	ids["productId"] = strconv.FormatInt(starred.ID, 10)
	rec = houseRequest(app, owner, app.handleProductsUpdate, http.MethodPatch, "/x", `{"is_favorite":false,"aisle":"bakery"}`, ids)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"aisle":"bakery"`) || !strings.Contains(rec.Body.String(), `"is_favorite":false`) {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if rec = houseRequest(app, owner, app.handleProductsUpdate, http.MethodPatch, "/x", `{"aisle":"nope"}`, ids); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid aisle: %d", rec.Code)
	}
	if rec = houseRequest(app, owner, app.handleProductsDelete, http.MethodDelete, "/x", "", ids); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec = houseRequest(app, owner, app.handleProductsDelete, http.MethodDelete, "/x", "", ids); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rec.Code)
	}
}

// TestProductCatalogIsForHouseMembers: a list's share recipient can add to
// a shared Courses list (duplicates still merged) but can't read the
// catalog, which spans every list of the House.
func TestProductCatalogIsForHouseMembers(t *testing.T) {
	app := newTestApplication(t)
	owner, list := quickListFixture(t, app)
	friend := mustCreateTestUser(t, app, "friend@example.com")
	if _, err := app.DB.CreateOrUpdateListShare(context.Background(), list.ID, friend.ID, "write"); err != nil {
		t.Fatalf("sharing: %v", err)
	}

	postItem(app, owner, itemBody(list.ID, `"title":"Café"`))
	if rec := postItem(app, friend, itemBody(list.ID, `"title":"café"`)); rec.Code != http.StatusOK || !decodeItemResponse(t, rec).Merged {
		t.Fatalf("share recipient's duplicate: %d %s", rec.Code, rec.Body.String())
	}

	ids := houseID(list)
	for name, handler := range map[string]http.HandlerFunc{
		"search":      app.handleProductsIndex,
		"suggestions": app.handleProductSuggestions,
	} {
		if rec := houseRequest(app, friend, handler, http.MethodGet, "/x?q=caf", "", ids); rec.Code != http.StatusForbidden {
			t.Errorf("%s for a non-member: %d, want 403", name, rec.Code)
		}
	}
	if rec := houseRequest(app, friend, app.handleProductsUpsert, http.MethodPost, "/x", `{"name":"Café","is_favorite":true}`, ids); rec.Code != http.StatusForbidden {
		t.Errorf("upsert for a non-member: %d, want 403", rec.Code)
	}
}

func TestProductSuggestions(t *testing.T) {
	app := newTestApplication(t)
	owner, list := quickListFixture(t, app)
	ctx := context.Background()
	now := time.Now()
	day := 24 * time.Hour
	purchase := func(name string, ago ...time.Duration) {
		t.Helper()
		for _, a := range ago {
			if err := app.DB.RecordProductPurchased(ctx, list.HouseID, name, catalog.Key(name), "", now.Add(-a)); err != nil {
				t.Fatalf("RecordProductPurchased: %v", err)
			}
		}
	}
	purchase("Lait", 21*day, 14*day, 7*day)      // weekly, due today
	purchase("Pain", 4*day, 3*day, 2*day, 1*day) // daily, due
	purchase("Lessive", 60*day, 30*day, 2*day)   // monthly-ish, bought recently: not due
	purchase("Café", 20*day, 10*day)             // due, but already on the list
	purchase("Chocolat", 40*day, 20*day)         // due, but a favorite: in the other part
	purchase("Sel", 100*day)                     // bought once: no rhythm yet
	postItem(app, owner, itemBody(list.ID, `"title":"café"`))
	fav := true
	if _, err := app.DB.UpsertProduct(ctx, list.HouseID, "Chocolat", "chocolat", &fav, nil); err != nil {
		t.Fatalf("UpsertProduct: %v", err)
	}

	rec := houseRequest(app, owner, app.handleProductSuggestions, http.MethodGet, "/x?list_id="+strconv.FormatInt(list.ID, 10), "", houseID(list))
	var got productSuggestions
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("suggestions: %d %s", rec.Code, rec.Body.String())
	}
	if len(got.Favorites) != 1 || got.Favorites[0].Name != "Chocolat" {
		t.Fatalf("favorites %+v", got.Favorites)
	}
	var names, reasons []string
	for _, s := range got.Recurring {
		names = append(names, s.Name)
		reasons = append(reasons, s.Reason)
	}
	if strings.Join(names, ",") != "Pain,Lait,Lessive" || strings.Join(reasons, ",") != "due,due,frequent" {
		t.Fatalf("recurring %v %v, want Pain,Lait (due) then Lessive", names, reasons)
	}

	// list_id must be a list of this House.
	stranger := mustCreateTestUser(t, app, "stranger@example.com")
	otherHouse, _ := app.DB.CreateHouseWithOwner(ctx, "Autre", stranger.ID)
	otherList, _ := app.DB.CreateList(ctx, "Courses", "groceries", otherHouse.ID, nil, "")
	if rec := houseRequest(app, owner, app.handleProductSuggestions, http.MethodGet, "/x?list_id="+strconv.FormatInt(otherList.ID, 10), "", houseID(list)); rec.Code != http.StatusBadRequest {
		t.Fatalf("foreign list_id: %d", rec.Code)
	}
}

func TestProductBarcodeLookup(t *testing.T) {
	app := newTestApplication(t)
	app.Config.ProductLookupEnabled = true
	owner, list := quickListFixture(t, app)
	var asked []string
	app.barcodeLookupHook = func(_ context.Context, code, lang string) (*scraper.BarcodeProduct, error) {
		asked = append(asked, code+"/"+lang)
		switch code {
		case "3017620422003":
			return &scraper.BarcodeProduct{Barcode: code, Name: "Nutella", Categories: []string{"en:sweet-spreads"}}, nil
		case "4006381333931":
			return nil, scraper.ErrProductNotFound
		default:
			return nil, errors.New("connection refused")
		}
	}
	lookup := func(code string) *httptest.ResponseRecorder {
		ids := houseID(list)
		ids["code"] = code
		return houseRequest(app, owner, app.handleProductBarcodeLookup, http.MethodGet, "/x", "", ids)
	}

	// Unknown to the House: Open Food Facts, aisle from its categories.
	rec := lookup("3017620422003")
	var got models.BarcodeLookup
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("lookup: %d %s", rec.Code, rec.Body.String())
	}
	if got.Source != "openfoodfacts" || got.Name != "Nutella" || got.Aisle != catalog.AisleBreakfast || got.Product != nil {
		t.Fatalf("got %+v", got)
	}

	// Once added with its code, the House knows it — under the name chosen.
	postItem(app, owner, itemBody(list.ID, `"title":"Pâte à tartiner","barcode":"3017620422003","aisle":"breakfast"`))
	asked = nil
	rec = lookup("3017620422003")
	got = models.BarcodeLookup{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Source != "house" || got.Name != "Pâte à tartiner" || got.Product == nil || len(asked) != 0 {
		t.Fatalf("house lookup %+v (Open Food Facts asked: %v)", got, asked)
	}

	if rec = lookup("4006381333931"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Fatalf("unknown code: %d %s", rec.Code, rec.Body.String())
	}
	if rec = lookup("96385074"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"lookup_failed"`) {
		t.Fatalf("failed lookup: %d %s", rec.Code, rec.Body.String())
	}
	if rec = lookup("12ab"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid code: %d", rec.Code)
	}

	// Lookup disabled: only the House catalog answers.
	app.Config.ProductLookupEnabled = false
	asked = nil
	if rec = lookup("5000112637922"); rec.Code != http.StatusNotFound || len(asked) != 0 {
		t.Fatalf("disabled lookup: %d, asked %v", rec.Code, asked)
	}
}
