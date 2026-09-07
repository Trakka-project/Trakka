// Package handlers implements the HTTP layer: routing, request
// validation, and translating db-layer results into JSON responses. It
// never touches database/sql directly — all persistence goes through
// internal/db.
package handlers

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"trakka/internal/auth"
	"trakka/internal/config"
	"trakka/internal/db"
	"trakka/internal/logbuffer"
)

// Application holds the dependencies shared by every handler.
type Application struct {
	DB            *db.DB
	StaticDir     string
	Logger        *slog.Logger
	Auth          *auth.Service
	LoginTemplate *template.Template
	// LogBuffer backs GET /api/v1/admin/logs (see admin_logs.go) — the
	// in-memory ring buffer of recent log records that cmd/server/main.go
	// wraps around Logger's own handler. May be nil (e.g. in tests that
	// build an Application by hand), in which case the logs endpoint
	// reports an empty list rather than panicking.
	LogBuffer *logbuffer.Handler
	// Config is the env-var configuration loaded at startup. Handlers read
	// it for values that stay env-only even after the admin settings panel
	// was added — currently just BaseURL, needed to build the OIDC
	// redirect_uri when the admin (re)enables OIDC at runtime (see
	// internal/handlers/admin.go) — and pass it to internal/settings.Resolve
	// as the fallback under whatever's stored in system_settings.
	Config config.Config

	// Authentication rate-limiter state (see ratelimit.go). Lazily built
	// through sync.Once rather than in a constructor, because Application is
	// built as a plain struct literal in cmd/server and in tests — a zero
	// value must stay usable.
	authIPLimiterOnce    sync.Once
	authIPLimiterVal     *rateLimiter
	authEmailLimiterOnce sync.Once
	authEmailLimiterVal  *rateLimiter
}

// authIPLimiter is the per-client-IP authentication attempt bucket.
func (app *Application) authIPLimiter() *rateLimiter {
	app.authIPLimiterOnce.Do(func() { app.authIPLimiterVal = newRateLimiter(authRateWindow) })
	return app.authIPLimiterVal
}

// authEmailLimiter is the per-account authentication attempt bucket.
func (app *Application) authEmailLimiter() *rateLimiter {
	app.authEmailLimiterOnce.Do(func() { app.authEmailLimiterVal = newRateLimiter(authRateWindow) })
	return app.authEmailLimiterVal
}

