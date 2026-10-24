package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marco-fabbri/portico/internal/settings"
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
	realmSecret, descriptor string // portico-admin secret the realm token accepts; descriptor entityID
}

func newFakeKCAdmin(t *testing.T) *fakeKCAdmin {
	f := &fakeKCAdmin{adminSecret: "kc-sec-admin-777", oidcSecret: "kc-sec-oidc-888", realms: map[string]map[string]any{}, clients: map[string]map[string]any{}, mapped: map[string][]string{}, mapperNames: map[string][]string{},
		realmSecret: "admin-secret-1", descriptor: "https://id.example.test/realms/portico"}
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
	if realm, ok := strings.CutPrefix(r.URL.Path, "/realms/"); ok {
		name, rest, _ := strings.Cut(realm, "/")
		switch rest {
		case "protocol/openid-connect/token":
			r.ParseForm()
			if _, exists := f.realms[name]; !exists || r.Form.Get("client_secret") != f.realmSecret {
				w.WriteHeader(401)
				return
			}
			f.tokens++
			fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":60}`, f.tokens)
		case "protocol/saml/descriptor":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="%s"/>`, f.descriptor)
		default:
			w.WriteHeader(404)
		}
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
		id := "cli-" + strings.NewReplacer("/", "_", ":", "_").Replace(body["clientId"].(string))
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

// fakePCAdmin is a compact Prism Central for the bootstrap route: Basic
// auth admin/pc-pw-1 on the IAM calls, the issued key on the lists.
type fakePCAdmin struct {
	srv  *httptest.Server
	URL  string
	mu   sync.Mutex
	user map[string]any
	key  string
	idps []map[string]any
}

// addIdP pre-seeds an identity provider.
func (f *fakePCAdmin) addIdP(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.idps = append(f.idps, map[string]any{"extId": "idp-" + name, "name": name})
}

func newFakePCAdmin(t *testing.T) *fakePCAdmin {
	f := &fakePCAdmin{key: "issued-key-4242"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	f.URL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePCAdmin) issuedKey() string { return f.key }

func (f *fakePCAdmin) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	user, pw, basic := r.BasicAuth()
	if r.Header.Get("X-Ntnx-Api-Key") != f.key && !(basic && user == "admin" && pw == "pc-pw-1") {
		w.WriteHeader(401)
		w.Write([]byte(`{"data":{"error":"unauthorized"}}`))
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	reply := func(code int, data any) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/authn/saml-identity-providers") && r.Method == "GET":
		out := f.idps
		if out == nil {
			out = []map[string]any{}
		}
		reply(200, out)
	case strings.HasSuffix(p, "/authn/saml-identity-providers") && r.Method == "POST":
		idp := map[string]any{"extId": "idp-" + body["name"].(string), "name": body["name"], "redirectURL": body["redirectURL"]}
		f.idps = append(f.idps, idp)
		reply(201, idp)
	case strings.HasSuffix(p, "/sp-metadata"):
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://prism.example.test/api/iam/authn"><md:SPSSODescriptor><md:AssertionConsumerService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://prism.example.test/api/iam/authn/callback" index="1"/></md:SPSSODescriptor></md:EntityDescriptor>`))
	case strings.HasSuffix(p, "/config/clusters"):
		reply(200, []map[string]any{{"extId": "cl-1", "name": "NTNX-01", "config": map[string]any{"clusterFunction": []string{"AOS"}}}})
	case strings.HasSuffix(p, "/config/storage-containers"):
		reply(200, []map[string]any{{"containerExtId": "sc-1", "name": "default"}})
	case strings.HasSuffix(p, "/authn/users") && r.Method == "GET":
		out := []map[string]any{}
		if f.user != nil {
			out = append(out, f.user)
		}
		reply(200, out)
	case strings.HasSuffix(p, "/authn/users") && r.Method == "POST":
		f.user = map[string]any{"extId": "sa-1", "username": body["username"], "userType": "SERVICE_ACCOUNT"}
		reply(201, f.user)
	case strings.HasSuffix(p, "/authn/users/sa-1/keys") && r.Method == "POST":
		reply(201, map[string]any{"extId": "key-1", "name": body["name"], "keyDetails": map[string]any{"apiKey": f.key}, "expiryTime": "2126-10-18T00:00:00Z"})
	case strings.HasSuffix(p, "/authz/authorization-policies"):
		reply(200, []map[string]any{{"extId": "pol-pm", "displayName": "Project Manager_acp"}})
	case strings.HasSuffix(p, "/authz/authorization-policies/pol-pm") && r.Method == "GET":
		w.Header().Set("ETag", `"p"`)
		reply(200, map[string]any{"extId": "pol-pm", "displayName": "Project Manager_acp", "role": "role-pm",
			"identities": []any{map[string]any{"identityFilter": map[string]any{"user": map[string]any{"uuid": map[string]any{"anyof": []any{"u-existing"}}}}}}})
	case strings.HasSuffix(p, "/authz/authorization-policies/pol-pm") && r.Method == "PUT":
		reply(200, map[string]any{"message": "updated"})
	default:
		w.WriteHeader(404)
		w.Write([]byte(`{"data":{"error":"no route"}}`))
	}
}

