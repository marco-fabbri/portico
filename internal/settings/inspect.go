package settings

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/marco-fabbri/portico/internal/proxy"
)

// CertInfo is what a Prism Central's certificate says, shown on "Add a
// site" before any credential is asked (sites usability design 4.1 step 1).
type CertInfo struct {
	Trusted     bool      `json:"trusted"`
	VerifyError string    `json:"verify_error,omitempty"`
	Issuer      string    `json:"issuer"`
	Subject     string    `json:"subject"`
	Fingerprint string    `json:"fingerprint"`
	Names       []string  `json:"names"`
	NotAfter    time.Time `json:"not_after"`
}

// InspectDialTimeout bounds the TLS handshake of InspectCertificate.
const InspectDialTimeout = 10 * time.Second

// InspectCertificate reads the leaf certificate a Prism Central presents
// and verifies it against the system roots plus caPEM and the URL's host.
// It completes one TLS handshake and sends no HTTP request, so no
// credential can travel. The error is for "nothing answers" (or a bad
// URL); an untrusted certificate is a result.
func InspectCertificate(ctx context.Context, rawURL string, caPEM []byte) (CertInfo, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.Scheme != "https" {
		return CertInfo{}, fmt.Errorf("the Prism Central URL %q is not a valid https URL", rawURL)
	}
	if u.User != nil {
		return CertInfo{}, errors.New("the Prism Central URL must not carry credentials")
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "443")
	}
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: InspectDialTimeout},
		// Verification is done below, by hand, to report what was found.
		Config: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12, InsecureSkipVerify: true},
	}
	dctx, cancel := context.WithTimeout(ctx, InspectDialTimeout)
	defer cancel()
	conn, err := d.DialContext(dctx, "tcp", addr)
	if err != nil {
		return CertInfo{}, fmt.Errorf("nothing answers at %s: %w", u.Host, err)
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return CertInfo{}, fmt.Errorf("%s presented no certificate", u.Host)
	}
	leaf := certs[0]
	info := CertInfo{
		Issuer:      leaf.Issuer.String(),
		Subject:     leaf.Subject.String(),
		Fingerprint: proxy.Fingerprint(leaf.Raw),
		NotAfter:    leaf.NotAfter,
		Names:       append([]string{}, leaf.DNSNames...),
	}
	for _, ip := range leaf.IPAddresses {
		info.Names = append(info.Names, ip.String())
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if len(caPEM) > 0 && !roots.AppendCertsFromPEM(caPEM) {
		return CertInfo{}, errors.New("no certificate found in the CA PEM")
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: u.Hostname(), Roots: roots, Intermediates: inter}); err != nil {
		info.VerifyError = err.Error()
	} else {
		info.Trusted = true
	}
	return info, nil
}
