package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"trakka/internal/catalog"
	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/scraper"
	"trakka/internal/validate"
)

// Quick shopping lists: what makes a "Courses" (groceries) list fast to fill.
//
//   - No duplicates: adding a product already on the list updates that item
//     instead (see addToQuickShoppingList) — unchecked if it was done, its
//     quantity adjusted.
//   - Aisles: every item can carry a store aisle (models.Item.Aisle); a new
//     one gets the household's usual aisle for that product, else a keyword
//     guess (resolveItemAisle).
//   - The House's product catalog (house_products, db/products.go): every
//     product added to or checked off one of its Courses lists, which feeds
//     autocomplete, favorites, "time to buy it again" suggestions and
//     barcode recognition — the /api/v1/houses/{id}/products endpoints
//     below, for House members only. A share recipient who isn't a member
//     can still add to a shared Courses list (and so feeds the catalog), but
//     never reads it: the catalog spans every list of the House, not only
//     the one shared with them.

// quickShoppingListTypes are the list types the features above apply to.
var quickShoppingListTypes = map[string]bool{"groceries": true}

func isQuickShoppingList(list *models.List) bool {
	return quickShoppingListTypes[list.Type]
}

const (
	// productSearchDefaultLimit/productSearchMaxLimit bound
	// GET /houses/{id}/products?q=, an autocomplete.
	productSearchDefaultLimit = 8
	productSearchMaxLimit     = 20
	// repurchaseSuggestionLimit bounds the "time to buy it again" part of
	// GET /houses/{id}/products/suggestions.
	repurchaseSuggestionLimit = 12
	// barcodeLookupTimeout bounds one Open Food Facts lookup, inside the
	// scraper client's own timeout: the user is standing in a shop aisle
	// waiting for it.
	barcodeLookupTimeout = 6 * time.Second
	// barcodeLookupRateWin/barcodeLookupRateLimit cap Open Food Facts
	// lookups per account (codes already in the catalog don't count): plenty
	// for scanning a trolley, too few to turn this server into a proxy for
	// someone else's bulk queries.
	barcodeLookupRateWin   = time.Minute
	barcodeLookupRateLimit = 30
)

// findDuplicateItem returns the item of items that is the same product as
// title (same catalog.Key), or nil. When the list somehow already holds
// several (added before this rule existed, or under a since-retyped list),
// a still-active one wins over a done one, then the most recently updated.
func findDuplicateItem(items []*models.Item, title string) *models.Item {
	key := catalog.Key(title)
	if key == "" {
		return nil
	}
	var best *models.Item
	for _, it := range items {
		if catalog.Key(it.Title) != key {
			continue
		}
		switch {
		case best == nil:
			best = it
		case best.Done && !it.Done:
			best = it
		case best.Done == it.Done && it.UpdatedAt > best.UpdatedAt:
			best = it
		}
	}
	return best
}

// mergedQuantity is the quantity an existing item ends up with when the same
// product is added to the list again, requested at a time: still on the
// list, the two add up (capped at maxItemQuantity); already bought (done),
// it's a new need, so the new quantity replaces the old one rather than
// adding to what was already bought.
func mergedQuantity(existing *models.Item, requested int) int {
	if requested <= 0 {
		requested = 1
	}
	if existing.Done {
		return requested
	}
	return min(existing.Quantity+requested, maxItemQuantity)
}

// resolveItemAisle picks the aisle of an item about to be added to a Courses
// list: the one the request chose, else the household's usual aisle for
// that product, else hint (the aisle Open Food Facts suggested for a
// scanned product — better informed than a guess from the name, but never
// above what the household itself settled on), else a keyword guess, else "".
func (app *Application) resolveItemAisle(ctx context.Context, list *models.List, title, explicit, hint string) string {
	if explicit != "" {
		return explicit
	}
	product, err := app.DB.GetProductByKey(ctx, list.HouseID, catalog.Key(title))
	if err == nil && product.Aisle != "" {
		return product.Aisle
	}
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		app.Logger.Warn("reading product catalog", "house_id", list.HouseID, "error", err)
	}
	if hint != "" {
		return hint
	}
	return catalog.GuessAisle(title)
}

