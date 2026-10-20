package scraper

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// search-a-licious answers {"hits": [...]}, brands as an array.
const searchALiciousAnswer = `{"hits":[
	{"code":"3274080005003","product_name":" Eau  de source ","brands":["Cristaline"],
	 "categories_tags":["en:beverages","en:waters"],"nutriscore_grade":"a","image_front_small_url":"https://images.openfoodfacts.org/x.jpg"},
	{"code":"1","brands":["Sans nom"]},
	{"code":"2","product_name":"Water","product_name_en":"Spring water","brands":["Volvic","Danone"],"nutriscore_grade":"unknown"}],
	"page":1,"page_size":5,"count":3}`

// The legacy /cgi/search.pl answers {"products": [...]}, brands as a string.
const legacySearchAnswer = `{"count":1,"products":[
	{"code":"3259421340012","product_name":"Madeleines","product_name_fr":"Madeleines extra moelleuses","brands":" Ker Cadélac ",
	 "categories_tags":["en:biscuits-and-cakes"],"nutriscore_grade":"d"}]}`

func TestSearchProducts(t *testing.T) {
	var gotPath, gotAgent string
	var gotQuery map[string][]string
	withOpenFoodFacts(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAgent = r.URL.Path, r.URL.Query(), r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(searchALiciousAnswer))
	})

	results, err := SearchProducts(context.Background(), "  eau   de source ", "fr")
	if err != nil {
		t.Fatalf("SearchProducts: %v", err)
	}
	if gotPath != "/search" || gotQuery["q"][0] != "eau de source" || gotQuery["langs"][0] != "fr" ||
		gotQuery["sort_by"][0] != "-unique_scans_n" || !strings.Contains(gotQuery["fields"][0], "nutriscore_grade") || !strings.HasPrefix(gotAgent, "Trakka/") {
		t.Fatalf("request %s %v (agent %q)", gotPath, gotQuery, gotAgent)
	}
	// The nameless result is dropped.
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(results), results)
	}
	if r := results[0]; r.Name != "Eau de source" || r.Brands != "Cristaline" || r.NutriScore != "A" || r.Barcode != "3274080005003" ||
		r.ImageURL != "https://images.openfoodfacts.org/x.jpg" || len(r.Categories) != 2 {
		t.Fatalf("first result %+v", r)
	}
	if r := results[1]; r.NutriScore != "" || r.Name != "Water" || r.Brands != "Volvic, Danone" {
		t.Fatalf("second result %+v", r)
	}
	// English prefers the English name.
	if results, _ = SearchProducts(context.Background(), "water", "en"); results[1].Name != "Spring water" {
		t.Fatalf("english name %q", results[1].Name)
	}
}

// When search-a-licious fails, the legacy search is asked.
func TestSearchProductsFallsBackToLegacySearch(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	withOpenFoodFacts(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/search" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if q := r.URL.Query(); q.Get("search_terms") != "madeleines" || q.Get("json") != "1" || q.Get("sort_by") != "unique_scans_n" {
			t.Errorf("legacy query %v", q)
		}
		_, _ = w.Write([]byte(legacySearchAnswer))
	})
	results, err := SearchProducts(context.Background(), "madeleines", "fr")
	if err != nil || len(results) != 1 {
		t.Fatalf("SearchProducts: %+v, %v", results, err)
	}
	if r := results[0]; r.Name != "Madeleines extra moelleuses" || r.Brands != "Ker Cadélac" || r.NutriScore != "D" {
		t.Fatalf("legacy result %+v", r)
	}
	if strings.Join(paths, ",") != "/search,/cgi/search.pl" {
		t.Fatalf("asked %v", paths)
	}
}

func TestSearchProductsFailures(t *testing.T) {
	withOpenFoodFacts(t, func(w http.ResponseWriter, r *http.Request) {
		terms := r.URL.Query().Get("q") + r.URL.Query().Get("search_terms")
		switch terms {
		case "busy":
			// What Open Food Facts answers when it sheds search load.
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`<!DOCTYPE html><title>Page temporarily unavailable</title>`))
		case "garbled":
			_, _ = w.Write([]byte(`not json`))
		default:
			_, _ = w.Write([]byte(`{"hits":[],"count":0}`))
		}
	})
	for _, terms := range []string{"busy", "garbled", "   "} {
		if _, err := SearchProducts(context.Background(), terms, "fr"); err == nil {
			t.Errorf("SearchProducts(%q) succeeded, want an error", terms)
		}
	}
	if results, err := SearchProducts(context.Background(), "rien", "fr"); err != nil || len(results) != 0 {
		t.Fatalf("no result: %v, %v", results, err)
	}
	// Long terms are cut, not refused.
	if _, err := SearchProducts(context.Background(), strings.Repeat("a", 500), "fr"); err != nil {
		t.Fatalf("long terms: %v", err)
	}
}
