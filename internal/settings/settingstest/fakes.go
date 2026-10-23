// Package settingstest holds httptest doubles of the two systems the
// bootstrap talks to, for the tests of the packages above settings.
package settingstest

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

// KC is a Keycloak double: master token for kcadmin/AdminPassword,
// realm token for portico-admin/RealmSecret, realm creation, clients
// with fixed secrets, service accounts, role mappings, protocol mappers
// and the SAML descriptor naming Descriptor.
type KC struct {
	Srv                                    *httptest.Server
	AdminPassword, RealmSecret, Descriptor string
	ClientSecret                           string // every client secret
	mu                                     sync.Mutex
	Realms                                 map[string]map[string]any
	Clients                                map[string]map[string]any
	Mapped, MapperNames                    map[string][]string
	tokens                                 int
}

// NewKC starts a Keycloak double.
func NewKC(t *testing.T) *KC {
	f := &KC{AdminPassword: "adm-pw-1", RealmSecret: "admin-secret-1", Descriptor: "https://id.example.test/realms/msp", ClientSecret: "kc-sec-9x",
		Realms: map[string]map[string]any{}, Clients: map[string]map[string]any{}, Mapped: map[string][]string{}, MapperNames: map[string][]string{}}
	f.Srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Srv.Close)
	return f
}

