package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/branding"
	"github.com/marco-fabbri/portico/internal/secrets"
	"github.com/marco-fabbri/portico/internal/settings"
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
	pc        *httptest.Server
}

func sampleSettings(pcURL string) settings.Settings {
	var s settings.Settings
	s.PrismHost, s.IDHost = "prism.example.test", "id.example.test"
	s.PrismCentral.URL, s.PrismCentral.TLSVerify = pcURL, false
	s.PrismCentral.APIKey, s.PrismCentral.SAMLIdPName = "key-secret-1", "lab-idp"
	s.Keycloak.Realm, s.Keycloak.AdminClientID, s.Keycloak.AdminClientSecret = "portico", "portico-admin", "admin-secret-1"
	s.OIDC.Issuer, s.OIDC.ClientID, s.OIDC.ClientSecret = "https://id.example.test/realms/portico", "portico", "client-secret-1"
	s.Tenants.ProjectPrefix, s.Tenants.GroupPrefix, s.Tenants.Role = "portico", "tenant-", "Project Admin"
	s.Tenants.ClusterExtID, s.Tenants.StorageContainerExtID = "cl-1", "sc-1"
	s.Tenants.OperationTimeout = 10 * time.Minute
	return s
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
	pc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Ntnx-Api-Key") != "key-secret-1" {
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
	}))
	t.Cleanup(pc.Close)
	e := &settingsEnv{svc: svc, log: &bytes.Buffer{}, pc: pc}
	if stored {
		if err := svc.Set(context.Background(), sampleSettings(pc.URL)); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewJSONHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tsvc := tenant.NewService(st, tenanttest.NewPC("lab-idp"), tenanttest.NewKC(), tenant.Config{SAMLIdPName: "lab-idp", ClusterExtID: "cl", StorageContainerExtID: "sc"}, log)
	file := sampleSettings(pc.URL)
	so := &SettingsOptions{Service: svc, File: &file, Checker: settings.NewChecker(log), KeycloakURL: "http://127.0.0.1:1", Restart: func() { e.restarted++ }}
	e.h = BasicAuth(st, "portico", log)(New(tsvc, st, branding.NewService(st, log), so, log))
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
	pc := out["prism_central"].(map[string]any)
	if pc["api_key_set"] != true || pc["url"] != e.pc.URL {
		t.Fatalf("prism_central = %v", pc)
	}
	body := rec.Body.String()
	for _, secret := range []string{"key-secret-1", "admin-secret-1", "client-secret-1", "v1:"} {
		if strings.Contains(body, secret) {
			t.Fatalf("GET body contains %q", secret)
		}
	}
	e2 := newSettingsEnv(t, false)
	rec, out = e2.do("GET", "/api/v1/settings", "")
	if rec.Code != 200 || out["stored"] != false || out["source"] != "file" || out["prism_central"].(map[string]any)["api_key_set"] != true {
		t.Fatalf("GET from file: %d %v", rec.Code, out)
	}
	if body := rec.Body.String(); strings.Contains(body, "key-secret-1") || strings.Contains(body, "client-secret-1") {
		t.Fatalf("GET from file leaks a secret: %s", body)
	}
}

func TestSettingsPutKeepsSecretsWhenOmitted(t *testing.T) {
	e := newSettingsEnv(t, true)
	rec, out := e.do("PUT", "/api/v1/settings", `{"prism_host":"prism.example.test","id_host":"id.example.test","prism_central":{"url":"`+e.pc.URL+`","tls_verify":false,"saml_idp_name":"lab-idp"},"keycloak":{"realm":"portico","admin_client_id":"portico-admin"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","cluster_ext_id":"cl-1","storage_container_ext_id":"sc-1","operation_timeout":"12m"}}`)
	if rec.Code != 200 || out["prism_central"].(map[string]any)["api_key_set"] != true || out["tenants"].(map[string]any)["operation_timeout"] != "12m0s" {
		t.Fatalf("PUT: %d %v", rec.Code, out)
	}
	got, _ := e.svc.Get(context.Background())
	if got.PrismCentral.APIKey != "key-secret-1" || got.Tenants.OperationTimeout != 12*time.Minute {
		t.Fatalf("stored = %+v", got)
	}
}

func TestSettingsPutValidationNamesField(t *testing.T) {
	e := newSettingsEnv(t, true)
	rec, out := e.do("PUT", "/api/v1/settings", `{"prism_host":"prism.example.test","id_host":"id.example.test","prism_central":{"url":"`+e.pc.URL+`","saml_idp_name":"lab-idp"},"keycloak":{"realm":"portico","admin_client_id":"portico-admin"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico"},"tenants":{"project_prefix":"Bad Prefix","group_prefix":"tenant-","role":"Project Admin","cluster_ext_id":"cl-1","storage_container_ext_id":"sc-1","operation_timeout":"10m"}}`)
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "tenants.project_prefix") {
		t.Fatalf("PUT invalid: %d %v", rec.Code, out)
	}
}

func TestSettingsPutNeverLogsSecret(t *testing.T) {
	e := newSettingsEnv(t, false)
	rec, _ := e.do("PUT", "/api/v1/settings", `{"prism_host":"prism.example.test","id_host":"id.example.test","prism_central":{"url":"`+e.pc.URL+`","saml_idp_name":"lab-idp","api_key":"brand-new-key-xyz"},"keycloak":{"realm":"portico","admin_client_id":"portico-admin","admin_client_secret":"adm-xyz"},"oidc":{"issuer":"https://id.example.test/realms/portico","client_id":"portico","client_secret":"cli-xyz"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","cluster_ext_id":"cl-1","storage_container_ext_id":"sc-1","operation_timeout":"10m"}}`)
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
	rec, out := e.do("POST", "/api/v1/settings/check", `{"target":"prism_central","prism_central":{"url":"`+e.pc.URL+`","tls_verify":false,"api_key":"wrong-key"}}`)
	if rec.Code != 200 || out["ok"] != false || !strings.Contains(out["message"].(string), "API key") {
		t.Fatalf("check with a wrong key: %d %v", rec.Code, out)
	}
	rec, out = e.do("POST", "/api/v1/settings/check", `{"target":"prism_central","prism_central":{"url":"`+e.pc.URL+`","tls_verify":false}}`)
	if rec.Code != 200 || out["ok"] != true || len(out["clusters"].([]any)) != 1 {
		t.Fatalf("check with the stored key: %d %v", rec.Code, out)
	}
	got, _ := e.svc.Get(context.Background())
	if got.PrismCentral.APIKey != "key-secret-1" {
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
