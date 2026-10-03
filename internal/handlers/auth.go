package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"trakka/internal/auth"
	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/settings"
	"trakka/internal/validate"
)

// loginPageData is what templates/login.html renders. Error is always
// mapped from a fixed, known ?error= code below — never raw query-string
// content — even though html/template auto-escapes regardless.
type loginPageData struct {
	OIDCEnabled      bool
	RegistrationOpen bool
	OIDCExclusive    bool   // hides the password and registration forms
	OIDCProviderName string // SSO button label; the template falls back to "SSO" when empty
	InstanceName     string
	Mode             string // "login" | "register"
	Error            string
	// CSRFToken is rendered into a hidden field on both forms and must come
	// back matching the HttpOnly cookie set alongside it — see csrf.go for
	// why the unauthenticated auth forms need their own token even though
	// the rest of the app relies on SameSite=Lax.
	CSRFToken string
}

var loginErrorMessages = map[string]string{ // #nosec G101 -- these are user-facing UI strings, not credentials
	"bad_request":         "Requête invalide, veuillez réessayer.",
	"invalid_credentials": "Email ou mot de passe incorrect.",
	"email_taken":         "Un compte existe déjà avec cet email.",
	"oidc_failed":         "La connexion via le fournisseur externe a échoué.",
	"weak_password":       "Le mot de passe doit contenir au moins 8 caractères.",
	"password_too_long":   "Le mot de passe ne doit pas dépasser 72 caractères.",
	"rate_limited":        "Trop de tentatives. Réessayez dans quelques minutes.",
	"csrf_failed":         "Session de formulaire expirée, veuillez réessayer.",
	"invalid_email":       "Adresse email invalide.",
	"registration_closed": "Les inscriptions sont actuellement fermées.",
}

func (app *Application) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if user, err := app.Auth.UserFromRequest(r); err == nil && user != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	current, err := settings.Resolve(r.Context(), app.DB, app.Config)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	exclusive, err := app.oidcExclusive(r, current)
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	mode := "login"
	if r.URL.Query().Get("mode") == "register" && !exclusive {
		mode = "register"
	}
	if mode == "register" && !current.RegistrationOpen {
		http.Redirect(w, r, "/auth/login?error=registration_closed", http.StatusFound)
		return
	}
	csrfToken, err := app.ensureCSRFToken(w, r)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := loginPageData{
		OIDCEnabled:      app.Auth.OIDC() != nil,
		RegistrationOpen: current.RegistrationOpen,
		OIDCExclusive:    exclusive,
		OIDCProviderName: current.OIDCProviderName,
		InstanceName:     current.InstanceName,
		Mode:             mode,
		Error:            loginErrorMessages[r.URL.Query().Get("error")],
		CSRFToken:        csrfToken,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := app.LoginTemplate.Execute(w, data); err != nil {
		app.serverError(w, r, err)
	}
}

// oidcExclusive reports whether local password login and registration are
// disabled. It re-checks the admin PATCH's lockout safeguard on every call:
// if OIDC isn't live (e.g. discovery failed at boot) or no admin is linked
// to the configured issuer any more, local login comes back instead of
// locking every administrator out.
func (app *Application) oidcExclusive(r *http.Request, current settings.Values) (bool, error) {
	if !current.OIDCExclusive || app.Auth.OIDC() == nil {
		return false, nil
	}
	n, err := app.DB.CountOIDCAdmins(r.Context(), current.OIDCIssuer)
	return n > 0, err
}

// rejectLocalAuth answers 403 and returns true while OIDC-exclusive mode is
// in effect — checked before anything else in the password form handlers.
func (app *Application) rejectLocalAuth(w http.ResponseWriter, r *http.Request, current settings.Values) bool {
	exclusive, err := app.oidcExclusive(r, current)
	if err != nil {
		app.serverError(w, r, err)
		return true
	}
	if exclusive {
		http.Error(w, "La connexion par mot de passe est désactivée : utilisez la connexion SSO.", http.StatusForbidden)
	}
	return exclusive
}

func (app *Application) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	current, err := settings.Resolve(r.Context(), app.DB, app.Config)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if app.rejectLocalAuth(w, r, current) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/auth/login?error=bad_request", http.StatusFound)
		return
	}
	if !checkCSRFToken(r) {
		http.Redirect(w, r, "/auth/login?error=csrf_failed", http.StatusFound)
		return
	}

	email := normalizeEmail(r.PostFormValue("email"))
	if !app.allowAuthAttempt(r, email) {
		http.Redirect(w, r, "/auth/login?error=rate_limited", http.StatusFound)
		return
	}

	user, err := app.Auth.Authenticate(r.Context(), email, r.PostFormValue("password"))
	if errors.Is(err, auth.ErrInvalidCredentials) {
		http.Redirect(w, r, "/auth/login?error=invalid_credentials", http.StatusFound)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}

	// A correct password clears this account's failure budget, so a user who
	// fumbles their password a few times and then gets it right is never
	// left locked out by their own typos.
	app.authEmailLimiter().reset(email)
	app.finishLogin(w, r, user.ID)
}

