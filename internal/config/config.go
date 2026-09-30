// Package config loads Trakka's runtime configuration from environment
// variables. There is no config file; every setting is an env var so the
// container image stays fully generic across deployments.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"trakka/internal/validate"
)

type Config struct {
	Port      string
	DBPath    string
	StaticDir string

	TemplatesDir string // dir containing login.html, mirrors StaticDir's pattern

	// BaseURL is the externally-visible origin (e.g. "https://trakka.example.com"),
	// used to construct the OIDC redirect_uri deterministically. Required
	// only when OIDC is configured.
	BaseURL string

	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string

	SessionCookieSecure bool
	SessionTTL          time.Duration

	// PriceCheckInterval is how often the background price-drop scan
	// (internal/handlers.RunPriceAlertScan) re-checks every eligible item.
	// A value <= 0 disables the periodic scan entirely (on-demand checks via
	// POST /api/v1/items/{id}/price-check still work regardless).
	PriceCheckInterval time.Duration

	// TargetPriceScrapeInterval is how often the target-price background
	// worker (internal/handlers.RunTargetPriceScan) re-scrapes every item
	// with an active "notify me when the price drops" threshold (see
	// models.Item.TargetPrice/AlertOnPriceDrop) and applies whatever current
	// price it finds. This is distinct from PriceCheckInterval above: that
	// scan compares a scraped price against the item's own current price and
	// only ever proposes a price_alerts row for the user to accept/reject,
	// while this one compares against the user's own explicit threshold and,
	// once crossed, writes items.price directly and notifies immediately
	// with no accept/reject step. A value <= 0 disables the periodic scan
	// entirely.
	TargetPriceScrapeInterval time.Duration

	// InstanceName and RegistrationOpen are the env-var defaults for two of
	// the settings manageable at runtime via the admin-only
	// PATCH /api/v1/admin/settings endpoint (see internal/settings.Resolve).
	// A row in the system_settings table always takes priority over these
	// once one exists; these are only what a fresh instance starts with.
	InstanceName     string
	RegistrationOpen bool

	// VAPIDPublicKey/VAPIDPrivateKey are this instance's Web Push
	// application-server identity (see internal/webpush) — a P-256 key pair,
	// base64url-encoded exactly as internal/webpush.GenerateVAPIDKeys (and
	// `trakka -generate-vapid-keys`) produce them. VAPIDSubject is the
	// contact URI (mailto: or https:) sent in every VAPID JWT's "sub" claim,
	// as RFC 8292 requires. All three are all-or-nothing, like OIDC below —
	// see Validate.
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	VAPIDSubject    string

	// NotifDueScanInterval is how often the due-date reminder background
	// scan (internal/handlers.RunDueReminderScan) re-checks every item with
	// an active reminder — independent of, and normally much finer-grained
	// than, any individual reminder's own offset/time, so a reminder due at
	// e.g. 09:00 is actually caught reasonably close to on time rather than
	// only once a day. Defaults to one minute: with the "at the exact due
	// time" reminder mode, a coarser interval shows up directly as a late
	// notification, and the scan is one small query. A value <= 0 disables
	// the periodic scan entirely.
	NotifDueScanInterval time.Duration

	// AppTimeZone names the IANA time zone (e.g. "Europe/Paris", "UTC")
	// reminder times of day (see models.User.ReminderDefaultTime,
	// models.Item.ReminderTime) are interpreted in — there is otherwise no
	// per-user or per-instance notion of a time zone anywhere in this
	// codebase, and "remind me at 09:00" is meaningless without one. This is
	// deliberately a single instance-wide setting rather than a per-user
	// preference: Trakka targets one small, self-hosted household/group per
	// instance (see CLAUDE.md), which in practice shares one time zone.
	// Resolved once at startup (see internal/handlers.Application.Location);
	// an unrecognized value falls back to UTC with a startup warning rather
	// than failing to start, the same graceful-degrade posture already used
	// for a broken OIDC discovery.
	AppTimeZone string

	// DefaultAppLanguage is the UI language (see static/locales/{fr,en}.json)
	// shown to any account that has never set its own preference — a brand
	// new registration (users.language starts out empty) and any
	// pre-existing account that never touched the Language section of the
	// "Paramètres" modal both resolve to this at read time (see
	// internal/handlers.resolveUserLanguage and models.User.Language),
	// rather than the instance's admin having to change it per account.
	// Defaults to "en"; an unrecognized value falls back to "en" too, the
	// same "invalid env var falls back to the fallback" convention as
	// envBool/envInt below.
	DefaultAppLanguage string

	// BackupWebDAVAllowPrivate lets the encrypted-backup feature (see
	// internal/backup) reach a WebDAV server on a private, loopback or
	// CGNAT/Tailscale address — the usual self-hosted setup, e.g. a
	// Nextcloud on the same LAN. Off by default: the WebDAV URL is entered
	// at runtime from the admin console, so the backup client applies the
	// same public-addresses-only SSRF guard as the scraper and Web Push
	// until the operator — who knows the deployment's network — opts in
	// here. Link-local addresses (including the cloud metadata endpoint)
	// stay blocked either way.
	BackupWebDAVAllowPrivate bool
}

