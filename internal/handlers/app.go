// Package handlers implements the HTTP layer: routing, request
// validation, and translating db-layer results into JSON responses. It
// never touches database/sql directly — all persistence goes through
// internal/db.
package handlers

import (
	"context"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"trakka/internal/auth"
	"trakka/internal/backup"
	"trakka/internal/config"
	"trakka/internal/db"
	"trakka/internal/fluv"
	"trakka/internal/logbuffer"
	"trakka/internal/scraper"
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

	// Location is the time.Location reminder times of day (see
	// models.User.ReminderDefaultTime, models.Item.ReminderTime) are
	// interpreted in — resolved once at startup from Config.AppTimeZone
	// (cmd/server/main.go), falling back to UTC there with a startup
	// warning on an unrecognized zone name. May be nil (e.g. in tests that
	// build an Application by hand), in which case location() below falls
	// back to time.UTC — the same "zero value must stay usable" contract
	// the rate-limiter fields below already follow.
	Location *time.Location

	// Backups runs encrypted WebDAV backups and restores (see
	// internal/backup) behind /api/v1/admin/backups/.... May be nil (e.g.
	// in tests that build an Application by hand), in which case those
	// endpoints answer 404.
	Backups *backup.Service

	// Fluv sends the users' purchases to Fluv, their personal finance app
	// (see internal/fluv and fluv.go), behind /api/v1/integrations/fluv.
	// Nil when the operator turned it off (FLUV_INTEGRATION_ENABLED=false)
	// and in tests that build an Application by hand: those routes then
	// answer 404 and checking items off sends nothing.
	Fluv *fluv.Service

	// RequestRestart asks cmd/server to shut down gracefully and exit for
	// its supervisor to start it again — what POST /api/v1/admin/restart
	// calls once every check has passed (see admin_restart.go). May be nil
	// (e.g. in tests that build an Application by hand), in which case the
	// restart endpoints answer 404.
	RequestRestart func()

	// Authentication rate-limiter state (see ratelimit.go). Lazily built
	// through sync.Once rather than in a constructor, because Application is
	// built as a plain struct literal in cmd/server and in tests — a zero
	// value must stay usable.
	authIPLimiterOnce    sync.Once
	authIPLimiterVal     *rateLimiter
	authEmailLimiterOnce sync.Once
	authEmailLimiterVal  *rateLimiter

	// Per-account sharing-autocomplete bucket (see user_search.go), lazily
	// built for the same reason.
	userSearchLimiterOnce sync.Once
	userSearchLimiterVal  *rateLimiter

	// Per-account Open Food Facts lookup bucket (see products.go), lazily
	// built for the same reason.
	barcodeLookupLimiterOnce sync.Once
	barcodeLookupLimiterVal  *rateLimiter

	// Per-account Fluv connection test bucket (see fluv.go), lazily built
	// for the same reason.
	fluvTestLimiterOnce sync.Once
	fluvTestLimiterVal  *rateLimiter

	// Per-link bucket of public link visitors' writes (see public_links.go),
	// lazily built for the same reason.
	publicWriteLimiterOnce sync.Once
	publicWriteLimiterVal  *rateLimiter

	// quickAddMu serializes "is this product already on the list? then
	// update it, else create it" on Courses lists (see
	// addToQuickShoppingList), so two simultaneous adds can't both miss the
	// other and create a duplicate. Held only for those few queries. A zero
	// value is ready to use.
	quickAddMu sync.Mutex

	// Handoff codes of SSO sign-ins started by the Android app (see
	// oidc_app.go), lazily built for the same reason.
	appHandoffsOnce sync.Once
	appHandoffsVal  *appHandoffStore

	// QR codes signing the Android app in (see qr_login.go), lazily built
	// for the same reason.
	qrLoginsOnce sync.Once
	qrLoginsVal  *qrLoginStore

	// Pending server-restart challenges (see admin_restart.go), lazily built
	// for the same reason, and whether a restart is already under way.
	restartChallengesOnce sync.Once
	restartChallengesVal  *restartChallengeStore
	restarting            atomic.Bool

	// Retried OIDC discovery (see oidc_discovery.go): one attempt at a time,
	// and the time and error of the last one.
	oidcDiscoveryMu   sync.Mutex
	oidcDiscoveryLast time.Time
	oidcDiscoveryErr  error

	// pushHook, when set (tests only), receives every notification
	// sendToUsers would deliver instead of it being sent: no test can reach
	// a real push service, whose endpoints the SSRF guard keeps off
	// loopback anyway.
	pushHook func(userIDs []int64, payload pushPayload)

	// productPageHook, when set (tests only), stands in for
	// scraper.FetchProductInfo in the price checks (fetchProductPage): the
	// SSRF guard keeps the real fetch off a loopback test server, the same
	// reason as pushHook.
	productPageHook func(ctx context.Context, rawURL string) (*scraper.ProductInfo, error)

	// barcodeLookupHook, when set (tests only), stands in for
	// scraper.LookupBarcode (Open Food Facts) in lookupBarcode, for the same
	// reason as productPageHook.
	barcodeLookupHook func(ctx context.Context, code, lang string) (*scraper.BarcodeProduct, error)

	// productSearchHook, when set (tests only), stands in for
	// scraper.SearchProducts (Open Food Facts' text search) in
	// searchProducts, for the same reason as productPageHook.
	productSearchHook func(ctx context.Context, terms, lang string) ([]scraper.SearchProduct, error)

	// productSearchLimiterOnce/Val is the instance-wide Open Food Facts
	// text-search bucket, and productSearchCache what recent searches
	// found (see product_enrichment.go).
	productSearchLimiterOnce sync.Once
	productSearchLimiterVal  *rateLimiter
	productSearchCache       productSearchCache
}

