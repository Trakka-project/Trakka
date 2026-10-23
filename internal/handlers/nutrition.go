package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"trakka/internal/catalog"
	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// Nutrition scores of grocery products: an item's Nutri-Score grade ("A" to
// "E"), its NOVA group (1 to 4, how processed it is) and a legacy Yuka
// score (0-100), all optional (models.Item.NutriScore/NovaGroup/YukaScore,
// migrations 39 and 40).
//
//   - Where they come from: the Nutri-Score and the NOVA group from Open
//     Food Facts — when a barcode is scanned (scraper.LookupBarcode,
//     answered by handleProductBarcodeLookup for the client to send with the
//     item it adds), by the background text search for a product typed by
//     hand (product_enrichment.go), or on demand from the edit form
//     (handleProductNutritionLookup) —, or whatever a client sends on
//     POST/PUT/PATCH /api/v1/items. Nothing fills the Yuka score in by
//     itself: Yuka has no public API, and its score is proprietary, so
//     Trakka never computes a look-alike. The PWA replaced it with the NOVA
//     group; the API keeps it, so nothing a client stored is lost.
//   - On a "Courses" list they're also learned by the House catalog, like
//     the aisle: an item added with scores teaches the product's, an item
//     added by name without any inherits them (resolveItemNutrition), and
//     scores set on an item through PUT/PATCH become the product's.
//   - Whether they are shown is each user's own choice, per kind of screen
//     (models.User.NutritionScoresMobile/NutritionScoresWeb), applied by the
//     frontend (static/js/nutrition.js): the API always reports them.

// nutritionScores is an item's nutrition scores; "" / nil is an unknown one.
type nutritionScores struct {
	NutriScore string
	NovaGroup  *int
	YukaScore  *int
}

func itemNutrition(item *models.Item) nutritionScores {
	return nutritionScores{NutriScore: stringValue(item.NutriScore), NovaGroup: item.NovaGroup, YukaScore: item.YukaScore}
}

// complete reports whether every score is known.
func (s nutritionScores) complete() bool {
	return s.NutriScore != "" && s.NovaGroup != nil && s.YukaScore != nil
}

// cleanNutritionScores validates the nutri_score/nova_group/yuka_score of a
// POST /api/v1/items ("" / absent for none).
func cleanNutritionScores(nutriScore string, novaGroup, yukaScore *int) (nutritionScores, error) {
	grade, err := validate.NutriScore(nutriScore)
	if err != nil {
		return nutritionScores{}, err
	}
	if novaGroup != nil {
		if err := validate.NovaGroup(*novaGroup); err != nil {
			return nutritionScores{}, err
		}
	}
	if yukaScore != nil {
		if err := validate.YukaScore(*yukaScore); err != nil {
			return nutritionScores{}, err
		}
	}
	return nutritionScores{NutriScore: grade, NovaGroup: novaGroup, YukaScore: yukaScore}, nil
}

// nutritionInput is the nutrition part of a PUT or PATCH /api/v1/items:
// each field nil when absent.
type nutritionInput struct {
	NutriScore *string         `json:"nutri_score"`
	NovaGroup  json.RawMessage `json:"nova_group"`
	YukaScore  json.RawMessage `json:"yuka_score"`
}

// applyNutritionInput applies the nutri_score/nova_group/yuka_score fields
// of a PUT or PATCH /api/v1/items to current and returns the result. All
// follow PATCH's convention on PUT too — absent leaves the score as it is,
// nutri_score "" or nova_group/yuka_score null clears it — because the
// scores usually come from a scan: an edit form or an integration that
// doesn't show them must not wipe them out (the same reasoning as
// assignee_ids on PUT).
func applyNutritionInput(current nutritionScores, in nutritionInput) (nutritionScores, error) {
	if in.NutriScore != nil {
		grade, err := validate.NutriScore(*in.NutriScore)
		if err != nil {
			return current, err
		}
		current.NutriScore = grade
	}
	var err error
	if current.NovaGroup, err = applyScoreInput(current.NovaGroup, in.NovaGroup, "nova_group", validate.NovaGroup); err != nil {
		return current, err
	}
	if current.YukaScore, err = applyScoreInput(current.YukaScore, in.YukaScore, "yuka_score", validate.YukaScore); err != nil {
		return current, err
	}
	return current, nil
}

