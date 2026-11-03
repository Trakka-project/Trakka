package fluv

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Timeouts of one exchange with Fluv.
const (
	dialTimeout    = 5 * time.Second
	requestTimeout = 15 * time.Second
	// maxResponseBody is all of a response that is ever read: Fluv answers
	// a small JSON object or problem document.
	maxResponseBody = 4 << 10
)

// errBlockedAddress is what the pinned dialer returns when no pinned
// address passes the policy any more (the operator narrowed it since).
var errBlockedAddress = errors.New("no pinned address is allowed by FLUV_INTEGRATION_ALLOWED_IPS")

// pinnedDialer is the IP lock: it connects to the pinned addresses only,
// in order, on the URL's port, whatever host name the transport asks for —
// the host name is never resolved, so neither DNS nor a rebinding attack
// can send the token elsewhere. TLS still verifies the certificate against
// the URL's host name (the transport sets ServerName from the URL, not
// from the address dialed), so the lock adds to certificate checking
// rather than replacing it. Every address is checked against the policy
// again here, at each dial.
type pinnedDialer struct {
	pins   []netip.Addr
	port   string
	policy Policy
}

func (d pinnedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("splitting address %q: %w", addr, err)
	}
	if port != d.port {
		return nil, fmt.Errorf("refusing to dial port %s: the endpoint is on port %s", port, d.port)
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	var lastErr error
	for _, ip := range d.pins {
		if !d.policy.allows(ip) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, netip.AddrPortFrom(ip, portNumber(port)).String())
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errBlockedAddress
	}
	return nil, lastErr
}

func portNumber(port string) uint16 {
	n := 0
	for _, c := range port {
		n = n*10 + int(c-'0')
	}
	return uint16(n) // #nosec G115 -- validTarget/endpointPort only ever yield 1–65535
}

// endpointPort is the port the URL names, or its scheme's default.
func endpointPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "http" {
		return "80"
	}
	return "443"
}

// newClient builds the client of one exchange with t: the pinned dialer,
// no proxy from the environment (it would see the token, and decide where
// to connect), no redirects (which would resend the token to wherever
// Location points), no connection kept open afterwards.
func newClient(t *target, policy Policy) *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            pinnedDialer{pins: t.pins, port: endpointPort(t.endpoint), policy: policy}.DialContext,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    dialTimeout,
			ResponseHeaderTimeout:  requestTimeout,
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: 16 << 10,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// response is what is kept of one of Fluv's answers.
type response struct {
	status int
	// code is the "code" of a problem document (RFC 9457, as Fluv writes
	// them), "" when there is none.
	code string
	// tooLarge: the body went past what the caller reads.
	tooLarge bool
}

// newRequest builds one request to Fluv with the token. The token only ever
// goes in the Authorization header of a request to a pinned address; it is
// never logged, nor is the body.
func newRequest(ctx context.Context, method, rawURL, token string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Trakka")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// send makes one exchange and reads at most limit bytes of the answer,
// keeping the problem code of one that isn't a success.
func send(client *http.Client, req *http.Request, limit int64) (response, []byte, error) {
	resp, err := client.Do(req) // #nosec G704 -- the client only dials pinned, policy-checked addresses (pinnedDialer)
	if err != nil {
		return response{}, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out := response{status: resp.StatusCode}
	// A body cut short is not an error: the status is what counts, and a
	// JSON answer cut short doesn't decode.
	data, _ := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if int64(len(data)) > limit {
		data = nil
		out.tooLarge = true
	}
	if resp.StatusCode >= 300 {
		var problem struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(data, &problem) == nil {
			out.code = cleanCode(problem.Code)
		}
	}
	return out, data, nil
}

// do sends one request to Fluv with the token and returns its status and
// problem code.
func do(ctx context.Context, client *http.Client, method, rawURL, token string, body any) (response, error) {
	req, err := newRequest(ctx, method, rawURL, token, body)
	if err != nil {
		return response{}, err
	}
	out, _, err := send(client, req, maxResponseBody)
	return out, err
}

// getJSON sends one GET to Fluv with the token, in lang (Accept-Language),
// and decodes a 200 answer of at most limit bytes into dst. A 200 that is
// too large or isn't the JSON expected comes back with code
// "invalid_response"; any other status with its problem code, as do.
func getJSON(ctx context.Context, client *http.Client, rawURL, token, lang string, limit int64, dst any) (response, error) {
	req, err := newRequest(ctx, http.MethodGet, rawURL, token, nil)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Accept-Language", lang)
	out, data, err := send(client, req, limit)
	if err != nil || out.status != http.StatusOK {
		return out, err
	}
	if out.tooLarge || json.Unmarshal(data, dst) != nil {
		out.code = "invalid_response"
	}
	return out, nil
}

// cleanCode keeps a problem code only if it looks like one (lowercase
// letters and underscores), so nothing Fluv sends ends up verbatim in the
// database, the logs or the settings screen.
func cleanCode(code string) string {
	if code == "" || len(code) > 40 {
		return ""
	}
	for _, c := range code {
		if (c < 'a' || c > 'z') && c != '_' {
			return ""
		}
	}
	return code
}

// networkErrorCode classifies a failed exchange for the settings screen.
func networkErrorCode(err error) string {
	var netErr net.Error
	var certErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	switch {
	case errors.Is(err, errBlockedAddress):
		return "blocked_address"
	case errors.As(err, &certErr), errors.As(err, &recordErr), strings.Contains(err.Error(), "tls:"):
		return "tls_error"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	default:
		return "unreachable"
	}
}
