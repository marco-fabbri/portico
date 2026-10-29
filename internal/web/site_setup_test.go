package web

import (
	"context"
	"encoding/pem"
	"net/url"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/proxy"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/settings/settingstest"
)

// withSiteFakes gives the setup options a Prism Central over https and a
// Keycloak a setup run can use; it returns them and the fingerprint the
// Prism Central presents.
func withSiteFakes(t *testing.T, e *env) (*settingstest.KC, *settingstest.PC, string) {
	t.Helper()
	kc, pc := settingstest.NewKC(t), settingstest.NewTLSPC(t)
	pc.Version = "pc.7.6.0.6"
	kc.Realms["portico"] = map[string]any{"realm": "portico"}
	kc.Descriptor = "https://id.example.test/realms/portico"
	e.web.setup.KeycloakURL = kc.Srv.URL
	e.web.setup.Bootstrapper = settings.NewBootstrapper(nil, e.web.setup.Service)
	return kc, pc, proxy.Fingerprint(pc.Srv.Certificate().Raw)
}

// storeSite adds s to the stored document.
func storeSite(t *testing.T, svc *settings.Service, s settings.Site) {
	t.Helper()
	if err := svc.Patch(context.Background(), func(d *settings.Settings) { d.Sites = append(d.Sites, s) }); err != nil {
		t.Fatal(err)
	}
}

func TestNewSiteStep1HasNoPasswordField(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	e.login(t)
	body := e.do("GET", "/setup/sites/new", nil, true).Body.String()
	for _, want := range []string{`name="code"`, `name="url"`, `hx-post="/setup/sites/new/inspect"`, `name="ca_pem"`, `name="service_account"`, `name="api_key"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("step 1 lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `name="password"`) || strings.Contains(body, `name="tls_verify"`) {
		t.Fatalf("step 1 asks for a password or offers verify off:\n%s", body)
	}
}

func TestInspectUntrustedShowsFingerprintAndCheckbox(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	_, pc, fp := withSiteFakes(t, e)
	e.login(t)
	rec := e.do("POST", "/setup/sites/new/inspect", e.form("url", pc.Srv.URL), true)
	body := rec.Body.String()
	for _, want := range []string{fp, "Certificate is not valid", `type="checkbox" name="confirmed"`, "I compared it: this is my Prism Central's certificate", `hx-trigger="change"`, `name="pin" value="` + fp + `"`} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("untrusted fragment lacks %q: %d\n%s", want, rec.Code, body)
		}
	}
	if strings.Contains(body, `name="password"`) || pc.Credentialed != 0 {
		t.Fatalf("credentials asked or sent before the confirmation:\n%s", body)
	}
	body = e.do("POST", "/setup/sites/new/inspect", e.form("url", pc.Srv.URL, "confirmed", "on", "pin", fp), true).Body.String()
	if !strings.Contains(body, `name="password"`) || !strings.Contains(body, `checked`) {
		t.Fatalf("a confirmed certificate shows no step 2:\n%s", body)
	}
	// A fingerprint that is no longer the one presented is not confirmed.
	body = e.do("POST", "/setup/sites/new/inspect", e.form("url", pc.Srv.URL, "confirmed", "on", "pin", "AB:CD"), true).Body.String()
	if strings.Contains(body, `name="password"`) {
		t.Fatalf("an old fingerprint confirmed the certificate:\n%s", body)
	}
	body = e.do("POST", "/setup/sites/new/inspect", e.form("url", "https://127.0.0.1:1"), true).Body.String()
	if !strings.Contains(body, "banner error") || strings.Contains(body, `name="password"`) {
		t.Fatalf("nothing answers:\n%s", body)
	}
}

func TestInspectTrustedShowsCredentials(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	_, pc, _ := withSiteFakes(t, e)
	e.login(t)
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pc.Srv.Certificate().Raw}))
	body := e.do("POST", "/setup/sites/new/inspect", e.form("url", pc.Srv.URL, "ca_pem", ca), true).Body.String()
	if !strings.Contains(body, "The certificate is trusted") || !strings.Contains(body, `name="password"`) || strings.Contains(body, `name="confirmed"`) {
		t.Fatalf("trusted fragment:\n%s", body)
	}
}

func TestNewSiteRunShowsSummary(t *testing.T) {
	e := newEnv(t, true)
	svc, _, _ := withSetup(t, e, true)
	_, pc, fp := withSiteFakes(t, e)
	e.login(t)
	rec := e.do("POST", "/setup/sites/new", e.form("code", "AZ-US-NYC1", "name", "New York", "url", pc.Srv.URL, "confirmed", "on", "pin", fp, "username", "admin", "password", "pc-pw-1"), true)
	body := rec.Body.String()
	if rec.Code != 200 || strings.Count(body, `class="step `) != 9 || !strings.Contains(body, `href="/setup/sites/az-us-nyc1"`) {
		t.Fatalf("summary: %d\n%s", rec.Code, body)
	}
	if strings.Contains(body, "pc-pw-1") || strings.Contains(body, pc.Key) || strings.Contains(e.log.String(), "pc-pw-1") {
		t.Fatal("a secret in the summary or the log")
	}
	cur, _ := svc.Get(context.Background())
	if s, ok := cur.Site("az-us-nyc1"); !ok || !s.Complete() || s.TLSPin != fp {
		t.Fatalf("stored site = %+v", s)
	}
	// The form names its progress text.
	page := e.do("GET", "/setup/sites/new", nil, true).Body.String()
	if !strings.Contains(page, `hx-indicator=`) || !strings.Contains(page, "Setting up the site, this takes up to two minutes") {
		t.Fatalf("no progress indicator:\n%s", page)
	}
}

