package handlers

import (
	"context"
	"strings"
	"sync"
	"time"

	"trakka/internal/catalog"
	"trakka/internal/models"
	"trakka/internal/scraper"
	"trakka/internal/validate"
)

// Enriching a product typed by hand on a Courses list from Open Food Facts'
// text search: what the barcode lookup does for a scanned product, for one
// added by name ("Eau", "ker-cadelac").
//
// The order of trust, for the aisle: the household's catalog, then the
// keyword dictionary (catalog.GuessAisle) — both already applied by
// resolveItemAisle when the item is created —, and only then Open Food
// Facts, when neither knew. For the Nutri-Score: the catalog
// (resolveItemNutrition), then Open Food Facts, only for a user who shows
// the scores. A search is made only when one of those is still missing.
//
// It never holds up the request: Open Food Facts' search answers in 2 to
// 15 s and sheds load with 503s, so it runs in the background and the
// response says enrichment_status "pending" — the client reads the item
// again a few seconds later. What it finds only ever fills gaps
// (db.FillItemEnrichment / FillProductEnrichment), so a choice someone made
// meanwhile always wins. Open Food Facts allows about 10 searches a minute
// per IP: one bucket for the whole instance stays under that, and answers
// (including "nothing relevant") are cached, while failures are not.

const (
	// productSearchTimeout bounds one background search.
	productSearchTimeout = 12 * time.Second
	// productSearchRateWin/productSearchRateLimit: instance-wide, under
	// Open Food Facts' own limit for this endpoint. Past it, an addition
	// simply isn't enriched.
	productSearchRateWin   = time.Minute
	productSearchRateLimit = 8
	// productSearchCacheTTL/productSearchCacheMax bound the cache of
	// answers; when full it starts over rather than tracking ages.
	productSearchCacheTTL = 6 * time.Hour
	productSearchCacheMax = 500
)

// productEnrichment is what a search found for a product name; "" for
// what it didn't.
type productEnrichment struct {
	Aisle      string
	NutriScore string
	ImageURL   string
}

type cachedProductSearch struct {
	found productEnrichment
	at    time.Time
}

// productSearchCache remembers recent answers by language and name key.
type productSearchCache struct {
	mu      sync.Mutex
	entries map[string]cachedProductSearch
}

func (c *productSearchCache) get(key string, now time.Time) (productEnrichment, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || now.Sub(entry.at) > productSearchCacheTTL {
		return productEnrichment{}, false
	}
	return entry.found, true
}

func (c *productSearchCache) put(key string, found productEnrichment, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= productSearchCacheMax {
		c.entries = make(map[string]cachedProductSearch)
	}
	c.entries[key] = cachedProductSearch{found: found, at: now}
}

// productSearchLimiter is the instance-wide search bucket, lazily built
// like the other limiters (see ratelimit.go).
func (app *Application) productSearchLimiter() *rateLimiter {
	app.productSearchLimiterOnce.Do(func() { app.productSearchLimiterVal = newRateLimiter(productSearchRateWin) })
	return app.productSearchLimiterVal
}

// searchProducts asks Open Food Facts: scraper.SearchProducts, or
// productSearchHook in tests.
func (app *Application) searchProducts(ctx context.Context, terms, lang string) ([]scraper.SearchProduct, error) {
	if app.productSearchHook != nil {
		return app.productSearchHook(ctx, terms, lang)
	}
	return scraper.SearchProducts(ctx, terms, lang)
}

// maybeEnrichProduct starts the background search for an item just added
// to a Courses list when it's worth one — PRODUCT_LOOKUP_ENABLED, typed
// rather than scanned (a scan already asked Open Food Facts), and still
// missing its aisle, or its Nutri-Score for a user who shows the scores —
// and returns the response's enrichment_status: "pending" when started,
// "" otherwise.
func (app *Application) maybeEnrichProduct(ctx context.Context, list *models.List, item *models.Item, barcode string, user *models.User) string {
	if !app.Config.ProductLookupEnabled || barcode != "" {
		return ""
	}
	needAisle := item.Aisle == nil
	needNutri := item.NutriScore == nil && wantsNutritionScores(user)
	key := catalog.Key(item.Title)
	if (!needAisle && !needNutri) || key == "" {
		return ""
	}
	app.resolveUserLanguage(user)
	lang, itemID, houseID := user.Language, item.ID, list.HouseID
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), productSearchTimeout)
		defer cancel()
		found, ok := app.productSearch(ctx, key, lang)
		if !ok {
			return
		}
		if !needAisle {
			found.Aisle = ""
		}
		if !needNutri {
			found.NutriScore = ""
		}
		if found == (productEnrichment{}) {
			return
		}
		if err := app.DB.FillItemEnrichment(ctx, itemID, found.Aisle, found.NutriScore, found.ImageURL); err != nil {
			app.Logger.Warn("saving product search enrichment", "item_id", itemID, "error", err)
			return
		}
		if err := app.DB.FillProductEnrichment(ctx, houseID, key, found.Aisle, found.NutriScore); err != nil {
			app.Logger.Warn("saving product search enrichment to the catalog", "house_id", houseID, "error", err)
		}
	}()
	return "pending"
}

