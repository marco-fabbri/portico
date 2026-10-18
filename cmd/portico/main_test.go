package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func writeAdminConfig(t *testing.T) (cfgPath, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "portico.db")
	cfgPath = filepath.Join(dir, "portico.yaml")
	cfg := "public:\n  prism_host: prism.example.test\n  id_host: id.example.test\n" +
		"prism_central:\n  url: https://pc.example.test:9440\n  api_key_file: /nonexistent-key\n  saml_idp_name: lab-idp\n" +
		"keycloak:\n  realm: portico\n  admin_client_secret_file: /nonexistent-kc\n" +
		"oidc:\n  issuer: https://id.example.test/realms/portico\n  client_id: portico\n  client_secret_file: /nonexistent\n" +
		"tenants:\n  cluster_ext_id: cl-1\n  storage_container_ext_id: sc-1\n" +
		"store:\n  path: " + dbPath + "\n  master_key_file: " + filepath.Join(dir, "master.key") + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, dbPath
}

func TestAdminSetPasswordReadsStdin(t *testing.T) {
	cfgPath, dbPath := writeAdminConfig(t)
	var out, errOut strings.Builder
	code := run([]string{"admin", "set-password", "-config", cfgPath}, strings.NewReader("s3cret\n"), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	s, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hash, err := s.AdminPasswordHash(context.Background(), "admin")
	if err != nil {
		t.Fatalf("AdminPasswordHash: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cret")); err != nil {
		t.Fatalf("stored hash does not verify: %v", err)
	}
	if strings.Contains(out.String()+errOut.String(), "s3cret") {
		t.Fatal("the password was printed")
	}
}

func TestAdminSetPasswordRefusesEmpty(t *testing.T) {
	cfgPath, _ := writeAdminConfig(t)
	var out, errOut strings.Builder
	code := run([]string{"admin", "set-password", "-config", cfgPath}, strings.NewReader("\n"), &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "password is empty") {
		t.Fatalf("exit %d, stderr %q; want 2 and 'password is empty'", code, errOut.String())
	}
}

// writeFullConfig writes a YAML with runtime sections and the three
// secret files, as a Milestone 1 to 2c installation has.
func writeFullConfig(t *testing.T) (cfgPath, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "portico.db")
	cfgPath = filepath.Join(dir, "portico.yaml")
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	key := write("prism-api-key", "api-key-from-file\n")
	adm := write("kc-admin-secret", "admin-secret-from-file\n")
	cli := write("oidc-secret", "client-secret-from-file\n")
	ca := write("ca.pem", "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")
	cfg := "public:\n  prism_host: prism.example.test\n  id_host: id.example.test\n" +
		"prism_central:\n  url: https://pc.example.test:9440\n  tls_verify: true\n  ca_file: " + ca + "\n  api_key_file: " + key + "\n  saml_idp_name: lab-idp\n" +
		"keycloak:\n  realm: portico\n  admin_client_secret_file: " + adm + "\n" +
		"oidc:\n  issuer: https://id.example.test/realms/portico\n  client_id: portico\n  client_secret_file: " + cli + "\n" +
		"tenants:\n  cluster_ext_id: cl-1\n  storage_container_ext_id: sc-1\n" +
		"store:\n  path: " + dbPath + "\n  master_key_file: " + filepath.Join(dir, "master.key") + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, dbPath
}

func TestSettingsImportMovesValuesAndSeals(t *testing.T) {
	cfgPath, dbPath := writeFullConfig(t)
	var out, errOut strings.Builder
	if code := run([]string{"settings", "import", "-config", cfgPath}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("import exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "prism_central.api_key") || !strings.Contains(out.String(), "can now be removed") {
		t.Fatalf("import output: %q", out.String())
	}
	s, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.GetSetting(context.Background(), "settings")
	s.Close()
	if err != nil {
		t.Fatalf("store after import: %v", err)
	}
	for _, secret := range []string{"api-key-from-file", "admin-secret-from-file", "client-secret-from-file"} {
		if strings.Contains(string(raw), secret) || strings.Contains(out.String(), secret) {
			t.Fatalf("a secret is in clear (store or stdout): %s", secret)
		}
	}
	if !strings.Contains(string(raw), "BEGIN CERTIFICATE") || !strings.Contains(string(raw), "pc.example.test") {
		t.Fatalf("store lacks the CA or the URL: %s", raw)
	}
	out.Reset()
	if code := run([]string{"settings", "show", "-config", cfgPath}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("show exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "<set>") || !strings.Contains(out.String(), "https://pc.example.test:9440") || strings.Contains(out.String(), "api-key-from-file") || strings.Contains(out.String(), "v1:") {
		t.Fatalf("show output: %q", out.String())
	}
}

func TestSettingsImportRefusesOverwrite(t *testing.T) {
	cfgPath, _ := writeFullConfig(t)
	var out, errOut strings.Builder
	if code := run([]string{"settings", "import", "-config", cfgPath}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("first import: %d %s", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"settings", "import", "-config", cfgPath}, strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "-force") {
		t.Fatalf("second import: %d %q", code, errOut.String())
	}
	// The refusal left the stored document as it was: a changed file is
	// not imported without -force.
	cfg, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(cfgPath, []byte(strings.Replace(string(cfg), "https://pc.example.test:9440", "https://pc-moved.example.test:9440", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := run([]string{"settings", "import", "-config", cfgPath}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Fatalf("third import: %d %q", code, errOut.String())
	}
	out.Reset()
	if code := run([]string{"settings", "show", "-config", cfgPath}, strings.NewReader(""), &out, &errOut); code != 0 || !strings.Contains(out.String(), "https://pc.example.test:9440") || strings.Contains(out.String(), "pc-moved") {
		t.Fatalf("show after a refused import: %d %q", code, out.String())
	}
	errOut.Reset()
	if code := run([]string{"settings", "import", "-config", cfgPath, "-force"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("forced import: %d %q", code, errOut.String())
	}
	out.Reset()
	if code := run([]string{"settings", "show", "-config", cfgPath}, strings.NewReader(""), &out, &errOut); code != 0 || !strings.Contains(out.String(), "pc-moved") {
		t.Fatalf("show after -force: %d %q", code, out.String())
	}
}

func TestSettingsShowWhenNotStored(t *testing.T) {
	cfgPath, _ := writeAdminConfig(t)
	var out, errOut strings.Builder
	if code := run([]string{"settings", "show", "-config", cfgPath}, strings.NewReader(""), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "no settings stored") {
		t.Fatalf("show on empty: %d %q", code, errOut.String())
	}
}

func TestSettingsShowDoesNotCreateKey(t *testing.T) {
	cfgPath, _ := writeAdminConfig(t)
	keyPath := filepath.Join(filepath.Dir(cfgPath), "master.key")
	var out, errOut strings.Builder
	if code := run([]string{"settings", "show", "-config", cfgPath}, strings.NewReader(""), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "no settings stored") {
		t.Fatalf("show on an empty store: %d %s", code, errOut.String())
	}
	if _, err := os.Stat(keyPath); err == nil {
		t.Fatal("settings show created the master key; a read-only command must not")
	}
}

func TestSettingsImportRefusesRoot(t *testing.T) {
	cfgPath, _ := writeFullConfig(t)
	old := euid
	euid = func() int { return 0 }
	t.Cleanup(func() { euid = old })
	var out, errOut strings.Builder
	if code := run([]string{"settings", "import", "-config", cfgPath}, strings.NewReader(""), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "sudo -u portico") {
		t.Fatalf("import as root: %d %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfgPath), "master.key")); err == nil {
		t.Fatal("import as root created the master key")
	}
}
