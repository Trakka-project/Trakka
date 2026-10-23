package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withOpenFoodFacts points LookupBarcode and SearchProducts at handler for
// one test: both base URLs at an httptest server, and httpClient at that
// server's own client (the SSRF guard would refuse its loopback address).
func withOpenFoodFacts(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	savedClient, savedURL, savedSearchURL := httpClient, openFoodFactsBaseURL, openFoodFactsSearchBaseURL
	httpClient, openFoodFactsBaseURL, openFoodFactsSearchBaseURL = srv.Client(), srv.URL, srv.URL
	t.Cleanup(func() {
		httpClient, openFoodFactsBaseURL, openFoodFactsSearchBaseURL = savedClient, savedURL, savedSearchURL
		srv.Close()
	})
}

func TestLookupBarcodeFound(t *testing.T) {
	var gotPath, gotAgent, gotFields string
	withOpenFoodFacts(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAgent, gotFields = r.URL.Path, r.Header.Get("User-Agent"), r.URL.Query().Get("fields")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"3017620422003","status":1,"status_verbose":"product found",
			"product":{"code":"3017620422003","product_name":"Nutella","product_name_fr":"  Nutella   pâte à tartiner ",
			"brands":"Ferrero, Nutella","categories_tags":["en:breakfasts","en:sweet-spreads"],"nutriscore_grade":"e","nova_group":4}}`))
	})

	p, err := LookupBarcode(context.Background(), "3017620422003", "fr")
	if err != nil {
		t.Fatalf("LookupBarcode: %v", err)
	}
	if gotPath != "/api/v2/product/3017620422003" || !strings.HasPrefix(gotAgent, "Trakka/") || !strings.Contains(gotFields, "categories_tags") || !strings.Contains(gotFields, "nutriscore_grade") || !strings.Contains(gotFields, "nova_group") {
		t.Fatalf("request path %q, agent %q, fields %q", gotPath, gotAgent, gotFields)
	}
	if p.Name != "Nutella pâte à tartiner" || p.Brand != "Ferrero" || len(p.Categories) != 2 || p.Barcode != "3017620422003" || p.NutriScore != "E" || p.NovaGroup != 4 {
		t.Fatalf("got %+v", p)
	}

	// English falls back to the default name when there's no English one.
	if p, _ = LookupBarcode(context.Background(), "3017620422003", "en"); p.Name != "Nutella" {
		t.Fatalf("english name %q, want the default name", p.Name)
	}
}

// Open Food Facts' nutriscore_grade becomes an upper-case grade, and its
// placeholders for "no grade" become "".
func TestOpenFoodFactsNutriScore(t *testing.T) {
	for raw, want := range map[string]string{"a": "A", "B": "B", " c ": "C", "d": "D", "e": "E",
		"": "", "unknown": "", "not-applicable": "", "f": "", "ab": ""} {
		body := `{"status":1,"product":{"code":"3017620422003","product_name":"X","nutriscore_grade":"` + raw + `"}}`
		p, err := parseOpenFoodFactsProduct([]byte(body), "3017620422003", "fr")
		if err != nil {
			t.Fatalf("parsing grade %q: %v", raw, err)
		}
		if p.NutriScore != want {
			t.Errorf("nutriscore_grade %q → %q, want %q", raw, p.NutriScore, want)
		}
	}
}

func TestLookupBarcodeNotFound(t *testing.T) {
	withOpenFoodFacts(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/00000000") {
			// Known product without any name: as good as unknown.
			_, _ = w.Write([]byte(`{"status":1,"product":{"code":"00000000","brands":"X"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":0,"status_verbose":"product not found"}`))
	})
	for _, code := range []string{"4006381333931", "00000000"} {
		if _, err := LookupBarcode(context.Background(), code, "fr"); !errors.Is(err, ErrProductNotFound) {
			t.Fatalf("LookupBarcode(%s) error = %v, want ErrProductNotFound", code, err)
		}
	}
}

func TestLookupBarcodeFailures(t *testing.T) {
	withOpenFoodFacts(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/12345670") {
			_, _ = w.Write([]byte(`not json`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	for _, code := range []string{"4006381333931", "12345670"} {
		if _, err := LookupBarcode(context.Background(), code, "fr"); err == nil || errors.Is(err, ErrProductNotFound) {
			t.Fatalf("LookupBarcode(%s) error = %v, want a lookup failure", code, err)
		}
	}
	// Never reaches the network with anything but digits.
	for _, code := range []string{"", "123", "../../x", "3017620422003?x=1", "301762042200a"} {
		if _, err := LookupBarcode(context.Background(), code, "fr"); err == nil {
			t.Fatalf("LookupBarcode(%q) accepted", code)
		}
	}
}

func TestNovaGroup(t *testing.T) {
	for raw, want := range map[string]int{
		`1`: 1, `4`: 4, `"3"`: 3, `" 2 "`: 2,
		`0`: 0, `5`: 0, `null`: 0, `""`: 0, `"unknown"`: 0, `2.5`: 0, ``: 0,
	} {
		if got := novaGroup(json.RawMessage(raw)); got != want {
			t.Errorf("novaGroup(%s) = %d, want %d", raw, got, want)
		}
	}
}
