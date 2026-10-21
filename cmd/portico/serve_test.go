package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/marco-fabbri/portico/internal/secrets"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/config"
)

func TestServeTLSNilWhenUnset(t *testing.T) {
	c, err := serverTLS(config.TLS{})
	if c != nil || err != nil {
		t.Errorf("got %v %v", c, err)
	}
}

func TestServeTLSLoadsPair(t *testing.T) {
	dir := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	c, err := serverTLS(config.TLS{CertFile: certFile, KeyFile: keyFile})
	if err != nil || c == nil || c.MinVersion != tls.VersionTLS12 || len(c.Certificates) != 1 {
		t.Errorf("got %+v %v", c, err)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func testServeConfig(t *testing.T, prismURL string) *config.Config {
	t.Helper()
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("s3cret\n"), 0o600)
	cfg := config.Default()
	cfg.Public.Listen = freeAddr(t)
	cfg.Public.PrismHost = "prism.test"
	cfg.Public.IDHost = "id.test"
	cfg.Internal.AdminListen = freeAddr(t)
	cfg.Internal.PassthroughListen = freeAddr(t)
	cfg.PrismCentral.URL = prismURL
	cfg.Keycloak.URL = "http://127.0.0.1:1"
	cfg.OIDC.Issuer = "http://127.0.0.1:1/realms/x"
	cfg.OIDC.ClientID = "portico"
	cfg.OIDC.ClientSecretFile = secret
	cfg.PrismCentral.APIKeyFile = secret
	cfg.PrismCentral.SAMLIdPName = "portico-idp"
	cfg.Keycloak.Realm = "x"
	cfg.Keycloak.AdminClientSecretFile = secret
	cfg.Store.Path = filepath.Join(t.TempDir(), "portico.db")
	cfg.Store.MasterKeyFile = filepath.Join(t.TempDir(), "master.key")
	cfg.Tenants.ClusterExtID, cfg.Tenants.StorageContainerExtID = "cl-1", "sc-1"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

// apiClient is a client without keep-alives: the default transport can
// dial a second connection next to the pooled one and leave it without
// a request, and net/http's Shutdown waits five seconds before it
// treats such a connection as idle, which makes a restart test slow.
func apiClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
}

func waitHealthy(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("admin listener did not become healthy")
}

func get(t *testing.T, addr, host, path string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { io.Copy(io.Discard, resp.Body); resp.Body.Close() })
	return resp
}

