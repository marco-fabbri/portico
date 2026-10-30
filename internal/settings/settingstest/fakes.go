// Package settingstest holds httptest doubles of the two systems the
// bootstrap talks to, for the tests of the packages above settings.
package settingstest

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	// FailClientCreate, when set, is the status of every client creation.
	FailClientCreate int
	// FailClientDelete, when set, is the status of every client deletion.
	FailClientDelete int
	tokens           int
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
	case p == "clients" && r.Method == "POST" && f.FailClientCreate != 0:
		w.WriteHeader(f.FailClientCreate)
	case p == "clients" && r.Method == "POST":
		id := "cli-" + strings.NewReplacer("/", "_", ":", "_").Replace(body["clientId"].(string))
		body["id"] = id
		f.Clients[id] = body
		w.Header().Set("Location", f.Srv.URL+r.URL.Path+"/"+id)
		w.WriteHeader(201)
	case len(parts) == 2 && parts[0] == "clients" && r.Method == "DELETE" && f.FailClientDelete != 0:
		w.WriteHeader(f.FailClientDelete)
	case len(parts) == 2 && parts[0] == "clients" && r.Method == "DELETE":
		if _, ok := f.Clients[parts[1]]; !ok {
			w.WriteHeader(404)
			return
		}
		delete(f.Clients, parts[1])
		w.WriteHeader(204)
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
// SAML identity providers; the admin's policies, role and operations.
type PC struct {
	Srv           *httptest.Server
	Password, Key string
	// Version, when set, is Prism Central's own entry in the clusters list.
	Version string
	// AdminOps are the operations the admin's role grants (OpCatalog when
	// nil); RolesForbidden refuses the read of the admin's role with 403.
	AdminOps       []string
	RolesForbidden bool
	// Containers replaces the storage containers list when set.
	Containers []map[string]any
	// Credentialed counts the requests that carried Basic credentials.
	Credentialed int
	mu           sync.Mutex
	User         map[string]any
	IdPs         []map[string]any
	Posts        []string
	// DeletedKeys are the API keys revoked, by extId.
	DeletedKeys []string
	// SAMLUsers are the users Prism Central made at sign-in through an
	// identity provider; while one names a provider, deleting it is
	// refused as on pc.7.6 (IAM-21016, write-up 15). UserDeleteForbidden
	// refuses their deletion with 403.
	SAMLUsers           []map[string]any
	UserDeleteForbidden bool
	// RefuseUserDelete refuses the deletion of this one user (an extId).
	RefuseUserDelete string
	// Roles are the roles listed by name and read by extId: Project Admin
	// (role-pa) at start, then the ones created. RoleCreateStatus and
	// RoleUpdateStatus, when set, refuse a creation or an update with that
	// status; RoleUpdates counts the accepted updates.
	Roles                              []map[string]any
	RoleCreateStatus, RoleUpdateStatus int
	RoleUpdates                        int
	// Policies are the authorization policies created.
	Policies []map[string]any
	closed   atomic.Int32
	nextID   int
}

// ProjectAdminOps are the operations of the double's Project Admin role.
// It holds Create_Role_Membership, an operation a custom role cannot hold
// (SYSTEM_DEFINED_ONLY, measured on pc.7.6.0.6 2026-10-30).
var ProjectAdminOps = []string{"op-View_Project", "op-View_Subnet", "op-Create_Floating_Ip", "op-Delete_Floating_Ip", "op-Update_Floating_Ip", "op-Create_Role_Membership"}

// SystemOnlyOps are the operations of type SYSTEM_DEFINED_ONLY: Prism
// Central refuses a custom role holding one with a 400.
var SystemOnlyOps = []string{"Create_Role_Membership", "Delete_Role_Membership"}

// systemOnlyIn names the first SYSTEM_DEFINED_ONLY operation in ops.
func systemOnlyIn(ops any) string {
	list, _ := ops.([]any)
	for _, o := range list {
		for _, n := range SystemOnlyOps {
			if o == "op-"+n {
				return n
			}
		}
	}
	return ""
}

// Role returns the role with that display name, or nil.
func (f *PC) Role(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.Roles {
		if r["displayName"] == name {
			return r
		}
	}
	return nil
}

// Policy returns the created policy with that display name, or nil.
func (f *PC) Policy(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.Policies {
		if p["displayName"] == name {
			return p
		}
	}
	return nil
}

func (f *PC) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

