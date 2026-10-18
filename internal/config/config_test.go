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
	if cfg.Public.PrismHost != "prism.example.test" || cfg.OIDC.ClientID != "portico" {
		t.Errorf("values from the file not loaded: %+v", cfg)
	}
}

func TestValidateListsEveryMissingField(t *testing.T) {
	var cfg Config
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate on an empty Config returned nil")
	}
	for _, want := range []string{"public.prism_host", "public.id_host", "prism_central.url", "oidc.issuer", "oidc.client_id", "oidc.client_secret_file"} {
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
	cfg.Public.PrismHost, cfg.Public.IDHost = "prism.example.test", "id.example.test"
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
	if cfg.Tenants.ProjectPrefix != "portico" || cfg.Tenants.GroupPrefix != "tenant-" || cfg.Tenants.Role != "Project Admin" || cfg.Tenants.OperationTimeout != 10*time.Minute {
		t.Errorf("Tenants defaults = %+v", cfg.Tenants)
	}
	if cfg.Store.Path != "/var/lib/portico/portico.db" {
		t.Errorf("Store.Path = %q", cfg.Store.Path)
	}
	if cfg.Keycloak.AdminClientID != "portico-admin" {
		t.Errorf("Keycloak.AdminClientID = %q", cfg.Keycloak.AdminClientID)
	}
	if cfg.Tenants.ClusterExtID != "cl-1" || cfg.PrismCentral.SAMLIdPName != "lab-idp" {
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
