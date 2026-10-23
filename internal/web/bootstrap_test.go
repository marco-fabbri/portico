package web

import (
	"context"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/settings/settingstest"
)

// withBootstrapFakes points the setup options at a fake Keycloak and a
// fake Prism Central.
func withBootstrapFakes(t *testing.T, e *env) (*settingstest.KC, *settingstest.PC) {
	t.Helper()
	kc, pc := settingstest.NewKC(t), settingstest.NewPC(t)
	e.web.setup.KeycloakURL = kc.Srv.URL
	e.web.setup.Bootstrapper = settings.NewBootstrapper(nil, e.web.setup.Service)
	return kc, pc
}

func TestSetupBootstrapKeycloakFragmentFillsIdentityCard(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	withBootstrapFakes(t, e)
	e.login(t)
	e.do("POST", "/setup/save", e.form("section", "hosts", "prism_host", "prism.example.test", "id_host", "id.example.test"), true)
	rec := e.do("POST", "/setup/bootstrap/keycloak", e.form("admin_username", "kcadmin", "admin_password", "adm-pw-1", "realm", "msp"), true)
	body := rec.Body.String()
	for _, want := range []string{`id="issuer"`, `value="https://id.example.test/realms/msp"`, `hx-swap-oob`, `placeholder="unchanged"`, `name="admin_password" value=""`, "banner notice"} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("fragment lacks %q: %d %s", want, rec.Code, body)
		}
	}
	if strings.Contains(body, "adm-pw-1") || strings.Contains(body, "<html") {
		t.Fatal("password echoed or full page rendered")
	}
	got, _ := e.web.setup.Service.Get(context.Background())
	if got.Keycloak.Realm != "msp" || got.OIDC.ClientSecret == "" {
		t.Fatalf("stored = %+v", got)
	}
}

// Finish setup on the site page: a refused password is a failed line
// and stores nothing; the right one completes the site. No secret
// reaches the page.
func TestFinishSetupFromTheSitePage(t *testing.T) {
	e := newEnv(t, true)
	svc, _, _ := withSetup(t, e, true)
	_, pc, fp := withSiteFakes(t, e)
	storeSite(t, svc, settings.Site{ID: "nyc1", Code: "AZ-US-NYC1", URL: pc.Srv.URL, TLSPin: fp})
	e.login(t)
	status := e.do("GET", "/setup/sites/nyc1/status", nil, true).Body.String()
	if !strings.Contains(status, ">Finish setup</button>") || !strings.Contains(status, "no service account key is stored") {
		t.Fatalf("an incomplete site's card:\n%s", status)
	}
	rec := e.do("POST", "/setup/sites/nyc1/setup", e.form("username", "admin", "password", "wrong-pw-9"), true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "refused the credentials") || !strings.Contains(body, "is incomplete and not served") || strings.Contains(body, "wrong-pw-9") {
		t.Fatalf("a refused password: %d\n%s", rec.Code, body)
	}
	if cur, _ := svc.Get(context.Background()); func() bool { s, _ := cur.Site("nyc1"); return s.APIKey != "" }() {
		t.Fatal("a key was stored after a refusal")
	}
	rec = e.do("POST", "/setup/sites/nyc1/setup", e.form("username", "admin", "password", "pc-pw-1"), true)
	body = rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "is set up and serving") || strings.Contains(body, "pc-pw-1") || strings.Contains(body, pc.Key) {
		t.Fatalf("finish setup: %d\n%s", rec.Code, body)
	}
	cur, _ := svc.Get(context.Background())
	if s, _ := cur.Site("nyc1"); !s.Complete() {
		t.Fatalf("stored = %+v", s)
	}
}

