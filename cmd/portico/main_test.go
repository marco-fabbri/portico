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
		"prism_central:\n  url: https://pc.example.test:9440\n" +
		"oidc:\n  issuer: https://id.example.test/realms/portico\n  client_id: portico\n  client_secret_file: /nonexistent\n" +
		"store:\n  path: " + dbPath + "\n"
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
