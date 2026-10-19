# Milestone 3: Appliance Image Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An MSP downloads `portico-<version>.qcow2` from a GitHub
release, imports it into Prism Central, boots it, sets the network and
the break-glass password on the VM console, and reaches the setup page;
the same installer puts Portico on a Rocky Linux 9 host the MSP already
runs.

**Architecture:** One installer, `deploy/install.sh`, does everything
that the lab's `provision.sh` and `deploy.sh` do today, host-agnostic:
packages, the `portico` user and directories, the binary, the PostgreSQL
and Keycloak quadlets, the units (`portico.service`, the console on tty1,
cloudflared), firewalld and the SSH policy. The image build runs it
inside a Packer QEMU build from the Rocky 9 boot ISO with a kickstart
(the CIS partition layout needs a fresh disk), then hardens with OpenSCAP
and ships the report. A new Go package `internal/console` and the
subcommand `portico console` give the VM console a wizard: network,
break-glass password, exposure, status; it runs as root on tty1 through
a getty override. A thin OpenTofu module under `deploy/tofu/nutanix`
uploads the image from the release URL and creates the VM. A tag `v*`
builds and attaches everything to a GitHub release.

**Tech Stack:** Go 1.27 standard library (no new dependency: the wizard
is line-oriented prompts on the tty, not a TUI library), bash, Packer
1.11 with the QEMU plugin, Rocky Linux 9 boot ISO + kickstart (LVM),
OpenSCAP `scap-security-guide`, podman quadlets, cloudflared from the
Cloudflare rpm repository, OpenTofu 1.8 with the `nutanix/nutanix`
provider 2.5 (v4 APIs), GitHub Actions (ubuntu-24.04 runners with KVM).
Lab: NTNX-01, Prism Central pc.7.6.0.6.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md` sections
4.2 (listeners, break-glass account), 4.6 (TLS, tunnel mode), 4.8
(console wizard), 4.9 (image, installer, hardening, release, builder's
calls of 2026-10-19), 5 (threat table), 9 row 3. Lab write-ups 02 (VM
from an image through the v4 API, cloud-init network in `bootcmd`), 03
(Keycloak and PostgreSQL quadlets), 14 (settings in the store, master
key), 15 (setup mode and bootstraps).

## Global Constraints

- All text on disk in English. Commit messages in English, each with the
  `Co-Authored-By` trailer; pull requests merged with rebase.
- Branch `m3-appliance-image` from `main` (created, holds the design
  update and this plan); pull request merged by the executor, tag
  `m3-appliance-image`.
- The appliance target of design 4.9: 2 vCPU, 6 GiB RAM, 40 GiB disk;
  Rocky Linux 9 x86_64; SELinux enforcing; firewalld with 443, 8443 and
  9440 only (plus SSH on the internal interface); SSH key-only, root
  login refused; `/tmp`, `/var/tmp`, `/dev/shm` noexec; separate
  `/var`, `/var/log`, `/var/log/audit`, `/home`, `/var/lib/containers`
  (exec allowed, documented exception), `/var/lib/portico`.
- Secrets the installer generates (Keycloak administrator and database
  passwords) live in `/etc/portico/keycloak.env`, root, mode 0600, and
  are shown only by the console wizard on request; they never reach a
  log, a release asset, a Packer output or a CI log. The image contains
  no secret: the installer generates them at first boot, not at build
  time, and the build removes SSH host keys, machine-id, build user and
  package caches.
- The master key and the store are created by the binary at the first
  `serve`, as owned by `portico` (write-up 14); the image never carries
  one.
- The public path fails closed: nothing in the image starts a listener
  that reaches Prism Central without a Portico session; the appliance
  starts in setup mode (write-up 15).
- `CGO_ENABLED=0`; `gofmt -l cmd internal`, `go vet ./...`,
  `go test ./...` clean at every commit; `lab/test_scripts.sh` green; the
  CI gains `packer validate`, `ksvalidator`, `tofu validate` and
  `tofu fmt -check`.
- Nothing in `image/`, `deploy/` or the workflows names the lab (hosts,
  addresses, realm): the lab specifics stay under `lab/`.
- Lab: Prism Central `https://10.23.210.175:9440`, VLAN 210, free IPs
  from `.190` (the appliance VM takes `.191`, checked with ping before
  creation as `lab/vm/create-vm.sh` does), secrets in
  `~/portico-secrets/`. The lab VM `portico-lab` (`.190`) stays.

## Review Focus

1. A gateway outside the interface's subnet, a prefix above 32, an
   address equal to the gateway: the wizard refuses each with the field
   named and asks again, and writes no file (Task 1).
2. Running the wizard a second time on a configured appliance: the
   existing values are the defaults, the keyfile is rewritten in place,
   the connection stays up, nothing is duplicated (Task 1).
3. `install.sh` run twice on the same host: the second run changes no
   secret, keeps the store, replaces only the binary and the unit files,
   and the services come back (Task 2).
4. The appliance booted with a cloud-init network and no console: the
   services start, `portico` is in setup mode, the admin listener answers
   on the configured address, the console unit sits idle until someone
   opens the console (Task 2, measured in Task 5).
5. A VM created with a disk smaller than the kickstart layout: the
   installation stops with the layout named, not a half-installed image
   (Task 3).

---

### Task 0: Design and plan recorded

