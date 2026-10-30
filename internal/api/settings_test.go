package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/branding"
	"github.com/marco-fabbri/portico/internal/secrets"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/settings/settingstest"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
	"github.com/marco-fabbri/portico/internal/tenant/tenanttest"
	"golang.org/x/crypto/bcrypt"
)

type settingsEnv struct {
	h         http.Handler
	svc       *settings.Service
	log       *bytes.Buffer
	restarted int
	busy      int
	pc        *httptest.Server
	so        *SettingsOptions
	kcAdmin   *fakeKCAdmin
	pcAdmin   *fakePCAdmin
	st        store.Store
	sealer    *secrets.Sealer
	tenants   *tenant.Service
	kc        *settingstest.KC // set by withSetup
}

func sampleSettings(pcURL string) settings.Settings {
	var s settings.Settings
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	s.Sites = []settings.Site{{ID: "site-a", Code: "SITE-A", Name: "pc", URL: pcURL, APIKey: "key-secret-1", SAMLIdPName: "portico-idp", ClusterExtID: "cl-1", StorageContainerExtID: "sc-1"}}
	s.Keycloak.Realm, s.Keycloak.AdminClientID, s.Keycloak.AdminClientSecret = "portico", "portico-admin", "admin-secret-1"
	s.OIDC.Issuer, s.OIDC.ClientID, s.OIDC.ClientSecret = "https://id.example.test/realms/portico", "portico", "client-secret-1"
	s.Tenants.ProjectPrefix, s.Tenants.GroupPrefix, s.Tenants.Role = "portico", "tenant-", "Project Admin"
	s.Tenants.OperationTimeout = 10 * time.Minute
	return s
}

// fakePC answers the two list calls of the Prism Central check; seen, when
// given, records the API key header of every request.
func fakePC(key string, seen *[]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = append(*seen, r.Header.Get("X-Ntnx-Api-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Ntnx-Api-Key") != key {
			w.WriteHeader(401)
			w.Write([]byte(`{"data":{"error":"unauthorized"}}`))
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/config/clusters"):
			w.Write([]byte(`{"data":[{"extId":"cl-1","name":"NTNX-01","config":{"clusterFunction":["AOS"]}}]}`))
		case strings.HasSuffix(r.URL.Path, "/config/storage-containers"):
			w.Write([]byte(`{"data":[{"containerExtId":"sc-1","name":"default"}]}`))
		default:
			w.WriteHeader(404)
		}
	})
}

func newSettingsEnv(t *testing.T, stored bool) *settingsEnv {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, _ := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.MinCost)
	st.SetAdminPassword(context.Background(), "admin", string(hash))
	sealer, _ := secrets.NewSealer(bytes.Repeat([]byte{7}, secrets.KeySize))
	svc := settings.NewService(st, sealer)
	// A fake Prism Central for the check: accepts the key "key-secret-1".
	pc := httptest.NewServer(fakePC("key-secret-1", nil))
	t.Cleanup(pc.Close)
	e := &settingsEnv{svc: svc, log: &bytes.Buffer{}, pc: pc, st: st, sealer: sealer}
	if stored {
		if err := svc.Set(context.Background(), sampleSettings(pc.URL)); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewJSONHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tsvc := tenant.NewService(st, []tenant.Site{{ID: "site-a", Code: "SITE-A", PC: tenanttest.NewPC("portico-idp"), SAMLIdPName: "portico-idp", ClusterExtID: "cl", StorageContainerExtID: "sc"}}, tenanttest.NewKC(), tenant.Config{RoleName: "Project Admin", OperationTimeout: 5 * time.Second}, log)
	e.tenants = tsvc
	file := sampleSettings(pc.URL)
	e.busy = 0
	e.kcAdmin = newFakeKCAdmin(t)
	e.pcAdmin = newFakePCAdmin(t)
	so := &SettingsOptions{Service: svc, File: &file, Checker: settings.NewChecker(log), KeycloakURL: e.kcAdmin.srv.URL, Restart: func() { e.restarted++ }, Busy: func() int { return e.busy }}
	e.so = so
	e.h = RequireAdmin(st, nil, nil, "portico", log)(New(tsvc, st, branding.NewService(st, log), so, log))
	return e
}

