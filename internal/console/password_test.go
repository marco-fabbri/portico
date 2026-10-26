package console

import (
	"errors"
	"strings"
	"testing"
)

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