// applyScoreInput reads one integer score field of applyNutritionInput:
// absent keeps current, null clears it, a number is checked by valid.
func applyScoreInput(current *int, raw json.RawMessage, field string, valid func(int) error) (*int, error) {
	if raw == nil {
		return current, nil
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var score int
	if err := json.Unmarshal(raw, &score); err != nil {
		return current, errors.New(field + " must be a number")
	}
	if err := valid(score); err != nil {
		return current, err
	}
	return &score, nil
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameNutrition(a, b nutritionScores) bool {
	return a.NutriScore == b.NutriScore && sameIntPtr(a.NovaGroup, b.NovaGroup) && sameIntPtr(a.YukaScore, b.YukaScore)
}

// novaGroupPtr turns a scraper's NOVA group (0 for unknown) into a score.
func novaGroupPtr(group int) *int {
	if group == 0 {
		return nil
	}
	return &group
}

// saveItemNutrition writes scores onto item when they differ from what it
// has, returning the item as stored.
func (app *Application) saveItemNutrition(ctx context.Context, item *models.Item, scores nutritionScores) (*models.Item, error) {
	if sameNutrition(itemNutrition(item), scores) {
		return item, nil
	}
	return app.DB.SetItemNutrition(ctx, item.ID, nullableString(scores.NutriScore), scores.YukaScore, scores.NovaGroup)
}

// resolveItemNutrition picks the scores of an item about to be added to a
// Courses list: each one the request gave, else the House catalog's for
// that product (the household scanned it before), else none.
func (app *Application) resolveItemNutrition(ctx context.Context, list *models.List, title string, explicit nutritionScores) nutritionScores {
	if explicit.complete() {
		return explicit
	}
	product, err := app.DB.GetProductByKey(ctx, list.HouseID, catalog.Key(title))
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			app.Logger.Warn("reading product catalog", "house_id", list.HouseID, "error", err)
		}
		return explicit
	}
	if explicit.NutriScore == "" {
		explicit.NutriScore = product.NutriScore
	}
	if explicit.NovaGroup == nil {
		explicit.NovaGroup = product.NovaGroup
	}
	if explicit.YukaScore == nil {
		explicit.YukaScore = product.YukaScore
	}
	return explicit
}

// fillItemNutrition gives an item just added to (or brought back on) a
// Courses list the scores resolved for it: a known one replaces the item's,
// an unknown one leaves the item's as it was.
func (app *Application) fillItemNutrition(ctx context.Context, item *models.Item, resolved nutritionScores) (*models.Item, error) {
	scores := itemNutrition(item)
	if resolved.NutriScore != "" {
		scores.NutriScore = resolved.NutriScore
	}
	if resolved.NovaGroup != nil {
		scores.NovaGroup = resolved.NovaGroup
	}
	if resolved.YukaScore != nil {
		scores.YukaScore = resolved.YukaScore
	}
	return app.saveItemNutrition(ctx, item, scores)
}

// learnProductNutrition makes the scores someone just set on a Courses item
// the House catalog product's. Best effort, like learnProductAisle.
func (app *Application) learnProductNutrition(ctx context.Context, list *models.List, item *models.Item) {
	key := catalog.Key(item.Title)
	if key == "" {
		return
	}
	scores := itemNutrition(item)
	if err := app.DB.SetProductNutrition(ctx, list.HouseID, item.Title, key, scores.NutriScore, scores.YukaScore, scores.NovaGroup); err != nil {
		app.Logger.Warn("learning product nutrition scores", "house_id", list.HouseID, "item_id", item.ID, "error", err)
	}
}

// wantsNutritionScores reports whether user shows nutrition scores anywhere.
func wantsNutritionScores(user *models.User) bool {
	return user.NutritionScoresMobile || user.NutritionScoresWeb
}