// newDraftEnv is a fresh appliance: nothing stored and no file.
func newDraftEnv(t *testing.T) *settingsEnv {
	e := newSettingsEnv(t, false)
	e.so.File, e.so.Mode = nil, "setup"
	return e
}

func (e *settingsEnv) do(method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	r := httptest.NewRequest(method, "https://127.0.0.1:8443"+path, rd)
	r.Header.Set("Content-Type", "application/json")
	r.SetBasicAuth("admin", adminPassword)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestSettingsGetHidesSecrets(t *testing.T) {
	e := newSettingsEnv(t, true)
	rec, out := e.do("GET", "/api/v1/settings", "")
	if rec.Code != 200 || out["stored"] != true || out["source"] != "store" {
		t.Fatalf("GET: %d %v", rec.Code, out)
	}
	body := rec.Body.String()
	for _, secret := range []string{"key-secret-1", "admin-secret-1", "client-secret-1", "v1:"} {
		if strings.Contains(body, secret) {
			t.Fatalf("GET body contains %q", secret)
		}
	}
	rec, out = e.do("GET", "/api/v1/sites", "")
	if site := out["sites"].([]any)[0].(map[string]any); rec.Code != 200 || site["api_key_set"] != true || site["url"] != e.pc.URL || strings.Contains(rec.Body.String(), "key-secret-1") {
		t.Fatalf("GET sites: %d %s", rec.Code, rec.Body.String())
	}
	e2 := newSettingsEnv(t, false)
	rec, out = e2.do("GET", "/api/v1/settings", "")
	if rec.Code != 200 || out["stored"] != false || out["source"] != "file" {
		t.Fatalf("GET from file: %d %v", rec.Code, out)
	}
	if _, sites := e2.do("GET", "/api/v1/sites", ""); sites["sites"].([]any)[0].(map[string]any)["api_key_set"] != true {
		t.Fatalf("GET sites from file: %v", sites)
	}
	if body := rec.Body.String(); strings.Contains(body, "key-secret-1") || strings.Contains(body, "client-secret-1") {
		t.Fatalf("GET from file leaks a secret: %s", body)
	}
}

func TestSettingsPutKeepsSecretsWhenOmitted(t *testing.T) {
	e := newSettingsEnv(t, true)
	rec, out := e.do("PUT", "/api/v1/settings", `{"portal_host":"prism.example.test","id_host":"id.example.test","keycloak":{"realm":"portico","admin_client_id":"portico-admin"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"12m"}}`)
	if rec.Code != 200 || out["tenants"].(map[string]any)["operation_timeout"] != "12m0s" {
		t.Fatalf("PUT: %d %v", rec.Code, out)
	}
	got, _ := e.svc.Get(context.Background())
	if got.Sites[0].APIKey != "key-secret-1" || got.Tenants.OperationTimeout != 12*time.Minute {
		t.Fatalf("stored = %+v", got)
	}
}

func TestSettingsPutValidationNamesField(t *testing.T) {
	e := newSettingsEnv(t, true)
	rec, out := e.do("PUT", "/api/v1/settings", `{"portal_host":"prism.example.test","id_host":"id.example.test","keycloak":{"realm":"portico","admin_client_id":"portico-admin"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico"},"tenants":{"project_prefix":"Bad Prefix","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"10m"}}`)
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "tenants.project_prefix") {
		t.Fatalf("PUT invalid: %d %v", rec.Code, out)
	}
}

func TestSettingsPutNeverLogsSecret(t *testing.T) {
	e := newSettingsEnv(t, false)
	rec, _ := e.do("PUT", "/api/v1/settings", `{"portal_host":"prism.example.test","id_host":"id.example.test","keycloak":{"realm":"portico","admin_client_id":"portico-admin","admin_client_secret":"adm-xyz"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico","client_secret":"cli-xyz"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"10m"}}`)
	if rec.Code != 200 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"brand-new-key-xyz", "adm-xyz", "cli-xyz"} {
		if strings.Contains(e.log.String(), secret) || strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("secret %q in the log or the answer", secret)
		}
	}
	if !strings.Contains(e.log.String(), "settings saved") {
		t.Fatalf("no save line in the log: %s", e.log.String())
	}
}

