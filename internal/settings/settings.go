// Package settings resolves Trakka's dynamically configurable runtime
// settings — OIDC/SSO configuration, whether local registration is open,
// and the instance's display name — by merging whatever is stored in the
// system_settings table over the equivalent environment variable from
// internal/config, per key. A key with no row in system_settings falls
// back to its env var (or that var's own default); once a row exists it
// wins, exactly as CLAUDE.md's admin-settings design calls for. This is the
// single place both cmd/server/main.go (constructing the OIDC client at
// startup) and internal/handlers (the admin settings endpoint, the login
// page, the registration gate) read from, so they can never disagree about
// what's actually in effect right now.
package settings

import (
	"context"
	"fmt"
	"strconv"

	"trakka/internal/config"
	"trakka/internal/db"
)

// Settings keys, as stored in the system_settings table.
const (
	KeyInstanceName     = "instance_name"
	KeyRegistrationOpen = "registration_open"
	KeyOIDCEnabled      = "oidc_enabled"
	KeyOIDCIssuer       = "oidc_issuer"
	KeyOIDCClientID     = "oidc_client_id"
	KeyOIDCClientSecret = "oidc_client_secret" // #nosec G101 -- this is the settings-table key name, not a credential value
	KeyOIDCExclusive    = "oidc_exclusive"
	KeyOIDCProviderName = "oidc_provider_name"
)

// Values is the fully resolved set of runtime settings. OIDCClientSecret is
// the real secret value (needed to actually construct an auth.OIDCClient) —
// callers that expose Values to an admin over the API must redact it
// themselves (see internal/handlers/admin.go), never serialize it directly.
type Values struct {
	InstanceName     string
	RegistrationOpen bool
	OIDCEnabled      bool
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCExclusive    bool
	OIDCProviderName string // login button label; empty means the generic "SSO"
}

// Resolve reads system_settings and merges it over cfg's env-var defaults.
func Resolve(ctx context.Context, database *db.DB, cfg config.Config) (Values, error) {
	stored, err := database.GetAllSettings(ctx)
	if err != nil {
		return Values{}, fmt.Errorf("resolving settings: %w", err)
	}

	v := Values{
		InstanceName:     stringSetting(stored, KeyInstanceName, cfg.InstanceName),
		RegistrationOpen: boolSetting(stored, KeyRegistrationOpen, cfg.RegistrationOpen),
		OIDCEnabled:      boolSetting(stored, KeyOIDCEnabled, cfg.OIDCEnabled()),
		OIDCIssuer:       stringSetting(stored, KeyOIDCIssuer, cfg.OIDCIssuer),
		OIDCClientID:     stringSetting(stored, KeyOIDCClientID, cfg.OIDCClientID),
		OIDCClientSecret: stringSetting(stored, KeyOIDCClientSecret, cfg.OIDCClientSecret),
		OIDCExclusive:    boolSetting(stored, KeyOIDCExclusive, false),
		OIDCProviderName: stringSetting(stored, KeyOIDCProviderName, ""),
	}
	return v, nil
}

func stringSetting(stored map[string]string, key, fallback string) string {
	if v, ok := stored[key]; ok {
		return v
	}
	return fallback
}

func boolSetting(stored map[string]string, key string, fallback bool) bool {
	raw, ok := stored[key]
	if !ok {
		return fallback
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return b
}
