package tenant

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/store"
)

type fakeUserGroups map[string][]keycloak.Group

func (f fakeUserGroups) UserGroups(ctx context.Context, userID string) ([]keycloak.Group, error) {
	gs, ok := f[userID]
	if !ok {
		return nil, keycloak.ErrNotFound
	}
	return gs, nil
}

func resolverEnv(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, id := range []string{"acme", "beta"} {
		st.CreateTenant(context.Background(), &store.Tenant{ID: id, Status: store.StatusActive})
	}
	return st
}

func TestResolverOneTenant(t *testing.T) {
	st := resolverEnv(t)
	r := Resolver(fakeUserGroups{"u1": {{ID: "g", Name: "staff"}, {ID: "g1", Name: "tenant-acme"}}}, st, "tenant-", slog.Default())
	if got, err := r(context.Background(), "u1"); err != nil || got != "acme" {
		t.Fatalf("resolve = %q %v", got, err)
	}
}

func TestResolverNoGroup(t *testing.T) {
	st := resolverEnv(t)
	r := Resolver(fakeUserGroups{"u1": {{ID: "g", Name: "staff"}}}, st, "tenant-", slog.Default())
	if got, err := r(context.Background(), "u1"); err != nil || got != "" {
		t.Fatalf("resolve = %q %v", got, err)
	}
	if _, err := r(context.Background(), "unknown"); !errors.Is(err, keycloak.ErrNotFound) {
		t.Fatalf("unknown user = %v", err)
	}
}

func TestResolverTwoTenantsIsNone(t *testing.T) {
	st := resolverEnv(t)
	r := Resolver(fakeUserGroups{"u1": {{ID: "g1", Name: "tenant-acme"}, {ID: "g2", Name: "tenant-beta"}}}, st, "tenant-", slog.Default())
	if got, err := r(context.Background(), "u1"); err != nil || got != "" {
		t.Fatalf("a user in two tenants resolved to %q %v", got, err)
	}
}

func TestResolverGroupWithoutRow(t *testing.T) {
	st := resolverEnv(t)
	r := Resolver(fakeUserGroups{"u1": {{ID: "g1", Name: "tenant-gone"}}}, st, "tenant-", slog.Default())
	if got, err := r(context.Background(), "u1"); err != nil || got != "" {
		t.Fatalf("a group without a tenant row resolved to %q %v", got, err)
	}
}