func TestRunServeStartsAndStops(t *testing.T) {
	prism := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("prism")) }))
	defer prism.Close()
	cfg := testServeConfig(t, prism.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	waitHealthy(t, cfg.Internal.AdminListen)

	resp := get(t, cfg.Public.Listen, "prism.test", "/console/", map[string]string{"Accept": "text/html"})
	if resp.StatusCode != 302 || !strings.HasPrefix(resp.Header.Get("Location"), "/.portico/login") {
		t.Errorf("public /console/: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp = get(t, cfg.Internal.PassthroughListen, "127.0.0.1", "/PrismGateway/services/rest/v1/cluster", map[string]string{"Authorization": "Basic x"})
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "prism" {
		t.Errorf("passthrough: %d %q", resp.StatusCode, b)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runServe returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runServe did not stop within 2s")
	}
}

func TestRunServeWithIdPDown(t *testing.T) {
	prism := httptest.NewServer(http.NotFoundHandler())
	defer prism.Close()
	cfg := testServeConfig(t, prism.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runServe(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	waitHealthy(t, cfg.Internal.AdminListen)

	resp := get(t, cfg.Public.Listen, "prism.test", "/api/x", map[string]string{"Authorization": "Basic x"})
	if resp.StatusCode != 403 {
		t.Errorf("Basic with IdP down: %d, want 403", resp.StatusCode)
	}
	resp = get(t, cfg.Public.Listen, "prism.test", "/.portico/login", nil)
	if resp.StatusCode != 503 {
		t.Errorf("login with IdP down: %d, want 503", resp.StatusCode)
	}
	resp = get(t, cfg.Internal.AdminListen, "127.0.0.1", "/healthz", nil)
	if resp.StatusCode != 200 {
		t.Errorf("healthz with IdP down: %d", resp.StatusCode)
	}
}

func TestServeOpensStoreAndMountsAPI(t *testing.T) {
	prism := httptest.NewServer(http.NotFoundHandler())
	defer prism.Close()
	cfg := testServeConfig(t, prism.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	waitHealthy(t, cfg.Internal.AdminListen)

	resp := get(t, cfg.Internal.AdminListen, "127.0.0.1", "/api/v1/tenants", nil)
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("API without credentials: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	resp = get(t, cfg.Internal.AdminListen, "127.0.0.1", "/healthz", nil)
	if resp.StatusCode != 200 {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
	if _, err := os.Stat(cfg.Store.Path); err != nil {
		t.Errorf("store file: %v", err)
	}
	// A connection the client dialed and never used counts as busy for
	// 5 s in http.Server.Shutdown: close the client's idle ones first.
	http.DefaultClient.CloseIdleConnections()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runServe returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runServe did not stop within 2s")
	}
}

func TestServeServesLoginPage(t *testing.T) {
	prism := httptest.NewServer(http.NotFoundHandler())
	defer prism.Close()
	cfg := testServeConfig(t, prism.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runServe(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	waitHealthy(t, cfg.Internal.AdminListen)
	// A fresh store gets the factory password at start: the login page is
	// served, not the "password not set" page of the CLI path.
	resp := get(t, cfg.Internal.AdminListen, "127.0.0.1", "/login", nil)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Sign in") {
		t.Errorf("/login on a fresh store: %d %q", resp.StatusCode, b)
	}
	resp = get(t, cfg.Internal.AdminListen, "127.0.0.1", "/static/portico.css", nil)
	if resp.StatusCode != 200 {
		t.Errorf("static asset: %d", resp.StatusCode)
	}
}

// Milestone 2d: the runtime settings may come from the store, sealed with
// the master key; a YAML with only process settings is enough then.
func storeServeConfig(t *testing.T, prismURL string, seed byte) (*config.Config, *bytes.Buffer) {
	t.Helper()
	cfg := testServeConfig(t, prismURL)
	// Seed the store through the settings service with a key file.
	keyPath := filepath.Join(t.TempDir(), "master.key")
	key := bytes.Repeat([]byte{seed}, secrets.KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	sealer, _ := secrets.NewSealer(key)
	st, err := store.OpenSQLite(cfg.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	doc := settings.FromConfig(cfg, "api-key-from-store", "admin-secret-from-store", "client-secret-from-store", "")
	if err := settings.NewService(st, sealer).Set(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	st.Close()
	// Now strip the runtime sections from the config: the store must carry them.
	stripped := config.Default()
	stripped.Public.Listen, stripped.Internal.AdminListen, stripped.Internal.PassthroughListen = cfg.Public.Listen, cfg.Internal.AdminListen, cfg.Internal.PassthroughListen
	stripped.Store.Path = cfg.Store.Path
	stripped.Store.MasterKeyFile = keyPath
	stripped.Keycloak.ThemeDir = t.TempDir()
	if err := stripped.ValidateProcess(); err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	return &stripped, &logBuf
}

func TestServeStartsFromStoreSettings(t *testing.T) {
	prism := httptest.NewServer(http.NotFoundHandler())
	defer prism.Close()
	cfg, logBuf := storeServeConfig(t, prism.URL, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg, slog.New(slog.NewJSONHandler(logBuf, nil))) }()
	waitHealthy(t, cfg.Internal.AdminListen)
	if !strings.Contains(logBuf.String(), `"source":"store"`) {
		t.Errorf("log lacks the settings source: %s", logBuf.String())
	}
	for _, secret := range []string{"api-key-from-store", "admin-secret-from-store", "client-secret-from-store"} {
		if strings.Contains(logBuf.String(), secret) {
			t.Errorf("a secret reached the log: %s", logBuf.String())
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runServe returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runServe did not stop within 2s")
	}
}

func TestServeRefusesUnopenableSettings(t *testing.T) {
	prism := httptest.NewServer(http.NotFoundHandler())
	defer prism.Close()
	cfg, logBuf := storeServeConfig(t, prism.URL, 1)
	// Replace the key: the stored secrets no longer open.
	if err := os.WriteFile(cfg.Store.MasterKeyFile, bytes.Repeat([]byte{2}, secrets.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := runServe(ctx, cfg, slog.New(slog.NewJSONHandler(logBuf, nil)))
	if err == nil || !strings.Contains(err.Error(), "sites.site-1.api_key") {
		t.Fatalf("runServe with another key = %v, want an error naming sites.site-1.api_key", err)
	}
	if resp, err := http.Get("http://" + cfg.Internal.AdminListen + "/healthz"); err == nil {
		resp.Body.Close()
		t.Fatal("a listener was bound although the settings could not be opened")
	}
}

func TestServeRestartsOnRequest(t *testing.T) {
	prism := httptest.NewServer(http.NotFoundHandler())
	defer prism.Close()
	cfg, logBuf := storeServeConfig(t, prism.URL, 1)
	st, err := store.OpenSQLite(cfg.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("restart-pw-1"), bcrypt.MinCost)
	if err := st.SetAdminPassword(context.Background(), "admin", string(hash)); err != nil {
		t.Fatal(err)
	}
	st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	log := slog.New(slog.NewJSONHandler(logBuf, nil))
	go func() { done <- runServe(ctx, cfg, log) }()
	waitHealthy(t, cfg.Internal.AdminListen)
	req, _ := http.NewRequest("POST", "http://"+cfg.Internal.AdminListen+"/api/v1/settings/apply", nil)
	req.SetBasicAuth("admin", "restart-pw-1")
	resp, err := apiClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 202 || !strings.Contains(string(body), `"restarting":true`) {
		t.Fatalf("apply: %d %s", resp.StatusCode, body)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrRestart) {
			t.Fatalf("runServe returned %v, want ErrRestart", err)
		}
		var stderr bytes.Buffer
		if code := serveExitCode(err, &stderr, log); code != 0 || stderr.Len() != 0 {
			t.Fatalf("exit code %d, stderr %q; a requested restart must exit 0 quietly", code, stderr.String())
		}
		if !strings.Contains(logBuf.String(), "restarting on request") {
			t.Fatalf("log lacks the restart line: %s", logBuf.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runServe did not return within 3 s of the apply")
	}
}

func TestServeSetupModeWithEmptyStore(t *testing.T) {
	cfg := config.Default()
	cfg.Public.Listen, cfg.Internal.AdminListen, cfg.Internal.PassthroughListen = freeAddr(t), freeAddr(t), freeAddr(t)
	cfg.Store.Path, cfg.Store.MasterKeyFile = filepath.Join(t.TempDir(), "portico.db"), filepath.Join(t.TempDir(), "master.key")
	if err := cfg.ValidateProcess(); err != nil {
		t.Fatal(err)
	}
	st, _ := store.OpenSQLite(cfg.Store.Path)
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw-12345678"), bcrypt.MinCost)
	st.SetAdminPassword(context.Background(), "admin", string(hash))
	st.Close()
	var logBuf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, &cfg, slog.New(slog.NewTextHandler(&logBuf, nil))) }()
	waitHealthy(t, cfg.Internal.AdminListen)
	if _, err := net.DialTimeout("tcp", cfg.Public.Listen, 200*time.Millisecond); err == nil {
		t.Fatal("the public listener is up in setup mode")
	}
	var setupLine string
	for _, l := range strings.Split(logBuf.String(), "\n") {
		if strings.Contains(l, "setup mode") {
			setupLine = l
		}
	}
	if setupLine == "" || !strings.Contains(setupLine, "prism_host") || !strings.Contains(setupLine, "keycloak.realm") {
		t.Fatalf("setup-mode line %q must list the missing keys; log: %s", setupLine, logBuf.String())
	}
	client := apiClient()
	req, _ := http.NewRequest("GET", "http://"+cfg.Internal.AdminListen+"/api/v1/settings", nil)
	req.SetBasicAuth("admin", "pw-12345678")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != 200 || out["mode"] != "setup" {
		t.Fatalf("settings: %d %v", resp.StatusCode, out)
	}
	// A complete document stored through the API, then Apply → ErrRestart.
	body := `{"prism_host":"prism.test","id_host":"id.test","keycloak":{"realm":"x","admin_client_id":"portico-admin","admin_client_secret":"s"},"oidc":{"issuer":"https://id.test/realms/x","client_id":"portico","client_secret":"s"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"10m"}}`
	req, _ = http.NewRequest("PUT", "http://"+cfg.Internal.AdminListen+"/api/v1/settings", strings.NewReader(body))
	req.SetBasicAuth("admin", "pw-12345678")
	req.Header.Set("Content-Type", "application/json")
	if resp, err = client.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("PUT: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	site := `{"code":"SITE-1","url":"https://127.0.0.1:1","tls_verify":false,"api_key":"k","saml_idp_name":"idp"}`
	req, _ = http.NewRequest("POST", "http://"+cfg.Internal.AdminListen+"/api/v1/sites", strings.NewReader(site))
	req.SetBasicAuth("admin", "pw-12345678")
	req.Header.Set("Content-Type", "application/json")
	if resp, err = client.Do(req); err != nil || resp.StatusCode != 201 {
		t.Fatalf("POST site: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	req, _ = http.NewRequest("POST", "http://"+cfg.Internal.AdminListen+"/api/v1/settings/apply", nil)
	req.SetBasicAuth("admin", "pw-12345678")
	if resp, err = client.Do(req); err != nil || resp.StatusCode != 202 {
		t.Fatalf("apply: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRestart) {
			t.Fatalf("runServe returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runServe did not restart")
	}
}

// Review I1: a site added but not finished (no key, no SAML trust yet)
// is not served, and it does not send the appliance back to setup mode.
func TestServeSkipsAnIncompleteSite(t *testing.T) {
	prism := httptest.NewServer(http.NotFoundHandler())
	defer prism.Close()
	cfg, logBuf := storeServeConfig(t, prism.URL, 1)
	key, _ := os.ReadFile(cfg.Store.MasterKeyFile)
	sealer, _ := secrets.NewSealer(key)
	st, err := store.OpenSQLite(cfg.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(st, sealer)
	if err := svc.Patch(context.Background(), func(d *settings.Settings) {
		d.Sites = append(d.Sites, settings.Site{ID: "nyc1", Code: "AZ-US-NYC1", URL: "https://pc2.example.test:9440"})
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg, slog.New(slog.NewJSONHandler(logBuf, nil))) }()
	waitHealthy(t, cfg.Internal.AdminListen)
	conn, err := net.DialTimeout("tcp", cfg.Public.Listen, time.Second)
	if err != nil {
		t.Fatalf("the public listener is off with an incomplete site: %v\n%s", err, logBuf.String())
	}
	conn.Close()
	if strings.Contains(logBuf.String(), "setup mode") || !strings.Contains(logBuf.String(), "site not served until it is complete") {
		t.Fatalf("log: %s", logBuf.String())
	}
	cancel()
	<-done
}

// A fresh appliance's login page wears the default branding: the theme
// is written at every start, also with no branding saved (design
// branding goal 4).
func TestServeAppliesTheDefaultBrandingAtStart(t *testing.T) {
	prism := httptest.NewServer(http.NotFoundHandler())
	defer prism.Close()
	cfg, logBuf := storeServeConfig(t, prism.URL, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg, slog.New(slog.NewJSONHandler(logBuf, nil))) }()
	waitHealthy(t, cfg.Internal.AdminListen)
	css, err := os.ReadFile(filepath.Join(cfg.Keycloak.ThemeDir, "login/resources/css/portico.css"))
	if err != nil || !strings.Contains(string(css), "--portico-bg: #a73422") {
		t.Errorf("theme at start: %v %s", err, css)
	}
	if _, err := os.Stat(filepath.Join(cfg.Keycloak.ThemeDir, "login/resources/img/logo.svg")); err != nil {
		t.Errorf("the arch is not in the theme: %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runServe did not stop within 2s")
	}
}
