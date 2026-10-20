package tenant

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant/tenanttest"
)

// env2 is a service over two sites, each with its own fake Prism Central.
type env2 struct {
	svc      *Service
	st       *store.SQLite
	pcA, pcB *tenanttest.PC
	kc       *tenanttest.KC
	log      *bytes.Buffer
	changes  []string
}

func newEnv2(t *testing.T) *env2 {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "portico.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env2{st: st, pcA: tenanttest.NewPC("portico-idp"), pcB: tenanttest.NewPC("portico-idp"), kc: tenanttest.NewKC(), log: &bytes.Buffer{}}
	log := slog.New(slog.NewJSONHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sites := []Site{
		{ID: "site-a", Code: "SITE-A", PC: e.pcA, SAMLIdPName: "portico-idp", ClusterExtID: "cl-a", StorageContainerExtID: "sc-a"},
		{ID: "site-b", Code: "SITE-B", PC: e.pcB, SAMLIdPName: "portico-idp", ClusterExtID: "cl-b", StorageContainerExtID: "sc-b"},
	}
	cfg := Config{ProjectPrefix: "portico", GroupPrefix: "tenant-", RoleName: "Project Admin", OperationTimeout: 5 * time.Second}
	e.svc = NewService(st, sites, e.kc, cfg, log)
	e.svc.OnPlacementChange(func(id string) { e.changes = append(e.changes, id) })
	return e
}

func (e *env2) createOn(t *testing.T, slug string, sites ...string) {
	t.Helper()
	req := CreateRequest{Slug: slug, DisplayName: strings.ToUpper(slug)}
	for _, s := range sites {
		req.Sites = append(req.Sites, PlacementRequest{Site: s})
	}
	if _, err := e.svc.Create(context.Background(), req); err != nil {
		t.Fatalf("Create %s on %v: %v", slug, sites, err)
	}
	e.svc.Wait()
}

func (e *env2) placement(t *testing.T, tenant, site string) *store.Placement {
	t.Helper()
	p, err := e.st.GetPlacement(context.Background(), tenant, site)
	if err != nil {
		t.Fatalf("placement %s/%s: %v", tenant, site, err)
	}
	return p
}

func (e *env2) tenant(t *testing.T, id string) *store.Tenant {
	t.Helper()
	tn, err := e.st.GetTenant(context.Background(), id)
	if err != nil {
		t.Fatalf("tenant %s: %v", id, err)
	}
	return tn
}

func TestCreateOnTwoSites(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a", "site-b")
	if tn := e.tenant(t, "acme"); tn.Status != store.StatusActive {
		t.Fatalf("tenant = %q (%s)", tn.Status, tn.LastError)
	}
	for _, c := range []struct {
		site string
		pc   *tenanttest.PC
		cl   string
	}{{"site-a", e.pcA, "cl-a"}, {"site-b", e.pcB, "cl-b"}} {
		p := e.placement(t, "acme", c.site)
		if p.Status != store.PlacementActive || p.ProjectID != "portico-acme" || p.ClusterExtID != c.cl || p.ProjectExtID == "" || p.SAMLGroupExtID == "" || p.RoleMembershipExtID == "" || !p.IdPShared {
			t.Fatalf("placement %s = %+v", c.site, p)
		}
		if c.pc.Creates["CreateProject"] != 1 || c.pc.RGCluster[c.pc.RGs[0].ExtID] != c.cl {
			t.Fatalf("%s: creates %v rgs %+v", c.site, c.pc.Creates, c.pc.RGs)
		}
	}
	if len(e.kc.Groups) != 1 {
		t.Fatalf("keycloak groups = %+v (one per tenant)", e.kc.Groups)
	}
	steps, _ := e.st.ListSteps(context.Background(), "acme")
	if len(steps) != 13 || steps[0].Name != "keycloak-group" || steps[0].SiteID != "" || steps[1].SiteID != "site-a" || steps[12].SiteID != "site-b" || steps[12].Name != "visibility" {
		t.Fatalf("steps = %+v", steps)
	}
}

func TestPartialFailureAndRetry(t *testing.T) {
	e := newEnv2(t)
	e.pcB.FailOnce["CreateRoleMembership"] = &nutanix.APIError{Status: 500, Message: "membership exploded"}
	e.createOn(t, "acme", "site-a", "site-b")
	tn := e.tenant(t, "acme")
	if tn.Status != store.StatusFailed || !strings.Contains(tn.LastError, "site SITE-B") || !strings.Contains(tn.LastError, "role-membership") {
		t.Fatalf("tenant = %q %q", tn.Status, tn.LastError)
	}
	if a := e.placement(t, "acme", "site-a"); a.Status != store.PlacementActive {
		t.Fatalf("site-a = %+v", a)
	}
	if b := e.placement(t, "acme", "site-b"); b.Status != store.PlacementFailed || !strings.Contains(b.LastError, "membership exploded") {
		t.Fatalf("site-b = %+v", b)
	}
	if err := e.svc.Retry(context.Background(), "acme"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.svc.Wait()
	if tn := e.tenant(t, "acme"); tn.Status != store.StatusActive || tn.LastError != "" {
		t.Fatalf("after retry = %q %q", tn.Status, tn.LastError)
	}
	if b := e.placement(t, "acme", "site-b"); b.Status != store.PlacementActive || b.LastError != "" {
		t.Fatalf("site-b after retry = %+v", b)
	}
	steps, _ := e.st.ListSteps(context.Background(), "acme")
	for _, s := range steps[13:] {
		if s.SiteID == "site-a" {
			t.Fatalf("the retry ran a step on the active site: %+v", s)
		}
	}
}

func TestCreateDefaultsToOnlySite(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	p, err := e.st.GetPlacement(context.Background(), "acme", "site-a")
	if err != nil || p.Status != store.PlacementActive || p.ClusterExtID != "cl-1" || p.StorageContainerExtID != "sc-1" {
		t.Fatalf("placement = %+v %v", p, err)
	}
}

func TestCreateRequestErrors(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		req  CreateRequest
		want error
	}{
		{"no site with several configured", CreateRequest{Slug: "a1", DisplayName: "x"}, ErrNoSites},
		{"unknown site", CreateRequest{Slug: "a2", DisplayName: "x", Sites: []PlacementRequest{{Site: "nope"}}}, ErrUnknownSite},
		{"site twice", CreateRequest{Slug: "a3", DisplayName: "x", Sites: []PlacementRequest{{Site: "site-a"}, {Site: "site-a"}}}, ErrPlacementExists},
	} {
		if _, err := e.svc.Create(ctx, c.req); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if _, err := e.st.GetTenant(ctx, c.req.Slug); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: a row was stored", c.name)
		}
	}
	e.svc.sites["site-a"] = Site{ID: "site-a", Code: "SITE-A", PC: e.pcA, SAMLIdPName: "portico-idp"}
	if _, err := e.svc.Create(ctx, CreateRequest{Slug: "a4", DisplayName: "x", Sites: []PlacementRequest{{Site: "site-a"}}}); !errors.Is(err, ErrNoPlacementTarget) {
		t.Errorf("no cluster: err = %v", err)
	}
	e.svc.Wait()
	if e.pcA.Calls != 0 || e.pcB.Calls != 0 || e.kc.Calls != 0 {
		t.Fatal("a refused request reached a backend")
	}
}

