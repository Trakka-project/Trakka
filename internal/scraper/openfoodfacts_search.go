package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// openFoodFactsSearchBaseURL is Open Food Facts' current search service
// ("search-a-licious"), which SearchProducts asks first. A var only so
// tests can point it at an httptest server.
var openFoodFactsSearchBaseURL = "https://search.openfoodfacts.org"

// openFoodFactsSearchFields is everything SearchProducts reads from each
// result, which keeps the answer to a few kilobytes.
const openFoodFactsSearchFields = "code,product_name,product_name_fr,product_name_en,brands,categories_tags,nutriscore_grade,image_front_small_url"

// openFoodFactsSearchPageSize is how many results SearchProducts asks for:
// the caller picks the first relevant one, and checks a brand's results
// agree on a grade (see internal/handlers' pickSearchMatch).
const openFoodFactsSearchPageSize = 5

// maxSearchTermsLen bounds the text sent as search_terms.
const maxSearchTermsLen = 100

// SearchProduct is one result of an Open Food Facts text search.
type SearchProduct struct {
	Barcode string
	// Name is the product's name in the requested language when Open Food
	// Facts has one, else its default name.
	Name       string
	Brands     string
	Categories []string
	// NutriScore is "A" to "E", or "" when Open Food Facts has none.
	NutriScore string
	// ImageURL is a small front picture of the product, "" when none. Not
	// validated here: the caller passes it through internal/validate.URL.
	ImageURL string
}

// openFoodFactsSearchHit is one result, in either service's shape: the
// legacy search answers {"products": [...]} with brands as one
// comma-separated string, search-a-licious {"hits": [...]} with brands as
// an array.
type openFoodFactsSearchHit struct {
	Code            string          `json:"code"`
	ProductName     string          `json:"product_name"`
	ProductNameFR   string          `json:"product_name_fr"`
	ProductNameEN   string          `json:"product_name_en"`
	Brands          json.RawMessage `json:"brands"`
	CategoriesTags  []string        `json:"categories_tags"`
	NutriscoreGrade string          `json:"nutriscore_grade"`
	ImageFrontSmall string          `json:"image_front_small_url"`
}

type openFoodFactsSearchResponse struct {
	Products []openFoodFactsSearchHit `json:"products"`
	Hits     []openFoodFactsSearchHit `json:"hits"`
}

// SearchProducts asks Open Food Facts' product text search for terms, most
// scanned products first, preferring names in lang ("fr" or "en"): its
// current search service (search-a-licious, search.openfoodfacts.org)
// first, and the legacy /cgi/search.pl only when that fails. The legacy
// endpoint is the documented one, but it limits hard (about 10 requests a
// minute per IP) and sheds load with a 503 HTML page — measured on
// 2026-10-20, more than half of its answers were 503s, against none from
// search-a-licious, which answers in well under a second. Like
// LookupBarcode, both go through the SSRF-guarded httpClient with an
// identifying User-Agent. When both fail the error is the last one, which
// callers treat as "nothing found this time". No result is an empty
// slice, not an error.
func SearchProducts(ctx context.Context, terms, lang string) ([]SearchProduct, error) {
	terms = strings.Join(strings.Fields(terms), " ")
	if terms == "" {
		return nil, fmt.Errorf("empty search terms")
	}
	if runes := []rune(terms); len(runes) > maxSearchTermsLen {
		terms = string(runes[:maxSearchTermsLen])
	}
	modern := url.Values{
		"q":         {terms},
		"langs":     {lang},
		"page_size": {fmt.Sprint(openFoodFactsSearchPageSize)},
		"sort_by":   {"-unique_scans_n"},
		"fields":    {openFoodFactsSearchFields},
	}
	results, err := fetchOpenFoodFactsSearch(ctx, openFoodFactsSearchBaseURL+"/search?"+modern.Encode(), lang)
	if err == nil {
		return results, nil
	}
	legacy := url.Values{
		"search_terms":  {terms},
		"search_simple": {"1"},
		"action":        {"process"},
		"json":          {"1"},
		"page_size":     {fmt.Sprint(openFoodFactsSearchPageSize)},
		"sort_by":       {"unique_scans_n"},
		"fields":        {openFoodFactsSearchFields},
	}
	return fetchOpenFoodFactsSearch(ctx, openFoodFactsBaseURL+"/cgi/search.pl?"+legacy.Encode(), lang)
}

// fetchOpenFoodFactsSearch runs one search request and reads its answer.
func fetchOpenFoodFactsSearch(ctx context.Context, searchURL, lang string) ([]SearchProduct, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building product search request: %w", err)
	}
	req.Header.Set("User-Agent", openFoodFactsUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searching products: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searching products: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("reading product search: %w", err)
	}
	return parseOpenFoodFactsSearch(body, lang)
}

// parseOpenFoodFactsSearch reads either service's JSON answer, dropping
// results without any name.
func parseOpenFoodFactsSearch(body []byte, lang string) ([]SearchProduct, error) {
	var decoded openFoodFactsSearchResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decoding product search: %w", err)
	}
	hits := append(decoded.Hits, decoded.Products...)
	results := make([]SearchProduct, 0, len(hits))
	for _, p := range hits {
		localized := p.ProductNameFR
		if lang == "en" {
			localized = p.ProductNameEN
		}
		name := firstNonBlank(localized, p.ProductName)
		if name == "" {
			continue
		}
		results = append(results, SearchProduct{
			Barcode:    p.Code,
			Name:       name,
			Brands:     searchBrands(p.Brands),
			Categories: p.CategoriesTags,
			NutriScore: nutriScoreGrade(p.NutriscoreGrade),
			ImageURL:   strings.TrimSpace(p.ImageFrontSmall),
		})
	}
	return results, nil
}

// searchBrands reads a result's brands, an array (search-a-licious) or a
// comma-separated string (legacy search), as one comma-separated string.
func searchBrands(raw json.RawMessage) string {
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return strings.Join(list, ", ")
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return strings.TrimSpace(one)
	}
	return ""
}