func TestSitePageStatusCardFromPrismCentral(t *testing.T) {
	e := newEnv(t, true)
	svc, _, _ := withSetup(t, e, true)
	_, pc, fp := withSiteFakes(t, e)
	storeSite(t, svc, settings.Site{ID: "nyc1", Code: "AZ-US-NYC1", URL: pc.Srv.URL, TLSPin: fp, APIKey: settings.Secret(pc.Key),
		SAMLIdPName: "portico-keycloak", SAMLIdPExtID: "idp-gone", SAMLEntityID: "https://prism.example.test/api/iam/authn/nyc1", ClusterExtID: "cl-1", StorageContainerExtID: "sc-1"})
	e.login(t)
	page := e.do("GET", "/setup/sites/nyc1", nil, true).Body.String()
	if !strings.Contains(page, `hx-post="/setup/sites/nyc1/status"`) || !strings.Contains(page, `hx-trigger="load"`) {
		t.Fatalf("the site page does not load its status:\n%s", page)
	}
	if !strings.Contains(page, `hx-vals='{"_csrf":"`+e.csrf+`"}'`) {
		t.Fatalf("the status call carries no CSRF token:\n%s", page)
	}
	// The check reports to the monitor: a POST with the token only.
	if rec := e.do("GET", "/setup/sites/nyc1/status", nil, true); rec.Code == 200 {
		t.Fatalf("GET status: %d", rec.Code)
	}
	if rec := e.do("POST", "/setup/sites/nyc1/status", url.Values{}, true); rec.Code != 403 {
		t.Fatalf("POST status without the token: %d", rec.Code)
	}
	body := e.do("POST", "/setup/sites/nyc1/status", e.form(), true).Body.String()
	for _, want := range []string{"Connection", "Service account", "SAML trust", "Defaults", "the identity provider portico-keycloak is missing in Prism Central",
		`hx-post="/setup/sites/nyc1/setup"`, ">Repair</button>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("status card lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, ">Finish setup</button>") {
		t.Fatalf("a site with a key and a trust name is offered Finish setup:\n%s", body)
	}
}

func TestSitePageNoVerifyOffControl(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	e.login(t)
	for _, path := range []string{"/setup/sites/site-a", "/setup/sites/new"} {
		if body := e.do("GET", path, nil, true).Body.String(); strings.Contains(body, `name="tls_verify"`) {
			t.Fatalf("%s offers verify off:\n%s", path, body)
		}
	}
}