// normalizeEmail lower-cases and trims a submitted address so the rate
// limiter's per-account bucket can't be sidestepped by varying the case (the
// users table itself is COLLATE NOCASE, so "A@b.com" and "a@b.com" are
// already the same account as far as authentication is concerned).
func normalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// allowAuthAttempt records one authentication attempt against both the
// per-IP and the per-email bucket and reports whether it may proceed. Both
// are always recorded (never short-circuited) so an attacker cannot keep one
// bucket cold by tripping the other.
func (app *Application) allowAuthAttempt(r *http.Request, email string) bool {
	ipOK := app.authIPLimiter().allow(clientIP(r), authRateMaxPerIP)
	emailOK := true
	if email != "" {
		emailOK = app.authEmailLimiter().allow(email, authRateMaxPerEmail)
	}
	if !ipOK || !emailOK {
		app.Logger.Warn("authentication attempt rate limited", "ip", clientIP(r), "ip_ok", ipOK, "email_ok", emailOK)
		return false
	}
	return true
}

func (app *Application) handleRegisterSubmit(w http.ResponseWriter, r *http.Request) {
	current, err := settings.Resolve(r.Context(), app.DB, app.Config)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if app.rejectLocalAuth(w, r, current) {
		return
	}
	if !current.RegistrationOpen {
		http.Redirect(w, r, "/auth/login?mode=register&error=registration_closed", http.StatusFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/auth/login?mode=register&error=bad_request", http.StatusFound)
		return
	}
	if !checkCSRFToken(r) {
		http.Redirect(w, r, "/auth/login?mode=register&error=csrf_failed", http.StatusFound)
		return
	}

	email := normalizeEmail(r.PostFormValue("email"))
	if !app.allowAuthAttempt(r, email) {
		http.Redirect(w, r, "/auth/login?mode=register&error=rate_limited", http.StatusFound)
		return
	}

	password := r.PostFormValue("password")
	if password != r.PostFormValue("password_confirm") {
		http.Redirect(w, r, "/auth/login?mode=register&error=bad_request", http.StatusFound)
		return
	}

	user, err := app.Auth.Register(r.Context(), email, password, r.PostFormValue("display_name"))
	switch {
	case errors.Is(err, db.ErrDuplicateEmail):
		http.Redirect(w, r, "/auth/login?mode=register&error=email_taken", http.StatusFound)
		return
	case errors.Is(err, auth.ErrPasswordTooLong):
		http.Redirect(w, r, "/auth/login?mode=register&error=password_too_long", http.StatusFound)
		return
	case err != nil:
		// Register() only otherwise fails on invalid input (bad email,
		// weak password) — a client-side problem, not a server one.
		http.Redirect(w, r, "/auth/login?mode=register&error=bad_request", http.StatusFound)
		return
	}

	if _, err := app.DB.CreateHouseWithOwner(r.Context(), auth.DefaultHouseName, user.ID); err != nil {
		app.serverError(w, r, err)
		return
	}
	// Someone may have invited this address before it had an account — that
	// is the whole point of pending invitations. Apply them now so the new
	// user's very first dashboard already shows what they were invited to.
	app.materializeInvitations(r, user)

	app.finishLogin(w, r, user.ID)
}

func (app *Application) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		_ = app.Auth.RevokeSession(r.Context(), cookie.Value)
	}
	app.Auth.ClearSessionCookie(w)
	http.Redirect(w, r, "/auth/login", http.StatusFound)
}

func (app *Application) finishLogin(w http.ResponseWriter, r *http.Request, userID int64) {
	rawToken, expiresAt, err := app.Auth.CreateSession(r.Context(), userID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.Auth.SetSessionCookie(w, rawToken, expiresAt)
	http.Redirect(w, r, "/", http.StatusFound)
}

// oidcFlowCookieName holds the one-time state/nonce/PKCE-verifier bundle
// between handleOIDCLogin and handleOIDCCallback. SameSite=Lax (not
// Strict): the browser must still send it on the cross-site top-level GET
// navigation the IdP redirects back with, which Strict would block.
const oidcFlowCookieName = "trakka_oidc_flow"

func (app *Application) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	oidcClient := app.Auth.OIDC()
	if oidcClient == nil {
		http.NotFound(w, r)
		return
	}

	state, err := auth.RandomToken(16)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	nonce, err := auth.RandomToken(16)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	verifier, challenge, err := auth.NewPKCE()
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	flow := url.Values{"state": {state}, "nonce": {nonce}, "verifier": {verifier}}
	// Set by the Android app, which runs this flow in the phone's browser and
	// takes the session back into its WebView: see oidc_app.go.
	if appChallenge := r.URL.Query().Get("app_challenge"); appChallenge != "" {
		if !validAppChallenge(appChallenge) {
			http.Error(w, "invalid app_challenge", http.StatusBadRequest)
			return
		}
		flow.Set("app_challenge", appChallenge)
	}
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is set from CookieSecure (SESSION_COOKIE_SECURE), gosec only recognizes a literal `true`
		Name:     oidcFlowCookieName,
		Value:    flow.Encode(),
		Path:     "/auth/oidc",
		HttpOnly: true,
		Secure:   app.Auth.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(w, r, oidcClient.AuthorizationURL(state, nonce, challenge), http.StatusFound)
}