// addToQuickShoppingList adds title to a Courses list without ever creating
// a duplicate: if the product is already on it, that item is reactivated
// (unchecked, quantity per mergedQuantity, aisle filled in if it had none,
// urgent if the new request is) and merged is true; otherwise create makes
// the new item, which gets aisle. One lookup-then-write at a time
// (quickAddMu), so two simultaneous adds of the same product — two members
// in the same shop, an offline queue replaying — still end up as one item.
func (app *Application) addToQuickShoppingList(ctx context.Context, list *models.List, title string, quantity int, aisle string, aisleExplicit, urgent bool, create func() (*models.Item, error)) (item *models.Item, merged bool, err error) {
	app.quickAddMu.Lock()
	defer app.quickAddMu.Unlock()

	items, err := app.DB.ListItemsByList(ctx, list.ID)
	if err != nil {
		return nil, false, err
	}
	if dup := findDuplicateItem(items, title); dup != nil {
		item, err = app.DB.ReactivateItem(ctx, dup.ID, mergedQuantity(dup, quantity), nullableString(aisle), urgent)
		if err == nil && aisleExplicit && stringValue(item.Aisle) != aisle {
			item, err = app.DB.SetItemAisle(ctx, item.ID, &aisle)
		}
		return item, true, err
	}
	item, err = create()
	if err == nil && aisle != "" {
		item, err = app.DB.SetItemAisle(ctx, item.ID, &aisle)
	}
	return item, false, err
}

// recordProductAdded notes a product added to a Courses list in its House's
// catalog. Best effort: the item is already saved, and a catalog that
// misses one addition only suggests a little less well.
func (app *Application) recordProductAdded(ctx context.Context, list *models.List, item *models.Item, aisleExplicit bool, barcode string) {
	key := catalog.Key(item.Title)
	if key == "" {
		return
	}
	err := app.DB.RecordProductAdded(ctx, db.ProductAddition{
		HouseID:       list.HouseID,
		Name:          item.Title,
		Key:           key,
		Aisle:         stringValue(item.Aisle),
		AisleExplicit: aisleExplicit,
		Barcode:       barcode,
	})
	if err != nil {
		app.Logger.Warn("recording product addition", "house_id", list.HouseID, "item_id", item.ID, "error", err)
	}
}

// recordProductPurchased notes an item checked off a Courses list as a
// purchase in its House's catalog. Best effort, like recordProductAdded.
func (app *Application) recordProductPurchased(ctx context.Context, list *models.List, item *models.Item) {
	key := catalog.Key(item.Title)
	if key == "" {
		return
	}
	if err := app.DB.RecordProductPurchased(ctx, list.HouseID, item.Title, key, stringValue(item.Aisle), time.Now()); err != nil {
		app.Logger.Warn("recording product purchase", "house_id", list.HouseID, "item_id", item.ID, "error", err)
	}
}

// learnProductAisle makes an aisle someone just filed a Courses item under
// the household's usual aisle for that product. Best effort.
func (app *Application) learnProductAisle(ctx context.Context, list *models.List, item *models.Item) {
	key := catalog.Key(item.Title)
	if key == "" {
		return
	}
	if err := app.DB.SetProductAisle(ctx, list.HouseID, item.Title, key, stringValue(item.Aisle)); err != nil {
		app.Logger.Warn("learning product aisle", "house_id", list.HouseID, "item_id", item.ID, "error", err)
	}
}

// updateProductCatalog keeps a Courses list's House catalog in step with an
// edit of one of its items: an aisle someone chose becomes the product's
// usual one, a check-off counts as a purchase. Nothing to do for another
// list type, nor when neither happened (the list isn't even read then).
func (app *Application) updateProductCatalog(ctx context.Context, item *models.Item, aisleChanged, justCompleted bool) {
	if !aisleChanged && !justCompleted {
		return
	}
	list, err := app.DB.GetList(ctx, item.ListID)
	if err != nil {
		app.Logger.Warn("reading list for the product catalog", "list_id", item.ListID, "error", err)
		return
	}
	if !isQuickShoppingList(list) {
		return
	}
	if aisleChanged {
		app.learnProductAisle(ctx, list, item)
	}
	if justCompleted {
		app.recordProductPurchased(ctx, list, item)
	}
}

// productIDFromPath reads {productId}, writing a 400 when it isn't a
// positive integer.
func productIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("productId"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid product id")
		return 0, false
	}
	return id, true
}

// handleProductsIndex answers GET /api/v1/houses/{id}/products: with
// ?q=..., up to ?limit= (default 8, at most 20) catalog products whose name
// contains q, for autocomplete (see db.SearchProducts for the order); with
// ?favorites=true, every favorite instead.
func (app *Application) handleProductsIndex(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok || !app.authorizeHouseAccess(w, r, houseID) {
		return
	}
	query := r.URL.Query()
	if query.Get("favorites") == "true" {
		products, err := app.DB.ListFavoriteProducts(r.Context(), houseID)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, products)
		return
	}

	q := validate.Text(query.Get("q"))
	if !validate.MaxLen(q, validate.MaxTitleLen) {
		writeError(w, http.StatusBadRequest, "q is too long")
		return
	}
	limit := productSearchDefaultLimit
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = min(n, productSearchMaxLimit)
	}
	products, err := app.DB.SearchProducts(r.Context(), houseID, catalog.Key(q), limit)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, products)
}

