package handlers

import (
	"crypto/subtle"
	"net/http"
	"sync"
	"time"

	"trakka/internal/auth"
)

// Restarting the server from the admin console (Paramètres →
// Administration). The restart itself is cmd/server's job: the handler only
// calls Application.RequestRestart, which shuts the HTTP server down
// gracefully and exits with a code a supervisor (Docker/Podman restart
// policy, systemd) answers by starting the process again.
//
// Every check below is on top of RequireSession's and the CSRF layer's, and
// is there so that a restart can only come from the console itself, in a
// signed-in admin's browser:
//
//   - API tokens are refused outright. apiTokenRoutes already keeps tokens
//     off these routes; the handler checks again so that widening that list
//     by mistake still can't open a restart to an integration.
//   - X-Trakka-Admin-Action: confirm-restart must be present, on both
//     routes. A custom header can't ride on a navigation, a form post or an
//     <img>, and a cross-site fetch() carrying one needs a CORS preflight
//     this server never grants — and it makes a copied "curl the URL" fail.
//   - The POST must carry a challenge minted by the GET for the same
//     session, at most restartChallengeTTL ago, and each challenge is
//     consumed by the first POST that presents it, valid or not. A replayed
//     request, or one built from a challenge read on another session, is
//     refused.
//
// Requiring the header on the GET too is what keeps it from changing
// anything on a plain navigation: no GET route in this app may change state
// on one (see csrf.go), and minting a challenge replaces the session's
// pending one.
const (
	restartActionHeader = "X-Trakka-Admin-Action"
	restartActionValue  = "confirm-restart"

	// restartChallengeTTL is how long a challenge stays valid: the console
	// asks for one right when the admin confirms, so a minute is plenty.
	restartChallengeTTL = time.Minute
	// maxPendingRestartChallenges bounds the store: one entry per admin
	// session at most, and admins are few.
	maxPendingRestartChallenges = 100
	// restartDelay leaves time for the 200 to reach the console before
	// RequestRestart starts the shutdown (which itself still waits for
	// requests in flight).
	restartDelay = 500 * time.Millisecond
)

// restartChallengeResponse is GET /api/v1/admin/restart-challenge's body.
type restartChallengeResponse struct {
	Challenge string `json:"challenge"`
	ExpiresIn int    `json:"expires_in"`
}

// restartRequest is POST /api/v1/admin/restart's body.
type restartRequest struct {
	Challenge string `json:"challenge"`
}

type restartChallenge struct {
	userID  int64
	hash    string
	expires time.Time
}

// restartChallengeStore holds pending challenges by the hash of the session
// they were minted for. In memory, like appHandoffStore: a restart clears it
// anyway.
type restartChallengeStore struct {
	mu        sync.Mutex
	bySession map[string]restartChallenge
}

func newRestartChallengeStore() *restartChallengeStore {
	return &restartChallengeStore{bySession: make(map[string]restartChallenge)}
}

// put replaces the session's pending challenge, and reports false when too
// many other sessions already hold one.
func (s *restartChallengeStore) put(sessionHash string, challenge restartChallenge) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for hash, entry := range s.bySession {
		if now.After(entry.expires) {
			delete(s.bySession, hash)
		}
	}
	if _, exists := s.bySession[sessionHash]; !exists && len(s.bySession) >= maxPendingRestartChallenges {
		return false
	}
	s.bySession[sessionHash] = challenge
	return true
}

// take removes and returns the session's pending challenge, if still valid.
func (s *restartChallengeStore) take(sessionHash string) (restartChallenge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	challenge, ok := s.bySession[sessionHash]
	delete(s.bySession, sessionHash)
	if !ok || time.Now().After(challenge.expires) {
		return restartChallenge{}, false
	}
	return challenge, true
}

func (app *Application) restartChallenges() *restartChallengeStore {
	app.restartChallengesOnce.Do(func() { app.restartChallengesVal = newRestartChallengeStore() })
	return app.restartChallengesVal
}

// authorizeRestart runs the checks both restart routes share, in order: no
// API token, an admin, the feature wired up, the action header, and a
// session cookie (always there past RequireSession without a token). It
// returns the hash of that session, which challenges are bound to.
func (app *Application) authorizeRestart(w http.ResponseWriter, r *http.Request) (sessionHash string, ok bool) {
	if _, isToken := bearerAPIToken(r); isToken {
		writeError(w, http.StatusForbidden, "API tokens cannot restart the server")
		return "", false
	}
	if !app.authorizeAdmin(w, r) {
		return "", false
	}
	if app.RequestRestart == nil {
		writeError(w, http.StatusNotFound, "server restart is not available")
		return "", false
	}
	if r.Header.Get(restartActionHeader) != restartActionValue {
		writeError(w, http.StatusForbidden, "missing "+restartActionHeader+" header")
		return "", false
	}
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusForbidden, "a browser session is required")
		return "", false
	}
	return auth.HashToken(cookie.Value), true
}

// handleAdminRestartChallenge mints the single-use challenge the next
// POST /api/v1/admin/restart from this session must carry.
func (app *Application) handleAdminRestartChallenge(w http.ResponseWriter, r *http.Request) {
	sessionHash, ok := app.authorizeRestart(w, r)
	if !ok {
		return
	}
	challenge, err := auth.RandomToken(32)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	stored := app.restartChallenges().put(sessionHash, restartChallenge{
		userID:  userFromContext(r).ID,
		hash:    auth.HashToken(challenge),
		expires: time.Now().Add(restartChallengeTTL),
	})
	if !stored {
		writeError(w, http.StatusTooManyRequests, "too many pending restart requests")
		return
	}
	writeJSON(w, http.StatusOK, restartChallengeResponse{
		Challenge: challenge,
		ExpiresIn: int(restartChallengeTTL / time.Second),
	})
}

// handleAdminRestart answers 200 and then, after restartDelay, asks
// cmd/server to restart. A second request while one is under way is a 409.
func (app *Application) handleAdminRestart(w http.ResponseWriter, r *http.Request) {
	sessionHash, ok := app.authorizeRestart(w, r)
	if !ok {
		return
	}
	var req restartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user := userFromContext(r)
	// Taken before comparing: a wrong guess burns the challenge too.
	pending, found := app.restartChallenges().take(sessionHash)
	if !found || req.Challenge == "" || pending.userID != user.ID ||
		subtle.ConstantTimeCompare([]byte(pending.hash), []byte(auth.HashToken(req.Challenge))) != 1 {
		writeError(w, http.StatusForbidden, "invalid or expired restart challenge")
		return
	}
	if !app.restarting.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "a restart is already in progress")
		return
	}
	app.Logger.Warn("server restart requested from the admin console", "user_id", user.ID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "Server restarting..."})
	time.AfterFunc(restartDelay, app.RequestRestart)
}
