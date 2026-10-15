package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStaticAssetsAreRevalidated guards the fix for clients stuck on an old
// release: every static file (not only sw.js) must carry
// Cache-Control: no-cache, or a browser's heuristic HTTP caching — the
// Android WebView's in particular — keeps serving the previous JS/CSS after
// a deploy, even to a new service worker precaching the new version. The
// header must not cost a re-download either: an unchanged file still
// answers 304 to a conditional request.
func TestStaticAssetsAreRevalidated(t *testing.T) {
	app := newTestApplication(t)
	app.StaticDir = t.TempDir()
	for _, name := range []string{"index.html", "sw.js", "js/app.js", "css/base.css", "locales/fr.json"} {
		full := filepath.Join(app.StaticDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	routes := app.Routes()

	for _, path := range []string{"/", "/sw.js", "/js/app.js", "/css/base.css", "/locales/fr.json"} {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s: Cache-Control = %q, want no-cache", path, got)
		}

		lastModified := rec.Header().Get("Last-Modified")
		if lastModified == "" {
			t.Fatalf("GET %s: no Last-Modified to revalidate against", path)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("If-Modified-Since", lastModified)
		rec = httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("conditional GET %s: status %d, want 304", path, rec.Code)
		}
	}

	// A file changed after the client cached it is served again in full.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(app.StaticDir, "js", "app.js"), future, future); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/js/app.js", nil)
	req.Header.Set("If-Modified-Since", time.Now().UTC().Format(http.TimeFormat))
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /js/app.js after a change: status %d, want 200", rec.Code)
	}
}
