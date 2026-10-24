package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/proxy"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/settings/settingstest"
	"github.com/marco-fabbri/portico/internal/sites"
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
	if rec, out := e.do("DELETE", "/api/v1/sites/az-us-nyc1", ""); rec.Code != 200 || out["removed"] != true {
		t.Fatalf("delete: %d %v", rec.Code, out)
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

func TestSiteStatusEndpointReportsToMonitor(t *testing.T) {
	e := newSettingsEnv(t, true)
	mon, _ := withRuntime(t, e)
	// The identity provider Portico created is gone from Prism Central.
	if err := e.svc.Patch(context.Background(), func(d *settings.Settings) { d.Sites[0].SAMLIdPExtID = "idp-gone" }); err != nil {
		t.Fatal(err)
	}
	rec, out := e.do("GET", "/api/v1/sites/site-a/status", "")
	if rec.Code != 200 || out["status"] != "incomplete" || !strings.Contains(out["reason"].(string), "missing") {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	saml := out["saml"].(map[string]any)
	if saml["ok"] != false || saml["action"] != "Repair" || out["connection"].(map[string]any)["ok"] != true || out["defaults"].(map[string]any)["ok"] != true {
		t.Fatalf("parts: %s", rec.Body.String())
	}
	if d := mon.Detail("site-a"); d.Status != sites.Incomplete || d.Reason != out["reason"] {
		t.Fatalf("monitor detail = %+v", d)
	}
	if rec, _ := e.do("GET", "/api/v1/sites/nope/status", ""); rec.Code != 404 {
		t.Fatalf("unknown site: %d", rec.Code)
	}
}

func TestSiteStatusInSetupMode(t *testing.T) {
	e := newSettingsEnv(t, true)
	rec, out := e.do("GET", "/api/v1/sites/site-a/status", "")
	if rec.Code != 200 || out["connection"].(map[string]any)["ok"] != true {
		t.Fatalf("status without a runtime: %d %s", rec.Code, rec.Body.String())
	}
	if _, has := out["status"]; has {
		t.Fatalf("a status without a monitor: %v", out)
	}
}

// withSetup gives the settings env a Prism Central over https and a
// Keycloak a setup run can use; it returns the Prism Central and the
// fingerprint it presents.
func withSetup(t *testing.T, e *settingsEnv) (*settingstest.PC, string) {
	t.Helper()
	pc, kc := settingstest.NewTLSPC(t), settingstest.NewKC(t)
	pc.Version = "pc.7.6.0.6"
	kc.Realms["portico"] = map[string]any{"realm": "portico"}
	kc.Descriptor = "https://id.example.test/realms/portico"
	e.so.KeycloakURL, e.so.Bootstrapper, e.kc = kc.Srv.URL, settings.NewBootstrapper(nil, e.svc), kc
	return pc, proxy.Fingerprint(pc.Srv.Certificate().Raw)
}

// addSetUpSite adds AZ-US-NYC1 on pc in one run and returns its stored
// form.
func addSetUpSite(t *testing.T, e *settingsEnv, pc *settingstest.PC, fp string) settings.Site {
	t.Helper()
	body := `{"code":"AZ-US-NYC1","url":"` + pc.Srv.URL + `","tls_pin":"` + fp + `","admin":{"username":"admin","password":"pc-pw-1"}}`
	if rec, out := e.do("POST", "/api/v1/sites", body); rec.Code != 201 || out["complete"] != true {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	cur, _ := e.svc.Get(context.Background())
	site, _ := cur.Site("az-us-nyc1")
	if site.SAMLEntityID == "" || e.kcClient(site.SAMLEntityID) == nil || len(pc.IdPs) != 1 || site.APIKeyExtID == "" {
		t.Fatalf("the setup left %+v", site)
	}
	return site
}

func (e *settingsEnv) kcClient(clientID string) map[string]any {
	for _, c := range e.kc.Clients {
		if c["clientId"] == clientID {
			return c
		}
	}
	return nil
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestRemoveSiteWithoutCredentials(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	site := addSetUpSite(t, e, pc, fp)
	rec, out := e.do("DELETE", "/api/v1/sites/az-us-nyc1", "")
	if rec.Code != 200 || out["removed"] != true {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if e.kcClient(site.SAMLEntityID) != nil {
		t.Fatal("the Keycloak client is still there")
	}
	if len(pc.IdPs) != 1 || len(pc.DeletedKeys) != 0 {
		t.Fatalf("Prism Central was changed: %v %v", pc.IdPs, pc.DeletedKeys)
	}
	left := strs(out["left_in_prism_central"])
	if len(left) != 3 || !strings.Contains(left[0], "identity provider") || !strings.Contains(left[1], "service account") || !strings.Contains(left[2], "API key "+site.APIKeyExtID) {
		t.Fatalf("left = %q", left)
	}
	if !strings.Contains(strings.Join(strs(out["deleted"]), "|"), "Keycloak client "+site.SAMLEntityID) {
		t.Fatalf("deleted = %v", out["deleted"])
	}
	if rec, _ := e.do("GET", "/api/v1/sites/az-us-nyc1", ""); rec.Code != 404 {
		t.Fatalf("site after remove: %d", rec.Code)
	}
}

func TestRemoveSiteWithCredentials(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	site := addSetUpSite(t, e, pc, fp)
	mon := sites.NewMonitor(e.st, nil, nil)
	e.so.Runtime = &Runtime{Monitor: mon}
	if err := mon.SetMaintenance(context.Background(), "az-us-nyc1", true); err != nil {
		t.Fatal(err)
	}
	if err := mon.SetMaintenance(context.Background(), "site-a", true); err != nil {
		t.Fatal(err)
	}
	// A wrong password removes nothing.
	if rec, out := e.do("DELETE", "/api/v1/sites/az-us-nyc1", `{"username":"admin","password":"wrong"}`); rec.Code != 409 || !strings.Contains(out["error"].(string), "refused the credentials") {
		t.Fatalf("a wrong password: %d %v", rec.Code, out)
	}
	if e.kcClient(site.SAMLEntityID) == nil || len(pc.IdPs) != 1 {
		t.Fatal("a refused removal deleted something")
	}
	rec, out := e.do("DELETE", "/api/v1/sites/az-us-nyc1", `{"username":"admin","password":"pc-pw-1"}`)
	if rec.Code != 200 || out["removed"] != true {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if len(pc.IdPs) != 0 || len(pc.DeletedKeys) != 1 || pc.DeletedKeys[0] != site.APIKeyExtID || pc.User == nil {
		t.Fatalf("Prism Central after remove: idps %v keys %v user %v", pc.IdPs, pc.DeletedKeys, pc.User)
	}
	deleted, left := strs(out["deleted"]), strs(out["left_in_prism_central"])
	if len(deleted) != 3 || len(left) != 1 || !strings.Contains(left[0], "service account") {
		t.Fatalf("deleted %q left %q", deleted, left)
	}
	raw, _ := e.st.GetSetting(context.Background(), sites.StateKey)
	if strings.Contains(string(raw), "az-us-nyc1") || !strings.Contains(string(raw), "site-a") {
		t.Fatalf("site_state = %s", raw)
	}
	if strings.Contains(rec.Body.String(), "pc-pw-1") || strings.Contains(e.log.String(), "pc-pw-1") {
		t.Fatal("the password reached the answer or the log")
	}
}

// Prism Central refuses to delete an identity provider while users it
// made at sign-in through it exist (IAM-21016): the removal deletes them
// first, and names them by count, not by address (write-up 26).
func TestRemoveSiteDeletesTheProvidersSAMLUsersFirst(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	site := addSetUpSite(t, e, pc, fp)
	pc.AddSAMLUser("su-1", "carol@acme.example", site.SAMLIdPExtID)
	pc.AddSAMLUser("su-2", "probe@acme.example", site.SAMLIdPExtID)
	pc.AddSAMLUser("su-3", "other@example.test", "idp-another")
	rec, out := e.do("DELETE", "/api/v1/sites/az-us-nyc1", `{"username":"admin","password":"pc-pw-1"}`)
	if rec.Code != 200 || out["removed"] != true {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if len(pc.IdPs) != 0 || len(pc.SAMLUsers) != 1 || pc.SAMLUsers[0]["extId"] != "su-3" {
		t.Fatalf("Prism Central after remove: idps %v saml users %v", pc.IdPs, pc.SAMLUsers)
	}
	deleted := strings.Join(strs(out["deleted"]), "; ")
	if !strings.Contains(deleted, "the identity provider's 2 SAML users") || !strings.Contains(deleted, "identity provider portico-keycloak") {
		t.Fatalf("deleted %q", deleted)
	}
	if strings.Contains(rec.Body.String(), "carol@acme.example") || strings.Contains(e.log.String(), "carol@acme.example") {
		t.Fatal("a user's address in the answer or the log")
	}
}

// A deletion Prism Central refuses is left with Prism Central's own
// reason, not a connection failure.
func TestRemoveSiteLeavesTheProviderWithPrismCentralsReason(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	site := addSetUpSite(t, e, pc, fp)
	pc.AddSAMLUser("su-1", "carol@acme.example", site.SAMLIdPExtID)
	pc.UserDeleteForbidden = true
	rec, out := e.do("DELETE", "/api/v1/sites/az-us-nyc1", `{"username":"admin","password":"pc-pw-1"}`)
	if rec.Code != 200 || out["removed"] != true {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	left := strings.Join(strs(out["left_in_prism_central"]), "; ")
	if !strings.Contains(left, "SAML users (made by Prism Central at sign-in): not deleted, Prism Central answered 403") ||
		!strings.Contains(left, "identity provider portico-keycloak") || !strings.Contains(left, "Prism Central answered 400: Failed to delete SamlIdentityProvider") ||
		strings.Contains(left, "connection failed") {
		t.Fatalf("left %q", left)
	}
}

// A deletion stopped part way says how many users went, and Prism
// Central's refusal of a user is reduced to its status and code: its text
// names the user.
func TestRemoveSiteCountsTheSAMLUsersDeleted(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	site := addSetUpSite(t, e, pc, fp)
	pc.AddSAMLUser("su-1", "carol@acme.example", site.SAMLIdPExtID)
	pc.AddSAMLUser("su-2", "dave@acme.example", site.SAMLIdPExtID)
	pc.RefuseUserDelete = "su-2"
	rec, out := e.do("DELETE", "/api/v1/sites/az-us-nyc1", `{"username":"admin","password":"pc-pw-1"}`)
	if rec.Code != 200 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	deleted := strings.Join(strs(out["deleted"]), "; ")
	left := strings.Join(strs(out["left_in_prism_central"]), "; ")
	if !strings.Contains(deleted, "1 of the identity provider's 2 SAML users") ||
		!strings.Contains(left, "1 of the identity provider's 2 SAML users (made by Prism Central at sign-in): not deleted, Prism Central answered 403 (IAM-30403)") {
		t.Fatalf("deleted %q, left %q", deleted, left)
	}
	if strings.Contains(rec.Body.String(), "dave@acme.example") || strings.Contains(e.log.String(), "dave@acme.example") {
		t.Fatal("a user's address in the answer or the log")
	}
}

func TestRemoveSiteRefusedWithTenant(t *testing.T) {
	e := newSettingsEnv(t, true)
	placeTenant(t, e.st, "acme", "site-a")
	p, _ := e.st.GetPlacement(context.Background(), "acme", "site-a")
	p.Status = store.PlacementSuspended
	if err := e.st.UpdatePlacement(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if rec, out := e.do("DELETE", "/api/v1/sites/site-a", ""); rec.Code != 409 || !strings.Contains(out["error"].(string), "acme (suspended)") {
		t.Fatalf("remove with a suspended connection: %d %v", rec.Code, out)
	}
	e.busy = 1
	if rec, _ := e.do("POST", "/api/v1/sites", `{"code":"AZ-US-NYC1","url":"https://pc2.example.test:9440","tls_pin":"AB:CD"}`); rec.Code != 201 {
		t.Fatal(rec.Code)
	}
	if rec, _ := e.do("DELETE", "/api/v1/sites/az-us-nyc1", ""); rec.Code != 409 {
		t.Fatalf("remove while a tenant operation runs: %d", rec.Code)
	}
	if rec, _ := e.do("GET", "/api/v1/sites/site-a", ""); rec.Code != 200 {
		t.Fatalf("site-a after the refusals: %d", rec.Code)
	}
}

func TestForgetDeletesKeycloakClient(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	site := addSetUpSite(t, e, pc, fp)
	if rec, out := e.do("POST", "/api/v1/sites/az-us-nyc1/forget", `{"confirm":"AZ-US-NYC1"}`); rec.Code != 200 || out["forgotten"] != "AZ-US-NYC1" {
		t.Fatalf("forget: %d %v", rec.Code, out)
	}
	if e.kcClient(site.SAMLEntityID) != nil {
		t.Fatal("the Keycloak client is still there")
	}
	if len(pc.IdPs) != 1 {
		t.Fatal("forget reached Prism Central")
	}
}

func TestCreateSiteRefusesVerifyOff(t *testing.T) {
	e := newSettingsEnv(t, true)
	rec, out := e.do("POST", "/api/v1/sites", `{"code":"AZ-US-NYC1","url":"https://pc2.example.test:9440","tls_verify":false}`)
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "a new site needs a trusted or pinned certificate") {
		t.Fatalf("verify off without a pin: %d %v", rec.Code, out)
	}
	if rec, _ := e.do("POST", "/api/v1/sites", `{"code":"AZ-US-NYC1","url":"https://pc2.example.test:9440","tls_verify":false,"tls_pin":"AB:CD"}`); rec.Code != 201 {
		t.Fatalf("verify off with a pin: %d", rec.Code)
	}
}

func TestInspectEndpointShowsCertificate(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	rec, out := e.do("POST", "/api/v1/sites/inspect", `{"url":"`+pc.Srv.URL+`"}`)
	if rec.Code != 200 || out["fingerprint"] != fp || out["trusted"] != false || pc.Credentialed != 0 {
		t.Fatalf("inspect: %d %v", rec.Code, out)
	}
	if rec, _ := e.do("POST", "/api/v1/sites/inspect", `{"url":"http://pc.example.test"}`); rec.Code != 400 {
		t.Fatalf("an http URL: %d", rec.Code)
	}
}

func TestCreateSiteWithAdminRunsSetup(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	body := `{"code":"AZ-US-NYC1","name":"New York","url":"` + pc.Srv.URL + `","tls_pin":"` + fp + `","admin":{"username":"admin","password":"pc-pw-1"}}`
	rec, out := e.do("POST", "/api/v1/sites", body)
	if rec.Code != 201 || out["complete"] != true || len(out["steps"].([]any)) != 8 || out["site"].(map[string]any)["id"] != "az-us-nyc1" || out["site"].(map[string]any)["tls_pin"] != fp {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pc-pw-1") || strings.Contains(rec.Body.String(), pc.Key) || strings.Contains(e.log.String(), "pc-pw-1") {
		t.Fatal("a secret in the answer or the log")
	}
	// Finish setup on a complete site changes nothing.
	rec, out = e.do("POST", "/api/v1/sites/az-us-nyc1/setup", `{"username":"admin","password":"pc-pw-1"}`)
	if rec.Code != 200 || out["complete"] != true {
		t.Fatalf("setup again: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := e.do("POST", "/api/v1/sites/nope/setup", `{"username":"admin","password":"pc-pw-1"}`); rec.Code != 404 {
		t.Fatalf("unknown site: %d", rec.Code)
	}
}

// A complete run clears what the monitor holds against the site at once:
// otherwise its tenants stay out until the next trust check, up to five
// minutes after Repair said the site is served (write-up 26).
func TestCompleteSetupClearsIncompleteAtOnce(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	mon, _ := withRuntime(t, e)
	addSetUpSite(t, e, pc, fp)
	mon.SetProbes(map[string]sites.Prober{"az-us-nyc1": func(context.Context) error { return nil }})
	mon.Report("az-us-nyc1", &sites.IncompleteError{Reason: "the identity provider portico-keycloak is missing in Prism Central", Immediate: true})
	if mon.Status("az-us-nyc1") != sites.Incomplete {
		t.Fatal("the monitor did not take the missing provider")
	}
	rec, out := e.do("POST", "/api/v1/sites/az-us-nyc1/setup", `{"username":"admin","password":"pc-pw-1"}`)
	if rec.Code != 200 || out["complete"] != true {
		t.Fatalf("repair: %d %s", rec.Code, rec.Body.String())
	}
	if st := mon.Status("az-us-nyc1"); st == sites.Incomplete {
		t.Fatalf("status after a complete repair = %v", st)
	}
}

func TestPinEndpointRefusesChangedFingerprint(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	if err := e.svc.Patch(context.Background(), func(d *settings.Settings) { d.Sites[0].URL, d.Sites[0].TLSPin = pc.Srv.URL, "AB:CD" }); err != nil {
		t.Fatal(err)
	}
	rec, out := e.do("POST", "/api/v1/sites/site-a/pin", `{"fingerprint":"AB:CD"}`)
	if rec.Code != 409 || out["fingerprint"] != fp {
		t.Fatalf("an old fingerprint: %d %v", rec.Code, out)
	}
	if s, _ := e.svc.Get(context.Background()); s.Sites[0].TLSPin != "AB:CD" {
		t.Fatalf("the pin changed: %q", s.Sites[0].TLSPin)
	}
	if rec, out := e.do("POST", "/api/v1/sites/site-a/pin", `{"fingerprint":"`+strings.ToLower(fp)+`"}`); rec.Code != 200 || out["tls_pin"] != fp {
		t.Fatalf("the presented fingerprint: %d %v", rec.Code, out)
	}
}

func TestUpdateRefusesTurningVerifyOff(t *testing.T) {
	e := newSettingsEnv(t, true)
	if err := e.svc.Patch(context.Background(), func(d *settings.Settings) {
		d.Sites[0].URL, d.Sites[0].TLSVerify = "https://pc.example.test:9440", true
	}); err != nil {
		t.Fatal(err)
	}
	rec, out := e.do("PUT", "/api/v1/sites/site-a", `{"code":"SITE-A","name":"pc","url":"https://pc.example.test:9440","tls_verify":false}`)
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "pin") {
		t.Fatalf("verify off: %d %v", rec.Code, out)
	}
	if s, _ := e.svc.Get(context.Background()); !s.Sites[0].TLSVerify {
		t.Fatal("verification was turned off")
	}
}

func TestSetupEndpointRepairsWhenIdPChanged(t *testing.T) {
	e := newSettingsEnv(t, true)
	pc, fp := withSetup(t, e)
	if rec, out := e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME","sites":[{"site":"site-a"}]}`); rec.Code != 202 {
		t.Fatalf("create acme: %d %v", rec.Code, out)
	}
	e.tenants.Wait()
	// site-a's trust is gone from its Prism Central: the setup creates it
	// again, with a new id.
	if err := e.svc.Patch(context.Background(), func(d *settings.Settings) {
		d.Sites[0].URL, d.Sites[0].TLSPin, d.Sites[0].SAMLIdPExtID = pc.Srv.URL, fp, "idp-gone"
	}); err != nil {
		t.Fatal(err)
	}
	rec, out := e.do("POST", "/api/v1/sites/site-a/setup", `{"username":"admin","password":"pc-pw-1"}`)
	if rec.Code != 200 || out["idp_changed"] != true {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	var line map[string]any
	for _, s := range out["steps"].([]any) {
		if m := s.(map[string]any); m["name"] == "tenant acme" {
			line = m
		}
	}
	if line == nil || line["status"] != "ok" {
		t.Fatalf("no repair line for acme: %s", rec.Body.String())
	}
}
