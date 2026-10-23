package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// openFoodFactsBaseURL is the Open Food Facts API host LookupBarcode asks.
// A var only so tests can point it at an httptest server.
var openFoodFactsBaseURL = "https://world.openfoodfacts.org"

// openFoodFactsUserAgent identifies Trakka to Open Food Facts, as its API
// terms ask of every client (unlike the product-page fetches, which present
// as a browser: this is a documented, public API).
const openFoodFactsUserAgent = "Trakka/1.0 (self-hosted shopping lists; https://github.com/Trakka-project/Trakka)"

// openFoodFactsFields is everything LookupBarcode reads; asking for only
// these keeps the answer a few hundred bytes instead of the full record.
const openFoodFactsFields = "code,product_name,product_name_fr,product_name_en,generic_name,generic_name_fr,generic_name_en,brands,categories_tags,nutriscore_grade,nova_group"

// barcodePattern is what LookupBarcode accepts: the caller normalizes with
// internal/validate.Barcode first; this is a second check right where the
// value becomes part of a URL path.
var barcodePattern = regexp.MustCompile(`^[0-9]{8,14}$`)

// ErrProductNotFound is LookupBarcode's answer for a barcode Open Food
// Facts doesn't know (or knows without any name).
var ErrProductNotFound = errors.New("product not found")

// BarcodeProduct is what Open Food Facts knows about a barcode.
type BarcodeProduct struct {
	Barcode string
	// Name is the product's name in the requested language when Open Food
	// Facts has one, else its default name, else its generic name.
	Name       string
	Brand      string
	Categories []string
	// NutriScore is the product's Nutri-Score grade, "A" to "E", or ""
	// when Open Food Facts has none (it answers "unknown" or
	// "not-applicable" then, or nothing at all).
	NutriScore string
	// NovaGroup is the product's NOVA group, 1 to 4, or 0 when Open Food
	// Facts has none.
	NovaGroup int
}

type openFoodFactsResponse struct {
	Status  int `json:"status"`
	Product struct {
		Code            string          `json:"code"`
		ProductName     string          `json:"product_name"`
		ProductNameFR   string          `json:"product_name_fr"`
		ProductNameEN   string          `json:"product_name_en"`
		GenericName     string          `json:"generic_name"`
		GenericNameFR   string          `json:"generic_name_fr"`
		GenericNameEN   string          `json:"generic_name_en"`
		Brands          string          `json:"brands"`
		CategoriesTags  []string        `json:"categories_tags"`
		NutriscoreGrade string          `json:"nutriscore_grade"`
		NovaGroup       json.RawMessage `json:"nova_group"`
	} `json:"product"`
}

// LookupBarcode asks Open Food Facts (the collaborative open food products
// database) what product barcode is, preferring names in lang ("fr" or
// "en"). It goes through the same SSRF-guarded httpClient as every other
// outbound request of this package, although the host is fixed. Unknown
// barcodes return ErrProductNotFound; any other failure (network, timeout,
// unexpected answer) a different error, which callers treat the same way:
// the product simply isn't recognized.
func LookupBarcode(ctx context.Context, barcode, lang string) (*BarcodeProduct, error) {
	if !barcodePattern.MatchString(barcode) {
		return nil, fmt.Errorf("invalid barcode")
	}
	lookupURL := openFoodFactsBaseURL + "/api/v2/product/" + barcode + "?fields=" + openFoodFactsFields
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building barcode lookup request: %w", err)
	}
	req.Header.Set("User-Agent", openFoodFactsUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("looking barcode up: %w", err)
	}
	defer resp.Body.Close()

	// An unknown barcode is a 404 carrying {"status": 0}.
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrProductNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("looking barcode up: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("reading barcode lookup: %w", err)
	}
	return parseOpenFoodFactsProduct(body, barcode, lang)
}

// parseOpenFoodFactsProduct reads an Open Food Facts v2 product answer.
func parseOpenFoodFactsProduct(body []byte, barcode, lang string) (*BarcodeProduct, error) {
	var decoded openFoodFactsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decoding barcode lookup: %w", err)
	}
	if decoded.Status != 1 {
		return nil, ErrProductNotFound
	}
	p := decoded.Product
	localized, localizedGeneric := p.ProductNameFR, p.GenericNameFR
	if lang == "en" {
		localized, localizedGeneric = p.ProductNameEN, p.GenericNameEN
	}
	name := firstNonBlank(localized, p.ProductName, localizedGeneric, p.GenericName)
	if name == "" {
		return nil, ErrProductNotFound
	}
	brand, _, _ := strings.Cut(p.Brands, ",")
	return &BarcodeProduct{
		Barcode:    barcode,
		Name:       name,
		Brand:      strings.TrimSpace(brand),
		Categories: p.CategoriesTags,
		NutriScore: nutriScoreGrade(p.NutriscoreGrade),
		NovaGroup:  novaGroup(p.NovaGroup),
	}, nil
}

// nutriScoreGrade turns Open Food Facts' nutriscore_grade ("a" to "e",
// else "unknown", "not-applicable" or "") into "A" to "E", or "" for
// anything else.
func nutriScoreGrade(raw string) string {
	switch grade := strings.ToUpper(strings.TrimSpace(raw)); grade {
	case "A", "B", "C", "D", "E":
		return grade
	}
	return ""
}

// novaGroup reads Open Food Facts' nova_group — a number today, a string
// ("4") in older records, absent or null when unknown — as 1 to 4, or 0
// for anything else.
func novaGroup(raw json.RawMessage) int {
	var group int
	if err := json.Unmarshal(raw, &group); err != nil {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return 0
		}
		if group, err = strconv.Atoi(strings.TrimSpace(text)); err != nil {
			return 0
		}
	}
	if group < 1 || group > 4 {
		return 0
	}
	return group
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if v = strings.Join(strings.Fields(v), " "); v != "" {
			return v
		}
	}
	return ""
}
