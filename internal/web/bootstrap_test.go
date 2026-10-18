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
	rec := e.do("POST", "/setup/bootstrap/prism-central", e.form("username", "admin", "password", "wrong", "url", pc.Srv.URL, "service_account", "portico"), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "banner error") || !strings.Contains(rec.Body.String(), "refused the credentials") {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body.String())
	}
	if page := e.do("GET", "/setup", nil, true).Body.String(); strings.Contains(page, `placeholder="unchanged"`) {
		t.Fatal("a key was stored after a refusal")
	}
	rec = e.do("POST", "/setup/bootstrap/prism-central", e.form("username", "admin", "password", "pc-pw-1", "url", pc.Srv.URL, "service_account", "portico"), true)
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
	if got.PrismCentral.APIKey != settings.Secret(pc.Key) || got.PrismCentral.URL != pc.Srv.URL {
		t.Fatalf("stored = %+v", got)
	}
}

func TestSetupBootstrapSAMLNeedsPreconditions(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	withBootstrapFakes(t, e)
	e.login(t)
	rec := e.do("POST", "/setup/bootstrap/saml", e.form("saml_idp_name", "portico-keycloak"), true)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "must be set first") || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("saml on a fresh store: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("POST", "/setup/bootstrap/nope", e.form(), true)
	if rec.Code != 404 {
		t.Fatalf("unknown target: %d", rec.Code)
	}
}

func TestSetupBootstrapSAMLFragmentStoresTheName(t *testing.T) {
	e := newEnv(t, true)
	svc, _, _ := withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	kc, pc := withBootstrapFakes(t, e)
	kc.Realms["msp"] = map[string]any{"realm": "msp"}
	doc := setupSample(pc.Srv.URL)
	doc.Keycloak.Realm, doc.OIDC.Issuer, doc.PrismCentral.APIKey, doc.PrismCentral.SAMLIdPName = "msp", "https://id.example.test/realms/msp", settings.Secret(pc.Key), ""
	if err := svc.Set(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	e.login(t)
	rec := e.do("POST", "/setup/bootstrap/saml", e.form("saml_idp_name", "msp-idp"), true)
	body := rec.Body.String()
	for _, want := range []string{"banner notice", `id="saml_idp_name"`, `value="msp-idp"`, `hx-swap-oob`} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("fragment lacks %q: %d %s", want, rec.Code, body)
		}
	}
	got, _ := svc.Get(context.Background())
	if got.PrismCentral.SAMLIdPName != "msp-idp" || len(got.Missing()) != 0 {
		t.Fatalf("stored = %+v missing=%v", got, got.Missing())
	}
}

func TestSetupSAMLCardSavesTheIdPName(t *testing.T) {
	e := newEnv(t, true)
	svc, pc, _ := withSetup(t, e, true)
	e.login(t)
	rec := e.do("POST", "/setup/save", e.form("section", "saml", "saml_idp_name", "idp-two"), true)
	if rec.Code != 303 {
		t.Fatalf("save saml: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := svc.Get(context.Background())
	if got.PrismCentral.SAMLIdPName != "idp-two" || got.PrismCentral.URL != pc.URL {
		t.Fatalf("stored = %+v", got)
	}
	rec = e.do("POST", "/setup/save", e.form("section", "prism_central", "url", pc.URL, "tls_verify", ""), true)
	got, _ = svc.Get(context.Background())
	if rec.Code != 303 || got.PrismCentral.SAMLIdPName != "idp-two" {
		t.Fatalf("prism_central save changed the IdP name: %d %+v", rec.Code, got)
	}
	page := e.do("GET", "/setup", nil, true).Body.String()
	if !strings.Contains(page, "SAML trust") || !strings.Contains(page, `id="form-saml"`) || !strings.Contains(page, `value="idp-two"`) {
		t.Fatalf("page lacks the SAML card: %s", page)
	}
}

func TestSetupBootstrapRequiresCSRF(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	kc, pc := withBootstrapFakes(t, e)
	e.login(t)
	for _, target := range []string{"keycloak", "prism-central", "saml"} {
		rec := e.do("POST", "/setup/bootstrap/"+target, map[string][]string{"admin_username": {"kcadmin"}, "admin_password": {"adm-pw-1"}, "username": {"admin"}, "password": {"pc-pw-1"}, "url": {pc.Srv.URL}}, true)
		if rec.Code != 403 {
			t.Errorf("%s without CSRF: %d", target, rec.Code)
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
	rec := e.do("POST", "/setup/bootstrap/prism-central", e.form("username", "admin", "password", "wrong-pw-9", "url", pc.Srv.URL, "service_account", "svc-x"), true)
	body := rec.Body.String()
	for _, want := range []string{"banner error", `name="username" value="admin"`, `name="service_account" value="svc-x"`, `name="password" value=""`} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("fragment lacks %q: %d %s", want, rec.Code, body)
		}
	}
	if strings.Contains(body, "wrong-pw-9") {
		t.Fatal("the password reached the page")
	}
	rec = e.do("POST", "/setup/bootstrap/keycloak", e.form("admin_username", "kcadmin", "admin_password", "wrong-pw-9", "realm", "msp", "display_name", "Lab MSP"), true)
	body = rec.Body.String()
	for _, want := range []string{"banner error", `name="admin_username" value="kcadmin"`, `name="display_name" value="Lab MSP"`, `name="admin_password" value=""`} {
		if !strings.Contains(body, want) {
			t.Fatalf("keycloak fragment lacks %q: %d %s", want, rec.Code, body)
		}
	}
}
