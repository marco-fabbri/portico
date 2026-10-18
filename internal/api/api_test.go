package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/branding"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
	"github.com/marco-fabbri/portico/internal/tenant/tenanttest"
	"golang.org/x/crypto/bcrypt"
)

const adminPassword = "correct-horse-7"

type env struct {
	h     http.Handler
	svc   *tenant.Service
	st    *store.SQLite
	pc    *tenanttest.PC
	kc    *tenanttest.KC
	brand *branding.Service
	log   *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "portico.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, _ := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.MinCost)
	if err := st.SetAdminPassword(context.Background(), "admin", string(hash)); err != nil {
		t.Fatal(err)
	}
	pc := tenanttest.NewPC("lab-idp")
	kc := tenanttest.NewKC()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := tenant.NewService(st, pc, kc, tenant.Config{SAMLIdPName: "lab-idp", ClusterExtID: "cl", StorageContainerExtID: "sc", OperationTimeout: 5 * time.Second}, log)
	brand := branding.NewService(st, log)
	h := BasicAuth(st, "portico", log)(New(svc, st, brand, nil, log))
	return &env{h: h, svc: svc, st: st, pc: pc, kc: kc, brand: brand, log: &buf}
}

func (e *env) do(method, path, body string, auth bool) (*httptest.ResponseRecorder, map[string]any) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "https://127.0.0.1:8443"+path, rd)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if auth {
		r.SetBasicAuth("admin", adminPassword)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestUnauthenticatedIs401WithChallenge(t *testing.T) {
	e := newEnv(t)
	rec, out := e.do("GET", "/api/v1/tenants", "", false)
	if rec.Code != 401 || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), `Basic realm="portico"`) || out["error"] != "unauthorized" {
		t.Fatalf("no credentials: %d %q %v", rec.Code, rec.Header().Get("WWW-Authenticate"), out)
	}
	r := httptest.NewRequest("GET", "https://127.0.0.1:8443/api/v1/tenants", nil)
	r.SetBasicAuth("admin", "wrong")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	if rec.Code != 401 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	r = httptest.NewRequest("GET", "https://127.0.0.1:8443/api/v1/tenants", nil)
	r.SetBasicAuth("root", adminPassword)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	if rec.Code != 401 {
		t.Fatalf("wrong user: %d", rec.Code)
	}
}

