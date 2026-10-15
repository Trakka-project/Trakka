package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"syscall"
)

// ErrIssuerMismatch is returned by NewOIDCClient when the discovery
// document names another issuer than the configured one. OIDC requires an
// exact match, trailing slash included.
var ErrIssuerMismatch = errors.New("discovery document issuer does not match the configured issuer")

// DiscoveryStatusError is returned by NewOIDCClient when the discovery
// document request answers with a status other than 200.
type DiscoveryStatusError struct {
	Status int
}

func (e *DiscoveryStatusError) Error() string {
	return fmt.Sprintf("discovery document request returned %d", e.Status)
}

// DiscoveryFailure explains why NewOIDCClient failed, for the logs: a short
// reason code and a hint at what to check. Discovery runs from wherever
// Trakka runs (typically a container), whose network view of the provider
// can differ from a browser's: a public name resolving differently, or not
// at all, inside the container is the usual culprit.
func DiscoveryFailure(err error) (reason, hint string) {
	var (
		dnsErr       *net.DNSError
		certErr      *tls.CertificateVerificationError
		unknownCAErr x509.UnknownAuthorityError
		hostnameErr  x509.HostnameError
		invalidErr   x509.CertificateInvalidError
		statusErr    *DiscoveryStatusError
		netErr       net.Error
		recordHdrErr tls.RecordHeaderError
	)
	switch {
	case errors.Is(err, ErrIssuerMismatch):
		return "issuer_mismatch", "the configured issuer must equal the discovery document's \"issuer\" exactly, trailing slash included (Authentik's ends with a slash: https://<host>/application/o/<slug>/)"
	case errors.Is(err, ErrInsecureIssuer):
		return "insecure_issuer", "use an https:// issuer, or set OIDC_ALLOW_INSECURE_ISSUER=true for a provider reachable only over a private network"
	case errors.As(err, &statusErr):
		return "http_status", "the issuer URL answers, but not with a discovery document: check the issuer URL (Authentik: https://<host>/application/o/<slug>/)"
	case errors.As(err, &dnsErr):
		return "dns", "Trakka cannot resolve the issuer's host name from where it runs: check DNS inside the Trakka container (getent hosts <host>), a LAN-only or split-horizon name, or the container's DNS settings"
	case errors.As(err, &certErr), errors.As(err, &unknownCAErr), errors.As(err, &hostnameErr), errors.As(err, &invalidErr):
		return "tls", "Trakka does not trust the issuer's HTTPS certificate: self-signed or private CA, expired, or issued for another host name"
	case errors.As(err, &recordHdrErr):
		return "tls", "the issuer's address does not speak HTTPS (plain HTTP on that port?)"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused", "nothing accepts connections at the issuer's address as seen from Trakka: provider stopped, wrong port, or the host name resolves to an address that does not reach it from the container (hairpin NAT)"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout", "the issuer did not answer in time: provider down, a firewall, or the server unable to reach its own public address (hairpin NAT)"
	default:
		return "other", "see the error"
	}
}
