package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"trakka/internal/models"
	"trakka/internal/scraper"
)

// putItemJSON sends body as PUT /api/v1/items/{id} from user and returns
// the recorder.
func putItemJSON(app *Application, user *models.User, itemID int64, body string) *httptest.ResponseRecorder {
	id := strconv.FormatInt(itemID, 10)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/items/"+id, strings.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	app.handleItemsUpdate(rec, withUser(req, user))
	return rec
}

// patchItemRec is patchItemJSON without decoding, for refused requests.
func patchItemRec(app *Application, user *models.User, itemID int64, body string) *httptest.ResponseRecorder {
	id := strconv.FormatInt(itemID, 10)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/items/"+id, strings.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	app.handleItemsPatch(rec, withUser(req, user))
	return rec
}

// wantScores fails the test unless item carries exactly nutri/yuka ("" / -1
// for none).
func wantScores(t *testing.T, item *models.Item, nutri string, yuka int) {
	t.Helper()
	gotNutri, gotYuka := stringValue(item.NutriScore), -1
	if item.YukaScore != nil {
		gotYuka = *item.YukaScore
	}
	if gotNutri != nutri || gotYuka != yuka {
		t.Fatalf("item %q scores = %q / %d, want %q / %d", item.Title, gotNutri, gotYuka, nutri, yuka)
	}
}