func projectAdmin() []map[string]any {
	ops := make([]any, len(ProjectAdminOps))
	for i, o := range ProjectAdminOps {
		ops[i] = o
	}
	return []map[string]any{{"extId": "role-pa", "displayName": "Project Admin", "operations": ops}}
}

// OpCatalog is the operations the double lists by name.
var OpCatalog = []string{"Create_User", "Create_User_Key", "Update_Authorization_Policy", "Create_Saml_Identity_Provider", "Create_Role", "Update_Role", "Delete_Role",
	"Create_Authorization_Policy", "Delete_Authorization_Policy", "Share_Saml_Identity_Provider", "View_Project",
	"View_Subnet", "Create_Subnet", "Delete_Subnet", "Share_Subnet", "Unshare_Subnet", "View_VPC", "View_Floating_Ip", "Create_Floating_Ip", "Update_Floating_Ip", "Delete_Floating_Ip",
	"View_VPC_Route_Table", "View_Route_Table_Route", "Create_Route_Table_Route", "Delete_Route_Table_Route"}

// NewPC starts a Prism Central double over http.
func NewPC(t *testing.T) *PC {
	f := &PC{Password: "pc-pw-1", Key: "issued-key-4242", Roles: projectAdmin()}
	f.Srv = httptest.NewUnstartedServer(http.HandlerFunc(f.handle))
	f.Srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateClosed {
			f.closed.Add(1)
		}
	}
	f.Srv.Start()
	t.Cleanup(f.Srv.Close)
	return f
}

// ClosedConns counts the client connections the double saw closed.
func (f *PC) ClosedConns() int { return int(f.closed.Load()) }

// NewTLSPC starts a Prism Central double over https, with httptest's
// certificate (trusted by no root).
func NewTLSPC(t *testing.T) *PC {
	f := &PC{Password: "pc-pw-1", Key: "issued-key-4242", Roles: projectAdmin()}
	f.Srv = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Srv.Close)
	return f
}

