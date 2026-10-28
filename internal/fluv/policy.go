package fluv

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"

	"trakka/internal/validate"
)

// purchasesPath ends every Fluv purchase endpoint: Fluv shows
// <its BASE_URL>/api/v1/integrations/trakka/purchases in its settings, and
// a purchase is taken back with a DELETE on <that>/<ref>. Requiring it keeps
// the setting to what it is for — a user can't point Trakka's outbound
// requests at an arbitrary path of some other service.
const purchasesPath = "/api/v1/integrations/trakka/purchases"

// Bounds of a connection's settings.
const (
	maxEndpointURLLength = 2048
	maxPinnedIPs         = 4
)

// tokenPrefix starts every token Fluv makes for its link with Trakka
// (Fluv's ledger.TrakkaTokenPrefix), followed by 26 characters of
// lowercase base32 (crypto/rand.Text).
const tokenPrefix = "fluv_trk_" // #nosec G101 -- the prefix of a token, not a credential

// ValidationError is a setting Save refuses, with a stable Code for the
// frontend to translate.
type ValidationError struct {
	Field   string
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func invalid(field, code, message string) error {
	return &ValidationError{Field: field, Code: code, Message: message}
}

// Policy is which addresses a user may pin — the operator's side of the IP
// lock (FLUV_INTEGRATION_ALLOWED_IPS, config.FluvAllowedNetworks).
//
// This is this package's SSRF defense, the fourth place (after
// internal/scraper, internal/webpush and internal/backup) where the server
// sends a request to an address entered at runtime. It is stricter than
// those three: the URL's host name is never resolved, the request only ever
// goes to the pinned addresses (see pinnedDialer), and each one must pass
// allows — at save time and again at every dial, so narrowing the
// operator's list takes effect on what is already saved.
type Policy struct {
	// Allowed are the networks a pinned address must fall in. Empty: any
	// public address, and no other.
	Allowed []netip.Prefix
}

// reservedRanges are never a public server — the same list
// internal/scraper, internal/webpush and internal/backup keep, by hand, in
// lockstep.
var reservedRanges = func() []netip.Prefix {
	cidrs := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "100::/64", "2001:db8::/32",
	}
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefixes = append(prefixes, netip.MustParsePrefix(cidr))
	}
	return prefixes
}()

