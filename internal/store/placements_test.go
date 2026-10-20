package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func samplePlacement(site string) *Placement {
	return &Placement{
		TenantID: "acme", SiteID: site, Status: PlacementCreating,
		ClusterExtID: "c-1", StorageContainerExtID: "sc-1",
		ProjectID: "portico-acme", ProjectExtID: "p-" + site, ResourceGroupExtID: "rg-" + site,
		SAMLGroupExtID: "g-" + site, RoleMembershipExtID: "rm-" + site, IdPShared: true,
	}
}

func TestPlacementCRUD(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := s.CreateTenant(ctx, &Tenant{ID: "acme", Status: StatusCreating}); err != nil {
		t.Fatal(err)
	}
	a, b := samplePlacement("site-a"), samplePlacement("site-b")
	for _, p := range []*Placement{a, b} {
		if err := s.CreatePlacement(ctx, p); err != nil {
			t.Fatalf("CreatePlacement %s: %v", p.SiteID, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		t.Fatal("CreatePlacement must set the timestamps")
	}
	if err := s.CreatePlacement(ctx, samplePlacement("site-a")); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate = %v, want ErrExists", err)
	}
	got, err := s.GetPlacement(ctx, "acme", "site-a")
	if err != nil {
		t.Fatal(err)
	}
	want := *a
	got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	if *got != want {
		t.Fatalf("GetPlacement = %+v, want %+v", *got, want)
	}
	list, err := s.ListPlacements(ctx, "acme")
	if err != nil || len(list) != 2 || list[0].SiteID != "site-a" || list[1].SiteID != "site-b" {
		t.Fatalf("ListPlacements = %+v, %v", list, err)
	}
	got.Status, got.LastError = PlacementFailed, "boom"
	if err := s.UpdatePlacement(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetPlacement(ctx, "acme", "site-a")
	if again.Status != PlacementFailed || again.LastError != "boom" {
		t.Fatalf("after update = %+v", again)
	}
	if err := s.DeletePlacement(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPlacement(ctx, "acme", "site-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
	if err := s.DeletePlacement(ctx, "acme", "site-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
	if err := s.UpdatePlacement(ctx, &Placement{TenantID: "acme", SiteID: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing placement = %v, want ErrNotFound", err)
	}
}

func TestPlacementsCascadeWithTenant(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	s.CreateTenant(ctx, &Tenant{ID: "acme", Status: StatusActive})
	s.CreatePlacement(ctx, samplePlacement("site-a"))
	if err := s.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListPlacements(ctx, "acme"); err != nil || len(list) != 0 {
		t.Fatalf("placements after tenant delete = %+v, %v", list, err)
	}
}

func TestDeletePlacementsBySite(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for _, id := range []string{"acme", "beta"} {
		s.CreateTenant(ctx, &Tenant{ID: id, Status: StatusActive})
		for _, site := range []string{"site-a", "site-b"} {
			p := samplePlacement(site)
			p.TenantID = id
			if err := s.CreatePlacement(ctx, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	bySite, err := s.ListPlacementsBySite(ctx, "site-a")
	if err != nil || len(bySite) != 2 || bySite[0].TenantID != "acme" || bySite[1].TenantID != "beta" {
		t.Fatalf("ListPlacementsBySite = %+v, %v", bySite, err)
	}
	n, err := s.DeletePlacementsBySite(ctx, "site-a")
	if err != nil || n != 2 {
		t.Fatalf("DeletePlacementsBySite = %d, %v", n, err)
	}
	if left, _ := s.ListPlacements(ctx, "acme"); len(left) != 1 || left[0].SiteID != "site-b" {
		t.Fatalf("left = %+v", left)
	}
}

func TestStepSiteRoundTrip(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	s.CreateTenant(ctx, &Tenant{ID: "acme", Status: StatusCreating})
	s.AddStep(ctx, Step{TenantID: "acme", Name: "keycloak-group", OK: true})
	s.AddStep(ctx, Step{TenantID: "acme", SiteID: "site-a", Name: "project", OK: true})
	steps, err := s.ListSteps(ctx, "acme")
	if err != nil || len(steps) != 2 || steps[0].SiteID != "" || steps[1].SiteID != "site-a" {
		t.Fatalf("steps = %+v, %v", steps, err)
	}
}

// openAtVersion builds a database with the migrations up to version
// applied, as an appliance of that release left it.
func openAtVersion(t *testing.T, path string, version int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	entries, _ := migrations.ReadDir("migrations")
	for _, e := range entries {
		v, _ := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if v > version {
			continue
		}
		body, _ := migrations.ReadFile("migrations/" + e.Name())
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		db.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, v)
	}
	return db
}

func TestMigrationMovesTenantIdentifiersToSite1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portico.db")
	db := openAtVersion(t, path, 3)
	now := formatTime(time.Now())
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO tenants (id, display_name, status, project_id, project_ext_id, resource_group_ext_id, saml_group_ext_id,
		keycloak_group_id, role_membership_ext_id, idp_shared, last_op, created_at, updated_at)
		VALUES ('acme','ACME','active','portico-acme','p-1','rg-1','g-1','kc-1','rm-1',1,'provision',?,?)`, now, now)
	mustExec(`INSERT INTO tenants (id, status, created_at, updated_at) VALUES ('empty','failed',?,?)`, now, now)
	mustExec(`INSERT INTO settings (key, value, updated_at) VALUES ('settings', ?, ?)`,
		[]byte(`{"prism_central":{"url":"https://pc:9440"},"tenants":{"cluster_ext_id":"c-1","storage_container_ext_id":"sc-1"}}`), now)
	mustExec(`INSERT INTO tenant_steps (tenant_id, name, ok, at) VALUES ('acme','keycloak-group',1,?)`, now)
	mustExec(`INSERT INTO tenant_steps (tenant_id, name, ok, at) VALUES ('acme','project',1,?)`, now)
	db.Close()

	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	ps, err := s.ListPlacements(ctx, "acme")
	if err != nil || len(ps) != 1 {
		t.Fatalf("placements = %+v, %v", ps, err)
	}
	p := ps[0]
	if p.SiteID != LegacySiteID || p.Status != PlacementActive || p.ProjectID != "portico-acme" || p.ProjectExtID != "p-1" ||
		p.ResourceGroupExtID != "rg-1" || p.SAMLGroupExtID != "g-1" || p.RoleMembershipExtID != "rm-1" || !p.IdPShared ||
		p.ClusterExtID != "c-1" || p.StorageContainerExtID != "sc-1" {
		t.Fatalf("migrated placement = %+v", p)
	}
	if ps, _ := s.ListPlacements(ctx, "empty"); len(ps) != 0 {
		t.Fatalf("a tenant without a project got a placement: %+v", ps)
	}
	steps, _ := s.ListSteps(ctx, "acme")
	if len(steps) != 2 || steps[0].SiteID != "" || steps[1].SiteID != LegacySiteID {
		t.Fatalf("migrated steps = %+v", steps)
	}
}
