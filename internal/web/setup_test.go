package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/secrets"
	"github.com/marco-fabbri/portico/internal/settings"
)

func setupSample(pcURL string) settings.Settings {
	var s settings.Settings
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	s.Sites = []settings.Site{{ID: "site-a", Code: "SITE-A", Name: "pc", URL: pcURL, APIKey: "key-secret-1", SAMLIdPName: "portico-idp", ClusterExtID: "cl-1", StorageContainerExtID: "sc-1"}}
	s.Keycloak.Realm, s.Keycloak.AdminClientID, s.Keycloak.AdminClientSecret = "portico", "portico-admin", "admin-secret-1"
	s.OIDC.Issuer, s.OIDC.ClientID, s.OIDC.ClientSecret = "https://id.example.test/realms/portico", "portico", "client-secret-1"
	s.Tenants.ProjectPrefix, s.Tenants.GroupPrefix, s.Tenants.Role = "portico", "tenant-", "Project Admin"
	s.Tenants.OperationTimeout = 10 * time.Minute
	return s
}

// withSetup adds the settings service, a fake Prism Central and a restart
// counter to the web env.
// setupPCEmpty makes the fake Prism Central answer no clusters and no
// containers (a Prism Central with nothing registered yet).
var setupPCEmpty atomic.Bool

