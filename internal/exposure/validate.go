// Package exposure is how the public path is published (design 4.2,
// 4.6): the checks on a certificate and key or a tunnel token the admin
// UI receives, and the root-side Apply that installs them, run by the
// portico-exposure path unit because Portico itself is unprivileged.
package exposure

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Info describes a public certificate, for the admin UI.
type Info struct {
	Names       []string
	NotAfter    time.Time
	Fingerprint string // SHA-256 of the leaf, colon-separated hex
	Issuer      string
	SelfSigned  bool
}

// Inspect reads the leaf of a PEM chain without a key: the details the
// Settings page shows for the installed certificate.
func Inspect(certPEM []byte) (Info, error) {
	certs, err := parseChain(certPEM)
	if err != nil {
		return Info{}, err
	}
	return info(certs[0]), nil
}

// Validate checks a certificate chain and its key before anything is
// installed; the error names the failing check in words an operator can
// act on. The leaf comes first, each certificate is followed by its
// issuer, the leaf names every host, is for TLS servers, is valid at now
// and matches the key. PKCS#1, PKCS#8 and EC keys are accepted.
func Validate(certPEM, keyPEM []byte, hosts []string, now time.Time) (Info, error) {
	certs, err := parseChain(certPEM)
	if err != nil {
		return Info{}, err
	}
	leaf := certs[0]
	if leaf.IsCA {
		return Info{}, fmt.Errorf("the first certificate must be the server's, followed by the intermediates; this one is a CA certificate (%s)", leaf.Subject.CommonName)
	}
	for i := 1; i < len(certs); i++ {
		if !bytes.Equal(certs[i-1].RawIssuer, certs[i].RawSubject) {
			return Info{}, fmt.Errorf("the chain is in the wrong order: certificate %d (%s) is not the issuer of the one before it; put the server's first, then each issuer", i+1, certs[i].Subject.CommonName)
		}
	}
	for _, h := range hosts {
		if leaf.VerifyHostname(h) != nil {
			return Info{}, fmt.Errorf("the certificate does not name %s (it names %s)", h, strings.Join(leaf.DNSNames, ", "))
		}
	}
	if len(leaf.ExtKeyUsage) > 0 && !hasServerAuth(leaf.ExtKeyUsage) {
		return Info{}, fmt.Errorf("the certificate is not for TLS servers (its extended key usage has no serverAuth)")
	}
	if now.Before(leaf.NotBefore) {
		return Info{}, fmt.Errorf("the certificate is not valid until %s", leaf.NotBefore.UTC().Format(time.RFC3339))
	}
	if now.After(leaf.NotAfter) {
		return Info{}, fmt.Errorf("the certificate expired on %s", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return Info{}, err
	}
	pub, ok := leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(key.Public()) {
		return Info{}, fmt.Errorf("the key does not match the certificate")
	}
	return info(leaf), nil
}

func parseChain(certPEM []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := certPEM
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if strings.Contains(b.Type, "PRIVATE KEY") {
			return nil, fmt.Errorf("the certificate file holds a private key: put the key in the key field")
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("certificate %d cannot be read: %v", len(certs)+1, err)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("the certificate file is not PEM (it must start with -----BEGIN CERTIFICATE-----)")
	}
	return certs, nil
}

func parseKey(keyPEM []byte) (crypto.Signer, error) {
	rest := keyPEM
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return nil, fmt.Errorf("the key file is not PEM (it must start with -----BEGIN ... PRIVATE KEY-----)")
		}
		if !strings.HasSuffix(b.Type, "PRIVATE KEY") {
			continue // EC PARAMETERS from openssl ecparam, or a certificate
		}
		if b.Type == "ENCRYPTED PRIVATE KEY" || strings.Contains(b.Headers["Proc-Type"], "ENCRYPTED") {
			return nil, fmt.Errorf("the key is encrypted: export it without a passphrase")
		}
		var k any
		var err error
		switch b.Type {
		case "RSA PRIVATE KEY":
			k, err = x509.ParsePKCS1PrivateKey(b.Bytes)
		case "EC PRIVATE KEY":
			k, err = x509.ParseECPrivateKey(b.Bytes)
		default:
			k, err = x509.ParsePKCS8PrivateKey(b.Bytes)
		}
		if err != nil {
			return nil, fmt.Errorf("the key cannot be read (%s)", b.Type)
		}
		s, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("the key type is not supported")
		}
		return s, nil
	}
}

func hasServerAuth(ekus []x509.ExtKeyUsage) bool {
	for _, u := range ekus {
		if u == x509.ExtKeyUsageServerAuth || u == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func info(c *x509.Certificate) Info {
	sum := sha256.Sum256(c.Raw)
	hexes := make([]string, len(sum))
	for i, b := range sum {
		hexes[i] = fmt.Sprintf("%02X", b)
	}
	issuer := c.Issuer.CommonName
	if issuer == "" {
		issuer = c.Issuer.String()
	}
	return Info{
		Names:       append([]string(nil), c.DNSNames...),
		NotAfter:    c.NotAfter,
		Fingerprint: strings.Join(hexes, ":"),
		Issuer:      issuer,
		SelfSigned:  bytes.Equal(c.RawIssuer, c.RawSubject) && c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil,
	}
}

// ValidateToken checks that a pasted string is a Cloudflare connector
// token (base64 of a JSON object with the account, the tunnel and the
// secret), so a whole command line or a truncated paste is refused
// before cloudflared is touched. It never puts the token in the error.
func ValidateToken(token string) error {
	if token == "" {
		return fmt.Errorf("no token")
	}
	if strings.ContainsAny(token, " \t") {
		return fmt.Errorf("the token has spaces: paste only the token, not the whole command")
	}
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err = enc.DecodeString(token); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("this is not a connector token (it must be the long string from the tunnel's install command)")
	}
	var t struct{ A, T, S string }
	if json.Unmarshal(raw, &t) != nil || t.A == "" || t.T == "" || t.S == "" {
		return fmt.Errorf("this is not a connector token (it must be the long string from the tunnel's install command)")
	}
	return nil
}

// SelfSigned makes an ECDSA P-256 certificate for the names, one year.
func SelfSigned(names []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), nil
}
