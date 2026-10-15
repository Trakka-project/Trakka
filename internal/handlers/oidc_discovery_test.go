package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"trakka/internal/settings"
)

// fakeProvider serves an OIDC discovery document over HTTPS, and makes the
// default transport (which auth.NewOIDCClient's client uses) trust it for
// the duration of the test.
func fakeProvider(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/application/o/trakka/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		issuer := srv.URL + "/application/o/trakka/"
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": srv.URL + "/application/o/authorize/",
			"token_endpoint":         srv.URL + "/application/o/token/",
			"jwks_uri":               srv.URL + "/application/o/trakka/jwks/",
		})
	}))
	t.Cleanup(srv.Close)
	previous := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	return srv
}

// An SSO sign-in started while OIDC isn't live yet (discovery failed at boot)
// runs discovery on the spot, then sends the browser straight to the
// provider: no Trakka page in between, from the app's Custom Tab as from a
// browser.
func TestOIDCLoginDiscoversThenRedirectsStraightToTheProvider(t *testing.T) {
	provider := fakeProvider(t)
	app := newTestApplication(t)
	app.Config.BaseURL = "https://trakka.example.com"
	withOIDC(app, false)
	if err := app.DB.SetSettings(context.Background(), map[string]string{
		settings.KeyOIDCEnabled:      "true",
		settings.KeyOIDCIssuer:       provider.URL + "/application/o/trakka/",
		settings.KeyOIDCClientID:     "trakka",
		settings.KeyOIDCClientSecret: "secret",
	}); err != nil {
		t.Fatal(err)
	}

	challenge := strings.Repeat("A", 43)
	rec := httptest.NewRecorder()
	app.handleOIDCLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/oidc/login?app_challenge="+challenge, nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("got %d, want 302 to the provider", rec.Code)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := location.Scheme+"://"+location.Host+location.Path, provider.URL+"/application/o/authorize/"; got != want {
		t.Fatalf("redirected to %q, want the provider's authorization endpoint %q", got, want)
	}
	query := location.Query()
	if query.Get("client_id") != "trakka" || query.Get("redirect_uri") != "https://trakka.example.com/auth/oidc/callback" ||
		query.Get("code_challenge") == "" || query.Get("state") == "" {
		t.Errorf("authorization request missing parameters: %v", query)
	}
	if app.Auth.OIDC() == nil {
		t.Error("discovery succeeded but the OIDC client was not installed")
	}
	var flow string
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == oidcFlowCookieName {
			flow = cookie.Value
		}
	}
	if values, _ := url.ParseQuery(flow); values.Get("app_challenge") != challenge {
		t.Errorf("flow cookie %q does not carry the app's challenge", flow)
	}
}