func TestCreateTenantReturns202AndRow(t *testing.T) {
	e := newEnv(t)
	rec, out := e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME","contacts":"ops@acme.test"}`, true)
	if rec.Code != 202 || out["status"] != "creating" || out["id"] != "acme" || out["project_id"] != "portico-acme" {
		t.Fatalf("POST: %d %v", rec.Code, out)
	}
	e.svc.Wait()
	rec, out = e.do("GET", "/api/v1/tenants/acme", "", true)
	if rec.Code != 200 || out["status"] != "active" || out["display_name"] != "ACME" || out["idp_shared"] != true {
		t.Fatalf("GET: %d %v", rec.Code, out)
	}
	rec, _ = e.do("GET", "/api/v1/tenants/acme/steps", "", true)
	var steps []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &steps)
	if rec.Code != 200 || len(steps) != 7 {
		t.Fatalf("steps: %d %v", rec.Code, steps)
	}
	for _, s := range steps {
		if s["ok"] != true || s["name"] == "" || s["at"] == "" {
			t.Fatalf("step = %v", s)
		}
	}
	rec, _ = e.do("GET", "/api/v1/tenants", "", true)
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list) != 1 || list[0]["id"] != "acme" {
		t.Fatalf("list: %d %v", rec.Code, list)
	}
	rec, out = e.do("GET", "/api/v1/tenants/acme/visibility", "", true)
	if rec.Code != 200 || out["ok"] != true {
		t.Fatalf("visibility: %d %v", rec.Code, out)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestInvalidSlugIs400WithNoBackendCalls(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{`{"slug":"Acme"}`, `{"slug":"a b"}`, `{"slug":""}`, `{"slug":"` + strings.Repeat("a", 40) + `"}`, `{"slug":"-a"}`, `not json`} {
		rec, out := e.do("POST", "/api/v1/tenants", body, true)
		if rec.Code != 400 || out["error"] == "" {
			t.Fatalf("%s: %d %v", body, rec.Code, out)
		}
	}
	e.svc.Wait()
	if e.pc.Calls != 0 || e.kc.Calls != 0 {
		t.Fatalf("backend calls: pc=%d kc=%d", e.pc.Calls, e.kc.Calls)
	}
}

func TestDuplicateIs409(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME"}`, true)
	e.svc.Wait()
	rec, out := e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME"}`, true)
	if rec.Code != 409 || out["error"] == "" {
		t.Fatalf("duplicate: %d %v", rec.Code, out)
	}
	rec, _ = e.do("POST", "/api/v1/tenants/acme/provision", "", true)
	if rec.Code != 409 {
		t.Fatalf("provision on active: %d", rec.Code)
	}
	rec, _ = e.do("GET", "/api/v1/tenants/nope", "", true)
	if rec.Code != 404 {
		t.Fatalf("unknown tenant: %d", rec.Code)
	}
}

func TestBusyIs409(t *testing.T) {
	e := newEnv(t)
	e.pc.GateCreate = tenanttest.NewGate()
	rec, _ := e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME"}`, true)
	if rec.Code != 202 {
		t.Fatalf("POST: %d", rec.Code)
	}
	select {
	case <-e.pc.GateCreate.Started:
	case <-time.After(2 * time.Second):
		t.Fatal("provisioning did not start")
	}
	rec, out := e.do("DELETE", "/api/v1/tenants/acme?delete_project=true", "", true)
	if rec.Code != 409 || out["error"] == "" {
		t.Fatalf("DELETE while creating: %d %v", rec.Code, out)
	}
	close(e.pc.GateCreate.Release)
	e.svc.Wait()
	rec, _ = e.do("DELETE", "/api/v1/tenants/acme?delete_project=true", "", true)
	if rec.Code != 202 {
		t.Fatalf("DELETE: %d", rec.Code)
	}
	e.svc.Wait()
	rec, _ = e.do("GET", "/api/v1/tenants/acme", "", true)
	if rec.Code != 404 {
		t.Fatalf("after delete: %d", rec.Code)
	}
}

func TestCreateUserReturnsPasswordOnce(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME"}`, true)
	e.svc.Wait()
	rec, out := e.do("POST", "/api/v1/tenants/acme/users", `{"email":"carol@acme.test","first_name":"Carol","last_name":"Lab"}`, true)
	pw, _ := out["temporary_password"].(string)
	if rec.Code != 201 || len(pw) != 20 || out["email"] != "carol@acme.test" || out["id"] == "" {
		t.Fatalf("create user: %d %v", rec.Code, out)
	}
	rec, _ = e.do("GET", "/api/v1/tenants/acme", "", true)
	steps, _ := e.do("GET", "/api/v1/tenants/acme/steps", "", true)
	if strings.Contains(rec.Body.String(), pw) || strings.Contains(steps.Body.String(), pw) || strings.Contains(e.log.String(), pw) {
		t.Fatal("the temporary password is visible again")
	}
	rec, _ = e.do("POST", "/api/v1/tenants/acme/users", `{"email":"carol@acme.test"}`, true)
	if rec.Code != 409 {
		t.Fatalf("duplicate user: %d", rec.Code)
	}
	rec, _ = e.do("POST", "/api/v1/tenants/acme/users", `{"email":""}`, true)
	if rec.Code != 400 {
		t.Fatalf("empty email: %d", rec.Code)
	}
	rec, out = e.do("POST", "/api/v1/tenants/acme/disable", "", true)
	if rec.Code != 200 || out["status"] != "disabled" {
		t.Fatalf("disable: %d %v", rec.Code, out)
	}
	rec, _ = e.do("POST", "/api/v1/tenants/acme/users", `{"email":"dave@acme.test"}`, true)
	if rec.Code != 409 {
		t.Fatalf("user on disabled tenant: %d", rec.Code)
	}
	rec, out = e.do("POST", "/api/v1/tenants/acme/enable", "", true)
	if rec.Code != 200 || out["status"] != "active" {
		t.Fatalf("enable: %d %v", rec.Code, out)
	}
}

func TestPasswordNeverInLog(t *testing.T) {
	e := newEnv(t)
	r := httptest.NewRequest("GET", "https://127.0.0.1:8443/api/v1/tenants", nil)
	r.SetBasicAuth("admin", "hunter2-wrong")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	if rec.Code != 401 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	if strings.Contains(e.log.String(), "hunter2") || strings.Contains(e.log.String(), adminPassword) {
		t.Fatal("a password reached the log")
	}
}

func TestUnknownPathIs404JSON(t *testing.T) {
	e := newEnv(t)
	rec, out := e.do("GET", "/api/v1/nope", "", true)
	if rec.Code != 404 || out["error"] != "not found" {
		t.Fatalf("unknown path: %d %v", rec.Code, out)
	}
	rec, out = e.do("PATCH", "/api/v1/tenants", "", true)
	if rec.Code != 405 && rec.Code != 404 {
		t.Fatalf("PATCH: %d %v", rec.Code, out)
	}
}

// 2a review minor: delete_project is parsed as a boolean, garbage is 400.
func TestDeleteProjectFlagParsing(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME"}`, true)
	e.svc.Wait()
	rec, out := e.do("DELETE", "/api/v1/tenants/acme?delete_project=yes", "", true)
	if rec.Code != 400 || out["error"] == "" {
		t.Fatalf("delete_project=yes: %d %v", rec.Code, out)
	}
	rec, _ = e.do("DELETE", "/api/v1/tenants/acme?delete_project=1", "", true)
	if rec.Code != 202 {
		t.Fatalf("delete_project=1: %d", rec.Code)
	}
	e.svc.Wait()
	for _, p := range e.pc.Projects {
		if p.Name == "portico-acme" {
			t.Fatal("project kept although delete_project=1")
		}
	}
}

const pngBytes = "\x89PNG\r\n\x1a\n"

func TestBrandingGetDefault(t *testing.T) {
	e := newEnv(t)
	rec, out := e.do("GET", "/api/v1/settings/branding", "", true)
	if rec.Code != 200 || out["msp_name"] != "Portico" || out["accent_color"] != "#1f5f8b" {
		t.Fatalf("GET branding: %d %v", rec.Code, out)
	}
	rec, _ = e.do("GET", "/api/v1/settings/branding/logo", "", true)
	if rec.Code != 404 {
		t.Fatalf("logo without one: %d", rec.Code)
	}
}

func TestBrandingPutValidatesAndStores(t *testing.T) {
	e := newEnv(t)
	rec, out := e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"ACME","accent_color":"blue"}`, true)
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "accent_color") {
		t.Fatalf("bad colour: %d %v", rec.Code, out)
	}
	logo := base64.StdEncoding.EncodeToString([]byte(pngBytes))
	rec, out = e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"ACME Cloud","tagline":"Your VMs","accent_color":"#8b1f5f","logo_type":"image/png","logo_data":"`+logo+`","min_display_ms":500}`, true)
	if rec.Code != 200 || out["msp_name"] != "ACME Cloud" || out["logo_type"] != "image/png" || out["warning"] != nil {
		t.Fatalf("PUT: %d %v", rec.Code, out)
	}
	rec, out = e.do("GET", "/api/v1/settings/branding", "", true)
	if rec.Code != 200 || out["accent_color"] != "#8b1f5f" || out["min_display_ms"] != float64(500) {
		t.Fatalf("GET after PUT: %d %v", rec.Code, out)
	}
	rec, _ = e.do("GET", "/api/v1/settings/branding/logo", "", true)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Body.String() != pngBytes {
		t.Fatalf("logo: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Content-Security-Policy") != "default-src 'none'" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("logo headers: %v", rec.Header())
	}
}

