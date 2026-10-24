package console

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// A self-signed pair for the tests, never checked in.
func testPair(t *testing.T, name string) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: time.Now(), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
}

const sampleYAML = "public:\n  listen: \":443\"                 # tunnel mode: \"127.0.0.1:8081\" with no tls block\n  tls:                           # both empty = plain HTTP behind cloudflared\n    cert_file: /etc/portico/tls/public.crt\n    key_file: /etc/portico/tls/public.key\n  portal_host: prism.msp.example\n"

func TestAdminPasswordScreenExecsSetPasswordAsPortico(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	out := run(t, sys, "2\nCorrect-Horse-9\nCorrect-Horse-9\n0\n")
	if len(sys.execs) != 1 || strings.Join(sys.execs[0], " ") != "runuser -u portico -- /usr/local/bin/portico admin set-password -config /etc/portico/portico.yaml" {
		t.Fatalf("execs: %v", sys.execs)
	}
	if sys.stdins[0] != "Correct-Horse-9\n" {
		t.Fatalf("password not passed on stdin: %q", sys.stdins[0])
	}
	if strings.Contains(out, "Correct-Horse-9") {
		t.Fatalf("password echoed:\n%s", out)
	}
}

func TestAdminPasswordScreenRefusesAMismatchAndAShortOne(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	out := run(t, sys, "2\nCorrect-Horse-9\nOther\n2\nshort\n0\n")
	if len(sys.execs) != 0 || !strings.Contains(out, "do not match") || !strings.Contains(out, "at least 12") {
		t.Fatalf("execs %v:\n%s", sys.execs, out)
	}
}

func TestExposureDirectInstallsTheCertificateAndRestartsPortico(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	certPEM, keyPEM := testPair(t, "prism.msp.example")
	sys.files["/root/public.crt"] = []byte(certPEM)
	sys.files["/root/public.key"] = []byte(keyPEM)
	sys.files["/etc/portico/portico.yaml"] = []byte(strings.Replace(sampleYAML, `":443"`, `"127.0.0.1:8080"`, 1))
	run(t, sys, "3\n1\n/root/public.crt\n/root/public.key\n0\n")
	if string(sys.files["/etc/portico/tls/public.crt"]) != certPEM || string(sys.files["/etc/portico/tls/public.key"]) != keyPEM {
		t.Fatalf("files not installed")
	}
	if sys.modes["/etc/portico/tls/public.key"] != 0o640 || sys.modes["/etc/portico/tls/public.crt"] != 0o644 {
		t.Fatalf("modes %o %o", sys.modes["/etc/portico/tls/public.crt"], sys.modes["/etc/portico/tls/public.key"])
	}
	if !sys.execd("systemctl", "disable", "--now", "cloudflared") || !sys.execd("systemctl", "restart", "portico") {
		t.Fatalf("execs: %v", sys.execs)
	}
	if y := string(sys.files["/etc/portico/portico.yaml"]); !strings.Contains(y, "listen: \":443\"") || !strings.Contains(y, "cert_file: /etc/portico/tls/public.crt") {
		t.Fatalf("yaml not switched to direct mode:\n%s", y)
	}
}

func TestExposureDirectRefusesAKeyThatDoesNotMatch(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	certPEM, _ := testPair(t, "prism.msp.example")
	_, otherKey := testPair(t, "other.example")
	sys.files["/root/public.crt"] = []byte(certPEM)
	sys.files["/root/other.key"] = []byte(otherKey)
	out := run(t, sys, "3\n1\n/root/public.crt\n/root/other.key\n0\n")
	if _, ok := sys.files["/etc/portico/tls/public.crt"]; ok || !strings.Contains(out, "does not match") {
		t.Fatalf("mismatched pair accepted:\n%s", out)
	}
}