func (app *Application) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	oidcClient := app.Auth.OIDC()
	if oidcClient == nil {
		http.NotFound(w, r)
		return
	}

	flowCookie, cookieErr := r.Cookie(oidcFlowCookieName)
	// Clear the flow cookie unconditionally: it's single-use regardless of
	// whether this callback succeeds. Attributes mirror the cookie set in
	// handleOIDCLogin above so the browser matches it for deletion.
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is set from CookieSecure (SESSION_COOKIE_SECURE), gosec only recognizes a literal `true`
		Name:     oidcFlowCookieName,
		Value:    "",
		Path:     "/auth/oidc",
		HttpOnly: true,
		Secure:   app.Auth.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	// Set once the flow cookie is read: a sign-in started by the Android app
	// returns to it, failures included (see oidc_app.go).
	appChallenge := ""
	fail := func(code string) {
		if appChallenge != "" {
			appSSOReturn(w, r, url.Values{"error": {code}})
			return
		}
		http.Redirect(w, r, "/auth/login?error="+code, http.StatusFound)
	}

	if cookieErr != nil {
		fail("oidc_failed")
		return
	}
	flow, err := url.ParseQuery(flowCookie.Value)
	if err != nil {
		fail("oidc_failed")
		return
	}
	if validAppChallenge(flow.Get("app_challenge")) {
		appChallenge = flow.Get("app_challenge")
	}
	if r.URL.Query().Get("error") != "" {
		fail("oidc_failed")
		return
	}
	if r.URL.Query().Get("state") != flow.Get("state") {
		fail("oidc_failed")
		return
	}

	claims, err := oidcClient.Exchange(r.Context(), r.URL.Query().Get("code"), flow.Get("verifier"))
	if err != nil {
		app.Logger.Warn("oidc exchange failed", "error", err)
		fail("oidc_failed")
		return
	}
	if claims.Nonce != flow.Get("nonce") {
		fail("oidc_failed")
		return
	}

	// Resolve the live "registration open" setting so a closed instance also
	// refuses to auto-provision a brand new OIDC identity — see
	// auth.LoginOrProvisionOIDCUser. An existing OIDC account still logs in.
	current, err := settings.Resolve(r.Context(), app.DB, app.Config)
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	user, err := app.Auth.LoginOrProvisionOIDCUser(r.Context(), claims, current.RegistrationOpen)
	if errors.Is(err, auth.ErrOIDCEmailConflict) {
		fail("email_taken")
		return
	} else if errors.Is(err, auth.ErrRegistrationClosed) {
		fail("registration_closed")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}

	// Same as the local registration path: an OIDC identity signing in for
	// the first time may already have invitations waiting for its address.
	app.materializeInvitations(r, user)

	if appChallenge != "" {
		app.handOffToApp(w, r, user.ID, appChallenge)
		return
	}
	app.finishLogin(w, r, user.ID)
}

// handleMe returns the authenticated user, and is also where invitations
// addressed to their email become real memberships and shares.
//
// This is the hook because it is the one endpoint the frontend calls exactly
// once per app boot (static/js/app.js's init), before it loads houses or
// lists — so an invited person sees what they were invited to on their very
// next visit, without paying for a membership lookup on every API request.
// Failure to materialize is logged but never fails the request: knowing who
// you are must not depend on invitation bookkeeping succeeding.
func (app *Application) handleMe(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	app.materializeInvitations(r, user)
	app.resolveUserLanguage(user)
	writeJSON(w, http.StatusOK, user)
}

// resolveUserLanguage fills in user.Language with the instance-wide
// DEFAULT_APP_LANGUAGE (internal/config) whenever the account itself has no
// explicit preference recorded (an empty users.language column, the state
// every account — new or pre-existing — starts in). Applied here, at the
// point of every JSON response carrying a User, rather than baked into the
// column at account-creation time, so raising or changing that env var also
// takes effect retroactively for every account that never set its own
// language, not just new ones created after the change.
func (app *Application) resolveUserLanguage(user *models.User) {
	if user.Language == "" {
		user.Language = app.Config.DefaultAppLanguage
	}
}

