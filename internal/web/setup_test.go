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
	s.PrismHost, s.IDHost = "prism.example.test", "id.example.test"
	s.PrismCentral.URL, s.PrismCentral.TLSVerify = pcURL, false
	s.PrismCentral.APIKey, s.PrismCentral.SAMLIdPName = "key-secret-1", "portico-idp"
	s.Keycloak.Realm, s.Keycloak.AdminClientID, s.Keycloak.AdminClientSecret = "portico", "portico-admin", "admin-secret-1"
	s.OIDC.Issuer, s.OIDC.ClientID, s.OIDC.ClientSecret = "https://id.example.test/realms/portico", "portico", "client-secret-1"
	s.Tenants.ProjectPrefix, s.Tenants.GroupPrefix, s.Tenants.Role = "portico", "tenant-", "Project Admin"
	s.Tenants.ClusterExtID, s.Tenants.StorageContainerExtID = "cl-1", "sc-1"
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
	if rec.Code != 200 || !strings.Contains(body, "running from the file") || !strings.Contains(body, `value="`+pc.URL+`"`) || !strings.Contains(body, `href="/setup"`) {
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
	// Save the four sections one by one, as the page does.
	for _, f := range []url.Values{
		e.form("section", "hosts", "prism_host", "prism.example.test", "id_host", "id.example.test"),
		e.form("section", "prism_central", "url", pc.URL, "tls_verify", "", "api_key", "key-secret-2"),
		e.form("section", "saml", "saml_idp_name", "portico-idp"),
		e.form("section", "keycloak", "realm", "portico", "admin_client_id", "portico-admin", "admin_client_secret", "admin-secret-1", "issuer", "https://id.example.test/realms/portico", "client_id", "portico", "client_secret", "client-secret-1"),
		e.form("section", "tenants", "project_prefix", "portico", "group_prefix", "tenant-", "role", "Project Admin", "cluster_ext_id", "cl-1", "storage_container_ext_id", "sc-1", "operation_timeout", "10m"),
	} {
		rec := e.do("POST", "/setup/save", f, true)
		if rec.Code != 303 || rec.Header().Get("Location") != "/setup" {
			t.Fatalf("save %s: %d %q %s", f.Get("section"), rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
	}
	got, err := svc.Get(context.Background())
	if err != nil || got.PrismCentral.APIKey != "key-secret-2" || got.OIDC.ClientSecret != "client-secret-1" || got.Tenants.OperationTimeout != 10*time.Minute {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	rec := e.do("GET", "/setup", nil, true)
	body := rec.Body.String()
	if strings.Contains(body, "key-secret-") || !strings.Contains(body, `placeholder="unchanged"`) || strings.Contains(body, "running from the file") {
		t.Fatalf("page after save: %s", body)
	}
	// Saving a section without the secret keeps it.
	rec = e.do("POST", "/setup/save", e.form("section", "prism_central", "url", pc.URL, "tls_verify", ""), true)
	if rec.Code != 303 {
		t.Fatalf("second save: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("POST", "/setup/save", e.form("section", "saml", "saml_idp_name", "portico-idp-2"), true)
	if rec.Code != 303 {
		t.Fatalf("saml save: %d %s", rec.Code, rec.Body.String())
	}
	got, _ = svc.Get(context.Background())
	if got.PrismCentral.APIKey != "key-secret-2" || got.PrismCentral.SAMLIdPName != "portico-idp-2" {
		t.Fatalf("stored after second save = %+v", got)
	}
}

func TestSetupCheckRendersResultAndPickers(t *testing.T) {
	e := newEnv(t, true)
	_, pc, _ := withSetup(t, e, true)
	e.login(t)
	rec := e.do("POST", "/setup/check", e.form("target", "prism_central", "url", pc.URL, "tls_verify", ""), true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "NTNX-01") || !strings.Contains(body, `value="cl-1"`) || !strings.Contains(body, "default-container") || strings.Contains(body, "<html") {
		t.Fatalf("check fragment: %d %s", rec.Code, body)
	}
	rec = e.do("POST", "/setup/check", e.form("target", "prism_central", "url", pc.URL, "tls_verify", "", "api_key", "wrong"), true)
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
	for _, path := range []string{"/setup/save", "/setup/check", "/setup/apply"} {
		rec := e.do("POST", path, url.Values{"section": {"hosts"}, "prism_host": {"evil.test"}, "target": {"prism_central"}, "url": {pc.URL}}, true)
		if rec.Code != 403 {
			t.Errorf("%s without CSRF: %d", path, rec.Code)
		}
	}
	after, _ := svc.Get(context.Background())
	if after.PrismHost != before.PrismHost || *restarted != 0 {
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
	for _, want := range []string{"Setup mode", "Missing: prism_host, id_host", `value="portico"`, `disabled`} {
		if !strings.Contains(body, want) {
			t.Fatalf("setup page lacks %q: %s", want, body)
		}
	}
	rec = e.do("POST", "/setup/save", e.form("section", "hosts", "prism_host", "prism.example.test", "id_host", "id.example.test"), true)
	if rec.Code != 303 {
		t.Fatalf("save hosts on a fresh store: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("GET", "/setup", nil, true)
	if body := rec.Body.String(); strings.Contains(body, "Missing: prism_host") || !strings.Contains(body, "Missing: prism_central.url") {
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
	rec := e.do("POST", "/setup/check", e.form("target", "prism_central", "url", pc.URL, "tls_verify", ""), true)
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
	page := e.do("GET", "/setup", nil, true).Body.String()
	if regexp.MustCompile(`type="password"[^>]*autocomplete="off"`).MatchString(page) {
		t.Fatal("a password input still says autocomplete=off")
	}
	if n := strings.Count(page, `autocomplete="new-password"`); n < 6 {
		t.Fatalf("%d new-password inputs, want at least 6:\n%s", n, page)
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
	if n := strings.Count(body, `<p class="state done">`); n != 6 {
		t.Fatalf("%d configured states, want 6 (five cards and Apply):\n%s", n, body)
	}
	if strings.Contains(body, `class="state todo"`) || strings.Contains(body, `<details open>`) {
		t.Fatalf("a to-do state or an open bootstrap form on a complete store:\n%s", body)
	}
	for _, want := range []string{`<summary>Let Portico create the service account</summary>`, `<summary>Let Portico create the realm and clients</summary>`, `<summary>Let Portico create the SAML trust</summary>`, `<code>portico-idp</code>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("setup page lacks %q:\n%s", want, body)
		}
	}

	e = newEnv(t, true)
	withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	e.login(t)
	body = e.do("GET", "/setup", nil, true).Body.String()
	if n, o := strings.Count(body, `<p class="state todo">`), strings.Count(body, `<details open>`); n != 6 || o != 3 {
		t.Fatalf("%d to-do states and %d open bootstrap forms on an empty store, want 6 and 3:\n%s", n, o, body)
	}
}
