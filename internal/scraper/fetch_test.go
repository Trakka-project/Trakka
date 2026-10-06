package scraper

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestFetchProductInfoSeesPriceChange fetches the same product page twice,
// its price changing in between, through a real HTTP round trip: each fetch
// must read the page's current price — not a stale one, nor the crossed-out
// "before" price next to it — ask caches for a fresh copy, and log what it
// read. The SSRF guard refuses loopback by design, so the test swaps in the
// test server's own client for its duration.
func TestFetchProductInfoSeesPriceChange(t *testing.T) {
	var mu sync.Mutex
	price := "459,00"
	var cacheHeaders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		current := price
		cacheHeaders = append(cacheHeaders, r.Header.Get("Cache-Control")+"|"+r.Header.Get("Pragma"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<html><head><title>Console de jeux</title>
<meta property="og:price:amount" content="%s"></head>
<body><p class="price"><del>499,00 €</del> <span>%s €</span></p></body></html>`, current, current)
	}))
	defer srv.Close()

	saved := httpClient
	httpClient = srv.Client()
	defer func() { httpClient = saved }()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	first, err := FetchProductInfo(context.Background(), srv.URL+"/produit", logger)
	if err != nil {
		t.Fatal(err)
	}
	if first.Price == nil || *first.Price != 459 || first.PriceSource != "og:price" || first.PriceRaw != "459,00" {
		t.Fatalf("first fetch: price %v, source %q, raw %q", first.Price, first.PriceSource, first.PriceRaw)
	}

	mu.Lock()
	price = "419,99"
	mu.Unlock()
	second, err := FetchProductInfo(context.Background(), srv.URL+"/produit", logger)
	if err != nil {
		t.Fatal(err)
	}
	if second.Price == nil || *second.Price != 419.99 {
		t.Fatalf("second fetch read %v, want the new price 419.99", second.Price)
	}

	for _, h := range cacheHeaders {
		if h != "no-cache|no-cache" {
			t.Errorf("request sent Cache-Control|Pragma = %q, want no-cache|no-cache", h)
		}
	}
	for _, want := range []string{"product page request", "status=200", "price_source=og:price", "price_raw=419,99", "price=419.99", `page_title="Console de jeux"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("debug log lacks %q:\n%s", want, logs.String())
		}
	}
}

// TestFetchProductInfoReportsHTTPStatus: a blocked page is an error naming
// the status — never a page "without a price change".
func TestFetchProductInfoReportsHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Access Denied", http.StatusForbidden)
	}))
	defer srv.Close()
	saved := httpClient
	httpClient = srv.Client()
	defer func() { httpClient = saved }()

	_, err := FetchProductInfo(context.Background(), srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "unexpected status 403") {
		t.Fatalf("err = %v, want one naming status 403", err)
	}
}