// productSuggestions is GET /api/v1/houses/{id}/products/suggestions' body.
type productSuggestions struct {
	Favorites []*models.Product          `json:"favorites"`
	Recurring []models.ProductSuggestion `json:"recurring"`
}

// handleProductSuggestions answers GET /api/v1/houses/{id}/products/suggestions
// for the quick-add panel: every favorite, and up to 12 products bought
// regularly (at least twice) — the ones due for buying again first, see
// catalog.RankRepurchases. With ?list_id= (a list of this House), products
// already active on that list and favorites are left out of the second
// part, which would only repeat them.
func (app *Application) handleProductSuggestions(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok || !app.authorizeHouseAccess(w, r, houseID) {
		return
	}
	ctx := r.Context()

	active := map[string]bool{}
	if raw := r.URL.Query().Get("list_id"); raw != "" {
		listID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || listID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid list_id")
			return
		}
		list, err := app.DB.GetList(ctx, listID)
		if errors.Is(err, db.ErrNotFound) || (err == nil && list.HouseID != houseID) {
			writeError(w, http.StatusBadRequest, "list_id is not a list of this house")
			return
		} else if err != nil {
			app.serverError(w, r, err)
			return
		}
		items, err := app.DB.ListItemsByList(ctx, listID)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
		for _, it := range items {
			if !it.Done {
				active[catalog.Key(it.Title)] = true
			}
		}
	}

	favorites, err := app.DB.ListFavoriteProducts(ctx, houseID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	purchased, err := app.DB.ListPurchasedProducts(ctx, houseID, 2)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	candidates := make([]*models.Product, 0, len(purchased))
	histories := make([]catalog.PurchaseHistory, 0, len(purchased))
	for _, p := range purchased {
		if p.IsFavorite || active[catalog.Key(p.Name)] {
			continue
		}
		first, errFirst := time.Parse(time.RFC3339, *p.FirstPurchasedAt)
		last, errLast := time.Parse(time.RFC3339, *p.LastPurchasedAt)
		if errFirst != nil || errLast != nil {
			continue
		}
		candidates = append(candidates, p)
		histories = append(histories, catalog.PurchaseHistory{Count: p.PurchaseCount, First: first, Last: last})
	}

	recurring := []models.ProductSuggestion{}
	for _, ranked := range catalog.RankRepurchases(histories, time.Now(), repurchaseSuggestionLimit) {
		reason := "frequent"
		if ranked.Due {
			reason = "due"
		}
		recurring = append(recurring, models.ProductSuggestion{
			Product:         *candidates[ranked.Index],
			Reason:          reason,
			AvgIntervalDays: ranked.AvgIntervalDays,
			DaysSinceLast:   ranked.DaysSinceLast,
		})
	}
	writeJSON(w, http.StatusOK, productSuggestions{Favorites: favorites, Recurring: recurring})
}

