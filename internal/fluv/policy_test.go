package fluv

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

const testEndpoint = "https://fluv.example.com/api/v1/integrations/trakka/purchases"

func TestPolicyAllows(t *testing.T) {
	public := Policy{}
	lan := Policy{Allowed: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("127.0.0.1/32")}}
	cases := []struct {
		ip           string
		public, lanp bool
	}{
		{"93.184.216.34", true, false},
		{"2606:4700::1111", true, false},
		{"192.168.1.20", false, true},
		{"192.168.2.20", false, false},
		{"127.0.0.1", false, true},
		{"10.0.0.1", false, false},
		{"100.64.0.1", false, false},
		{"::ffff:192.168.1.20", false, true}, // IPv4-mapped: judged as the IPv4 address
		{"169.254.169.254", false, false},    // cloud metadata: never, not even if listed
		{"fe80::1", false, false},
		{"224.0.0.1", false, false},
		{"0.0.0.0", false, false},
		{"::", false, false},
		{"203.0.113.5", false, false},
	}
	for _, c := range cases {
		ip := netip.MustParseAddr(c.ip)
		if got := public.allows(ip); got != c.public {
			t.Errorf("public policy allows(%s) = %v, want %v", c.ip, got, c.public)
		}
		if got := lan.allows(ip); got != c.lanp {
			t.Errorf("LAN policy allows(%s) = %v, want %v", c.ip, got, c.lanp)
		}
	}
	metadata := Policy{Allowed: []netip.Prefix{netip.MustParsePrefix("169.254.0.0/16")}}
	if metadata.allows(netip.MustParseAddr("169.254.169.254")) {
		t.Error("an operator allowlist opened the link-local metadata address")
	}
}