func TestSettingsCheckDoesNotStore(t *testing.T) {
	e := newSettingsEnv(t, true)
	rec, out := e.do("POST", "/api/v1/sites/site-a/check", `{"url":"`+e.pc.URL+`","tls_verify":false,"api_key":"wrong-key"}`)
	if rec.Code != 200 || out["ok"] != false || !strings.Contains(out["message"].(string), "API key") {
		t.Fatalf("check with a wrong key: %d %v", rec.Code, out)
	}
	rec, out = e.do("POST", "/api/v1/sites/site-a/check", `{"url":"`+e.pc.URL+`","tls_verify":false}`)
	if rec.Code != 200 || out["ok"] != true || len(out["clusters"].([]any)) != 1 {
		t.Fatalf("check with the stored key: %d %v", rec.Code, out)
	}
	got, _ := e.svc.Get(context.Background())
	if got.Sites[0].APIKey != "key-secret-1" {
		t.Fatal("a check changed the stored settings")
	}
	rec, out = e.do("POST", "/api/v1/settings/check", `{"target":"nope"}`)
	if rec.Code != 400 {
		t.Fatalf("unknown target: %d %v", rec.Code, out)
	}
}

func TestSettingsApplyNeedsStoredSettings(t *testing.T) {
	e := newSettingsEnv(t, false)
	rec, _ := e.do("POST", "/api/v1/settings/apply", "")
	if rec.Code != 409 || e.restarted != 0 {
		t.Fatalf("apply without stored settings: %d restarted=%d", rec.Code, e.restarted)
	}
	e2 := newSettingsEnv(t, true)
	rec, out := e2.do("POST", "/api/v1/settings/apply", "")
	if rec.Code != 202 || out["restarting"] != true || e2.restarted != 1 {
		t.Fatalf("apply: %d %v restarted=%d", rec.Code, out, e2.restarted)
	}
}

func TestSettingsCheckUsesStoredFieldsWhenOmitted(t *testing.T) {
	e := newSettingsEnv(t, true)
	// A body with the target only: every field comes from the stored document.
	rec, out := e.do("POST", "/api/v1/sites/site-a/check", `{}`)
	if rec.Code != 200 || out["ok"] != true {
		t.Fatalf("check with the stored fields: %d %v", rec.Code, out)
	}
	// Only the URL given: the stored key and TLS settings still apply.
	rec, out = e.do("POST", "/api/v1/sites/site-a/check", `{"url":"`+e.pc.URL+`"}`)
	if rec.Code != 200 || out["ok"] != true {
		t.Fatalf("check with the URL only: %d %v", rec.Code, out)
	}
}

func TestSettingsCheckRefusesStoredKeyForAnotherHost(t *testing.T) {
	e := newSettingsEnv(t, true)
	var seen []string
	other := httptest.NewServer(fakePC("never", &seen))
	defer other.Close()
	// The stored key must not travel to a host the caller names.
	rec, out := e.do("POST", "/api/v1/sites/site-a/check", `{"url":"`+other.URL+`","tls_verify":false}`)
	if rec.Code != 200 || out["ok"] != false || !strings.Contains(out["message"].(string), "API key must be given") {
		t.Fatalf("check against another host without a key: %d %v", rec.Code, out)
	}
	if len(seen) != 0 {
		t.Fatalf("the other host received %d request(s)", len(seen))
	}
	// With a key in the body the check runs against the new host with that key only.
	rec, out = e.do("POST", "/api/v1/sites/site-a/check", `{"url":"`+other.URL+`","tls_verify":false,"api_key":"given-key"}`)
	if rec.Code != 200 || out["ok"] != false || len(seen) != 1 || seen[0] != "given-key" {
		t.Fatalf("check against another host with a key: %d %v seen=%v", rec.Code, out, seen)
	}
}