func TestLegacyVerifyOffShowsPinAction(t *testing.T) {
	e := newEnv(t, true)
	svc, _, _ := withSetup(t, e, true)
	_, pc, fp := withSiteFakes(t, e)
	storeSite(t, svc, settings.Site{ID: "nyc1", Code: "AZ-US-NYC1", URL: pc.Srv.URL, APIKey: settings.Secret(pc.Key)})
	e.login(t)
	body := e.do("POST", "/setup/sites/nyc1/status", e.form(), true).Body.String()
	for _, want := range []string{"certificate is not verified", fp, `action="/setup/sites/nyc1/pin"`, `name="fingerprint" value="` + fp + `"`, "Pin the current certificate"} {
		if !strings.Contains(body, want) {
			t.Fatalf("legacy status lacks %q:\n%s", want, body)
		}
	}
	const pinFirst = "Pin the certificate on the status card first: Portico sends the credentials only to a confirmed certificate."
	if page := e.do("GET", "/setup/sites/nyc1", nil, true).Body.String(); !strings.Contains(page, pinFirst) {
		t.Fatalf("the Remove card of an unverified site does not say to pin first:\n%s", page)
	}
	rec := e.do("POST", "/setup/sites/nyc1/pin", e.form("fingerprint", fp), true)
	if rec.Code != 303 || strings.Contains(rec.Header().Get("Location"), "Apply") {
		t.Fatalf("pin: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	cur, _ := svc.Get(context.Background())
	if s, _ := cur.Site("nyc1"); s.TLSPin != fp {
		t.Fatalf("pin = %q", s.TLSPin)
	}
	if page := e.do("GET", "/setup/sites/nyc1", nil, true).Body.String(); strings.Contains(page, pinFirst) {
		t.Fatal("a pinned site is still asked to pin first")
	}
}

func TestCertificateChangedShowsConfirm(t *testing.T) {
	e := newEnv(t, true)
	svc, _, _ := withSetup(t, e, true)
	_, pc, fp := withSiteFakes(t, e)
	storeSite(t, svc, settings.Site{ID: "nyc1", Code: "AZ-US-NYC1", URL: pc.Srv.URL, TLSPin: "AB:CD", APIKey: settings.Secret(pc.Key)})
	e.login(t)
	body := e.do("POST", "/setup/sites/nyc1/status", e.form(), true).Body.String()
	for _, want := range []string{"certificate changed", fp, `action="/setup/sites/nyc1/pin"`, "Confirm the new certificate"} {
		if !strings.Contains(body, want) {
			t.Fatalf("changed certificate lacks %q:\n%s", want, body)
		}
	}
	// A fingerprint other than the presented one is refused and named.
	rec := e.do("POST", "/setup/sites/nyc1/pin", e.form("fingerprint", "AB:CD"), true)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), fp) {
		t.Fatalf("an old fingerprint: %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestRemoveWithoutCredentialsListsLeftovers(t *testing.T) {
	e := newEnv(t, true)
	svc, _, _ := withSetup(t, e, true)
	kc, pc, fp := withSiteFakes(t, e)
	e.login(t)
	if rec := e.do("POST", "/setup/sites/new", e.form("code", "AZ-US-NYC1", "url", pc.Srv.URL, "confirmed", "on", "pin", fp, "username", "admin", "password", "pc-pw-1"), true); rec.Code != 200 {
		t.Fatalf("add: %d", rec.Code)
	}
	page := e.do("GET", "/setup/sites/az-us-nyc1", nil, true).Body.String()
	if !strings.Contains(page, `action="/setup/sites/az-us-nyc1/delete"`) || !strings.Contains(page, `name="password"`) {
		t.Fatalf("the remove card offers no credentials:\n%s", page)
	}
	rec := e.do("POST", "/setup/sites/az-us-nyc1/delete", e.form("username", "", "password", ""), true)
	body := rec.Body.String()
	for _, want := range []string{"Site AZ-US-NYC1 removed", "identity provider portico-keycloak", "service account sa-1", "API key key-1", "Keycloak client"} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("removed page lacks %q: %d\n%s", want, rec.Code, body)
		}
	}
	if strings.Contains(body, "Apply") {
		t.Fatalf("the removed page speaks of Apply:\n%s", body)
	}
	cur, _ := svc.Get(context.Background())
	if _, ok := cur.Site("az-us-nyc1"); ok || len(pc.IdPs) != 1 {
		t.Fatalf("after remove: site kept or Prism Central changed (%d providers)", len(pc.IdPs))
	}
	for _, c := range kc.Clients {
		if strings.HasSuffix(c["clientId"].(string), "/az-us-nyc1") {
			t.Fatal("the Keycloak client is still there")
		}
	}
}

func TestNoApplyNoticeForSites(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	e.login(t)
	rec := e.do("POST", "/setup/sites/site-a/save", e.form("code", "SITE-A", "name", "Milan", "order", "1"), true)
	loc := rec.Header().Get("Location")
	nc := cookieNamed(rec, noticeCookie)
	if rec.Code != 303 || nc == nil {
		t.Fatalf("save: %d %q (no notice cookie)", rec.Code, loc)
	}
	notice, _ := url.QueryUnescape(nc.Value)
	if strings.Contains(notice, "Apply") {
		t.Fatalf("the notice speaks of Apply: %q", notice)
	}
	for _, path := range []string{"/setup/sites/site-a", "/setup/sites/new"} {
		if body := e.do("GET", path, nil, true).Body.String(); strings.Contains(body, "apply") || strings.Contains(body, "Apply") {
			t.Fatalf("%s speaks of Apply:\n%s", path, body)
		}
	}
}