func TestBrandingLogoSVGHeaders(t *testing.T) {
	e := newEnv(t)
	active := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	rec, out := e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"ACME","accent_color":"#000000","logo_type":"image/svg+xml","logo_data":"`+active+`"}`, true)
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "logo") {
		t.Fatalf("PUT active svg: %d %v", rec.Code, out)
	}
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>`))
	rec, _ = e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"ACME","accent_color":"#000000","logo_type":"image/svg+xml","logo_data":"`+svg+`"}`, true)
	if rec.Code != 200 {
		t.Fatalf("PUT svg: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = e.do("GET", "/api/v1/settings/branding/logo", "", true)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" || rec.Header().Get("Content-Security-Policy") != "default-src 'none'" {
		t.Fatalf("svg logo: %d %v", rec.Code, rec.Header())
	}
}

func TestBrandingPutWarningWhenApplyFails(t *testing.T) {
	e := newEnv(t)
	e.brand.OnChange(func(ctx context.Context, b branding.Branding) error { return errors.New("keycloak realm: status 500") })
	rec, out := e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"ACME","accent_color":"#000000"}`, true)
	if rec.Code != 200 || out["msp_name"] != "ACME" || !strings.Contains(out["warning"].(string), "keycloak realm") {
		t.Fatalf("PUT with a failing hook: %d %v", rec.Code, out)
	}
}
