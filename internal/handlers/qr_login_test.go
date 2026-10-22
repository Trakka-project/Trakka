package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"trakka/internal/auth"
	"trakka/internal/models"
)

// generateQRLogin asks for a QR sign-in code with the cookie's session.
func (e *tokenTestEnv) generateQRLogin(t *testing.T, cookie *http.Cookie) qrLoginResponse {
	t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/auth/qr-code/generate", "", "", cookie)
	var got qrLoginResponse
	decodeInto(t, rec, http.StatusCreated, &got)
	return got
}

// redeemQRLogin posts a scanned code the way the app's WebView does
// (postUrl: a form, Origin: null, Sec-Fetch-Site: none), through the router.
func (e *tokenTestEnv) redeemQRLogin(token string, headers map[string]string) *httptest.ResponseRecorder {
	form := url.Values{"token": {token}}
	req := httptest.NewRequest(http.MethodPost, "/auth/qr-code/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "null")
	req.Header.Set("Sec-Fetch-Site", "none")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// sessionCookieFrom returns the session cookie a response set, or nil.
func sessionCookieFrom(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName && cookie.Value != "" {
			return cookie
		}
	}
	return nil
}

func assertQRLoginRefused(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized || sessionCookieFrom(rec) != nil {
		t.Fatalf("got %d (session cookie set: %v): %s, want 401 and no session",
			rec.Code, sessionCookieFrom(rec) != nil, rec.Body.String())
	}
}

func TestQRLoginGenerate(t *testing.T) {
	e := newTokenTestEnv(t)
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	cookie := e.sessionCookie(t, alice)

	before := time.Now()
	got := e.generateQRLogin(t, cookie)
	if len(got.Token) != 43 {
		t.Fatalf("token %q: want 43 base64url characters (32 random bytes)", got.Token)
	}
	if got.ExpiresIn != int(qrLoginTTL/time.Second) || got.ExpiresIn < 180 || got.ExpiresIn > 300 {
		t.Fatalf("expires_in = %d, want qrLoginTTL (3 to 5 minutes)", got.ExpiresIn)
	}
	if got.ExpiresAt.Before(before.Add(qrLoginTTL)) || got.ExpiresAt.After(time.Now().Add(qrLoginTTL)) {
		t.Fatalf("expires_at = %v, want about now + %v", got.ExpiresAt, qrLoginTTL)
	}

	// Stored by hash only, bound to the caller.
	store := e.app.qrLogins()
	store.mu.Lock()
	code, ok := store.codes[auth.HashToken(got.Token)]
	_, rawStored := store.codes[got.Token]
	store.mu.Unlock()
	if !ok || code.userID != alice.ID || rawStored {
		t.Fatalf("stored code = %+v (found by hash: %v, by raw value: %v), want alice's, by hash only", code, ok, rawStored)
	}

	if rec := e.do(http.MethodPost, "/api/v1/auth/qr-code/generate", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a session: got %d, want 401", rec.Code)
	}
	// An API token reads and writes lists: it must never mint a session.
	apiToken := e.createToken(t, cookie, "Home Assistant")
	if rec := e.bearer(http.MethodPost, "/api/v1/auth/qr-code/generate", "", apiToken.Token); rec.Code != http.StatusForbidden {
		t.Fatalf("with an API token: got %d, want 403", rec.Code)
	}
}

func TestQRLoginOpensASessionOnce(t *testing.T) {
	e := newTokenTestEnv(t)
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	code := e.generateQRLogin(t, e.sessionCookie(t, alice))

	rec := e.redeemQRLogin(code.Token, nil)
	session := sessionCookieFrom(rec)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" || session == nil {
		t.Fatalf("redeeming: got %d to %q (session cookie: %v): %s, want a session and a redirect to /",
			rec.Code, rec.Header().Get("Location"), session != nil, rec.Body.String())
	}
	if !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie %+v: want HttpOnly, SameSite=Lax", session)
	}

	// The new session is alice's.
	me := e.do(http.MethodGet, "/api/v1/me", "", "", &http.Cookie{Name: auth.SessionCookieName, Value: session.Value})
	var user models.User
	decodeInto(t, me, http.StatusOK, &user)
	if user.ID != alice.ID {
		t.Fatalf("the QR code's session belongs to user %d, want alice (%d)", user.ID, alice.ID)
	}

	// Single use.
	assertQRLoginRefused(t, e.redeemQRLogin(code.Token, nil))
}

func TestQRLoginRefusesExpiredCodes(t *testing.T) {
	e := newTokenTestEnv(t)
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	code := e.generateQRLogin(t, e.sessionCookie(t, alice))

	store := e.app.qrLogins()
	hash := auth.HashToken(code.Token)
	store.mu.Lock()
	entry := store.codes[hash]
	entry.expires = time.Now().Add(-time.Second)
	store.codes[hash] = entry
	store.mu.Unlock()

	assertQRLoginRefused(t, e.redeemQRLogin(code.Token, nil))
	store.mu.Lock()
	_, kept := store.codes[hash]
	store.mu.Unlock()
	if kept {
		t.Fatal("an expired code must be removed once tried")
	}
}