// enrichProductNutrition fills in the Nutri-Score and/or NOVA group of a
// product the House catalog knew by its barcode without them (filed before
// the scores existed, or under a name typed by hand) from Open Food Facts,
// and keeps them in the catalog. Best effort and quiet: only for a user who
// shows the scores, only with PRODUCT_LOOKUP_ENABLED, within the same
// per-account limit as other lookups; any failure leaves the product as it
// was.
func (app *Application) enrichProductNutrition(ctx context.Context, user *models.User, product *models.Product, code string) {
	if (product.NutriScore != "" && product.NovaGroup != nil) || !app.Config.ProductLookupEnabled || !wantsNutritionScores(user) {
		return
	}
	if !app.barcodeLookupLimiter().allow(strconv.FormatInt(user.ID, 10), barcodeLookupRateLimit) {
		return
	}
	app.resolveUserLanguage(user)
	lookupCtx, cancel := context.WithTimeout(ctx, barcodeLookupTimeout)
	defer cancel()
	found, err := app.lookupBarcode(lookupCtx, code, user.Language)
	if err != nil {
		return
	}
	grade, nova := product.NutriScore, product.NovaGroup
	if grade == "" {
		grade = found.NutriScore
	}
	if nova == nil {
		nova = novaGroupPtr(found.NovaGroup)
	}
	if grade == product.NutriScore && sameIntPtr(nova, product.NovaGroup) {
		return
	}
	key := catalog.Key(product.Name)
	if err := app.DB.SetProductNutrition(ctx, product.HouseID, product.Name, key, grade, product.YukaScore, nova); err != nil {
		app.Logger.Warn("saving product nutrition scores", "house_id", product.HouseID, "product_id", product.ID, "error", err)
		return
	}
	product.NutriScore, product.NovaGroup = grade, nova
}

// handleProductNutritionLookup answers
// GET /api/v1/houses/{id}/products/nutrition?name=…: the scores known for a
// product name, for the "Rechercher sur Open Food Facts" button of the edit
// form's nutrition panel (static/js/nutrition.js) — an item added before the
// household knew its scores, or whose background search found nothing. The
// House catalog first (free), then, for a Nutri-Score or NOVA group it
// doesn't know, Open Food Facts' text search, through the same cache,
// instance-wide bucket and trust rules as the background enrichment
// (productSearch, pickSearchMatch), within the per-account lookup limit.
// Nothing is stored: the form shows what was found, and saving the item
// teaches the catalog as any edit does. Like the barcode lookup, never a
// 502/503/504 (the service worker would take it for "Trakka unreachable"):
// a search that couldn't be made is a 200 with search "unavailable".
func (app *Application) handleProductNutritionLookup(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok || !app.authorizeHouseAccess(w, r, houseID) {
		return
	}
	name := validate.Text(r.URL.Query().Get("name"))
	key := catalog.Key(name)
	if key == "" || !validate.MaxLen(name, validate.MaxTitleLen) {
		writeError(w, http.StatusBadRequest, "name is required, up to 500 characters")
		return
	}
	ctx := r.Context()
	answer := models.NutritionLookup{Name: name}
	product, err := app.DB.GetProductByKey(ctx, houseID, key)
	switch {
	case err == nil:
		answer.NutriScore, answer.NovaGroup, answer.YukaScore = product.NutriScore, product.NovaGroup, product.YukaScore
		if answer.NutriScore != "" || answer.NovaGroup != nil {
			answer.Source = "house"
		}
	case !errors.Is(err, db.ErrNotFound):
		app.serverError(w, r, err)
		return
	}
	switch {
	case answer.NutriScore != "" && answer.NovaGroup != nil:
		answer.Search = "not_needed"
	case !app.Config.ProductLookupEnabled:
		answer.Search = "disabled"
	default:
		user := userFromContext(r)
		if !app.barcodeLookupLimiter().allow(strconv.FormatInt(user.ID, 10), barcodeLookupRateLimit) {
			w.Header().Set("Retry-After", strconv.Itoa(int(barcodeLookupRateWin.Seconds())))
			writeError(w, http.StatusTooManyRequests, "too many product lookups, try again in a minute")
			return
		}
		app.resolveUserLanguage(user)
		searchCtx, cancel := context.WithTimeout(ctx, productSearchTimeout)
		defer cancel()
		found, ok := app.productSearch(searchCtx, key, user.Language)
		if !ok {
			answer.Search = "unavailable"
			break
		}
		answer.Search = "done"
		searched := false
		if answer.NutriScore == "" && found.NutriScore != "" {
			answer.NutriScore, searched = found.NutriScore, true
		}
		if answer.NovaGroup == nil && found.NovaGroup != 0 {
			answer.NovaGroup, searched = novaGroupPtr(found.NovaGroup), true
		}
		if searched {
			answer.Source, answer.MatchedName = "openfoodfacts", found.MatchedName
		}
	}
	writeJSON(w, http.StatusOK, answer)
}
