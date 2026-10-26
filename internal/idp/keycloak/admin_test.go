package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

const testAdminPassword = "kc-admin-pw-456"

const testDescriptor = `<?xml version="1.0" encoding="UTF-8"?><EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://id.example.test/realms/msp"><IDPSSODescriptor/></EntityDescriptor>`

// adminState is the realm-administration side of the fake: realms
// created at the root, clients with their secrets, service-account
// users, client-role mappings and protocol mappers.
type adminState struct {
	masterTokenReqs int
	masterForm      url.Values
	realms          map[string]map[string]any
	clients         map[string]map[string]any // uuid → representation
	mapped          map[string][]string       // userID/clientUUID → role names
	mappers         map[string][]map[string]any
	roles           []string // roles of the realm-management client
}

func (f *fakeKC) admin() *adminState {
	if f.adm == nil {
		f.adm = &adminState{realms: map[string]map[string]any{}, clients: map[string]map[string]any{}, mapped: map[string][]string{}, mappers: map[string][]map[string]any{},
			roles: []string{"manage-users", "view-users", "query-users", "query-groups", "view-realm", "manage-realm", "manage-clients", "view-clients", "impersonation"}}
		f.adm.clients["cli-realm-management"] = map[string]any{"id": "cli-realm-management", "clientId": "realm-management"}
	}
	return f.adm
}

