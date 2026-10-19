package handlers

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"trakka/internal/models"
)

// restartTestEnv is a tokenTestEnv whose RequestRestart reports each call on
// restarts instead of stopping anything.
type restartTestEnv struct {
	*tokenTestEnv
	restarts chan struct{}
	admin    *models.User
	cookie   *http.Cookie
}

func newRestartTestEnv(t *testing.T) *restartTestEnv {
	t.Helper()
	e := &restartTestEnv{tokenTestEnv: newTokenTestEnv(t), restarts: make(chan struct{}, 10)}
	e.app.RequestRestart = func() { e.restarts <- struct{}{} }
	e.admin = mustCreateTestUser(t, e.app, "admin@example.com") // the first account is an admin
	if !e.admin.IsAdmin {
		t.Fatal("test setup: the first user should be an admin")
	}
	e.cookie = e.sessionCookie(t, e.admin)
	return e
}

// send goes through the router like the console does: session cookie (may
// be nil), and the action header unless withHeader is false.
func (e *restartTestEnv) send(method, target, body string, cookie *http.Cookie, withHeader bool, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if withHeader {
		req.Header.Set(restartActionHeader, restartActionValue)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *restartTestEnv) challenge(t *testing.T, cookie *http.Cookie) string {
	t.Helper()
	got := decodeAs[restartChallengeResponse](t, e.send(http.MethodGet, "/api/v1/admin/restart-challenge", "", cookie, true, ""), http.StatusOK)
	if got.Challenge == "" || got.ExpiresIn != int(restartChallengeTTL/time.Second) {
		t.Fatalf("unexpected challenge response: %+v", got)
	}
	return got.Challenge
}

func (e *restartTestEnv) restart(cookie *http.Cookie, challenge string) *httptest.ResponseRecorder {
	return e.send(http.MethodPost, "/api/v1/admin/restart", `{"challenge":`+strconv.Quote(challenge)+`}`, cookie, true, "")
}

// expectNoRestart fails if RequestRestart was (or gets) called.
func (e *restartTestEnv) expectNoRestart(t *testing.T) {
	t.Helper()
	select {
	case <-e.restarts:
		t.Fatal("RequestRestart was called")
	case <-time.After(2 * restartDelay):
	}
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
}

func TestAdminRestartSucceedsWithSessionAndChallenge(t *testing.T) {
	e := newRestartTestEnv(t)

	rec := e.restart(e.cookie, e.challenge(t, e.cookie))
	got := decodeAs[map[string]string](t, rec, http.StatusOK)
	if got["message"] != "Server restarting..." {
		t.Fatalf("unexpected body: %v", got)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	select {
	case <-e.restarts:
	case <-time.After(5 * time.Second):
		t.Fatal("RequestRestart was never called")
	}

	// A second, fully valid request while the first is under way.
	wantStatus(t, e.restart(e.cookie, e.challenge(t, e.cookie)), http.StatusConflict)
	e.expectNoRestart(t)
}

// TestAdminRestartRejectsAPITokens: an admin's own token is refused on both
// routes, by the token gate and by the handler itself, cookie or not.
func TestAdminRestartRejectsAPITokens(t *testing.T) {
	e := newRestartTestEnv(t)
	token := e.createToken(t, e.cookie, "HA").Token
	challenge := e.challenge(t, e.cookie)
	body := `{"challenge":` + strconv.Quote(challenge) + `}`

	wantStatus(t, e.send(http.MethodGet, "/api/v1/admin/restart-challenge", "", nil, true, "Bearer "+token), http.StatusForbidden)
	wantStatus(t, e.send(http.MethodPost, "/api/v1/admin/restart", body, nil, true, "Bearer "+token), http.StatusForbidden)
	// A valid cookie riding along changes nothing: the caller chose the token.
	wantStatus(t, e.send(http.MethodPost, "/api/v1/admin/restart", body, e.cookie, true, "Bearer "+token), http.StatusForbidden)

	// The handler's own check, should apiTokenRoutes ever let one through.
	for _, call := range []struct {
		method  string
		handler http.HandlerFunc
	}{
		{http.MethodGet, e.app.handleAdminRestartChallenge},
		{http.MethodPost, e.app.handleAdminRestart},
	} {
		req := withUser(httptest.NewRequest(call.method, "/api/v1/admin/restart", strings.NewReader(body)), e.admin)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(restartActionHeader, restartActionValue)
		req.AddCookie(e.cookie)
		rec := httptest.NewRecorder()
		call.handler(rec, req)
		wantStatus(t, rec, http.StatusForbidden)
		if !strings.Contains(rec.Body.String(), "API tokens") {
			t.Fatalf("expected the handler's own token refusal, got %s", rec.Body.String())
		}
	}

	// The challenge was never consumed by any of the refused requests.
	wantStatus(t, e.restart(e.cookie, challenge), http.StatusOK)
}

func TestAdminRestartRequiresActionHeader(t *testing.T) {
	e := newRestartTestEnv(t)

	// Without the header, the GET mints nothing (a plain navigation).
	wantStatus(t, e.send(http.MethodGet, "/api/v1/admin/restart-challenge", "", e.cookie, false, ""), http.StatusForbidden)

	challenge := e.challenge(t, e.cookie)
	body := `{"challenge":` + strconv.Quote(challenge) + `}`
	wantStatus(t, e.send(http.MethodPost, "/api/v1/admin/restart", body, e.cookie, false, ""), http.StatusForbidden)

	// A wrong value is no better than none.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/restart", strings.NewReader(body))
	req.Header.Set(restartActionHeader, "restart")
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	wantStatus(t, rec, http.StatusForbidden)
	e.expectNoRestart(t)
}

func TestAdminRestartRequiresValidChallenge(t *testing.T) {
	e := newRestartTestEnv(t)

	t.Run("no challenge was ever minted", func(t *testing.T) {
		wantStatus(t, e.restart(e.cookie, "made-up"), http.StatusForbidden)
	})
	t.Run("empty or missing challenge", func(t *testing.T) {
		e.challenge(t, e.cookie)
		wantStatus(t, e.restart(e.cookie, ""), http.StatusForbidden)
		e.challenge(t, e.cookie)
		wantStatus(t, e.send(http.MethodPost, "/api/v1/admin/restart", `{}`, e.cookie, true, ""), http.StatusForbidden)
	})
	t.Run("a wrong guess burns the challenge", func(t *testing.T) {
		challenge := e.challenge(t, e.cookie)
		wantStatus(t, e.restart(e.cookie, challenge+"x"), http.StatusForbidden)
		wantStatus(t, e.restart(e.cookie, challenge), http.StatusForbidden)
	})
	t.Run("only the latest challenge counts", func(t *testing.T) {
		first := e.challenge(t, e.cookie)
		e.challenge(t, e.cookie)
		wantStatus(t, e.restart(e.cookie, first), http.StatusForbidden)
	})
	t.Run("bound to the session it was minted for", func(t *testing.T) {
		other := e.sessionCookie(t, e.admin) // same admin, another browser
		challenge := e.challenge(t, other)
		wantStatus(t, e.restart(e.cookie, challenge), http.StatusForbidden)
	})
	t.Run("expired", func(t *testing.T) {
		challenge := e.challenge(t, e.cookie)
		store := e.app.restartChallenges()
		store.mu.Lock()
		for hash, entry := range store.bySession {
			entry.expires = time.Now().Add(-time.Second)
			store.bySession[hash] = entry
		}
		store.mu.Unlock()
		wantStatus(t, e.restart(e.cookie, challenge), http.StatusForbidden)
	})
	t.Run("unknown fields are rejected", func(t *testing.T) {
		challenge := e.challenge(t, e.cookie)
		wantStatus(t, e.send(http.MethodPost, "/api/v1/admin/restart", `{"challenge":"`+challenge+`","force":true}`, e.cookie, true, ""), http.StatusBadRequest)
	})
	e.expectNoRestart(t)

	t.Run("replay of a used challenge", func(t *testing.T) {
		challenge := e.challenge(t, e.cookie)
		wantStatus(t, e.restart(e.cookie, challenge), http.StatusOK)
		<-e.restarts
		e.app.restarting.Store(false) // as after a real restart: a fresh process
		wantStatus(t, e.restart(e.cookie, challenge), http.StatusForbidden)
		e.expectNoRestart(t)
	})
}

func TestAdminRestartRequiresAdminSession(t *testing.T) {
	e := newRestartTestEnv(t)
	member := mustCreateTestUser(t, e.app, "member@example.com")
	if member.IsAdmin {
		t.Fatal("test setup: the second user should not be an admin")
	}
	memberCookie := e.sessionCookie(t, member)

	wantStatus(t, e.send(http.MethodGet, "/api/v1/admin/restart-challenge", "", memberCookie, true, ""), http.StatusForbidden)
	wantStatus(t, e.restart(memberCookie, "anything"), http.StatusForbidden)
	// An admin's challenge doesn't open the route to someone else's session.
	wantStatus(t, e.restart(memberCookie, e.challenge(t, e.cookie)), http.StatusForbidden)

	wantStatus(t, e.send(http.MethodGet, "/api/v1/admin/restart-challenge", "", nil, true, ""), http.StatusUnauthorized)
	wantStatus(t, e.restart(nil, "anything"), http.StatusUnauthorized)

	// A cross-site POST is stopped by the CSRF layer before anything else.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/restart", strings.NewReader(`{"challenge":"x"}`))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set(restartActionHeader, restartActionValue)
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	wantStatus(t, rec, http.StatusForbidden)
	e.expectNoRestart(t)
}

func TestAdminRestartUnavailableWithoutRequestRestart(t *testing.T) {
	e := newRestartTestEnv(t)
	e.app.RequestRestart = nil
	wantStatus(t, e.send(http.MethodGet, "/api/v1/admin/restart-challenge", "", e.cookie, true, ""), http.StatusNotFound)
	wantStatus(t, e.restart(e.cookie, "anything"), http.StatusNotFound)
}