// Routes builds the full HTTP handler: middleware chain + route table.
//
// /api/v1/... is gated behind RequireSession as a whole (wrapped once,
// below) rather than per-route — every JSON API endpoint requires a valid
// session. /auth/... and static files stay unauthenticated: /auth/... is
// how a session gets established in the first place, and gating static
// files would break the service worker's cache-first app-shell strategy
// (the client-side redirect in app.js's apiRequest() handles the UX layer
// on top of the API's 401s).
func (app *Application) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", app.handleHealthz)

	mux.HandleFunc("GET /auth/login", app.handleLoginPage)
	mux.HandleFunc("POST /auth/login", app.handleLoginSubmit)
	mux.HandleFunc("POST /auth/register", app.handleRegisterSubmit)
	mux.HandleFunc("POST /auth/logout", app.handleLogout)
	mux.HandleFunc("GET /auth/oidc/login", app.handleOIDCLogin)
	mux.HandleFunc("GET /auth/oidc/callback", app.handleOIDCCallback)

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /api/v1/me", app.handleMe)
	apiMux.HandleFunc("PATCH /api/v1/me", app.handleMeUpdate)

	apiMux.HandleFunc("GET /api/v1/houses", app.handleHousesIndex)
	apiMux.HandleFunc("POST /api/v1/houses", app.handleHousesCreate)
	apiMux.HandleFunc("GET /api/v1/houses/{id}", app.handleHousesShow)
	apiMux.HandleFunc("PUT /api/v1/houses/{id}", app.handleHousesUpdate)
	apiMux.HandleFunc("DELETE /api/v1/houses/{id}", app.handleHousesDelete)
	apiMux.HandleFunc("GET /api/v1/houses/{id}/members", app.handleHouseMembersIndex)
	apiMux.HandleFunc("POST /api/v1/houses/{id}/members", app.handleHouseMembersInvite)
	apiMux.HandleFunc("DELETE /api/v1/houses/{id}/members/{userId}", app.handleHouseMembersRemove)
	// Withdrawing an invitation that has not been accepted yet. Keyed by
	// ?email= rather than a path segment, since a pending invitation has no
	// user id to name it by — that is precisely what makes it pending.
	apiMux.HandleFunc("DELETE /api/v1/houses/{id}/invitations", app.handleHouseInvitationRevoke)

	apiMux.HandleFunc("GET /api/v1/custom-categories", app.handleCustomCategoriesIndex)
	apiMux.HandleFunc("POST /api/v1/custom-categories", app.handleCustomCategoriesCreate)
	apiMux.HandleFunc("PUT /api/v1/custom-categories/{id}", app.handleCustomCategoriesUpdate)
	apiMux.HandleFunc("DELETE /api/v1/custom-categories/{id}", app.handleCustomCategoriesDelete)
	// "Space" is this app's user-facing name for a custom category (see
	// static/js/spaces.js's "Espaces" tab) — these routes live under the
	// existing /custom-categories/{id} resource, not a separate /spaces
	// root, so there's only ever one URL naming a given category.
	apiMux.HandleFunc("GET /api/v1/custom-categories/{id}/share", app.handleSpaceShareIndex)
	apiMux.HandleFunc("POST /api/v1/custom-categories/{id}/share", app.handleSpaceShareCreate)
	apiMux.HandleFunc("PATCH /api/v1/custom-categories/{id}/share/pin", app.handleSpaceSharePin)
	apiMux.HandleFunc("DELETE /api/v1/custom-categories/{id}/share/{userId}", app.handleSpaceShareRevoke)
	apiMux.HandleFunc("DELETE /api/v1/custom-categories/{id}/invitations", app.handleSpaceShareInvitationRevoke)

	apiMux.HandleFunc("GET /api/v1/lists", app.handleListsIndex)
	apiMux.HandleFunc("POST /api/v1/lists", app.handleListsCreate)
	apiMux.HandleFunc("GET /api/v1/lists/{id}", app.handleListsShow)
	apiMux.HandleFunc("PUT /api/v1/lists/{id}", app.handleListsUpdate)
	apiMux.HandleFunc("DELETE /api/v1/lists/{id}", app.handleListsDelete)
	apiMux.HandleFunc("PUT /api/v1/lists/{id}/reorder", app.handleListsReorder)
	apiMux.HandleFunc("GET /api/v1/lists/{id}/share", app.handleListShareIndex)
	apiMux.HandleFunc("POST /api/v1/lists/{id}/share", app.handleListShareCreate)
	apiMux.HandleFunc("PATCH /api/v1/lists/{id}/share/pin", app.handleListSharePin)
	apiMux.HandleFunc("DELETE /api/v1/lists/{id}/share/{userId}", app.handleListShareRevoke)
	apiMux.HandleFunc("DELETE /api/v1/lists/{id}/invitations", app.handleListShareInvitationRevoke)

	apiMux.HandleFunc("GET /api/v1/items", app.handleItemsIndex)
	apiMux.HandleFunc("POST /api/v1/items", app.handleItemsCreate)
	apiMux.HandleFunc("GET /api/v1/items/{id}", app.handleItemsShow)
	apiMux.HandleFunc("PUT /api/v1/items/{id}", app.handleItemsUpdate)
	apiMux.HandleFunc("PATCH /api/v1/items/{id}", app.handleItemsPatch)
	apiMux.HandleFunc("DELETE /api/v1/items/{id}", app.handleItemsDelete)
	apiMux.HandleFunc("POST /api/v1/items/{id}/price-check", app.handleItemsPriceCheck)

	apiMux.HandleFunc("GET /api/v1/price-alerts", app.handlePriceAlertsIndex)
	apiMux.HandleFunc("PATCH /api/v1/price-alerts/{id}", app.handlePriceAlertsUpdate)

	apiMux.HandleFunc("GET /api/v1/admin/settings", app.handleAdminSettingsShow)
	apiMux.HandleFunc("PATCH /api/v1/admin/settings", app.handleAdminSettingsUpdate)
	apiMux.HandleFunc("GET /api/v1/admin/users", app.handleAdminUsersList)
	apiMux.HandleFunc("PATCH /api/v1/admin/users/{id}", app.handleAdminUsersUpdate)
	apiMux.HandleFunc("DELETE /api/v1/admin/users/{id}", app.handleAdminUsersDelete)
	apiMux.HandleFunc("GET /api/v1/admin/spaces", app.handleAdminSpacesList)
	apiMux.HandleFunc("DELETE /api/v1/admin/spaces/{id}", app.handleAdminSpacesDelete)
	apiMux.HandleFunc("GET /api/v1/admin/logs", app.handleAdminLogsList)

	apiMux.HandleFunc("GET /api/v1/push/vapid-public-key", app.handlePushVAPIDPublicKey)
	apiMux.HandleFunc("POST /api/v1/push/subscribe", app.handlePushSubscribe)
	apiMux.HandleFunc("DELETE /api/v1/push/subscribe", app.handlePushUnsubscribe)
	apiMux.HandleFunc("POST /api/v1/push/test", app.handlePushTest)

	mux.Handle("/api/v1/", app.RequireSession(apiMux))
	mux.Handle("/", http.FileServer(staticFileSystem{http.Dir(app.StaticDir)}))

	var handler http.Handler = mux
	// Cross-origin write rejection wraps everything (both /auth/... and
	// /api/v1/...) and sits inside the header/logging middleware, so a
	// rejected request is still logged and still carries the hardening
	// headers. See csrf.go for the two gaps this closes that the session
	// cookie's SameSite=Lax attribute alone does not.
	handler = requireSameOriginWrite(baseURLHost(app.Config.BaseURL), handler)
	// HSTS is only asserted when the deployment says it is behind TLS —
	// SESSION_COOKIE_SECURE is exactly that statement, and sending HSTS from
	// a plain-HTTP localhost instance would pin a developer's browser to a
	// scheme the instance does not serve.
	handler = SecurityHeaders(app.Config.SessionCookieSecure, handler)
	handler = Logging(app.Logger, handler)
	handler = Recover(app.Logger, handler)
	return handler
}

