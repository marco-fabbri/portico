package settings

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/config"
	"github.com/marco-fabbri/portico/internal/secrets"
	"github.com/marco-fabbri/portico/internal/store"
)

func sealerFor(t *testing.T, seed byte) *secrets.Sealer {
	t.Helper()
	s, err := secrets.NewSealer(bytes.Repeat([]byte{seed}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func sample() Settings {
	var s Settings
	s.PrismHost, s.IDHost = "prism.example.test", "id.example.test"
	s.PrismCentral.URL = "https://pc.example.test:9440"
	s.PrismCentral.TLSVerify = true
	s.PrismCentral.CAPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	s.PrismCentral.APIKey = "k1-api-key-secret"
	s.PrismCentral.SAMLIdPName = "lab-idp"
	s.Keycloak.Realm = "portico"
	s.Keycloak.AdminClientID = "portico-admin"
	s.Keycloak.AdminClientSecret = "kc-admin-secret"
	s.OIDC.Issuer = "https://id.example.test/realms/portico"
	s.OIDC.ClientID = "portico"
	s.OIDC.ClientSecret = "oidc-client-secret"
	s.Tenants.ProjectPrefix, s.Tenants.GroupPrefix, s.Tenants.Role = "portico", "tenant-", "Project Admin"
	s.Tenants.ClusterExtID, s.Tenants.StorageContainerExtID = "cl-1", "sc-1"
	s.Tenants.OperationTimeout = 10 * time.Minute
	return s
}

func TestFromConfigOverlayRoundTrip(t *testing.T) {
	cfg, err := config.Load("../config/testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := FromConfig(cfg, "k1", "a1", "c1", "CA-PEM")
	if s.PrismHost != "prism.example.test" || s.PrismCentral.APIKey != "k1" || s.Keycloak.AdminClientSecret != "a1" || s.OIDC.ClientSecret != "c1" || s.PrismCentral.CAPEM != "CA-PEM" || s.Tenants.ClusterExtID != "cl-1" {
		t.Fatalf("FromConfig = %+v", s)
	}
	out := config.Default()
	rt, err := s.Overlay(&out)
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}
	if out.Public.PrismHost != cfg.Public.PrismHost || out.PrismCentral.URL != cfg.PrismCentral.URL || out.Keycloak.Realm != cfg.Keycloak.Realm || out.OIDC.Issuer != cfg.OIDC.Issuer || out.Tenants.ClusterExtID != cfg.Tenants.ClusterExtID || out.Tenants.OperationTimeout != cfg.Tenants.OperationTimeout {
		t.Fatalf("Overlay result = %+v", out)
	}
	if rt.APIKey != "k1" || rt.AdminSecret != "a1" || rt.ClientSecret != "c1" || rt.CAPEM != "CA-PEM" {
		t.Fatalf("Runtime = %+v", rt)
	}
	// The overlay must mark the secret-file fields as provided so that
	// ValidateRuntime does not demand files that no longer exist.
	if err := out.ValidateRuntime(); err != nil {
		t.Fatalf("ValidateRuntime after Overlay: %v", err)
	}
}

func TestServiceSetSealsSecrets(t *testing.T) {
	st := newStore(t)
	svc := NewService(st, sealerFor(t, 1))
	ctx := context.Background()
	if err := svc.Set(ctx, sample()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	raw, err := st.GetSetting(ctx, SettingKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"k1-api-key-secret", "kc-admin-secret", "oidc-client-secret"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("raw store value contains %q", secret)
		}
	}
	if n := bytes.Count(raw, []byte(`"v1:`)); n != 3 {
		t.Fatalf("sealed values in the raw store = %d, want 3: %s", n, raw)
	}
	got, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PrismCentral.APIKey != "k1-api-key-secret" || got.OIDC.ClientSecret != "oidc-client-secret" || got.PrismCentral.URL != "https://pc.example.test:9440" || got.Tenants.OperationTimeout != 10*time.Minute {
		t.Fatalf("Get = %+v", got)
	}
	if !svc.Stored(ctx) {
		t.Fatal("Stored = false after Set")
	}
}

func TestServiceGetNotStored(t *testing.T) {
	svc := NewService(newStore(t), sealerFor(t, 1))
	if _, err := svc.Get(context.Background()); !errors.Is(err, ErrNotStored) {
		t.Fatalf("Get on empty = %v, want ErrNotStored", err)
	}
	if svc.Stored(context.Background()) {
		t.Fatal("Stored = true on an empty store")
	}
}

func TestServiceGetWrongKeyNamesField(t *testing.T) {
	st := newStore(t)
	if err := NewService(st, sealerFor(t, 1)).Set(context.Background(), sample()); err != nil {
		t.Fatal(err)
	}
	_, err := NewService(st, sealerFor(t, 2)).Get(context.Background())
	var oe *OpenError
	if !errors.As(err, &oe) || oe.Field != "prism_central.api_key" || !strings.Contains(err.Error(), "prism_central.api_key") {
		t.Fatalf("Get with another key = %v, want *OpenError naming prism_central.api_key", err)
	}
	if !errors.Is(err, secrets.ErrOpen) {
		t.Fatalf("OpenError should wrap secrets.ErrOpen: %v", err)
	}
}

func TestServiceUpdateKeepsSecretsWhenEmpty(t *testing.T) {
	svc := NewService(newStore(t), sealerFor(t, 1))
	ctx := context.Background()
	if err := svc.Set(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	in := sample()
	in.PrismCentral.URL = "https://pc2.example.test:9440"
	in.PrismCentral.APIKey, in.Keycloak.AdminClientSecret, in.OIDC.ClientSecret = "", "", "oidc-new"
	if err := svc.Update(ctx, in); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := svc.Get(ctx)
	if got.PrismCentral.URL != "https://pc2.example.test:9440" || got.PrismCentral.APIKey != "k1-api-key-secret" || got.Keycloak.AdminClientSecret != "kc-admin-secret" || got.OIDC.ClientSecret != "oidc-new" {
		t.Fatalf("Get after Update = %+v", got)
	}
}

func TestSetValidates(t *testing.T) {
	st := newStore(t)
	svc := NewService(st, sealerFor(t, 1))
	in := sample()
	in.Tenants.ProjectPrefix = "Bad Prefix"
	err := svc.Set(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "tenants.project_prefix") {
		t.Fatalf("Set invalid = %v", err)
	}
	if svc.Stored(context.Background()) {
		t.Fatal("an invalid Set stored something")
	}
	in = sample()
	in.PrismCentral.APIKey = ""
	err = svc.Set(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "prism_central.api_key") {
		t.Fatalf("Set without the API key = %v, want an error naming it", err)
	}
}
