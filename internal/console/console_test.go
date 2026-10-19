package console

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// fakeSystem records keyfiles and applies; interfaces and files come from
// maps. It is the whole appliance as the wizard sees it.
type fakeSystem struct {
	ifaces   []Interface
	keyfiles map[string]NetConfig
	applied  []string
	files    map[string][]byte
	modes    map[string]os.FileMode
	execs    [][]string // argv of every Exec, in order
	stdins   []string   // what each Exec read on stdin
	execOut  map[string]string
	health   string
	units    map[string]string
}

func newFakeSystem(ifaces ...Interface) *fakeSystem {
	return &fakeSystem{ifaces: ifaces, keyfiles: map[string]NetConfig{}, files: map[string][]byte{}, modes: map[string]os.FileMode{}, execOut: map[string]string{}, health: "ok", units: map[string]string{"portico": "active", "keycloak": "active", "postgres": "active", "cloudflared": "inactive"}}
}

func (f *fakeSystem) Interfaces() ([]Interface, error) { return f.ifaces, nil }
func (f *fakeSystem) ReadKeyfile(name string) (NetConfig, bool, error) {
	c, ok := f.keyfiles[name]
	return c, ok, nil
}
func (f *fakeSystem) WriteKeyfile(name string, c NetConfig) error {
	f.keyfiles[name] = c
	return nil
}
func (f *fakeSystem) Apply(name string) error { f.applied = append(f.applied, name); return nil }
func (f *fakeSystem) Health(url string) (string, error) {
	if f.health == "" {
		return "", errors.New("connection refused")
	}
	return f.health, nil
}
func (f *fakeSystem) Unit(name string) (string, error) {
	if s, ok := f.units[name]; ok {
		return s, nil
	}
	return "inactive", nil
}
func (f *fakeSystem) Exec(stdin io.Reader, argv ...string) (string, error) {
	f.execs = append(f.execs, argv)
	in := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	f.stdins = append(f.stdins, in)
	return f.execOut[strings.Join(argv, " ")], nil
}
func (f *fakeSystem) ReadFile(path string) ([]byte, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}
func (f *fakeSystem) WriteFile(path string, data []byte, mode os.FileMode) error {
	f.files[path] = data
	f.modes[path] = mode
	return nil
}

// execd reports whether an Exec with exactly these arguments happened.
func (f *fakeSystem) execd(argv ...string) bool {
	want := strings.Join(argv, " ")
	for _, e := range f.execs {
		if strings.Join(e, " ") == want {
			return true
		}
	}
	return false
}

func run(t *testing.T, sys *fakeSystem, input string) string {
	t.Helper()
	var out bytes.Buffer
	if err := Run(context.Background(), strings.NewReader(input), &out, sys); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestNetworkScreenWritesTheKeyfileAndApplies(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", MAC: "52:54:00:aa:bb:cc", Up: true})
	out := run(t, sys, "1\nportico\n10.0.0.5\n24\n10.0.0.254\n1.1.1.1 9.9.9.9\ny\n0\n")
	c := sys.keyfiles["eth0"]
	if c.Hostname != "portico" || c.Address != "10.0.0.5" || c.Prefix != 24 || c.Gateway != "10.0.0.254" || len(c.DNS) != 2 {
		t.Fatalf("keyfile written: %+v", c)
	}
	if len(sys.applied) != 1 || sys.applied[0] != "eth0" {
		t.Fatalf("applied: %v", sys.applied)
	}
	if !sys.execd("hostnamectl", "set-hostname", "portico") {
		t.Fatalf("hostname not set: %v", sys.execs)
	}
	if !strings.Contains(out, "eth0 (52:54:00:aa:bb:cc, up)") || !strings.Contains(out, "https://10.0.0.5:8443/") {
		t.Fatalf("screen:\n%s", out)
	}
}

func TestNetworkScreenRefusesABadGatewayAndAsksAgain(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	// Gateway outside the subnet, then a good one.
	out := run(t, sys, "1\nportico\n10.0.0.5\n24\n10.0.1.254\n10.0.0.254\n\ny\n0\n")
	if !strings.Contains(out, "gateway") || sys.keyfiles["eth0"].Gateway != "10.0.0.254" {
		t.Fatalf("bad gateway accepted or not named:\n%s", out)
	}
}

func TestNetworkScreenOffersTheCurrentValuesAsDefaults(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.keyfiles["eth0"] = NetConfig{Hostname: "portico", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254", DNS: []string{"1.1.1.1"}}
	// Enter on every prompt keeps the value; "n" at the confirmation writes nothing.
	out := run(t, sys, "1\n\n\n\n\n\nn\n0\n")
	if !strings.Contains(out, "[10.0.0.5]") || len(sys.applied) != 0 {
		t.Fatalf("defaults or apply:\n%s", out)
	}
}

func TestTwoInterfacesAreConfiguredOneAfterTheOther(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true}, Interface{Name: "eth1", Up: false})
	run(t, sys, "1\nportico\n10.0.0.5\n24\n10.0.0.254\n\ny\n192.168.9.5\n24\n\n\ny\n0\n")
	if sys.keyfiles["eth1"].Address != "192.168.9.5" || sys.keyfiles["eth1"].Gateway != "" || len(sys.applied) != 2 {
		t.Fatalf("second interface: %+v applied %v", sys.keyfiles["eth1"], sys.applied)
	}
}

func TestMenuShowsTheStatusLineAndLeavesOnZeroOrEOF(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.health = `{"status":"setup"}`
	sys.units["cloudflared"] = "inactive"
	out := run(t, sys, "0\n")
	for _, want := range []string{"portico: active", "keycloak: active", "cloudflared: inactive", "1) Network", "0) Log out", "admin UI: set the network first"} {
		if !strings.Contains(out, want) {
			t.Fatalf("menu lacks %q:\n%s", want, out)
		}
	}
	if out := run(t, sys, ""); !strings.Contains(out, "1) Network") {
		t.Fatalf("EOF: %s", out)
	}
	if out := run(t, sys, "9\n0\n"); !strings.Contains(out, "unknown choice") {
		t.Fatalf("unknown choice: %s", out)
	}
}