func Load() Config {
	return Config{
		Port:      envOr("PORT", "8080"),
		DBPath:    envOr("DB_PATH", "/data/trakka.db"),
		StaticDir: envOr("STATIC_DIR", "/app/static"),

		TemplatesDir: envOr("TEMPLATES_DIR", "/app/templates"),
		BaseURL:      envOr("BASE_URL", ""),

		OIDCIssuer:       envOr("OIDC_ISSUER", ""),
		OIDCClientID:     envOr("OIDC_CLIENT_ID", ""),
		OIDCClientSecret: envOr("OIDC_CLIENT_SECRET", ""),

		SessionCookieSecure: envBool("SESSION_COOKIE_SECURE", true),
		SessionTTL:          time.Duration(envInt("SESSION_TTL_HOURS", 720)) * time.Hour,

		PriceCheckInterval: time.Duration(envInt("PRICE_CHECK_INTERVAL_HOURS", 24)) * time.Hour,

		TargetPriceScrapeInterval: envDuration("SCRAPE_INTERVAL", 12*time.Hour),

		InstanceName:     envOr("INSTANCE_NAME", "Trakka"),
		RegistrationOpen: envBool("REGISTRATION_OPEN", true),

		VAPIDPublicKey:  envOr("VAPID_PUBLIC_KEY", ""),
		VAPIDPrivateKey: envOr("VAPID_PRIVATE_KEY", ""),
		VAPIDSubject:    envOr("VAPID_SUBJECT", ""),

		NotifDueScanInterval: time.Duration(envInt("NOTIF_DUE_SCAN_INTERVAL_MINUTES", 1)) * time.Minute,

		AppTimeZone: envOr("APP_TIMEZONE", "Europe/Paris"),

		DefaultAppLanguage: envLanguage("DEFAULT_APP_LANGUAGE", "en"),

		BackupWebDAVAllowPrivate: envBool("BACKUP_WEBDAV_ALLOW_PRIVATE", false),
	}
}

// OIDCEnabled reports whether all three OIDC env vars are configured.
func (c Config) OIDCEnabled() bool {
	return c.OIDCIssuer != "" && c.OIDCClientID != "" && c.OIDCClientSecret != ""
}

// PushEnabled reports whether all three VAPID env vars are configured —
// checked before wiring up the push subscribe/vapid-public-key routes and
// the recurring-due-date scan (see cmd/server/main.go), mirroring how
// OIDCEnabled gates the OIDC client.
func (c Config) PushEnabled() bool {
	return c.VAPIDPublicKey != "" && c.VAPIDPrivateKey != "" && c.VAPIDSubject != ""
}

// Validate checks cross-field constraints Load() alone can't enforce.
func (c Config) Validate() error {
	set := 0
	for _, v := range []string{c.OIDCIssuer, c.OIDCClientID, c.OIDCClientSecret} {
		if v != "" {
			set++
		}
	}
	if set != 0 && set != 3 {
		return errors.New("OIDC_ISSUER, OIDC_CLIENT_ID and OIDC_CLIENT_SECRET must all be set together, or none of them")
	}
	if c.OIDCEnabled() && c.BaseURL == "" {
		return errors.New("BASE_URL is required when OIDC is configured")
	}

	vapidSet := 0
	for _, v := range []string{c.VAPIDPublicKey, c.VAPIDPrivateKey, c.VAPIDSubject} {
		if v != "" {
			vapidSet++
		}
	}
	if vapidSet != 0 && vapidSet != 3 {
		return errors.New("VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY and VAPID_SUBJECT must all be set together, or none of them")
	}

	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// envLanguage reads key and validates it against validate.SupportedLanguages
// (case-insensitively, trimmed), falling back — same as every other envXxx
// helper here — when the variable is unset or isn't one of them.
func envLanguage(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		if lang, ok := validate.Language(v); ok {
			return lang
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := parseDurationWithDays(v)
	if err != nil {
		return fallback
	}
	return d
}

// parseDurationWithDays extends time.ParseDuration with a whole-number "Nd"
// day suffix (e.g. "1d", "3d") — Go's own parser has no day unit, and
// SCRAPE_INTERVAL is meant to be set in days as often as in hours (see its
// own doc comment above), so a bare "1d" needs to work without operators
// having to spell out "24h" themselves.
func parseDurationWithDays(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid day duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
