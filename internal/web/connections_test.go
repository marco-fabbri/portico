package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant/tenanttest"
)

// createOnBoth creates acme on both sites of withTwoSites.
func (e *env) createOnBoth(t *testing.T) {
	t.Helper()
	rec := e.do("POST", "/tenants", e.form("slug", "acme", "display_name", "ACME"), true)
	if rec.Code != 303 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	e.svc.Wait()
	for _, site := range []string{"site-a", "site-b"} {
		if _, err := e.st.GetPlacement(context.Background(), "acme", site); err != nil {
			f := e.form("site", site)
			if rec := e.do("POST", "/tenants/acme/sites", f, true); rec.Code != 303 {
				t.Fatalf("connect %s: %d %s", site, rec.Code, rec.Body.String())
			}
			e.svc.Wait()
		}
	}
}

// withSessions gives the UI a runtime holding the given tenant sessions.
func withSessions(e *env, ss ...auth.Session) {
	store := auth.NewMemoryStore(8*time.Hour, 15*time.Minute)
	now := time.Now()
	for _, s := range ss {
		s.CreatedAt, s.LastSeen = now, now
		store.Create(s)
	}
	if e.web.setup == nil {
		e.web.setup = &api.SettingsOptions{}
	}
	e.web.setup.Runtime = &api.Runtime{Store: store}
}