// fetchProductPage reads an item's own page for a price check:
// scraper.FetchProductInfo, or productPageHook in tests.
func (app *Application) fetchProductPage(ctx context.Context, rawURL string) (*scraper.ProductInfo, error) {
	if app.productPageHook != nil {
		return app.productPageHook(ctx, rawURL)
	}
	return scraper.FetchProductInfo(ctx, rawURL, app.Logger)
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

// location returns app.Location, falling back to time.UTC when it hasn't
// been set (see Location's own doc comment) — every reminder-time
// computation goes through this rather than reading the field directly.
func (app *Application) location() *time.Location {
	if app.Location == nil {
		return time.UTC
	}
	return app.Location
}

// Routes builds the full HTTP handler: middleware chain + route table.
//
// /api/v1/... is gated behind RequireSession as a whole (wrapped once,
// below) rather than per-route — every JSON API endpoint requires a valid
// session, or an API token for the few routes tokens open (api_tokens.go). /auth/... and static files stay unauthenticated: /auth/... is
// how a session gets established in the first place, and gating static
// files would break the service worker's cache-first app-shell strategy
// (the client-side redirect in app.js's apiRequest() handles the UX layer
// on top of the API's 401s).
func (app *Application) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", app.handleHealthz)

	mux.HandleFunc("GET /auth/login", app.handleLoginPage)
	mux.HandleFunc("GET /auth/methods", app.handleAuthMethods)
	mux.HandleFunc("POST /auth/login", app.handleLoginSubmit)
	mux.HandleFunc("POST /auth/register", app.handleRegisterSubmit)
	mux.HandleFunc("POST /auth/logout", app.handleLogout)
	mux.HandleFunc("GET /auth/oidc/login", app.handleOIDCLogin)
	mux.HandleFunc("GET /auth/oidc/callback", app.handleOIDCCallback)
	mux.HandleFunc("POST /auth/oidc/app-session", app.handleOIDCAppSession)
	// The Android app's half of signing in by QR code (qr_login.go): under
	// /auth/ because it opens the session the /api/v1/ routes require.
	mux.HandleFunc("POST /auth/qr-code/login", app.handleQRLoginRedeem)

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /api/v1/me", app.handleMe)
	apiMux.HandleFunc("PATCH /api/v1/me", app.handleMeUpdate)
	apiMux.HandleFunc("GET /api/v1/config", app.handleConfig)
	apiMux.HandleFunc("POST /api/v1/auth/qr-code/generate", app.handleQRLoginGenerate)
	apiMux.HandleFunc("GET /api/v1/users/search", app.handleUsersSearch)

	apiMux.HandleFunc("GET /api/v1/houses", app.handleHousesIndex)
	apiMux.HandleFunc("POST /api/v1/houses", app.handleHousesCreate)
	apiMux.HandleFunc("GET /api/v1/houses/{id}", app.handleHousesShow)
	apiMux.HandleFunc("PUT /api/v1/houses/{id}", app.handleHousesUpdate)
	apiMux.HandleFunc("DELETE /api/v1/houses/{id}", app.handleHousesDelete)
	apiMux.HandleFunc("GET /api/v1/houses/{id}/members", app.handleHouseMembersIndex)
	apiMux.HandleFunc("POST /api/v1/houses/{id}/members", app.handleHouseMembersInvite)
	apiMux.HandleFunc("DELETE /api/v1/houses/{id}/members/{userId}", app.handleHouseMembersRemove)
	apiMux.HandleFunc("GET /api/v1/houses/{id}/products", app.handleProductsIndex)
	apiMux.HandleFunc("POST /api/v1/houses/{id}/products", app.handleProductsUpsert)
	apiMux.HandleFunc("GET /api/v1/houses/{id}/products/suggestions", app.handleProductSuggestions)
	apiMux.HandleFunc("GET /api/v1/houses/{id}/products/barcode/{code}", app.handleProductBarcodeLookup)
	apiMux.HandleFunc("GET /api/v1/houses/{id}/products/nutrition", app.handleProductNutritionLookup)
	apiMux.HandleFunc("PATCH /api/v1/houses/{id}/products/{productId}", app.handleProductsUpdate)
	apiMux.HandleFunc("DELETE /api/v1/houses/{id}/products/{productId}", app.handleProductsDelete)
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
	apiMux.HandleFunc("GET /api/v1/lists/{id}/assignable-users", app.handleListAssignableUsers)
	apiMux.HandleFunc("GET /api/v1/shopping-templates", app.handleShoppingTemplatesIndex)
	apiMux.HandleFunc("POST /api/v1/shopping-templates", app.handleShoppingTemplatesCreate)
	apiMux.HandleFunc("DELETE /api/v1/shopping-templates/{id}", app.handleShoppingTemplatesDelete)
	apiMux.HandleFunc("GET /api/v1/lists/{id}/share", app.handleListShareIndex)
	apiMux.HandleFunc("POST /api/v1/lists/{id}/share", app.handleListShareCreate)
	apiMux.HandleFunc("PATCH /api/v1/lists/{id}/share/pin", app.handleListSharePin)
	apiMux.HandleFunc("DELETE /api/v1/lists/{id}/share/{userId}", app.handleListShareRevoke)
	apiMux.HandleFunc("DELETE /api/v1/lists/{id}/invitations", app.handleListShareInvitationRevoke)
	// The list's public share links (see public_links.go). Not under
	// /share, whose POST already shares the list with a person by email.
	apiMux.HandleFunc("GET /api/v1/lists/{id}/public-links", app.handleListPublicLinksIndex)
	apiMux.HandleFunc("POST /api/v1/lists/{id}/public-links", app.handleListPublicLinksCreate)
	apiMux.HandleFunc("DELETE /api/v1/lists/{id}/public-links/{linkId}", app.handleListPublicLinksDelete)

	apiMux.HandleFunc("GET /api/v1/items", app.handleItemsIndex)
	apiMux.HandleFunc("POST /api/v1/items", app.handleItemsCreate)
	apiMux.HandleFunc("GET /api/v1/items/{id}", app.handleItemsShow)
	apiMux.HandleFunc("PUT /api/v1/items/{id}", app.handleItemsUpdate)
	apiMux.HandleFunc("PATCH /api/v1/items/{id}", app.handleItemsPatch)
	apiMux.HandleFunc("DELETE /api/v1/items/{id}", app.handleItemsDelete)
	apiMux.HandleFunc("POST /api/v1/items/{id}/price-check", app.handleItemsPriceCheck)
	apiMux.HandleFunc("GET /api/v1/items/{id}/price-history", app.handleItemsPriceHistory)
	apiMux.HandleFunc("GET /api/v1/reminders/upcoming", app.handleRemindersUpcoming)

	apiMux.HandleFunc("GET /api/v1/price-alerts", app.handlePriceAlertsIndex)
	apiMux.HandleFunc("PATCH /api/v1/price-alerts/{id}", app.handlePriceAlertsUpdate)
	apiMux.HandleFunc("GET /api/v1/price-notifications", app.handlePriceNotificationsIndex)
	apiMux.HandleFunc("POST /api/v1/price-notifications/read", app.handlePriceNotificationsRead)
	apiMux.HandleFunc("DELETE /api/v1/price-notifications", app.handlePriceNotificationsClear)
	apiMux.HandleFunc("DELETE /api/v1/price-notifications/{id}", app.handlePriceNotificationsDelete)

	apiMux.HandleFunc("GET /api/v1/admin/settings", app.handleAdminSettingsShow)
	apiMux.HandleFunc("PATCH /api/v1/admin/settings", app.handleAdminSettingsUpdate)
	apiMux.HandleFunc("GET /api/v1/admin/users", app.handleAdminUsersList)
	apiMux.HandleFunc("PATCH /api/v1/admin/users/{id}", app.handleAdminUsersUpdate)
	apiMux.HandleFunc("DELETE /api/v1/admin/users/{id}", app.handleAdminUsersDelete)
	apiMux.HandleFunc("GET /api/v1/admin/spaces", app.handleAdminSpacesList)
	apiMux.HandleFunc("DELETE /api/v1/admin/spaces/{id}", app.handleAdminSpacesDelete)
	apiMux.HandleFunc("GET /api/v1/admin/logs", app.handleAdminLogsList)
	apiMux.HandleFunc("GET /api/v1/admin/backups", app.handleAdminBackupsStatus)
	apiMux.HandleFunc("PUT /api/v1/admin/backups/config", app.handleAdminBackupsConfigUpdate)
	apiMux.HandleFunc("POST /api/v1/admin/backups/test", app.handleAdminBackupsTest)
	apiMux.HandleFunc("POST /api/v1/admin/backups/run", app.handleAdminBackupsRun)
	apiMux.HandleFunc("GET /api/v1/admin/backups/remote", app.handleAdminBackupsRemoteList)
	// A POST although it returns data: it also records that the key was
	// saved, and no GET route in this app may change state (see csrf.go).
	apiMux.HandleFunc("POST /api/v1/admin/backups/key/export", app.handleAdminBackupsKeyExport)
	apiMux.HandleFunc("POST /api/v1/admin/backups/restore", app.handleAdminBackupsRestore)
	// Only ever minted for a request carrying the restart action header, so
	// a plain navigation to this GET changes nothing (see admin_restart.go).
	apiMux.HandleFunc("GET /api/v1/admin/restart-challenge", app.handleAdminRestartChallenge)
	apiMux.HandleFunc("POST /api/v1/admin/restart", app.handleAdminRestart)

	apiMux.HandleFunc("GET /api/v1/push/vapid-public-key", app.handlePushVAPIDPublicKey)
	apiMux.HandleFunc("POST /api/v1/push/subscribe", app.handlePushSubscribe)
	apiMux.HandleFunc("DELETE /api/v1/push/subscribe", app.handlePushUnsubscribe)
	apiMux.HandleFunc("POST /api/v1/push/test", app.handlePushTest)

	apiMux.HandleFunc("GET /api/v1/calendar/feed-token", app.handleCalendarFeedTokenShow)
	apiMux.HandleFunc("POST /api/v1/calendar/feed-token", app.handleCalendarFeedTokenCreate)
	apiMux.HandleFunc("DELETE /api/v1/calendar/feed-token", app.handleCalendarFeedTokenDelete)

	// Managing API tokens needs a session: a token opens none of these (see
	// apiTokenRoutes in api_tokens.go for the routes it does open).
	apiMux.HandleFunc("GET /api/v1/api-tokens", app.handleAPITokensIndex)
	apiMux.HandleFunc("POST /api/v1/api-tokens", app.handleAPITokensCreate)
	apiMux.HandleFunc("DELETE /api/v1/api-tokens/{id}", app.handleAPITokensDelete)

	// The caller's connection to Fluv (fluv.go): session only, like the
	// token routes above — an API token can't redirect anyone's purchases.
	apiMux.HandleFunc("GET /api/v1/integrations/fluv", app.handleFluvShow)
	apiMux.HandleFunc("PUT /api/v1/integrations/fluv", app.handleFluvSave)
	apiMux.HandleFunc("DELETE /api/v1/integrations/fluv", app.handleFluvDelete)
	apiMux.HandleFunc("POST /api/v1/integrations/fluv/test", app.handleFluvTest)
	apiMux.HandleFunc("POST /api/v1/integrations/fluv/retry", app.handleFluvRetry)

	mux.Handle("/api/v1/", app.RequireSession(apiMux))
	// The only /api/v1/... routes outside RequireSession, each authenticated
	// by a secret token instead and opening nothing else: the calendar feed,
	// which calendar apps poll in the background with no session (token in
	// its query string, see calendar_feed.go), and a list's public share
	// link, opened by visitors with no account (token in the path, see
	// public_links.go). More specific patterns than "/api/v1/" above, so the
	// mux always routes them here. Don't add another.
	mux.HandleFunc("GET /api/v1/calendar/feed.ics", app.handleCalendarFeed)
	mux.HandleFunc("GET /api/v1/public/lists/{token}", app.handlePublicListShow)
	mux.HandleFunc("POST /api/v1/public/lists/{token}/items", app.handlePublicItemCreate)
	mux.HandleFunc("PATCH /api/v1/public/lists/{token}/items/{itemId}", app.handlePublicItemPatch)
	// The visitor's page of a public link: a static page, but at a
	// token-bearing URL that must be neither cached nor indexed.
	mux.HandleFunc("GET /public/lists/{token}", app.handlePublicListPage)
	fileServer := http.FileServer(staticFileSystem{http.Dir(app.StaticDir)})
	// Every static file, sw.js first among them, is served with an explicit
	// Cache-Control: no-cache (see revalidateStatic). Every browser-side
	// update-detection mechanism this app relies on (static/js/app.js's
	// watchForServiceWorkerUpdate/checkForServiceWorkerUpdate, the
	// visibilitychange/focus/interval triggers, and the manual "Vérifier les
	// mises à jour" button) ultimately depends on registration.update()
	// actually fetching a fresh copy of sw.js to byte-compare against the
	// installed one — without that header, a browser's own heuristic HTTP
	// caching (or an intermediate proxy/mobile carrier cache) can serve back
	// a stale, already-cached copy instead, silently defeating that
	// comparison and leaving a phone "stuck" on an old version no matter how
	// often it checks. And detecting the update isn't enough on its own: the
	// JS/CSS/HTML the new worker then precaches, and that the page reloads,
	// must not come back from a stale HTTP cache either.
	mux.Handle("/", revalidateStatic(fileServer))

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

// revalidateStatic sets Cache-Control: no-cache on the static assets
// (index.html, /js, /css, /locales, /emoji, /icons, manifest.json, sw.js).
// Without any Cache-Control, http.FileServer's responses carry only
// Last-Modified, from which browsers derive a heuristic freshness lifetime
// (typically 10% of the file's age): the Android WebView kept serving a
// previous release's JS/CSS from its HTTP cache for days after a deploy,
// even to a brand-new service worker precaching the new version. no-cache
// still allows caching but forces a revalidation each time, which the file
// server answers with a body-less 304 when nothing changed (If-Modified-
// Since against the file's mtime) — cheap, and in normal use the service
// worker's own cache answers first anyway.
func revalidateStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
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
	app.Logger.Error("unhandled error", "method", r.Method, "path", logPath(r), "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}