// Finish setup before the Keycloak realm exists is refused with the
// reason, in the page's summary block.
func TestFinishSetupNeedsTheRealm(t *testing.T) {
	e := newEnv(t, true)
	svc, _, _ := withSetup(t, e, true)
	_, pc, fp := withSiteFakes(t, e)
	if err := svc.Patch(context.Background(), func(d *settings.Settings) {
		d.Keycloak.AdminClientSecret = ""
		d.Sites[0].URL, d.Sites[0].TLSPin = pc.Srv.URL, fp
	}); err != nil {
		t.Fatal(err)
	}
	e.login(t)
	rec := e.do("POST", "/setup/sites/site-a/setup", e.form("username", "admin", "password", "pc-pw-1"), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "must be set first") || strings.Contains(rec.Body.String(), "<html") || pc.Credentialed != 0 {
		t.Fatalf("setup before the realm: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/setup/bootstrap/nope", "/setup/bootstrap/saml", "/setup/sites/site-a/bootstrap/prism-central", "/setup/sites/nope/setup"} {
		if rec := e.do("POST", path, e.form(), true); rec.Code != 404 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
}

// The manual path sits under Advanced on the site page: a typed key and
// a provider name made by hand are stored; an empty name keeps the
// stored one.
func TestSiteAdvancedSavesTheManualPath(t *testing.T) {
	e := newEnv(t, true)
	svc, pc, _ := withSetup(t, e, true)
	e.login(t)
	page := e.do("GET", "/setup/sites/site-a", nil, true).Body.String()
	for _, want := range []string{"<summary>Advanced</summary>", `name="saml_idp_name" value="portico-idp"`, `name="api_key"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("site page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "/bootstrap/") || strings.Contains(e.do("GET", "/setup", nil, true).Body.String(), `id="card-saml"`) {
		t.Fatal("a bootstrap form or the single SAML card is still offered")
	}
	if rec := e.do("POST", "/setup/sites/site-a/save", e.form("code", "SITE-A", "url", pc.URL, "api_key", "key-secret-2", "saml_idp_name", "msp-idp"), true); rec.Code != 303 {
		t.Fatalf("save: %d", rec.Code)
	}
	got, _ := svc.Get(context.Background())
	if got.Sites[0].APIKey != "key-secret-2" || got.Sites[0].SAMLIdPName != "msp-idp" {
		t.Fatalf("stored = %+v", got.Sites[0])
	}
	e.do("POST", "/setup/sites/site-a/save", e.form("code", "SITE-A", "url", pc.URL, "saml_idp_name", ""), true)
	if got, _ := svc.Get(context.Background()); got.Sites[0].SAMLIdPName != "msp-idp" || got.Sites[0].APIKey != "key-secret-2" {
		t.Fatalf("an empty field changed the stored site: %+v", got.Sites[0])
	}
}

func TestSetupBootstrapRequiresCSRF(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	kc, pc, fp := withSiteFakes(t, e)
	e.login(t)
	for _, path := range []string{"/setup/bootstrap/keycloak", "/setup/sites/new/inspect", "/setup/sites/new", "/setup/sites/site-a/setup", "/setup/sites/site-a/pin"} {
		rec := e.do("POST", path, map[string][]string{"admin_username": {"kcadmin"}, "admin_password": {"adm-pw-1"}, "code": {"AZ-X"}, "username": {"admin"}, "password": {"pc-pw-1"},
			"url": {pc.Srv.URL}, "confirmed": {"on"}, "pin": {fp}, "fingerprint": {fp}}, true)
		if rec.Code != 403 {
			t.Errorf("%s without CSRF: %d", path, rec.Code)
		}
	}
	if len(kc.Realms) != 1 || len(pc.Posts) != 0 || pc.Credentialed != 0 {
		t.Fatal("a backend was called without CSRF")
	}
}

// A failed bootstrap gives the typed non-secret fields back and never
// the password.
func TestSetupBootstrapFailureKeepsTypedFields(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	withBootstrapFakes(t, e)
	e.login(t)
	rec := e.do("POST", "/setup/bootstrap/keycloak", e.form("admin_username", "kcadmin", "admin_password", "wrong-pw-9", "realm", "msp", "display_name", "ACME MSP"), true)
	body := rec.Body.String()
	for _, want := range []string{"banner error", `name="admin_username" value="kcadmin"`, `name="display_name" value="ACME MSP"`, `name="admin_password" value=""`} {
		if !strings.Contains(body, want) {
			t.Fatalf("keycloak fragment lacks %q: %d %s", want, rec.Code, body)
		}
	}
}