func TestExposureSelfSignedGeneratesAPairForTheNames(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.files["/etc/portico/portico.yaml"] = []byte(sampleYAML)
	out := run(t, sys, "3\n2\nprism.msp.example id.msp.example\n0\n")
	block, _ := pem.Decode(sys.files["/etc/portico/tls/public.crt"])
	if block == nil {
		t.Fatalf("no certificate written:\n%s", out)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(cert.DNSNames) != 2 || cert.DNSNames[0] != "prism.msp.example" || cert.DNSNames[1] != "id.msp.example" {
		t.Fatalf("cert: %v %v", err, cert)
	}
	if kb, _ := pem.Decode(sys.files["/etc/portico/tls/public.key"]); kb == nil || !strings.Contains(out, "browsers will warn") || !sys.execd("systemctl", "restart", "portico") {
		t.Fatalf("key, warning or restart missing:\n%s", out)
	}
}

func TestExposureTunnelWritesTheTokenAndEnablesCloudflared(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.files["/etc/portico/portico.yaml"] = []byte(sampleYAML)
	out := run(t, sys, "3\n3\neyJhIjoiYiJ9.token-value\n0\n")
	if string(sys.files["/etc/cloudflared/token"]) != "eyJhIjoiYiJ9.token-value\n" || sys.modes["/etc/cloudflared/token"] != 0o600 {
		t.Fatalf("token file: %q mode %o", sys.files["/etc/cloudflared/token"], sys.modes["/etc/cloudflared/token"])
	}
	if !sys.execd("systemctl", "enable", "--now", "cloudflared") || !sys.execd("systemctl", "restart", "portico") || strings.Contains(out, "token-value") {
		t.Fatalf("execs %v or token echoed", sys.execs)
	}
	// Tunnel mode: Portico's public listener moves to the loopback for cloudflared (design 4.6).
	y := string(sys.files["/etc/portico/portico.yaml"])
	if !strings.Contains(y, "listen: \"127.0.0.1:8081\"") || !strings.Contains(y, "cert_file: \"\"") || !strings.Contains(y, "key_file: \"\"") || !strings.Contains(y, "portal_host: prism.msp.example") {
		t.Fatalf("yaml not switched to tunnel mode:\n%s", y)
	}
}

func TestKeycloakCredentialsScreenShowsThemOnceFromTheEnvFile(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.files["/etc/portico/keycloak.env"] = []byte("KC_BOOTSTRAP_ADMIN_USERNAME=kcadmin\nKC_BOOTSTRAP_ADMIN_PASSWORD=generated-pw-1\nKC_DB_PASSWORD=db-pw\n")
	out := run(t, sys, "4\n\n0\n")
	if !strings.Contains(out, "kcadmin") || !strings.Contains(out, "generated-pw-1") || strings.Contains(out, "db-pw") {
		t.Fatalf("credentials screen:\n%s", out)
	}
	// Shown until Enter, then the screen is cleared.
	if !strings.Contains(out[strings.Index(out, "generated-pw-1"):], "\x1b[2J") {
		t.Fatalf("screen not cleared after the credentials")
	}
}

// Review of Milestone 3: a failed cloudflared start or Portico restart is
// reported, not hidden behind the success line.
func TestExposureReportsAFailedCommand(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.files["/etc/portico/portico.yaml"] = []byte(sampleYAML)
	sys.execErr["systemctl enable --now cloudflared"] = errors.New("Unit cloudflared.service not found")
	out := run(t, sys, "3\n3\neyJhIjoiYiJ9.token-value\n\n0\n")
	if !strings.Contains(out, "cloudflared.service not found") || strings.Contains(out, "tunnel mode: Portico listens") {
		t.Fatalf("failure hidden:\n%s", out)
	}
}

// A process YAML whose public block is not in the shape the wizard edits
// is left untouched, and the wizard says so (no silent no-op restart).
func TestExposureRefusesAYAMLItCannotEdit(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	odd := "public:\n  listen: :443\n"
	sys.files["/etc/portico/portico.yaml"] = []byte(odd)
	out := run(t, sys, "3\n3\neyJhIjoiYiJ9.token-value\n\n0\n")
	if string(sys.files["/etc/portico/portico.yaml"]) != odd || !strings.Contains(out, "public.listen") || sys.execd("systemctl", "restart", "portico") {
		t.Fatalf("odd YAML edited or restarted:\n%s\n%q", out, sys.files["/etc/portico/portico.yaml"])
	}
}

// Review of Milestone 3: an appliance without an SSH user has no way to
// put PEM files on disk, so the direct screen also takes the certificate
// and the key pasted on the console (an empty path, then the PEM, then an
// empty line).
func TestExposureDirectTakesAPastedPair(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.files["/etc/portico/portico.yaml"] = []byte(sampleYAML)
	certPEM, keyPEM := testPair(t, "prism.msp.example")
	in := "3\n1\n\n" + certPEM + "\n\n" + keyPEM + "\n\n0\n"
	out := run(t, sys, in)
	if strings.TrimSpace(string(sys.files["/etc/portico/tls/public.crt"])) != strings.TrimSpace(certPEM) || strings.TrimSpace(string(sys.files["/etc/portico/tls/public.key"])) != strings.TrimSpace(keyPEM) {
		t.Fatalf("pasted pair not installed:\n%s", out)
	}
	if strings.Contains(out, strings.Split(keyPEM, "\n")[1]) {
		t.Fatalf("the key was echoed:\n%s", out)
	}
}

// Review of Milestone 3: with two interfaces the second (no gateway) is the
// internal one, and the admin listener moves there, off the public side;
// with one interface it stays on every address and the guide says so.
func TestSecondInterfaceTakesTheAdminListener(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true}, Interface{Name: "eth1", Up: true})
	sys.files["/etc/portico/portico.yaml"] = []byte(sampleYAML + "internal:\n  admin_listen: \"0.0.0.0:8443\"\n  passthrough_listen: \"127.0.0.1:9440\"\n")
	out := run(t, sys, "1\nportico\n203.0.113.5\n24\n203.0.113.1\n\ny\n192.168.9.5\n24\n\n\ny\n0\n")
	y := string(sys.files["/etc/portico/portico.yaml"])
	if !strings.Contains(y, `admin_listen: "192.168.9.5:8443"`) || !strings.Contains(y, `passthrough_listen: "127.0.0.1:9440"`) || !sys.execd("systemctl", "restart", "portico") {
		t.Fatalf("admin listener not moved to the internal interface:\n%s\n%s", y, out)
	}
	if !strings.Contains(out, "https://192.168.9.5:8443/") {
		t.Fatalf("admin URL not the internal one:\n%s", out)
	}
	// One interface: nothing moves.
	sys = newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.files["/etc/portico/portico.yaml"] = []byte(sampleYAML + "internal:\n  admin_listen: \"0.0.0.0:8443\"\n")
	run(t, sys, "1\nportico\n10.0.0.5\n24\n10.0.0.254\n\ny\n0\n")
	if !strings.Contains(string(sys.files["/etc/portico/portico.yaml"]), `admin_listen: "0.0.0.0:8443"`) {
		t.Fatal("single interface: admin listener moved")
	}
}

