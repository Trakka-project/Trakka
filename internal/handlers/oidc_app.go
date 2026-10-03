package handlers

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"sync"
	"time"

	"trakka/internal/auth"
)

// SSO sign-in from Trakka's Android app (android/, docs/MOBILE_BUILD.md).
//
// The app shows Trakka in a WebView, and an Android WebView cannot run
// WebAuthn for a site that isn't tied to the app through Digital Asset Links
// — which no identity provider an instance happens to use (Authentik,
// Keycloak...) ever is. A provider that asks for a passkey or a security key
// then fails inside the app ("Error creating credential"). So the app runs
// the whole OIDC flow in the phone's browser (a Custom Tab), and this file
// brings the result back into the app's WebView:
//
//  1. The app makes a secret verifier and opens
//     /auth/oidc/login?app_challenge=<base64url(SHA-256(verifier))> in the
//     browser. handleOIDCLogin keeps the challenge in its flow cookie.
//  2. Once the provider sends the browser back, handleOIDCCallback signs no
//     one in there: it stores a single-use handoff code bound to the user and
//     the challenge, and redirects to appSSOReturnURL?code=<code>, which
//     Android hands to the app.
//  3. The app POSTs code + verifier to /auth/oidc/app-session from its
//     WebView (handleOIDCAppSession), which opens the session there.
//
// The custom scheme is not exclusive: any app can declare it and receive the
// code. That code alone is useless, though: redeeming it takes the verifier,
// which never leaves the Trakka app (the same reasoning as PKCE, RFC 7636).
// The redeeming POST also goes through requireSameOriginWrite, so another
// site cannot sign a victim's browser into an account whose code and
// verifier it obtained itself (login CSRF).

// appSSOReturnURL is the deep link the Android app declares
// (android/native/app/src/main/AndroidManifest.xml), named after the app's
// own reverse domain as RFC 8252 section 7.1 recommends.
const appSSOReturnURL = "io.github.trakka-project.app://sso"

const (
	// The app redeems the code right after the browser hands it over; the
	// margin only covers a slow phone.
	appHandoffTTL = 2 * time.Minute
	// Bounds the memory a flood of completed sign-ins that are never
	// redeemed could take. Each one requires a real sign-in at the provider.
	maxPendingAppHandoffs = 1000
)

type appHandoff struct {
	userID    int64
	challenge string
	expires   time.Time
}

// appHandoffStore holds the handoff codes waiting for the app, by hash. In
// memory, like the rate limiters: Trakka runs as a single process, and a
// restart only costs a sign-in in progress.
type appHandoffStore struct {
	mu      sync.Mutex
	entries map[string]appHandoff
}

func newAppHandoffStore() *appHandoffStore {
	return &appHandoffStore{entries: make(map[string]appHandoff)}
}

// put stores a handoff, and reports false when too many are already waiting.
func (s *appHandoffStore) put(codeHash string, handoff appHandoff) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for hash, entry := range s.entries {
		if now.After(entry.expires) {
			delete(s.entries, hash)
		}
	}
	if len(s.entries) >= maxPendingAppHandoffs {
		return false
	}
	s.entries[codeHash] = handoff
	return true
}

// take removes and returns a handoff: a code is redeemed at most once,
// whether or not the verifier that came with it matches.
func (s *appHandoffStore) take(codeHash string) (appHandoff, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	handoff, ok := s.entries[codeHash]
	delete(s.entries, codeHash)
	if !ok || time.Now().After(handoff.expires) {
		return appHandoff{}, false
	}
	return handoff, true
}

func (app *Application) appHandoffs() *appHandoffStore {
	app.appHandoffsOnce.Do(func() { app.appHandoffsVal = newAppHandoffStore() })
	return app.appHandoffsVal
}

// validAppChallenge reports whether s has the shape of an S256 challenge:
// the unpadded base64url encoding of a SHA-256 digest.
func validAppChallenge(s string) bool {
	if len(s) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(decoded) == sha256.Size
}

// appSSOReturn redirects the browser back to the app, with either a handoff
// code or an error code for the app to show on Trakka's sign-in page.
func appSSOReturn(w http.ResponseWriter, r *http.Request, params url.Values) {
	// The URL carries a credential: no cache may keep this response.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, appSSOReturnURL+"?"+params.Encode(), http.StatusFound)
}

// handOffToApp ends handleOIDCCallback for a sign-in started by the app.
func (app *Application) handOffToApp(w http.ResponseWriter, r *http.Request, userID int64, challenge string) {
	code, err := auth.RandomToken(32)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	stored := app.appHandoffs().put(auth.HashToken(code), appHandoff{
		userID:    userID,
		challenge: challenge,
		expires:   time.Now().Add(appHandoffTTL),
	})
	if !stored {
		app.Logger.Warn("too many pending app sign-ins; refusing a new one")
		appSSOReturn(w, r, url.Values{"error": {"oidc_failed"}})
		return
	}
	appSSOReturn(w, r, url.Values{"code": {code}})
}

// handleOIDCAppSession redeems a handoff code, from the app's WebView.
func (app *Application) handleOIDCAppSession(w http.ResponseWriter, r *http.Request) {
	if app.Auth.OIDC() == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/auth/login?error=bad_request", http.StatusFound)
		return
	}

	handoff, ok := app.appHandoffs().take(auth.HashToken(r.PostFormValue("code")))
	sum := sha256.Sum256([]byte(r.PostFormValue("verifier")))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if !ok || subtle.ConstantTimeCompare([]byte(challenge), []byte(handoff.challenge)) != 1 {
		http.Redirect(w, r, "/auth/login?error=oidc_failed", http.StatusFound)
		return
	}
	app.finishLogin(w, r, handoff.userID)
}
