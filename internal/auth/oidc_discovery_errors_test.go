package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
)

func TestDiscoveryFailureReasons(t *testing.T) {
	mismatch := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		// Authentik's issuer ends with a slash; the configured one below doesn't.
		_, _ = fmt.Fprintf(w, `{"issuer":%q}`, "https://"+r.Host+"/")
	}))
	defer mismatch.Close()

	discover := func(issuer string, trusted bool) error {
		t.Helper()
		previous := http.DefaultTransport
		if trusted {
			http.DefaultTransport = mismatch.Client().Transport
		}
		defer func() { http.DefaultTransport = previous }()
		_, err := NewOIDCClient(context.Background(), issuer, "id", "secret", "https://trakka.example.com/auth/oidc/callback")
		if err == nil {
			t.Fatalf("discovery of %s succeeded, want an error", issuer)
		}
		return err
	}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"issuer mismatch", discover(mismatch.URL, true), "issuer_mismatch"},
		{"not a discovery document", discover(mismatch.URL+"/nowhere", true), "http_status"},
		{"untrusted certificate", discover(mismatch.URL, false), "tls"},
		{"plain http", discover("http://idp.example.com", false), "insecure_issuer"},
		{"dns", fmt.Errorf("fetching discovery document: %w", &net.DNSError{Err: "no such host", Name: "auth.example.com", IsNotFound: true}), "dns"},
		{"refused", fmt.Errorf("fetching discovery document: %w", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}), "connection_refused"},
		{"timeout", fmt.Errorf("fetching discovery document: %w", context.DeadlineExceeded), "timeout"},
		{"other", errors.New("something else"), "other"},
	}
	for _, tc := range cases {
		if reason, hint := DiscoveryFailure(tc.err); reason != tc.want || hint == "" {
			t.Errorf("%s: DiscoveryFailure(%v) = %q, %q; want %q with a hint", tc.name, tc.err, reason, hint, tc.want)
		}
	}
}