func TestSettingsCheckKeepsTLSVerifyWhenOmitted(t *testing.T) {
	e := newSettingsEnv(t, false)
	tlsPC := httptest.NewTLSServer(fakePC("key-secret-1", nil))
	defer tlsPC.Close()
	s := sampleSettings(tlsPC.URL)
	s.Sites[0].TLSVerify = true
	if err := e.svc.Set(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// The flag is not in the body: the stored true applies and the self-signed certificate fails.
	rec, out := e.do("POST", "/api/v1/sites/site-a/check", `{"url":"`+tlsPC.URL+`"}`)
	if rec.Code != 200 || out["ok"] != false || !strings.Contains(out["message"].(string), "certificate") {
		t.Fatalf("check with the flag omitted: %d %v", rec.Code, out)
	}
	rec, out = e.do("POST", "/api/v1/sites/site-a/check", `{"url":"`+tlsPC.URL+`","tls_verify":false}`)
	if rec.Code != 200 || out["ok"] != true {
		t.Fatalf("check with the flag false: %d %v", rec.Code, out)
	}
	// GET still reports the stored flag.
	rec, out = e.do("GET", "/api/v1/sites/site-a", "")
	if rec.Code != 200 || out["tls_verify"] != true {
		t.Fatalf("GET after the checks: %d %v", rec.Code, out)
	}
}

func TestSettingsPutAdoptsFileSecrets(t *testing.T) {
	e := newSettingsEnv(t, false)
	rec, out := e.do("PUT", "/api/v1/settings", `{"portal_host":"prism.example.test","id_host":"id.example.test","keycloak":{"realm":"portico","admin_client_id":"portico-admin"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"10m"}}`)
	if rec.Code != 200 || out["stored"] != true || out["source"] != "store" {
		t.Fatalf("PUT on a file-based installation: %d %v", rec.Code, out)
	}
	got, err := e.svc.Get(context.Background())
	if err != nil || got.Sites[0].APIKey != "key-secret-1" || got.OIDC.ClientSecret != "client-secret-1" {
		t.Fatalf("stored = %+v, %v: the file's secrets must be adopted", got, err)
	}
}

func TestSettingsApplyRefusedWhileTenantOperationRuns(t *testing.T) {
	e := newSettingsEnv(t, true)
	e.busy = 2
	rec, out := e.do("POST", "/api/v1/settings/apply", "")
	if rec.Code != 409 || e.restarted != 0 || !strings.Contains(out["error"].(string), "2 tenant operation") {
		t.Fatalf("apply while busy: %d %v restarted=%d", rec.Code, out, e.restarted)
	}
	e.busy = 0
	rec, _ = e.do("POST", "/api/v1/settings/apply", "")
	if rec.Code != 202 || e.restarted != 1 {
		t.Fatalf("apply when idle: %d restarted=%d", rec.Code, e.restarted)
	}
}

func TestGetSettingsReportsModeAndMissing(t *testing.T) {
	e := newDraftEnv(t)
	rec, out := e.do("GET", "/api/v1/settings", "")
	if rec.Code != 200 || out["mode"] != "setup" || out["complete"] != false || out["stored"] != false {
		t.Fatalf("GET: %d %v", rec.Code, out)
	}
	missing, _ := out["missing"].([]any)
	if len(missing) != 7 || missing[0] != "portal_host" || missing[2] != "sites" {
		t.Fatalf("missing = %v", missing)
	}
	if tenants, _ := out["tenants"].(map[string]any); tenants["project_prefix"] != "portico" || tenants["operation_timeout"] != "10m0s" {
		t.Fatalf("defaults not offered: %v", out["tenants"])
	}
}

func TestPutDraftThenApplyRefusedUntilComplete(t *testing.T) {
	e := newDraftEnv(t)
	rec, out := e.do("PUT", "/api/v1/settings", `{"portal_host":"prism.example.test","id_host":"id.example.test","tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"10m"}}`)
	if rec.Code != 200 || out["stored"] != true || out["complete"] != false {
		t.Fatalf("PUT draft: %d %v", rec.Code, out)
	}
	rec, out = e.do("POST", "/api/v1/settings/apply", "")
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "settings incomplete: missing sites") || e.restarted != 0 {
		t.Fatalf("apply on a draft: %d %v restarted=%d", rec.Code, out, e.restarted)
	}
}

