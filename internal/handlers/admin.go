package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"trakka/internal/auth"
	"trakka/internal/settings"
	"trakka/internal/validate"
)

// oidcDiscoveryTimeout bounds how long an admin PATCH that enables or
// reconfigures OIDC waits for the new issuer's discovery document + JWKS,
// the same reasoning as the scraper's syncScrapeTimeout: a slow or
// unreachable IdP must fail the request cleanly rather than hang it.
const oidcDiscoveryTimeout = 10 * time.Second

// adminSettingsView is the admin settings panel's JSON shape. It never
// includes the raw OIDC client secret — only whether one is currently set —
// so that a GET of this endpoint (e.g. a browser's dev tools, a server log
// of the response) can't leak it; PATCH still accepts a new value to set it
// blind, the same "write-only secret" pattern used by most admin panels
// that manage third-party API credentials.
type adminSettingsView struct {
	InstanceName        string `json:"instance_name"`
	RegistrationOpen    bool   `json:"registration_open"`
	OIDCEnabled         bool   `json:"oidc_enabled"`
	OIDCIssuer          string `json:"oidc_issuer"`
	OIDCClientID        string `json:"oidc_client_id"`
	OIDCClientSecretSet bool   `json:"oidc_client_secret_set"`
	OIDCExclusive       bool   `json:"oidc_exclusive"`
	OIDCProviderName    string `json:"oidc_provider_name"`
}

func adminSettingsViewFrom(v settings.Values) adminSettingsView {
	return adminSettingsView{
		InstanceName:        v.InstanceName,
		RegistrationOpen:    v.RegistrationOpen,
		OIDCEnabled:         v.OIDCEnabled,
		OIDCIssuer:          v.OIDCIssuer,
		OIDCClientID:        v.OIDCClientID,
		OIDCClientSecretSet: v.OIDCClientSecret != "",
		OIDCExclusive:       v.OIDCExclusive,
		OIDCProviderName:    v.OIDCProviderName,
	}
}

func (app *Application) handleAdminSettingsShow(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) {
		return
	}
	current, err := settings.Resolve(r.Context(), app.DB, app.Config)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminSettingsViewFrom(current))
}

// adminSettingsUpdate is the PATCH request body: every field is optional
// (a pointer, or for OIDCClientSecret a plain string treated as "leave
// unchanged" when empty) so a caller can update just one setting — e.g.
// toggling registration_open — without resending everything else.
type adminSettingsUpdate struct {
	InstanceName     *string `json:"instance_name"`
	RegistrationOpen *bool   `json:"registration_open"`
	OIDCEnabled      *bool   `json:"oidc_enabled"`
	OIDCIssuer       *string `json:"oidc_issuer"`
	OIDCClientID     *string `json:"oidc_client_id"`
	// OIDCClientSecret is only ever applied when non-nil AND non-empty:
	// there is no way to distinguish "the admin submitted an empty secret
	// field to intentionally blank it out" from "the admin left the
	// (always-blank, per adminSettingsView) secret field untouched", so —
	// consistent with treating it as write-only rather than round-tripped —
	// an empty value is always read as "unchanged". Disabling OIDC
	// altogether is done via OIDCEnabled, not by clearing the secret.
	OIDCClientSecret *string `json:"oidc_client_secret"`
	OIDCExclusive    *bool   `json:"oidc_exclusive"`
	OIDCProviderName *string `json:"oidc_provider_name"`
}