// Design 4.9: "SSH key-only, internal interface only". With two
// interfaces SSH follows the admin listener to the internal one; a single
// interface keeps it where it is.
func TestTwoInterfacesMoveSSHToTheInternalOne(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true}, Interface{Name: "eth1", Up: true})
	sys.files["/etc/portico/portico.yaml"] = []byte(sampleYAML + "internal:\n  admin_listen: \"0.0.0.0:8443\"\n")
	out := run(t, sys, "1\nportico\n203.0.113.5\n24\n203.0.113.1\n\ny\n192.168.10.5\n24\n\n\ny\n0\n")
	f := string(sys.files[sshListenPath])
	if !strings.Contains(f, "ListenAddress 192.168.10.5\n") || sys.modes[sshListenPath] != 0o600 {
		t.Fatalf("ssh listen file = %q mode %v\n%s", f, sys.modes[sshListenPath], out)
	}
	if !sys.execd("sshd", "-t") || !sys.execd("systemctl", "reload", "sshd") {
		t.Fatalf("sshd not checked and reloaded: %v", sys.execs)
	}
	if !strings.Contains(out, "SSH moved to the internal interface: 192.168.10.5") {
		t.Fatalf("no message:\n%s", out)
	}
	sys = newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.files["/etc/portico/portico.yaml"] = []byte(sampleYAML + "internal:\n  admin_listen: \"0.0.0.0:8443\"\n")
	run(t, sys, "1\nportico\n10.0.0.5\n24\n10.0.0.254\n\ny\n0\n")
	if _, ok := sys.files[sshListenPath]; ok {
		t.Fatal("single interface: SSH listen file written")
	}
}

// A listen address sshd refuses is withdrawn before the reload, so the
// console never locks the administrator out.
func TestSSHNotMovedWhenTheConfigIsRefused(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true}, Interface{Name: "eth1", Up: true})
	sys.files["/etc/portico/portico.yaml"] = []byte(sampleYAML + "internal:\n  admin_listen: \"0.0.0.0:8443\"\n")
	sys.execErr["sshd -t"] = errors.New("exit status 255")
	out := run(t, sys, "1\nportico\n203.0.113.5\n24\n203.0.113.1\n\ny\n192.168.10.5\n24\n\n\ny\n0\n")
	if f := string(sys.files[sshListenPath]); strings.Contains(f, "ListenAddress") {
		t.Fatalf("refused listen address kept: %q", f)
	}
	if sys.execd("systemctl", "reload", "sshd") {
		t.Fatal("sshd reloaded with a refused configuration")
	}
	if !strings.Contains(out, "SSH not moved: ") {
		t.Fatalf("no message:\n%s", out)
	}
}

// Back to one interface, the drop-in loses its ListenAddress: sshd would
// otherwise fail to bind an address the host no longer has.
func TestBackToOneInterfaceClearsTheSSHListenAddress(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true}, Interface{Name: "eth1", Up: true})
	sys.files["/etc/portico/portico.yaml"] = []byte(sampleYAML + "internal:\n  admin_listen: \"0.0.0.0:8443\"\n")
	run(t, sys, "1\nportico\n203.0.113.5\n24\n203.0.113.1\n\ny\n192.168.10.5\n24\n\n\ny\n0\n")
	if !strings.Contains(string(sys.files[sshListenPath]), "ListenAddress 192.168.10.5") {
		t.Fatal("precondition: not moved")
	}
	sys.ifaces = []Interface{{Name: "eth0", Up: true}}
	sys.execs = nil
	out := run(t, sys, "1\nportico\n203.0.113.5\n24\n203.0.113.1\n\ny\n0\n")
	if f := string(sys.files[sshListenPath]); strings.Contains(f, "ListenAddress") || !strings.Contains(f, "#") {
		t.Fatalf("drop-in after going back to one interface: %q", f)
	}
	if !sys.execd("sshd", "-t") || !sys.execd("systemctl", "reload", "sshd") || !strings.Contains(out, "SSH listens on every address again") {
		t.Fatalf("sshd not reloaded or no message: %v\n%s", sys.execs, out)
	}
}