// baseURLHost extracts the hostname from a configured BASE_URL, for the
// cross-origin write check to accept alongside the request's own Host (which
// a reverse proxy may rewrite). Returns "" when BASE_URL is unset or
// unparseable, in which case only the request's own Host is accepted.
func baseURLHost(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// staticFileSystem wraps http.Dir to remove two behaviors of the bare
// http.FileServer that this app has no use for:
//
//   - Directory listings. static/ has several directories with no
//     index.html (js/, css/, icons/, locales/), so the default FileServer
//     published a browsable index of every asset — a free inventory of the
//     application's client-side surface for anyone probing it. Requests for a
//     directory now 404 unless it actually contains an index.html.
//   - Dotfiles. Nothing in static/ starts with a dot today, but an editor
//     swap file or a stray .env landing there should never be reachable over
//     HTTP just because it was dropped in the served directory.
type staticFileSystem struct {
	fs http.FileSystem
}

func (sfs staticFileSystem) Open(name string) (http.File, error) {
	for _, part := range strings.Split(path.Clean(name), "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return nil, fs.ErrNotExist
		}
	}

	f, err := sfs.fs.Open(name)
	if err != nil {
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if stat.IsDir() {
		// http.FileServer falls back to a generated listing when a directory
		// has no index.html; refusing to open index.html here is what makes
		// it give up and 404 instead.
		index, err := sfs.fs.Open(path.Join(name, "index.html"))
		if err != nil {
			_ = f.Close()
			return nil, fs.ErrNotExist
		}
		_ = index.Close()
	}
	return f, nil
}

// serverError logs the underlying error (never exposed to the client) and
// writes a generic 500 response.
func (app *Application) serverError(w http.ResponseWriter, r *http.Request, err error) {
	app.Logger.Error("unhandled error", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}
