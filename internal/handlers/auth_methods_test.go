package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"trakka/internal/auth"
	"trakka/internal/settings"
)

func getAuthMethods(t *testing.T, app *Application) authMethods {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handleAuthMethods(rec, httptest.NewRequest(http.MethodGet, "/auth/methods", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /auth/methods: got %d, want 200", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /auth/methods must not be cacheable, got Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	var methods authMethods
	if err := json.NewDecoder(rec.Body).Decode(&methods); err != nil {
		t.Fatalf("decoding /auth/methods: %v", err)
	}
	return methods
}

func TestAuthMethodsMirrorTheSignInPage(t *testing.T) {
	ctx := context.Background()

	// Without OIDC: password only, whatever the stored provider name.
	app := newTestApplication(t)
	if err := app.DB.SetSettings(ctx, map[string]string{
		settings.KeyRegistrationOpen: "true",
		settings.KeyOIDCProviderName: "Authentik",
	}); err != nil {
		t.Fatal(err)
	}
	withOIDC(app, false)
	if got, want := getAuthMethods(t, app), (authMethods{Password: true, Registration: true}); got != want {
		t.Errorf("without OIDC: got %+v, want %+v", got, want)
	}

	// OIDC alongside passwords.
	withOIDC(app, true)
	if got, want := getAuthMethods(t, app), (authMethods{Password: true, OIDC: true, OIDCProviderName: "Authentik", Registration: true}); got != want {
		t.Errorf("with OIDC: got %+v, want %+v", got, want)
	}

	// OIDC exclusive, with an admin linked to the issuer: no password, no registration.
	const issuer = "https://idp.example.com"
	subject, iss := "admin-subject", issuer
	admin, err := app.DB.CreateUser(ctx, "admin@example.com", nil, &subject, &iss, "Admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.SetUserAdmin(ctx, admin.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.SetSettings(ctx, map[string]string{
		settings.KeyOIDCExclusive: "true",
		settings.KeyOIDCIssuer:    issuer,
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := getAuthMethods(t, app), (authMethods{OIDC: true, OIDCProviderName: "Authentik"}); got != want {
		t.Errorf("OIDC exclusive: got %+v, want %+v", got, want)
	}

	// Exclusive and configured, but discovery failed (the provider was down at boot): passwords
	// stay off, and the SSO button stays, retrying discovery.
	app.Config.BaseURL = "https://trakka.example.com"
	if err := app.DB.SetSettings(ctx, map[string]string{
		settings.KeyOIDCEnabled:      "true",
		settings.KeyOIDCClientID:     "trakka",
		settings.KeyOIDCClientSecret: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	withOIDC(app, false)
	if got, want := getAuthMethods(t, app), (authMethods{OIDC: true, OIDCProviderName: "Authentik"}); got != want {
		t.Errorf("OIDC exclusive, provider unreachable: got %+v, want %+v", got, want)
	}

	// OIDC switched off (or no longer completely configured): passwords come back rather than
	// locking every administrator out.
	if err := app.DB.SetSettings(ctx, map[string]string{settings.KeyOIDCEnabled: "false"}); err != nil {
		t.Fatal(err)
	}
	if got, want := getAuthMethods(t, app), (authMethods{Password: true, Registration: true}); got != want {
		t.Errorf("OIDC exclusive but switched off: got %+v, want %+v", got, want)
	}
}

func TestOIDCLoginWhileTheProviderIsUnreachable(t *testing.T) {
	ctx := context.Background()
	app := newTestApplication(t)
	app.Config.BaseURL = "https://trakka.example.com"
	withOIDC(app, false)
	// A discovery attempt just failed: this one is skipped rather than sent to the provider.
	app.oidcDiscoveryLast = time.Now()
	app.oidcDiscoveryErr = &auth.DiscoveryStatusError{Status: http.StatusBadGateway}

	login := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		app.handleOIDCLogin(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}

	// Not configured: no SSO at all.
	if rec := login("/auth/oidc/login"); rec.Code != http.StatusNotFound {
		t.Fatalf("OIDC not configured: got %d, want 404", rec.Code)
	}

	if err := app.DB.SetSettings(ctx, map[string]string{
		settings.KeyOIDCEnabled:      "true",
		settings.KeyOIDCIssuer:       "https://idp.example.com",
		settings.KeyOIDCClientID:     "trakka",
		settings.KeyOIDCClientSecret: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	rec := login("/auth/oidc/login")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/auth/login?error=oidc_unavailable" {
		t.Errorf("provider unreachable: got %d to %q, want a redirect to the sign-in page's error", rec.Code, rec.Header().Get("Location"))
	}
	rec = login("/auth/oidc/login?app_challenge=" + strings.Repeat("A", 43))
	if want := appSSOReturnURL + "?error=oidc_unavailable"; rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Errorf("provider unreachable, from the app: got %d to %q, want %q", rec.Code, rec.Header().Get("Location"), want)
	}
}

// withOIDC gives app an auth service, with or without a (never contacted) OIDC client.
func withOIDC(app *Application, enabled bool) {
	var client *auth.OIDCClient
	if enabled {
		client = &auth.OIDCClient{}
	}
	app.Auth = auth.NewService(app.DB, client, time.Hour, false)
}