func TestTenantRoutesAnswer503InSetupMode(t *testing.T) {
	e := newEnv(t)
	e.h = RequireAdmin(e.st, nil, nil, "portico", nil)(New(nil, e.st, e.brand, nil, nil))
	for _, c := range []struct{ method, path string }{{"GET", "/api/v1/tenants"}, {"POST", "/api/v1/tenants"}, {"GET", "/api/v1/tenants/x/visibility"}} {
		rec, out := e.do(c.method, c.path, `{"slug":"x"}`, true)
		if rec.Code != 503 || out["error"] != "Portico is in setup mode: complete the settings and apply them" {
			t.Fatalf("%s %s: %d %v", c.method, c.path, rec.Code, out)
		}
	}
}

// failingStore refuses every write: the API must report it as its own
// failure (500), not as a bad request.
type failingStore struct{ store.Store }

func (failingStore) SetSetting(context.Context, string, []byte) error {
	return errors.New("disk full")
}

func TestSettingsPutStoreFailureIs500(t *testing.T) {
	e := newSettingsEnv(t, true)
	e.so.Service = settings.NewService(failingStore{e.st}, e.sealer)
	rec, out := e.do("PUT", "/api/v1/settings", `{"portal_host":"prism.example.test","id_host":"id.example.test","keycloak":{"realm":"portico","admin_client_id":"portico-admin"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"10m"}}`)
	if rec.Code != 500 || !strings.Contains(out["error"].(string), "disk full") {
		t.Fatalf("PUT on a broken store: %d %v", rec.Code, out)
	}
}

