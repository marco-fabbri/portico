package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Public.Listen != ":443" {
		t.Errorf("Public.Listen = %q, want :443", cfg.Public.Listen)
	}
	if cfg.Keycloak.URL != "http://127.0.0.1:8080" {
		t.Errorf("Keycloak.URL = %q", cfg.Keycloak.URL)
	}
	if cfg.Session.MaxAge != 8*time.Hour || cfg.Session.Idle != 15*time.Minute {
		t.Errorf("Session = %+v, want 8h/15m", cfg.Session)
	}
	if !reflect.DeepEqual(cfg.Policy.AllowPrefixes, DefaultAllowPrefixes) {
		t.Errorf("AllowPrefixes = %v", cfg.Policy.AllowPrefixes)
	}
	if !cfg.PrismCentral.TLSVerify {
		t.Error("PrismCentral.TLSVerify should default to true")
	}
	if cfg.Internal.AdminListen != "127.0.0.1:8443" || cfg.Internal.PassthroughListen != "127.0.0.1:9440" {
		t.Errorf("Internal = %+v", cfg.Internal)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q", cfg.Log.Level)
	}
	if cfg.Public.PortalHost != "prism.example.test" || cfg.OIDC.ClientID != "portico" {
		t.Errorf("values from the file not loaded: %+v", cfg)
	}
}

func TestValidateListsEveryMissingField(t *testing.T) {
	var cfg Config
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate on an empty Config returned nil")
	}
	for _, want := range []string{"public.portal_host", "public.id_host", "prism_central.url", "oidc.issuer", "oidc.client_id", "oidc.client_secret_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestValidateRejectsHalfTLS(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Public.TLS.CertFile = "/some/cert.pem"
	err = cfg.Validate()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "tls") {
		t.Errorf("want a tls error, got %v", err)
	}
}

func TestClientSecretRefusesWorldReadable(t *testing.T) {
	f := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(f, []byte("s3cret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{OIDC: OIDC{ClientSecretFile: f}}
	_, err := cfg.ClientSecret()
	if err == nil || !strings.Contains(err.Error(), "mode") {
		t.Errorf("want a mode error, got %v", err)
	}
}

func TestClientSecretTrimsNewline(t *testing.T) {
	f := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(f, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{OIDC: OIDC{ClientSecretFile: f}}
	got, err := cfg.ClientSecret()
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cret" {
		t.Errorf("got %q, want s3cret", got)
	}
}

// m1Config returns a Config with every Milestone 1 field set and nothing
// from Milestone 2.
func m1Config() Config {
	cfg := Default()
	cfg.Public.PortalHost, cfg.Public.IDHost = "prism.example.test", "id.example.test"
	cfg.PrismCentral.URL = "https://pc.example.test:9440"
	cfg.OIDC.Issuer, cfg.OIDC.ClientID, cfg.OIDC.ClientSecretFile = "https://id.example.test/realms/portico", "portico", "/nonexistent"
	return cfg
}

func TestValidateRequiresMilestone2Fields(t *testing.T) {
	cfg := m1Config()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate without the Milestone 2 fields returned nil")
	}
	for _, want := range []string{"prism_central.api_key_file", "prism_central.saml_idp_name", "keycloak.realm", "keycloak.admin_client_secret_file", "tenants.cluster_ext_id", "tenants.storage_container_ext_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestDefaultsForTenants(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tenants.ProjectPrefix != "portico" || cfg.Tenants.GroupPrefix != "tenant-" || cfg.Tenants.Role != "Portico Tenant" || cfg.Tenants.OperationTimeout != 10*time.Minute {
		t.Errorf("Tenants defaults = %+v", cfg.Tenants)
	}
	if cfg.Store.Path != "/var/lib/portico/portico.db" {
		t.Errorf("Store.Path = %q", cfg.Store.Path)
	}
	if cfg.Keycloak.AdminClientID != "portico-admin" {
		t.Errorf("Keycloak.AdminClientID = %q", cfg.Keycloak.AdminClientID)
	}
	if cfg.Tenants.ClusterExtID != "cl-1" || cfg.PrismCentral.SAMLIdPName != "portico-idp" {
		t.Errorf("values from the file not loaded: %+v %+v", cfg.Tenants, cfg.PrismCentral)
	}
}

func TestPrefixMustBeSlugSafe(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tenants.ProjectPrefix = "Bad Prefix"
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "tenants.project_prefix") {
		t.Errorf("want a project_prefix error, got %v", err)
	}
}

