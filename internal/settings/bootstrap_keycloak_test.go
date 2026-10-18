package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeKCAdmin is an httptest Keycloak for the bootstrap: the master
// token (password grant for kcadmin/adm-pw-1), realm creation, clients,
// secrets, service accounts and realm-management role mappings.
type fakeKCAdmin struct {
	srv       *httptest.Server
	mu        sync.Mutex
	realmReps map[string]map[string]any
	clients   map[string]map[string]any // uuid → representation
	mapped    map[string][]string       // userID/clientUUID → roles
	mappers   map[string][]map[string]any
	tokens    int
	// descriptorEntityID is what the realm's SAML descriptor names;
	// adminSecret is the portico-admin client secret the realm token
	// endpoint accepts.
	descriptorEntityID, adminSecret string
}

func newFakeKCAdmin(t *testing.T) *fakeKCAdmin {
	f := &fakeKCAdmin{realmReps: map[string]map[string]any{}, clients: map[string]map[string]any{}, mapped: map[string][]string{}, mappers: map[string][]map[string]any{},
		descriptorEntityID: "https://id.example.test/realms/msp", adminSecret: "secret-of-portico-admin"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// clientUUID is the fake's internal id for a clientId: real ids are
// UUIDs, so a clientId that is a URL must not leak slashes into paths.
func clientUUID(clientID string) string {
	return "cli-" + strings.NewReplacer("/", "_", ":", "_").Replace(clientID)
}

func (f *fakeKCAdmin) client(clientID string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.clients {
		if c["clientId"] == clientID {
			return c
		}
	}
	return nil
}

func (f *fakeKCAdmin) mappedRoles(clientID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mapped["sa-"+clientID+"/cli-realm-management"]
}

func (f *fakeKCAdmin) realms() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.realmReps)
}

