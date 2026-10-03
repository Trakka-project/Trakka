package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"trakka/internal/auth"
)

// newOIDCTestApplication is newTestApplication with OIDC switched on. The
// client is never asked to talk to a provider here, only to exist.
func newOIDCTestApplication(t *testing.T) *Application {
	t.Helper()
	app := newTestApplication(t)
	app.Auth = auth.NewService(app.DB, &auth.OIDCClient{}, time.Hour, false)
	return app
}

// startAppHandoff runs what handleOIDCCallback does once the provider has
// vouched for the user, and returns the code the app would receive.
func startAppHandoff(t *testing.T, app *Application, userID int64, challenge string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handOffToApp(rec, httptest.NewRequest(http.MethodGet, "/auth/oidc/callback", nil), userID, challenge)
	location := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || !strings.HasPrefix(location, appSSOReturnURL+"?") {
		t.Fatalf("handoff: got %d to %q, want a redirect to the app", rec.Code, location)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("handoff redirect must not be cacheable, got Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parsing %q: %v", location, err)
	}
	code := parsed.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in %q", location)
	}
	return code
}

func redeemAppHandoff(app *Application, code, verifier string) *httptest.ResponseRecorder {
	form := url.Values{"code": {code}, "verifier": {verifier}}
	req := httptest.NewRequest(http.MethodPost, "/auth/oidc/app-session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.handleOIDCAppSession(rec, req)
	return rec
}

func sessionCookieSet(rec *httptest.ResponseRecorder) bool {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName && cookie.Value != "" {
			return true
		}
	}
	return false
}

func TestAppHandoffOpensASessionOnceForTheRightVerifier(t *testing.T) {
	app := newOIDCTestApplication(t)
	user := mustCreateTestUser(t, app, "sso@example.com")
	verifier, challenge, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}

	code := startAppHandoff(t, app, user.ID, challenge)
	rec := redeemAppHandoff(app, code, verifier)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" || !sessionCookieSet(rec) {
		t.Fatalf("redeeming: got %d to %q (session cookie: %v), want a session and a redirect to /",
			rec.Code, rec.Header().Get("Location"), sessionCookieSet(rec))
	}

	// Single use.
	rec = redeemAppHandoff(app, code, verifier)
	if sessionCookieSet(rec) || rec.Header().Get("Location") != "/auth/login?error=oidc_failed" {
		t.Fatalf("second redemption: got %d to %q, want it refused", rec.Code, rec.Header().Get("Location"))
	}
}

// Whoever intercepts the deep link (any app can declare its scheme) holds the
// code but not the verifier, which stays in the Trakka app.
func TestAppHandoffRefusesAnotherVerifierAndBurnsTheCode(t *testing.T) {
	app := newOIDCTestApplication(t)
	user := mustCreateTestUser(t, app, "sso@example.com")
	verifier, challenge, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	otherVerifier, _, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}

	code := startAppHandoff(t, app, user.ID, challenge)
	for _, attempt := range []string{otherVerifier, "", challenge} {
		rec := redeemAppHandoff(app, code, attempt)
		if sessionCookieSet(rec) {
			t.Fatalf("verifier %q opened a session", attempt)
		}
	}
	if rec := redeemAppHandoff(app, code, verifier); sessionCookieSet(rec) {
		t.Fatal("a code must not survive a failed redemption")
	}
}

func TestAppHandoffExpires(t *testing.T) {
	app := newOIDCTestApplication(t)
	user := mustCreateTestUser(t, app, "sso@example.com")
	verifier, challenge, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}

	code := startAppHandoff(t, app, user.ID, challenge)
	store := app.appHandoffs()
	store.mu.Lock()
	for hash, entry := range store.entries {
		entry.expires = time.Now().Add(-time.Second)
		store.entries[hash] = entry
	}
	store.mu.Unlock()

	if rec := redeemAppHandoff(app, code, verifier); sessionCookieSet(rec) {
		t.Fatal("an expired code opened a session")
	}
}

// Login CSRF: a site that went through the flow itself, and so holds a valid
// code and verifier for its own account, must not be able to make a
// visitor's browser redeem them.
func TestAppHandoffRefusesCrossSiteRedemption(t *testing.T) {
	app := newOIDCTestApplication(t)
	user := mustCreateTestUser(t, app, "attacker@example.com")
	verifier, challenge, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	code := startAppHandoff(t, app, user.ID, challenge)
	handler := requireSameOriginWrite("", http.HandlerFunc(app.handleOIDCAppSession))

	form := url.Values{"code": {code}, "verifier": {verifier}}
	for _, headers := range []map[string]string{
		{"Origin": "https://evil.example"},
		{"Origin": "null", "Sec-Fetch-Site": "cross-site"},
	} {
		req := httptest.NewRequest(http.MethodPost, "https://trakka.example/auth/oidc/app-session", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || sessionCookieSet(rec) {
			t.Fatalf("headers %v: got %d (session cookie: %v), want 403", headers, rec.Code, sessionCookieSet(rec))
		}
	}

	// The app's own request, made by the WebView on the app's behalf rather
	// than by a page, goes through.
	req := httptest.NewRequest(http.MethodPost, "https://trakka.example/auth/oidc/app-session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "null")
	req.Header.Set("Sec-Fetch-Site", "none")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !sessionCookieSet(rec) {
		t.Fatalf("the app's own redemption: got %d, want a session", rec.Code)
	}
}

func TestOIDCLoginRejectsMalformedAppChallenge(t *testing.T) {
	app := newOIDCTestApplication(t)
	for _, challenge := range []string{"short", strings.Repeat("A", 42) + "=", strings.Repeat("!", 43)} {
		rec := httptest.NewRecorder()
		app.handleOIDCLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/oidc/login?app_challenge="+url.QueryEscape(challenge), nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("app_challenge %q: got %d, want 400", challenge, rec.Code)
		}
	}
}

// A sign-in the app started comes back to the app even when it fails, so the
// app can show the error on its own sign-in page; a browser sign-in still
// lands on the browser's.
func TestOIDCCallbackFailureReturnsToWhereTheSignInStarted(t *testing.T) {
	app := newOIDCTestApplication(t)
	_, challenge, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		flow url.Values
		want string
	}{
		{url.Values{"state": {"s"}, "nonce": {"n"}, "verifier": {"v"}, "app_challenge": {challenge}}, appSSOReturnURL + "?error=oidc_failed"},
		{url.Values{"state": {"s"}, "nonce": {"n"}, "verifier": {"v"}}, "/auth/login?error=oidc_failed"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?error=access_denied&state=s", nil)
		req.AddCookie(&http.Cookie{Name: oidcFlowCookieName, Value: tc.flow.Encode()})
		rec := httptest.NewRecorder()
		app.handleOIDCCallback(rec, req)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != tc.want {
			t.Fatalf("flow %v: got %d to %q, want %q", tc.flow, rec.Code, rec.Header().Get("Location"), tc.want)
		}
	}
}