// Review fix 4: the break-glass password travels as HTTP Basic, so an
// internal listener off the loopback must have TLS.
func TestValidateRefusesCleartextInternalListenerOffLoopback(t *testing.T) {
	cases := []struct {
		admin, pass string
		tls         bool
		wantErr     bool
	}{
		{"127.0.0.1:8443", "127.0.0.1:9440", false, false},
		{"[::1]:8443", "localhost:9440", false, false},
		{"0.0.0.0:8443", "127.0.0.1:9440", false, true},
		{"127.0.0.1:8443", "10.0.0.5:9440", false, true},
		{":8443", "127.0.0.1:9440", false, true},
		{"10.0.0.5:8443", "10.0.0.5:9440", true, false},
	}
	for _, c := range cases {
		cfg, err := Load("testdata/minimal.yaml")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Internal.AdminListen, cfg.Internal.PassthroughListen = c.admin, c.pass
		if c.tls {
			cfg.Internal.TLS = TLS{CertFile: "/c.pem", KeyFile: "/k.pem"}
		}
		err = cfg.Validate()
		if c.wantErr && (err == nil || !strings.Contains(err.Error(), "internal.tls")) {
			t.Errorf("%s/%s tls=%v: want an internal.tls error, got %v", c.admin, c.pass, c.tls, err)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s/%s tls=%v: unexpected error %v", c.admin, c.pass, c.tls, err)
		}
	}
}

func TestKeycloakThemeDefaults(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Keycloak.ThemeDir != "/var/lib/portico/keycloak-theme" || cfg.Keycloak.ThemeName != "portico" {
		t.Errorf("Keycloak theme defaults = %q %q", cfg.Keycloak.ThemeDir, cfg.Keycloak.ThemeName)
	}
	cfg.Keycloak.ThemeName = "Bad Name"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "keycloak.theme_name") {
		t.Errorf("want a theme_name error, got %v", err)
	}
}

// Milestone 2d: the YAML's runtime sections may live in the store, so
// Validate splits into process checks (always) and runtime checks (after
// the store overlay).
func TestValidateProcessOnlyChecksProcessFields(t *testing.T) {
	cfg := Default()
	if err := cfg.ValidateProcess(); err != nil {
		t.Fatalf("ValidateProcess on Default() = %v, want nil", err)
	}
	err := cfg.ValidateRuntime()
	if err == nil {
		t.Fatal("ValidateRuntime on Default() = nil, want the runtime fields named")
	}
	for _, want := range []string{"public.portal_host", "prism_central.url", "prism_central.api_key_file", "keycloak.realm", "oidc.issuer", "tenants.cluster_ext_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateRuntime does not mention %s: %v", want, err)
		}
	}
	cfg.Internal.AdminListen = "0.0.0.0:8443"
	err = cfg.ValidateProcess()
	if err == nil || !strings.Contains(err.Error(), "internal.tls") {
		t.Errorf("ValidateProcess must keep the loopback/TLS rule: %v", err)
	}
	cfg.Internal.AdminListen = "127.0.0.1:8443"
	cfg.Session.Idle = 0
	if err := cfg.ValidateProcess(); err == nil || !strings.Contains(err.Error(), "session") {
		t.Errorf("ValidateProcess must keep the session rule: %v", err)
	}
}

func TestValidateIsBoth(t *testing.T) {
	cfg := Default()
	cfg.Session.Idle = 0
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "session") || !strings.Contains(err.Error(), "prism_central.url") {
		t.Fatalf("Validate = %v, want both kinds of errors", err)
	}
}