// On the appliance Keycloak learns its public name from Portico: Apply
// writes KC_HOSTNAME for the identity host into the file the quadlet
// loads, and a path unit restarts Keycloak (deploy/keycloak-hostname.path).
func TestApplyWritesTheKeycloakHostnameFile(t *testing.T) {
	e := newSettingsEnv(t, true)
	path := filepath.Join(t.TempDir(), "keycloak.hostname.env")
	e.so.HostnameFile = path
	rec, _ := e.do("POST", "/api/v1/settings/apply", "")
	if rec.Code != 202 || e.restarted != 1 {
		t.Fatalf("apply: %d restarted=%d", rec.Code, e.restarted)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "KC_HOSTNAME=https://id.example.test\nKC_HOSTNAME_BACKCHANNEL_DYNAMIC=true\n" {
		t.Fatalf("hostname file: %q %v", b, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %o, want 0644 (systemd reads it as root, nothing secret inside)", info.Mode().Perm())
	}
	// An unwritable path refuses the apply before the restart, with the path named.
	e2 := newSettingsEnv(t, true)
	e2.so.HostnameFile = filepath.Join(t.TempDir(), "missing-dir", "x.env")
	rec, out := e2.do("POST", "/api/v1/settings/apply", "")
	if rec.Code != 500 || e2.restarted != 0 || !strings.Contains(fmt.Sprint(out["error"]), "missing-dir") {
		t.Fatalf("apply with an unwritable hostname file: %d restarted=%d %v", rec.Code, e2.restarted, out)
	}
}

// The API accepts the old field name on a PUT and answers the new one only.
func TestSettingsAPIAcceptsTheOldField(t *testing.T) {
	e := newSettingsEnv(t, true)
	rec, out := e.do("PUT", "/api/v1/settings", `{"prism_host":"old.example.test","id_host":"id.example.test","keycloak":{"realm":"portico","admin_client_id":"portico-admin"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"10m"}}`)
	if rec.Code != 200 || out["portal_host"] != "old.example.test" {
		t.Fatalf("PUT with prism_host: %d %v", rec.Code, out)
	}
	if _, ok := out["prism_host"]; ok {
		t.Fatalf("the answer still carries prism_host: %v", out)
	}
	rec, out = e.do("GET", "/api/v1/settings", "")
	if rec.Code != 200 || out["portal_host"] != "old.example.test" {
		t.Fatalf("GET: %d %v", rec.Code, out)
	}
	rec, out = e.do("PUT", "/api/v1/settings", `{"prism_host":"a.example.test","portal_host":"b.example.test","id_host":"id.example.test","keycloak":{"realm":"portico","admin_client_id":"portico-admin"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"10m"}}`)
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "prism_host is the old name of portal_host") {
		t.Fatalf("both keys differing: %d %v", rec.Code, out)
	}
}

type kickCounter struct{ kicks int }

func (k *kickCounter) State() (string, string) { return "waiting-names", "" }
func (k *kickCounter) Kick()                   { k.kicks++ }

// The API saving new public names starts the realm job in setup mode,
// as the setup page does; in service they wait for Apply.
func TestSettingsPutNamesKickTheRealmJobInSetupMode(t *testing.T) {
	e := newSettingsEnv(t, true)
	realm := &kickCounter{}
	e.so.Realm, e.so.Mode = realm, "setup"
	body := func(portal string) string {
		return `{"portal_host":"` + portal + `","id_host":"id.example.test","keycloak":{"realm":"portico","admin_client_id":"portico-admin"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"10m"}}`
	}
	if rec, _ := e.do("PUT", "/api/v1/settings", body("prism.example.test")); rec.Code != 200 || realm.kicks != 0 {
		t.Fatalf("same names: %d kicks %d", rec.Code, realm.kicks)
	}
	if rec, _ := e.do("PUT", "/api/v1/settings", body("portal.new.test")); rec.Code != 200 || realm.kicks != 1 {
		t.Fatalf("new names in setup mode: %d kicks %d", rec.Code, realm.kicks)
	}
	e.so.Mode = "running"
	if rec, _ := e.do("PUT", "/api/v1/settings", body("portal.other.test")); rec.Code != 200 || realm.kicks != 1 {
		t.Fatalf("new names in service: %d kicks %d", rec.Code, realm.kicks)
	}
}

// The pool round-trips through the API: a GET then a PUT of its body
// leaves a custom pool as it was (write-up 36, P7).
func TestSettingsPutKeepsThePool(t *testing.T) {
	e := newSettingsEnv(t, true)
	if err := e.svc.Patch(context.Background(), func(s *settings.Settings) { s.Tenants.AddressPool = "172.20.0.0/16" }); err != nil {
		t.Fatal(err)
	}
	rec, out := e.do("GET", "/api/v1/settings", "")
	if rec.Code != 200 || out["tenants"].(map[string]any)["address_pool"] != "172.20.0.0/16" {
		t.Fatalf("GET: %d %v", rec.Code, out)
	}
	body, _ := json.Marshal(out)
	if rec, out := e.do("PUT", "/api/v1/settings", string(body)); rec.Code != 200 {
		t.Fatalf("PUT: %d %v", rec.Code, out)
	}
	got, _ := e.svc.Get(context.Background())
	if got.Tenants.AddressPool != "172.20.0.0/16" {
		t.Fatalf("stored pool = %q", got.Tenants.AddressPool)
	}
}

func TestSettingsPutRefusesPoolChangeWhileInUse(t *testing.T) {
	e := newSettingsEnv(t, true)
	ctx := context.Background()
	if err := e.st.CreateTenant(ctx, &store.Tenant{ID: "acme", DisplayName: "Acme", Status: store.StatusActive, Subnet: "10.128.0.0/24"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreatePlacement(ctx, &store.Placement{TenantID: "acme", SiteID: "site-a", Status: store.PlacementActive, SubnetExtID: "net-1", NetState: store.NetReady}); err != nil {
		t.Fatal(err)
	}
	_, out := e.do("GET", "/api/v1/settings", "")
	out["tenants"].(map[string]any)["address_pool"] = "172.21.0.0/16"
	body, _ := json.Marshal(out)
	rec, out := e.do("PUT", "/api/v1/settings", string(body))
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "ready network") {
		t.Fatalf("PUT: %d %v", rec.Code, out)
	}
	got, _ := e.svc.Get(ctx)
	if got.Tenants.AddressPool != settings.DefaultAddressPool {
		t.Fatalf("stored pool = %q", got.Tenants.AddressPool)
	}
}

func TestSettingsPutValidatesThePool(t *testing.T) {
	e := newSettingsEnv(t, true)
	_, out := e.do("GET", "/api/v1/settings", "")
	out["tenants"].(map[string]any)["address_pool"] = "8.8.0.0/16"
	body, _ := json.Marshal(out)
	rec, out := e.do("PUT", "/api/v1/settings", string(body))
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "tenants.address_pool") {
		t.Fatalf("PUT: %d %v", rec.Code, out)
	}
}