// handleProductsUpsert answers POST /api/v1/houses/{id}/products
// ({"name", "is_favorite"?, "aisle"?}): applies the changes to the
// product named name (same catalog.Key), creating it if the catalog doesn't
// know it — how an item is starred from the list view, where only its name
// is at hand. Answers 200 with the product.
func (app *Application) handleProductsUpsert(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok || !app.authorizeHouseAccess(w, r, houseID) {
		return
	}
	var in struct {
		Name       string  `json:"name"`
		IsFavorite *bool   `json:"is_favorite"`
		Aisle      *string `json:"aisle"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	name := validate.Text(in.Name)
	key := catalog.Key(name)
	if key == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !validate.MaxLen(name, validate.MaxTitleLen) {
		writeError(w, http.StatusBadRequest, "name is too long")
		return
	}
	aisle, ok := validateOptionalAisle(w, in.Aisle)
	if !ok {
		return
	}
	product, err := app.DB.UpsertProduct(r.Context(), houseID, name, key, in.IsFavorite, aisle)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, product)
}

// handleProductsUpdate answers PATCH /api/v1/houses/{id}/products/{productId}
// ({"is_favorite"?, "aisle"?}; absent = unchanged, aisle "" clears it).
func (app *Application) handleProductsUpdate(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok || !app.authorizeHouseAccess(w, r, houseID) {
		return
	}
	productID, ok := productIDFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		IsFavorite *bool   `json:"is_favorite"`
		Aisle      *string `json:"aisle"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	aisle, ok := validateOptionalAisle(w, in.Aisle)
	if !ok {
		return
	}
	product, err := app.DB.UpdateProduct(r.Context(), houseID, productID, in.IsFavorite, aisle)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "product not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, product)
}

// handleProductsDelete answers DELETE /api/v1/houses/{id}/products/{productId}:
// the catalog forgets the product (a typo it keeps suggesting, something
// the household no longer buys). Items on lists are untouched.
func (app *Application) handleProductsDelete(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok || !app.authorizeHouseAccess(w, r, houseID) {
		return
	}
	productID, ok := productIDFromPath(w, r)
	if !ok {
		return
	}
	if err := app.DB.DeleteProduct(r.Context(), houseID, productID); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "product not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleProductBarcodeLookup answers GET /api/v1/houses/{id}/products/barcode/{code}:
// the product a scanned barcode stands for — from the household's own
// catalog first (codes are filed there when an item is added with one),
// else, with PRODUCT_LOOKUP_ENABLED, from Open Food Facts, its aisle
// derived from the categories found there or guessed from its name. An
// unknown code is a 404 with code "not_found" (or "lookup_failed" when Open
// Food Facts couldn't be asked): the frontend then asks for the product's
// name, and the item added with the code teaches the catalog.
func (app *Application) handleProductBarcodeLookup(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok || !app.authorizeHouseAccess(w, r, houseID) {
		return
	}
	code, err := validate.Barcode(r.PathValue("code"))
	if err != nil || code == "" {
		writeError(w, http.StatusBadRequest, validate.ErrInvalidBarcode.Error())
		return
	}

	product, err := app.DB.GetProductByBarcode(r.Context(), houseID, code)
	if err == nil {
		writeJSON(w, http.StatusOK, models.BarcodeLookup{Barcode: code, Name: product.Name, Aisle: product.Aisle, Source: "house", Product: product})
		return
	}
	if !errors.Is(err, db.ErrNotFound) {
		app.serverError(w, r, err)
		return
	}
	if !app.Config.ProductLookupEnabled {
		writeCodedError(w, http.StatusNotFound, "not_found", "unknown barcode")
		return
	}

	user := userFromContext(r)
	if !app.barcodeLookupLimiter().allow(strconv.FormatInt(user.ID, 10), barcodeLookupRateLimit) {
		w.Header().Set("Retry-After", strconv.Itoa(int(barcodeLookupRateWin.Seconds())))
		writeError(w, http.StatusTooManyRequests, "too many barcode lookups, try again in a minute")
		return
	}
	app.resolveUserLanguage(user)
	ctx, cancel := context.WithTimeout(r.Context(), barcodeLookupTimeout)
	defer cancel()
	found, err := app.lookupBarcode(ctx, code, user.Language)
	if errors.Is(err, scraper.ErrProductNotFound) {
		writeCodedError(w, http.StatusNotFound, "not_found", "unknown barcode")
		return
	} else if err != nil {
		app.Logger.Warn("open food facts barcode lookup failed", "error", err)
		writeCodedError(w, http.StatusNotFound, "lookup_failed", "barcode lookup failed")
		return
	}
	// A name from a third-party database goes through the same cleaning as
	// one typed into the item form, and fits in an item title.
	name := validate.Text(found.Name)
	if !validate.MaxLen(name, validate.MaxTitleLen) {
		name = strings.TrimSpace(string([]rune(name)[:validate.MaxTitleLen]))
	}
	aisle := catalog.AisleFromCategories(found.Categories)
	if aisle == "" {
		aisle = catalog.GuessAisle(name)
	}
	writeJSON(w, http.StatusOK, models.BarcodeLookup{Barcode: code, Name: name, Aisle: aisle, Source: "openfoodfacts"})
}

// lookupBarcode asks Open Food Facts about a barcode:
// scraper.LookupBarcode, or barcodeLookupHook in tests.
func (app *Application) lookupBarcode(ctx context.Context, code, lang string) (*scraper.BarcodeProduct, error) {
	if app.barcodeLookupHook != nil {
		return app.barcodeLookupHook(ctx, code, lang)
	}
	return scraper.LookupBarcode(ctx, code, lang)
}

// barcodeLookupLimiter is the per-account Open Food Facts lookup bucket,
// lazily built like the authentication ones (see ratelimit.go).
func (app *Application) barcodeLookupLimiter() *rateLimiter {
	app.barcodeLookupLimiterOnce.Do(func() { app.barcodeLookupLimiterVal = newRateLimiter(barcodeLookupRateWin) })
	return app.barcodeLookupLimiterVal
}

// validateOptionalAisle validates an optional aisle field (nil = absent,
// left as is), writing a 400 for an unknown aisle.
func validateOptionalAisle(w http.ResponseWriter, raw *string) (*string, bool) {
	if raw == nil {
		return nil, true
	}
	clean, err := validate.Aisle(*raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return &clean, true
}