// The two display preferences: off by default, each settable on its own.
func TestMeNutritionScoreDisplay(t *testing.T) {
	app := newTestApplication(t)
	user := mustCreateTestUser(t, app, "nutrition@example.com")
	if user.NutritionScoresMobile || user.NutritionScoresWeb {
		t.Fatalf("defaults: %+v", user)
	}
	patchMe := func(body string) models.User {
		t.Helper()
		rec := httptest.NewRecorder()
		app.handleMeUpdate(rec, withUser(httptest.NewRequest(http.MethodPatch, "/api/v1/me", strings.NewReader(body)), user))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
		var got models.User
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := patchMe(`{"nutrition_scores_mobile":true}`); !got.NutritionScoresMobile || got.NutritionScoresWeb {
		t.Fatalf("mobile only: %+v", got)
	}
	if got := patchMe(`{"nutrition_scores_web":true}`); !got.NutritionScoresMobile || !got.NutritionScoresWeb {
		t.Fatalf("web added, mobile kept: %+v", got)
	}
	// An unrelated preference leaves both alone.
	if got := patchMe(`{"keep_last_page":false}`); !got.NutritionScoresMobile || !got.NutritionScoresWeb {
		t.Fatalf("after an unrelated PATCH: %+v", got)
	}
	if got := patchMe(`{"nutrition_scores_mobile":false,"nutrition_scores_web":false}`); got.NutritionScoresMobile || got.NutritionScoresWeb {
		t.Fatalf("both off: %+v", got)
	}
	if !strings.Contains(getMeBody(t, app, user), `"nutrition_scores_mobile":false`) {
		t.Fatal("GET /me doesn't report nutrition_scores_mobile")
	}
}

// getMeBody answers GET /api/v1/me for user, as stored.
func getMeBody(t *testing.T, app *Application, user *models.User) string {
	t.Helper()
	fresh, err := app.DB.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.handleMe(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/v1/me", nil), fresh))
	return rec.Body.String()
}

// POST /api/v1/items takes both scores, normalized and validated, on any
// list type.
func TestItemCreateNutritionScores(t *testing.T) {
	app := newTestApplication(t)
	owner, list := quickListFixture(t, app)

	wantScores(t, decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Yaourt","nutri_score":" b ","yuka_score":88`))), "B", 88)
	wantScores(t, decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Eau"`))), "", -1)

	todo, err := app.DB.CreateList(context.Background(), "Tâches", "todo", list.HouseID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	wantScores(t, decodeItemResponse(t, postItem(app, owner, itemBody(todo.ID, `"title":"Goûter","nutri_score":"a"`))), "A", -1)

	for _, fields := range []string{`"nutri_score":"F"`, `"nutri_score":"unknown"`, `"yuka_score":101`, `"yuka_score":-1`} {
		if rec := postItem(app, owner, itemBody(list.ID, `"title":"Refusé",`+fields)); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s, want 400", fields, rec.Code, rec.Body.String())
		}
	}
}

// On a Courses list the House catalog learns the scores an item is added
// with, an item added again by name inherits them, and a merge takes the
// new ones.
func TestQuickListNutritionScoresAreLearned(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()
	owner, list := quickListFixture(t, app)

	postItem(app, owner, itemBody(list.ID, `"title":"Nutella","barcode":"3017620422003","nutri_score":"E","yuka_score":12`))
	product, err := app.DB.GetProductByKey(ctx, list.HouseID, "nutella")
	if err != nil || product.NutriScore != "E" || product.YukaScore == nil || *product.YukaScore != 12 {
		t.Fatalf("catalog product = %+v, %v", product, err)
	}

	// Another Courses list of the House: added by name, it inherits them.
	other, err := app.DB.CreateList(ctx, "Courses 2", "groceries", list.HouseID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	wantScores(t, decodeItemResponse(t, postItem(app, owner, itemBody(other.ID, `"title":"nutella"`))), "E", 12)
	// An explicit score wins over the catalog's, the other one inherited.
	third, _ := app.DB.CreateList(ctx, "Courses 3", "groceries", list.HouseID, nil, "")
	wantScores(t, decodeItemResponse(t, postItem(app, owner, itemBody(third.ID, `"title":"Nutella","nutri_score":"D"`))), "D", 12)

	// Added again to the first list: merged, with the new grade.
	rec := postItem(app, owner, itemBody(list.ID, `"title":"Nutella","nutri_score":"C"`))
	merged := decodeItemResponse(t, rec)
	if rec.Code != http.StatusOK || !merged.Merged {
		t.Fatalf("expected a merge, got %d %+v", rec.Code, merged)
	}
	wantScores(t, merged, "C", 12)
	if items := listItems(t, app, list.ID); len(items) != 1 {
		t.Fatalf("a merge must not add an item, got %d", len(items))
	}
}

// PUT and PATCH leave the scores alone when absent, clear them with "" /
// null, and teach the House catalog what's set on a Courses item.
func TestItemUpdateNutritionScores(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()
	owner, list := quickListFixture(t, app)
	item := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Céréales"`)))

	wantScores(t, patchItemJSON(t, app, owner, item.ID, `{"nutri_score":"c"}`), "C", -1)
	wantScores(t, patchItemJSON(t, app, owner, item.ID, `{"yuka_score":42}`), "C", 42)
	wantScores(t, patchItemJSON(t, app, owner, item.ID, `{"done":true}`), "C", 42)
	product, err := app.DB.GetProductByKey(ctx, list.HouseID, "cereales")
	if err != nil || product.NutriScore != "C" || product.YukaScore == nil || *product.YukaScore != 42 {
		t.Fatalf("catalog product = %+v, %v", product, err)
	}

	// A full replace that doesn't mention them keeps them...
	wantScores(t, decodeItemResponse(t, putItemJSON(app, owner, item.ID, `{"title":"Céréales","quantity":1}`)), "C", 42)
	// ...and one that does sets them.
	wantScores(t, decodeItemResponse(t, putItemJSON(app, owner, item.ID, `{"title":"Céréales","quantity":1,"nutri_score":"A","yuka_score":null}`)), "A", -1)

	wantScores(t, patchItemJSON(t, app, owner, item.ID, `{"nutri_score":"","yuka_score":null}`), "", -1)
	if product, _ = app.DB.GetProductByKey(ctx, list.HouseID, "cereales"); product.NutriScore != "" || product.YukaScore != nil {
		t.Fatalf("clearing an item's scores clears the product's: %+v", product)
	}

	for _, body := range []string{`{"nutri_score":"G"}`, `{"yuka_score":"high"}`, `{"yuka_score":150}`} {
		if rec := patchItemRec(app, owner, item.ID, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("PATCH %s: %d, want 400", body, rec.Code)
		}
		if rec := putItemJSON(app, owner, item.ID, `{"title":"Céréales",`+strings.TrimPrefix(body, "{")); rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s: %d, want 400", body, rec.Code)
		}
	}
}

// The barcode lookup answers Open Food Facts' Nutri-Score, and fills in the
// grade of a product the House knew without one — for a user who shows the
// scores only.
func TestBarcodeLookupNutriScore(t *testing.T) {
	app := newTestApplication(t)
	app.Config.ProductLookupEnabled = true
	ctx := context.Background()
	owner, list := quickListFixture(t, app)
	var asked int
	app.barcodeLookupHook = func(_ context.Context, code, _ string) (*scraper.BarcodeProduct, error) {
		asked++
		return &scraper.BarcodeProduct{Barcode: code, Name: "Nutella", NutriScore: "E", NovaGroup: 3}, nil
	}
	lookup := func(user *models.User, code string) models.BarcodeLookup {
		t.Helper()
		ids := houseID(list)
		ids["code"] = code
		rec := houseRequest(app, user, app.handleProductBarcodeLookup, http.MethodGet, "/x", "", ids)
		var got models.BarcodeLookup
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("lookup %s: %d %s", code, rec.Code, rec.Body.String())
		}
		return got
	}

	if got := lookup(owner, "3017620422003"); got.Source != "openfoodfacts" || got.NutriScore != "E" {
		t.Fatalf("Open Food Facts lookup: %+v", got)
	}

	// Filed without a grade (as before the scores existed).
	postItem(app, owner, itemBody(list.ID, `"title":"Pâte à tartiner","barcode":"5000112637922"`))
	asked = 0
	if got := lookup(owner, "5000112637922"); got.Source != "house" || got.NutriScore != "" || asked != 0 {
		t.Fatalf("scores not shown: no Open Food Facts call expected, got %+v (asked %d)", got, asked)
	}

	on := true
	if _, err := app.DB.UpdateUserNutritionScoreDisplay(ctx, owner.ID, &on, nil); err != nil {
		t.Fatal(err)
	}
	owner.NutritionScoresMobile = true
	if got := lookup(owner, "5000112637922"); got.Source != "house" || got.NutriScore != "E" || got.Product.NutriScore != "E" || asked != 1 {
		t.Fatalf("enriched house lookup: %+v (asked %d)", got, asked)
	}
	// Kept in the catalog: no second call.
	if got := lookup(owner, "5000112637922"); got.NutriScore != "E" || asked != 1 {
		t.Fatalf("second house lookup: %+v (asked %d)", got, asked)
	}
}

// TestProductNutritionLookup: the edit form's on-demand lookup reads the
// House catalog first, asks Open Food Facts only for a grade it doesn't
// know, and stores nothing.
func TestProductNutritionLookup(t *testing.T) {
	app, owner, list, search := enrichmentFixture(t, true)
	ctx := context.Background()
	lookup := func(user *models.User, name string) (int, models.NutritionLookup) {
		t.Helper()
		target := "/x?name=" + url.QueryEscape(name)
		rec := houseRequest(app, user, app.handleProductNutritionLookup, http.MethodGet, target, "", houseID(list))
		var got models.NutritionLookup
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("lookup %q: %v (%s)", name, err, rec.Body.String())
			}
		}
		return rec.Code, got
	}

	// Unknown to the catalog: Open Food Facts' search, the matched name reported.
	code, got := lookup(owner, "Eau")
	if code != http.StatusOK || got.NutriScore != "A" || got.NovaGroup == nil || *got.NovaGroup != 1 || got.Source != "openfoodfacts" ||
		got.MatchedName != "Eau minérale naturelle" || got.Search != "done" || search.count() != 1 {
		t.Fatalf("Open Food Facts lookup: %d %+v (asked %d)", code, got, search.count())
	}
	if _, err := app.DB.GetProductByKey(ctx, list.HouseID, "eau"); err == nil {
		t.Fatal("a lookup must not file the product")
	}

	// Nothing relevant: a 200 without a grade.
	if code, got := lookup(owner, "Prince"); code != http.StatusOK || got.NutriScore != "" || got.Source != "" || got.Search != "done" {
		t.Fatalf("nothing found: %d %+v", code, got)
	}

	// Known to the catalog with the grade and the NOVA group: no search.
	yuka, nova := 64, 2
	if err := app.DB.SetProductNutrition(ctx, list.HouseID, "Yaourt nature", "yaourt nature", "B", &yuka, &nova); err != nil {
		t.Fatal(err)
	}
	asked := search.count()
	if code, got := lookup(owner, "  yaourt NATURE "); code != http.StatusOK || got.NutriScore != "B" || got.NovaGroup == nil || *got.NovaGroup != 2 ||
		got.YukaScore == nil || *got.YukaScore != 64 || got.Source != "house" || got.Search != "not_needed" || search.count() != asked {
		t.Fatalf("catalog lookup: %d %+v (asked %d)", code, got, search.count()-asked)
	}

	// The catalog knows only the grade: kept, the NOVA group searched.
	if err := app.DB.SetProductNutrition(ctx, list.HouseID, "Eau", "eau", "C", nil, nil); err != nil {
		t.Fatal(err)
	}
	if code, got := lookup(owner, "Eau"); code != http.StatusOK || got.NutriScore != "C" || got.NovaGroup == nil || *got.NovaGroup != 1 || got.Source != "openfoodfacts" {
		t.Fatalf("catalog grade + Open Food Facts NOVA group: %d %+v", code, got)
	}

	// Search failing, then disabled: still a 200, the reason said.
	search.err = errors.New("503 from Open Food Facts")
	if code, got := lookup(owner, "Café moulu"); code != http.StatusOK || got.Search != "unavailable" {
		t.Fatalf("failing search: %d %+v", code, got)
	}
	app.Config.ProductLookupEnabled = false
	if code, got := lookup(owner, "Thé vert"); code != http.StatusOK || got.Search != "disabled" {
		t.Fatalf("lookups disabled: %d %+v", code, got)
	}

	// A name is required; an outsider of the House is refused.
	if code, _ := lookup(owner, "   "); code != http.StatusBadRequest {
		t.Fatalf("blank name: %d", code)
	}
	outsider := mustCreateTestUser(t, app, "outsider@example.com")
	if code, _ := lookup(outsider, "Eau"); code == http.StatusOK {
		t.Fatal("an outsider must not read the House catalog")
	}
}

// wantNova fails the test unless item's NOVA group is group (0 for none).
func wantNova(t *testing.T, item *models.Item, group int) {
	t.Helper()
	got := 0
	if item.NovaGroup != nil {
		got = *item.NovaGroup
	}
	if got != group {
		t.Fatalf("item %q nova_group = %d, want %d", item.Title, got, group)
	}
}

// The NOVA group goes through POST/PUT/PATCH like the other scores —
// validated, absent = unchanged on PUT, null clears — and the House
// catalog learns it.
func TestItemNovaGroup(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()
	owner, list := quickListFixture(t, app)

	item := decodeItemResponse(t, postItem(app, owner, itemBody(list.ID, `"title":"Chips","nova_group":4`)))
	wantNova(t, item, 4)
	other, _ := app.DB.CreateList(ctx, "Courses 2", "groceries", list.HouseID, nil, "")
	wantNova(t, decodeItemResponse(t, postItem(app, owner, itemBody(other.ID, `"title":"chips"`))), 4)

	wantNova(t, patchItemJSON(t, app, owner, item.ID, `{"nova_group":3}`), 3)
	wantNova(t, patchItemJSON(t, app, owner, item.ID, `{"nutri_score":"D"}`), 3)
	if product, err := app.DB.GetProductByKey(ctx, list.HouseID, "chips"); err != nil || product.NovaGroup == nil || *product.NovaGroup != 3 {
		t.Fatalf("catalog product = %+v, %v", product, err)
	}
	wantNova(t, decodeItemResponse(t, putItemJSON(app, owner, item.ID, `{"title":"Chips","quantity":1}`)), 3)
	wantNova(t, patchItemJSON(t, app, owner, item.ID, `{"nova_group":null}`), 0)

	for _, fields := range []string{`"nova_group":0`, `"nova_group":5`, `"nova_group":"4"`} {
		if rec := postItem(app, owner, itemBody(list.ID, `"title":"Refusé",`+fields)); rec.Code != http.StatusBadRequest {
			t.Fatalf("POST %s: %d, want 400", fields, rec.Code)
		}
		if rec := patchItemRec(app, owner, item.ID, "{"+fields+"}"); rec.Code != http.StatusBadRequest {
			t.Fatalf("PATCH %s: %d, want 400", fields, rec.Code)
		}
	}
}

// The barcode lookup answers Open Food Facts' NOVA group, and fills in
// that of a product the House knew without one.
func TestBarcodeLookupNovaGroup(t *testing.T) {
	app, owner, list, _ := enrichmentFixture(t, true)
	asked := 0
	app.barcodeLookupHook = func(_ context.Context, code, _ string) (*scraper.BarcodeProduct, error) {
		asked++
		return &scraper.BarcodeProduct{Barcode: code, Name: "Nutella", NutriScore: "E", NovaGroup: 4}, nil
	}
	lookup := func(code string) models.BarcodeLookup {
		t.Helper()
		ids := houseID(list)
		ids["code"] = code
		rec := houseRequest(app, owner, app.handleProductBarcodeLookup, http.MethodGet, "/x", "", ids)
		var got models.BarcodeLookup
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("lookup %s: %d %s", code, rec.Code, rec.Body.String())
		}
		return got
	}
	if got := lookup("3017620422003"); got.NovaGroup == nil || *got.NovaGroup != 4 || got.Source != "openfoodfacts" {
		t.Fatalf("Open Food Facts lookup: %+v", got)
	}
	// Filed with a grade but no NOVA group: completed, and kept.
	postItem(app, owner, itemBody(list.ID, `"title":"Pâte à tartiner","barcode":"5000112637922","nutri_score":"D"`))
	asked = 0
	got := lookup("5000112637922")
	if got.Source != "house" || got.NutriScore != "D" || got.NovaGroup == nil || *got.NovaGroup != 4 || asked != 1 {
		t.Fatalf("enriched house lookup: %+v (asked %d)", got, asked)
	}
	if got := lookup("5000112637922"); got.NovaGroup == nil || *got.NovaGroup != 4 || asked != 1 {
		t.Fatalf("second house lookup: %+v (asked %d)", got, asked)
	}
}