func TestMasterKeyFileDefaultAndRequired(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Store.MasterKeyFile != "/var/lib/portico/master.key" {
		t.Errorf("Store.MasterKeyFile = %q", cfg.Store.MasterKeyFile)
	}
	cfg.Store.MasterKeyFile = ""
	if err := cfg.ValidateProcess(); err == nil || !strings.Contains(err.Error(), "store.master_key_file") {
		t.Errorf("want a master_key_file error, got %v", err)
	}
}

// Milestone 2d: a YAML without the runtime sections loads (they may be
// in the store); the runtime checks run in serve after the overlay.
func TestLoadAcceptsProcessOnlyYAML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.yaml")
	os.WriteFile(p, []byte("public:\n  listen: \":443\"\nstore:\n  path: /tmp/x.db\n"), 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load process-only YAML: %v", err)
	}
	if err := cfg.ValidateRuntime(); err == nil {
		t.Fatal("ValidateRuntime should still fail on a process-only YAML")
	}
}

// The public listener must not take the loopback port Keycloak is
// published on (review of Milestone 3: tunnel mode listened on
// 127.0.0.1:8080, Keycloak's port, and cloudflared would have reached
// Keycloak instead of Portico).
func TestValidateRefusesThePublicListenerOnKeycloaksPort(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Keycloak.URL = "http://127.0.0.1:8080"
	cfg.Public.Listen, cfg.Public.TLS = "127.0.0.1:8080", TLS{}
	if err := cfg.ValidateProcess(); err == nil || !strings.Contains(err.Error(), "keycloak.url") {
		t.Fatalf("public.listen on Keycloak's port: %v", err)
	}
	cfg.Public.Listen = "127.0.0.1:8081"
	if err := cfg.ValidateProcess(); err != nil {
		t.Fatalf("a free loopback port refused: %v", err)
	}
}

// public.prism_host was renamed public.portal_host before v0.1.0 (with
// sites the name leads to the portal, not to a Prism Central); a file
// written with the old key still loads.
func TestConfigReadsTheOldPortalHostKey(t *testing.T) {
	base, _ := os.ReadFile("testdata/minimal.yaml")
	path := filepath.Join(t.TempDir(), "old.yaml")
	os.WriteFile(path, []byte(strings.Replace(string(base), "portal_host:", "prism_host:", 1)), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Public.PortalHost != "prism.example.test" {
		t.Fatalf("PortalHost = %q", cfg.Public.PortalHost)
	}
}

func TestConfigRefusesBothHostKeysDiffering(t *testing.T) {
	base, _ := os.ReadFile("testdata/minimal.yaml")
	path := filepath.Join(t.TempDir(), "both.yaml")
	os.WriteFile(path, []byte(strings.Replace(string(base), "portal_host:", "prism_host: other.example.test\n  portal_host:", 1)), 0o600)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "public.prism_host is the old name of public.portal_host; keep one") {
		t.Fatalf("err = %v", err)
	}
}

// One public name cannot serve both the portal and the identity provider:
// Portico routes by host name.
func TestValidateRefusesEqualPublicHosts(t *testing.T) {
	cfg := m1Config()
	cfg.Public.IDHost = "Prism.example.test"
	err := cfg.ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "public.id_host must differ from public.portal_host") {
		t.Fatalf("equal hosts: %v", err)
	}
}

// Tenants' sessions travel only under TLS: a public listener off the
// loopback without a pair is refused (write-up 36, P3).
func TestValidateRefusesCleartextPublicListenerOffLoopback(t *testing.T) {
	cases := []struct {
		listen  string
		wantErr bool
	}{
		{":443", true},
		{"0.0.0.0:443", true},
		{"127.0.0.1:8081", false},
	}
	for _, c := range cases {
		cfg, err := Load("testdata/minimal.yaml")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Public.Listen, cfg.Public.TLS = c.listen, TLS{}
		err = cfg.CheckPublicTLS()
		if c.wantErr && (err == nil || !strings.Contains(err.Error(), "public.listen") || !strings.Contains(err.Error(), "public.tls is empty")) {
			t.Errorf("%s: err = %v, want a refusal naming public.listen and public.tls", c.listen, err)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: err = %v", c.listen, err)
		}
	}
}
