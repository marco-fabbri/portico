package console

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

const (
	configPath   = "/etc/portico/portico.yaml"
	certPath     = "/etc/portico/tls/public.crt"
	keyPath      = "/etc/portico/tls/public.key"
	tokenPath    = "/etc/cloudflared/token"
	keycloakEnv  = "/etc/portico/keycloak.env"
	porticoBin   = "/usr/local/bin/portico"
	tunnelListen = "127.0.0.1:8081" // 8080 is Keycloak's published port
	directListen = ":443"
)

// adminPassword sets the break-glass password through the binary run as
// the portico user: the store and the master key must belong to it, and
// the wizard runs as root. The password travels on stdin, never on a
// command line, and is never echoed.
func (s *session) adminPassword() {
	fmt.Fprintln(s.out, "Break-glass password of the admin UI and API (user admin), at least 12 characters.")
	pw, ok := s.secret("New password")
	if !ok {
		return
	}
	if len(pw) < 12 {
		fmt.Fprintln(s.out, "  at least 12 characters")
		s.pause()
		return
	}
	again, ok := s.secret("Again")
	if !ok {
		return
	}
	if pw != again {
		fmt.Fprintln(s.out, "  the two entries do not match")
		s.pause()
		return
	}
	if _, err := s.sys.Exec(strings.NewReader(pw+"\n"), "runuser", "-u", "portico", "--", porticoBin, "admin", "set-password", "-config", configPath); err != nil {
		fmt.Fprintln(s.out, "  "+err.Error())
	} else {
		fmt.Fprintln(s.out, "password set")
	}
	s.pause()
}

// Echoer is implemented by a System whose input is a terminal: the
// wizard turns the echo off while a secret is typed. The fake of the
// tests does not implement it.
type Echoer interface {
	Echo(on bool)
}

func (s *session) echo(on bool) {
	if e, ok := s.sys.(Echoer); ok {
		e.Echo(on)
	}
}

// secret reads a line with the echo off when the input is a terminal.
func (s *session) secret(label string) (string, bool) {
	fmt.Fprintf(s.out, "%s: ", label)
	s.echo(false)
	v, ok := s.line()
	s.echo(true)
	fmt.Fprintln(s.out)
	return strings.TrimSpace(v), ok
}

// exposure switches the public path between direct TLS and the tunnel.
func (s *session) exposure() {
	fmt.Fprintln(s.out, "1) Direct: install a certificate and key for the public names")
	fmt.Fprintln(s.out, "2) Direct: generate a self-signed pair (trial only)")
	fmt.Fprintln(s.out, "3) Cloudflare tunnel: paste the connector token")
	fmt.Fprint(s.out, "> ")
	choice, ok := s.line()
	if !ok {
		return
	}
	switch strings.TrimSpace(choice) {
	case "1":
		s.directFromFiles()
	case "2":
		s.selfSigned()
	case "3":
		s.tunnel()
	default:
		fmt.Fprintln(s.out, "unknown choice")
	}
	s.pause()
}

func (s *session) directFromFiles() {
	fmt.Fprintln(s.out, "Give the file paths, or press Enter at a path to paste the PEM instead (end it with an empty line).")
	cp, ok := s.ask("Certificate file (PEM, full chain)", "")
	if !ok {
		return
	}
	var cert, key []byte
	var err error
	if cp == "" {
		if cert, ok = s.pastePEM("Paste the certificate chain", true); !ok {
			return
		}
	} else if cert, err = s.sys.ReadFile(cp); err != nil {
		fmt.Fprintf(s.out, "  certificate: %v\n", err)
		return
	}
	kp, ok := s.ask("Private key file (PEM)", "")
	if !ok {
		return
	}
	if kp == "" {
		if key, ok = s.pastePEM("Paste the private key (not echoed)", false); !ok {
			return
		}
	} else if key, err = s.sys.ReadFile(kp); err != nil {
		fmt.Fprintf(s.out, "  key: %v\n", err)
		return
	}
	if _, err := tls.X509KeyPair(cert, key); err != nil {
		fmt.Fprintln(s.out, "  the key does not match the certificate, or the files are not PEM: "+err.Error())
		return
	}
	s.installDirect(cert, key)
}

// pastePEM reads PEM lines until an empty line after an END line; the key
// is read with the echo off.
func (s *session) pastePEM(label string, echo bool) ([]byte, bool) {
	fmt.Fprintf(s.out, "%s, then an empty line:\n", label)
	if !echo {
		s.echo(false)
		defer func() { s.echo(true); fmt.Fprintln(s.out) }()
	}
	var b strings.Builder
	ended := false
	for {
		line, ok := s.line()
		if !ok {
			return nil, false
		}
		if strings.TrimSpace(line) == "" {
			if ended {
				return []byte(b.String()), true
			}
			continue
		}
		b.WriteString(strings.TrimRight(line, "\r") + "\n")
		if strings.HasPrefix(strings.TrimSpace(line), "-----END ") {
			ended = true
		}
	}
}

func (s *session) selfSigned() {
	names, ok := s.ask("Public names, space-separated (prism and identity hosts)", "")
	if !ok || strings.TrimSpace(names) == "" {
		fmt.Fprintln(s.out, "  at least one name is needed")
		return
	}
	cert, key, err := SelfSigned(strings.Fields(names))
	if err != nil {
		fmt.Fprintln(s.out, "  "+err.Error())
		return
	}
	fmt.Fprintln(s.out, "self-signed pair generated: browsers will warn until a real certificate is installed")
	s.installDirect(cert, key)
}

