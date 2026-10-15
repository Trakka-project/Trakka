package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// clockSkew is the tolerance applied when checking id_token exp/iat, to
// absorb minor clock drift between Trakka and the OIDC provider.
const clockSkew = 60 * time.Second

// jwksCacheTTL controls how long fetched JWKS keys are trusted before a
// routine refresh; an unknown kid always forces one immediate refetch
// regardless of this TTL, to handle key rotation promptly.
const jwksCacheTTL = time.Hour

// maxOIDCResponseBytes caps every response body read from the identity
// provider (discovery document, token response, JWKS). These are all small
// JSON documents; without a cap, a compromised, hostile, or merely
// misbehaving IdP could stream an unbounded body into memory and take the
// process down — the provider is trusted to assert identity, which is not
// the same as being trusted with the server's memory.
const maxOIDCResponseBytes = 1 << 20 // 1 MiB

// minRSAKeyBits is the smallest id_token signing key this client will accept
// from a JWKS. RS256 says nothing about modulus size, so a JWKS advertising a
// 512-bit key would otherwise be honored — and a key that small can be
// factored, letting anyone who can serve or tamper with that JWKS mint
// id_tokens this server would verify happily. 2048 is the floor every current
// recommendation (NIST SP 800-57, RFC 7518 §3.3) sets for RSA signatures.
const minRSAKeyBits = 2048

// ErrInsecureIssuer is returned when an issuer URL is not https and the
// operator has not explicitly opted into plaintext.
var ErrInsecureIssuer = errors.New("oidc issuer must be an https URL (set OIDC_ALLOW_INSECURE_ISSUER=true to allow http, e.g. for a provider reachable only inside a private container network)")

// allowInsecureIssuer is the opt-in escape hatch for an IdP reachable only
// over plain HTTP — typically an Authelia/Keycloak container on the same
// private Docker network, where there is no public path to intercept. It is
// read once at package initialization from the environment rather than
// threaded through config, because auth.NewOIDCClient is called both from
// cmd/server at startup and from the admin settings endpoint at runtime, and
// this is a deployment-level property of the network, never a per-request or
// per-admin-action choice.
var allowInsecureIssuer = os.Getenv("OIDC_ALLOW_INSECURE_ISSUER") == "true"

// validateIssuerURL enforces the transport requirement on an issuer URL.
// Discovery, the JWKS fetch, and the token exchange all happen against
// endpoints derived from this URL: over plaintext http, anyone on the path
// can swap the JWKS for keys they hold and forge an id_token for any account
// on the instance, so https is the default and http is opt-in only.
func validateIssuerURL(issuer string) error {
	parsed, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("parsing issuer url: %w", err)
	}
	if parsed.Host == "" {
		return errors.New("oidc issuer must be an absolute URL with a host")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		if allowInsecureIssuer {
			return nil
		}
		return ErrInsecureIssuer
	default:
		return fmt.Errorf("unsupported oidc issuer scheme %q", parsed.Scheme)
	}
}

// ProviderConfig is the subset of an OIDC discovery document Trakka needs.
type ProviderConfig struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwks struct {
	Keys []jwk `json:"keys"`
}

// OIDCClient is a minimal, hand-rolled OIDC Authorization Code + PKCE
// client: discovery, authorization URL construction, code exchange, and
// RS256 id_token verification against the provider's JWKS. Deliberately
// stdlib-only (no coreos/go-oidc or golang.org/x/oauth2), to keep Trakka's
// only new dependency at bcrypt.
type OIDCClient struct {
	httpClient   *http.Client
	issuer       string
	clientID     string
	clientSecret string
	redirectURI  string

	provider *ProviderConfig

	mu     sync.RWMutex
	keys   map[string]*rsa.PublicKey
	keysAt time.Time
}