func (f *fakeKCAdmin) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/realms/master/protocol/openid-connect/token" {
		r.ParseForm()
		if r.Form.Get("grant_type") != "password" || r.Form.Get("client_id") != "admin-cli" || r.Form.Get("username") != "kcadmin" || r.Form.Get("password") != "adm-pw-1" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		f.tokens++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":60}`, f.tokens)
		return
	}
	if realm, ok := strings.CutPrefix(r.URL.Path, "/realms/"); ok {
		name, rest, _ := strings.Cut(realm, "/")
		switch rest {
		case "protocol/openid-connect/token":
			r.ParseForm()
			if _, exists := f.realmReps[name]; !exists || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "portico-admin" || r.Form.Get("client_secret") != f.adminSecret {
				w.WriteHeader(401)
				return
			}
			f.tokens++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":60}`, f.tokens)
			return
		case "protocol/saml/descriptor":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0"?><EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="%s"><IDPSSODescriptor/></EntityDescriptor>`, f.descriptorEntityID)
			return
		}
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
		w.WriteHeader(401)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/admin/realms" && r.Method == "POST" {
		name := body["realm"].(string)
		f.realmReps[name] = body
		f.clients["cli-realm-management"] = map[string]any{"id": "cli-realm-management", "clientId": "realm-management"}
		w.WriteHeader(201)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/realms/")
	realm, p, _ := strings.Cut(rest, "/")
	if _, ok := f.realmReps[realm]; !ok {
		w.WriteHeader(404)
		return
	}
	parts := strings.Split(p, "/")
	switch {
	case p == "" && r.Method == "GET":
		json.NewEncoder(w).Encode(f.realmReps[realm])
	case p == "clients" && r.Method == "GET":
		out := []map[string]any{}
		for _, c := range f.clients {
			if c["clientId"] == r.URL.Query().Get("clientId") {
				out = append(out, c)
			}
		}
		json.NewEncoder(w).Encode(out)
	case p == "clients" && r.Method == "POST":
		id := clientUUID(body["clientId"].(string))
		body["id"] = id
		f.clients[id] = body
		w.Header().Set("Location", f.srv.URL+r.URL.Path+"/"+id)
		w.WriteHeader(201)
	case len(parts) == 2 && parts[0] == "clients" && r.Method == "PUT":
		body["id"] = parts[1]
		f.clients[parts[1]] = body
		w.WriteHeader(204)
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "client-secret":
		json.NewEncoder(w).Encode(map[string]any{"value": "secret-of-" + strings.TrimPrefix(parts[1], "cli-")})
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "service-account-user":
		json.NewEncoder(w).Encode(map[string]any{"id": "sa-" + strings.TrimPrefix(parts[1], "cli-")})
	case len(parts) == 4 && parts[0] == "clients" && parts[2] == "roles":
		json.NewEncoder(w).Encode(map[string]any{"id": "role-" + parts[3], "name": parts[3]})
	case len(parts) == 4 && parts[0] == "clients" && parts[2] == "protocol-mappers":
		if r.Method == "POST" {
			f.mappers[parts[1]] = append(f.mappers[parts[1]], body)
			w.WriteHeader(201)
			return
		}
		out := f.mappers[parts[1]]
		if out == nil {
			out = []map[string]any{}
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

func TestKeycloakBootstrapCreatesRealmAndClients(t *testing.T) {
	kc := newFakeKCAdmin(t)
	b := NewBootstrapper(nil)
	s := Defaults()
	s.PrismHost, s.IDHost = "prism.example.test", "id.example.test"
	res, out, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if err != nil || !res.OK || out.Keycloak.Realm != "msp" || out.OIDC.Issuer != "https://id.example.test/realms/msp" || out.Keycloak.AdminClientSecret == "" || out.OIDC.ClientSecret == "" {
		t.Fatalf("res=%+v out=%+v err=%v", res, out, err)
	}
	if out.Keycloak.AdminClientID != "portico-admin" || out.OIDC.ClientID != "portico" || out.Keycloak.AdminClientSecret != "secret-of-portico-admin" || out.OIDC.ClientSecret != "secret-of-portico" {
		t.Fatalf("out = %+v", out)
	}
	if got := kc.client("portico"); got["frontchannelLogout"] != false || got["redirectUris"].([]any)[0] != "https://prism.example.test/.portico/callback" || got["attributes"].(map[string]any)["pkce.code.challenge.method"] != "S256" {
		t.Fatalf("oidc client = %v", got)
	}
	if got := kc.client("portico-admin"); got["serviceAccountsEnabled"] != true || got["standardFlowEnabled"] != false {
		t.Fatalf("admin client = %v", got)
	}
	if roles := kc.mappedRoles("portico-admin"); len(roles) != 8 || !slices.Contains(roles, "manage-clients") {
		t.Fatalf("roles = %v", roles)
	}
	if res.Created[0] != "realm" || len(res.Created) != 3 {
		t.Fatalf("created = %v", res.Created)
	}
	if rep := kc.realmReps["msp"]; rep["loginWithEmailAllowed"] != true || rep["registrationEmailAsUsername"] != true || rep["displayName"] != "msp" {
		t.Fatalf("realm = %v", rep)
	}
	// Second run: nothing created, same secrets, still OK.
	res2, out2, _ := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if !res2.OK || len(res2.Created) != 0 || out2.OIDC.ClientSecret != out.OIDC.ClientSecret {
		t.Fatalf("second run = %+v", res2)
	}
}

func TestKeycloakBootstrapPreconditionsAndRefusal(t *testing.T) {
	kc := newFakeKCAdmin(t)
	b := NewBootstrapper(nil)
	var se *StateError
	if _, _, err := b.Keycloak(context.Background(), kc.srv.URL, Defaults(), KeycloakInput{AdminUsername: "a", AdminPassword: "b", Realm: "msp"}); !errors.As(err, &se) || !strings.Contains(err.Error(), "public names") {
		t.Fatalf("no hosts: %v", err)
	}
	s := Defaults()
	s.PrismHost, s.IDHost = "prism.example.test", "id.example.test"
	if _, _, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "a", AdminPassword: "b", Realm: "Bad Realm"}); !errors.As(err, &se) {
		t.Fatalf("bad realm: %v", err)
	}
	if _, _, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "", AdminPassword: "b", Realm: "msp"}); !errors.As(err, &se) {
		t.Fatalf("no username: %v", err)
	}
	res, _, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "wrong", Realm: "msp"})
	if err != nil || res.OK || res.Message != "Keycloak refused the administrator credentials" || kc.realms() != 0 {
		t.Fatalf("wrong password: %+v %v realms=%d", res, err, kc.realms())
	}
	res, _, err = b.Keycloak(context.Background(), "http://127.0.0.1:1", s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if err != nil || res.OK || !strings.HasPrefix(res.Message, "Keycloak did not answer: ") {
		t.Fatalf("keycloak down: %+v %v", res, err)
	}
}
