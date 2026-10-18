package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeKCAdmin is a compact Keycloak for the bootstrap routes: master
// token for kcadmin/adm-pw-1, realm creation, clients with fixed
// secrets, service accounts, role mappings and protocol mappers.
type fakeKCAdmin struct {
	srv                     *httptest.Server
	adminSecret, oidcSecret string
	mu                      sync.Mutex
	realms                  map[string]map[string]any
	clients                 map[string]map[string]any
	mapped, mapperNames     map[string][]string
	tokens                  int
}

func newFakeKCAdmin(t *testing.T) *fakeKCAdmin {
	f := &fakeKCAdmin{adminSecret: "kc-sec-admin-777", oidcSecret: "kc-sec-oidc-888", realms: map[string]map[string]any{}, clients: map[string]map[string]any{}, mapped: map[string][]string{}, mapperNames: map[string][]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKCAdmin) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/realms/master/protocol/openid-connect/token" {
		r.ParseForm()
		if r.Form.Get("grant_type") != "password" || r.Form.Get("username") != "kcadmin" || r.Form.Get("password") != "adm-pw-1" {
			w.WriteHeader(401)
			return
		}
		f.tokens++
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":60}`, f.tokens)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
		w.WriteHeader(401)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	if r.URL.Path == "/admin/realms" && r.Method == "POST" {
		f.realms[body["realm"].(string)] = body
		f.clients["cli-realm-management"] = map[string]any{"id": "cli-realm-management", "clientId": "realm-management"}
		w.WriteHeader(201)
		return
	}
	realm, p, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/admin/realms/"), "/")
	if _, ok := f.realms[realm]; !ok {
		w.WriteHeader(404)
		return
	}
	parts := strings.Split(p, "/")
	switch {
	case p == "":
		json.NewEncoder(w).Encode(f.realms[realm])
	case p == "clients" && r.Method == "GET":
		out := []map[string]any{}
		for _, c := range f.clients {
			if c["clientId"] == r.URL.Query().Get("clientId") {
				out = append(out, c)
			}
		}
		json.NewEncoder(w).Encode(out)
	case p == "clients" && r.Method == "POST":
		id := "cli-" + body["clientId"].(string)
		body["id"] = id
		f.clients[id] = body
		w.Header().Set("Location", f.srv.URL+r.URL.Path+"/"+id)
		w.WriteHeader(201)
	case len(parts) == 2 && parts[0] == "clients" && r.Method == "PUT":
		body["id"] = parts[1]
		f.clients[parts[1]] = body
		w.WriteHeader(204)
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "client-secret":
		secret := f.oidcSecret
		if parts[1] == "cli-portico-admin" {
			secret = f.adminSecret
		}
		json.NewEncoder(w).Encode(map[string]any{"value": secret})
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "service-account-user":
		json.NewEncoder(w).Encode(map[string]any{"id": "sa-" + parts[1]})
	case len(parts) == 4 && parts[0] == "clients" && parts[2] == "roles":
		json.NewEncoder(w).Encode(map[string]any{"id": "role-" + parts[3], "name": parts[3]})
	case len(parts) == 4 && parts[0] == "clients" && parts[2] == "protocol-mappers":
		if r.Method == "POST" {
			f.mapperNames[parts[1]] = append(f.mapperNames[parts[1]], body["name"].(string))
			w.WriteHeader(201)
			return
		}
		out := []map[string]any{}
		for _, n := range f.mapperNames[parts[1]] {
			out = append(out, map[string]any{"name": n})
		}
		json.NewEncoder(w).Encode(out)
	case len(parts) == 5 && parts[0] == "users" && parts[2] == "role-mappings":
		key := parts[1] + "/" + parts[4]
		if r.Method == "POST" {
			var reps []map[string]any
			json.Unmarshal(raw, &reps)
			for _, rep := range reps {
				f.mapped[key] = append(f.mapped[key], rep["name"].(string))
			}
			w.WriteHeader(204)
			return
		}
		out := []map[string]any{}
		for _, n := range f.mapped[key] {
			out = append(out, map[string]any{"id": "role-" + n, "name": n})
		}
		json.NewEncoder(w).Encode(out)
	default:
		w.WriteHeader(404)
	}
}

// putHosts stores the public names as a draft.
func (e *settingsEnv) putHosts(t *testing.T) {
	t.Helper()
	rec, out := e.do("PUT", "/api/v1/settings", `{"prism_host":"prism.example.test","id_host":"id.example.test"}`)
	if rec.Code != 200 || out["stored"] != true {
		t.Fatalf("PUT hosts: %d %v", rec.Code, out)
	}
}

func TestBootstrapKeycloakStoresAndHidesSecrets(t *testing.T) {
	e := newDraftEnv(t)
	e.putHosts(t)
	rec, out := e.do("POST", "/api/v1/settings/bootstrap/keycloak", `{"admin_username":"kcadmin","admin_password":"adm-pw-1","realm":"msp"}`)
	if rec.Code != 200 || out["ok"] != true || out["stored"] != true || out["issuer"] != "https://id.example.test/realms/msp" {
		t.Fatalf("bootstrap: %d %v", rec.Code, out)
	}
	for _, secret := range []string{"adm-pw-1", e.kcAdmin.adminSecret, e.kcAdmin.oidcSecret} {
		if strings.Contains(rec.Body.String(), secret) || strings.Contains(e.log.String(), secret) {
			t.Fatalf("secret %q leaked in the body or the log", secret)
		}
	}
	if !strings.Contains(e.log.String(), `"msg":"bootstrap keycloak"`) {
		t.Fatalf("log lacks the bootstrap line: %s", e.log.String())
	}
	rec, out = e.do("GET", "/api/v1/settings", "")
	kc, _ := out["keycloak"].(map[string]any)
	if kc["realm"] != "msp" || kc["admin_client_secret_set"] != true || out["oidc"].(map[string]any)["client_secret_set"] != true || out["prism_host"] != "prism.example.test" {
		t.Fatalf("stored: %v", out)
	}
	rec, out = e.do("POST", "/api/v1/settings/bootstrap/keycloak", `{"admin_username":"kcadmin","admin_password":"x","realm":"msp"}`)
	if rec.Code != 200 || out["ok"] != false || out["stored"] != nil {
		t.Fatalf("refused bootstrap: %d %v", rec.Code, out)
	}
	rec, out = e.do("POST", "/api/v1/settings/bootstrap/keycloak", `{"admin_username":"kcadmin","admin_password":"adm-pw-1","realm":"msp","bogus":1}`)
	if rec.Code != 400 {
		t.Fatalf("malformed body: %d %v", rec.Code, out)
	}
}

func TestBootstrapKeycloakNeedsHosts(t *testing.T) {
	e := newDraftEnv(t)
	rec, out := e.do("POST", "/api/v1/settings/bootstrap/keycloak", `{"admin_username":"kcadmin","admin_password":"adm-pw-1","realm":"msp"}`)
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "public names") {
		t.Fatalf("%d %v", rec.Code, out)
	}
}