func TestPlacementDefaultsFromSite(t *testing.T) {
	e := newEnv2(t)
	if _, err := e.svc.Create(context.Background(), CreateRequest{Slug: "acme", DisplayName: "x", Sites: []PlacementRequest{{Site: "site-a", ClusterExtID: "cl-own"}, {Site: "site-b"}}}); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if a := e.placement(t, "acme", "site-a"); a.ClusterExtID != "cl-own" || a.StorageContainerExtID != "sc-a" {
		t.Fatalf("site-a = %+v", a)
	}
	if b := e.placement(t, "acme", "site-b"); b.ClusterExtID != "cl-b" || b.StorageContainerExtID != "sc-b" {
		t.Fatalf("site-b = %+v", b)
	}
}

func TestAddSiteLater(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	e.createOn(t, "acme", "site-a")
	before := e.pcA.Creates["CreateProject"]
	e.pcB.FailOnce["ShareIdP"] = &nutanix.APIError{Status: 500, Message: "share failed"}
	if err := e.svc.AddSite(ctx, "acme", PlacementRequest{Site: "site-b"}); err != nil {
		t.Fatalf("AddSite: %v", err)
	}
	e.svc.Wait()
	if b := e.placement(t, "acme", "site-b"); b.Status != store.PlacementFailed {
		t.Fatalf("site-b after a failure = %+v", b)
	}
	if tn := e.tenant(t, "acme"); tn.Status != store.StatusActive {
		t.Fatalf("a failed new site changed the tenant: %q", tn.Status)
	}
	if err := e.svc.Retry(ctx, "acme"); err != nil {
		t.Fatalf("Retry of the new site: %v", err)
	}
	e.svc.Wait()
	if b := e.placement(t, "acme", "site-b"); b.Status != store.PlacementActive {
		t.Fatalf("site-b after retry = %+v", b)
	}
	if e.pcA.Creates["CreateProject"] != before {
		t.Fatal("adding a site touched the other one")
	}
	if err := e.svc.AddSite(ctx, "acme", PlacementRequest{Site: "site-b"}); !errors.Is(err, ErrPlacementExists) {
		t.Fatalf("AddSite twice = %v", err)
	}
	if err := e.svc.AddSite(ctx, "acme", PlacementRequest{Site: "nope"}); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("AddSite unknown = %v", err)
	}
}