// Created reports whether a POST to a path ending in suffix was made.
func (f *PC) Created(suffix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.Posts {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

// AddSAMLUser adds a user made at sign-in through the identity provider.
func (f *PC) AddSAMLUser(extID, username, idpExtID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SAMLUsers = append(f.SAMLUsers, map[string]any{"extId": extID, "username": username, "userType": "SAML", "idpId": idpExtID})
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
	if basic {
		f.Credentialed++
	}
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
		idp := map[string]any{"extId": "idp-" + body["name"].(string), "name": body["name"], "redirectURL": body["redirectURL"], "entityIssuer": body["entityIssuer"]}
		f.IdPs = append(f.IdPs, idp)
		reply(201, idp)
	case strings.Contains(p, "/authn/saml-identity-providers/") && !strings.HasSuffix(p, "/sp-metadata") && r.Method == "GET":
		id := p[strings.LastIndex(p, "/")+1:]
		for _, idp := range f.IdPs {
			if idp["extId"] == id {
				w.Header().Set("ETag", `"i"`)
				reply(200, idp)
				return
			}
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"data":{"error":"no such identity provider"}}`))
	case strings.Contains(p, "/authn/saml-identity-providers/") && r.Method == "DELETE":
		id := p[strings.LastIndex(p, "/")+1:]
		for _, u := range f.SAMLUsers {
			if u["idpId"] == id {
				w.WriteHeader(400)
				w.Write([]byte(`{"data":{"error":{"message":"Failed to delete SamlIdentityProvider as users or user groups associated with the directory service are still present or in use."}}}`))
				return
			}
		}
		for i, idp := range f.IdPs {
			if idp["extId"] == id && r.Header.Get("If-Match") == `"i"` {
				f.IdPs = append(f.IdPs[:i:i], f.IdPs[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"data":{"error":"no such identity provider"}}`))
	case strings.HasSuffix(p, "/sp-metadata"):
		entity := "https://prism.example.test/api/iam/authn"
		for _, idp := range f.IdPs {
			if s, _ := idp["entityIssuer"].(string); s != "" && strings.Contains(p, "/"+idp["extId"].(string)+"/") {
				entity = s
			}
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="%s"><md:SPSSODescriptor><md:AssertionConsumerService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://prism.example.test/api/iam/authn/callback" index="1"/></md:SPSSODescriptor></md:EntityDescriptor>`, entity)
	case strings.HasSuffix(p, "/config/clusters"):
		out := []map[string]any{{"extId": "cl-1", "name": "cluster-1", "config": map[string]any{"clusterFunction": []string{"AOS"}}}}
		if f.Version != "" {
			out = append(out, map[string]any{"extId": "pc-1", "name": "pc", "config": map[string]any{"clusterFunction": []string{"PRISM_CENTRAL"}, "buildInfo": map[string]any{"version": f.Version}}})
		}
		reply(200, out)
	case strings.HasSuffix(p, "/config/storage-containers"):
		out := f.Containers
		if out == nil {
			out = []map[string]any{{"containerExtId": "sc-1", "name": "default", "clusterExtId": "cl-1"}}
		}
		reply(200, out)
	case strings.HasSuffix(p, "/authn/users") && r.Method == "GET":
		out := []map[string]any{{"extId": "u-admin", "username": "admin", "userType": "LOCAL"}}
		if f.User != nil {
			out = append(out, f.User)
		}
		if strings.Contains(r.URL.Query().Get("$filter"), "SAML") {
			out = f.SAMLUsers
		}
		reply(200, out)
	case strings.Contains(p, "/authn/users/su-") && (r.Method == "GET" || r.Method == "DELETE"):
		id := p[strings.LastIndex(p, "/")+1:]
		for i, u := range f.SAMLUsers {
			if u["extId"] != id {
				continue
			}
			switch {
			case r.Method == "GET":
				w.Header().Set("ETag", `"u"`)
				reply(200, u)
			case f.UserDeleteForbidden || f.RefuseUserDelete == id:
				// pc.7.6's error names the user it refused.
				w.WriteHeader(403)
				fmt.Fprintf(w, `{"data":{"error":[{"code":"IAM-30403","message":"user %s cannot be deleted"}]}}`, u["username"])
			default:
				f.SAMLUsers = append(f.SAMLUsers[:i:i], f.SAMLUsers[i+1:]...)
				w.WriteHeader(204)
			}
			return
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"data":{"error":"no such user"}}`))
	case strings.HasSuffix(p, "/authn/users") && r.Method == "POST":
		f.User = map[string]any{"extId": "sa-1", "username": body["username"], "userType": "SERVICE_ACCOUNT"}
		reply(201, f.User)
	case strings.Contains(p, "/authn/users/sa-1/keys/") && (r.Method == "GET" || r.Method == "DELETE"):
		id := p[strings.LastIndex(p, "/")+1:]
		if f.User == nil || id != "key-1" || slices.Contains(f.DeletedKeys, id) {
			w.WriteHeader(404)
			w.Write([]byte(`{"data":{"error":"no such key"}}`))
			return
		}
		if r.Method == "GET" {
			w.Header().Set("ETag", `"k"`)
			reply(200, map[string]any{"extId": id})
			return
		}
		f.DeletedKeys = append(f.DeletedKeys, id)
		w.WriteHeader(204)
	case strings.HasSuffix(p, "/authn/users/sa-1/keys") && r.Method == "POST":
		reply(201, map[string]any{"extId": "key-1", "name": body["name"], "keyDetails": map[string]any{"apiKey": f.Key}, "expiryTime": "2126-10-18T00:00:00Z"})
	case strings.HasSuffix(p, "/authz/authorization-policies") && r.Method == "POST":
		pol := body
		pol["extId"] = f.id("pol")
		f.Policies = append(f.Policies, pol)
		reply(201, pol)
	case strings.HasSuffix(p, "/authz/authorization-policies"):
		named := func(u string) []any {
			return []any{map[string]any{"identityFilter": map[string]any{"user": map[string]any{"uuid": map[string]any{"anyof": []any{u}}}}}}
		}
		out := []map[string]any{{"extId": "pol-pm", "displayName": "Project Manager_acp", "role": "role-pm", "identities": named("u-existing")},
			{"extId": "pol-sa", "displayName": "Super Admin_acp", "role": "role-sa", "identities": named("u-admin")}}
		reply(200, append(out, f.Policies...))
	case strings.HasSuffix(p, "/authz/roles/role-sa") && f.RolesForbidden:
		w.WriteHeader(403)
		w.Write([]byte(`{"data":{"error":"forbidden"}}`))
	case strings.HasSuffix(p, "/authz/roles/role-sa"):
		ops := []string{}
		names := f.AdminOps
		if names == nil {
			names = OpCatalog
		}
		for _, n := range names {
			ops = append(ops, "op-"+n)
		}
		reply(200, map[string]any{"extId": "role-sa", "displayName": "Super Admin", "operations": ops})
	case strings.HasSuffix(p, "/authz/operations") && strings.Contains(r.URL.Query().Get("$filter"), "SYSTEM_DEFINED_ONLY"):
		out := []map[string]any{}
		for _, n := range SystemOnlyOps {
			out = append(out, map[string]any{"extId": "op-" + n, "displayName": n, "operationType": "SYSTEM_DEFINED_ONLY"})
		}
		reply(200, out)
	case strings.HasSuffix(p, "/authz/operations"):
		out := []map[string]any{}
		for _, n := range OpCatalog {
			if strings.Contains(r.URL.Query().Get("$filter"), "'"+n+"'") {
				out = append(out, map[string]any{"extId": "op-" + n, "displayName": n})
			}
		}
		reply(200, out)
	case strings.HasSuffix(p, "/authz/authorization-policies/pol-pm") && r.Method == "GET":
		w.Header().Set("ETag", `"p"`)
		reply(200, map[string]any{"extId": "pol-pm", "displayName": "Project Manager_acp", "role": "role-pm",
			"identities": []any{map[string]any{"identityFilter": map[string]any{"user": map[string]any{"uuid": map[string]any{"anyof": []any{"u-existing"}}}}}}})
	case strings.HasSuffix(p, "/authz/authorization-policies/pol-pm") && r.Method == "PUT":
		reply(200, map[string]any{"message": "updated"})
	case strings.Contains(p, "/authz/authorization-policies/pol-") && (r.Method == "GET" || r.Method == "DELETE"):
		id := p[strings.LastIndex(p, "/")+1:]
		for i, pol := range f.Policies {
			if pol["extId"] != id {
				continue
			}
			if r.Method == "GET" {
				w.Header().Set("ETag", `"pol"`)
				reply(200, pol)
			} else {
				f.Policies = append(f.Policies[:i:i], f.Policies[i+1:]...)
				w.WriteHeader(204)
			}
			return
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"data":{"error":"no such policy"}}`))
	case strings.HasSuffix(p, "/authz/roles") && r.Method == "GET":
		out := []map[string]any{}
		for _, role := range f.Roles {
			if strings.Contains(r.URL.Query().Get("$filter"), "'"+role["displayName"].(string)+"'") {
				out = append(out, map[string]any{"extId": role["extId"], "displayName": role["displayName"]})
			}
		}
		reply(200, out)
	case strings.HasSuffix(p, "/authz/roles") && r.Method == "POST":
		if f.RoleCreateStatus != 0 {
			w.WriteHeader(f.RoleCreateStatus)
			w.Write([]byte(`{"data":{"error":[{"message":"role creation refused"}]}}`))
			return
		}
		if systemOnlyIn(body["operations"]) != "" {
			w.WriteHeader(400)
			w.Write([]byte(`{"data":{"error":[{"message":"Failed to create role as invalid operations are provided in the request."}]}}`))
			return
		}
		role := body
		role["extId"] = f.id("role")
		f.Roles = append(f.Roles, role)
		reply(201, role)
	case strings.Contains(p, "/authz/roles/"):
		id := p[strings.LastIndex(p, "/")+1:]
		for i, role := range f.Roles {
			if role["extId"] != id {
				continue
			}
			switch {
			case r.Method == "GET":
				w.Header().Set("ETag", `"role"`)
				reply(200, role)
			case r.Method == "PUT" && f.RoleUpdateStatus != 0:
				w.WriteHeader(f.RoleUpdateStatus)
				w.Write([]byte(`{"data":{"error":[{"message":"role update refused"}]}}`))
			case r.Method == "PUT" && systemOnlyIn(body["operations"]) != "":
				w.WriteHeader(400)
				w.Write([]byte(`{"data":{"error":[{"message":"Failed to update role as invalid operations are provided in the request."}]}}`))
			case r.Method == "PUT":
				f.RoleUpdates++
				role["operations"] = body["operations"]
				reply(200, map[string]any{"message": "updated"})
			case r.Method == "DELETE":
				f.Roles = append(f.Roles[:i:i], f.Roles[i+1:]...)
				w.WriteHeader(204)
			}
			return
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"data":{"error":"no such role"}}`))
	default:
		w.WriteHeader(404)
		w.Write([]byte(`{"data":{"error":"no route"}}`))
	}
}
