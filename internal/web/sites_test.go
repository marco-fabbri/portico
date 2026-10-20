package web

import (
	"context"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
)

func TestSetupPageListsSites(t *testing.T) {
	e := newEnv(t, true)
	svc, pc, _ := withSetup(t, e, true)
	doc, _ := svc.Get(context.Background())
	doc.Sites = append(doc.Sites, settings.Site{ID: "nyc1", Code: "AZ-US-NYC1", Name: "New York", Order: 2, URL: "https://pc2.example.test:9440"})
	if err := svc.Set(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	e.st.CreateTenant(context.Background(), &store.Tenant{ID: "acme", Status: store.StatusActive})
	e.st.CreatePlacement(context.Background(), &store.Placement{TenantID: "acme", SiteID: "site-a", Status: store.PlacementActive})
	e.login(t)
	body := e.do("GET", "/setup", nil, true).Body.String()
	for _, want := range []string{`href="/setup/sites/site-a">SITE-A</a>`, `href="/setup/sites/nyc1">AZ-US-NYC1</a>`, "New York", pc.URL, "key stored", `href="/setup/sites/new"`, "Not configured yet: add at least one site"} {
		if !strings.Contains(body, want) {
			t.Fatalf("setup page lacks %q:\n%s", want, body)
		}
	}
	if i, j := strings.Index(body, "SITE-A</a>"), strings.Index(body, "AZ-US-NYC1</a>"); i > j {
		t.Fatal("sites are not in order")
	}
}

func TestSiteNewForm(t *testing.T) {
	e := newEnv(t, true)
	svc, pc, _ := withSetup(t, e, true)
	e.login(t)
	if body := e.do("GET", "/setup/sites/new", nil, true).Body.String(); !strings.Contains(body, `name="code"`) || !strings.Contains(body, `name="tls_verify" value="on" checked`) {
		t.Fatalf("new site form: %s", body)
	}
	rec := e.do("POST", "/setup/sites/new", e.form("code", "AZ-US-NYC1", "name", "New York", "order", "2", "url", pc.URL, "tls_verify", "on"), true)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/setup/sites/az-us-nyc1?notice=") {
		t.Fatalf("create: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	got, _ := svc.Get(context.Background())
	if s, ok := got.Site("az-us-nyc1"); !ok || s.Code != "AZ-US-NYC1" || s.Order != 2 || !s.TLSVerify || s.APIKey != "" {
		t.Fatalf("stored site = %+v %v", s, ok)
	}
	rec = e.do("POST", "/setup/sites/new", e.form("code", "AZ-US-NYC1", "url", pc.URL), true)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "exists") || !strings.Contains(rec.Body.String(), `value="AZ-US-NYC1"`) {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("POST", "/setup/sites/new", e.form("code", "AZ-2", "order", "x"), true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "order") {
		t.Fatalf("bad order: %d", rec.Code)
	}
	if rec := e.do("GET", "/setup/sites/nope", nil, true); rec.Code != 404 {
		t.Fatalf("unknown site page: %d", rec.Code)
	}
}

func TestSiteRemoveAndForgetFromThePage(t *testing.T) {
	e := newEnv(t, true)
	svc, pc, _ := withSetup(t, e, true)
	e.login(t)
	if rec := e.do("POST", "/setup/sites/new", e.form("code", "AZ-SECOND", "url", pc.URL), true); rec.Code != 303 {
		t.Fatalf("add a second site: %d", rec.Code)
	}
	ctx := context.Background()
	e.st.CreateTenant(ctx, &store.Tenant{ID: "acme", Status: store.StatusActive})
	e.st.CreatePlacement(ctx, &store.Placement{TenantID: "acme", SiteID: "site-a", Status: store.PlacementActive, ProjectID: "portico-acme", ProjectExtID: "p-1"})
	// The site with a tenant offers no plain removal.
	if body := e.do("GET", "/setup/sites/site-a", nil, true).Body.String(); strings.Contains(body, "/setup/sites/site-a/delete") || !strings.Contains(body, `href="/tenants/acme"`) {
		t.Fatalf("site page with a tenant: %s", body)
	}
	if rec := e.do("POST", "/setup/sites/site-a/delete", e.form(), true); rec.Code != 409 || !strings.Contains(rec.Body.String(), "has tenants: acme") {
		t.Fatalf("delete with a tenant: %d", rec.Code)
	}
	if rec := e.do("POST", "/setup/sites/site-a/forget", e.form("confirm", "site-a"), true); rec.Code != 400 {
		t.Fatalf("forget with a wrong confirmation: %d", rec.Code)
	}
	rec := e.do("POST", "/setup/sites/site-a/forget", e.form("confirm", "SITE-A"), true)
	if body := rec.Body.String(); rec.Code != 200 || !strings.Contains(body, "Site SITE-A forgotten") || !strings.Contains(body, "tenant acme: project portico-acme (p-1)") {
		t.Fatalf("forget: %d %s", rec.Code, body)
	}
	got, _ := svc.Get(ctx)
	if _, ok := got.Site("site-a"); ok {
		t.Fatal("site still stored after forget")
	}
	if ps, _ := e.st.ListPlacements(ctx, "acme"); len(ps) != 0 {
		t.Fatalf("placements left: %+v", ps)
	}
	// The second site, without tenants, is removed plainly.
	rec = e.do("POST", "/setup/sites/az-second/delete", e.form(), true)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/setup?notice=") {
		t.Fatalf("delete: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if got, _ := svc.Get(ctx); len(got.Sites) != 0 {
		t.Fatalf("sites left: %+v", got.Sites)
	}
}
