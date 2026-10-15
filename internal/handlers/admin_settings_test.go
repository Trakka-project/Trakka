package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"trakka/internal/models"
	"trakka/internal/settings"
)

// hangingProvider stands for an identity provider that accepts connections
// but never answers (down behind a proxy, a firewall dropping packets): any
// discovery against it would wait out oidcDiscoveryTimeout. It counts the
// requests it receives and is trusted by the default transport for the
// duration of the test.
func hangingProvider(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	previous := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	return srv, hits
}

func patchAdminSettings(app *Application, admin *models.User, body string) *httptest.ResponseRecorder {
	req := withUser(httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", strings.NewReader(body)), admin)
	rec := httptest.NewRecorder()
	app.handleAdminSettingsUpdate(rec, req)
	return rec
}

// Turning oidc_exclusive off — the way back to password login — must save
// at once while the provider is unreachable: the OIDC configuration is
// unchanged, so it is not re-discovered. Saving used to wait the full 10s
// discovery timeout and answer 400, leaving password login off.
func TestAdminSettingsDisableOIDCExclusiveWithProviderUnreachable(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("client_live=%v", live), func(t *testing.T) {
			provider, hits := hangingProvider(t)
			app := newTestApplication(t)
			app.Config.BaseURL = "https://trakka.example.com"
			withOIDC(app, live)
			admin := mustCreateTestUser(t, app, "admin@example.com")
			issuer := provider.URL + "/application/o/trakka/"
			if err := app.DB.SetSettings(context.Background(), map[string]string{
				settings.KeyOIDCEnabled:      "true",
				settings.KeyOIDCIssuer:       issuer,
				settings.KeyOIDCClientID:     "trakka",
				settings.KeyOIDCClientSecret: "secret",
				settings.KeyOIDCExclusive:    "true",
			}); err != nil {
				t.Fatal(err)
			}
			liveClient := app.Auth.OIDC()

			// Every field, as the admin console sends them; the secret is
			// left out (blank field = unchanged).
			body := fmt.Sprintf(`{"instance_name":"Trakka","registration_open":false,"oidc_enabled":true,`+
				`"oidc_issuer":%q,"oidc_client_id":"trakka","oidc_exclusive":false,"oidc_provider_name":"Authentik"}`, issuer)
			start := time.Now()
			rec := patchAdminSettings(app, admin, body)
			elapsed := time.Since(start)

			if rec.Code != http.StatusOK {
				t.Fatalf("got %d (%s), want 200", rec.Code, rec.Body.String())
			}
			if elapsed >= oidcDiscoveryTimeout/2 {
				t.Errorf("save took %s: it waited on the unreachable provider", elapsed)
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("provider contacted %d times for an unchanged OIDC configuration", n)
			}
			saved, err := settings.Resolve(context.Background(), app.DB, app.Config)
			if err != nil {
				t.Fatal(err)
			}
			if saved.OIDCExclusive || !saved.OIDCEnabled || saved.OIDCClientSecret != "secret" {
				t.Errorf("saved exclusive=%v enabled=%v secret kept=%v, want false/true/true",
					saved.OIDCExclusive, saved.OIDCEnabled, saved.OIDCClientSecret == "secret")
			}
			if app.Auth.OIDC() != liveClient {
				t.Error("the OIDC client in force changed although its configuration did not")
			}
		})
	}
}

// A new issuer that can't be reached is still refused (saving it untested
// could lock everyone out under oidc_exclusive), with a 400 that says why
// rather than a bare discovery error, and nothing saved.
func TestAdminSettingsNewUnreachableIssuerExplainsWhy(t *testing.T) {
	down := httptest.NewTLSServer(http.NotFoundHandler())
	issuer := down.URL + "/application/o/trakka/"
	down.Close()
	app := newTestApplication(t)
	app.Config.BaseURL = "https://trakka.example.com"
	withOIDC(app, false)
	admin := mustCreateTestUser(t, app, "admin@example.com")

	body := fmt.Sprintf(`{"instance_name":"Trakka","oidc_enabled":true,"oidc_issuer":%q,"oidc_client_id":"trakka","oidc_client_secret":"secret"}`, issuer)
	rec := patchAdminSettings(app, admin, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d (%s), want 400", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Error, "OIDC discovery failed (connection_refused): ") {
		t.Errorf("error %q does not name the failure", resp.Error)
	}
	saved, err := settings.Resolve(context.Background(), app.DB, app.Config)
	if err != nil {
		t.Fatal(err)
	}
	if saved.OIDCEnabled || saved.OIDCIssuer == issuer {
		t.Error("a configuration whose discovery failed was saved")
	}
}

func TestOIDCDiscoveryErrorMessageSpellsOutTimeouts(t *testing.T) {
	msg := oidcDiscoveryErrorMessage(fmt.Errorf("fetching discovery document: %w", context.DeadlineExceeded))
	if !strings.HasPrefix(msg, "OIDC discovery timed out after 10s: ") {
		t.Errorf("got %q", msg)
	}
}