func withSetup(t *testing.T, e *env, stored bool) (*settings.Service, *httptest.Server, *int) {
	t.Helper()
	pc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Ntnx-Api-Key") != "key-secret-1" {
			w.WriteHeader(401)
			w.Write([]byte(`{"data":{"error":"unauthorized"}}`))
			return
		}
		switch {
		case setupPCEmpty.Load() && (strings.HasSuffix(r.URL.Path, "/config/clusters") || strings.HasSuffix(r.URL.Path, "/config/storage-containers")):
			w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/config/clusters"):
			w.Write([]byte(`{"data":[{"extId":"cl-1","name":"NTNX-01","config":{"clusterFunction":["AOS"]}}]}`))
		case strings.HasSuffix(r.URL.Path, "/config/storage-containers"):
			w.Write([]byte(`{"data":[{"containerExtId":"sc-1","name":"default-container"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(pc.Close)
	sealer, _ := secrets.NewSealer(bytes.Repeat([]byte{7}, secrets.KeySize))
	svc := settings.NewService(e.st, sealer)
	if stored {
		if err := svc.Set(context.Background(), setupSample(pc.URL)); err != nil {
			t.Fatal(err)
		}
	}
	restarted := 0
	file := setupSample(pc.URL)
	e.web.setup = &api.SettingsOptions{Service: svc, File: &file, Checker: settings.NewChecker(nil), KeycloakURL: "http://127.0.0.1:1", Restart: func() { restarted++ }}
	return svc, pc, &restarted
}

func TestSetupPagePrefillsFromFileWhenNotStored(t *testing.T) {
	e := newEnv(t, true)
	_, pc, _ := withSetup(t, e, false)
	e.login(t)
	rec := e.do("GET", "/setup", nil, true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "running from the file") || !strings.Contains(body, pc.URL) || !strings.Contains(body, `href="/setup/sites/site-a"`) || !strings.Contains(body, `href="/setup"`) {
		t.Fatalf("setup page: %d %s", rec.Code, body)
	}
	if strings.Contains(body, "key-secret-1") {
		t.Fatal("a secret is prefilled")
	}
}

func TestSetupSaveSectionAndSecretsStayHidden(t *testing.T) {
	e := newEnv(t, true)
	svc, pc, _ := withSetup(t, e, false)
	e.login(t)
	// Save the sections one by one, as the page does; the site's
	// connection is saved on its own page.
	rec := e.do("POST", "/setup/sites/site-a/save", e.form("code", "SITE-A", "url", pc.URL, "tls_verify", "", "api_key", "key-secret-2"), true)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/setup/sites/site-a") {
		t.Fatalf("save site: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	for _, f := range []url.Values{
		e.form("section", "hosts", "portal_host", "prism.example.test", "id_host", "id.example.test"),
		e.form("section", "keycloak", "realm", "portico", "admin_client_id", "portico-admin", "admin_client_secret", "admin-secret-1", "issuer", "https://id.example.test/realms/portico", "client_id", "portico", "client_secret", "client-secret-1"),
		e.form("section", "tenants", "project_prefix", "portico", "group_prefix", "tenant-", "role", "Project Admin", "operation_timeout", "10m"),
	} {
		rec := e.do("POST", "/setup/save", f, true)
		if rec.Code != 303 || rec.Header().Get("Location") != "/setup" {
			t.Fatalf("save %s: %d %q %s", f.Get("section"), rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
	}
	got, err := svc.Get(context.Background())
	if err != nil || got.Sites[0].APIKey != "key-secret-2" || got.OIDC.ClientSecret != "client-secret-1" || got.Tenants.OperationTimeout != 10*time.Minute {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	for _, page := range []string{"/setup", "/setup/sites/site-a"} {
		body := e.do("GET", page, nil, true).Body.String()
		if strings.Contains(body, "key-secret-") || !strings.Contains(body, `placeholder="unchanged"`) || strings.Contains(body, "running from the file") {
			t.Fatalf("%s after save: %s", page, body)
		}
	}
	// Saving the site without the key keeps it, and keeps the SAML trust.
	rec = e.do("POST", "/setup/sites/site-a/save", e.form("code", "SITE-A", "name", "Milan", "url", pc.URL, "tls_verify", ""), true)
	if rec.Code != 303 {
		t.Fatalf("second save: %d %s", rec.Code, rec.Body.String())
	}
	got, _ = svc.Get(context.Background())
	if got.Sites[0].APIKey != "key-secret-2" || got.Sites[0].SAMLIdPName != "portico-idp" || got.Sites[0].Name != "Milan" {
		t.Fatalf("stored after second save = %+v", got.Sites[0])
	}
}

func TestSetupCheckRendersResultAndPickers(t *testing.T) {
	e := newEnv(t, true)
	_, pc, _ := withSetup(t, e, true)
	e.login(t)
	rec := e.do("POST", "/setup/sites/site-a/check", e.form("url", pc.URL, "tls_verify", ""), true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "NTNX-01") || !strings.Contains(body, `value="cl-1"`) || !strings.Contains(body, "default-container") || strings.Contains(body, "<html") {
		t.Fatalf("check fragment: %d %s", rec.Code, body)
	}
	rec = e.do("POST", "/setup/sites/site-a/check", e.form("url", pc.URL, "tls_verify", "", "api_key", "wrong"), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "API key") {
		t.Fatalf("check with a wrong key: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSetupApplyRendersWaitingPage(t *testing.T) {
	e := newEnv(t, true)
	_, _, restarted := withSetup(t, e, true)
	e.login(t)
	rec := e.do("POST", "/setup/apply", e.form(), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `hx-get="/setup/ready"`) || *restarted != 1 {
		t.Fatalf("apply: %d restarted=%d %s", rec.Code, *restarted, rec.Body.String())
	}
	// The readiness fragment needs no session and reports the restart.
	rec = e.do("GET", "/setup/ready", nil, false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "restarting") {
		t.Fatalf("ready while restarting: %d %s", rec.Code, rec.Body.String())
	}
	e2 := newEnv(t, true)
	withSetup(t, e2, true)
	rec = e2.do("GET", "/setup/ready", nil, false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `href="/login"`) {
		t.Fatalf("ready after restart: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSetupApplyWithoutStoredSettingsIsRefused(t *testing.T) {
	e := newEnv(t, true)
	_, _, restarted := withSetup(t, e, false)
	e.login(t)
	rec := e.do("POST", "/setup/apply", e.form(), true)
	if rec.Code != 409 || *restarted != 0 || !strings.Contains(rec.Body.String(), "save them first") {
		t.Fatalf("apply without stored settings: %d restarted=%d", rec.Code, *restarted)
	}
}

func TestSetupRequiresCSRF(t *testing.T) {
	e := newEnv(t, true)
	svc, pc, restarted := withSetup(t, e, true)
	e.login(t)
	before, _ := svc.Get(context.Background())
	for _, path := range []string{"/setup/save", "/setup/check", "/setup/apply", "/setup/sites/new", "/setup/sites/site-a/save", "/setup/sites/site-a/check", "/setup/sites/site-a/delete", "/setup/sites/site-a/forget"} {
		rec := e.do("POST", path, url.Values{"section": {"hosts"}, "portal_host": {"evil.test"}, "target": {"keycloak"}, "code": {"EVIL"}, "confirm": {"SITE-A"}, "url": {pc.URL}}, true)
		if rec.Code != 403 {
			t.Errorf("%s without CSRF: %d", path, rec.Code)
		}
	}
	after, _ := svc.Get(context.Background())
	if after.PortalHost != before.PortalHost || len(after.Sites) != 1 || after.Sites[0].Code != "SITE-A" || *restarted != 0 {
		t.Fatal("a form without CSRF changed state")
	}
}

func TestSetupPageInSetupModeListsMissingAndDisablesApply(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	e.login(t)
	rec := e.do("GET", "/setup", nil, true)
	body := rec.Body.String()
	for _, want := range []string{"Setup mode", "Missing: portal_host, id_host, sites", `value="portico"`, `disabled`, `href="/setup/sites/new"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("setup page lacks %q: %s", want, body)
		}
	}
	rec = e.do("POST", "/setup/save", e.form("section", "hosts", "portal_host", "prism.example.test", "id_host", "id.example.test"), true)
	if rec.Code != 303 {
		t.Fatalf("save hosts on a fresh store: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("GET", "/setup", nil, true)
	if body := rec.Body.String(); strings.Contains(body, "Missing: portal_host") || !strings.Contains(body, "Missing: sites") {
		t.Fatalf("missing list after save: %s", body)
	}
	rec = e.do("POST", "/setup/apply", e.form(), true)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "settings incomplete") {
		t.Fatalf("apply on a draft: %d", rec.Code)
	}
}

// Zero clusters or containers: the pickers stay required and say so,
// instead of rendering an empty select that submits nothing.
func TestSetupCheckRendersEmptyPickers(t *testing.T) {
	e := newEnv(t, true)
	_, pc, _ := withSetup(t, e, true)
	e.login(t)
	setupPCEmpty.Store(true)
	defer setupPCEmpty.Store(false)
	rec := e.do("POST", "/setup/sites/site-a/check", e.form("url", pc.URL, "tls_verify", ""), true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "none found") || !regexp.MustCompile(`<select name="cluster_ext_id"[^>]*required`).MatchString(body) || !regexp.MustCompile(`<select name="storage_container_ext_id"[^>]*required`).MatchString(body) {
		t.Fatalf("empty pickers: %d %s", rec.Code, body)
	}
}

// Password inputs say new-password: "off" does not stop password
// managers from offering and saving them.
func TestSetupSecretInputsStopPasswordManagers(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	e.login(t)
	for path, min := range map[string]int{"/setup": 3, "/setup/sites/site-a": 2, "/setup/sites/new": 1} {
		page := e.do("GET", path, nil, true).Body.String()
		if regexp.MustCompile(`type="password"[^>]*autocomplete="off"`).MatchString(page) {
			t.Fatalf("%s: a password input still says autocomplete=off", path)
		}
		if n := strings.Count(page, `autocomplete="new-password"`); n < min {
			t.Fatalf("%s: %d new-password inputs, want at least %d:\n%s", path, n, min, page)
		}
	}
}

// Every card says whether it is configured, and the bootstrap forms are
// folded away once it is (Marco, 2026-10-19: with the fields filled and
// an open "let Portico create it" form, the page did not say Prism
// Central was done). The Apply card states the whole.
func TestSetupPageStatesWhatIsConfigured(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	e.login(t)
	body := e.do("GET", "/setup", nil, true).Body.String()
	if n := strings.Count(body, `<p class="state done">`); n != 5 {
		t.Fatalf("%d configured states, want 5 (four cards and Apply):\n%s", n, body)
	}
	if strings.Contains(body, `class="state todo"`) || strings.Contains(body, `<details open>`) {
		t.Fatalf("a to-do state or an open bootstrap form on a complete store:\n%s", body)
	}
	for _, want := range []string{`<summary>Use an existing realm</summary>`, `<code>portico-idp</code>`, `href="/setup/sites/site-a"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("setup page lacks %q:\n%s", want, body)
		}
	}
	// The site page says nothing from Portico's own row: its status card
	// asks Prism Central and Keycloak (sites usability design 4.2).
	site := e.do("GET", "/setup/sites/site-a", nil, true).Body.String()
	if strings.Contains(site, "Configured:") || strings.Contains(site, `<details open>`) || !strings.Contains(site, `hx-post="/setup/sites/site-a/status"`) {
		t.Fatalf("site page on a complete site:\n%s", site)
	}

	e = newEnv(t, true)
	withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	e.login(t)
	body = e.do("GET", "/setup", nil, true).Body.String()
	if n, o := strings.Count(body, `<p class="state todo">`), strings.Count(body, `<details open>`); n != 4 || o != 1 {
		t.Fatalf("%d to-do states and %d open bootstrap forms on an empty store, want 4 (hosts, sites, identity, apply) and 1:\n%s", n, o, body)
	}
}

type fakeRealm struct {
	phase, msg string
	kicks      int
}

func (f *fakeRealm) State() (string, string) { return f.phase, f.msg }
func (f *fakeRealm) Kick()                   { f.kicks++ }

// The Identity card shows the realm job and polls it until it is ready;
// saving the public names starts it, and Retry kicks it again.
func TestIdentityCardFollowsTheRealmJob(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	realm := &fakeRealm{phase: "waiting-keycloak", msg: "Keycloak is starting"}
	e.web.setup.Realm = realm
	e.login(t)
	body := e.do("GET", "/setup", nil, true).Body.String()
	if !strings.Contains(body, "Keycloak is starting") || !strings.Contains(body, `hx-get="/setup/realm"`) || !strings.Contains(body, "Use an existing realm") {
		t.Fatalf("identity card:\n%s", body)
	}
	if rec := e.do("POST", "/setup/save", e.form("section", "hosts", "portal_host", "prism.example.test", "id_host", "id.example.test"), true); rec.Code != 303 || realm.kicks != 1 {
		t.Fatalf("hosts save: %d kicks %d", rec.Code, realm.kicks)
	}
	realm.phase, realm.msg = "failed", "Keycloak refused"
	frag := e.do("GET", "/setup/realm", nil, true).Body.String()
	if !strings.Contains(frag, "Keycloak refused") || strings.Contains(frag, "every 3s") || !strings.Contains(frag, `hx-post="/setup/realm/retry"`) {
		t.Fatalf("failed fragment: %s", frag)
	}
	if rec := e.do("POST", "/setup/realm/retry", e.form(), true); rec.Code != 200 || realm.kicks != 2 {
		t.Fatalf("retry: %d kicks %d", rec.Code, realm.kicks)
	}
	realm.phase, realm.msg = "ready", "realm portico is ready"
	if frag := e.do("GET", "/setup/realm", nil, true).Body.String(); strings.Contains(frag, "every 3s") || !strings.Contains(frag, "realm portico is ready") {
		t.Fatalf("ready fragment: %s", frag)
	}
}
