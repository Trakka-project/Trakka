package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"trakka/internal/db"
)

// Sharing autocomplete (GET /api/v1/users/search) and the instance
// configuration the frontend needs to drive it (GET /api/v1/config).
//
// What a search may match depends on the deployment (config.Config.SaaSMode,
// see db.SearchUsers): on a self-hosted instance any part of any member's
// name or email address — a household knows each other; in SaaS mode only
// an email address, and only among the caller's existing contacts, so one
// tenant can never browse another's names or addresses, nor test whether an
// arbitrary address has an account (docs/AUDIT.md L-06). Both modes are
// bounded by a per-account rate limit, so the autocomplete can't be turned
// into a bulk scraper of whatever it is allowed to reveal.
const (
	userSearchMinRunes  = 2
	userSearchMaxBytes  = 254 // the longest valid email address
	userSearchRateWin   = time.Minute
	userSearchRateLimit = 60
)

// instanceConfig is GET /api/v1/config's body: instance-wide settings any
// signed-in user's frontend needs, as opposed to the admin-only
// /api/v1/admin/settings.
type instanceConfig struct {
	SaaSMode bool `json:"saas_mode"`
	// Timezone is the IANA name of APP_TIMEZONE, the zone an item's
	// due_time and reminder times are wall-clock times in. The Home
	// Assistant integration reads it to turn due_date + due_time into an
	// absolute time.
	Timezone string `json:"timezone"`
}

func (app *Application) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, instanceConfig{SaaSMode: app.Config.SaaSMode, Timezone: app.location().String()})
}

// handleUsersSearch answers GET /api/v1/users/search?q=... with up to
// db.UserSearchLimit suggestions, never including the caller. A query too
// short to be useful gets an empty list rather than an error, since the
// autocomplete sends whatever has been typed so far.
func (app *Application) handleUsersSearch(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	if !app.userSearchLimiter().allow(strconv.FormatInt(user.ID, 10), userSearchRateLimit) {
		w.Header().Set("Retry-After", strconv.Itoa(int(userSearchRateWin.Seconds())))
		writeError(w, http.StatusTooManyRequests, "too many searches, try again in a minute")
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > userSearchMaxBytes {
		writeError(w, http.StatusBadRequest, "q is too long")
		return
	}
	if utf8.RuneCountInString(q) < userSearchMinRunes {
		writeJSON(w, http.StatusOK, []struct{}{})
		return
	}

	scope := db.UserSearchInstance
	if app.Config.SaaSMode {
		scope = db.UserSearchContactsByEmail
	}
	results, err := app.DB.SearchUsers(r.Context(), q, user.ID, scope)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

// userSearchLimiter is the per-account search bucket, lazily built like the
// authentication ones (see ratelimit.go).
func (app *Application) userSearchLimiter() *rateLimiter {
	app.userSearchLimiterOnce.Do(func() { app.userSearchLimiterVal = newRateLimiter(userSearchRateWin) })
	return app.userSearchLimiterVal
}
