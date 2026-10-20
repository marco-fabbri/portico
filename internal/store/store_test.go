package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTemp(t *testing.T) *SQLite {
	t.Helper()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "portico.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleTenant() *Tenant {
	return &Tenant{
		ID: "acme", DisplayName: "ACME", Contacts: "ops@acme.test", Status: StatusCreating,
		KeycloakGroupID: "kc-1", LastError: "", LastOp: OpProvision,
	}
}

func TestOpenCreatesSchemaAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portico.db")
	s1, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s1.Close()
	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s2.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file: %v", err)
	}
	if _, err := s2.ListTenants(context.Background()); err != nil {
		t.Fatalf("ListTenants after reopen: %v", err)
	}
}

func TestTenantCRUD(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	in := sampleTenant()
	if err := s.CreateTenant(ctx, in); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if in.CreatedAt.IsZero() || in.UpdatedAt.IsZero() {
		t.Fatal("CreateTenant must set CreatedAt and UpdatedAt")
	}
	got, err := s.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	want := *in
	got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	if *got != want {
		t.Fatalf("GetTenant = %+v, want %+v", *got, want)
	}
	if err := s.CreateTenant(ctx, sampleTenant()); !errors.Is(err, ErrExists) {
		t.Fatalf("second CreateTenant = %v, want ErrExists", err)
	}
	got.Status = StatusActive
	got.LastError = ""
	before := in.UpdatedAt
	time.Sleep(5 * time.Millisecond)
	if err := s.UpdateTenant(ctx, got); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	again, _ := s.GetTenant(ctx, "acme")
	if again.Status != StatusActive {
		t.Fatalf("Status after update = %q", again.Status)
	}
	if !again.UpdatedAt.After(before) {
		t.Fatalf("UpdatedAt not bumped: %v vs %v", again.UpdatedAt, before)
	}
	list, err := s.ListTenants(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "acme" {
		t.Fatalf("ListTenants = %v, %v", list, err)
	}
	if err := s.UpdateTenant(ctx, &Tenant{ID: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateTenant unknown = %v, want ErrNotFound", err)
	}
	if err := s.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if _, err := s.GetTenant(ctx, "acme"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTenant after delete = %v, want ErrNotFound", err)
	}
}

func TestStepsAreOrderedAndCascade(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := s.CreateTenant(ctx, sampleTenant()); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"keycloak-group", "project", "resource-group"} {
		if err := s.AddStep(ctx, Step{TenantID: "acme", Name: n, OK: n != "project", Detail: "d-" + n}); err != nil {
			t.Fatalf("AddStep %s: %v", n, err)
		}
	}
	steps, err := s.ListSteps(ctx, "acme")
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 3 || steps[0].Name != "keycloak-group" || steps[1].Name != "project" || steps[2].Name != "resource-group" {
		t.Fatalf("ListSteps order = %+v", steps)
	}
	if steps[1].OK || !steps[0].OK || steps[0].At.IsZero() || steps[0].Detail != "d-keycloak-group" {
		t.Fatalf("step fields = %+v", steps[0])
	}
	if err := s.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	steps, _ = s.ListSteps(ctx, "acme")
	if len(steps) != 0 {
		t.Fatalf("steps after DeleteTenant = %d, want 0", len(steps))
	}
}

func TestAdminPassword(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.AdminPasswordHash(ctx, "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AdminPasswordHash empty = %v, want ErrNotFound", err)
	}
	if err := s.SetAdminPassword(ctx, "admin", "$2a$hash1"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.AdminPasswordHash(ctx, "admin"); h != "$2a$hash1" {
		t.Fatalf("hash = %q", h)
	}
	if err := s.SetAdminPassword(ctx, "admin", "$2a$hash2"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.AdminPasswordHash(ctx, "admin"); h != "$2a$hash2" {
		t.Fatalf("hash after overwrite = %q", h)
	}
}

func TestOpenSQLiteRefusesUnwritableDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "portico.db")
	_, err := OpenSQLite(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("OpenSQLite = %v, want an error naming %s", err, path)
	}
}

// 2a review minor: the database and its sidecars are not group/world readable.
func TestOpenSQLiteSetsFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portico.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetAdminPassword(context.Background(), "admin", "h"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(f)
		if err != nil {
			continue // a sidecar may not exist yet
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s has mode %04o, want 0600", f, mode)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsRoundTripAndNotFound(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.GetSetting(ctx, "branding"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSetting unknown = %v, want ErrNotFound", err)
	}
	if err := s.SetSetting(ctx, "branding", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if v, err := s.GetSetting(ctx, "branding"); err != nil || string(v) != `{"a":1}` {
		t.Fatalf("GetSetting = %q, %v", v, err)
	}
	if err := s.SetSetting(ctx, "branding", []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting(ctx, "branding"); string(v) != `{"a":2}` {
		t.Fatalf("GetSetting after overwrite = %q", v)
	}
}