func TestTenantPageListsConnections(t *testing.T) {
	e := newEnv(t, true)
	withTwoSites(t, e)
	e.login(t)
	e.createOnBoth(t)
	body := e.do("GET", "/tenants/acme", nil, true).Body.String()
	for _, want := range []string{"AZ-IT-MI1", "AZ-US-NYC1", `href="/tenants/acme/sites/site-a/suspend"`, `href="/tenants/acme/sites/site-b/disconnect"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("tenant page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "delete the project too") || strings.Contains(body, "/remove") {
		t.Fatalf("tenant page still offers the old removal:\n%s", body)
	}
}

func TestSuspendConfirmCountsUsers(t *testing.T) {
	e := newEnv(t, true)
	withTwoSites(t, e)
	e.login(t)
	e.createOnBoth(t)
	withSessions(e,
		auth.Session{ID: "s1", Tenant: "acme", Site: "site-a"},
		auth.Session{ID: "s2", Tenant: "acme", Site: "site-a"},
		auth.Session{ID: "s3", Tenant: "beta", Site: "site-a"},
		auth.Session{ID: "s4", Tenant: "acme", Site: "site-b"})
	rec := e.do("GET", "/tenants/acme/sites/site-a/suspend", nil, true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Suspend ACME on AZ-IT-MI1?") || !strings.Contains(body, "2 of its users are on this site now") ||
		!strings.Contains(body, "VMs keep running") || !strings.Contains(body, `action="/tenants/acme/sites/site-a/suspend"`) {
		t.Fatalf("suspend confirmation: %d\n%s", rec.Code, body)
	}
}

func TestSuspendPostSuspends(t *testing.T) {
	e := newEnv(t, true)
	withTwoSites(t, e)
	e.login(t)
	e.createOnBoth(t)
	if rec := e.do("POST", "/tenants/acme/sites/site-a/suspend", map[string][]string{}, true); rec.Code != 403 {
		t.Fatalf("suspend without CSRF: %d", rec.Code)
	}
	rec := e.do("POST", "/tenants/acme/sites/site-a/suspend", e.form(), true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/tenants/acme" {
		t.Fatalf("suspend: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if p, _ := e.st.GetPlacement(context.Background(), "acme", "site-a"); p.Status != store.PlacementSuspended {
		t.Fatalf("site-a = %s", p.Status)
	}
	body := e.do("GET", "/tenants/acme", nil, true).Body.String()
	if !strings.Contains(body, `class="status status-suspended"`) || !strings.Contains(body, `action="/tenants/acme/sites/site-a/resume"`) {
		t.Fatalf("tenant page after suspend:\n%s", body)
	}
	if rec := e.do("POST", "/tenants/acme/sites/site-a/resume", e.form(), true); rec.Code != 303 {
		t.Fatalf("resume: %d", rec.Code)
	}
	if p, _ := e.st.GetPlacement(context.Background(), "acme", "site-a"); p.Status != store.PlacementActive {
		t.Fatalf("site-a after resume = %s", p.Status)
	}
}

func TestDisconnectConfirmShowsRefusal(t *testing.T) {
	e := newEnv(t, true)
	withTwoSites(t, e)
	e.login(t)
	e.createOnBoth(t)
	p, _ := e.st.GetPlacement(context.Background(), "acme", "site-a")
	e.pc.Contents[p.ProjectExtID] = nutanix.Contents{Counts: map[string]int{"vms": 2}}
	rec := e.do("GET", "/tenants/acme/sites/site-a/disconnect", nil, true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "VMs: 2") || !strings.Contains(body, `href="https://pc-a.test:9440"`) || strings.Contains(body, `action="/tenants/acme/sites/site-a/disconnect"`) {
		t.Fatalf("disconnect refusal: %d\n%s", rec.Code, body)
	}
	body = e.do("GET", "/tenants/acme/sites/site-b/disconnect", nil, true).Body.String()
	if !strings.Contains(body, "Disconnect ACME from AZ-US-NYC1?") || !strings.Contains(body, "portico-acme") || !strings.Contains(body, `action="/tenants/acme/sites/site-b/disconnect"`) {
		t.Fatalf("disconnect confirmation:\n%s", body)
	}
}

func TestDisconnectPostEmpty(t *testing.T) {
	e := newEnv(t, true)
	withTwoSites(t, e)
	e.login(t)
	e.createOnBoth(t)
	e.pc.GateDelete = tenanttest.NewGate()
	rec := e.do("POST", "/tenants/acme/sites/site-a/disconnect", e.form(), true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/tenants/acme" {
		t.Fatalf("disconnect: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if p, _ := e.st.GetPlacement(context.Background(), "acme", "site-a"); p.Status != store.PlacementRemoving {
		t.Fatalf("site-a = %s", p.Status)
	}
	close(e.pc.GateDelete.Release)
	e.svc.Wait()
}

func TestConnectFromSitePage(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	withTwoSites(t, e)
	e.login(t)
	if rec := e.do("POST", "/tenants", e.form("slug", "acme", "display_name", "ACME", "site", "site-b"), true); rec.Code != 303 {
		t.Fatalf("create: %d", rec.Code)
	}
	e.svc.Wait()
	body := e.do("GET", "/setup/sites/site-a", nil, true).Body.String()
	if !strings.Contains(body, "Connect a tenant to this site") || !strings.Contains(body, `<option value="acme">`) || !strings.Contains(body, `hx-get="/targets?form=connect&amp;site=site-a"`) {
		t.Fatalf("site page:\n%s", body)
	}
	rec := e.do("POST", "/setup/sites/site-a/tenants", e.form("tenant", "acme", "cluster_ext_id", "cl-own", "storage_container_ext_id", "sc-own"), true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/setup/sites/site-a" {
		t.Fatalf("connect: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	e.svc.Wait()
	body = e.do("GET", "/setup/sites/site-a", nil, true).Body.String()
	if !strings.Contains(body, "Tenants on this site") || !strings.Contains(body, `href="/tenants/acme"`) {
		t.Fatalf("site page after connect:\n%s", body)
	}
	p, err := e.st.GetPlacement(context.Background(), "acme", "site-a")
	if err != nil || p.ClusterExtID != "cl-own" || p.StorageContainerExtID != "sc-own" {
		t.Fatalf("site-a = %+v %v", p, err)
	}
}

func TestTargetsFragment(t *testing.T) {
	e := newEnv(t, true)
	withTwoSites(t, e)
	e.login(t)
	e.pc.Clusters = []nutanix.Cluster{{ExtID: "cl-a", Name: "NTNX-01", IsAOS: true}}
	e.pc.Containers = []nutanix.StorageContainer{{ExtID: "sc-a", Name: "default-container", ClusterExtID: "cl-a"}}
	body := e.do("GET", "/targets?site=site-a&form=connect", nil, true).Body.String()
	if strings.Contains(body, "<html") || !strings.Contains(body, `name="cluster_ext_id"`) || !strings.Contains(body, `name="storage_container_ext_id"`) || !strings.Contains(body, "(site default)") || !strings.Contains(body, "NTNX-01") {
		t.Fatalf("connect fragment:\n%s", body)
	}
	body = e.do("GET", "/targets?site=site-a&form=create", nil, true).Body.String()
	if !strings.Contains(body, `name="cluster_site-a"`) || !strings.Contains(body, `name="container_site-a"`) {
		t.Fatalf("create fragment:\n%s", body)
	}
	e.pc.FailOnce["ListClusters"] = context.DeadlineExceeded
	body = e.do("GET", "/targets?site=site-a&form=connect", nil, true).Body.String()
	if !strings.Contains(body, "Prism Central did not answer: the site's defaults are offered") || !strings.Contains(body, `value="cl-a"`) {
		t.Fatalf("fragment on error:\n%s", body)
	}
	if rec := e.do("GET", "/targets?site=nope&form=connect", nil, true); rec.Code != 404 {
		t.Fatalf("unknown site: %d", rec.Code)
	}
}

func TestDeleteHiddenWithConnections(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.createTenant(t, "acme")
	body := e.do("GET", "/tenants/acme", nil, true).Body.String()
	if strings.Contains(body, `href="/tenants/acme/delete"`) || !strings.Contains(body, "Disconnect it from every site to delete it") {
		t.Fatalf("tenant page with a connection:\n%s", body)
	}
	if err := e.svc.Disconnect(context.Background(), "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	body = e.do("GET", "/tenants/acme", nil, true).Body.String()
	if !strings.Contains(body, `href="/tenants/acme/delete"`) {
		t.Fatalf("tenant page without connections:\n%s", body)
	}
	if _, err := e.svc.CreateUser(context.Background(), "acme", "carol@acme.test", "C", "L"); err != nil {
		t.Fatal(err)
	}
	body = e.do("GET", "/tenants/acme/delete", nil, true).Body.String()
	if !strings.Contains(body, "Delete ACME?") || !strings.Contains(body, "its 1 user") || !strings.Contains(body, `action="/tenants/acme/delete"`) {
		t.Fatalf("delete confirmation:\n%s", body)
	}
}

func TestCreateTenantNoSite(t *testing.T) {
	e := newEnv(t, true)
	withTwoSites(t, e)
	e.login(t)
	rec := e.do("POST", "/tenants", e.form("slug", "bare", "display_name", "Bare"), true)
	if rec.Code != 303 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	e.svc.Wait()
	if ps, _ := e.st.ListPlacements(context.Background(), "bare"); len(ps) != 0 {
		t.Fatalf("placements = %+v", ps)
	}
	body := e.do("GET", "/tenants/bare", nil, true).Body.String()
	if !strings.Contains(body, "Connect this tenant to a site") || !strings.Contains(body, `hx-get="/targets?form=connect"`) {
		t.Fatalf("tenant page without sites:\n%s", body)
	}
}