func (app *Application) handleAdminSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) {
		return
	}

	var body adminSettingsUpdate
	if !decodeJSON(w, r, &body) {
		return
	}

	current, err := settings.Resolve(r.Context(), app.DB, app.Config)
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	next := current
	if body.InstanceName != nil {
		next.InstanceName = validate.Text(*body.InstanceName)
	}
	if body.RegistrationOpen != nil {
		next.RegistrationOpen = *body.RegistrationOpen
	}
	if body.OIDCIssuer != nil {
		next.OIDCIssuer = strings.TrimSpace(*body.OIDCIssuer)
	}
	if body.OIDCClientID != nil {
		next.OIDCClientID = strings.TrimSpace(*body.OIDCClientID)
	}
	secretChanged := body.OIDCClientSecret != nil && *body.OIDCClientSecret != ""
	if secretChanged {
		next.OIDCClientSecret = *body.OIDCClientSecret
	}
	if body.OIDCEnabled != nil {
		next.OIDCEnabled = *body.OIDCEnabled
	}
	if body.OIDCExclusive != nil {
		next.OIDCExclusive = *body.OIDCExclusive
	}
	if body.OIDCProviderName != nil {
		next.OIDCProviderName = validate.Text(*body.OIDCProviderName)
	}

	if next.InstanceName == "" {
		writeError(w, http.StatusBadRequest, "instance_name cannot be empty")
		return
	}
	if !validate.MaxLen(next.InstanceName, validate.MaxNameLen) {
		writeError(w, http.StatusBadRequest, "instance_name is too long")
		return
	}
	if !validate.MaxLen(next.OIDCProviderName, validate.MaxNameLen) {
		writeError(w, http.StatusBadRequest, "oidc_provider_name is too long")
		return
	}
	// Lockout prevention: OIDC-exclusive mode needs OIDC itself and at least
	// one admin able to sign in through it (with the resulting issuer), since
	// there is no CLI to grant the admin role back.
	if next.OIDCExclusive {
		if !next.OIDCEnabled {
			writeError(w, http.StatusBadRequest, "Impossible d'activer le mode OIDC exclusif : la connexion OIDC doit être activée.")
			return
		}
		n, err := app.DB.CountOIDCAdmins(r.Context(), next.OIDCIssuer)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
		if n == 0 {
			writeError(w, http.StatusBadRequest, "Impossible d'activer le mode OIDC exclusif : aucun administrateur n'est lié à un compte OIDC.")
			return
		}
	}

	// Discovery only runs when the OIDC configuration itself changes (OIDC
	// switched on, or a new issuer, client ID or secret). The admin console
	// resends every field on each save, and re-running discovery on an
	// unchanged configuration made every save — turning oidc_exclusive off
	// included, the way back to password login — wait the full timeout and
	// fail while the provider was unreachable. An unchanged configuration
	// keeps its live client, or, if discovery has not succeeded yet, the
	// retries already in place (oidc_discovery.go).
	oidcChanged := next.OIDCEnabled && (!current.OIDCEnabled || next.OIDCIssuer != current.OIDCIssuer ||
		next.OIDCClientID != current.OIDCClientID || next.OIDCClientSecret != current.OIDCClientSecret)
	var newClient *auth.OIDCClient
	if next.OIDCEnabled {
		if next.OIDCIssuer == "" || next.OIDCClientID == "" || next.OIDCClientSecret == "" {
			writeError(w, http.StatusBadRequest, "oidc_issuer, oidc_client_id and oidc_client_secret must all be set to enable OIDC")
			return
		}
		if app.Config.BaseURL == "" {
			writeError(w, http.StatusBadRequest, "BASE_URL must be configured on the server (as an environment variable) to enable OIDC")
			return
		}
	}
	if oidcChanged {
		ctx, cancel := context.WithTimeout(r.Context(), oidcDiscoveryTimeout)
		defer cancel()
		newClient, err = auth.NewOIDCClient(ctx, next.OIDCIssuer, next.OIDCClientID, next.OIDCClientSecret, app.Config.BaseURL+"/auth/oidc/callback")
		if err != nil {
			reason, hint := auth.DiscoveryFailure(err)
			app.Logger.Warn("admin settings not saved: oidc discovery failed",
				"issuer", next.OIDCIssuer, "reason", reason, "hint", hint, "error", err)
			writeError(w, http.StatusBadRequest, oidcDiscoveryErrorMessage(err))
			return
		}
	}

	// Persist only after a successful discovery above (when the OIDC
	// configuration changed), so a broken issuer/client configuration is
	// never saved — the previous, known-good settings (and OIDC client) stay
	// in effect until a working configuration is submitted.
	updates := map[string]string{
		settings.KeyInstanceName:     next.InstanceName,
		settings.KeyRegistrationOpen: strconv.FormatBool(next.RegistrationOpen),
		settings.KeyOIDCEnabled:      strconv.FormatBool(next.OIDCEnabled),
		settings.KeyOIDCIssuer:       next.OIDCIssuer,
		settings.KeyOIDCClientID:     next.OIDCClientID,
		settings.KeyOIDCExclusive:    strconv.FormatBool(next.OIDCExclusive),
		settings.KeyOIDCProviderName: next.OIDCProviderName,
	}
	if secretChanged {
		updates[settings.KeyOIDCClientSecret] = next.OIDCClientSecret
	}
	if err := app.DB.SetSettings(r.Context(), updates); err != nil {
		app.serverError(w, r, err)
		return
	}

	switch {
	case oidcChanged:
		app.Auth.SetOIDC(newClient)
	case !next.OIDCEnabled:
		app.Auth.SetOIDC(nil)
	}

	writeJSON(w, http.StatusOK, adminSettingsViewFrom(next))
}

// oidcDiscoveryErrorMessage is the PATCH's 400 message when discovery of a
// new OIDC configuration fails: the reason (a timeout spelled out as such)
// and what to check, so the admin console shows something actionable.
func oidcDiscoveryErrorMessage(err error) string {
	reason, hint := auth.DiscoveryFailure(err)
	if reason == "timeout" {
		return fmt.Sprintf("OIDC discovery timed out after %s: %s", oidcDiscoveryTimeout, hint)
	}
	return fmt.Sprintf("OIDC discovery failed (%s): %v — %s", reason, err, hint)
}
