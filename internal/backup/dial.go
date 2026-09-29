package backup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// errBlockedAddress is what the dial guard returns when every address the
// WebDAV host resolves to is off-limits — surfaced to the admin as its own
// error code, since the fix (BACKUP_WEBDAV_ALLOW_PRIVATE) is specific.
var errBlockedAddress = errors.New("the WebDAV server resolves to a private or reserved address")

// dialGuard is this package's SSRF defense for the WebDAV URL, the third
// place (after internal/scraper and internal/webpush) where this server
// makes outbound requests to a URL entered at runtime rather than fixed at
// deploy time. Like those two, it resolves the host itself, checks every
// address, and dials the checked IP literal directly, so DNS rebinding
// can't swap in a different address between the check and the connection.
// It is an independent copy, per the SSRF rule's "reuse an existing guard
// or add an equivalent one", because it needs one knob the other two
// don't: allowPrivate.
//
// Only an admin can set the WebDAV URL, but the admin role is granted at
// runtime (from the web UI) while network reachability is a property of
// the deployment — so by default the same public-addresses-only policy as
// the other two guards applies, and reaching a WebDAV server on the LAN, a
// Tailscale/CGNAT address or loopback (a common self-hosted setup: a
// Nextcloud next to Trakka) requires the operator to opt in with
// BACKUP_WEBDAV_ALLOW_PRIVATE=true. Even then, link-local addresses (which
// include the 169.254.169.254 cloud metadata endpoint), multicast and
// unspecified addresses stay blocked: no WebDAV server lives there.
type dialGuard struct {
	allowPrivate bool
	timeout      time.Duration
}

func (g dialGuard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("splitting address %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", host, err)
	}

	dialer := &net.Dialer{Timeout: g.timeout}
	var lastErr error
	for _, ip := range ips {
		if !allowedIP(ip, g.allowPrivate) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
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

// reservedRanges are never a legitimate public WebDAV server — the same
// list internal/scraper and internal/webpush keep, by hand, in lockstep.
var reservedRanges = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "100::/64", "2001:db8::/32",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("backup: bad reserved CIDR " + cidr) // a constant above is malformed; a build-time bug
		}
		nets = append(nets, n)
	}
	return nets
}()

// allowedIP applies the policy described on dialGuard.
func allowedIP(ip net.IP, allowPrivate bool) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if allowPrivate {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return false
	}
	for _, n := range reservedRanges {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}
