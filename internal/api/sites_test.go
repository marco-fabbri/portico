package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/store"
)

func TestSitesAPICRUD(t *testing.T) {
	e := newSettingsEnv(t, true)
	rec, out := e.do("POST", "/api/v1/sites", `{"code":"AZ-US-NYC1","name":"New York","order":2,"url":"https://pc2.example.test:9440","api_key":"nyc-key-1"}`)
	if rec.Code != 201 || out["id"] != "az-us-nyc1" || out["api_key_set"] != true || out["tls_verify"] != true || strings.Contains(rec.Body.String(), "nyc-key-1") {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec, out := e.do("POST", "/api/v1/sites", `{"code":"AZ-US-NYC1","url":"https://x:9440"}`); rec.Code != 409 || !strings.Contains(out["error"].(string), "exists") {
		t.Fatalf("duplicate code: %d %v", rec.Code, out)
	}
	if rec, out := e.do("POST", "/api/v1/sites", `{"code":"bad code"}`); rec.Code != 400 || !strings.Contains(out["error"].(string), "code") {
		t.Fatalf("bad code: %d %v", rec.Code, out)
	}
	rec, out = e.do("GET", "/api/v1/sites", "")
	sites := out["sites"].([]any)
	if rec.Code != 200 || len(sites) != 2 || sites[0].(map[string]any)["id"] != "site-a" || sites[1].(map[string]any)["code"] != "AZ-US-NYC1" {
		t.Fatalf("list: %d %v", rec.Code, out)
	}
	// A rename keeps the id and the key.
	rec, out = e.do("PUT", "/api/v1/sites/az-us-nyc1", `{"code":"AZ-US-NYC2","name":"New York 2","order":2,"url":"https://pc2.example.test:9440"}`)
	if rec.Code != 200 || out["id"] != "az-us-nyc1" || out["code"] != "AZ-US-NYC2" || out["api_key_set"] != true {
		t.Fatalf("update: %d %v", rec.Code, out)
	}
	got, _ := e.svc.Get(context.Background())
	if s, _ := got.Site("az-us-nyc1"); s.APIKey != "nyc-key-1" {
		t.Fatalf("the key was lost by the update: %+v", s)
	}
	if rec, _ := e.do("PUT", "/api/v1/sites/nope", `{"code":"X1"}`); rec.Code != 404 {
		t.Fatalf("update unknown: %d", rec.Code)
	}
	if rec, _ := e.do("DELETE", "/api/v1/sites/az-us-nyc1", ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec, _ := e.do("GET", "/api/v1/sites/az-us-nyc1", ""); rec.Code != 404 {
		t.Fatalf("get after delete: %d", rec.Code)
	}
	if strings.Contains(e.log.String(), "nyc-key-1") {
		t.Fatal("the key reached the log")
	}
}

func placeTenant(t *testing.T, st store.Store, tenant, site string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.GetTenant(ctx, tenant); err != nil {
		if err := st.CreateTenant(ctx, &store.Tenant{ID: tenant, Status: store.StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	p := &store.Placement{TenantID: tenant, SiteID: site, Status: store.PlacementActive, ProjectID: "portico-" + tenant, ProjectExtID: "p-" + tenant,
		SAMLGroupExtID: "g-" + tenant, RoleMembershipExtID: "rm-" + tenant, IdPShared: true}
	if err := st.CreatePlacement(ctx, p); err != nil {
		t.Fatal(err)
	}
}

func TestSitesAPIDeleteRefusedWithTenants(t *testing.T) {
	e := newSettingsEnv(t, true)
	placeTenant(t, e.st, "acme", "site-a")
	rec, out := e.do("DELETE", "/api/v1/sites/site-a", "")
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "site SITE-A has tenants: acme") {
		t.Fatalf("delete with tenants: %d %v", rec.Code, out)
	}
	_, out = e.do("GET", "/api/v1/sites/site-a", "")
	if tenants := out["tenants"].([]any); len(tenants) != 1 || tenants[0] != "acme" {
		t.Fatalf("site tenants: %v", out)
	}
}

func TestSitesAPIForget(t *testing.T) {
	e := newSettingsEnv(t, true)
	var seen []string
	gone := httptest.NewServer(fakePC("key-secret-1", &seen))
	defer gone.Close()
	if rec, _ := e.do("POST", "/api/v1/sites", `{"code":"AZ-GONE","url":"`+gone.URL+`","api_key":"gone-key"}`); rec.Code != 201 {
		t.Fatal(rec.Code)
	}
	placeTenant(t, e.st, "acme", "az-gone")
	placeTenant(t, e.st, "acme", "site-a")
	if rec, out := e.do("POST", "/api/v1/sites/az-gone/forget", `{"confirm":"az-gone"}`); rec.Code != 400 || !strings.Contains(out["error"].(string), "code") {
		t.Fatalf("wrong confirmation: %d %v", rec.Code, out)
	}
	e.busy = 1
	if rec, _ := e.do("POST", "/api/v1/sites/az-gone/forget", `{"confirm":"AZ-GONE"}`); rec.Code != 409 {
		t.Fatalf("forget while busy: %d", rec.Code)
	}
	e.busy = 0
	rec, out := e.do("POST", "/api/v1/sites/az-gone/forget", `{"confirm":"AZ-GONE"}`)
	if rec.Code != 200 || out["forgotten"] != "AZ-GONE" {
		t.Fatalf("forget: %d %v", rec.Code, out)
	}
	left := strings.Join(func() []string {
		var l []string
		for _, x := range out["left_in_prism_central"].([]any) {
			l = append(l, x.(string))
		}
		return l
	}(), "|")
	for _, want := range []string{"tenant acme: project portico-acme (p-acme)", "tenant acme: SAML user group g-acme", "tenant acme: role membership rm-acme", "Portico's service account"} {
		if !strings.Contains(left, want) {
			t.Fatalf("left = %q, want %q", left, want)
		}
	}
	if len(seen) != 0 {
		t.Fatalf("the forgotten Prism Central was called %d time(s)", len(seen))
	}
	ps, _ := e.st.ListPlacements(context.Background(), "acme")
	if len(ps) != 1 || ps[0].SiteID != "site-a" {
		t.Fatalf("placements after forget: %+v", ps)
	}
	if rec, _ := e.do("GET", "/api/v1/sites/az-gone", ""); rec.Code != 404 {
		t.Fatalf("site after forget: %d", rec.Code)
	}
	if !strings.Contains(e.log.String(), "site forgotten") || strings.Contains(e.log.String(), "gone-key") {
		t.Fatalf("log: %s", e.log.String())
	}
}
