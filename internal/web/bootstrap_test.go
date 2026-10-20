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

func TestSetupBootstrapPrismCentralFragmentAndRefusal(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	_, pc := withBootstrapFakes(t, e)
	e.login(t)
	addSite(t, e, pc.Srv.URL)
	rec := e.do("POST", "/setup/sites/site-a/bootstrap/prism-central", e.form("username", "admin", "password", "wrong", "url", pc.Srv.URL, "service_account", "portico"), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "banner error") || !strings.Contains(rec.Body.String(), "refused the credentials") {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body.String())
	}
	if page := e.do("GET", "/setup/sites/site-a", nil, true).Body.String(); strings.Contains(page, `placeholder="unchanged"`) {
		t.Fatal("a key was stored after a refusal")
	}
	rec = e.do("POST", "/setup/sites/site-a/bootstrap/prism-central", e.form("username", "admin", "password", "pc-pw-1", "url", pc.Srv.URL, "service_account", "portico"), true)
	body := rec.Body.String()
	for _, want := range []string{"banner notice", `id="api_key"`, `placeholder="unchanged"`, `id="cluster_ext_id"`, "NTNX-01", `name="password" value=""`} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("fragment lacks %q: %d %s", want, rec.Code, body)
		}
	}
	if strings.Contains(body, pc.Key) || strings.Contains(body, "pc-pw-1") {
		t.Fatal("the key or the password reached the page")
	}
	got, _ := e.web.setup.Service.Get(context.Background())
	if got.Sites[0].APIKey != settings.Secret(pc.Key) || got.Sites[0].URL != pc.Srv.URL {
		t.Fatalf("stored = %+v", got)
	}
}

func TestSetupBootstrapSAMLNeedsPreconditions(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	_, pc := withBootstrapFakes(t, e)
	e.login(t)
	addSite(t, e, pc.Srv.URL)
	rec := e.do("POST", "/setup/sites/site-a/bootstrap/saml", e.form("saml_idp_name", "portico-keycloak"), true)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "must be set first") || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("saml on a fresh store: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/setup/bootstrap/nope", "/setup/bootstrap/saml", "/setup/sites/site-a/bootstrap/nope", "/setup/sites/nope/bootstrap/saml"} {
		if rec := e.do("POST", path, e.form(), true); rec.Code != 404 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
}

func TestSetupBootstrapSAMLFragmentStoresTheName(t *testing.T) {
	e := newEnv(t, true)
	svc, _, _ := withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	kc, pc := withBootstrapFakes(t, e)
	kc.Realms["msp"] = map[string]any{"realm": "msp"}
	doc := setupSample(pc.Srv.URL)
	doc.Keycloak.Realm, doc.OIDC.Issuer, doc.Sites[0].APIKey, doc.Sites[0].SAMLIdPName = "msp", "https://id.example.test/realms/msp", settings.Secret(pc.Key), ""
	if err := svc.Set(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	e.login(t)
	rec := e.do("POST", "/setup/sites/site-a/bootstrap/saml", e.form("saml_idp_name", "msp-idp"), true)
	body := rec.Body.String()
	for _, want := range []string{"banner notice", `id="saml_idp_name"`, `value="msp-idp"`, `hx-swap-oob`} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("fragment lacks %q: %d %s", want, rec.Code, body)
		}
	}
	got, _ := svc.Get(context.Background())
	if got.Sites[0].SAMLIdPName != "msp-idp" || len(got.Missing()) != 0 {
		t.Fatalf("stored = %+v missing=%v", got, got.Missing())
	}
}

// The site page shows its SAML trust; the name is set by the bootstrap.
func TestSitePageShowsTheSAMLTrust(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	e.login(t)
	page := e.do("GET", "/setup/sites/site-a", nil, true).Body.String()
	for _, want := range []string{"SAML trust", `id="form-site_saml"`, `value="portico-idp"`, "trusts the identity provider", `hx-post="/setup/sites/site-a/bootstrap/saml"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("site page lacks %q: %s", want, page)
		}
	}
	if strings.Contains(e.do("GET", "/setup", nil, true).Body.String(), `id="card-saml"`) {
		t.Fatal("the setup page still has a single SAML card")
	}
}

func TestSetupBootstrapRequiresCSRF(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	kc, pc := withBootstrapFakes(t, e)
	e.login(t)
	for _, path := range []string{"/setup/bootstrap/keycloak", "/setup/sites/site-a/bootstrap/prism-central", "/setup/sites/site-a/bootstrap/saml"} {
		rec := e.do("POST", path, map[string][]string{"admin_username": {"kcadmin"}, "admin_password": {"adm-pw-1"}, "username": {"admin"}, "password": {"pc-pw-1"}, "url": {pc.Srv.URL}}, true)
		if rec.Code != 403 {
			t.Errorf("%s without CSRF: %d", path, rec.Code)
		}
	}
	if len(kc.Realms) != 0 || len(pc.Posts) != 0 {
		t.Fatal("a backend was called without CSRF")
	}
}

// A failed bootstrap gives the typed non-secret fields back and never
// the password.
func TestSetupBootstrapFailureKeepsTypedFields(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	_, pc := withBootstrapFakes(t, e)
	e.login(t)
	addSite(t, e, pc.Srv.URL)
	rec := e.do("POST", "/setup/sites/site-a/bootstrap/prism-central", e.form("username", "admin", "password", "wrong-pw-9", "url", pc.Srv.URL, "service_account", "svc-x"), true)
	body := rec.Body.String()
	for _, want := range []string{"banner error", `name="username" value="admin"`, `name="service_account" value="svc-x"`, `name="password" value=""`} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("fragment lacks %q: %d %s", want, rec.Code, body)
		}
	}
	if strings.Contains(body, "wrong-pw-9") {
		t.Fatal("the password reached the page")
	}
	rec = e.do("POST", "/setup/bootstrap/keycloak", e.form("admin_username", "kcadmin", "admin_password", "wrong-pw-9", "realm", "msp", "display_name", "ACME MSP"), true)
	body = rec.Body.String()
	for _, want := range []string{"banner error", `name="admin_username" value="kcadmin"`, `name="display_name" value="ACME MSP"`, `name="admin_password" value=""`} {
		if !strings.Contains(body, want) {
			t.Fatalf("keycloak fragment lacks %q: %d %s", want, rec.Code, body)
		}
	}
}

// addSite creates site SITE-A (id site-a) through the page, as an
// administrator does before its bootstraps.
func addSite(t *testing.T, e *env, pcURL string) {
	t.Helper()
	rec := e.do("POST", "/setup/sites/new", e.form("code", "SITE-A", "name", "Milan", "url", pcURL, "tls_verify", ""), true)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/setup/sites/site-a?") {
		t.Fatalf("add site: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
}
