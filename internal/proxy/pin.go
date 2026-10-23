package proxy

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
)

// SiteTrust is how Portico trusts a site's Prism Central (sites usability
// design 4.3): pinned by the SHA-256 of the leaf certificate's DER, which
// the MSP confirmed; otherwise verified against the system roots or a CA
// PEM. Verify false with no pin is the stored "verify off" of sites made
// before pinning, kept working for that migration only.
type SiteTrust struct {
	CAPEM  []byte
	Pin    string // Fingerprint form; empty when not pinned
	Verify bool
}

// PinMismatchError is a pinned site presenting another certificate: Prism
// Central renewed it, or something else answers on its address.
type PinMismatchError struct {
	Want, Got string
}

func (e *PinMismatchError) Error() string {
	return "certificate changed: presented " + e.Got
}

// Fingerprint is the SHA-256 of a DER certificate in the colon-separated
// upper-case form that openssl and browsers show (write-up 24 item 5).
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	var b strings.Builder
	for i, c := range sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02X", c)
	}
	return b.String()
}

// PrismTLSFor returns the client TLS settings for a site's Prism Central.
// A pin wins over everything else: the leaf must have that fingerprint,
// whatever its names or issuer (the factory certificate has no IP SAN).
func PrismTLSFor(targetHost string, t SiteTrust) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: targetHost, MinVersion: tls.VersionTLS12}
	if t.Pin != "" {
		want := strings.ToUpper(strings.TrimSpace(t.Pin))
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("no certificate presented")
			}
			if got := Fingerprint(rawCerts[0]); got != want {
				return &PinMismatchError{Want: want, Got: got}
			}
			return nil
		}
		return cfg, nil
	}
	if !t.Verify {
		cfg.InsecureSkipVerify = true
		return cfg, nil
	}
	if len(t.CAPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(t.CAPEM) {
			return nil, errors.New("no certificate found in the CA PEM")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}
