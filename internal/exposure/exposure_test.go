package exposure

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

type issued struct {
	cert *x509.Certificate
	der  []byte
	key  crypto.Signer
}

// issue makes a certificate for names signed by parent (self-signed when
// parent is nil), valid around now.
func issue(t *testing.T, names []string, ca bool, parent *issued, key crypto.Signer, notAfter time.Time) issued {
	t.Helper()
	if key == nil {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key = k
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    now.Add(-24 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	if ca {
		tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
	} else {
		tmpl.DNSNames = names
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	parentCert, parentKey := tmpl, key
	if parent != nil {
		parentCert, parentKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parentCert, key.Public(), parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return issued{cert: c, der: der, key: key}
}

func certPEM(certs ...issued) []byte {
	var b []byte
	for _, c := range certs {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der})...)
	}
	return b
}

func ecPEM(t *testing.T, k crypto.Signer) []byte {
	der, err := x509.MarshalECPrivateKey(k.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func pkcs8PEM(t *testing.T, k crypto.Signer) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

var hosts = []string{"prism.msp.example", "id.msp.example"}

func TestValidateNamesTheFailingCheck(t *testing.T) {
	ca := issue(t, []string{"Test CA"}, true, nil, nil, now.Add(10*365*24*time.Hour))
	leaf := issue(t, hosts, false, &ca, nil, now.Add(90*24*time.Hour))
	wrongName := issue(t, []string{"prism.msp.example"}, false, &ca, nil, now.Add(90*24*time.Hour))
	expired := issue(t, hosts, false, &ca, nil, now.Add(-time.Hour))
	other := issue(t, hosts, false, &ca, nil, now.Add(90*24*time.Hour))
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaLeaf := issue(t, hosts, false, &ca, rsaKey, now.Add(90*24*time.Hour))

	for _, c := range []struct {
		name      string
		cert, key []byte
		want      string // "" = accepted
	}{
		{"chain, EC key", certPEM(leaf, ca), ecPEM(t, leaf.key), ""},
		{"EC key as PKCS#8", certPEM(leaf, ca), pkcs8PEM(t, leaf.key), ""},
		{"RSA PKCS#1", certPEM(rsaLeaf, ca), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}), ""},
		{"RSA PKCS#8", certPEM(rsaLeaf), pkcs8PEM(t, rsaKey), ""},
		{"wrong name", certPEM(wrongName, ca), ecPEM(t, wrongName.key), "does not name id.msp.example"},
		{"expired", certPEM(expired, ca), ecPEM(t, expired.key), "expired"},
		{"key mismatch", certPEM(leaf, ca), ecPEM(t, other.key), "the key does not match the certificate"},
		{"chain reversed", certPEM(ca, leaf), ecPEM(t, leaf.key), "the first certificate must be the server's"},
		{"encrypted PKCS#8", certPEM(leaf), pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{1, 2, 3}}), "the key is encrypted: export it without a passphrase"},
		{"encrypted legacy", certPEM(leaf), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,00"}, Bytes: []byte{1}}), "the key is encrypted"},
		{"not PEM", []byte("hello"), ecPEM(t, leaf.key), "the certificate file is not PEM"},
		{"key in the certificate field", ecPEM(t, leaf.key), certPEM(leaf), "the certificate file holds a private key"},
		{"no key", certPEM(leaf), []byte(""), "the key file is not PEM"},
	} {
		t.Run(c.name, func(t *testing.T) {
			info, err := Validate(c.cert, c.key, hosts, now)
			if c.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if len(info.Names) != 2 || info.Fingerprint == "" || info.NotAfter.IsZero() || info.SelfSigned {
					t.Fatalf("info: %+v", info)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// sha1Issue is issue with a SHA-1 signature, which browsers refuse.
func sha1Issue(t *testing.T, names []string, ca bool, parent *issued) issued {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: names[0]}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(90 * 24 * time.Hour),
		SignatureAlgorithm: x509.SHA1WithRSA, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign}
	if ca {
		tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
	} else {
		tmpl.DNSNames, tmpl.ExtKeyUsage = names, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	parentCert, parentKey := tmpl, crypto.Signer(key)
	if parent != nil {
		parentCert, parentKey = parent.cert, parent.key
		if _, ok := parent.key.(*rsa.PrivateKey); !ok {
			t.Fatal("a SHA-1 signature needs an RSA parent")
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parentCert, key.Public(), parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return issued{cert: c, der: der, key: key}
}

// Browsers refuse small RSA keys, curves other than P-256 and P-384,
// Ed25519 certificates and SHA-1 signatures: Validate refuses them
// first, with what to ask the certificate authority for.
func TestValidateRefusesWhatBrowsersRefuse(t *testing.T) {
	gen := func(k crypto.Signer, err error) crypto.Signer {
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	rsaCA := issue(t, []string{"RSA CA"}, true, nil, gen(rsa.GenerateKey(rand.Reader, 2048)), now.Add(10*365*24*time.Hour))
	ca := issue(t, []string{"Test CA"}, true, nil, nil, now.Add(10*365*24*time.Hour))
	leafWith := func(k crypto.Signer) issued { return issue(t, hosts, false, &ca, k, now.Add(90*24*time.Hour)) }
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	sha1Inter := sha1Issue(t, []string{"SHA-1 intermediate"}, true, &rsaCA)
	sha1Leaf := sha1Issue(t, hosts, false, &rsaCA)
	underSha1 := issue(t, hosts, false, &sha1Inter, nil, now.Add(90*24*time.Hour))
	for _, c := range []struct {
		name string
		leaf issued
		rest []issued
		want string // "" = accepted
	}{
		{"RSA 3072", leafWith(gen(rsa.GenerateKey(rand.Reader, 3072))), []issued{ca}, ""},
		{"P-384", leafWith(gen(ecdsa.GenerateKey(elliptic.P384(), rand.Reader))), []issued{ca}, ""},
		{"RSA 1024", leafWith(gen(rsa.GenerateKey(rand.Reader, 1024))), []issued{ca}, "the key is RSA 1024 bits: browsers need at least 2048"},
		{"P-521", leafWith(gen(ecdsa.GenerateKey(elliptic.P521(), rand.Reader))), []issued{ca}, "the key is on curve P-521: browsers accept P-256 and P-384"},
		{"P-224", leafWith(gen(ecdsa.GenerateKey(elliptic.P224(), rand.Reader))), []issued{ca}, "the key is on curve P-224"},
		{"Ed25519", leafWith(ed), []issued{ca}, "browsers do not accept Ed25519 certificates"},
		{"SHA-1 leaf", sha1Leaf, []issued{rsaCA}, "certificate 1 (prism.msp.example) is signed with SHA1-RSA"},
		{"SHA-1 intermediate", underSha1, []issued{sha1Inter, rsaCA}, "certificate 2 (SHA-1 intermediate) is signed with SHA1-RSA"},
		{"SHA-1 self-signed leaf", sha1Issue(t, hosts, false, nil), nil, "certificate 1 (prism.msp.example) is signed with SHA1-RSA"},
	} {
		t.Run(c.name, func(t *testing.T) {
			key, err := x509.MarshalPKCS8PrivateKey(c.leaf.key)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Validate(certPEM(append([]issued{c.leaf}, c.rest...)...), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), hosts, now)
			if c.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	// A self-signed root's own signature is never checked by a browser.
	sha1Root := sha1Issue(t, []string{"SHA-1 root"}, true, nil)
	leaf := issue(t, hosts, false, &sha1Root, gen(rsa.GenerateKey(rand.Reader, 2048)), now.Add(90*24*time.Hour))
	key, _ := x509.MarshalPKCS8PrivateKey(leaf.key)
	if _, err := Validate(certPEM(leaf, sha1Root), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), hosts, now); err != nil {
		t.Fatalf("a SHA-1 self-signed root refused: %v", err)
	}
}

func TestSelfSignedCoversTheNamesAndValidates(t *testing.T) {
	c, k, err := SelfSigned(hosts)
	if err != nil {
		t.Fatal(err)
	}
	info, err := Validate(c, k, hosts, time.Now())
	if err != nil || !info.SelfSigned {
		t.Fatalf("info %+v err %v", info, err)
	}
}

func TestValidateTokenRefusesAnythingButAConnectorToken(t *testing.T) {
	good := base64.StdEncoding.EncodeToString([]byte(`{"a":"acct","t":"3f1c0e0a-0000-4000-8000-000000000000","s":"c2VjcmV0"}`))
	if err := ValidateToken(good); err != nil {
		t.Fatalf("good token refused: %v", err)
	}
	for _, bad := range []string{"", "cloudflared tunnel run --token " + good, "not-base64!", base64.StdEncoding.EncodeToString([]byte(`{"a":"acct"}`))} {
		if err := ValidateToken(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

// fakeHost is the appliance as Apply sees it.
type fakeHost struct {
	files   map[string][]byte
	links   map[string]bool // request-directory entries that are not regular files
	modes   map[string]os.FileMode
	execs   []string
	execErr map[string]error
	execOut map[string]string
}

func newFakeHost() *fakeHost {
	return &fakeHost{files: map[string][]byte{}, links: map[string]bool{}, modes: map[string]os.FileMode{}, execErr: map[string]error{}, execOut: map[string]string{}}
}

func (h *fakeHost) ReadRequestFile(p string) ([]byte, error) {
	if h.links[p] {
		return nil, errors.New("not a regular file")
	}
	return h.ReadFile(p)
}

func (h *fakeHost) WriteResult(p string, b []byte) error { return h.WriteFile(p, b, 0o644) }

func (h *fakeHost) ReadFile(p string) ([]byte, error) {
	b, ok := h.files[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return b, nil
}

func (h *fakeHost) WriteFile(p string, b []byte, m os.FileMode) error {
	h.files[p], h.modes[p] = append([]byte(nil), b...), m
	return nil
}

func (h *fakeHost) Remove(p string) error {
	if _, ok := h.files[p]; !ok {
		return fs.ErrNotExist
	}
	delete(h.files, p)
	return nil
}

func (h *fakeHost) Exec(stdin io.Reader, argv ...string) (string, error) {
	cmd := strings.Join(argv, " ")
	h.execs = append(h.execs, cmd)
	return h.execOut[cmd], h.execErr[cmd]
}

func (h *fakeHost) did(cmd string) bool {
	for _, e := range h.execs {
		if e == cmd {
			return true
		}
	}
	return false
}

const (
	dir        = "/var/lib/portico/exposure"
	cfgPath    = "/etc/portico/portico.yaml"
	directYAML = "public:\n  listen: \":443\"                 # tunnel mode: \"127.0.0.1:8081\" with no tls block\n  tls:                           # both empty = plain HTTP behind cloudflared\n    cert_file: /etc/portico/tls/public.crt\n    key_file: /etc/portico/tls/public.key\n  portal_host: prism.msp.example\n"
	tunnelYAML = "public:\n  listen: \"127.0.0.1:8081\"\n  tls:\n    cert_file: \"\"\n    key_file: \"\"\n"
)

func request(h *fakeHost, r Request) {
	b, _ := json.Marshal(r)
	h.files[dir+"/request.json"] = b
}

func directRequest(t *testing.T, h *fakeHost) (cert, key []byte) {
	cert, key, err := SelfSigned(hosts)
	if err != nil {
		t.Fatal(err)
	}
	h.files[dir+"/public.crt"], h.files[dir+"/public.key"] = cert, key
	request(h, Request{ID: "r1", Mode: "direct", SelfSigned: true})
	return cert, key
}

func TestApplyDirectSameModeDoesNotRestart(t *testing.T) {
	h := newFakeHost()
	h.files[cfgPath] = []byte(directYAML)
	cert, key := directRequest(t, h)
	r := Apply(h, dir, cfgPath)
	if !r.OK || r.ID != "r1" || r.Mode != "direct" {
		t.Fatalf("result %+v", r)
	}
	if string(h.files[CertPath]) != string(cert) || string(h.files[KeyPath]) != string(key) || h.modes[CertPath] != 0o644 || h.modes[KeyPath] != 0o640 {
		t.Fatalf("pair not installed: modes %o %o", h.modes[CertPath], h.modes[KeyPath])
	}
	if h.did("systemctl restart portico") {
		t.Fatalf("restarted on a renewal: %v", h.execs)
	}
	var written Result
	if err := json.Unmarshal(h.files[dir+"/result.json"], &written); err != nil || written.ID != "r1" || !written.OK || h.modes[dir+"/result.json"] != 0o644 {
		t.Fatalf("result.json %q", h.files[dir+"/result.json"])
	}
}

func TestApplyModeChangeRestartsPortico(t *testing.T) {
	h := newFakeHost()
	h.files[cfgPath] = []byte(tunnelYAML)
	directRequest(t, h)
	if r := Apply(h, dir, cfgPath); !r.OK {
		t.Fatalf("result %+v", r)
	}
	y := string(h.files[cfgPath])
	if !strings.Contains(y, `listen: ":443"`) || !strings.Contains(y, "cert_file: /etc/portico/tls/public.crt") || !strings.Contains(y, "key_file: /etc/portico/tls/public.key") {
		t.Fatalf("yaml:\n%s", y)
	}
	if !h.did("systemctl disable --now cloudflared") || !h.did("systemctl restart portico") {
		t.Fatalf("execs %v", h.execs)
	}

	// And back to the tunnel.
	h = newFakeHost()
	h.files[cfgPath] = []byte(directYAML)
	h.files[dir+"/token"] = []byte("tok\n")
	readyAfter(t, 1)
	request(h, Request{ID: "r2", Mode: "tunnel"})
	if r := Apply(h, dir, cfgPath); !r.OK || r.Mode != "tunnel" {
		t.Fatalf("result %+v", r)
	}
	if string(h.files[TokenPath]) != "tok\n" || h.modes[TokenPath] != 0o600 {
		t.Fatalf("token %q %o", h.files[TokenPath], h.modes[TokenPath])
	}
	y = string(h.files[cfgPath])
	if !strings.Contains(y, `listen: "127.0.0.1:8081"`) || !strings.Contains(y, `cert_file: ""`) || !strings.Contains(y, "portal_host: prism.msp.example") {
		t.Fatalf("yaml:\n%s", y)
	}
	if !h.did("systemctl enable cloudflared") || !h.did("systemctl restart cloudflared") || !h.did("systemctl restart portico") {
		t.Fatalf("execs %v", h.execs)
	}
}

func TestApplyTunnelFailureKeepsExposure(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(h *fakeHost)
	}{
		{"start refused", func(h *fakeHost) {
			h.execErr["systemctl restart cloudflared"] = errors.New("Unit cloudflared.service not found")
		}},
		// Measured with cloudflared 2026.10.4: a token Cloudflare does not
		// know keeps it running and retrying ("Failed to get tunnel"),
		// with /ready answering 503 and no restart.
		{"runs but never connects", func(h *fakeHost) {}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeHost()
			h.files[cfgPath] = []byte(directYAML)
			h.files[dir+"/token"] = []byte("bad\n")
			c.set(h)
			request(h, Request{ID: "r3", Mode: "tunnel"})
			r := Apply(h, dir, cfgPath)
			if r.OK || !strings.Contains(r.Message, "cloudflared") || !strings.Contains(r.Message, "keeps its current exposure") {
				t.Fatalf("result %+v", r)
			}
			if string(h.files[cfgPath]) != directYAML || h.did("systemctl restart portico") || !h.did("systemctl disable --now cloudflared") {
				t.Fatalf("exposure changed: execs %v", h.execs)
			}
			if _, ok := h.files[TokenPath]; ok {
				t.Fatal("bad token left in place")
			}
		})
	}
}

func TestApplyDeletesRequestFiles(t *testing.T) {
	h := newFakeHost()
	h.files[cfgPath] = []byte(directYAML)
	h.files[dir+"/public.crt"] = []byte("not a certificate")
	h.files[dir+"/public.key"] = []byte("not a key")
	h.files[dir+"/token"] = []byte("tok\n")
	request(h, Request{ID: "r4", Mode: "direct"})
	r := Apply(h, dir, cfgPath)
	if r.OK || r.ID != "r4" {
		t.Fatalf("result %+v", r)
	}
	for _, f := range []string{"request.json", "public.crt", "public.key", "token"} {
		if _, ok := h.files[dir+"/"+f]; ok {
			t.Fatalf("%s left behind", f)
		}
	}
	if _, ok := h.files[CertPath]; ok {
		t.Fatal("a bad pair was installed")
	}
}

// The path unit fires again when Apply deletes the request: with no
// request there is nothing to do and the last result stays.
func TestApplyWithoutARequestKeepsTheLastResult(t *testing.T) {
	h := newFakeHost()
	h.files[dir+"/result.json"] = []byte(`{"id":"r1","ok":true}`)
	if r := Apply(h, dir, cfgPath); r.ID != "" {
		t.Fatalf("result %+v", r)
	}
	if string(h.files[dir+"/result.json"]) != `{"id":"r1","ok":true}` || len(h.execs) != 0 {
		t.Fatalf("result overwritten or commands run: %v", h.execs)
	}
}

func TestSetModeEditsTheYAML(t *testing.T) {
	text, err := setMode(directYAML, true)
	if err != nil || !strings.Contains(text, `listen: "127.0.0.1:8081"`) || !strings.Contains(text, `key_file: ""`) || !strings.Contains(text, "# tunnel mode") {
		t.Fatalf("%v\n%s", err, text)
	}
	if _, err := setMode("public:\n  listen: :443\n", true); err == nil || !strings.Contains(err.Error(), "public.listen") {
		t.Fatalf("odd YAML: %v", err)
	}
	if !tunnelMode(text) || tunnelMode(directYAML) {
		t.Fatal("tunnelMode")
	}
}

func init() {
	sleep = func(time.Duration) {}
	tunnelReady = func() bool { return false }
}

// readyAfter makes the connector report a registered connection from
// its n-th look on.
func readyAfter(t *testing.T, n int) *int {
	looks := 0
	tunnelReady = func() bool { looks++; return looks >= n }
	t.Cleanup(func() { tunnelReady = func() bool { return false } })
	return &looks
}

// Cloudflare registers the connection seconds after the start (about
// eight, measured): Apply waits for it, up to tunnelWait.
func TestApplyTunnelWaitsForARegisteredConnection(t *testing.T) {
	h := newFakeHost()
	h.files[cfgPath] = []byte(directYAML)
	h.files[dir+"/token"] = []byte("tok\n")
	looks := readyAfter(t, 8)
	request(h, Request{ID: "r5", Mode: "tunnel"})
	if r := Apply(h, dir, cfgPath); !r.OK || *looks != 8 {
		t.Fatalf("result %+v after %d looks", r, *looks)
	}
	h = newFakeHost()
	h.files[cfgPath] = []byte(directYAML)
	h.files[dir+"/token"] = []byte("tok\n")
	looks = readyAfter(t, 1000)
	request(h, Request{ID: "r6", Mode: "tunnel"})
	r := Apply(h, dir, cfgPath)
	if r.OK || !strings.Contains(r.Message, "did not register") || *looks > int(tunnelWait/tunnelPoll)+1 {
		t.Fatalf("result %+v after %d looks", r, *looks)
	}
}

// The request directory belongs to the unprivileged portico user: Apply,
// as root, reads only regular files there (never through a link).
func TestApplyReadsOnlyRegularRequestFiles(t *testing.T) {
	h := newFakeHost()
	h.files[cfgPath] = []byte(directYAML)
	directRequest(t, h)
	h.links[dir+"/public.key"] = true
	r := Apply(h, dir, cfgPath)
	if r.OK || !strings.Contains(r.Message, "key") {
		t.Fatalf("result %+v", r)
	}
	if _, ok := h.files[KeyPath]; ok {
		t.Fatal("installed through a link")
	}
}

// orderHost checks, when the request is deleted, that its result is
// already written: the admin UI stops waiting on either sign.
type orderHost struct {
	*fakeHost
	t *testing.T
}

func (h orderHost) Remove(p string) error {
	if strings.HasSuffix(p, "/request.json") {
		var res Result
		if err := json.Unmarshal(h.files[dir+"/result.json"], &res); err != nil || res.ID != "r9" {
			h.t.Fatalf("request deleted before its result: %q", h.files[dir+"/result.json"])
		}
	}
	return h.fakeHost.Remove(p)
}

func TestApplyWritesTheResultBeforeDeletingTheRequest(t *testing.T) {
	h := newFakeHost()
	h.files[cfgPath] = []byte(directYAML)
	request(h, Request{ID: "r9", Mode: "direct"})
	Apply(orderHost{h, t}, dir, cfgPath)
	if _, ok := h.files[dir+"/request.json"]; ok {
		t.Fatal("request left behind")
	}
}
