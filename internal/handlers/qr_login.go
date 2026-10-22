package handlers

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"trakka/internal/auth"
	"trakka/internal/db"
)

// Signing Trakka's Android app in by QR code (android/, docs/MOBILE_BUILD.md).
//
//  1. Someone signed in to the PWA (on a computer, typically) opens
//     Paramètres → Intégrations → "Connecter l'application mobile via QR
//     Code" (static/js/qr-login.js), which asks POST
//     /api/v1/auth/qr-code/generate for a code and shows it as a QR code of
//     <origin>/#trakka-qr-login=<code>.
//  2. The app scans it on its connect or sign-in screen (android/www/
//     connect.js), opens that server, and its WebView POSTs the code to
//     /auth/qr-code/login (handleQRLoginRedeem), which opens a session
//     there — the way /auth/oidc/app-session ends an SSO sign-in.
//
// The code opens a whole session, so:
//
//   - it lives qrLoginTTL, is kept only as HashToken's hash, in memory like
//     appHandoffStore, and is consumed by the first attempt to redeem it,
//     whether or not that attempt succeeds;
//   - a user holds one at most: generating another (the modal's
//     "Régénérer") drops the previous one, which also bounds the store;
//   - only a session generates one, never an API token (the route is not on
//     apiTokenRoutes, and the handler refuses one anyway): a token limited
//     to lists and items must not turn into a session;
//   - it rides in the URL's fragment, which browsers never send: a phone's
//     camera app that opens the URL leaves it out of every log;
//   - redeeming it goes through requireSameOriginWrite, so another site
//     cannot post its own code to sign a victim's browser into the
//     attacker's account (login CSRF) — the WebView's native postUrl
//     (Origin: null, Sec-Fetch-Site: none) passes —, and through the
//     per-IP authentication rate limit.
//
// The redeeming route is under /auth/, not /api/v1/: it opens a session,
// and every /api/v1/... route requires one (CLAUDE.md, security rules).

const (
	// qrLoginTTL is how long a code stays valid: the time to pick up the
	// phone and scan it. The modal counts it down.
	qrLoginTTL = 3 * time.Minute
	// maxQRLoginCodeLength rejects absurd form values before hashing: a real
	// code is 43 characters.
	maxQRLoginCodeLength = 128
)

// qrLoginResponse is POST /api/v1/auth/qr-code/generate's body. ExpiresIn
// is what the modal counts down from, so a computer whose clock is off still
// shows the right time left.
type qrLoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	ExpiresIn int       `json:"expires_in"`
}

type qrLoginCode struct {
	userID  int64
	expires time.Time
}

// qrLoginStore holds the codes waiting to be scanned, by hash, with each
// user's current one.
type qrLoginStore struct {
	mu     sync.Mutex
	codes  map[string]qrLoginCode
	byUser map[int64]string
}

func newQRLoginStore() *qrLoginStore {
	return &qrLoginStore{codes: make(map[string]qrLoginCode), byUser: make(map[int64]string)}
}

// put makes codeHash the user's code, replacing any previous one.
func (s *qrLoginStore) put(codeHash string, code qrLoginCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.byUser[code.userID]; ok {
		delete(s.codes, previous)
	}
	s.codes[codeHash] = code
	s.byUser[code.userID] = codeHash
}

// take removes a code and returns its user: a code is redeemed at most
// once, and an expired one is gone all the same.
func (s *qrLoginStore) take(codeHash string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code, ok := s.codes[codeHash]
	if !ok {
		return 0, false
	}
	s.removeLocked(codeHash, code.userID)
	if !time.Now().Before(code.expires) {
		return 0, false
	}
	return code.userID, true
}

// prune removes the codes expired at now, and returns how many.
func (s *qrLoginStore) prune(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for hash, code := range s.codes {
		if !now.Before(code.expires) {
			s.removeLocked(hash, code.userID)
			removed++
		}
	}
	return removed
}

func (s *qrLoginStore) removeLocked(codeHash string, userID int64) {
	delete(s.codes, codeHash)
	if s.byUser[userID] == codeHash {
		delete(s.byUser, userID)
	}
}

func (app *Application) qrLogins() *qrLoginStore {
	app.qrLoginsOnce.Do(func() { app.qrLoginsVal = newQRLoginStore() })
	return app.qrLoginsVal
}

// RunQRLoginCleanup drops the QR codes that expired unscanned, so the store
// only ever holds live ones (cmd/server's runQRLoginCleanupLoop). Returns
// how many were removed.
func (app *Application) RunQRLoginCleanup() int {
	return app.qrLogins().prune(time.Now())
}

// handleQRLoginGenerate issues the caller a new QR sign-in code.
func (app *Application) handleQRLoginGenerate(w http.ResponseWriter, r *http.Request) {
	if _, isToken := bearerAPIToken(r); isToken {
		writeError(w, http.StatusForbidden, "API tokens cannot sign a device in")
		return
	}
	user := userFromContext(r)
	code, err := auth.RandomToken(32)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	expires := time.Now().Add(qrLoginTTL)
	app.qrLogins().put(auth.HashToken(code), qrLoginCode{userID: user.ID, expires: expires})
	writeJSON(w, http.StatusCreated, qrLoginResponse{
		Token:     code,
		ExpiresAt: expires.UTC(),
		ExpiresIn: int(qrLoginTTL / time.Second),
	})
}

// handleQRLoginRedeem opens a session for a scanned code, from the app's
// WebView: a redirect to / with the session cookie, or a 401 the app shows
// on its sign-in screen (MainActivity).
func (app *Application) handleQRLoginRedeem(w http.ResponseWriter, r *http.Request) {
	if !app.allowAuthAttempt(r, "") {
		writeError(w, http.StatusTooManyRequests, "too many sign-in attempts")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}
	code := r.PostFormValue("token")
	if code == "" || len(code) > maxQRLoginCodeLength {
		writeError(w, http.StatusUnauthorized, "invalid, expired or already used QR code")
		return
	}
	userID, ok := app.qrLogins().take(auth.HashToken(code))
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid, expired or already used QR code")
		return
	}
	// The account may have been deleted since the code was generated.
	if _, err := app.DB.GetUser(r.Context(), userID); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "invalid, expired or already used QR code")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.Logger.Info("signed in by QR code", "user_id", userID, "ip", clientIP(r))
	app.finishLogin(w, r, userID)
}
