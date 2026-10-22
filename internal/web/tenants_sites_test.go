package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
	"github.com/marco-fabbri/portico/internal/tenant/tenanttest"
)

// withTwoSites gives the env a tenant service over two sites.
func withTwoSites(t *testing.T, e *env) *tenanttest.PC {
	t.Helper()
	pcB := tenanttest.NewPC("portico-idp")
	sites := []tenant.Site{
		{ID: "site-a", Code: "AZ-IT-MI1", PC: e.pc, SAMLIdPName: "portico-idp", ClusterExtID: "cl-a", StorageContainerExtID: "sc-a"},
		{ID: "site-b", Code: "AZ-US-NYC1", PC: pcB, SAMLIdPName: "portico-idp", ClusterExtID: "cl-b", StorageContainerExtID: "sc-b"},
	}
	e.svc = tenant.NewService(e.st, sites, e.kc, tenant.Config{OperationTimeout: 5 * time.Second}, nil)
	e.web.svc = e.svc
	return pcB
}

func TestNewTenantFormHasOneRowPerSite(t *testing.T) {
	e := newEnv(t, true)
	withTwoSites(t, e)
	e.login(t)
	body := e.do("GET", "/tenants/new", nil, true).Body.String()
	for _, want := range []string{`name="site" value="site-a"`, `name="site" value="site-b"`, "AZ-IT-MI1", "AZ-US-NYC1", `name="cluster_site-a" value="cl-a"`, `name="container_site-b" value="sc-b"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("new tenant form lacks %q:\n%s", want, body)
		}
	}
	rec := e.do("POST", "/tenants", e.form("slug", "acme", "display_name", "ACME", "site", "site-b", "cluster_site-b", "cl-own", "container_site-b", ""), true)
	if rec.Code != 303 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	e.svc.Wait()
	ps, _ := e.st.ListPlacements(context.Background(), "acme")
	if len(ps) != 1 || ps[0].SiteID != "site-b" || ps[0].ClusterExtID != "cl-own" || ps[0].StorageContainerExtID != "sc-b" || ps[0].Status != store.PlacementActive {
		t.Fatalf("placements = %+v", ps)
	}
	rec = e.do("POST", "/tenants", e.form("slug", "beta", "display_name", "B"), true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "sites") {
		t.Fatalf("create without a site: %d", rec.Code)
	}
}

func TestTenantDetailPerSiteAddAndRemove(t *testing.T) {
	e := newEnv(t, true)
	pcB := withTwoSites(t, e)
	e.login(t)
	e.do("POST", "/tenants", e.form("slug", "acme", "display_name", "ACME", "site", "site-a"), true)
	e.svc.Wait()
	body := e.do("GET", "/tenants/acme", nil, true).Body.String()
	for _, want := range []string{"AZ-IT-MI1", `action="/tenants/acme/sites"`, `<option value="site-b">AZ-US-NYC1`, `class="ok"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `action="/tenants/acme/sites/site-a/remove"`) {
		t.Fatal("the last site offers a removal")
	}
	if rec := e.do("POST", "/tenants/acme/sites", e.form("site", "site-b"), true); rec.Code != 303 {
		t.Fatalf("add site: %d %s", rec.Code, rec.Body.String())
	}
	e.svc.Wait()
	if len(pcB.Memberships) != 1 {
		t.Fatal("site-b not provisioned")
	}
	body = e.do("GET", "/tenants/acme", nil, true).Body.String()
	if !strings.Contains(body, `action="/tenants/acme/sites/site-b/remove"`) || !strings.Contains(body, "AZ-US-NYC1") {
		t.Fatalf("detail with two sites:\n%s", body)
	}
	if rec := e.do("POST", "/tenants/acme/sites/site-b/remove", e.form("delete_project", "on"), true); rec.Code != 303 {
		t.Fatalf("remove site: %d %s", rec.Code, rec.Body.String())
	}
	e.svc.Wait()
	if len(pcB.Memberships) != 0 || len(pcB.Projects) != 1 {
		t.Fatal("site-b objects left")
	}
	for _, path := range []string{"/tenants/acme/sites", "/tenants/acme/sites/site-a/remove"} {
		if rec := e.do("POST", path, map[string][]string{"site": {"site-b"}}, true); rec.Code != 403 {
			t.Fatalf("%s without CSRF: %d", path, rec.Code)
		}
	}
	// The last site can be disconnected too (design sites usability 4.6).
	if rec := e.do("POST", "/tenants/acme/sites/site-a/remove", e.form(), true); rec.Code != 303 {
		t.Fatalf("disconnect the last site: %d", rec.Code)
	}
}
