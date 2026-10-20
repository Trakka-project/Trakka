package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"trakka/internal/catalog"
	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// Nutrition scores of grocery products: an item's Nutri-Score grade ("A" to
// "E") and Yuka score (0-100), both optional (models.Item.NutriScore/
// YukaScore, migration 39).
//
//   - Where they come from: the Nutri-Score from Open Food Facts when a
//     barcode is scanned (scraper.LookupBarcode, answered by
//     handleProductBarcodeLookup for the client to send with the item it
//     adds), or whatever a client sends on POST/PUT/PATCH /api/v1/items.
//     Nothing fills the Yuka score in by itself: Yuka has no public API,
//     and its score is proprietary, so Trakka never computes a look-alike.
//   - On a "Courses" list they're also learned by the House catalog, like
//     the aisle: an item added with scores teaches the product's, an item
//     added by name without any inherits them (resolveItemNutrition), and
//     scores set on an item through PUT/PATCH become the product's.
//   - Whether they are shown is each user's own choice, per kind of screen
//     (models.User.NutritionScoresMobile/NutritionScoresWeb), applied by the
//     frontend (static/js/nutrition.js): the API always reports them.

// nutritionScores is a pair of nutrition scores; "" / nil is an unknown one.
type nutritionScores struct {
	NutriScore string
	YukaScore  *int
}

func itemNutrition(item *models.Item) nutritionScores {
	return nutritionScores{NutriScore: stringValue(item.NutriScore), YukaScore: item.YukaScore}
}

// cleanNutritionScores validates the nutri_score/yuka_score of a
// POST /api/v1/items ("" / absent for none).
func cleanNutritionScores(nutriScore string, yukaScore *int) (nutritionScores, error) {
	grade, err := validate.NutriScore(nutriScore)
	if err != nil {
		return nutritionScores{}, err
	}
	if yukaScore != nil {
		if err := validate.YukaScore(*yukaScore); err != nil {
			return nutritionScores{}, err
		}
	}
	return nutritionScores{NutriScore: grade, YukaScore: yukaScore}, nil
}

// applyNutritionInput applies the nutri_score/yuka_score fields of a PUT or
// PATCH /api/v1/items to current and returns the result. Both follow PATCH's
// convention on PUT too — absent leaves the score as it is, nutri_score ""
// or yuka_score null clears it — because the scores usually come from a
// scan: an edit form or an integration that doesn't show them must not wipe
// them out (the same reasoning as assignee_ids on PUT).
func applyNutritionInput(current nutritionScores, nutriScore *string, yukaScore json.RawMessage) (nutritionScores, error) {
	if nutriScore != nil {
		grade, err := validate.NutriScore(*nutriScore)
		if err != nil {
			return current, err
		}
		current.NutriScore = grade
	}
	if yukaScore != nil {
		if string(yukaScore) == "null" {
			current.YukaScore = nil
		} else {
			var score int
			if err := json.Unmarshal(yukaScore, &score); err != nil {
				return current, errors.New("yuka_score must be a number")
			}
			if err := validate.YukaScore(score); err != nil {
				return current, err
			}
			current.YukaScore = &score
		}
	}
	return current, nil
}

func sameNutrition(a, b nutritionScores) bool {
	if a.NutriScore != b.NutriScore || (a.YukaScore == nil) != (b.YukaScore == nil) {
		return false
	}
	return a.YukaScore == nil || *a.YukaScore == *b.YukaScore
}

// saveItemNutrition writes scores onto item when they differ from what it
// has, returning the item as stored.
func (app *Application) saveItemNutrition(ctx context.Context, item *models.Item, scores nutritionScores) (*models.Item, error) {
	if sameNutrition(itemNutrition(item), scores) {
		return item, nil
	}
	return app.DB.SetItemNutrition(ctx, item.ID, nullableString(scores.NutriScore), scores.YukaScore)
}

// resolveItemNutrition picks the scores of an item about to be added to a
// Courses list: each one the request gave, else the House catalog's for
// that product (the household scanned it before), else none.
func (app *Application) resolveItemNutrition(ctx context.Context, list *models.List, title string, explicit nutritionScores) nutritionScores {
	if explicit.NutriScore != "" && explicit.YukaScore != nil {
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
	if err := app.DB.SetProductNutrition(ctx, list.HouseID, item.Title, key, scores.NutriScore, scores.YukaScore); err != nil {
		app.Logger.Warn("learning product nutrition scores", "house_id", list.HouseID, "item_id", item.ID, "error", err)
	}
}

// wantsNutritionScores reports whether user shows nutrition scores anywhere.
func wantsNutritionScores(user *models.User) bool {
	return user.NutritionScoresMobile || user.NutritionScoresWeb
}

// enrichProductNutriScore fills in the Nutri-Score of a product the House
// catalog knew by its barcode without one (filed before the scores existed,
// or under a name typed by hand) from Open Food Facts, and keeps it in the
// catalog. Best effort and quiet: only for a user who shows the scores, only
// with PRODUCT_LOOKUP_ENABLED, within the same per-account limit as other
// lookups; any failure leaves the product as it was.
func (app *Application) enrichProductNutriScore(ctx context.Context, user *models.User, product *models.Product, code string) {
	if product.NutriScore != "" || !app.Config.ProductLookupEnabled || !wantsNutritionScores(user) {
		return
	}
	if !app.barcodeLookupLimiter().allow(strconv.FormatInt(user.ID, 10), barcodeLookupRateLimit) {
		return
	}
	app.resolveUserLanguage(user)
	lookupCtx, cancel := context.WithTimeout(ctx, barcodeLookupTimeout)
	defer cancel()
	found, err := app.lookupBarcode(lookupCtx, code, user.Language)
	if err != nil || found.NutriScore == "" {
		return
	}
	key := catalog.Key(product.Name)
	if err := app.DB.SetProductNutrition(ctx, product.HouseID, product.Name, key, found.NutriScore, product.YukaScore); err != nil {
		app.Logger.Warn("saving product nutri-score", "house_id", product.HouseID, "product_id", product.ID, "error", err)
		return
	}
	product.NutriScore = found.NutriScore
}