// NewOIDCClient runs OIDC discovery eagerly, so a broken OIDC configuration
// fails the process at startup rather than on a user's first login.
func NewOIDCClient(ctx context.Context, issuer, clientID, clientSecret, redirectURI string) (*OIDCClient, error) {
	if err := validateIssuerURL(issuer); err != nil {
		return nil, err
	}
	c := &OIDCClient{
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		issuer:       issuer,
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  redirectURI,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, fmt.Errorf("building discovery request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching discovery document: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &DiscoveryStatusError{Status: resp.StatusCode}
	}

	var provider ProviderConfig
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOIDCResponseBytes)).Decode(&provider); err != nil {
		return nil, fmt.Errorf("decoding discovery document: %w", err)
	}
	if provider.Issuer != issuer {
		return nil, fmt.Errorf("%w: the document says %q, the configured issuer is %q", ErrIssuerMismatch, provider.Issuer, issuer)
	}
	if provider.AuthorizationEndpoint == "" || provider.TokenEndpoint == "" || provider.JWKSURI == "" {
		return nil, errors.New("discovery document is missing required endpoints")
	}
	// The discovery document is fetched from the issuer but its contents are
	// still the issuer's own claim about where to go next: hold each endpoint
	// it names to the same transport requirement as the issuer URL itself, so
	// a document served over https cannot redirect the token exchange or the
	// JWKS fetch onto plaintext http.
	for name, endpoint := range map[string]string{
		"authorization_endpoint": provider.AuthorizationEndpoint,
		"token_endpoint":         provider.TokenEndpoint,
		"jwks_uri":               provider.JWKSURI,
	} {
		if err := validateIssuerURL(endpoint); err != nil {
			return nil, fmt.Errorf("discovery document %s: %w", name, err)
		}
	}
	c.provider = &provider

	return c, nil
}

// AuthorizationURL builds the URL to redirect the user to at the IdP.
func (c *OIDCClient) AuthorizationURL(state, nonce, codeChallenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.clientID},
		"redirect_uri":          {c.redirectURI},
		"scope":                 {"openid email profile"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	return c.provider.AuthorizationEndpoint + "?" + q.Encode()
}

type tokenResponse struct {
	IDToken string `json:"id_token"`
}

// Exchange trades an authorization code for an id_token and returns its
// verified claims.
func (c *OIDCClient) Exchange(ctx context.Context, code, codeVerifier string) (*IDTokenClaims, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.redirectURI},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code_verifier": {codeVerifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.provider.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOIDCResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decoding token response: %w", err)
	}
	if tr.IDToken == "" {
		return nil, errors.New("token response did not include an id_token")
	}

	return c.VerifyIDToken(ctx, tr.IDToken)
}

// VerifyIDToken verifies an id_token's RS256 signature against the
// provider's JWKS and validates its issuer, audience, and expiry. It does
// not check the nonce — the caller compares that against the value it
// stashed before starting the flow.
func (c *OIDCClient) VerifyIDToken(ctx context.Context, rawIDToken string) (*IDTokenClaims, error) {
	claims, err := verifyIDToken(rawIDToken, func(kid string) (*rsa.PublicKey, error) {
		return c.getKey(ctx, kid)
	})
	if err != nil {
		return nil, err
	}

	if claims.Issuer != c.issuer {
		return nil, fmt.Errorf("id_token issuer %q does not match expected %q", claims.Issuer, c.issuer)
	}
	if !containsString(claims.Audience, c.clientID) {
		return nil, errors.New("id_token audience does not include this client")
	}
	now := time.Now()
	if now.After(time.Unix(claims.Expiry, 0).Add(clockSkew)) {
		return nil, errors.New("id_token has expired")
	}
	if time.Unix(claims.IssuedAt, 0).After(now.Add(clockSkew)) {
		return nil, errors.New("id_token issued in the future")
	}

	return claims, nil
}

func (c *OIDCClient) getKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	key, ok := c.keys[kid]
	fresh := time.Since(c.keysAt) < jwksCacheTTL
	c.mu.RUnlock()
	if ok && fresh {
		return key, nil
	}

	if err := c.refreshKeys(ctx); err != nil {
		return nil, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	key, ok = c.keys[kid]
	if !ok {
		return nil, fmt.Errorf("no signing key found for kid %q", kid)
	}
	return key, nil
}

func (c *OIDCClient) refreshKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.provider.JWKSURI, nil)
	if err != nil {
		return fmt.Errorf("building JWKS request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS endpoint returned %d", resp.StatusCode)
	}

	var set jwks
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOIDCResponseBytes)).Decode(&set); err != nil {
		return fmt.Errorf("decoding JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		key := &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(new(big.Int).SetBytes(eBytes).Int64()),
		}
		// Skip keys that are too weak to trust, or whose exponent is outside
		// the sane range: an attacker who can influence the JWKS (a hostile
		// provider, or anyone on the path of a plaintext jwks_uri) would
		// otherwise be able to publish a forgeable key and have every
		// id_token signed with it accepted.
		if key.N.BitLen() < minRSAKeyBits || key.E < 3 || key.E%2 == 0 {
			continue
		}
		keys[k.Kid] = key
	}

	c.mu.Lock()
	c.keys = keys
	c.keysAt = time.Now()
	c.mu.Unlock()
	return nil
}