func TestQRLoginRefusesUnknownCodes(t *testing.T) {
	e := newTokenTestEnv(t)
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	code := e.generateQRLogin(t, e.sessionCookie(t, alice))

	for name, token := range map[string]string{
		"missing":  "",
		"unknown":  strings.Repeat("A", 43),
		"too long": strings.Repeat("A", maxQRLoginCodeLength+1),
		// The hash, as stored, is not a code.
		"hash": auth.HashToken(code.Token),
	} {
		t.Run(name, func(t *testing.T) {
			assertQRLoginRefused(t, e.redeemQRLogin(token, nil))
		})
	}

	// None of those consumed alice's code.
	if rec := e.redeemQRLogin(code.Token, nil); rec.Code != http.StatusFound {
		t.Fatalf("alice's code after the failed attempts: got %d, want 302", rec.Code)
	}
}

func TestQRLoginNewCodeReplacesThePreviousOne(t *testing.T) {
	e := newTokenTestEnv(t)
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	bob := mustCreateTestUser(t, e.app, "bob@example.com")
	cookie := e.sessionCookie(t, alice)

	first := e.generateQRLogin(t, cookie)
	bobs := e.generateQRLogin(t, e.sessionCookie(t, bob))
	second := e.generateQRLogin(t, cookie)

	assertQRLoginRefused(t, e.redeemQRLogin(first.Token, nil))
	for name, token := range map[string]string{"alice's new code": second.Token, "bob's code": bobs.Token} {
		if rec := e.redeemQRLogin(token, nil); rec.Code != http.StatusFound {
			t.Fatalf("%s: got %d, want 302", name, rec.Code)
		}
	}
}

func TestQRLoginRejectsCrossSitePosts(t *testing.T) {
	e := newTokenTestEnv(t)
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	code := e.generateQRLogin(t, e.sessionCookie(t, alice))

	// Another site posting a code of its own would sign the visitor's
	// browser into the attacker's account.
	for name, headers := range map[string]map[string]string{
		"foreign origin":    {"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"},
		"opaque cross-site": {"Origin": "null", "Sec-Fetch-Site": "cross-site"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := e.redeemQRLogin(code.Token, headers)
			if rec.Code != http.StatusForbidden || sessionCookieFrom(rec) != nil {
				t.Fatalf("got %d, want 403 and no session", rec.Code)
			}
		})
	}
	// Rejected before the handler: the code is still there.
	if rec := e.redeemQRLogin(code.Token, nil); rec.Code != http.StatusFound {
		t.Fatalf("redeeming from the app after the rejected posts: got %d, want 302", rec.Code)
	}
}

func TestQRLoginRefusesADeletedAccount(t *testing.T) {
	e := newTokenTestEnv(t)
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	code := e.generateQRLogin(t, e.sessionCookie(t, alice))
	if err := e.app.DB.DeleteUser(context.Background(), alice.ID); err != nil {
		t.Fatal(err)
	}
	assertQRLoginRefused(t, e.redeemQRLogin(code.Token, nil))
}

func TestQRLoginIsRateLimited(t *testing.T) {
	e := newTokenTestEnv(t)
	for i := 0; i < authRateMaxPerIP; i++ {
		e.redeemQRLogin(strings.Repeat("A", 43), nil)
	}
	rec := e.redeemQRLogin(strings.Repeat("A", 43), nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d attempts: got %d, want 429", authRateMaxPerIP, rec.Code)
	}
}

func TestQRLoginCleanupRemovesOnlyExpiredCodes(t *testing.T) {
	e := newTokenTestEnv(t)
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	bob := mustCreateTestUser(t, e.app, "bob@example.com")
	stale := e.generateQRLogin(t, e.sessionCookie(t, alice))
	live := e.generateQRLogin(t, e.sessionCookie(t, bob))

	store := e.app.qrLogins()
	hash := auth.HashToken(stale.Token)
	store.mu.Lock()
	entry := store.codes[hash]
	entry.expires = time.Now().Add(-time.Second)
	store.codes[hash] = entry
	store.mu.Unlock()

	if n := e.app.RunQRLoginCleanup(); n != 1 {
		t.Fatalf("cleanup removed %d codes, want 1", n)
	}
	store.mu.Lock()
	_, staleKept := store.codes[hash]
	_, aliceKept := store.byUser[alice.ID]
	store.mu.Unlock()
	if staleKept || aliceKept {
		t.Fatalf("expired code kept (by hash: %v, by user: %v)", staleKept, aliceKept)
	}
	if rec := e.redeemQRLogin(live.Token, nil); rec.Code != http.StatusFound {
		t.Fatalf("live code after the cleanup: got %d, want 302", rec.Code)
	}
}

// The response carries the code once, and like every API response must not
// be cached.
func TestQRLoginGenerateResponseIsNotCacheable(t *testing.T) {
	e := newTokenTestEnv(t)
	alice := mustCreateTestUser(t, e.app, "alice@example.com")
	rec := e.do(http.MethodPost, "/api/v1/auth/qr-code/generate", "", "", e.sessionCookie(t, alice))
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"token", "expires_at", "expires_in"} {
		if _, ok := body[field]; !ok {
			t.Fatalf("response %s lacks %q", rec.Body.String(), field)
		}
	}
}