func TestRemoveSiteKeepsOtherSite(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	e.createOn(t, "acme", "site-a", "site-b")
	e.changes = nil
	if err := e.svc.RemoveSite(ctx, "acme", "site-b", true); err != nil {
		t.Fatalf("RemoveSite: %v", err)
	}
	e.svc.Wait()
	if _, err := e.st.GetPlacement(ctx, "acme", "site-b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("site-b placement after removal: %v", err)
	}
	if len(e.pcB.Memberships) != 0 || len(e.pcB.Groups) != 0 || len(e.pcB.RGs) != 0 || len(e.pcB.Projects) != 1 {
		t.Fatalf("site-b objects left: %+v %+v %+v %+v", e.pcB.Memberships, e.pcB.Groups, e.pcB.RGs, e.pcB.Projects)
	}
	if a := e.placement(t, "acme", "site-a"); a.Status != store.PlacementActive || len(e.pcA.Memberships) != 1 {
		t.Fatalf("site-a after removing site-b = %+v", a)
	}
	if tn := e.tenant(t, "acme"); tn.Status != store.StatusActive || len(e.kc.Groups) != 1 {
		t.Fatalf("tenant after removing a site = %+v", tn)
	}
	if len(e.changes) == 0 || e.changes[0] != "acme" {
		t.Fatalf("placement changes = %v", e.changes)
	}
	if err := e.svc.RemoveSite(ctx, "acme", "site-b", false); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RemoveSite twice = %v", err)
	}
}

func TestRemoveLastSiteRefused(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	if err := e.svc.RemoveSite(context.Background(), "acme", "site-a", false); !errors.Is(err, ErrLastSite) {
		t.Fatalf("RemoveSite of the last site = %v", err)
	}
}

func TestDeleteRemovesEverySite(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	e.createOn(t, "acme", "site-a", "site-b")
	if err := e.svc.Delete(ctx, "acme", true); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if _, err := e.st.GetTenant(ctx, "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row after delete: %v", err)
	}
	for name, pc := range map[string]*tenanttest.PC{"a": e.pcA, "b": e.pcB} {
		if len(pc.Memberships) != 0 || len(pc.Groups) != 0 || len(pc.RGs) != 0 || len(pc.Projects) != 1 || len(pc.IdPs[0].SharedWithProjects) != 0 {
			t.Fatalf("site %s objects left", name)
		}
	}
	if len(e.kc.Groups) != 0 {
		t.Fatal("keycloak group left")
	}
}

func TestVisibilityPerSite(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	e.createOn(t, "acme", "site-a", "site-b")
	vs, err := e.svc.Visibility(ctx, "acme")
	if err != nil || len(vs) != 2 || !vs["site-a"].OK || !vs["site-b"].OK {
		t.Fatalf("visibility = %+v %v", vs, err)
	}
	b := e.placement(t, "acme", "site-b")
	e.pcB.Policies["Leaky_acp"] = []string{b.SAMLGroupExtID}
	vs, _ = e.svc.Visibility(ctx, "acme")
	if !vs["site-a"].OK || vs["site-b"].OK || !strings.Contains(strings.Join(vs["site-b"].Findings, " "), "Leaky_acp") {
		t.Fatalf("visibility with a finding on site-b = %+v", vs)
	}
}

func TestPlacementChangeHookFires(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	if len(e.changes) == 0 || e.changes[len(e.changes)-1] != "acme" {
		t.Fatalf("changes after create = %v", e.changes)
	}
	n := len(e.changes)
	if err := e.svc.Delete(context.Background(), "acme", false); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if len(e.changes) == n {
		t.Fatal("no change reported for the deletion")
	}
}
