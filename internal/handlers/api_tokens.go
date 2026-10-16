package handlers

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"trakka/internal/auth"
	"trakka/internal/db"
	"trakka/internal/validate"
)

// Personal API tokens (docs/HOME_ASSISTANT.md, docs/API.md#api-tokens):
// each user can create named tokens for integrations that call the JSON API
// with no browser session — the Home Assistant integration
// (custom_components/trakka) first. A token is sent as
// "Authorization: Bearer trk_..." and RequireSession resolves it to its
// user, so every handler applies the same RBAC as for that user's session;
// it is not a route outside the session gate. The token is 32 crypto/rand
// bytes behind auth.APITokenPrefix, stored only as its SHA-256 hash
// (auth.HashToken, like a session token), so it is shown once, right after
// it is created; revoking it deletes the row.
//
// A token opens only apiTokenRoutes, whatever its user may do with a
// session: reading lists, and reading and writing their items. Everything
// else — the account, houses, sharing, admin, push, the calendar feed and
// these token routes themselves (a leaked token can't mint another, or
// outlive its own revocation) — answers 403 to a token. Creating, listing
// and revoking tokens needs a session.
//
// CSRF doesn't apply to a token: a browser never attaches an Authorization
// header on its own, so a cross-site page can't make a victim's browser
// send one. requireSameOriginWrite still runs (Home Assistant's aiohttp
// sends neither Origin nor Sec-Fetch-Site, which it lets through).

// apiTokenRoutes are the only /api/v1/... routes an API token opens. Each
// must be spelled exactly as registered in Routes() (TestAPITokenRoutesExist
// checks it).
var apiTokenRoutes = []string{
	"GET /api/v1/me",
	"GET /api/v1/config",
	"GET /api/v1/lists",
	"GET /api/v1/lists/{id}",
	"PUT /api/v1/lists/{id}/reorder",
	"GET /api/v1/items",
	"POST /api/v1/items",
	"GET /api/v1/items/{id}",
	"PUT /api/v1/items/{id}",
	"PATCH /api/v1/items/{id}",
	"DELETE /api/v1/items/{id}",
}

// apiTokenRouteMux matches a request against apiTokenRoutes with the same
// rules as the real router. Its handlers never run: only the pattern it
// picks is read (see apiTokenAllows).
var apiTokenRouteMux = func() *http.ServeMux {
	mux := http.NewServeMux()
	for _, pattern := range apiTokenRoutes {
		mux.Handle(pattern, http.NotFoundHandler())
	}
	return mux
}()

// apiTokenAllows reports whether ServeMux would route r to one of
// apiTokenRoutes — path cleaned first, so "/api/v1/lists/../admin/users"
// is the admin route it leads to. The matched pattern is compared with the
// list rather than only checked for non-empty, since for a trailing-slash
// redirect ServeMux reports the redirect's path instead of a pattern.
func apiTokenAllows(r *http.Request) bool {
	_, pattern := apiTokenRouteMux.Handler(r)
	return slices.Contains(apiTokenRoutes, pattern)
}

// bearerAPIToken returns the token of an "Authorization: Bearer trk_..."
// header. ok is false with no Authorization header, another scheme (Basic
// from a reverse proxy's own login), or a Bearer value that isn't a Trakka
// token (an id_token forwarded by oauth2-proxy): all of those fall through
// to the session cookie, so such a proxy in front of the PWA keeps working.
func bearerAPIToken(r *http.Request) (token string, ok bool) {
	scheme, value, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, auth.APITokenPrefix) {
		return "", false
	}
	return value, true
}

// serveWithAPIToken is RequireSession's path for a request carrying an API
// token: it authenticates the token, keeps it to apiTokenRoutes, and serves
// next as the token's user. A token that's present but unknown or revoked
// is a 401 even when a valid session cookie rides along: the caller chose
// the token.
func (app *Application) serveWithAPIToken(w http.ResponseWriter, r *http.Request, token string, next http.Handler) {
	user, err := app.Auth.ValidateAPIToken(r.Context(), token)
	if errors.Is(err, db.ErrNotFound) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="trakka", error="invalid_token"`)
		writeError(w, http.StatusUnauthorized, "invalid API token")
		return
	}
	if err != nil {
		// Not a 401: an integration answers that by asking its user for a
		// new token, which a database hiccup doesn't call for.
		app.serverError(w, r, err)
		return
	}
	if !apiTokenAllows(r) {
		writeError(w, http.StatusForbidden, "this endpoint is not available to API tokens")
		return
	}
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey, user)))
}

// maxAPITokensPerUser bounds how many tokens one account can hold: one per
// integration is the expected use, and each is a row checked on every
// token request.
const maxAPITokensPerUser = 20

// apiTokenResponse is the shape of GET/POST /api/v1/api-tokens. Token is
// only ever set in the POST response.
type apiTokenResponse struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Token      string  `json:"token,omitempty"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

func newAPITokenResponse(t *db.APIToken) apiTokenResponse {
	resp := apiTokenResponse{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt}
	if t.LastUsedAt != "" {
		resp.LastUsedAt = &t.LastUsedAt
	}
	return resp
}

// handleAPITokensIndex answers GET /api/v1/api-tokens: the caller's tokens,
// never the tokens themselves.
func (app *Application) handleAPITokensIndex(w http.ResponseWriter, r *http.Request) {
	tokens, err := app.DB.ListAPITokens(r.Context(), userFromContext(r).ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	resp := make([]apiTokenResponse, 0, len(tokens))
	for _, t := range tokens {
		resp = append(resp, newAPITokenResponse(t))
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAPITokensCreate answers POST /api/v1/api-tokens {"name": "..."}:
// creates a token and returns it — the only time it is ever returned.
func (app *Application) handleAPITokensCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	name := validate.Text(in.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !validate.MaxLen(name, validate.MaxNameLen) {
		writeError(w, http.StatusBadRequest, "name is too long")
		return
	}

	user := userFromContext(r)
	count, err := app.DB.CountAPITokens(r.Context(), user.ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if count >= maxAPITokensPerUser {
		writeError(w, http.StatusConflict, "too many API tokens: revoke one you no longer use first")
		return
	}

	token, err := auth.NewAPIToken()
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	t, err := app.DB.CreateAPIToken(r.Context(), user.ID, name, auth.HashToken(token))
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	resp := newAPITokenResponse(t)
	resp.Token = token
	writeJSON(w, http.StatusCreated, resp)
}

// handleAPITokensDelete answers DELETE /api/v1/api-tokens/{id}: the token
// stops working at once. Another user's token is a 404, like a missing one.
func (app *Application) handleAPITokensDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := app.DB.DeleteAPIToken(r.Context(), userFromContext(r).ID, id); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "API token not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