// productSearch returns what Open Food Facts' text search finds for the
// product name keyed key: from the cache, else from a search when the
// instance-wide bucket allows one. ok is false when no answer could be had
// (bucket empty, search failed), which isn't cached.
func (app *Application) productSearch(ctx context.Context, key, lang string) (productEnrichment, bool) {
	cacheKey := lang + "|" + key
	if found, ok := app.productSearchCache.get(cacheKey, time.Now()); ok {
		return found, true
	}
	if !app.productSearchLimiter().allow("instance", productSearchRateLimit) {
		app.Logger.Debug("open food facts search skipped: rate limit reached")
		return productEnrichment{}, false
	}
	results, err := app.searchProducts(ctx, key, lang)
	if err != nil {
		app.Logger.Debug("open food facts search failed", "error", err)
		return productEnrichment{}, false
	}
	found := pickSearchMatch(key, results)
	app.productSearchCache.put(cacheKey, found, time.Now())
	return found, true
}

// pickSearchMatch reads what a search for query found, trusting only a
// relevant result: one whose name holds every word of query (a word
// matching the start of one of the name's, so "madeleine" finds
// "Madeleines"), else one whose name and brands do ("ker cadelac" finds
// Ker Cadélac's products). Open Food Facts' own ranking is kept: the first
// such result wins.
//
//   - Aisle: from that result's categories (catalog.AisleFromCategories).
//   - Nutri-Score: that result's when its name matched. When only the
//     brand did, the query names a brand, not a product, so a grade is
//     taken only if every result of that brand that has one agrees (all
//     Cristaline waters are A; Ker Cadélac's cakes are D or E: none).
//   - Image: only for a name matching closely (query words at least half
//     of the name's), since a picture of one particular product would
//     mislead on a generic name ("Eau").
func pickSearchMatch(query string, results []scraper.SearchProduct) productEnrichment {
	words := strings.Fields(catalog.Key(query))
	if len(words) == 0 {
		return productEnrichment{}
	}
	var byName, byBrand *scraper.SearchProduct
	var brandGrades []string
	for i := range results {
		r := &results[i]
		nameWords := strings.Fields(catalog.Key(r.Name))
		if byName == nil && containsAllWords(nameWords, words) {
			byName = r
		}
		if containsAllWords(append(nameWords, strings.Fields(catalog.Key(r.Brands))...), words) {
			if byBrand == nil {
				byBrand = r
			}
			if r.NutriScore != "" {
				brandGrades = append(brandGrades, r.NutriScore)
			}
		}
	}

	var found productEnrichment
	switch {
	case byName != nil:
		found.Aisle = catalog.AisleFromCategories(byName.Categories)
		found.NutriScore = byName.NutriScore
		if 2*len(words) >= len(strings.Fields(catalog.Key(byName.Name))) {
			if clean, err := validate.URL(byName.ImageURL); err == nil {
				found.ImageURL = clean
			}
		}
	case byBrand != nil:
		found.Aisle = catalog.AisleFromCategories(byBrand.Categories)
		if len(brandGrades) >= 2 && allSame(brandGrades) {
			found.NutriScore = brandGrades[0]
		}
	}
	return found
}

// containsAllWords reports whether every word of want starts one of have's.
func containsAllWords(have, want []string) bool {
	for _, w := range want {
		matched := false
		for _, h := range have {
			if strings.HasPrefix(h, w) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func allSame(values []string) bool {
	for _, v := range values[1:] {
		if v != values[0] {
			return false
		}
	}
	return true
}