// materializeInvitations applies any invitations waiting for this user's
// email address. Best-effort: see handleMe.
func (app *Application) materializeInvitations(r *http.Request, user *models.User) {
	applied, err := app.DB.MaterializePendingInvitations(r.Context(), user.ID, user.Email)
	if err != nil {
		app.Logger.Error("materializing pending invitations", "user_id", user.ID, "error", err)
		return
	}
	if applied > 0 {
		app.Logger.Info("applied pending invitations", "user_id", user.ID, "count", applied)
	}
}

// handleMeUpdate applies a partial update to the caller's own profile
// preferences: keep_last_page (see the "keep last page on launch" feature in
// static/js/settings.js), language (the account's own UI-language
// preference, set from the "Langue" section of the "Paramètres" modal — see
// static/js/i18n.js/settings.js), and reminder_default_offset_days/
// reminder_default_time (the default due-date-reminder timing applied to any
// task left as "use the default" — see models.User.ReminderDefaultOffsetDays/
// ReminderDefaultTime and the reminder-resolution logic in items.go). All
// follow the same "absent = untouched" PATCH convention as handleItemsPatch;
// any subset may be present in a single request. reminder_default_offset_days
// and reminder_default_time are treated as a pair — either both are given
// together or neither is, since sending just one without the other would
// leave the account in an ambiguous half-updated state.
// reminder_default_at_due_time (the "at the exact due time" preset, see
// models.User.ReminderDefaultAtDueTime) belongs to the same setting: it may
// only come with that pair, and the pair on its own means false, so a
// client that predates the preset still saves a plain offset default.
// vibrate_on_notification (see models.User.VibrateOnNotification) is a
// plain on/off like keep_last_page.
func (app *Application) handleMeUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		KeepLastPage              *bool   `json:"keep_last_page"`
		Language                  *string `json:"language"`
		ReminderDefaultOffsetDays *int    `json:"reminder_default_offset_days"`
		ReminderDefaultTime       *string `json:"reminder_default_time"`
		ReminderDefaultAtDueTime  *bool   `json:"reminder_default_at_due_time"`
		VibrateOnNotification     *bool   `json:"vibrate_on_notification"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	if (in.ReminderDefaultOffsetDays == nil) != (in.ReminderDefaultTime == nil) {
		writeError(w, http.StatusBadRequest, "reminder_default_offset_days and reminder_default_time must be given together")
		return
	}
	if in.ReminderDefaultAtDueTime != nil && in.ReminderDefaultTime == nil {
		writeError(w, http.StatusBadRequest, "reminder_default_at_due_time must be given with reminder_default_offset_days and reminder_default_time")
		return
	}
	var cleanReminderTime string
	if in.ReminderDefaultTime != nil {
		if *in.ReminderDefaultOffsetDays < 0 {
			writeError(w, http.StatusBadRequest, "reminder_default_offset_days cannot be negative")
			return
		}
		var err error
		cleanReminderTime, err = validate.TimeOfDay(*in.ReminderDefaultTime)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if cleanReminderTime == "" {
			writeError(w, http.StatusBadRequest, "reminder_default_time is required")
			return
		}
	}

	user := userFromContext(r)

	if in.Language != nil {
		lang, ok := validate.Language(*in.Language)
		if !ok {
			writeError(w, http.StatusBadRequest, `language must be "fr" or "en"`)
			return
		}
		updated, err := app.DB.UpdateUserLanguage(r.Context(), user.ID, lang)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		} else if err != nil {
			app.serverError(w, r, err)
			return
		}
		user = updated
	}

	if in.KeepLastPage != nil {
		updated, err := app.DB.UpdateUserKeepLastPage(r.Context(), user.ID, *in.KeepLastPage)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		} else if err != nil {
			app.serverError(w, r, err)
			return
		}
		user = updated
	}

	if in.VibrateOnNotification != nil {
		updated, err := app.DB.UpdateUserVibrateOnNotification(r.Context(), user.ID, *in.VibrateOnNotification)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		} else if err != nil {
			app.serverError(w, r, err)
			return
		}
		user = updated
	}

	if in.ReminderDefaultTime != nil {
		atDueTime := in.ReminderDefaultAtDueTime != nil && *in.ReminderDefaultAtDueTime
		updated, err := app.DB.UpdateUserReminderDefaults(r.Context(), user.ID, *in.ReminderDefaultOffsetDays, cleanReminderTime, atDueTime)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		} else if err != nil {
			app.serverError(w, r, err)
			return
		}
		user = updated
	}

	app.resolveUserLanguage(user)
	writeJSON(w, http.StatusOK, user)
}
