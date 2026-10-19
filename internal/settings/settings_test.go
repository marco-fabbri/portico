package settings

import (
	"bytes"
	"context"
	"errors"
	"os"
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
	s.PrismCentral.SAMLIdPName = "portico-idp"
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
}

func newService(t *testing.T) *Service {
	t.Helper()
	return NewService(newStore(t), sealerFor(t, 1))
}

func TestMissingListsEmptyRequiredFields(t *testing.T) {
	var s Settings
	s.PrismHost = "prism.example.test"
	got := s.Missing()
	if len(got) != 16 || got[0] != "id_host" || got[len(got)-1] != "tenants.operation_timeout" {
		t.Fatalf("missing = %v", got)
	}
	full := sample()
	if m := full.Missing(); len(m) != 0 {
		t.Fatalf("complete document missing %v", m)
	}
}

func TestValidateNamesMissingFieldsAndFormat(t *testing.T) {
	var s Settings
	s.Tenants.ProjectPrefix = "Bad Prefix"
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "project_prefix") {
		t.Fatalf("format error: %v", err)
	}
	s = Defaults()
	var inc *IncompleteError
	if err := s.Validate(); !errors.As(err, &inc) || len(inc.Missing) == 0 || !strings.Contains(err.Error(), "missing prism_host, id_host") {
		t.Fatalf("incomplete error: %v", err)
	}
	if s.ValidateFormat() != nil {
		t.Fatal("defaults do not pass the format check")
	}
}

func TestServiceStoresADraft(t *testing.T) {
	svc := newService(t)
	d := Defaults()
	d.PrismHost, d.IDHost = "prism.example.test", "id.example.test"
	if err := svc.Set(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(context.Background())
	if err != nil || got.PrismHost != "prism.example.test" || len(got.Missing()) != 9 {
		t.Fatalf("draft = %+v %v", got, err)
	}
}

// Patch re-reads the stored document under a lock and changes only what
// the function touches, so two writers never revert each other.
func TestServicePatchMergesIntoTheStoredDocument(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	s := Defaults()
	s.PrismHost, s.IDHost = "prism.example.test", "id.example.test"
	if err := svc.Set(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := svc.Patch(ctx, func(d *Settings) { d.Keycloak.Realm = "msp" }); err != nil {
		t.Fatal(err)
	}
	if err := svc.Patch(ctx, func(d *Settings) { d.Tenants.ClusterExtID = "cl-9" }); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx)
	if err != nil || got.Keycloak.Realm != "msp" || got.Tenants.ClusterExtID != "cl-9" || got.PrismHost != "prism.example.test" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	// Nothing stored yet: the patch applies to the defaults.
	empty := newService(t)
	if err := empty.Patch(ctx, func(d *Settings) { d.PrismHost = "p.example.test" }); err != nil {
		t.Fatal(err)
	}
	got, _ = empty.Get(ctx)
	if got.PrismHost != "p.example.test" || got.Tenants.GroupPrefix != Defaults().Tenants.GroupPrefix {
		t.Fatalf("got=%+v", got)
	}
}

func TestValidateFormatRefusesUserinfoURL(t *testing.T) {
	s := Defaults()
	s.PrismCentral.URL = "https://admin:pw-in-url@10.0.0.10:9440"
	err := s.ValidateFormat()
	if err == nil || !strings.Contains(err.Error(), "prism_central.url must not carry credentials") || strings.Contains(err.Error(), "pw-in-url") {
		t.Fatalf("err = %v", err)
	}
}

// A document whose secret is not sealed (written by hand, or by a build
// that did not seal) is reported as that field failing to open.
func TestServiceGetNamesAPlaintextSecret(t *testing.T) {
	st := newStore(t)
	raw := []byte(`{"prism_central":{"url":"https://pc.example.test:9440","api_key":"plain-text-key"}}`)
	if err := st.SetSetting(context.Background(), SettingKey, raw); err != nil {
		t.Fatal(err)
	}
	_, err := NewService(st, sealerFor(t, 1)).Get(context.Background())
	var oe *OpenError
	if !errors.As(err, &oe) || oe.Field != "prism_central.api_key" {
		t.Fatalf("Get on a plaintext secret = %v, want *OpenError naming prism_central.api_key", err)
	}
}

// A change hook runs after every successful save with the document as
// saved (the appliance writes Keycloak's public hostname from it as soon
// as id_host is known: the SAML bootstrap needs it before Apply).
func TestSetRunsTheChangeHooks(t *testing.T) {
	svc := NewService(newStore(t), sealerFor(t, 1))
	var seen []string
	svc.OnChange(func(ctx context.Context, s Settings) error { seen = append(seen, s.IDHost); return nil })
	in := sample()
	in.IDHost = "id.example.test"
	if err := svc.Set(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "id.example.test" {
		t.Fatalf("hook calls: %v", seen)
	}
	// A hook error is the save's error, and the document is stored anyway.
	svc.OnChange(func(ctx context.Context, s Settings) error { return errors.New("disk full") })
	if err := svc.Set(context.Background(), in); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("hook error not returned: %v", err)
	}
	if !svc.Stored(context.Background()) {
		t.Fatal("document not stored")
	}
}

func TestWriteKeycloakHostnameFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keycloak.hostname.env")
	if err := WriteKeycloakHostname(p, "id.example.test"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "KC_HOSTNAME=https://id.example.test\nKC_HOSTNAME_BACKCHANNEL_DYNAMIC=true\n" {
		t.Fatalf("content %q", b)
	}
	// No host yet: nothing written, no error (the hosts card is not saved).
	p2 := filepath.Join(t.TempDir(), "x.env")
	if err := WriteKeycloakHostname(p2, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p2); !os.IsNotExist(err) {
		t.Fatal("a file was written without a host")
	}
}