// putHosts stores the public names as a draft.
func (e *settingsEnv) putHosts(t *testing.T) {
	t.Helper()
	rec, out := e.do("PUT", "/api/v1/settings", `{"portal_host":"prism.example.test","id_host":"id.example.test"}`)
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
	if kc["realm"] != "msp" || kc["admin_client_secret_set"] != true || out["oidc"].(map[string]any)["client_secret_set"] != true || out["portal_host"] != "prism.example.test" {
		t.Fatalf("stored: %v", out)
	}
	rec, out = e.do("POST", "/api/v1/settings/bootstrap/keycloak", `{"admin_username":"kcadmin","admin_password":"x","realm":"msp"}`)
	if rec.Code != 200 || out["ok"] != false || out["stored"] != false {
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

func TestBootstrapPrismCentralStoresKeyAndNeverLogsPassword(t *testing.T) {
	e := newDraftEnv(t)
	e.putHosts(t)
	if rec, out := e.do("POST", "/api/v1/sites", `{"code":"SITE-A","url":"`+e.pcAdmin.URL+`","tls_verify":false}`); rec.Code != 201 || out["id"] != "site-a" {
		t.Fatalf("create site: %d %v", rec.Code, out)
	}
	rec, out := e.do("POST", "/api/v1/sites/site-a/bootstrap/prism-central", `{"username":"admin","password":"pc-pw-1"}`)
	if rec.Code != 200 || out["ok"] != true || out["stored"] != true || out["service_account"] != "portico" || out["clusters"] == nil {
		t.Fatalf("bootstrap: %d %v", rec.Code, out)
	}
	for _, secret := range []string{"pc-pw-1", e.pcAdmin.issuedKey()} {
		if strings.Contains(rec.Body.String(), secret) || strings.Contains(e.log.String(), secret) {
			t.Fatalf("%q leaked", secret)
		}
	}
	if !strings.Contains(e.log.String(), `"msg":"bootstrap prism central"`) {
		t.Fatalf("log lacks the bootstrap line: %s", e.log.String())
	}
	rec, out = e.do("GET", "/api/v1/sites/site-a", "")
	if out["api_key_set"] != true || out["url"] != e.pcAdmin.URL {
		t.Fatalf("stored: %v", out)
	}
	if _, set := e.do("GET", "/api/v1/settings", ""); set["portal_host"] != "prism.example.test" {
		t.Fatalf("the bootstrap lost the hosts: %v", set)
	}
	rec, out = e.do("POST", "/api/v1/sites/site-a/bootstrap/prism-central", `{"username":"admin","password":"wrong"}`)
	if rec.Code != 200 || out["ok"] != false || !strings.Contains(out["message"].(string), "refused the credentials") || out["stored"] != false {
		t.Fatalf("wrong password: %d %v", rec.Code, out)
	}
	rec, out = e.do("POST", "/api/v1/sites/site-a/bootstrap/prism-central", `{"username":"admin"}`)
	if rec.Code != 409 {
		t.Fatalf("no password: %d %v", rec.Code, out)
	}
	if rec, _ := e.do("POST", "/api/v1/sites/nope/bootstrap/prism-central", `{"username":"admin","password":"pc-pw-1"}`); rec.Code != 404 {
		t.Fatalf("unknown site: %d", rec.Code)
	}
}

func TestBootstrapSAMLStoresIdPName(t *testing.T) {
	e := newSettingsEnv(t, false)
	e.so.File = nil
	// A document after the two previous bootstraps, pointing at the fakes.
	doc := sampleSettings(e.pcAdmin.URL)
	doc.Sites[0].APIKey, doc.Sites[0].SAMLIdPName = settings.Secret(e.pcAdmin.issuedKey()), ""
	if err := e.svc.Set(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	e.kcAdmin.realms["portico"] = map[string]any{"realm": "portico"}
	rec, out := e.do("POST", "/api/v1/sites/site-a/bootstrap/saml", `{}`)
	if rec.Code != 200 || out["ok"] != true || out["stored"] != true || out["entity_id"] != "https://prism.example.test/api/iam/authn" || out["used_credentials"] != false || out["name"] != "portico-keycloak" {
		t.Fatalf("bootstrap: %d %v", rec.Code, out)
	}
	// The log names the identity provider actually used, also when the
	// request left the name to its default.
	if !strings.Contains(e.log.String(), `"msg":"bootstrap saml"`) || !strings.Contains(e.log.String(), `"name":"portico-keycloak"`) {
		t.Fatalf("log lacks the bootstrap line with the name: %s", e.log.String())
	}
	rec, out = e.do("GET", "/api/v1/settings", "")
	if out["complete"] != true {
		t.Fatalf("stored: %v", out)
	}
	if _, site := e.do("GET", "/api/v1/sites/site-a", ""); site["saml_idp_name"] != "portico-keycloak" || site["saml_entity_id"] != "https://prism.example.test/api/iam/authn" {
		t.Fatalf("site after the SAML bootstrap: %v", site)
	}
	e.pcAdmin.addIdP("corp-adfs")
	rec, out = e.do("POST", "/api/v1/sites/site-a/bootstrap/saml", `{"name":"portico-keycloak"}`)
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "corp-adfs") {
		t.Fatalf("second IdP: %d %v", rec.Code, out)
	}
}
