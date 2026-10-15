package handlers

import (
	"context"
	"errors"
	"time"

	"trakka/internal/auth"
	"trakka/internal/settings"
)

// OIDC discovery that failed (the identity provider was down or not started
// yet when Trakka booted, a network hiccup after a restore) used to leave
// OIDC off until the next restart or admin PATCH — and, with it,
// oidcExclusive's safeguard brought password login back on an instance
// whose admin had turned it off. Discovery is now retried until it works,
// in the background (RetryOIDCDiscovery) and whenever someone starts an SSO
// sign-in (handleOIDCLogin), while password login stays as configured.

const (
	// Floor between two discovery attempts, whatever triggers them: an SSO
	// button pressed in a loop must not turn into a flood of requests to the
	// provider.
	oidcRetryMinInterval = 5 * time.Second
	// Background retry backoff, from the first to the slowest pace.
	oidcRetryFirstDelay = 10 * time.Second
	oidcRetryMaxDelay   = 5 * time.Minute
)

// oidcConfigured reports whether the settings ask for OIDC and hold a
// complete configuration, whether or not discovery has succeeded yet.
func (app *Application) oidcConfigured(current settings.Values) bool {
	return current.OIDCEnabled && current.OIDCIssuer != "" && current.OIDCClientID != "" &&
		current.OIDCClientSecret != "" && app.Config.BaseURL != ""
}

// errOIDCNotConfigured is ensureOIDC's error when the settings don't ask
// for OIDC, or not completely.
var errOIDCNotConfigured = errors.New("oidc is not enabled or not completely configured")

// ensureOIDC returns the live OIDC client, running discovery first when OIDC
// is configured but not live yet. An attempt already in progress is waited
// for rather than duplicated, and one made less than oidcRetryMinInterval
// ago is not repeated: its error is returned again. The error is
// errOIDCNotConfigured, or why discovery failed (auth.DiscoveryFailure).
func (app *Application) ensureOIDC(ctx context.Context, current settings.Values) (*auth.OIDCClient, error) {
	if client := app.Auth.OIDC(); client != nil {
		return client, nil
	}
	if !app.oidcConfigured(current) {
		return nil, errOIDCNotConfigured
	}
	app.oidcDiscoveryMu.Lock()
	defer app.oidcDiscoveryMu.Unlock()
	if client := app.Auth.OIDC(); client != nil {
		return client, nil
	}
	if time.Since(app.oidcDiscoveryLast) < oidcRetryMinInterval && app.oidcDiscoveryErr != nil {
		return nil, app.oidcDiscoveryErr
	}
	app.oidcDiscoveryLast = time.Now()

	discoveryCtx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()
	client, err := auth.NewOIDCClient(discoveryCtx, current.OIDCIssuer, current.OIDCClientID, current.OIDCClientSecret, app.Config.BaseURL+"/auth/oidc/callback")
	app.oidcDiscoveryErr = err
	if err != nil {
		reason, hint := auth.DiscoveryFailure(err)
		app.Logger.Warn("oidc discovery failed; OIDC sign-in unavailable until it succeeds",
			"issuer", current.OIDCIssuer, "reason", reason, "hint", hint, "error", err)
		return nil, err
	}
	// An admin may have changed or disabled OIDC while discovery ran: only
	// install a client for the configuration still in force.
	latest, err := settings.Resolve(ctx, app.DB, app.Config)
	if err != nil {
		return nil, err
	}
	if !app.oidcConfigured(latest) || latest.OIDCIssuer != current.OIDCIssuer ||
		latest.OIDCClientID != current.OIDCClientID || latest.OIDCClientSecret != current.OIDCClientSecret {
		return nil, errors.New("oidc settings changed during discovery")
	}
	app.Auth.SetOIDC(client)
	app.Logger.Info("oidc discovery succeeded; OIDC sign-in available", "issuer", current.OIDCIssuer)
	return client, nil
}

// RetryOIDCDiscovery retries OIDC discovery in the background, with backoff,
// until OIDC is live or no longer configured, or ctx ends. cmd/server starts
// it when discovery fails at startup.
func (app *Application) RetryOIDCDiscovery(ctx context.Context) {
	delay := oidcRetryFirstDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		current, err := settings.Resolve(ctx, app.DB, app.Config)
		if err != nil {
			app.Logger.Error("resolving settings for oidc discovery retry", "error", err)
		} else if app.Auth.OIDC() != nil || !app.oidcConfigured(current) {
			return
		} else if client, _ := app.ensureOIDC(ctx, current); client != nil {
			return
		}
		delay = min(delay*2, oidcRetryMaxDelay)
	}
}
