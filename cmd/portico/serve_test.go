package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
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
	cfg.PrismCentral.SAMLIdPName = "lab-idp"
	cfg.Keycloak.Realm = "x"
	cfg.Keycloak.AdminClientSecretFile = secret
	cfg.Store.Path = filepath.Join(t.TempDir(), "portico.db")
	cfg.Tenants.ClusterExtID, cfg.Tenants.StorageContainerExtID = "cl-1", "sc-1"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return &cfg
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
	resp := get(t, cfg.Internal.AdminListen, "127.0.0.1", "/login", nil)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 503 || !strings.Contains(string(b), "portico admin set-password") {
		t.Errorf("/login with no password set: %d %q", resp.StatusCode, b)
	}
	resp = get(t, cfg.Internal.AdminListen, "127.0.0.1", "/static/portico.css", nil)
	if resp.StatusCode != 200 {
		t.Errorf("static asset: %d", resp.StatusCode)
	}
}