func TestValidateTarget(t *testing.T) {
	lan := Policy{Allowed: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}}
	cases := []struct {
		name     string
		policy   Policy
		url      string
		ips      []string
		wantCode string
		wantURL  string
	}{
		{name: "public https", url: testEndpoint, ips: []string{"93.184.216.34"}, wantURL: testEndpoint},
		{name: "trailing slash and scheme case", url: "HTTPS://fluv.example.com/api/v1/integrations/trakka/purchases/", ips: []string{"93.184.216.34"}, wantURL: testEndpoint},
		{name: "sub-path prefix", url: "https://example.com/fluv/api/v1/integrations/trakka/purchases", ips: []string{"93.184.216.34"},
			wantURL: "https://example.com/fluv/api/v1/integrations/trakka/purchases"},
		{name: "duplicates folded", url: testEndpoint, ips: []string{"93.184.216.34", " 93.184.216.34 "}, wantURL: testEndpoint},
		{name: "empty url", url: "", ips: []string{"93.184.216.34"}, wantCode: "required"},
		{name: "javascript scheme", url: "javascript:alert(1)", ips: []string{"93.184.216.34"}, wantCode: "invalid_url"},
		{name: "ftp scheme", url: "ftp://fluv.example.com/api/v1/integrations/trakka/purchases", ips: []string{"93.184.216.34"}, wantCode: "invalid_url"},
		{name: "other path", url: "https://fluv.example.com/api/v1/admin/users", ips: []string{"93.184.216.34"}, wantCode: "not_fluv_endpoint"},
		{name: "credentials", url: "https://u:p@fluv.example.com/api/v1/integrations/trakka/purchases", ips: []string{"93.184.216.34"}, wantCode: "invalid_url"},
		{name: "query", url: testEndpoint + "?x=1", ips: []string{"93.184.216.34"}, wantCode: "invalid_url"},
		{name: "fragment", url: testEndpoint + "#x", ips: []string{"93.184.216.34"}, wantCode: "invalid_url"},
		{name: "no pin", url: testEndpoint, ips: nil, wantCode: "required"},
		{name: "too many pins", url: testEndpoint, ips: []string{"1.1.1.1", "1.0.0.1", "8.8.8.8", "8.8.4.4", "9.9.9.9"}, wantCode: "too_many"},
		{name: "not an ip", url: testEndpoint, ips: []string{"fluv.example.com"}, wantCode: "invalid_ip"},
		{name: "zone", url: testEndpoint, ips: []string{"fe80::1%eth0"}, wantCode: "invalid_ip"},
		{name: "private without allowlist", url: testEndpoint, ips: []string{"192.168.1.20"}, wantCode: "address_not_allowed"},
		{name: "loopback without allowlist", url: testEndpoint, ips: []string{"127.0.0.1"}, wantCode: "address_not_allowed"},
		{name: "metadata", url: testEndpoint, ips: []string{"169.254.169.254"}, wantCode: "address_not_allowed"},
		{name: "public with allowlist", policy: lan, url: testEndpoint, ips: []string{"93.184.216.34"}, wantCode: "address_not_allowed"},
		{name: "lan http with allowlist", policy: lan, url: "http://192.168.1.20:8080/api/v1/integrations/trakka/purchases", ips: []string{"192.168.1.20"},
			wantURL: "http://192.168.1.20:8080/api/v1/integrations/trakka/purchases"},
		{name: "ip literal not pinned", policy: lan, url: "https://192.168.1.21/api/v1/integrations/trakka/purchases", ips: []string{"192.168.1.20"}, wantCode: "host_not_pinned"},
		{name: "public http", url: "http://fluv.example.com/api/v1/integrations/trakka/purchases", ips: []string{"93.184.216.34"}, wantCode: "https_required"},
		{name: "bad port", url: "https://fluv.example.com:99999/api/v1/integrations/trakka/purchases", ips: []string{"93.184.216.34"}, wantCode: "invalid_url"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.policy.validateTarget(c.url, c.ips)
			if c.wantCode != "" {
				var verr *ValidationError
				if !errors.As(err, &verr) || verr.Code != c.wantCode {
					t.Fatalf("got %v, want code %s", err, c.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.endpoint.String() != c.wantURL {
				t.Errorf("endpoint = %s, want %s", got.endpoint, c.wantURL)
			}
			if len(got.pins) != 1 {
				t.Errorf("pins = %v, want one", got.pins)
			}
		})
	}
}

func TestValidToken(t *testing.T) {
	cases := map[string]bool{
		"fluv_trk_abcdefghijklmnopqrstuvwxyz":   true,
		"fluv_trk_234567abcdefghijklmnopqrst":   true,
		"fluv_trk_ABCDEFGHIJKLMNOPQRSTUVWXYZ":   false, // Fluv's tokens are lowercase
		"fluv_trk_abcdefghijklmnopqrstuvwxy":    false,
		"fluv_trk_abcdefghijklmnopqrstuvwxyz1":  false,
		"fluv_trk_abcdefghijklmnopqrstuvwxy1":   false, // 1 isn't base32
		"trk_abcdefghijklmnopqrstuvwxyzabcdefg": false,
		"fluv_trk_abcdefghijklm\r\nopqrstuvwxy": false,
		"":                                      false,
	}
	for token, want := range cases {
		if got := validToken(token); got != want {
			t.Errorf("validToken(%q) = %v, want %v", token, got, want)
		}
	}
}

// TestPinnedDialerIgnoresHostName checks the IP lock itself: whatever host
// name the transport asks for, the connection goes to the pinned address.
func TestPinnedDialerIgnoresHostName(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	loopback := Policy{Allowed: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	d := pinnedDialer{pins: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, port: port, policy: loopback}

	conn, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("attacker.invalid", port))
	if err != nil {
		t.Fatalf("dial through the pin: %v", err)
	}
	if got := conn.RemoteAddr().String(); got != ln.Addr().String() {
		t.Errorf("connected to %s, want the pinned %s", got, ln.Addr())
	}
	_ = conn.Close()

	if _, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("attacker.invalid", "22")); err == nil {
		t.Error("dialed another port than the endpoint's")
	}
	// The operator narrowed the allowlist since the pin was saved.
	d.policy = Policy{}
	if _, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("fluv.example.com", port)); !errors.Is(err, errBlockedAddress) {
		t.Errorf("dial after the policy changed: %v, want errBlockedAddress", err)
	}
}