// installDirect writes the pair, puts the YAML in direct mode, stops the
// tunnel and restarts Portico.
func (s *session) installDirect(cert, key []byte) {
	if err := s.sys.WriteFile(certPath, cert, 0o644); err != nil {
		fmt.Fprintln(s.out, "  "+err.Error())
		return
	}
	if err := s.sys.WriteFile(keyPath, key, 0o640); err != nil {
		fmt.Fprintln(s.out, "  "+err.Error())
		return
	}
	if err := s.setMode(false); err != nil {
		fmt.Fprintln(s.out, "  "+err.Error())
		return
	}
	// cloudflared may be absent (install.sh tolerates it): stopping it is best effort.
	s.sys.Exec(nil, "systemctl", "disable", "--now", "cloudflared")
	if _, err := s.sys.Exec(nil, "systemctl", "restart", "portico"); err != nil {
		fmt.Fprintln(s.out, "  portico did not restart: "+err.Error())
		return
	}
	fmt.Fprintln(s.out, "direct exposure on port 443 with the installed certificate")
}

func (s *session) tunnel() {
	fmt.Fprint(s.out, "Connector token (from the Cloudflare dashboard, not echoed): ")
	s.echo(false)
	token, ok := s.line()
	s.echo(true)
	fmt.Fprintln(s.out)
	if !ok || strings.TrimSpace(token) == "" {
		fmt.Fprintln(s.out, "  no token")
		return
	}
	if err := s.sys.WriteFile(tokenPath, []byte(strings.TrimSpace(token)+"\n"), 0o600); err != nil {
		fmt.Fprintln(s.out, "  "+err.Error())
		return
	}
	// cloudflared first: when it cannot start (not installed, bad token)
	// Portico stays where it is and the screen says why.
	if _, err := s.sys.Exec(nil, "systemctl", "enable", "--now", "cloudflared"); err != nil {
		fmt.Fprintln(s.out, "  cloudflared did not start: "+err.Error())
		fmt.Fprintln(s.out, "  Portico keeps its current exposure; check `journalctl -u cloudflared`")
		return
	}
	if err := s.setMode(true); err != nil {
		fmt.Fprintln(s.out, "  "+err.Error())
		return
	}
	if _, err := s.sys.Exec(nil, "systemctl", "restart", "portico"); err != nil {
		fmt.Fprintln(s.out, "  portico did not restart: "+err.Error())
		return
	}
	fmt.Fprintln(s.out, "tunnel mode: Portico listens on the loopback for cloudflared")
}

var (
	listenRe = regexp.MustCompile(`(?m)^(\s*listen:\s*)"[^"]*"`)
	certRe   = regexp.MustCompile(`(?m)^(\s*cert_file:\s*)\S*`)
	keyRe    = regexp.MustCompile(`(?m)^(\s*key_file:\s*)\S*`)
)

// setMode rewrites the three public lines of the process YAML, comments
// kept: tunnel mode listens on the loopback with no TLS files, direct
// mode on :443 with the installed pair. Only the first occurrence of
// each key is touched (the public block comes first in the file).
func (s *session) setMode(tunnel bool) error {
	y, err := s.sys.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("console: %s: %w", configPath, err)
	}
	listen, cert, key := directListen, certPath, keyPath
	if tunnel {
		listen, cert, key = tunnelListen, `""`, `""`
	}
	text := string(y)
	for _, r := range []struct {
		re   *regexp.Regexp
		repl string
		key  string
	}{{listenRe, `${1}"` + listen + `"`, "public.listen"}, {certRe, "${1}" + cert, "public.tls.cert_file"}, {keyRe, "${1}" + key, "public.tls.key_file"}} {
		var ok bool
		if text, ok = replaceFirst(r.re, text, r.repl); !ok {
			return fmt.Errorf("console: %s: %s not found in the shape the wizard edits (a quoted value); edit the file by hand", configPath, r.key)
		}
	}
	return s.sys.WriteFile(configPath, []byte(text), 0o640)
}

func replaceFirst(re *regexp.Regexp, text, repl string) (string, bool) {
	loc := re.FindStringSubmatchIndex(text)
	if loc == nil {
		return text, false
	}
	var dst []byte
	dst = re.ExpandString(dst, repl, text, loc)
	return text[:loc[0]] + string(dst) + text[loc[1]:], true
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

// keycloakCredentials shows the Keycloak administrator's username and
// bootstrap password from the installer's env file, for the setup page's
// Keycloak bootstrap, until Enter; the screen is cleared afterwards.
func (s *session) keycloakCredentials() {
	env, err := s.sys.ReadFile(keycloakEnv)
	if err != nil {
		fmt.Fprintf(s.out, "  %s: %v\n", keycloakEnv, err)
		s.pause()
		return
	}
	user, pw := "", ""
	for _, line := range strings.Split(string(env), "\n") {
		if v, ok := strings.CutPrefix(line, "KC_BOOTSTRAP_ADMIN_USERNAME="); ok {
			user = v
		}
		if v, ok := strings.CutPrefix(line, "KC_BOOTSTRAP_ADMIN_PASSWORD="); ok {
			pw = v
		}
	}
	fmt.Fprintln(s.out, "Keycloak administrator, for the Keycloak card of the setup page (used once there, never stored by Portico):")
	fmt.Fprintf(s.out, "  username: %s\n  password: %s\n", user, pw)
	s.pause()
}