// isPublic reports whether ip is an ordinary public unicast address.
func isPublic(ip netip.Addr) bool {
	if ip.IsLoopback() || ip.IsPrivate() || neverAllowed(ip) {
		return false
	}
	for _, p := range reservedRanges {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// neverAllowed are the addresses no operator setting opens: link-local
// (including the 169.254.169.254 cloud metadata endpoint), multicast and
// unspecified. No Fluv lives there.
func neverAllowed(ip netip.Addr) bool {
	return !ip.IsValid() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast()
}

// allows reports whether ip may be pinned and dialed.
func (p Policy) allows(ip netip.Addr) bool {
	ip = ip.Unmap()
	if neverAllowed(ip) {
		return false
	}
	if len(p.Allowed) == 0 {
		return isPublic(ip)
	}
	for _, prefix := range p.Allowed {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// target is a validated connection: where the purchases go, and the only
// addresses they may go to.
type target struct {
	endpoint *url.URL
	pins     []netip.Addr
}

// validateTarget checks a user's endpoint URL and pinned addresses against
// p, returning the URL in canonical form.
func (p Policy) validateTarget(rawURL string, rawIPs []string) (*target, error) {
	if len(rawURL) > maxEndpointURLLength {
		return nil, invalid("endpoint_url", "invalid_url", "the URL is too long")
	}
	clean, err := validate.URL(rawURL)
	if err != nil {
		return nil, invalid("endpoint_url", "invalid_url", "enter an absolute http:// or https:// URL")
	}
	if clean == "" {
		return nil, invalid("endpoint_url", "required", "enter Fluv's purchase endpoint URL")
	}
	u, err := url.Parse(clean)
	if err != nil {
		return nil, invalid("endpoint_url", "invalid_url", "enter an absolute http:// or https:// URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, invalid("endpoint_url", "invalid_url", "the URL can't carry credentials, a query or a fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	if !strings.HasSuffix(u.Path, purchasesPath) {
		return nil, invalid("endpoint_url", "not_fluv_endpoint", "the URL must end with "+purchasesPath+", as Fluv's settings show it")
	}
	if u.Port() != "" && !validPort(u.Port()) {
		return nil, invalid("endpoint_url", "invalid_url", "invalid port")
	}

	if len(rawIPs) == 0 {
		return nil, invalid("pinned_ips", "required", "pin at least one IP address of Fluv")
	}
	if len(rawIPs) > maxPinnedIPs {
		return nil, invalid("pinned_ips", "too_many", "pin at most 4 IP addresses")
	}
	pins := make([]netip.Addr, 0, len(rawIPs))
	for _, raw := range rawIPs {
		ip, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || ip.Zone() != "" {
			return nil, invalid("pinned_ips", "invalid_ip", "not an IP address: "+strings.TrimSpace(raw))
		}
		ip = ip.Unmap()
		if !p.allows(ip) {
			return nil, invalid("pinned_ips", "address_not_allowed", addressRefusal(p, ip))
		}
		if !containsAddr(pins, ip) {
			pins = append(pins, ip)
		}
	}

	// An IP literal in the URL is the address: it must be one of the pins,
	// or the lock would be pinning something else than what was named.
	if host, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); err == nil {
		if !containsAddr(pins, host.Unmap()) {
			return nil, invalid("pinned_ips", "host_not_pinned", "the URL names "+host.String()+": pin that address")
		}
	}

	// The token travels in every request: cleartext http is only accepted
	// when the traffic stays on the operator's own networks — every pin
	// non-public (loopback, private, CGNAT/Tailscale), which only an
	// operator allowlist permits.
	if u.Scheme == "http" {
		for _, ip := range pins {
			if isPublic(ip) {
				return nil, invalid("endpoint_url", "https_required", "use https:// — plain http:// is only accepted for a Fluv on a private network")
			}
		}
	}
	return &target{endpoint: u, pins: pins}, nil
}

func addressRefusal(p Policy, ip netip.Addr) string {
	switch {
	case neverAllowed(ip):
		return ip.String() + " is a link-local, multicast or unspecified address"
	case len(p.Allowed) == 0:
		return ip.String() + " is not a public address; the administrator can allow it with FLUV_INTEGRATION_ALLOWED_IPS"
	default:
		return ip.String() + " is outside the networks the administrator allowed (FLUV_INTEGRATION_ALLOWED_IPS)"
	}
}

func validPort(port string) bool {
	if len(port) > 5 {
		return false
	}
	n := 0
	for _, c := range port {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n >= 1 && n <= 65535
}

func containsAddr(addrs []netip.Addr, ip netip.Addr) bool {
	for _, a := range addrs {
		if a == ip {
			return true
		}
	}
	return false
}

// errInvalidToken is a token that isn't one Fluv makes.
var errInvalidToken = invalid("token", "invalid_token", "paste the token Fluv showed when you linked Trakka (fluv_trk_…)")

// validToken reports whether token has the shape of a Fluv link token:
// the prefix and 26 lowercase base32 characters. Checking the exact shape
// also keeps anything but those characters out of the Authorization
// header.
func validToken(token string) bool {
	rest, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok || len(rest) != 26 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// joinPins and splitPins convert pinned addresses to and from the
// fluv_integrations.pinned_ips column.
func joinPins(pins []netip.Addr) string {
	parts := make([]string, len(pins))
	for i, ip := range pins {
		parts[i] = ip.String()
	}
	return strings.Join(parts, ",")
}

func splitPins(s string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no pinned address stored")
	}
	return out, nil
}
