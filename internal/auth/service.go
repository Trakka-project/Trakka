package auth

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"sync/atomic"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// dummyHash is a fixed bcrypt hash checked when a login's email doesn't
// match any account, so Authenticate takes roughly the same time whether
// or not the email exists — a defense against user-enumeration via timing.
var dummyHash = mustHash("not-a-real-password-timing-fixture")

func mustHash(plain string) string {
	hash, err := HashPassword(plain)
	if err != nil {
		panic(err)
	}
	return hash
}

// Service implements registration, local authentication, and session
// management on top of internal/db. OIDC() returns nil when no provider is
// configured.
type Service struct {
	DB           *db.DB
	SessionTTL   time.Duration
	CookieSecure bool

	// oidc is held behind an atomic.Pointer rather than a plain field
	// because the admin settings panel (internal/handlers/admin.go) can
	// reconfigure or disable OIDC at any time from a request goroutine,
	// concurrently with other goroutines serving /auth/oidc/... — a plain
	// field read/written without synchronization would be a data race.
	oidc atomic.Pointer[OIDCClient]
}

// NewService constructs a Service. oidc may be nil if OIDC is not
// configured.
func NewService(database *db.DB, oidc *OIDCClient, sessionTTL time.Duration, cookieSecure bool) *Service {
	s := &Service{DB: database, SessionTTL: sessionTTL, CookieSecure: cookieSecure}
	s.oidc.Store(oidc)
	return s
}

// OIDC returns the currently active OIDC client, or nil if OIDC isn't
// configured right now.
func (s *Service) OIDC() *OIDCClient {
	return s.oidc.Load()
}

// SetOIDC atomically replaces the active OIDC client — called by the admin
// settings endpoint after successfully re-running discovery against a new
// issuer/client configuration, or with nil to disable OIDC login entirely.
// Any authorization flow already in flight against the previous client
// keeps working (Exchange only needs the client instance it started with),
// so swapping never invalidates a login that's mid-redirect.
func (s *Service) SetOIDC(oidc *OIDCClient) {
	s.oidc.Store(oidc)
}

// Register creates a new local account. Returns db.ErrDuplicateEmail if the
// email is already registered.
func (s *Service) Register(ctx context.Context, email, password, displayName string) (*models.User, error) {
	email = strings.ToLower(validate.Text(email))
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, errors.New("invalid email address")
	}
	if !validate.MaxLen(email, validate.MaxEmailLen) {
		return nil, errors.New("email address is too long")
	}
	if err := ValidatePasswordStrength(password); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	displayName = validate.Text(displayName)
	if !validate.MaxLen(displayName, validate.MaxDisplayNameLen) {
		return nil, errors.New("display name is too long")
	}
	return s.DB.CreateUser(ctx, email, &hash, nil, nil, displayName)
}

// Authenticate verifies an email/password pair. Returns ErrInvalidCredentials
// if the email is unknown, the account has no password (OIDC-only), or the
// password doesn't match.
func (s *Service) Authenticate(ctx context.Context, email, password string) (*models.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	user, err := s.DB.GetUserByEmail(ctx, email)
	if errors.Is(err, db.ErrNotFound) {
		VerifyPassword(dummyHash, password) // timing/enumeration mitigation
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if user.PasswordHash == nil {
		VerifyPassword(dummyHash, password) // OIDC-only account; same timing profile
		return nil, ErrInvalidCredentials
	}
	if !VerifyPassword(*user.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}
	return &user.User, nil
}

// CreateSession issues a new session for userID and returns the raw token
// to place in a cookie (the database only ever stores its hash).
func (s *Service) CreateSession(ctx context.Context, userID int64) (rawToken string, expiresAt time.Time, err error) {
	rawToken, err = RandomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = time.Now().Add(s.SessionTTL)
	if err := s.DB.CreateSession(ctx, HashToken(rawToken), userID, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return rawToken, expiresAt, nil
}

// ValidateSession resolves a raw session token to its user. Returns
// db.ErrNotFound if the token is unknown or expired.
func (s *Service) ValidateSession(ctx context.Context, rawToken string) (*models.User, error) {
	session, err := s.DB.GetSessionByHash(ctx, HashToken(rawToken))
	if err != nil {
		return nil, err
	}
	return s.DB.GetUser(ctx, session.UserID)
}

// ValidateAPIToken resolves a raw API token (see NewAPIToken) to its user,
// recording the use. Returns db.ErrNotFound if the token is malformed,
// unknown or revoked.
func (s *Service) ValidateAPIToken(ctx context.Context, rawToken string) (*models.User, error) {
	if !strings.HasPrefix(rawToken, APITokenPrefix) || len(rawToken) > MaxAPITokenLength {
		return nil, db.ErrNotFound
	}
	userID, err := s.DB.UseAPIToken(ctx, HashToken(rawToken))
	if err != nil {
		return nil, err
	}
	return s.DB.GetUser(ctx, userID)
}

// RevokeSession deletes a session (logout). Returns db.ErrNotFound if the
// token is unknown.
func (s *Service) RevokeSession(ctx context.Context, rawToken string) error {
	return s.DB.DeleteSessionByHash(ctx, HashToken(rawToken))
}

// SetSessionCookie writes the session cookie. HttpOnly + SameSite=Lax:
// this cookie is only ever needed on same-origin requests once a session
// exists, so Strict was the original choice — but the local-login and OIDC
// flows both finish with a same-site 302 redirect (finishLogin), and mobile
// WebKit (notably iOS Safari in "Add to Home Screen" standalone/PWA mode)
// has a well-documented quirk where a SameSite=Strict cookie set on the
// response of that redirect is not reliably attached to the very next
// request, producing a login that appears to succeed (302) immediately
// followed by a 401 on the first authenticated call. Lax still omits the
// cookie from any cross-site request that isn't a top-level GET navigation
// — in particular every fetch()/XHR the frontend issues, including all
// POST/PUT/PATCH/DELETE mutations — and every GET route this app exposes
// is a pure read (see internal/handlers/app.go), so there is no
// state-changing endpoint a top-level cross-site navigation could trigger.
// CSRF protection against the JSON API is therefore unchanged by this
// relaxation; only the previously-unnecessary block on same-site
// redirect-chain delivery is lifted. Also sets Max-Age alongside Expires:
// older WebKit releases have been inconsistent about honoring
// Expires-only Set-Cookie headers, and Max-Age is the more reliable of the
// two on that engine.
func (s *Service) SetSessionCookie(w http.ResponseWriter, rawToken string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is set from CookieSecure (SESSION_COOKIE_SECURE), gosec only recognizes a literal `true`
		Name:     SessionCookieName,
		Value:    rawToken,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   s.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the session cookie immediately (logout).
func (s *Service) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is set from CookieSecure (SESSION_COOKIE_SECURE), gosec only recognizes a literal `true`
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// UserFromRequest reads the session cookie from r and validates it. Returns
// an error if there's no cookie or the session is invalid/expired.
func (s *Service) UserFromRequest(r *http.Request) (*models.User, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return nil, err
	}
	return s.ValidateSession(r.Context(), cookie.Value)
}