// handleUnauthenticated serves the two endpoints that need no bearer:
// the master-realm token (password grant) and the SAML descriptor.
func (f *fakeKC) handleUnauthenticated(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/realms/master/protocol/openid-connect/token":
		r.ParseForm()
		f.mu.Lock()
		a := f.admin()
		a.masterTokenReqs++
		a.masterForm = r.Form
		n := a.masterTokenReqs
		f.mu.Unlock()
		if r.Form.Get("grant_type") != "password" || r.Form.Get("client_id") != "admin-cli" || r.Form.Get("username") != "kcadmin" || r.Form.Get("password") != testAdminPassword {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid user credentials ` + r.Form.Get("password") + `"}`))
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"access_token":"tok-m%d","expires_in":60,"token_type":"Bearer"}`, n)))
		return true
	case "/realms/msp/protocol/saml/descriptor":
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(400)
			return true
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testDescriptor))
		return true
	}
	return false
}

// handleAdmin serves the realm root and the client-management calls.
func (f *fakeKC) handleAdmin(w http.ResponseWriter, r *http.Request, rec recorded) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.admin()
	if r.URL.Path == "/admin/realms" && r.Method == "POST" {
		name, _ := rec.Body["realm"].(string)
		a.realms[name] = rec.Body
		w.Header().Set("Location", f.srv.URL+"/admin/realms/"+name)
		w.WriteHeader(201)
		return true
	}
	// A realm created at the root answers on its own path.
	if rest, ok := strings.CutPrefix(r.URL.Path, "/admin/realms/"); ok {
		name, p, _ := strings.Cut(rest, "/")
		if name != "msp" {
			rep, exists := a.realms[name]
			if !exists {
				w.WriteHeader(404)
				return true
			}
			if p == "" && r.Method == "GET" {
				json.NewEncoder(w).Encode(rep)
				return true
			}
		} else if p == "" && r.Method == "GET" && f.realm == nil {
			if rep, exists := a.realms["msp"]; exists {
				json.NewEncoder(w).Encode(rep)
				return true
			}
		}
	}
	p := strings.TrimPrefix(r.URL.Path, "/admin/realms/msp")
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	switch {
	case p == "/clients" && r.Method == "GET":
		out := []map[string]any{}
		for _, c := range a.clients {
			if c["clientId"] == r.URL.Query().Get("clientId") {
				out = append(out, c)
			}
		}
		json.NewEncoder(w).Encode(out)
	case p == "/clients" && r.Method == "POST":
		id := "cli-" + rec.Body["clientId"].(string)
		rep := rec.Body
		rep["id"] = id
		a.clients[id] = rep
		w.Header().Set("Location", f.srv.URL+"/admin/realms/msp/clients/"+id)
		w.WriteHeader(201)
	case len(parts) == 2 && parts[0] == "clients" && r.Method == "PUT":
		if _, ok := a.clients[parts[1]]; !ok {
			w.WriteHeader(404)
			return true
		}
		rep := rec.Body
		rep["id"] = parts[1]
		a.clients[parts[1]] = rep
		w.WriteHeader(204)
	case len(parts) == 2 && parts[0] == "clients" && r.Method == "DELETE":
		if _, ok := a.clients[parts[1]]; !ok {
			w.WriteHeader(404)
			return true
		}
		delete(a.clients, parts[1])
		w.WriteHeader(204)
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "client-secret":
		json.NewEncoder(w).Encode(map[string]any{"type": "secret", "value": "sec-" + strings.TrimPrefix(parts[1], "cli-")})
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "service-account-user":
		json.NewEncoder(w).Encode(map[string]any{"id": "sa-" + strings.TrimPrefix(parts[1], "cli-")})
	case len(parts) == 4 && parts[0] == "clients" && parts[2] == "roles":
		if parts[1] != "cli-realm-management" || !slices.Contains(a.roles, parts[3]) {
			w.WriteHeader(404)
			return true
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "role-" + parts[3], "name": parts[3], "clientRole": true})
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "protocol-mappers":
		w.WriteHeader(404)
	case len(parts) == 4 && parts[0] == "clients" && parts[2] == "protocol-mappers" && parts[3] == "models":
		if r.Method == "POST" {
			a.mappers[parts[1]] = append(a.mappers[parts[1]], rec.Body)
			w.WriteHeader(201)
			return true
		}
		out := a.mappers[parts[1]]
		if out == nil {
			out = []map[string]any{}
		}
		json.NewEncoder(w).Encode(out)
	case len(parts) == 5 && parts[0] == "users" && parts[2] == "role-mappings" && parts[3] == "clients":
		key := parts[1] + "/" + parts[4]
		if r.Method == "POST" {
			var reps []map[string]any
			json.Unmarshal(rec.Raw, &reps)
			for _, rep := range reps {
				a.mapped[key] = append(a.mapped[key], rep["name"].(string))
			}
			w.WriteHeader(204)
			return true
		}
		out := []map[string]any{}
		for _, n := range a.mapped[key] {
			out = append(out, map[string]any{"id": "role-" + n, "name": n})
		}
		json.NewEncoder(w).Encode(out)
	default:
		return false
	}
	return true
}

func adminClient(f *fakeKC, buf *bytes.Buffer) *Client {
	var log *slog.Logger
	if buf != nil {
		log = slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return NewWithPassword(f.srv.URL, "msp", "kcadmin", testAdminPassword, nil, log)
}

func TestPasswordGrantUsesMasterRealmAndAdminCli(t *testing.T) {
	f := newFakeKC(t)
	f.groups = []map[string]any{{"id": "grp-a", "name": "tenant-a"}}
	var buf bytes.Buffer
	c := adminClient(f, &buf)
	if _, err := c.FindGroup(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("FindGroup with the password grant: %v", err)
	}
	a := f.admin()
	if a.masterTokenReqs != 1 || f.tokenReqs != 0 || a.masterForm.Get("grant_type") != "password" || a.masterForm.Get("client_id") != "admin-cli" || a.masterForm.Get("username") != "kcadmin" {
		t.Fatalf("master token form = %v (master %d, realm %d)", a.masterForm, a.masterTokenReqs, f.tokenReqs)
	}
	wrong := NewWithPassword(f.srv.URL, "msp", "kcadmin", "nope-pw", nil, slog.New(slog.NewJSONHandler(&buf, nil)))
	_, err := wrong.FindGroup(context.Background(), "tenant-a")
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("wrong password: %v", err)
	}
	if out := buf.String() + err.Error(); strings.Contains(out, testAdminPassword) || strings.Contains(out, "nope-pw") || strings.Contains(out, "tok-m") {
		t.Fatalf("the password or a token reached the log or the error: %s", out)
	}
}

func TestCreateRealmPostsToAdminRoot(t *testing.T) {
	f := newFakeKC(t)
	c := adminClient(f, nil)
	ctx := context.Background()
	if _, err := c.Realm(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Realm before creation = %v, want ErrNotFound", err)
	}
	rep := map[string]any{"realm": "msp", "enabled": true, "displayName": "MSP", "loginWithEmailAllowed": true}
	if err := c.CreateRealm(ctx, rep); err != nil {
		t.Fatalf("CreateRealm: %v", err)
	}
	var post *recorded
	for _, r := range f.requests() {
		if r.Method == "POST" && r.Path == "/admin/realms" {
			rr := r
			post = &rr
		}
	}
	if post == nil || post.Body["realm"] != "msp" || post.Body["loginWithEmailAllowed"] != true {
		t.Fatalf("POST /admin/realms = %+v", post)
	}
	got, err := c.Realm(ctx)
	if err != nil || got["displayName"] != "MSP" {
		t.Fatalf("Realm after creation = %v, %v", got, err)
	}
}

func TestEnsureClientCreatesThenUpdates(t *testing.T) {
	f := newFakeKC(t)
	c := adminClient(f, nil)
	ctx := context.Background()
	rep := map[string]any{"clientId": "portico", "enabled": true, "name": "Portico"}
	id, created, err := c.EnsureClient(ctx, rep)
	if err != nil || !created || id != "cli-portico" {
		t.Fatalf("first EnsureClient = %q %v %v", id, created, err)
	}
	rep["name"] = "Portico v2"
	id2, created2, err := c.EnsureClient(ctx, rep)
	if err != nil || created2 || id2 != id {
		t.Fatalf("second EnsureClient = %q %v %v", id2, created2, err)
	}
	var put *recorded
	for _, r := range f.requests() {
		if r.Method == "PUT" && r.Path == "/admin/realms/msp/clients/cli-portico" {
			rr := r
			put = &rr
		}
	}
	if put == nil || put.Body["name"] != "Portico v2" {
		t.Fatalf("PUT on the client = %+v", put)
	}
	secret, err := c.ClientSecret(ctx, id)
	if err != nil || secret != "sec-portico" {
		t.Fatalf("ClientSecret = %q %v", secret, err)
	}
	uid, err := c.ServiceAccountUserID(ctx, id)
	if err != nil || uid != "sa-portico" {
		t.Fatalf("ServiceAccountUserID = %q %v", uid, err)
	}
}

func TestDeleteClient(t *testing.T) {
	f := newFakeKC(t)
	c := adminClient(f, nil)
	ctx := context.Background()
	if _, _, err := c.EnsureClient(ctx, map[string]any{"clientId": "pc-sp"}); err != nil {
		t.Fatalf("EnsureClient: %v", err)
	}
	if err := c.DeleteClient(ctx, "pc-sp"); err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}
	if _, err := c.FindClient(ctx, "pc-sp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("client after delete: %v", err)
	}
	if err := c.DeleteClient(ctx, "absent-sp"); err != nil {
		t.Fatalf("DeleteClient of an absent client: %v", err)
	}
	dels := 0
	for _, r := range f.requests() {
		if r.Method == "DELETE" {
			dels++
			if r.Path != "/admin/realms/msp/clients/cli-pc-sp" {
				t.Errorf("DELETE %s", r.Path)
			}
		}
	}
	if dels != 1 {
		t.Fatalf("%d DELETE requests, want 1", dels)
	}
}

func TestEnsureClientRolesAddsOnlyMissing(t *testing.T) {
	f := newFakeKC(t)
	a := f.admin()
	a.mapped["sa-portico-admin/cli-realm-management"] = []string{"view-users", "query-users"}
	c := adminClient(f, nil)
	added, err := c.EnsureClientRoles(context.Background(), "sa-portico-admin", "realm-management", []string{"manage-users", "view-users", "query-users", "query-groups", "view-realm"})
	if err != nil {
		t.Fatalf("EnsureClientRoles: %v", err)
	}
	if !slices.Equal(added, []string{"manage-users", "query-groups", "view-realm"}) {
		t.Fatalf("added = %v", added)
	}
	var post *recorded
	for _, r := range f.requests() {
		if r.Method == "POST" && r.Path == "/admin/realms/msp/users/sa-portico-admin/role-mappings/clients/cli-realm-management" {
			rr := r
			post = &rr
		}
	}
	var reps []map[string]any
	if post == nil || json.Unmarshal(post.Raw, &reps) != nil || len(reps) != 3 || reps[0]["id"] != "role-manage-users" {
		t.Fatalf("POST role mappings = %+v", post)
	}
	if got := a.mapped["sa-portico-admin/cli-realm-management"]; len(got) != 5 {
		t.Fatalf("mapped after = %v", got)
	}
	if _, err := c.EnsureClientRoles(context.Background(), "sa-portico-admin", "realm-management", []string{"no-such-role"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown role = %v, want ErrNotFound", err)
	}
}

func TestEnsureProtocolMappersByName(t *testing.T) {
	f := newFakeKC(t)
	a := f.admin()
	a.mappers["cli-saml"] = []map[string]any{{"name": "email", "protocol": "saml"}}
	c := adminClient(f, nil)
	added, err := c.EnsureProtocolMappers(context.Background(), "cli-saml", []map[string]any{{"name": "email", "protocol": "saml"}, {"name": "groups", "protocol": "saml", "protocolMapper": "saml-group-membership-mapper"}})
	if err != nil || !slices.Equal(added, []string{"groups"}) {
		t.Fatalf("EnsureProtocolMappers = %v, %v", added, err)
	}
	if got := a.mappers["cli-saml"]; len(got) != 2 || got[1]["protocolMapper"] != "saml-group-membership-mapper" {
		t.Fatalf("mappers after = %v", got)
	}
}

func TestSAMLDescriptorNeedsNoToken(t *testing.T) {
	f := newFakeKC(t)
	c := adminClient(f, nil)
	xml, err := c.SAMLDescriptor(context.Background())
	if err != nil || !strings.Contains(string(xml), `entityID="https://id.example.test/realms/msp"`) {
		t.Fatalf("SAMLDescriptor = %q, %v", xml, err)
	}
	if f.admin().masterTokenReqs != 0 {
		t.Fatal("the descriptor fetch requested a token")
	}
}

// The temporary administrator of the first boot is deleted from the
// master realm once Portico has its own clients; an absent one is fine.
func TestDeleteMasterUser(t *testing.T) {
	var deleted []string
	users := map[string]string{"kcadmin": "u-1"}
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/master/protocol/openid-connect/token":
			w.Write([]byte(`{"access_token":"t","expires_in":60}`))
		case r.URL.Path == "/admin/realms/master/users" && r.Method == "GET":
			if r.URL.Query().Get("username") == "" || r.URL.Query().Get("exact") != "true" {
				w.WriteHeader(400)
				return
			}
			out := []map[string]string{}
			if id, ok := users[r.URL.Query().Get("username")]; ok {
				out = append(out, map[string]string{"id": id, "username": r.URL.Query().Get("username")})
			}
			json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(r.URL.Path, "/admin/realms/master/users/") && r.Method == "DELETE":
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/admin/realms/master/users/"))
			delete(users, "kcadmin")
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	})
	c := NewWithPassword(srv.URL, "portico", "kcadmin", "pw", nil, slog.New(slog.DiscardHandler))
	if err := c.DeleteMasterUser(context.Background(), "kcadmin"); err != nil || len(deleted) != 1 || deleted[0] != "u-1" {
		t.Fatalf("err %v deleted %v", err, deleted)
	}
	if err := c.DeleteMasterUser(context.Background(), "kcadmin"); err != nil || len(deleted) != 1 {
		t.Fatalf("second delete: err %v deleted %v", err, deleted)
	}
}

func httptestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}