func (f *KC) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if realm, ok := strings.CutPrefix(r.URL.Path, "/realms/"); ok {
		name, rest, _ := strings.Cut(realm, "/")
		r.ParseForm()
		switch {
		case rest == "protocol/openid-connect/token" && name == "master":
			if r.Form.Get("grant_type") != "password" || r.Form.Get("username") != "kcadmin" || r.Form.Get("password") != f.AdminPassword {
				w.WriteHeader(401)
				return
			}
		case rest == "protocol/openid-connect/token":
			if _, exists := f.Realms[name]; !exists || r.Form.Get("client_secret") != f.RealmSecret {
				w.WriteHeader(401)
				return
			}
		case rest == "protocol/saml/descriptor":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="%s"/>`, f.Descriptor)
			return
		default:
			w.WriteHeader(404)
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
		f.Realms[body["realm"].(string)] = body
		f.Clients["cli-realm-management"] = map[string]any{"id": "cli-realm-management", "clientId": "realm-management"}
		w.WriteHeader(201)
		return
	}
	realm, p, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/admin/realms/"), "/")
	if _, ok := f.Realms[realm]; !ok {
		w.WriteHeader(404)
		return
	}
	parts := strings.Split(p, "/")
	switch {
	case p == "":
		json.NewEncoder(w).Encode(f.Realms[realm])
	case p == "clients" && r.Method == "GET":
		out := []map[string]any{}
		for _, c := range f.Clients {
			if c["clientId"] == r.URL.Query().Get("clientId") {
				out = append(out, c)
			}
		}
		json.NewEncoder(w).Encode(out)
	case p == "clients" && r.Method == "POST":
		id := "cli-" + strings.NewReplacer("/", "_", ":", "_").Replace(body["clientId"].(string))
		body["id"] = id
		f.Clients[id] = body
		w.Header().Set("Location", f.Srv.URL+r.URL.Path+"/"+id)
		w.WriteHeader(201)
	case len(parts) == 2 && parts[0] == "clients" && r.Method == "PUT":
		body["id"] = parts[1]
		f.Clients[parts[1]] = body
		w.WriteHeader(204)
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "client-secret":
		json.NewEncoder(w).Encode(map[string]any{"value": f.ClientSecret})
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "service-account-user":
		json.NewEncoder(w).Encode(map[string]any{"id": "sa-" + parts[1]})
	case len(parts) == 4 && parts[0] == "clients" && parts[2] == "roles":
		json.NewEncoder(w).Encode(map[string]any{"id": "role-" + parts[3], "name": parts[3]})
	case len(parts) == 4 && parts[0] == "clients" && parts[2] == "protocol-mappers":
		if r.Method == "POST" {
			f.MapperNames[parts[1]] = append(f.MapperNames[parts[1]], body["name"].(string))
			w.WriteHeader(201)
			return
		}
		out := []map[string]any{}
		for _, n := range f.MapperNames[parts[1]] {
			out = append(out, map[string]any{"name": n})
		}
		json.NewEncoder(w).Encode(out)
	case len(parts) == 5 && parts[0] == "users" && parts[2] == "role-mappings":
		key := parts[1] + "/" + parts[4]
		if r.Method == "POST" {
			var reps []map[string]any
			json.Unmarshal(raw, &reps)
			for _, rep := range reps {
				f.Mapped[key] = append(f.Mapped[key], rep["name"].(string))
			}
			w.WriteHeader(204)
			return
		}
		out := []map[string]any{}
		for _, n := range f.Mapped[key] {
			out = append(out, map[string]any{"id": "role-" + n, "name": n})
		}
		json.NewEncoder(w).Encode(out)
	default:
		w.WriteHeader(404)
	}
}

// PC is a Prism Central double: Basic auth admin/Password on every
// call, the issued Key on the lists; service account, key, policy and
// SAML identity providers.
type PC struct {
	Srv           *httptest.Server
	Password, Key string
	mu            sync.Mutex
	User          map[string]any
	IdPs          []map[string]any
	Posts         []string
}

// NewPC starts a Prism Central double.
func NewPC(t *testing.T) *PC {
	f := &PC{Password: "pc-pw-1", Key: "issued-key-4242"}
	f.Srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Srv.Close)
	return f
}

// AddIdP pre-seeds an identity provider.
func (f *PC) AddIdP(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.IdPs = append(f.IdPs, map[string]any{"extId": "idp-" + name, "name": name})
}

func (f *PC) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	user, pw, basic := r.BasicAuth()
	if r.Header.Get("X-Ntnx-Api-Key") != f.Key && !(basic && user == "admin" && pw == f.Password) {
		w.WriteHeader(401)
		w.Write([]byte(`{"data":{"error":"unauthorized"}}`))
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	if r.Method == "POST" {
		f.Posts = append(f.Posts, r.URL.Path)
	}
	reply := func(code int, data any) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/authn/saml-identity-providers") && r.Method == "GET":
		out := f.IdPs
		if out == nil {
			out = []map[string]any{}
		}
		reply(200, out)
	case strings.HasSuffix(p, "/authn/saml-identity-providers") && r.Method == "POST":
		idp := map[string]any{"extId": "idp-" + body["name"].(string), "name": body["name"], "redirectURL": body["redirectURL"]}
		f.IdPs = append(f.IdPs, idp)
		reply(201, idp)
	case strings.Contains(p, "/authn/saml-identity-providers/") && !strings.HasSuffix(p, "/sp-metadata") && r.Method == "GET":
		id := p[strings.LastIndex(p, "/")+1:]
		for _, idp := range f.IdPs {
			if idp["extId"] == id {
				reply(200, idp)
				return
			}
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"data":{"error":"no such identity provider"}}`))
	case strings.HasSuffix(p, "/sp-metadata"):
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://prism.example.test/api/iam/authn"><md:SPSSODescriptor><md:AssertionConsumerService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://prism.example.test/api/iam/authn/callback" index="1"/></md:SPSSODescriptor></md:EntityDescriptor>`))
	case strings.HasSuffix(p, "/config/clusters"):
		reply(200, []map[string]any{{"extId": "cl-1", "name": "NTNX-01", "config": map[string]any{"clusterFunction": []string{"AOS"}}}})
	case strings.HasSuffix(p, "/config/storage-containers"):
		reply(200, []map[string]any{{"containerExtId": "sc-1", "name": "default"}})
	case strings.HasSuffix(p, "/authn/users") && r.Method == "GET":
		out := []map[string]any{}
		if f.User != nil {
			out = append(out, f.User)
		}
		reply(200, out)
	case strings.HasSuffix(p, "/authn/users") && r.Method == "POST":
		f.User = map[string]any{"extId": "sa-1", "username": body["username"], "userType": "SERVICE_ACCOUNT"}
		reply(201, f.User)
	case strings.HasSuffix(p, "/authn/users/sa-1/keys") && r.Method == "POST":
		reply(201, map[string]any{"extId": "key-1", "name": body["name"], "keyDetails": map[string]any{"apiKey": f.Key}, "expiryTime": "2126-10-18T00:00:00Z"})
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