**Files:**
- Modify: `docs/superpowers/specs/2026-10-17-portico-design.md` (4.9 builder's calls, done)
- Create: `docs/superpowers/plans/2026-10-19-milestone-3-appliance-image.md` (this file)

- [ ] **Step 1: Commit on `m3-appliance-image`**

```bash
git add docs/superpowers/specs/2026-10-17-portico-design.md docs/superpowers/plans/2026-10-19-milestone-3-appliance-image.md
git commit -m "docs: Milestone 3 builder's calls in the design and the appliance image plan"
```

---

### Task 1: `portico console`: the wizard on tty1

**Files:**
- Create: `internal/console/console.go` (menu loop and screens),
  `internal/console/network.go` (interfaces, validation, keyfile),
  `internal/console/exposure.go` (certificate, tunnel), `internal/console/status.go`
- Create: `internal/console/console_test.go`, `network_test.go`, `exposure_test.go`, `status_test.go`
- Modify: `cmd/portico/main.go` (subcommand `console`, usage line)
- Create: `cmd/portico/console.go` (wiring: real sysfs, real commands)
- Create: `deploy/portico-console.service` (getty override on tty1)

**Interfaces:**
- Consumes: `config.Load` (process YAML, for the admin listener address
  and the TLS file paths), `setAdminPassword(cfg, stdin, stdout, stderr)`
  behaviour of `cmd/portico/main.go:114` (the wizard execs the binary as
  the `portico` user rather than calling it: the store must belong to
  `portico`, the wizard runs as root).
- Produces: `console.Run(ctx, in io.Reader, out io.Writer, sys System) error`;
  `type System interface { Interfaces() ([]Interface, error); ReadKeyfile(name string) (NetConfig, bool, error); WriteKeyfile(name string, c NetConfig) error; Apply(name string) error; Health(url string) (string, error); Unit(name string) (string, error); Exec(stdin io.Reader, argv ...string) (string, error); ReadFile(path string) ([]byte, error); WriteFile(path string, data []byte, mode os.FileMode) error }`;
  `type Interface struct { Name, MAC string; Up bool }`;
  `type NetConfig struct { Hostname, Address string; Prefix int; Gateway string; DNS []string }`;
  `func Validate(c NetConfig) error` (field-named errors);
  `func Keyfile(name string, c NetConfig) string` (NetworkManager keyfile text, `method=manual`, ipv6 disabled).
- The unit `deploy/portico-console.service` is installed by Task 2 as
  `/etc/systemd/system/getty@tty1.service.d/override.conf` content:
  `ExecStart=` reset, then `ExecStart=-/usr/local/bin/portico console -config /etc/portico/portico.yaml`,
  `StandardInput=tty`, `TTYPath=/dev/tty1`, `Restart=always`.

The wizard is a numbered menu on the console, re-runnable, nothing else
configurable from it (design 4.8):

```
Portico <version>                       https://10.0.0.5:8443/ (admin UI)
portico: running (setup mode)   keycloak: running   postgres: running   cloudflared: disabled
1) Network            3) Exposure (direct certificate or Cloudflare tunnel)
2) Admin password     4) Keycloak administrator credentials (show once)
5) Refresh            0) Log out
```

- [ ] **Step 1: Write the failing tests for validation and the keyfile**

```go
func TestValidateNamesTheField(t *testing.T) {
	cases := map[string]NetConfig{
		"address":  {Hostname: "portico", Address: "10.0.0.300", Prefix: 24, Gateway: "10.0.0.254"},
		"prefix":   {Hostname: "portico", Address: "10.0.0.5", Prefix: 33, Gateway: "10.0.0.254"},
		"gateway":  {Hostname: "portico", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.1.254"},
		"hostname": {Hostname: "bad host", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254"},
		"dns":      {Hostname: "portico", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254", DNS: []string{"one.one"}},
	}
	for field, c := range cases {
		if err := Validate(c); err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: %v", field, err)
		}
	}
	// The address equal to the gateway is refused too (Review Focus 1).
	if err := Validate(NetConfig{Hostname: "portico", Address: "10.0.0.254", Prefix: 24, Gateway: "10.0.0.254"}); err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Errorf("address = gateway: %v", err)
	}
	if err := Validate(NetConfig{Hostname: "portico", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254", DNS: []string{"1.1.1.1", "9.9.9.9"}}); err != nil {
		t.Errorf("valid config refused: %v", err)
	}
}

func TestKeyfileIsManualIPv4WithIPv6Off(t *testing.T) {
	got := Keyfile("eth0", NetConfig{Hostname: "portico", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254", DNS: []string{"1.1.1.1", "9.9.9.9"}})
	for _, want := range []string{"[connection]\nid=eth0\ntype=ethernet\ninterface-name=eth0\nautoconnect=true\n", "[ipv4]\nmethod=manual\naddress1=10.0.0.5/24,10.0.0.254\ndns=1.1.1.1;9.9.9.9;\n", "[ipv6]\nmethod=disabled\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("keyfile lacks %q:\n%s", want, got)
		}
	}
	// A second interface without a gateway is a plain subnet (the internal side).
	if got := Keyfile("eth1", NetConfig{Address: "192.168.9.5", Prefix: 24}); !strings.Contains(got, "address1=192.168.9.5/24\n") || strings.Contains(got, "dns=") {
		t.Fatalf("second interface:\n%s", got)
	}
}
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/console/ -run 'TestValidate|TestKeyfile' -v`
Expected: FAIL, "undefined: Validate", "undefined: Keyfile".

- [ ] **Step 3: Implement `Validate` and `Keyfile` in `internal/console/network.go`**

`Validate`: hostname per RFC 1123 label rules (letters, digits, dashes,
1 to 63), address and gateway parse as IPv4, prefix 1 to 32, gateway
inside the address's subnet and different from the address, every DNS
entry an IPv4; gateway and DNS optional (empty allowed) for a second
interface. Errors read `console: <field>: <reason>`. `Keyfile` renders
the text of Step 1 (the lab's `cloud-init.yaml` keyfile is the model).

- [ ] **Step 4: Run them**

Run: `go test ./internal/console/ -run 'TestValidate|TestKeyfile' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing test for the network screen through a fake System**

```go
// fakeSystem records keyfiles and applies; interfaces and files come from maps.
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

// execd reports whether an Exec with exactly these arguments happened.
func (f *fakeSystem) execd(argv ...string) bool { /* compare each f.execs entry */ }

func TestNetworkScreenWritesTheKeyfileAndApplies(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", MAC: "52:54:00:aa:bb:cc", Up: true})
	in := strings.NewReader("1\nportico\n10.0.0.5\n24\n10.0.0.254\n1.1.1.1 9.9.9.9\ny\n0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), in, &out, sys); err != nil {
		t.Fatal(err)
	}
	c := sys.keyfiles["eth0"]
	if c.Hostname != "portico" || c.Address != "10.0.0.5" || c.Prefix != 24 || c.Gateway != "10.0.0.254" || len(c.DNS) != 2 {
		t.Fatalf("keyfile written: %+v", c)
	}
	if len(sys.applied) != 1 || sys.applied[0] != "eth0" {
		t.Fatalf("applied: %v", sys.applied)
	}
	if !strings.Contains(out.String(), "eth0 (52:54:00:aa:bb:cc, up)") || !strings.Contains(out.String(), "https://10.0.0.5:8443/") {
		t.Fatalf("screen:\n%s", out.String())
	}
}

func TestNetworkScreenRefusesABadGatewayAndAsksAgain(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	// Gateway outside the subnet, then a good one.
	in := strings.NewReader("1\nportico\n10.0.0.5\n24\n10.0.1.254\n10.0.0.254\n\ny\n0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), in, &out, sys); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "gateway") || sys.keyfiles["eth0"].Gateway != "10.0.0.254" {
		t.Fatalf("bad gateway accepted or not named:\n%s", out.String())
	}
}

func TestNetworkScreenOffersTheCurrentValuesAsDefaults(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.keyfiles["eth0"] = NetConfig{Hostname: "portico", Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.254", DNS: []string{"1.1.1.1"}}
	// Enter on every prompt keeps the value; "n" at the confirmation writes nothing.
	in := strings.NewReader("1\n\n\n\n\n\nn\n0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), in, &out, sys); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[10.0.0.5]") || len(sys.applied) != 0 {
		t.Fatalf("defaults or apply:\n%s", out.String())
	}
}

func TestTwoInterfacesAreConfiguredOneAfterTheOther(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true}, Interface{Name: "eth1", Up: false})
	in := strings.NewReader("1\nportico\n10.0.0.5\n24\n10.0.0.254\n\ny\n192.168.9.5\n24\n\n\ny\n0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), in, &out, sys); err != nil {
		t.Fatal(err)
	}
	if sys.keyfiles["eth1"].Address != "192.168.9.5" || sys.keyfiles["eth1"].Gateway != "" || len(sys.applied) != 2 {
		t.Fatalf("second interface: %+v applied %v", sys.keyfiles["eth1"], sys.applied)
	}
}
```

- [ ] **Step 6: Run them**

Run: `go test ./internal/console/ -run 'TestNetworkScreen|TestTwoInterfaces' -v`
Expected: FAIL, "undefined: Run" (and the fake's type errors).

- [ ] **Step 7: Implement `Run`, the menu and the network screen in `internal/console/console.go` and `network.go`**

`Run` prints the header (version from a `Version` variable set by
`cmd/portico`, the admin URL built from the first interface's address
and the admin listener port of the config, or "admin UI: set the network
first" when no keyfile exists), the status line from `status.go`
(Task step 11), the menu, reads a line, dispatches; `0` returns nil;
EOF returns nil. The network screen: list the interfaces with MAC and
link state; for each, prompt hostname (first interface only), address,
prefix, gateway, DNS (space-separated), each with the current value in
brackets as default (Enter keeps it); `Validate`; on error print the
message and re-prompt that field; confirm `y/n`; `WriteKeyfile` then
`Apply`. The hostname is written with `Exec("hostnamectl", "set-hostname", h)`.
A prompt helper `ask(in *bufio.Reader, out io.Writer, label, def string) string`
is shared by the screens.

- [ ] **Step 8: Run them**

Run: `go test ./internal/console/ -v`
Expected: PASS (all so far).

- [ ] **Step 9: Write the failing tests for the password, exposure and credentials screens**

```go
func TestAdminPasswordScreenExecsSetPasswordAsPortico(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	in := strings.NewReader("2\nCorrect-Horse-9\nCorrect-Horse-9\n0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), in, &out, sys); err != nil {
		t.Fatal(err)
	}
	if len(sys.execs) != 1 || strings.Join(sys.execs[0], " ") != "runuser -u portico -- /usr/local/bin/portico admin set-password -config /etc/portico/portico.yaml" {
		t.Fatalf("execs: %v", sys.execs)
	}
	if sys.stdins[0] != "Correct-Horse-9\n" {
		t.Fatalf("password not passed on stdin")
	}
	if strings.Contains(out.String(), "Correct-Horse-9") {
		t.Fatalf("password echoed:\n%s", out.String())
	}
}

func TestAdminPasswordScreenRefusesAMismatchAndAShortOne(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	in := strings.NewReader("2\nCorrect-Horse-9\nOther\n2\nshort\n0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), in, &out, sys); err != nil {
		t.Fatal(err)
	}
	if len(sys.execs) != 0 || !strings.Contains(out.String(), "do not match") || !strings.Contains(out.String(), "at least 12") {
		t.Fatalf("execs %v:\n%s", sys.execs, out.String())
	}
}

func TestExposureDirectInstallsTheCertificateAndRestartsPortico(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.files["/root/public.crt"] = []byte(testCertPEM)
	sys.files["/root/public.key"] = []byte(testKeyPEM)
	in := strings.NewReader("3\n1\n/root/public.crt\n/root/public.key\n0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), in, &out, sys); err != nil {
		t.Fatal(err)
	}
	if string(sys.files["/etc/portico/tls/public.crt"]) != testCertPEM || string(sys.files["/etc/portico/tls/public.key"]) != testKeyPEM {
		t.Fatalf("files not installed")
	}
	if sys.modes["/etc/portico/tls/public.key"] != 0o640 {
		t.Fatalf("key mode %o", sys.modes["/etc/portico/tls/public.key"])
	}
	if !sys.execd("systemctl", "disable", "--now", "cloudflared") || !sys.execd("systemctl", "restart", "portico") {
		t.Fatalf("execs: %v", sys.execs)
	}
}

func TestExposureDirectRefusesAKeyThatDoesNotMatch(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.files["/root/public.crt"] = []byte(testCertPEM)
	sys.files["/root/other.key"] = []byte(otherKeyPEM)
	in := strings.NewReader("3\n1\n/root/public.crt\n/root/other.key\n0\n")
	var out bytes.Buffer
	_ = Run(context.Background(), in, &out, sys)
	if _, ok := sys.files["/etc/portico/tls/public.crt"]; ok || !strings.Contains(out.String(), "does not match") {
		t.Fatalf("mismatched pair accepted:\n%s", out.String())
	}
}

func TestExposureSelfSignedGeneratesAPairForTheNames(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	in := strings.NewReader("3\n2\nprism.msp.example id.msp.example\n0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), in, &out, sys); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(sys.files["/etc/portico/tls/public.crt"])
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(cert.DNSNames) != 2 || cert.DNSNames[0] != "prism.msp.example" {
		t.Fatalf("cert: %v %v", err, cert)
	}
}

func TestExposureTunnelWritesTheTokenAndEnablesCloudflared(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	in := strings.NewReader("3\n3\neyJhIjoiYiJ9.token-value\n0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), in, &out, sys); err != nil {
		t.Fatal(err)
	}
	if string(sys.files["/etc/cloudflared/token"]) != "eyJhIjoiYiJ9.token-value\n" || sys.modes["/etc/cloudflared/token"] != 0o600 {
		t.Fatalf("token file: %q mode %o", sys.files["/etc/cloudflared/token"], sys.modes["/etc/cloudflared/token"])
	}
	if !sys.execd("systemctl", "enable", "--now", "cloudflared") || strings.Contains(out.String(), "token-value") {
		t.Fatalf("execs %v or token echoed", sys.execs)
	}
	// Tunnel mode: Portico's public listener moves to the loopback for cloudflared (design 4.6).
	if !strings.Contains(string(sys.files["/etc/portico/portico.yaml"]), "listen: \"127.0.0.1:8080\"") {
		t.Fatalf("yaml not switched to tunnel mode:\n%s", sys.files["/etc/portico/portico.yaml"])
	}
}

func TestKeycloakCredentialsScreenShowsThemOnceFromTheEnvFile(t *testing.T) {
	sys := newFakeSystem(Interface{Name: "eth0", Up: true})
	sys.files["/etc/portico/keycloak.env"] = []byte("KC_BOOTSTRAP_ADMIN_USERNAME=kcadmin\nKC_BOOTSTRAP_ADMIN_PASSWORD=generated-pw-1\nKC_DB_PASSWORD=db-pw\n")
	in := strings.NewReader("4\n\n0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), in, &out, sys); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "kcadmin") || !strings.Contains(s, "generated-pw-1") || strings.Contains(s, "db-pw") {
		t.Fatalf("credentials screen:\n%s", s)
	}
	// Shown until Enter, then the screen is cleared (the terminal clear sequence follows).
	if !strings.Contains(s[strings.Index(s, "generated-pw-1"):], "\x1b[2J") {
		t.Fatalf("screen not cleared after the credentials")
	}
}
```

`testCertPEM`/`testKeyPEM`/`otherKeyPEM` are generated in the test file
with `crypto/ecdsa` and `x509.CreateCertificate` (self-signed, one
day), not checked in.

- [ ] **Step 10: Run them**

Run: `go test ./internal/console/ -run 'TestAdminPassword|TestExposure|TestKeycloakCredentials' -v`
Expected: FAIL, the screens do not exist (menu prints "unknown choice").

- [ ] **Step 11: Implement the screens in `console.go`, `exposure.go`, `status.go`**

Admin password: prompt twice with echo off (`Exec("stty", "-echo")`
around the read when `in` is a terminal; the fake is not, so the
screen must work without it), minimum 12 characters, equal; then
`Exec` with the `runuser` command of the test and the password on
stdin. Exposure: submenu 1 direct from files (read both, check the key
matches the certificate with `tls.X509KeyPair`, write to
`/etc/portico/tls/` with 0644 and 0640 root:portico, restore the YAML
`public.listen` to `":443"` and the tls block, disable cloudflared,
restart portico), 2 self-signed (names space-separated, ECDSA P-256,
365 days, same install path, warning that browsers will object), 3
tunnel (token to `/etc/cloudflared/token` 0600, YAML `public.listen` to
`"127.0.0.1:8080"` with the tls block emptied, `systemctl enable --now
cloudflared`, restart portico). The YAML edit is a text replacement on
the two lines, with the comment kept. Credentials: read the env file,
print the username and the bootstrap password, wait for Enter, clear.
Status (`status.go`): `Health(adminURL + "/healthz")`, `Unit` for
`portico`, `keycloak`, `postgres`, `cloudflared`, rendered as the header
of the screen.

- [ ] **Step 12: Run the package tests**

Run: `go test ./internal/console/ -v`
Expected: PASS.

- [ ] **Step 13: Wire `portico console` in `cmd/portico/console.go` and the unit file**

`case "console"` in `main.go`: `loadConfig("console", args[1:], stderr)`
then `console.Run(ctx, os.Stdin, os.Stdout, realSystem{cfg: cfg})`,
where `realSystem` reads `/sys/class/net/*` (skipping `lo` and
virtual devices under `/sys/devices/virtual`), keyfiles in
`/etc/NetworkManager/system-connections/<name>.nmconnection`, applies
with `nmcli connection reload` and `nmcli connection up <name>`, health
with an `http.Client` that skips verification on the loopback admin
listener, units with `systemctl is-active`. Usage line updated.
`deploy/portico-console.service` is the override of the Interfaces
block. The console loops: `Restart=always` on the override brings the
menu back after `0`.

- [ ] **Step 14: Build, vet, test, commit**

Run: `gofmt -l cmd internal; go vet ./... && go test ./... 2>&1 | tail -3`
Expected: no file listed; `ok` for every package.

```bash
git add internal/console cmd/portico deploy/portico-console.service
git commit -m "console: the tty1 wizard: network, admin password, exposure, Keycloak credentials, status"
```

---

### Task 2: `deploy/install.sh`: one installer for the image and for an existing host

**Files:**
- Create: `deploy/install.sh`
- Create: `deploy/quadlets/postgres.container`, `deploy/quadlets/keycloak.container`, `deploy/quadlets/portico.network`
- Create: `deploy/cloudflared.service`, `deploy/firewalld/portico.xml`, `deploy/sshd/00-portico.conf`, `deploy/sysctl/60-portico.conf`
- Create: `deploy/README.md`
- Modify: `deploy/portico.yaml.example` (the appliance defaults: admin listener on the interface address is set by the wizard; file unchanged otherwise)
- Modify: `lab/test_scripts.sh` (checks on the installer), `.github/workflows/ci.yml` (shellcheck covers `deploy/`)

**Interfaces:**
- Consumes: `deploy/portico.service` (M1), `deploy/portico-console.service` (Task 1), the quadlet layout of `lab/vm/*.container` (write-up 03) with the lab specifics removed.
- Produces: `install.sh [--binary PATH | --release VERSION] [--render-only DIR] [--no-start]`:
  - `--binary PATH` installs that file; `--release VERSION` downloads
    `portico-VERSION-linux-amd64.tar.gz` and its `.sha256` from the
    GitHub release and verifies the checksum before installing; neither
    on a second run keeps the installed binary.
  - `--render-only DIR` writes every file the installer would install,
    under DIR with the same absolute paths (`DIR/etc/portico/...`), runs
    no command and needs no root: this is what the tests assert on.
  - `--no-start` installs without starting the services (the image build).
  - Idempotent: a second run regenerates no secret, keeps
    `/var/lib/portico`, replaces the binary and the unit files, reloads
    and restarts what changed.
- Keycloak env: `KC_HOSTNAME_STRICT=false`, `KC_PROXY_HEADERS=xforwarded`,
  `KC_HTTP_ENABLED=true`, `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`, health
  on, the theme volume as in the lab, image `quay.io/keycloak/keycloak:26.3`;
  secrets `KC_BOOTSTRAP_ADMIN_PASSWORD`, `KC_DB_PASSWORD`, `POSTGRES_PASSWORD`
  generated with `openssl rand -base64 24 | tr -d '/+='` into
  `/etc/portico/keycloak.env` (0600 root) once.

- [ ] **Step 1: Write the failing checks in `lab/test_scripts.sh`**

```bash
# Milestone 3: the installer renders the appliance files without root.
d=$(mktemp -d)
if bash deploy/install.sh --render-only "$d" --binary /bin/true >/dev/null 2>&1; then ok "install.sh --render-only runs without root"; else ko "install.sh --render-only failed"; fi
for f in etc/portico/portico.yaml etc/containers/systemd/keycloak.container etc/containers/systemd/postgres.container etc/containers/systemd/portico.network etc/systemd/system/portico.service etc/systemd/system/getty@tty1.service.d/override.conf etc/systemd/system/cloudflared.service etc/firewalld/services/portico.xml etc/ssh/sshd_config.d/00-portico.conf etc/sysctl.d/60-portico.conf usr/local/bin/portico; do
  [ -e "$d/$f" ] && ok "install.sh renders $f" || ko "install.sh does not render $f"
done
if grep -q 'KC_HOSTNAME_STRICT=false' "$d/etc/containers/systemd/keycloak.container" && ! grep -q 'lab' "$d/etc/containers/systemd/keycloak.container"; then ok "keycloak.container learns its name from the request, no lab name"; else ko "keycloak.container hostname"; fi
if grep -q 'KC_BOOTSTRAP_ADMIN_PASSWORD=' "$d/etc/portico/keycloak.env" && [ "$(stat -f %Lp "$d/etc/portico/keycloak.env" 2>/dev/null || stat -c %a "$d/etc/portico/keycloak.env")" = 600 ]; then ok "keycloak.env generated, mode 0600"; else ko "keycloak.env"; fi
first=$(grep KC_BOOTSTRAP_ADMIN_PASSWORD "$d/etc/portico/keycloak.env")
bash deploy/install.sh --render-only "$d" --binary /bin/true >/dev/null 2>&1
if [ "$(grep KC_BOOTSTRAP_ADMIN_PASSWORD "$d/etc/portico/keycloak.env")" = "$first" ]; then ok "install.sh keeps the secrets on a second run"; else ko "install.sh regenerated a secret"; fi
if grep -q '<port protocol="tcp" port="443"/>' "$d/etc/firewalld/services/portico.xml" && grep -q 'port="8443"' "$d/etc/firewalld/services/portico.xml" && grep -q 'port="9440"' "$d/etc/firewalld/services/portico.xml" && ! grep -q 'port="8080"' "$d/etc/firewalld/services/portico.xml"; then ok "firewalld service: 443, 8443, 9440 only"; else ko "firewalld service ports"; fi
if grep -q 'PermitRootLogin no' "$d/etc/ssh/sshd_config.d/00-portico.conf" && grep -q 'PasswordAuthentication no' "$d/etc/ssh/sshd_config.d/00-portico.conf"; then ok "sshd: key only, no root"; else ko "sshd policy"; fi
if grep -q 'ExecStart=-/usr/local/bin/portico console' "$d/etc/systemd/system/getty@tty1.service.d/override.conf"; then ok "tty1 runs the console"; else ko "tty1 override"; fi
if ! grep -rn '10\.23\.210\|lab\.portico\.test\|portico-lab' deploy/ | grep -q .; then ok "deploy/ names no lab host"; else ko "deploy/ names the lab"; fi
rm -rf "$d"
```

- [ ] **Step 2: Run them**

Run: `lab/test_scripts.sh 2>&1 | grep -E 'install.sh|keycloak|firewalld service|sshd|tty1|deploy/' `
Expected: every line `FAIL` (the script does not exist).

- [ ] **Step 3: Write `deploy/install.sh` and the files it installs**

The installer, in order: argument parsing; a `put()` helper that
writes to `$ROOT/<path>` with mode and owner (owner skipped in render
mode); packages (`dnf -y install podman firewalld NetworkManager
policycoreutils-python-utils openssl` and cloudflared from
`https://pkg.cloudflare.com/cloudflared.repo`, skipped in render mode);
user `portico` (system, nologin, home `/var/lib/portico`); directories
(`/etc/portico` 0750 root:portico, `/etc/portico/tls` 0750,
`/var/lib/portico` 0755 portico, `/var/lib/portico/keycloak-theme`,
`/etc/cloudflared` 0700); the binary to `/usr/local/bin/portico` 0755
(downloaded and checksum-verified for `--release`); `portico.yaml` from
`deploy/portico.yaml.example` only if absent; `keycloak.env` generated
only if absent; the quadlets, units, firewalld service, sshd and sysctl
drop-ins; then, unless `--no-start` or render: `restorecon`,
`setsebool -P container_manage_cgroup 1` if needed, `systemctl
daemon-reload`, firewalld (service `portico` added to the default
zone, `cockpit` and `dhcpv6-client` removed), `sshd -t` then reload,
`systemctl enable --now postgres keycloak portico`, `systemctl
restart getty@tty1`. The quadlets are the lab ones with the lab name,
the `KC_HOSTNAME` line and the nginx assumptions removed.
`deploy/README.md` explains the two forms (image, existing host) and
the flags.

- [ ] **Step 4: Run the checks and shellcheck**

Run: `lab/test_scripts.sh 2>&1 | tail -1; shellcheck -x deploy/install.sh`
Expected: `PASS`; shellcheck silent.

- [ ] **Step 5: Run the installer for real on the lab VM, render mode first, then with `--no-start` into a scratch root**

Run: `source lab/env.sh && scp deploy/install.sh lab@$LAB_VM_IP:/tmp/ && ssh lab@$LAB_VM_IP 'bash /tmp/install.sh --render-only /tmp/r --binary /bin/true && find /tmp/r -type f | wc -l && rm -rf /tmp/r /tmp/install.sh'`
Expected: the file count of Step 1 (11 files), no error. (The lab VM
keeps its own provisioning; the real install lands on the new VM in
Task 5.)

- [ ] **Step 6: CI: shellcheck over `deploy/` too**

In `.github/workflows/ci.yml`, the scripts job already runs
`lab/test_scripts.sh`, which shellchecks `lab/`; add
`shellcheck -x deploy/install.sh` to the same script's shellcheck line.

- [ ] **Step 7: Commit**

```bash
git add deploy lab/test_scripts.sh .github/workflows/ci.yml
git commit -m "deploy: install.sh installs Portico, Keycloak, PostgreSQL, the console and cloudflared on Rocky 9, for the image and for an existing host"
```

---

### Task 3: The image: kickstart, Packer, OpenSCAP, release workflow

**Files:**
- Create: `image/portico.ks` (kickstart), `image/portico.pkr.hcl` (Packer, QEMU builder), `image/oscap/tailoring.xml`, `image/oscap/README.md` (exceptions, in the words of `nkp-preprovisioned-toolkit/cis/README.md`'s disclaimer), `image/scripts/harden.sh`, `image/scripts/cleanup.sh`, `image/build.sh`, `image/README.md`
- Create: `.github/workflows/image.yml`
- Modify: `.github/workflows/ci.yml` (validation job: `packer validate`, `ksvalidator`, tailoring XML)

**Interfaces:**
- Consumes: `deploy/install.sh --binary PATH --no-start` (Task 2); the
  binary built by the workflow with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$VERSION" ./cmd/portico`.
- Produces: `portico-<version>.qcow2`, `portico-<version>.qcow2.sha256`,
  `portico-<version>-oscap.html`, `portico-<version>-linux-amd64.tar.gz`
  (+ `.sha256`) attached to the release of tag `v<version>`;
  `image/build.sh VERSION BINARY` runs the same build locally on an
  x86_64 host with KVM.

Kickstart (LVM on a 40 GiB disk, sizes in MiB): `/boot` 1024 xfs,
`/boot/efi` 256 (UEFI boot, BIOS also bootable with a BIOS boot
partition), VG `portico`: `/` 10240, `/var` 6144, `/var/log` 4096,
`/var/log/audit` 2048, `/var/tmp` 1024, `/home` 1024,
`/var/lib/containers` 8192, `/var/lib/portico` 4096, no swap (6 GiB RAM,
no hibernation; CIS has no swap rule), the rest free in the VG for
growth. `fstab` options: `nodev,nosuid,noexec` on `/tmp` (tmpfs),
`/var/tmp`, `/dev/shm`, `/home`; `nodev,nosuid` on `/var`, `/var/log`,
`/var/log/audit`; `/var/lib/containers` with `nodev,nosuid` only
(containers need exec: the documented exception). Packages: `@core`
minimal, `cloud-init`, `cloud-utils-growpart`, `qemu-guest-agent`,
`NetworkManager`, `podman`, `firewalld`, `openscap-scanner`,
`scap-security-guide`, `chrony`, `audit`, `rsyslog`, `openssl`; no
`cockpit`. `%pre`: stop with "portico: the disk must be at least 40 GiB
(layout: ...)" on tty1 and in the log when the first disk is smaller
(Review Focus 5). `%post`: a build user with the Packer SSH key (removed
by `cleanup.sh`), `selinux --enforcing`, services enabled.

- [ ] **Step 1: Write the failing validation job in `ci.yml`**

```yaml
  image:
    name: Image definition (packer validate, kickstart, tailoring)
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: hashicorp/setup-packer@v3
        with:
          version: "1.11.2"
      - name: packer validate
        run: cd image && packer init portico.pkr.hcl && packer validate -var version=0.0.0 -var binary=/bin/true portico.pkr.hcl
      - name: kickstart
        run: pipx install pykickstart && ksvalidator -v RHEL9 image/portico.ks
      - name: tailoring is well-formed and names the profile
        run: xmllint --noout image/oscap/tailoring.xml && grep -q 'xccdf_org.ssgproject.content_profile_cis_server_l1' image/oscap/tailoring.xml
```

- [ ] **Step 2: Push the branch and read the job**

Run: `git push -u origin m3-appliance-image && gh run watch --exit-status $(gh run list --branch m3-appliance-image --limit 1 --json databaseId -q '.[0].databaseId')`
Expected: the `image` job fails (no `image/` directory yet); the other jobs pass.

- [ ] **Step 3: Write `image/portico.ks`, `image/portico.pkr.hcl`, the scripts and the tailoring**

Packer: `source "qemu" "portico"` with `iso_url` the Rocky 9 minimal
boot ISO (`https://dl.rockylinux.org/pub/rocky/9/isos/x86_64/Rocky-9-latest-x86_64-boot.iso`,
`iso_checksum` from `CHECKSUM` of the same directory, pinned in the
file), `disk_size "40G"`, `format qcow2`, `accelerator kvm`, `cpus 2`,
`memory 4096`, `http_directory "image"` serving the kickstart,
`boot_command` adding `inst.ks=http://{{ .HTTPIP }}:{{ .HTTPPort }}/portico.ks inst.text`,
`ssh_username build` with the generated key, `shutdown_command "sudo shutdown -P now"`,
`vm_name "portico-${var.version}.qcow2"`, `output_directory "output"`.
Provisioners: `file` for the binary and `deploy/` to `/tmp/portico-build/`;
`shell` `sudo bash /tmp/portico-build/deploy/install.sh --binary /tmp/portico-build/portico --no-start`;
`shell` `sudo bash /tmp/portico-build/image/scripts/harden.sh` (OpenSCAP:
`oscap xccdf eval --profile xccdf_org.ssgproject.content_profile_cis_server_l1 --tailoring-file tailoring.xml --remediate` on
`/usr/share/xml/scap/ssg/content/ssg-rl9-ds.xml`, then a second `eval`
with `--report /tmp/portico-build/oscap.html`, exit code 2 (fails
present) tolerated; the report is fetched with a `file` provisioner
`direction = "download"`); `shell` `cleanup.sh` (build user, SSH host
keys, `/etc/machine-id` emptied, dnf cache, logs, `cloud-init clean`,
`fstrim`). Post-processors: `checksum` sha256. The tailoring selects the
L1 server profile and unselects the rules the layout documents: the
`/var/lib/containers` exec rule, `sshd` listen address (the appliance
decides at the wizard), the rules about `cockpit` (not installed), and
nothing else; every unselected rule carries a `<description>` with the
reason. `image/oscap/README.md` carries the disclaimer: a subset of
remediations, not a compliance statement, CIS and CIS Benchmarks are
trademarks of the Center for Internet Security, Inc., no benchmark
document distributed.

- [ ] **Step 4: Run the validation job**

Run: `git push && gh run watch --exit-status $(gh run list --branch m3-appliance-image --limit 1 --json databaseId -q '.[0].databaseId')`
Expected: all jobs pass.

- [ ] **Step 5: Write `.github/workflows/image.yml`**

Triggers: `push: tags: ['v*']` and `workflow_dispatch` (input
`version`, default `0.0.0-dev`). Steps: checkout, setup-go
(`go.mod`), build the binary with the version, package the tarball and
its sha256, `apt-get install qemu-system-x86 qemu-utils`, setup-packer,
`sudo chmod 666 /dev/kvm` (the runner has KVM), `packer init` and
`packer build` with `-var version -var binary`, upload the qcow2, the
checksums, the report and the tarball as workflow artifacts (always),
and on a tag `softprops/action-gh-release@v2` with those files and
`generate_release_notes: true`. Timeout 60 minutes.

- [ ] **Step 6: Run the image workflow by hand and read the result**

Run: `gh workflow run image.yml --ref m3-appliance-image -f version=0.0.0-dev && sleep 60 && gh run watch --exit-status $(gh run list --workflow image.yml --limit 1 --json databaseId -q '.[0].databaseId')`
Expected: the run passes; the artifacts list the four files; the
kickstart log in the run shows the partitions of the layout; the
OpenSCAP report is downloadable. Record the build time and the qcow2
size for write-up 19. If Packer cannot reach the installer over the
HTTP server or the boot command mistypes (the two classic failures),
fix and re-run: the run log is the only feedback.

- [ ] **Step 7: Commit**

```bash
git add image .github/workflows/image.yml .github/workflows/ci.yml
git commit -m "image: Rocky 9 kickstart with the CIS layout, Packer QEMU build, OpenSCAP hardening and report, release workflow on v* tags"
```

---

### Task 4: OpenTofu deployer for Prism Central

**Files:**
- Create: `deploy/tofu/nutanix/main.tf`, `variables.tf`, `vm.tf`, `outputs.tf`, `cloud-init.tftpl`, `terraform.tfvars.example`, `README.md`, `.gitignore`
- Modify: `.github/workflows/ci.yml` (`tofu fmt -check`, `tofu validate`)

**Interfaces:**
- Consumes: the release asset URL
  `https://github.com/marco-fabbri/portico/releases/download/v<version>/portico-<version>.qcow2`
  (Task 3); the NKP module `tofu/nutanix` as the model (provider 2.5,
  `nutanix_images_v2` from a URL, `nutanix_virtual_machine_v2` with the
  `ipv4_config` reservation on IPAM subnets, the `terraform_data`
  rebuild trigger on the cloud-init hash).
- Produces: variables `nutanix_endpoint`, `nutanix_port` (9440),
  `nutanix_username`, `nutanix_password` (sensitive), `nutanix_insecure`,
  `nutanix_cluster_name`, `nutanix_subnet_name`, `nutanix_subnet_ipam`,
  `nutanix_storage_container_name` (optional), `portico_version`
  (selects the image URL; `portico_image_url` overrides it for a local
  mirror), `vm_name` (default `portico`), `vm_cpus` 2, `vm_memory_mib`
  6144, `vm_disk_gib` 40 (refused below 40 by a validation block),
  `network` (optional object `{address, prefix, gateway, dns}`: when
  set, cloud-init configures the interface in `bootcmd` as write-up 02
  measured, and the wizard is not needed for the network),
  `ssh_public_key` (optional: when set, cloud-init adds it to a user
  `admin` with sudo; without it the VM has no SSH login and the console
  is the only way in). Outputs: `vm_ext_id`, `admin_url`
  (`https://<address>:8443/` when `network` is set, else a note to use
  the console).

- [ ] **Step 1: Write the failing CI steps**

```yaml
  tofu:
    name: OpenTofu deployer (fmt, validate)
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: opentofu/setup-opentofu@v1
        with:
          tofu_version: "1.8.5"
      - run: cd deploy/tofu/nutanix && tofu fmt -check -recursive && tofu init -backend=false && tofu validate
```

- [ ] **Step 2: Push and read the job**

Expected: the `tofu` job fails (no module yet).

- [ ] **Step 3: Write the module**

From the NKP module: the `terraform` and `provider` blocks and the three
`data` lookups verbatim; one `nutanix_images_v2` named
`portico-${var.portico_version}` from the URL; one VM with the OS disk
cloned from the image and grown to `vm_disk_gib`, one NIC, the IPAM
reservation when `nutanix_subnet_ipam` and `network` are set,
`guest_customization` with the rendered cloud-init (the template writes
the keyfile in `bootcmd` exactly as `lab/vm/cloud-init.yaml` does, and
the SSH user only when a key is given), `ignore_changes` on it with the
hash trigger. `README.md`: prerequisites, the three commands, what the
wizard still does (password, exposure), the firewall rule towards 9440.

- [ ] **Step 4: Run the job**

Run: `git push && gh run watch --exit-status $(gh run list --branch m3-appliance-image --limit 1 --json databaseId -q '.[0].databaseId')`
Expected: all jobs pass.

- [ ] **Step 5: Commit**

```bash
git add deploy/tofu .github/workflows/ci.yml
git commit -m "deploy/tofu/nutanix: image from the release URL and the appliance VM on Prism Central, optional cloud-init network"
```

---

### Task 5: Lab: the appliance on NTNX-01, write-up 19, documentation

**Files:**
- Create: `docs/lab/2026-10-19-19-appliance-image.md`, `docs/install.md`
- Create: `lab/appliance/deploy.sh` (wraps `tofu` with the lab values from `lab/env.sh`), `lab/appliance/destroy.sh`, `lab/appliance/terraform.tfvars.example`
- Modify: `docs/lab/README.md`, `docs/lab/lab-inventory.md`, `README.md` (status row 3, badge `status`), `docs/operating.md` (console wizard section), `lab/README.md`

**Interfaces:**
- Consumes: the `0.0.0-dev` artifacts of Task 3 (downloaded with `gh run download` and served to Prism Central from a URL it can reach: the lab VM's nginx is off, so `python3 -m http.server` on the lab VM over SSH, port 8000 opened for the run and closed after), the module of Task 4, the console of Task 1 over the Prism Central VM console (Marco, in Chrome).

- [ ] **Step 1: Write `lab/appliance/deploy.sh` and `destroy.sh`**

`deploy.sh`: downloads the latest `image.yml` artifacts of the branch
(`gh run download`), copies the qcow2 to the lab VM, starts the HTTP
server there, writes a `terraform.tfvars` from `lab/env.sh` and
`~/portico-secrets/prism-pw` (file mode 0600, under `lab/appliance/`,
gitignored), with `vm_name portico-appliance`, address `.191` (ping
check first), the lab subnet `MGMT`, `portico_image_url` pointing at
the lab VM, then `tofu init && tofu apply -auto-approve`; prints the
console URL in Prism Central and the admin URL. `destroy.sh`: `tofu
destroy`, image deleted, HTTP server stopped. Both covered by
`lab/test_scripts.sh` for "no credential on a command line" and
"refuses without env".

- [ ] **Step 2: Run the deployment**

Run: `source lab/env.sh && lab/appliance/deploy.sh`
Expected: image uploaded (record the time), VM `portico-appliance`
powered on with `10.23.210.191`; `curl -k https://10.23.210.191:8443/healthz`
answers 200 within two minutes of power-on (cloud-init network path,
Review Focus 4). Record the boot time.

- [ ] **Step 3: Marco: the console**

Open the VM console in Prism Central: the wizard's menu is on tty1.
Re-run the network screen with Enter on every prompt (Review Focus 2),
set the admin password, show the Keycloak credentials, choose exposure
2 (self-signed for `prism.lab.portico.test id.lab.portico.test`).
Expected: each screen behaves as its test says; `portico` restarts and
the public listener answers on 443 with the self-signed pair.

- [ ] **Step 4: Setup and tenant**

With `/etc/hosts` on the Mac pointing both lab names at `.191` for the
duration of the test: setup page, the three bootstraps (the Prism
Central one creates a second service account `portico-appliance`, the
SAML bootstrap is refused because the lab IdP exists: run it after
`lab/saml/pc-idp.sh delete` or accept the refusal as the measurement
and point the names back to `.190` afterwards; Marco's call at the
time), apply, `lab/tenants/acceptance.sh` against `.191`, carol's login.
Expected: the whole path works from a downloaded image; record the
timings. Then `lab/tenants/acceptance.sh --delete`, names back to
`.190`, `lab/appliance/destroy.sh` unless Marco keeps the VM.

- [ ] **Step 5: Measure `KC_HOSTNAME_STRICT=false`**

On the appliance, with the setup done: the OIDC issuer in the Keycloak
discovery document and the SAML metadata entity ID must carry
`https://id.lab.portico.test`, never the loopback or the VM address.
Run: `curl -k https://id.lab.portico.test/realms/<realm>/.well-known/openid-configuration | jq -r .issuer` from the Mac, and the same through the loopback on the VM.
Expected: the public name from the Mac; from the loopback the issuer
still reads the public name (backchannel dynamic) or the measurement
says otherwise and the installer gets a `KC_HOSTNAME` written by the
wizard (fallback decided by the result).

- [ ] **Step 6: Write-up 19 and documentation**

`docs/lab/2026-10-19-19-appliance-image.md` from `TEMPLATE.md`: build
time and image size on the runner, OpenSCAP pass/fail counts and the
unselected rules, upload and boot times, the console screens as Marco
saw them, the hostname measurement, what failed on the first try.
`docs/install.md`: the appliance path (download, verify the checksum,
import, create the VM with the module or by hand, console, setup) and
the existing-host path (`install.sh --release`), the firewall rule
towards 9440, the two public names and the certificate. `README.md`
status row 3 with the measured numbers; `docs/operating.md` gets the
console wizard; inventory rows for the appliance VM, the image and the
second service account; `lab/README.md` the `lab/appliance/` scripts.

- [ ] **Step 7: Full verification and commit**

Run: `gofmt -l cmd internal; go vet ./... && go test ./... 2>&1 | grep -c '^ok'; lab/test_scripts.sh | tail -1`
Expected: no file; 14 (the console package is new); `PASS`.

```bash
git add docs lab README.md
git commit -m "lab: the appliance booted on NTNX-01 from the built image (write-up 19); install guide; inventory"
```

Then the pull request (rebase merge after the CI, tag
`m3-appliance-image` on `main`), the first release tag `v0.1.0-rc1`
only if Marco wants a downloadable image at this point (the workflow is
the same; the tag is his call).
